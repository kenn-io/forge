package settingsservertest

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/forge/internal/server"
	"go.kenn.io/forge/internal/server/telemetryapi"
	telemetrypkg "go.kenn.io/forge/internal/telemetry"
	serverfake "go.kenn.io/forge/internal/testutil/serverfake"
	servertest "go.kenn.io/forge/internal/testutil/servertest"
	"go.kenn.io/kit/telemetry/posthog"
)

func TestCaptureTelemetryEvent_UsesKitReportResult(t *testing.T) {
	serverfake.RunParallelServerTest(t)
	for _, tc := range []struct {
		name     string
		status   posthog.Status
		err      error
		code     int
		disabled bool
	}{
		{"queued", posthog.StatusQueued, nil, http.StatusAccepted, false},
		{"skipped", posthog.StatusSkipped, nil, http.StatusAccepted, false},
		{"disabled", posthog.StatusDisabled, nil, http.StatusAccepted, true},
		{"invalid property", "", posthog.ErrInvalidProperty, http.StatusBadRequest, false},
		{"storage failure", "", errors.New("storage unavailable"), http.StatusInternalServerError, false},
		{"queue failure", "", errors.New("queue full"), http.StatusInternalServerError, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			assert := assert.New(t)
			telemetry := &serverfake.FakeTelemetry{EnabledValue: !tc.disabled, ReportStatus: tc.status, ReportError: tc.err}
			srv := servertest.NewTelemetryTestServer(t, telemetry)
			req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/api/v1/telemetry/events", strings.NewReader(`{"event":"screen_viewed","properties":{"screen":"activity","surface":"web"}}`))
			req.Header.Set("Content-Type", "application/json")
			rr := httptest.NewRecorder()
			srv.ServeHTTP(rr, req)
			assert.Equal(tc.code, rr.Code)
			if tc.err != nil {
				assert.Empty(telemetry.Event)
				return
			}
			var body telemetryapi.TelemetryEventResponse
			require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &body))
			assert.Equal(string(tc.status), body.Status)
			assert.Equal("screen_viewed", telemetry.Event)
			assert.Equal(map[string]any{"screen": "activity", "surface": "web"}, telemetry.Properties)
		})
	}
}

func TestCaptureTelemetryEvent_ValidatesSessionDurationWhenDisabled(t *testing.T) {
	t.Setenv(telemetrypkg.EnabledEnv, "0")
	for _, mode := range []string{"disabled", "unavailable"} {
		t.Run(mode, func(t *testing.T) {
			options := server.ServerOptions{}
			if mode == "disabled" {
				options.Telemetry = telemetrypkg.DisabledReporter()
			}
			srv := server.New(serverfake.OpenTestDB(t), nil, nil, "/", nil, options)
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
						var body telemetryapi.TelemetryEventResponse
						require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &body))
						assert.Equal(t, "disabled", body.Status)
					}
				})
			}
		})
	}
}

func TestCaptureTelemetryEvent_QueuesEvent(t *testing.T) {
	serverfake.RunParallelServerTest(t)
	assert := assert.New(t)
	require := require.New(t)

	telemetry := &serverfake.FakeTelemetry{EnabledValue: true}
	srv := servertest.NewTelemetryTestServer(t, telemetry)

	req := httptest.NewRequestWithContext(t.Context(),
		http.MethodPost,
		"/api/v1/telemetry/events",
		strings.NewReader(`{"event":"app_opened","properties":{"surface":"web"}}`),
	)
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()

	srv.ServeHTTP(rr, req)

	assert.Equal(http.StatusAccepted, rr.Code)
	assert.Equal("app_opened", telemetry.Event)
	assert.Equal(map[string]any{"surface": "web"}, telemetry.Properties)

	var body telemetryapi.TelemetryEventResponse
	err := json.NewDecoder(rr.Body).Decode(&body)
	require.NoError(err)
	assert.Equal("queued", body.Status)
}

func TestCaptureTelemetryEvent_ReturnsDisabledWhenTelemetryUnavailable(t *testing.T) {
	serverfake.RunParallelServerTest(t)
	assert := assert.New(t)
	require := require.New(t)

	srv := servertest.NewTelemetryTestServer(t, nil)
	req := httptest.NewRequestWithContext(t.Context(),
		http.MethodPost,
		"/api/v1/telemetry/events",
		strings.NewReader(`{"event":"app_opened"}`),
	)
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()

	srv.ServeHTTP(rr, req)

	assert.Equal(http.StatusAccepted, rr.Code)

	var body telemetryapi.TelemetryEventResponse
	err := json.NewDecoder(rr.Body).Decode(&body)
	require.NoError(err)
	assert.Equal("disabled", body.Status)
}
