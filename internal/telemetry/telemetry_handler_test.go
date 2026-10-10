//go:build !kit_posthog_disabled

package telemetry_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/forge/internal/server"
	"go.kenn.io/forge/internal/server/telemetryapi"
	"go.kenn.io/forge/internal/telemetry"
	"go.kenn.io/forge/internal/testutil/dbtest"
	"go.kenn.io/forge/internal/testutil/serverfake"
	"go.kenn.io/kit/telemetry/posthog"
)

func TestCaptureTelemetryEvent_ValidatesSessionDurationWithRealReporter(t *testing.T) {
	t.Setenv(telemetry.EnabledEnv, "0")
	fallback := telemetry.DisabledReporter()
	t.Setenv(telemetry.EnabledEnv, "1")
	t.Setenv("KENN_FORGE_TELEMETRY_ENABLED", "1")
	endpoint := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer endpoint.Close()
	telemetry.SwapKitReporterForTest(t, func(opts posthog.Options, options ...posthog.Option) (telemetry.Client, error) {
		opts.Endpoint = endpoint.URL
		return posthog.NewReporter(opts, options...)
	})
	for _, mode := range []string{"enabled", "init failure"} {
		t.Run(mode, func(t *testing.T) {
			opts := telemetry.Options{}
			if mode == "enabled" {
				opts.Database = dbtest.Open(t)
				opts.DailyClaimsPath = filepath.Join(t.TempDir(), "daily.json")
			}
			reporter := telemetry.NewReporterOrDisabledForTest(opts)
			require.Equal(t, mode == "enabled", reporter.Enabled())
			if mode == "init failure" {
				require.Same(t, fallback, reporter)
			}
			t.Cleanup(func() { require.NoError(t, reporter.Close()) })
			srv := server.New(serverfake.OpenTestDB(t), nil, nil, "/", nil, server.ServerOptions{Telemetry: reporter})
			t.Cleanup(func() { serverfake.GracefulShutdown(t, srv) })
			for _, tc := range []struct {
				name       string
				properties string
				code       int
			}{
				{"valid", `{"duration_bucket":"under_1m"}`, http.StatusAccepted},
				{"missing", `{}`, http.StatusBadRequest},
				{"unknown", `{"duration_bucket":"unknown"}`, http.StatusBadRequest},
				{"null", `{"duration_bucket":null}`, http.StatusBadRequest},
				{"numeric", `{"duration_bucket":7}`, http.StatusBadRequest},
			} {
				t.Run(tc.name, func(t *testing.T) {
					req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/api/v1/telemetry/events", strings.NewReader(`{"event":"session_ended","properties":`+tc.properties+`}`))
					req.Header.Set("Content-Type", "application/json")
					rr := httptest.NewRecorder()
					srv.ServeHTTP(rr, req)
					assert.Equal(t, tc.code, rr.Code, rr.Body.String())
					if tc.code == http.StatusAccepted {
						status := "queued"
						if mode == "init failure" {
							status = "disabled"
						}
						var body telemetryapi.TelemetryEventResponse
						require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &body))
						assert.Equal(t, status, body.Status)
					}
				})
			}
		})
	}
}
