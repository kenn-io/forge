//go:build !kit_posthog_disabled

package telemetry_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync"
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
	reporter := telemetry.SetupDailyReporter(t,
		telemetry.Options{Database: database, DailyClaimsPath: filepath.Join(t.TempDir(), "daily.json"), Version: "v1.2.3"},
		time.Now(),
		http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) }),
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
	require.NoError(t, reporter.Close())
}

func TestOutcomeEndpointCountsRepeatedAttempts(t *testing.T) {
	database := dbtest.Open(t)
	var mu sync.Mutex
	type capturedEvent struct {
		Event      string         `json:"event"`
		Properties map[string]any `json:"properties"`
	}
	var received []capturedEvent
	reporter := telemetry.SetupDailyReporter(t,
		telemetry.Options{Database: database, DailyClaimsPath: filepath.Join(t.TempDir(), "daily.json")},
		time.Now(),
		http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			mu.Lock()
			defer mu.Unlock()
			var batch struct {
				Batch []capturedEvent `json:"batch"`
			}
			assert.NoError(t, json.NewDecoder(r.Body).Decode(&batch))
			received = append(received, batch.Batch...)
			w.WriteHeader(http.StatusOK)
		}),
	)
	srv := server.New(database, nil, nil, "/", nil, server.ServerOptions{Telemetry: reporter})
	t.Cleanup(func() { require.NoError(t, srv.Shutdown(context.WithoutCancel(t.Context()))) })
	for _, tc := range []struct {
		event      string
		properties map[string]any
	}{
		{"search_empty", map[string]any{"surface": "web"}},
		{"error_shown", map[string]any{"surface": "web", "kind": "pr_viewed"}},
	} {
		t.Run(tc.event+"/"+fmt.Sprint(tc.properties), func(t *testing.T) {
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
	mu.Lock()
	defer mu.Unlock()
	var searches, errors int
	for _, event := range received {
		switch event.Event {
		case "search_empty":
			searches++
			assert.Equal(t, "web", event.Properties["surface"])
		case "error_shown":
			errors++
			assert.Equal(t, "web", event.Properties["surface"])
			assert.Equal(t, "pr_viewed", event.Properties["kind"])
		}
	}
	assert.Equal(t, 2, searches)
	assert.Equal(t, 2, errors)
}
