package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
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

func TestCaptureTelemetryEvent_QueuesEvent(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)

	telemetry := &fakeTelemetry{enabled: true}
	srv := New(
		openTestDB(t), nil, nil, "/", nil,
		ServerOptions{Telemetry: telemetry},
	)

	req := httptest.NewRequest(
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

	var body telemetryEventResponse
	err := json.NewDecoder(rr.Body).Decode(&body)
	require.NoError(err)
	assert.Equal("queued", body.Status)
}

func TestCaptureTelemetryEvent_ReturnsDisabledWhenTelemetryUnavailable(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)

	srv := New(openTestDB(t), nil, nil, "/", nil, ServerOptions{})
	req := httptest.NewRequest(
		http.MethodPost,
		"/api/v1/telemetry/events",
		strings.NewReader(`{"event":"app_loaded"}`),
	)
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()

	srv.ServeHTTP(rr, req)

	assert.Equal(http.StatusAccepted, rr.Code)

	var body telemetryEventResponse
	err := json.NewDecoder(rr.Body).Decode(&body)
	require.NoError(err)
	assert.Equal("disabled", body.Status)
}

func TestCaptureTelemetryEvent_RejectsMissingEvent(t *testing.T) {
	assert := assert.New(t)

	srv := New(openTestDB(t), nil, nil, "/", nil, ServerOptions{})
	req := httptest.NewRequest(
		http.MethodPost,
		"/api/v1/telemetry/events",
		strings.NewReader(`{"event":"   ","properties":{"view":"pulls"}}`),
	).WithContext(context.Background())
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()

	srv.ServeHTTP(rr, req)

	assert.Equal(http.StatusBadRequest, rr.Code)
	assert.Contains(rr.Body.String(), "telemetry event is required")
}

func TestCaptureTelemetryEvent_RejectsUnsupportedEvent(t *testing.T) {
	assert := assert.New(t)

	srv := New(openTestDB(t), nil, nil, "/", nil, ServerOptions{})
	req := httptest.NewRequest(
		http.MethodPost,
		"/api/v1/telemetry/events",
		strings.NewReader(`{"event":"repo_opened","properties":{"view":"pulls"}}`),
	)
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()

	srv.ServeHTTP(rr, req)

	assert.Equal(http.StatusBadRequest, rr.Code)
	assert.Contains(rr.Body.String(), "unsupported telemetry event")
}
