//go:build !kit_posthog_disabled

package telemetry

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
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
	t.Setenv(EnabledEnv, "1")
	t.Setenv("KENN_FORGE_TELEMETRY_ENABLED", "1")
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
	endpoint := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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
	defer endpoint.Close()
	swapKitReporter(t, func(opts posthog.Options, options ...posthog.Option) (Client, error) {
		opts.Endpoint = endpoint.URL
		return posthog.NewReporter(opts, options...)
	})
	opts := Options{Database: database, DailyClaimsPath: filepath.Join(t.TempDir(), "daily.json")}
	reporter, err := newReporter(opts, now)
	require.NoError(err)
	t.Cleanup(func() { require.NoError(reporter.Close()) })
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

func TestReporterCoreActionsDailyClaims(t *testing.T) {
	t.Setenv(EnabledEnv, "1")
	t.Setenv("KENN_FORGE_TELEMETRY_ENABLED", "1")
	var events []string
	var mu sync.Mutex
	endpoint := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var batch struct {
			Batch []struct {
				Event string `json:"event"`
			} `json:"batch"`
		}
		if err := json.NewDecoder(r.Body).Decode(&batch); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		mu.Lock()
		defer mu.Unlock()
		for _, event := range batch.Batch {
			events = append(events, event.Event)
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer endpoint.Close()
	swapKitReporter(t, func(opts posthog.Options, options ...posthog.Option) (Client, error) {
		opts.Endpoint = endpoint.URL
		return posthog.NewReporter(opts, options...)
	})
	reporter, err := newReporter(Options{Database: dbtest.Open(t), DailyClaimsPath: filepath.Join(t.TempDir(), "daily.json")}, time.Now())
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, reporter.Close()) })
	for _, tc := range []struct {
		event, key string
		values     []string
	}{
		{"pr_viewed", "provider", []string{"github", "gitlab", "forgejo", "gitea", "bitbucket"}},
		{"workspace_opened", "item_type", []string{"pull_request", "issue", "kata_task", "adhoc"}},
	} {
		t.Run(tc.event, func(t *testing.T) {
			for _, properties := range []map[string]any{
				nil,
				{"surface": "web"},
				{tc.key: "unknown", "surface": "web"},
				{tc.key: tc.values[0]},
				{tc.key: tc.values[0], "surface": "cli"},
				{tc.key: tc.values[0], "surface": "tui"},
			} {
				_, err := reporter.Report(t.Context(), tc.event, properties)
				require.ErrorIs(t, err, posthog.ErrInvalidProperty)
			}
			for _, value := range tc.values {
				for _, expected := range []posthog.Status{posthog.StatusQueued, posthog.StatusSkipped} {
					status, err := reporter.Report(t.Context(), tc.event, map[string]any{tc.key: value, "surface": "web"})
					require.NoError(t, err)
					assert.Equal(t, expected, status)
				}
			}
		})
	}
	require.NoError(t, reporter.Close())
	mu.Lock()
	defer mu.Unlock()
	assert.ElementsMatch(t, []string{"pr_viewed", "pr_viewed", "pr_viewed", "pr_viewed", "pr_viewed", "workspace_opened", "workspace_opened", "workspace_opened", "workspace_opened"}, events)
}
