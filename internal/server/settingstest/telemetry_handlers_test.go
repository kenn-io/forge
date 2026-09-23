package settingstest

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"go.kenn.io/forge/internal/server"
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

func TestCaptureTelemetryEvent_RejectsMissingEvent(t *testing.T) {
	assert := assert.New(t)

	srv := newTelemetryTestServer(t, nil)
	req := httptest.NewRequestWithContext(t.Context(),
		http.MethodPost,
		"/api/v1/telemetry/events",
		strings.NewReader(`{"event":"   ","properties":{"view":"pulls"}}`),
	).WithContext(t.Context())
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()

	srv.ServeHTTP(rr, req)

	assert.Equal(http.StatusBadRequest, rr.Code)
	assert.Contains(rr.Body.String(), "telemetry event is required")
}

func TestCaptureTelemetryEvent_RejectsUnsupportedEvent(t *testing.T) {
	assert := assert.New(t)

	srv := newTelemetryTestServer(t, nil)
	req := httptest.NewRequestWithContext(t.Context(),
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
