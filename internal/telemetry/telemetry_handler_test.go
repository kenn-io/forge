//go:build !kit_posthog_disabled

package telemetry_test

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
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
	mode := os.Getenv("FORGE_TELEMETRY_HTTP_MODE")
	if mode == "" {
		executable, err := os.Executable()
		require.NoError(t, err)
		for _, mode := range []string{"enabled", "opted out", "init failure", "nil reporter", "validation failure"} {
			t.Run(mode, func(t *testing.T) {
				cmd := exec.CommandContext(t.Context(), executable, "-test.run=^TestCaptureTelemetryEvent_ValidatesSessionDurationWithRealReporter$")
				cmd.Env = append(os.Environ(), "FORGE_TELEMETRY_HTTP_MODE="+mode)
				output, err := cmd.CombinedOutput()
				require.NoError(t, err, string(output))
			})
		}
		return
	}
	t.Setenv(telemetry.EnabledEnv, "1")
	t.Setenv("KENN_FORGE_TELEMETRY_ENABLED", "1")
	require.False(t, posthog.ProcessDisabled())
	endpoint := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer endpoint.Close()
	telemetry.SwapKitReporterForTest(t, func(opts posthog.Options, options ...posthog.Option) (telemetry.Client, error) {
		if mode == "validation failure" {
			return nil, errors.New("allowlist construction failed")
		}
		opts.Endpoint = endpoint.URL
		return posthog.NewReporter(opts, options...)
	})
	options := server.ServerOptions{}
	if mode != "nil reporter" {
		opts := telemetry.Options{}
		switch mode {
		case "enabled":
			opts.Database = dbtest.Open(t)
			opts.DailyClaimsPath = filepath.Join(t.TempDir(), "daily.json")
		case "opted out":
			t.Setenv(telemetry.EnabledEnv, "0")
		}
		reporter := telemetry.NewReporterOrDisabledForTest(opts)
		require.Equal(t, mode == "enabled", reporter.Enabled())
		t.Cleanup(func() { require.NoError(t, reporter.Close()) })
		options.Telemetry = reporter
	}
	srv := server.New(serverfake.OpenTestDB(t), nil, nil, "/", nil, options)
	t.Cleanup(func() { serverfake.GracefulShutdown(t, srv) })
	type durationCase struct {
		name       string
		properties string
		code       int
	}
	cases := []durationCase{
		{"missing", `{}`, http.StatusBadRequest},
		{"unknown", `{"duration_bucket":"unknown"}`, http.StatusBadRequest},
		{"null", `{"duration_bucket":null}`, http.StatusBadRequest},
		{"numeric", `{"duration_bucket":7}`, http.StatusBadRequest},
		{"padded property", `{" duration_bucket ":"under_1m"}`, http.StatusAccepted},
	}
	for _, bucket := range []string{"under_1m", "1_to_5m", "5_to_30m", "over_30m", "30m_to_2h", "over_2h"} {
		cases = append(cases, durationCase{bucket, `{"duration_bucket":"` + bucket + `"}`, http.StatusAccepted})
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/api/v1/telemetry/events", strings.NewReader(`{"event":"session_ended","properties":`+tc.properties+`}`))
			req.Header.Set("Content-Type", "application/json")
			rr := httptest.NewRecorder()
			srv.ServeHTTP(rr, req)
			code := tc.code
			if mode == "validation failure" {
				code = http.StatusInternalServerError
			}
			assert.Equal(t, code, rr.Code, rr.Body.String())
			if code == http.StatusAccepted {
				status := "disabled"
				if mode == "enabled" {
					status = "queued"
				}
				var body telemetryapi.TelemetryEventResponse
				require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &body))
				assert.Equal(t, status, body.Status)
			}
		})
	}
	assert.Equal(t, mode == "init failure" || mode == "nil reporter" || mode == "validation failure", posthog.ProcessDisabled())
}
