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
	"go.kenn.io/forge/internal/server/telemetryapi"
	serverfake "go.kenn.io/forge/internal/testutil/serverfake"
	servertest "go.kenn.io/forge/internal/testutil/servertest"
	"go.kenn.io/kit/telemetry/posthog"
)

func TestCaptureTelemetryEvent_UsesKitReportResult(t *testing.T) {
	serverfake.RunParallelServerTest(t)
	for _, tc := range []struct {
		name   string
		status posthog.Status
		err    error
		code   int
	}{
		{"queued", posthog.StatusQueued, nil, http.StatusAccepted},
		{"skipped", posthog.StatusSkipped, nil, http.StatusAccepted},
		{"disabled", posthog.StatusDisabled, nil, http.StatusAccepted},
		{"invalid property", "", posthog.ErrInvalidProperty, http.StatusBadRequest},
		{"storage failure", "", errors.New("storage unavailable"), http.StatusInternalServerError},
		{"queue failure", "", errors.New("queue full"), http.StatusInternalServerError},
	} {
		t.Run(tc.name, func(t *testing.T) {
			assert := assert.New(t)
			telemetry := &serverfake.FakeTelemetry{EnabledValue: true, ReportStatus: tc.status, ReportError: tc.err}
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

func TestCaptureTelemetryEvent_SessionDuration(t *testing.T) {
	serverfake.RunParallelServerTest(t)
	for _, disabled := range []bool{false, true} {
		for _, tc := range []struct {
			name, properties string
			valid            bool
		}{
			{"under_1m", `{"duration_bucket":"under_1m"}`, true},
			{"1_to_5m", `{"duration_bucket":"1_to_5m"}`, true},
			{"5_to_30m", `{"duration_bucket":"5_to_30m"}`, true},
			{"over_30m", `{"duration_bucket":"over_30m"}`, true},
			{"30m_to_2h", `{"duration_bucket":"30m_to_2h"}`, true},
			{"over_2h", `{"duration_bucket":"over_2h"}`, true},
			{"missing", `{}`, false},
			{"unknown", `{"duration_bucket":"all_day"}`, false},
			{"null", `{"duration_bucket":null}`, false},
			{"number", `{"duration_bucket":31}`, false},
		} {
			t.Run(tc.name+map[bool]string{false: "/enabled", true: "/disabled"}[disabled], func(t *testing.T) {
				assert := assert.New(t)
				telemetry := &serverfake.FakeTelemetry{EnabledValue: !disabled}
				srv := servertest.NewTelemetryTestServer(t, telemetry)
				req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/api/v1/telemetry/events", strings.NewReader(`{"event":"session_ended","properties":`+tc.properties+`}`))
				req.Header.Set("Content-Type", "application/json")
				rr := httptest.NewRecorder()
				srv.ServeHTTP(rr, req)
				if !tc.valid {
					assert.Equal(http.StatusBadRequest, rr.Code)
					assert.Contains(rr.Body.String(), "unsupported or missing session duration")
					assert.Empty(telemetry.Event)
					return
				}
				assert.Equal(http.StatusAccepted, rr.Code)
				if disabled {
					assert.Empty(telemetry.Event)
				} else {
					assert.Equal("session_ended", telemetry.Event)
					assert.Equal(tc.name, telemetry.Properties["duration_bucket"])
				}
			})
		}
	}
}
