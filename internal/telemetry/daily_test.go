//go:build !kit_posthog_disabled

package telemetry

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/forge/internal/testutil/dbtest"
	"go.kenn.io/kit/telemetry/posthog"
)

func TestReporterUsesKitDailyClaimsAcrossUpgradeAndRestart(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	database := dbtest.Open(t)
	now := time.Now().UTC()
	for key, value := range map[string]string{
		InstallIDMetadataKey:        "install-a",
		"telemetry.screen.activity": "install-a\n" + now.Format(time.DateOnly),
		"telemetry.screen.docs":     "install-b\n" + now.Format(time.DateOnly),
	} {
		_, err := database.GetOrCreateAppMetadataValue(t.Context(), key, func() (string, error) { return value, nil })
		require.NoError(err)
	}
	var mu sync.Mutex
	var screens []string
	opts := Options{Database: database, DailyClaimsPath: filepath.Join(t.TempDir(), "daily.json")}
	reporter := setupDailyReporter(t, opts, now, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var batch struct {
			Batch []struct {
				Properties map[string]any `json:"properties"`
			} `json:"batch"`
		}
		if err := json.NewDecoder(r.Body).Decode(&batch); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		mu.Lock()
		defer mu.Unlock()
		for _, event := range batch.Batch {
			if screen, ok := event.Properties["screen"].(string); ok {
				screens = append(screens, screen)
			}
		}
		w.WriteHeader(http.StatusOK)
	}))
	for _, properties := range []map[string]any{nil, {"screen": "unknown"}, {"screen": 42}} {
		_, err := reporter.Report(t.Context(), "screen_viewed", properties)
		require.ErrorIs(err, posthog.ErrInvalidProperty)
	}
	status, err := reporter.Report(t.Context(), "screen_viewed", map[string]any{"screen": "activity", "surface": "web"})
	require.NoError(err)
	assert.Equal(posthog.StatusSkipped, status)
	status, err = reporter.Report(t.Context(), "screen_viewed", map[string]any{"screen": " docs ", "surface": "web"})
	require.NoError(err)
	assert.Equal(posthog.StatusQueued, status)
	require.NoError(reporter.Close())

	restarted, err := newReporter(opts, now)
	require.NoError(err)
	t.Cleanup(func() { require.NoError(restarted.Close()) })
	status, err = restarted.Report(t.Context(), "screen_viewed", map[string]any{"screen": "docs", "surface": "web"})
	require.NoError(err)
	assert.Equal(posthog.StatusSkipped, status)
	status, err = restarted.Report(t.Context(), "screen_viewed", map[string]any{"screen": "settings", "surface": "web"})
	require.NoError(err)
	assert.Equal(posthog.StatusQueued, status)
	require.NoError(restarted.Close())
	mu.Lock()
	defer mu.Unlock()
	assert.ElementsMatch([]string{"docs", "settings"}, screens)
}

func setupDailyReporter(t *testing.T, opts Options, now time.Time, handler http.Handler) *Reporter {
	t.Helper()
	t.Setenv(EnabledEnv, "1")
	t.Setenv("KENN_FORGE_TELEMETRY_ENABLED", "1")
	t.Setenv("DO_NOT_TRACK", "0")
	t.Setenv("CI", "")
	require.NoError(t, os.Unsetenv("CI"))
	endpoint := httptest.NewServer(handler)
	t.Cleanup(endpoint.Close)
	swapKitReporter(t, func(opts posthog.Options, options ...posthog.Option) (Client, error) {
		opts.Endpoint = endpoint.URL
		opts.Version = "v1.2.3"
		return posthog.NewReporter(opts, options...)
	})
	reporter, err := newReporter(opts, now)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, reporter.Close()) })
	return reporter
}
