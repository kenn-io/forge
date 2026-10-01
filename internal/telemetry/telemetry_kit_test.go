//go:build !kit_posthog_disabled

package telemetry

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"runtime"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/forge/internal/db"
	"go.kenn.io/forge/internal/testutil/dbtest"
	kittelemetry "go.kenn.io/kit/telemetry"
)

func enableTelemetryEnv(t *testing.T) {
	t.Helper()
	t.Setenv(EnabledEnv, "1")
	t.Setenv("KENN_FORGE_TELEMETRY_ENABLED", "1")
}

// swapKitReporter replaces newKitReporter for the test and restores it after.
func swapKitReporter(t *testing.T, factory func(kittelemetry.PostHogOptions, ...kittelemetry.PostHogOption) (kittelemetry.PostHogClient, error)) {
	t.Helper()
	original := newKitReporter
	newKitReporter = factory
	t.Cleanup(func() { newKitReporter = original })
}

func TestNewReporterBuildsKitReportersPerSource(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)

	enableTelemetryEnv(t)
	var built []kittelemetry.PostHogOptions
	swapKitReporter(t, func(opts kittelemetry.PostHogOptions, _ ...kittelemetry.PostHogOption) (kittelemetry.PostHogClient, error) {
		built = append(built, opts)
		return &fakeKitClient{}, nil
	})
	database := dbtest.Open(t)
	created := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)

	reporter, err := newReporter(Options{Database: database, Version: "1.2.3", Commit: "abc123"}, created)
	require.NoError(err)
	require.True(reporter.Enabled())

	storedID, found, err := database.AppMetadataValue(t.Context(), installIDMetadataKey)
	require.NoError(err)
	require.True(found)
	require.Len(built, 2)
	sources := []string{built[0].Source, built[1].Source}
	assert.ElementsMatch([]string{"daemon", "backend"}, sources)
	for _, opts := range built {
		assert.Equal(storedID, opts.DistinctID)
		assert.True(created.Equal(opts.InstalledAt))
		assert.Equal("kenn-forge", opts.Application)
		assert.Equal("KENN_FORGE", opts.EnvPrefix)
		assert.Equal(postHogEndpoint, opts.Endpoint)
		assert.Equal(postHogAPIKey, opts.APIKey)
		assert.Equal("1.2.3", opts.Version)
		assert.Equal("abc123", opts.Commit)
	}
}

func TestNewReporterDisabledByEnvDoesNotCreateInstallID(t *testing.T) {
	tests := []struct {
		name     string
		generic  string
		prefixed string
	}{
		{name: "generic opt-out", generic: "0", prefixed: "1"},
		{name: "prefixed opt-out", generic: "1", prefixed: "0"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert := assert.New(t)
			require := require.New(t)

			t.Setenv(EnabledEnv, tt.generic)
			t.Setenv("KENN_FORGE_TELEMETRY_ENABLED", tt.prefixed)
			calls := 0
			swapKitReporter(t, func(kittelemetry.PostHogOptions, ...kittelemetry.PostHogOption) (kittelemetry.PostHogClient, error) {
				calls++
				return &fakeKitClient{}, nil
			})
			database := dbtest.Open(t)

			reporter, err := newReporter(Options{Database: database}, time.Now())
			require.NoError(err)

			assert.False(reporter.Enabled())
			assert.Zero(calls)
			for _, key := range []string{installIDMetadataKey, installedAtKey} {
				_, found, err := database.AppMetadataValue(t.Context(), key)
				require.NoError(err)
				assert.False(found, key)
			}
		})
	}
}

type postHogEvent struct {
	Event      string         `json:"event"`
	DistinctID string         `json:"distinct_id"`
	Properties map[string]any `json:"properties"`
}

// sendThroughKit runs real kit reporters against a local batch endpoint and
// returns the events it received after Close.
func sendThroughKit(t *testing.T, database *db.DB, now time.Time, capture func(*Reporter)) []postHogEvent {
	t.Helper()
	var mu sync.Mutex
	var events []postHogEvent
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Batch []postHogEvent `json:"batch"`
		}
		if assert.NoError(t, json.NewDecoder(r.Body).Decode(&body)) {
			mu.Lock()
			events = append(events, body.Batch...)
			mu.Unlock()
		}
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(server.Close)
	swapKitReporter(t, func(opts kittelemetry.PostHogOptions, options ...kittelemetry.PostHogOption) (kittelemetry.PostHogClient, error) {
		opts.Endpoint = server.URL
		return kittelemetry.NewPostHogReporter(opts, options...)
	})
	enableTelemetryEnv(t)

	reporter, err := newReporter(Options{Database: database, Version: "1.2.3", Commit: "abc123"}, now)
	require.NoError(t, err)
	require.True(t, reporter.Enabled())
	capture(reporter)
	require.NoError(t, reporter.Close())

	mu.Lock()
	defer mu.Unlock()
	return events
}

func eventsByName(events []postHogEvent) map[string]postHogEvent {
	byName := make(map[string]postHogEvent, len(events))
	for _, event := range events {
		byName[event.Event] = event
	}
	return byName
}

func TestReporterSendsTodaysPropertiesThroughKit(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)

	database := dbtest.Open(t)
	events := sendThroughKit(t, database, time.Now(), func(reporter *Reporter) {
		require.NoError(reporter.Capture("app_loaded", map[string]any{
			"view":              "repos",
			"application":       "caller-app",
			"source":            "caller",
			"version":           "caller-version",
			"repo":              "owner/name",
			"install_age_hours": 999,
		}))
		require.NoError(reporter.Capture("daemon_active", map[string]any{"repo_count": 3}))
	})

	storedID, found, err := database.AppMetadataValue(t.Context(), installIDMetadataKey)
	require.NoError(err)
	require.True(found)
	require.Len(events, 2)
	byName := eventsByName(events)
	for name, want := range map[string]map[string]any{
		"app_loaded":    {"source": "backend", "view": "repos"},
		"daemon_active": {"source": "daemon", "repo_count": float64(3)},
	} {
		event, ok := byName[name]
		require.True(ok, name)
		assert.Equal(storedID, event.DistinctID)
		props := event.Properties
		for key, value := range want {
			assert.Equal(value, props[key], key)
		}
		assert.Equal(false, props["$process_person_profile"])
		assert.Equal(true, props["$geoip_disable"])
		assert.Equal("kenn-forge", props["application"])
		assert.Equal("1.2.3", props["version"])
		assert.Equal("abc123", props["commit"])
		assert.Equal(runtime.GOOS, props["goos"])
		assert.Equal(runtime.GOARCH, props["goarch"])
		assert.InDelta(0, props["install_age_hours"], 0)
		assert.NotContains(props, "repo")
	}
}

func TestReporterTagsInstallAgeThroughKit(t *testing.T) {
	tests := []struct {
		name      string
		seed      func(t *testing.T, database *db.DB)
		createdAt time.Time
		wantAge   any
	}{
		{name: "install created 30 hours ago", createdAt: time.Now().Add(-30 * time.Hour), wantAge: float64(30)},
		{
			name: "install ID without a creation time",
			seed: func(t *testing.T, database *db.DB) {
				_, err := database.GetOrCreateAppMetadataValue(t.Context(), installIDMetadataKey, func() (string, error) {
					return "existing-install-id", nil
				})
				require.NoError(t, err)
			},
			createdAt: time.Now(),
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			database := dbtest.Open(t)
			if tt.seed != nil {
				tt.seed(t, database)
			}

			events := sendThroughKit(t, database, tt.createdAt, func(reporter *Reporter) {
				require.NoError(t, reporter.Capture("daemon_active", map[string]any{"repo_count": 1}))
			})

			require.Len(t, events, 1)
			if tt.wantAge == nil {
				assert.NotContains(t, events[0].Properties, "install_age_hours")
				return
			}
			assert.Equal(t, tt.wantAge, events[0].Properties["install_age_hours"])
		})
	}
}

func TestReporterDropsUnsafePropertyValuesThroughKit(t *testing.T) {
	database := dbtest.Open(t)
	events := sendThroughKit(t, database, time.Now(), func(reporter *Reporter) {
		require.NoError(t, reporter.Capture("app_loaded", map[string]any{"view": "owner/repo"}))
	})

	require.Len(t, events, 1)
	assert.NotContains(t, events[0].Properties, "view")
	assert.Equal(t, "backend", events[0].Properties["source"])
}
