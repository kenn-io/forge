package settingsservertest

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/forge/internal/server"
	"go.kenn.io/forge/internal/server/telemetryapi"
)

type fakeTelemetry struct {
	enabled    bool
	event      string
	properties map[string]any
}

func (f *fakeTelemetry) Capture(event string, properties map[string]any) error {
	f.event = event
	f.properties = properties
	return nil
}

func (f *fakeTelemetry) Close() error { return nil }

func (f *fakeTelemetry) Enabled() bool { return f.enabled }

func newTelemetryTestServer(t *testing.T, telemetry *fakeTelemetry) *server.Server {
	t.Helper()
	options := server.ServerOptions{}
	if telemetry != nil {
		options.Telemetry = telemetry
	}
	srv := server.New(
		openTestDB(t), nil, nil, "/", nil,
		options,
	)
	t.Cleanup(func() { gracefulShutdown(t, srv) })
	return srv
}

func TestCaptureTelemetryEvent_QueuesEvent(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)

	telemetry := &fakeTelemetry{enabled: true}
	srv := newTelemetryTestServer(t, telemetry)

	req := httptest.NewRequestWithContext(t.Context(),
		http.MethodPost,
		"/api/v1/telemetry/events",
		strings.NewReader(`{"event":"app_loaded","properties":{"view":"pulls","distinct_id":"ignored"}}`),
	)
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()

	srv.ServeHTTP(rr, req)

	assert.Equal(http.StatusAccepted, rr.Code)
	assert.Equal("app_loaded", telemetry.event)
	assert.Equal("pulls", telemetry.properties["view"])
	assert.NotContains(telemetry.properties, "distinct_id")
	assert.True(telemetry.properties["$geoip_disable"].(bool))

	var body telemetryapi.TelemetryEventResponse
	err := json.NewDecoder(rr.Body).Decode(&body)
	require.NoError(err)
	assert.Equal("queued", body.Status)
}

func TestCaptureTelemetryEvent_ReturnsDisabledWhenTelemetryUnavailable(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)

	srv := newTelemetryTestServer(t, nil)
	req := httptest.NewRequestWithContext(t.Context(),
		http.MethodPost,
		"/api/v1/telemetry/events",
		strings.NewReader(`{"event":"app_loaded"}`),
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
