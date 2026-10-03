package settingstest

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	servertest "go.kenn.io/forge/internal/testutil/servertest"

	serverfake "go.kenn.io/forge/internal/testutil/serverfake"
)

func TestCaptureTelemetryEvent_RejectsMissingEvent(t *testing.T) {
	serverfake.RunParallelServerTest(t)
	assert := assert.New(t)

	srv := servertest.NewTelemetryTestServer(t, nil)
	req := httptest.NewRequestWithContext(t.Context(),
		http.MethodPost,
		"/api/v1/telemetry/events",
		strings.NewReader(`{"event":"   ","properties":{"surface":"web"}}`),
	).WithContext(t.Context())
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()

	srv.ServeHTTP(rr, req)

	assert.Equal(http.StatusBadRequest, rr.Code)
	assert.Contains(rr.Body.String(), "telemetry event is required")
}

func TestCaptureTelemetryEvent_RejectsUnsupportedEvent(t *testing.T) {
	serverfake.RunParallelServerTest(t)

	// daemon_active is allowlisted for the daemon only, so the UI can't send it.
	for _, event := range []string{"repo_opened", "daemon_active"} {
		t.Run(event, func(t *testing.T) {
			assert := assert.New(t)
			telemetry := &serverfake.FakeTelemetry{EnabledValue: true}
			srv := servertest.NewTelemetryTestServer(t, telemetry)
			req := httptest.NewRequestWithContext(t.Context(),
				http.MethodPost,
				"/api/v1/telemetry/events",
				strings.NewReader(`{"event":"`+event+`"}`),
			)
			req.Header.Set("Content-Type", "application/json")
			rr := httptest.NewRecorder()

			srv.ServeHTTP(rr, req)

			assert.Equal(http.StatusBadRequest, rr.Code)
			assert.Contains(rr.Body.String(), "unsupported telemetry event")
			assert.Empty(telemetry.Event)
		})
	}
}
