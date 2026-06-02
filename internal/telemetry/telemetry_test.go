package telemetry

import (
	"runtime"
	"testing"

	"github.com/posthog/posthog-go"
	Assert "github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/middleman/internal/testutil/dbtest"
)

type fakePostHogClient struct {
	message posthog.Message
}

func (f *fakePostHogClient) Enqueue(message posthog.Message) error {
	f.message = message
	return nil
}

func (f *fakePostHogClient) Close() error { return nil }

func TestNewReporterDisabledByEnvDoesNotCreateInstallID(t *testing.T) {
	assert := Assert.New(t)
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

func TestLoadOrCreateInstallIDIsStableAndAnonymous(t *testing.T) {
	assert := Assert.New(t)
	require := require.New(t)

	database := dbtest.Open(t)

	first, err := loadOrCreateInstallID(t.Context(), database)
	require.NoError(err)
	second, err := loadOrCreateInstallID(t.Context(), database)
	require.NoError(err)

	assert.Len(first, 32)
	assert.Equal(first, second)

	stored, found, err := database.AppMetadataValue(t.Context(), installIDMetadataKey)
	require.NoError(err)
	assert.True(found)
	assert.Equal(first, stored)
}

func TestReporterCaptureUsesAnonymousDistinctID(t *testing.T) {
	assert := Assert.New(t)
	require := require.New(t)

	client := &fakePostHogClient{}
	reporter := &Reporter{
		client:     client,
		distinctID: "anonymous-install-id",
		version:    "dev",
		commit:     "abc123",
		enabled:    true,
	}

	err := reporter.Capture("app_loaded", map[string]any{
		"$geoip_disable":          false,
		"$process_person_profile": true,
		"application":             "not-middleman",
		"distinct_id":             "user-provided",
		"repo":                    "owner/name",
		"view":                    "pulls",
	})
	require.NoError(err)

	capture, ok := client.message.(posthog.Capture)
	require.True(ok)
	assert.Equal("anonymous-install-id", capture.DistinctId)
	assert.Equal("app_loaded", capture.Event)
	assert.Equal("pulls", capture.Properties["view"])
	assert.NotContains(capture.Properties, "distinct_id")
	assert.NotContains(capture.Properties, "repo")
	assert.Equal("middleman", capture.Properties["application"])
	assert.Equal("dev", capture.Properties["version"])
	assert.Equal("abc123", capture.Properties["commit"])
	assert.Equal("backend", capture.Properties["source"])
	assert.True(capture.Properties["$geoip_disable"].(bool))
	assert.False(capture.Properties["$process_person_profile"].(bool))
}

func TestSanitizePropertiesForcesStandardPrivacyProperties(t *testing.T) {
	assert := Assert.New(t)
	require := require.New(t)

	properties, err := SanitizeProperties("daemon_active", map[string]any{
		"$geoip_disable":          false,
		"$process_person_profile": true,
		"application":             "not-middleman",
		"app":                     "legacy",
		"repo_count":              3,
	})
	require.NoError(err)

	assert.Equal(3, properties["repo_count"])
	assert.Equal("middleman", properties["application"])
	assert.True(properties["$geoip_disable"].(bool))
	assert.False(properties["$process_person_profile"].(bool))
	assert.NotContains(properties, "app")
}

func TestReporterCaptureRejectsUnsupportedEvents(t *testing.T) {
	require := require.New(t)

	client := &fakePostHogClient{}
	reporter := &Reporter{
		client:     client,
		distinctID: "anonymous-install-id",
		version:    "dev",
		commit:     "abc123",
		enabled:    true,
	}

	err := reporter.Capture("repo_opened", map[string]any{"view": "pulls"})
	require.ErrorIs(err, ErrUnsupportedEvent)
}

func TestReporterCaptureDropsUnsafePropertyValues(t *testing.T) {
	assert := Assert.New(t)
	require := require.New(t)

	client := &fakePostHogClient{}
	reporter := &Reporter{
		client:     client,
		distinctID: "anonymous-install-id",
		version:    "dev",
		commit:     "abc123",
		enabled:    true,
	}

	err := reporter.Capture("app_loaded", map[string]any{"view": "owner/repo"})
	require.NoError(err)

	capture, ok := client.message.(posthog.Capture)
	require.True(ok)
	assert.NotContains(capture.Properties, "view")
	assert.Equal("middleman", capture.Properties["application"])
	assert.Equal("dev", capture.Properties["version"])
	assert.True(capture.Properties["$geoip_disable"].(bool))
	assert.False(capture.Properties["$process_person_profile"].(bool))
}

func TestReporterCaptureDaemonActiveUsesStandardShape(t *testing.T) {
	assert := Assert.New(t)
	require := require.New(t)

	client := &fakePostHogClient{}
	reporter := &Reporter{
		client:     client,
		distinctID: "anonymous-install-id",
		version:    "dev",
		commit:     "abc123",
		enabled:    true,
	}

	err := reporter.Capture("daemon_active", map[string]any{"repo_count": 7})
	require.NoError(err)

	capture, ok := client.message.(posthog.Capture)
	require.True(ok)
	assert.Equal("anonymous-install-id", capture.DistinctId)
	assert.Equal("daemon_active", capture.Event)
	assert.Equal(7, capture.Properties["repo_count"])
	assert.Equal("middleman", capture.Properties["application"])
	assert.Equal("dev", capture.Properties["version"])
	assert.Equal("abc123", capture.Properties["commit"])
	assert.Equal(runtime.GOOS, capture.Properties["goos"])
	assert.Equal(runtime.GOARCH, capture.Properties["goarch"])
	assert.Equal("backend", capture.Properties["source"])
	assert.True(capture.Properties["$geoip_disable"].(bool))
	assert.False(capture.Properties["$process_person_profile"].(bool))
}
