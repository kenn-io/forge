//go:build !kit_posthog_disabled

package settingsservertest

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"go.kenn.io/forge/internal/testutil/serverfake"
	"go.kenn.io/forge/internal/testutil/servertest"
	"go.kenn.io/kit/telemetry/posthog"
)

func TestCaptureTelemetryEvent_CoreActionInvalidProperty(t *testing.T) {
	serverfake.RunParallelServerTest(t)
	for _, event := range []string{"pr_viewed", "workspace_opened"} {
		t.Run(event, func(t *testing.T) {
			reporter := &serverfake.FakeTelemetry{EnabledValue: true, ReportError: posthog.ErrInvalidProperty}
			srv := servertest.NewTelemetryTestServer(t, reporter)
			req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/api/v1/telemetry/events", strings.NewReader(`{"event":"`+event+`","properties":{}}`))
			req.Header.Set("Content-Type", "application/json")
			response := httptest.NewRecorder()
			srv.ServeHTTP(response, req)
			require.Equal(t, http.StatusBadRequest, response.Code, response.Body.String())
			require.Contains(t, response.Body.String(), "unsupported or missing telemetry property")
		})
	}
}
