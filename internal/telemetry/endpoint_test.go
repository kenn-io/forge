//go:build !kit_posthog_disabled

package telemetry_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
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
		telemetry.Options{Database: database, DailyClaimsPath: filepath.Join(t.TempDir(), "daily.json")},
		time.Now(),
		http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) }),
	)
	srv := server.New(database, nil, nil, "/", nil, server.ServerOptions{Telemetry: reporter})
	t.Cleanup(func() { require.NoError(t, srv.Shutdown(context.WithoutCancel(t.Context()))) })
	for _, tc := range []struct {
		event, key string
		values     []string
	}{
		{"pr_viewed", "provider", []string{"github", "gitlab", "forgejo", "gitea", "bitbucket"}},
		{"workspace_opened", "item_type", []string{"pull_request", "issue", "kata_task", "adhoc"}},
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
			for _, properties := range []map[string]any{
				nil,
				{"surface": "web"},
				{tc.key: "unknown", "surface": "web"},
				{tc.key: tc.values[0]},
				{tc.key: tc.values[0], "surface": "cli"},
				{tc.key: tc.values[0], "surface": "tui"},
			} {
				rr := post(properties)
				assert.Equal(http.StatusBadRequest, rr.Code, "properties: %v; response: %s", properties, rr.Body.String())
			}
			for _, value := range tc.values {
				for _, expected := range []string{"queued", "skipped"} {
					rr := post(map[string]any{tc.key: value, "surface": "web"})
					assert.Equal(http.StatusAccepted, rr.Code, "value: %s; response: %s", value, rr.Body.String())
					var body telemetryapi.TelemetryEventResponse
					require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &body))
					assert.Equal(expected, body.Status, "value: %s", value)
				}
			}
		})
	}
	require.NoError(t, reporter.Close())
}
