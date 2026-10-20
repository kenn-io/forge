//go:build !kit_posthog_disabled

package telemetry

import (
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

	enableTelemetryEnv(t)
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

	enableTelemetryEnv(t)
	t.Setenv(EnabledEnv, "0")
	database := dbtest.Open(t)

	reporter, err := newReporter(Options{Database: database, Version: "v1.2.3"}, time.Now())
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
	backend, err := posthog.NewReporter(posthog.Options{}, kitAllowedEvents("backend")...)
	require.NoError(err)

	assert.False(backend.EventAllowed("daemon_active"))
	assert.False(backend.EventAllowed("setup_finished"))
	daemon, err := posthog.NewReporter(posthog.Options{}, kitAllowedEvents("daemon")...)
	require.NoError(err)
	assert.True(daemon.EventAllowed("setup_finished"))
	assert.True(backend.EventAllowed("screen_viewed"))
	for _, tc := range []struct {
		screen any
		valid  bool
	}{
		{"activity", true},
		{"actions", true},
		{"repos", true},
		{"repo-browser", true},
		{"pulls", true},
		{"issues", true},
		{"docs", true},
		{"workspaces", true},
		{"terminal", true},
		{"workspace-item", true},
		{"settings", true},
		{"project-intake", true},
		{"design-system", true},
		{"onboarding", true},
		{"worklists", true},
		{nil, false},
		{7, false},
		{"owner/repo", false},
		{"", false},
	} {
		value, valid := allowedEvents["screen_viewed"]["screen"](tc.screen)
		assert.Equal(tc.valid, valid, "screen: %v", tc.screen)
		if tc.valid {
			assert.Equal(tc.screen, value)
		}
	}
	for _, duration := range []string{"under_1m", "1_to_5m", "5_to_30m", "over_30m", "30m_to_2h", "over_2h"} {
		value, valid := allowedEvents["session_ended"]["duration_bucket"](duration)
		assert.True(valid)
		assert.Equal(duration, value)
	}
	_, valid := allowedEvents["app_opened"]["surface"]("owner/repo")
	assert.False(valid)
}

func TestSyncFailedProperties(t *testing.T) {
	t.Setenv(EnabledEnv, "0")
	daemon, err := posthog.NewReporter(posthog.Options{}, kitAllowedEvents("daemon")...)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, daemon.Close()) })
	for _, tc := range []struct{ source, reason, wantSource, wantReason string }{
		{"issues", "auth", "issues", "auth"},
		{"issues", "private provider message", "issues", "other"},
	} {
		properties, err := daemon.SanitizeProperties("sync_failed", map[string]any{
			"sync_source": tc.source, "reason": tc.reason, "repo": "acme/widget", "error": "private details",
		})
		require.NoError(t, err)
		assert := assert.New(t)
		assert.Equal(tc.wantSource, properties["sync_source"])
		assert.Equal(tc.wantReason, properties["reason"])
	}
	for _, properties := range []map[string]any{{"reason": "auth"}, {"sync_source": "issues"}, {"sync_source": "acme/widget", "reason": "auth"}} {
		_, err := daemon.SanitizeProperties("sync_failed", properties)
		require.ErrorIs(t, err, posthog.ErrInvalidProperty)
	}
}
