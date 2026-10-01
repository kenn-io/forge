package telemetry

import (
	"runtime"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/posthog/posthog-go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/forge/internal/testutil/dbtest"
)

type fakePostHogClient struct {
	mu       sync.Mutex
	messages []posthog.Message
	closed   bool
}

func (f *fakePostHogClient) Enqueue(message posthog.Message) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.messages = append(f.messages, message)
	return nil
}

func (f *fakePostHogClient) Close() error {
	f.closed = true
	return nil
}

func (f *fakePostHogClient) captureTimes(t *testing.T) []time.Time {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	times := make([]time.Time, 0, len(f.messages))
	for _, message := range f.messages {
		capture, ok := message.(posthog.Capture)
		require.True(t, ok)
		times = append(times, capture.Timestamp)
	}
	return times
}

type fakeClock struct{ now time.Time }

func (c *fakeClock) Now() time.Time { return c.now }

func TestNewReporterDisabledByEnvDoesNotCreateInstallID(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)

	t.Setenv(EnabledEnv, "0")
	database := dbtest.Open(t)

	reporter, err := NewReporter(Options{Database: database})
	require.NoError(err)

	assert.False(reporter.Enabled())
	_, found, err := database.AppMetadataValue(t.Context(), installIDMetadataKey)
	require.NoError(err)
	assert.False(found)
}

func TestNewReporterDisabledInGoTestEvenWhenEnvEnabled(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)

	t.Setenv(EnabledEnv, "1")
	database := dbtest.Open(t)

	reporter, err := NewReporter(Options{Database: database})
	require.NoError(err)

	assert.False(reporter.Enabled())
	_, found, err := database.AppMetadataValue(t.Context(), installIDMetadataKey)
	require.NoError(err)
	assert.False(found)
}

func TestLoadOrCreateInstallIDIsStableAndAnonymous(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)

	database := dbtest.Open(t)

	first, _, err := loadOrCreateInstallID(t.Context(), database, time.Now())
	require.NoError(err)
	second, _, err := loadOrCreateInstallID(t.Context(), database, time.Now())
	require.NoError(err)

	assert.Len(first, 32)
	assert.Equal(first, second)

	stored, found, err := database.AppMetadataValue(t.Context(), installIDMetadataKey)
	require.NoError(err)
	assert.True(found)
	assert.Equal(first, stored)
}

func TestReporterCaptureUsesAnonymousDistinctID(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)

	client := &fakePostHogClient{}
	reporter := &Reporter{
		client:     client,
		distinctID: "anonymous-install-id",
		enabled:    true,
		version:    "1.2.3",
		commit:     "abc123",
	}

	err := reporter.Capture("daemon_active", map[string]any{
		"$geoip_disable":          false,
		"$process_person_profile": true,
		"application":             "caller-app",
		"app":                     "caller-app",
		"distinct_id":             "user-provided",
		"repo":                    "owner/name",
		"repo_count":              7,
		"source":                  "caller",
		"version":                 "caller-version",
	})
	require.NoError(err)

	require.Len(client.messages, 1)
	capture, ok := client.messages[0].(posthog.Capture)
	require.True(ok)
	assert.Equal("anonymous-install-id", capture.DistinctId)
	assert.Equal("daemon_active", capture.Event)
	assert.Equal(7, capture.Properties["repo_count"])
	assert.NotContains(capture.Properties, "distinct_id")
	assert.NotContains(capture.Properties, "repo")
	assert.NotContains(capture.Properties, "app")
	assert.False(capture.Properties["$process_person_profile"].(bool))
	assert.True(capture.Properties["$geoip_disable"].(bool))
	assert.Equal("kenn-forge", capture.Properties["application"])
	assert.Equal("1.2.3", capture.Properties["version"])
	assert.Equal("abc123", capture.Properties["commit"])
	assert.Equal(runtime.GOOS, capture.Properties["goos"])
	assert.Equal(runtime.GOARCH, capture.Properties["goarch"])
	assert.Equal("daemon", capture.Properties["source"])
}

func TestReporterCaptureRejectsUnsupportedEvents(t *testing.T) {
	require := require.New(t)

	client := &fakePostHogClient{}
	reporter := &Reporter{
		client:     client,
		distinctID: "anonymous-install-id",
		enabled:    true,
	}

	err := reporter.Capture("server_started", map[string]any{"repo_count": 7})
	require.ErrorIs(err, ErrUnsupportedEvent)
}

func TestReporterCaptureDropsUnsafePropertyValues(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)

	client := &fakePostHogClient{}
	reporter := &Reporter{
		client:     client,
		distinctID: "anonymous-install-id",
		enabled:    true,
	}

	err := reporter.Capture("app_loaded", map[string]any{"view": "owner/repo"})
	require.NoError(err)

	require.Len(client.messages, 1)
	capture, ok := client.messages[0].(posthog.Capture)
	require.True(ok)
	assert.NotContains(capture.Properties, "view")
	assert.False(capture.Properties["$process_person_profile"].(bool))
	assert.True(capture.Properties["$geoip_disable"].(bool))
	assert.Equal("kenn-forge", capture.Properties["application"])
	assert.Equal("backend", capture.Properties["source"])
}

func TestSanitizePropertiesAddsNonOverridablePrivacyAndApplication(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)

	properties, err := SanitizeProperties("app_loaded", map[string]any{
		"$geoip_disable":          false,
		"$process_person_profile": true,
		"application":             "caller-app",
		"view":                    "pulls",
	})
	require.NoError(err)

	assert.Equal("pulls", properties["view"])
	assert.False(properties["$process_person_profile"].(bool))
	assert.True(properties["$geoip_disable"].(bool))
	assert.Equal("kenn-forge", properties["application"])
}

func TestLoadOrCreateInstallIDRecordsCreationTimeOnce(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)

	database := dbtest.Open(t)
	created := time.Date(2026, 1, 2, 3, 4, 5, 6, time.UTC)

	_, first, err := loadOrCreateInstallID(t.Context(), database, created)
	require.NoError(err)
	_, second, err := loadOrCreateInstallID(t.Context(), database, created.Add(48*time.Hour))
	require.NoError(err)

	assert.True(created.Equal(first))
	assert.True(created.Equal(second))
}

func TestLoadOrCreateInstallIDWithoutCreationTimeCountsAsOld(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)

	database := dbtest.Open(t)
	_, err := database.GetOrCreateAppMetadataValue(t.Context(), installIDMetadataKey, func() (string, error) {
		return "existing-install-id", nil
	})
	require.NoError(err)

	id, installedAt, err := loadOrCreateInstallID(t.Context(), database, time.Now())
	require.NoError(err)

	assert.Equal("existing-install-id", id)
	assert.True(installedAt.IsZero())
	_, found, err := database.AppMetadataValue(t.Context(), installedAtKey)
	require.NoError(err)
	assert.False(found)
}

func newTestReporter(client *fakePostHogClient, clock *fakeClock, installedAt time.Time) *Reporter {
	return &Reporter{
		client:      client,
		distinctID:  "anonymous-install-id",
		enabled:     true,
		installedAt: installedAt,
		now:         clock.Now,
	}
}

func TestReporterHoldsEventsUntilInstallMatures(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)

	installedAt := time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC)
	clock := &fakeClock{now: installedAt.Add(time.Hour)}
	client := &fakePostHogClient{}
	reporter := newTestReporter(client, clock, installedAt)

	require.NoError(reporter.Capture("daemon_active", map[string]any{"repo_count": 1}))
	clock.now = installedAt.Add(23 * time.Hour)
	require.NoError(reporter.Capture("app_loaded", map[string]any{"view": "pulls"}))
	assert.Empty(client.messages)

	clock.now = installedAt.Add(maturityAge)
	require.NoError(reporter.Capture("daemon_active", map[string]any{"repo_count": 2}))

	assert.Equal([]time.Time{
		installedAt.Add(time.Hour),
		installedAt.Add(23 * time.Hour),
		installedAt.Add(maturityAge),
	}, client.captureTimes(t))
}

func TestReporterSendsImmediatelyForInstallWithoutCreationTime(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)

	clock := &fakeClock{now: time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC)}
	client := &fakePostHogClient{}
	reporter := newTestReporter(client, clock, time.Time{})

	require.NoError(reporter.Capture("daemon_active", map[string]any{"repo_count": 1}))

	assert.Equal([]time.Time{clock.now}, client.captureTimes(t))
}

func TestReporterCloseBeforeMaturityDropsHeldEvents(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)

	installedAt := time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC)
	clock := &fakeClock{now: installedAt.Add(time.Minute)}
	client := &fakePostHogClient{}
	reporter := newTestReporter(client, clock, installedAt)

	require.NoError(reporter.Capture("daemon_active", map[string]any{"repo_count": 1}))
	clock.now = installedAt.Add(2 * time.Minute)
	require.NoError(reporter.Close())

	assert.Empty(client.messages)
	assert.True(client.closed)
}

func TestReporterCloseAfterMaturityFlushesHeldEvents(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)

	installedAt := time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC)
	clock := &fakeClock{now: installedAt.Add(time.Hour)}
	client := &fakePostHogClient{}
	reporter := newTestReporter(client, clock, installedAt)

	require.NoError(reporter.Capture("daemon_active", map[string]any{"repo_count": 1}))
	clock.now = installedAt.Add(25 * time.Hour)
	require.NoError(reporter.Close())

	assert.Equal([]time.Time{installedAt.Add(time.Hour)}, client.captureTimes(t))
	assert.True(client.closed)
}

func TestReporterCapsHeldEvents(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)

	installedAt := time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC)
	clock := &fakeClock{now: installedAt}
	client := &fakePostHogClient{}
	reporter := newTestReporter(client, clock, installedAt)

	for range maxHeldEvents + 5 {
		require.NoError(reporter.Capture("app_loaded", map[string]any{"view": "pulls"}))
	}
	clock.now = installedAt.Add(maturityAge)
	require.NoError(reporter.Capture("app_loaded", map[string]any{"view": "pulls"}))

	assert.Len(client.messages, maxHeldEvents+1)
}

func TestReporterFlushesHeldEventsWhenInstallMaturesWithoutNewCapture(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		assert := assert.New(t)
		require := require.New(t)

		installedAt := time.Now().Add(-23 * time.Hour)
		client := &fakePostHogClient{}
		reporter := &Reporter{client: client, distinctID: "anonymous-install-id", enabled: true, installedAt: installedAt}
		reporter.scheduleMaturityFlush()

		require.NoError(reporter.Capture("daemon_active", map[string]any{"repo_count": 1}))
		captured := time.Now().UTC()
		time.Sleep(time.Hour - time.Second)
		synctest.Wait()
		assert.Empty(client.captureTimes(t))

		time.Sleep(time.Second)
		synctest.Wait()
		assert.Equal([]time.Time{captured}, client.captureTimes(t))
		require.NoError(reporter.Close())
	})
}
