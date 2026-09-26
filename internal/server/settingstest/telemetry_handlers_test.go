package settingstest

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	servertest "go.kenn.io/forge/internal/testutil/servertest"
)

func TestCaptureTelemetryEvent_RejectsMissingEvent(t *testing.T) {
	assert := assert.New(t)

	srv := servertest.NewTelemetryTestServer(t, nil)
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

	srv := servertest.NewTelemetryTestServer(t, nil)
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
