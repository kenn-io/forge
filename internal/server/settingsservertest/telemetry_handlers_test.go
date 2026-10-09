package settingsservertest

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/forge/internal/server"
	"go.kenn.io/forge/internal/server/telemetryapi"
	"go.kenn.io/forge/internal/testutil/dbtest"
	serverfake "go.kenn.io/forge/internal/testutil/serverfake"
	servertest "go.kenn.io/forge/internal/testutil/servertest"
)

type screenTelemetry struct {
	count      atomic.Int32
	disabled   bool
	err        error
	properties map[string]any
}

func (s *screenTelemetry) Enabled() bool { return !s.disabled }
func (s *screenTelemetry) Close() error  { return nil }
func (s *screenTelemetry) Capture(_ string, properties map[string]any) error {
	s.count.Add(1)
	s.properties = properties
	return s.err
}

func TestCaptureTelemetryEvent_ScreenClaims(t *testing.T) {
	serverfake.RunParallelServerTest(t)
	assert := assert.New(t)
	require := require.New(t)
	database := dbtest.Open(t)
	_, err := database.GetOrCreateAppMetadataValue(t.Context(), "telemetry.install_id", func() (string, error) { return "install-a", nil })
	require.NoError(err)
	telemetry := &screenTelemetry{}
	newServer := func() *server.Server {
		return servertest.New(t, database, nil, nil, "/", nil, server.ServerOptions{Telemetry: telemetry})
	}
	srv := newServer()
	var status string
	post := func(body string) int {
		req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/api/v1/telemetry/events", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		rr := httptest.NewRecorder()
		srv.ServeHTTP(rr, req)
		var resp telemetryapi.TelemetryEventResponse
		status = ""
		if rr.Code == http.StatusAccepted {
			require.NoError(json.Unmarshal(rr.Body.Bytes(), &resp))
			status = resp.Status
		}
		return rr.Code
	}
	assert.Equal(http.StatusAccepted, post(`{"event":"screen_viewed","properties":{}}`))
	assert.Equal("skipped", status)
	assert.Equal(int32(0), telemetry.count.Load())
	telemetry.disabled = true
	assert.Equal(http.StatusAccepted, post(`{"event":"screen_viewed","properties":{"screen":"activity"}}`))
	assert.Equal("disabled", status)
	_, found, err := database.AppMetadataValue(t.Context(), "telemetry.screen.activity")
	require.NoError(err)
	assert.False(found)
	telemetry.disabled = false
	assert.Equal(http.StatusAccepted, post(`{"event":"screen_viewed","properties":{"screen":" activity "," screen ":"docs","surface":"web"," surface ":"owner/repo"}}`))
	assert.Equal("queued", status)
	assert.Equal(map[string]any{"screen": "activity", "surface": "web"}, telemetry.properties)
	_, found, err = database.AppMetadataValue(t.Context(), "telemetry.screen.docs")
	require.NoError(err)
	assert.False(found)
	srv = newServer()
	assert.Equal(http.StatusAccepted, post(`{"event":"screen_viewed","properties":{"screen":"activity"}}`))
	assert.Equal("skipped", status)
	assert.Equal(int32(1), telemetry.count.Load())
	telemetry.err = errors.New("queue full")
	assert.Equal(http.StatusInternalServerError, post(`{"event":"screen_viewed","properties":{"screen":"docs"}}`))
	telemetry.err = nil
	assert.Equal(http.StatusAccepted, post(`{"event":"screen_viewed","properties":{"screen":"docs"}}`))
	assert.Equal(int32(3), telemetry.count.Load())
	require.NoError(database.Close())
	assert.Equal(http.StatusInternalServerError, post(`{"event":"screen_viewed","properties":{"screen":"settings"}}`))
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
