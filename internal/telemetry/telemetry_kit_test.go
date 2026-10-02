//go:build !kit_posthog_disabled

package telemetry

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/forge/internal/testutil/dbtest"
	kittelemetry "go.kenn.io/kit/telemetry"
)

// swapKitReporter replaces newKitReporter for the test and restores it after.
func swapKitReporter(t *testing.T, factory func(kittelemetry.PostHogOptions, ...kittelemetry.PostHogOption) (kittelemetry.PostHogClient, error)) {
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
	var built []kittelemetry.PostHogOptions
	swapKitReporter(t, func(opts kittelemetry.PostHogOptions, _ ...kittelemetry.PostHogOption) (kittelemetry.PostHogClient, error) {
		built = append(built, opts)
		return &fakeKitClient{}, nil
	})
	database := dbtest.Open(t)
	created := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)

	_, err := newReporter(Options{Database: database}, created)
	require.NoError(err)

	storedID, found, err := database.AppMetadataValue(t.Context(), installIDMetadataKey)
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
	for _, key := range []string{installIDMetadataKey, installedAtKey} {
		_, found, err := database.AppMetadataValue(t.Context(), key)
		require.NoError(err)
		assert.False(found, key)
	}
}

// postHogBatchEvent is the part of a posthog-go /batch/ message this test reads.
type postHogBatchEvent struct {
	Event      string         `json:"event"`
	DistinctID string         `json:"distinct_id"`
	Properties map[string]any `json:"properties"`
}

func TestCaptureHandlerSendsAppOpenedThroughBackendReporter(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)

	var (
		mu     sync.Mutex
		events []postHogBatchEvent
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/batch/") {
			var batch struct {
				Batch []postHogBatchEvent `json:"batch"`
			}
			body, err := io.ReadAll(r.Body)
			assert.NoError(err)
			assert.NoError(json.Unmarshal(body, &batch))
			mu.Lock()
			events = append(events, batch.Batch...)
			mu.Unlock()
		}
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)

	t.Setenv(EnabledEnv, "1")
	t.Setenv("KENN_FORGE_TELEMETRY_ENABLED", "1")
	swapKitReporter(t, func(opts kittelemetry.PostHogOptions, options ...kittelemetry.PostHogOption) (kittelemetry.PostHogClient, error) {
		opts.Endpoint = srv.URL
		return kittelemetry.NewPostHogReporter(opts, options...)
	})
	database := dbtest.Open(t)

	reporter, err := newReporter(Options{Database: database}, time.Now())
	require.NoError(err)
	handler := reporter.CaptureHandler()

	opened := postCapture(t, handler, `{"event":"app_opened"}`)
	assert.Equal(http.StatusAccepted, opened.Code)
	assert.JSONEq(`{"status":"queued"}`, opened.Body.String())
	assert.Equal(http.StatusBadRequest, postCapture(t, handler, `{"event":"repo_opened"}`).Code)
	require.NoError(reporter.Close())

	storedID, found, err := database.AppMetadataValue(t.Context(), installIDMetadataKey)
	require.NoError(err)
	require.True(found)
	mu.Lock()
	defer mu.Unlock()
	require.Len(events, 1)
	assert.Equal("app_opened", events[0].Event)
	assert.Equal("backend", events[0].Properties["source"])
	assert.Equal(storedID, events[0].DistinctID)
}

func TestReporterConstructionErrorAdmitsNoEvent(t *testing.T) {
	t.Setenv(EnabledEnv, "1")
	t.Setenv("KENN_FORGE_TELEMETRY_ENABLED", "1")
	swapKitReporter(t, func(kittelemetry.PostHogOptions, ...kittelemetry.PostHogOption) (kittelemetry.PostHogClient, error) {
		return nil, errors.New("kit reporter unavailable")
	})

	_, err := newReporter(Options{Database: dbtest.Open(t)}, time.Now())
	require.Error(t, err)

	// NewReporterOrDisabled falls back to DisabledReporter on any construction error.
	assert.Equal(t, http.StatusBadRequest, postCapture(t, DisabledReporter().CaptureHandler(), `{"event":"app_opened"}`).Code)
}
