//go:build !kit_posthog_disabled

package telemetry

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/forge/internal/testutil/dbtest"
	"go.kenn.io/kit/telemetry/posthog"
)

// swapKitReporter replaces newKitReporter for the test and restores it after.
func swapKitReporter(t *testing.T, factory func(posthog.Options, ...posthog.Option) (Client, error)) {
	t.Helper()
	original := newKitReporter
	newKitReporter = factory
	t.Cleanup(func() { newKitReporter = original })
}

func TestNewReporterHandsKitTheStoredInstallIdentity(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)

	t.Setenv(EnabledEnv, "1")
	t.Setenv("KENN_FORGE_TELEMETRY_ENABLED", "1")
	var built []posthog.Options
	swapKitReporter(t, func(opts posthog.Options, _ ...posthog.Option) (Client, error) {
		built = append(built, opts)
		return &fakeKitClient{}, nil
	})
	database := dbtest.Open(t)
	created := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)

	_, err := newReporter(Options{Database: database, DailyClaimsPath: filepath.Join(t.TempDir(), "daily.json")}, created)
	require.NoError(err)

	storedID, found, err := database.AppMetadataValue(t.Context(), InstallIDMetadataKey)
	require.NoError(err)
	require.True(found)
	require.Len(built, 2)
	assert.ElementsMatch([]string{"daemon", "backend"}, []string{built[0].Source, built[1].Source})
	for _, opts := range built {
		assert.Equal(storedID, opts.DistinctID)
		assert.True(created.Equal(opts.InstalledAt))
	}
}

func TestNewReporterDisabledByEnvDoesNotCreateInstallID(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)

	t.Setenv(EnabledEnv, "0")
	database := dbtest.Open(t)

	reporter, err := newReporter(Options{Database: database}, time.Now())
	require.NoError(err)

	assert.False(reporter.Enabled())
	for _, key := range []string{InstallIDMetadataKey, installedAtKey} {
		_, found, err := database.AppMetadataValue(t.Context(), key)
		require.NoError(err)
		assert.False(found, key)
	}
}

func TestKitAllowlistFiltersUIProperties(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)

	t.Setenv(EnabledEnv, "0")
	backend, err := posthog.NewReporter(posthog.Options{}, KitAllowedEvents("backend")...)
	require.NoError(err)

	assert.False(backend.EventAllowed("daemon_active"))
	assert.False(backend.EventAllowed("setup_finished"))
	daemon, err := posthog.NewReporter(posthog.Options{}, KitAllowedEvents("daemon")...)
	require.NoError(err)
	setupProperties, err := daemon.SanitizeProperties("setup_finished", map[string]any{"repo": "acme/widget", "item": 42, "surface": "web"})
	require.NoError(err)
	assert.NotContains(setupProperties, "repo")
	assert.NotContains(setupProperties, "item")
	assert.NotContains(setupProperties, "surface")
	assert.True(backend.EventAllowed("screen_viewed"))
	assert.True(UIEventAllowed("screen_viewed"))
	for _, screen := range []string{"activity", "actions", "repos", "repo-browser", "pulls", "issues", "docs", "workspaces", "terminal", "workspace-item", "settings", "project-intake", "design-system", "onboarding"} {
		properties, err := backend.SanitizeProperties("screen_viewed", map[string]any{"screen": screen, "surface": "web", "repo": "owner/repo"})
		require.NoError(err)
		assert.Equal(screen, properties["screen"])
		assert.Equal("web", properties["surface"])
		assert.NotContains(properties, "repo")
	}
	for _, invalid := range []any{nil, 7, "owner/repo", ""} {
		_, valid := screenFilter(invalid)
		assert.False(valid)
	}
	for _, duration := range []string{"under_1m", "1_to_5m", "5_to_30m", "over_30m", "30m_to_2h", "over_2h"} {
		properties, err := backend.SanitizeProperties("session_ended", map[string]any{"duration_bucket": duration})
		require.NoError(err)
		assert.Equal(duration, properties["duration_bucket"])
	}
	properties, err := backend.SanitizeProperties("app_opened", map[string]any{
		"surface":                 "web",
		"distinct_id":             "spoofed",
		"$geoip_disable":          false,
		"$process_person_profile": true,
	})
	require.NoError(err)
	assert.Equal("web", properties["surface"])
	assert.NotContains(properties, "distinct_id")
	assert.Equal(true, properties["$geoip_disable"])
	assert.Equal(false, properties["$process_person_profile"])

	properties, err = backend.SanitizeProperties("app_opened", map[string]any{"surface": "owner/repo"})
	require.NoError(err)
	assert.NotContains(properties, "surface")
}

func TestDaemonAllowsSyncFailureReason(t *testing.T) {
	t.Setenv(EnabledEnv, "1")
	t.Setenv("KENN_FORGE_TELEMETRY_ENABLED", "1")
	receiver := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) }))
	t.Cleanup(receiver.Close)
	swapKitReporter(t, func(opts posthog.Options, options ...posthog.Option) (Client, error) {
		opts.Endpoint = receiver.URL
		return posthog.NewReporter(opts, options...)
	})
	reporter, err := newReporter(Options{Database: dbtest.Open(t), DailyClaimsPath: filepath.Join(t.TempDir(), "daily.json")}, time.Now())
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, reporter.Close()) })
	status, err := reporter.Report(t.Context(), "sync_failed", map[string]any{"reason": "auth"})
	require.NoError(t, err)
	assert.Equal(t, posthog.StatusQueued, status)
}
