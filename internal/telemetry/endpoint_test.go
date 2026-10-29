//go:build !kit_posthog_disabled

package telemetry_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/forge/internal/server"
	"go.kenn.io/forge/internal/server/telemetryapi"
	"go.kenn.io/forge/internal/telemetry"
	"go.kenn.io/forge/internal/testutil/dbtest"
)

func TestCoreActionEndpointDailyClaims(t *testing.T) {
	database := dbtest.Open(t)
	var searches, errors atomic.Int32
	reporter := telemetry.SetupDailyReporter(t,
		telemetry.Options{Database: database, DailyClaimsPath: filepath.Join(t.TempDir(), "daily.json"), Version: "v1.2.3"},
		time.Now(),
		http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			var batch struct {
				Batch []struct {
					Event string `json:"event"`
				} `json:"batch"`
			}
			assert.NoError(t, json.NewDecoder(r.Body).Decode(&batch))
			for _, event := range batch.Batch {
				switch event.Event {
				case "search_empty":
					searches.Add(1)
				case "error_shown":
					errors.Add(1)
				}
			}
			w.WriteHeader(http.StatusOK)
		}),
	)
	srv := server.New(database, nil, nil, "/", nil, server.ServerOptions{Telemetry: reporter})
	t.Cleanup(func() { require.NoError(t, srv.Shutdown(context.WithoutCancel(t.Context()))) })
	for _, tc := range []struct {
		event, key, value string
	}{
		{"pr_viewed", "provider", "github"},
		{"workspace_opened", "item_type", "pull_request"},
	} {
		t.Run(tc.event, func(t *testing.T) {
			assert := assert.New(t)
			post := func(properties map[string]any) *httptest.ResponseRecorder {
				t.Helper()
				body, err := json.Marshal(map[string]any{"event": tc.event, "properties": properties})
				require.NoError(t, err)
				req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/api/v1/telemetry/events", bytes.NewReader(body))
				req.Header.Set("Content-Type", "application/json")
				rr := httptest.NewRecorder()
				srv.ServeHTTP(rr, req)
				return rr
			}
			rr := post(map[string]any{tc.key: "unknown", "surface": "web"})
			assert.Equal(http.StatusBadRequest, rr.Code, rr.Body.String())
			for _, expected := range []string{"queued", "skipped"} {
				rr := post(map[string]any{tc.key: tc.value, "surface": "web"})
				assert.Equal(http.StatusAccepted, rr.Code, rr.Body.String())
				var body telemetryapi.TelemetryEventResponse
				require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &body))
				assert.Equal(expected, body.Status)
			}
		})
	}
	for _, tc := range []struct {
		event      string
		properties map[string]any
	}{
		{"search_empty", map[string]any{"surface": "web"}},
		{"error_shown", map[string]any{"surface": "web", "kind": "pr_viewed"}},
	} {
		t.Run(tc.event, func(t *testing.T) {
			for range 2 {
				body, err := json.Marshal(map[string]any{"event": tc.event, "properties": tc.properties})
				require.NoError(t, err)
				req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/api/v1/telemetry/events", bytes.NewReader(body))
				req.Header.Set("Content-Type", "application/json")
				rr := httptest.NewRecorder()
				srv.ServeHTTP(rr, req)
				require.Equal(t, http.StatusAccepted, rr.Code, "response: %s", rr.Body.String())
				var receipt telemetryapi.TelemetryEventResponse
				require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &receipt))
				assert.Equal(t, "queued", receipt.Status)
			}
		})
	}
	require.NoError(t, reporter.Close())
	assert.Equal(t, int32(2), searches.Load())
	assert.Equal(t, int32(2), errors.Load())
}
