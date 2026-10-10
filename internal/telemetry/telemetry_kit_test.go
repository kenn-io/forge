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

func TestReporterValidatesSessionDurationInBothModes(t *testing.T) {
	t.Setenv(EnabledEnv, "1")
	t.Setenv("KENN_FORGE_TELEMETRY_ENABLED", "1")
	endpoint := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer endpoint.Close()
	swapKitReporter(t, func(opts posthog.Options, options ...posthog.Option) (Client, error) {
		opts.Endpoint = endpoint.URL
		return posthog.NewReporter(opts, options...)
	})
	for _, disabled := range []bool{false, true} {
		t.Run(map[bool]string{false: "enabled", true: "disabled"}[disabled], func(t *testing.T) {
			reporter := DisabledReporter()
			if !disabled {
				var err error
				reporter, err = newReporter(Options{Database: dbtest.Open(t), DailyClaimsPath: filepath.Join(t.TempDir(), "daily.json")}, time.Now())
				require.NoError(t, err)
			}
			defer func() { require.NoError(t, reporter.Close()) }()
			require.Equal(t, !disabled, reporter.Enabled())
			_, err := reporter.Report(t.Context(), "session_ended", nil)
			require.ErrorIs(t, err, posthog.ErrInvalidProperty)
			status, err := reporter.Report(t.Context(), "session_ended", map[string]any{" duration_bucket ": "under_1m"})
			require.NoError(t, err)
			want := posthog.StatusQueued
			if disabled {
				want = posthog.StatusDisabled
			}
			assert.Equal(t, want, status)
		})
	}
}

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
	backend, err := posthog.NewReporter(posthog.Options{}, kitAllowedEvents("backend")...)
	require.NoError(err)

	assert.False(backend.EventAllowed("daemon_active"))
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
