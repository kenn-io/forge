package server

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestOTelTraceableFiltersOnlyLongLivedStreams(t *testing.T) {
	tests := []struct {
		name     string
		basePath string
		method   string
		target   string
		upgrade  bool
		want     bool
	}{
		{name: "websocket", basePath: "/", method: http.MethodGet, target: "/ws/v1/terminal", upgrade: true, want: false},
		{name: "server events", basePath: "/", method: http.MethodGet, target: "/api/v1/events", want: false},
		{name: "prefixed server events", basePath: "/kenn-forge/", method: http.MethodGet, target: "/kenn-forge/api/v1/events", want: false},
		{name: "telemetry event", basePath: "/", method: http.MethodPost, target: "/api/v1/telemetry/events", want: true},
		{name: "roborev event stream", method: http.MethodGet, target: "/api/roborev/api/stream/events", want: false},
		{name: "roborev job output stream", method: http.MethodGet, target: "/api/roborev/api/job/output?job_id=7&stream=1", want: false},
		{name: "roborev job output snapshot", method: http.MethodGet, target: "/api/roborev/api/job/output?job_id=7", want: true},
		{name: "roborev raw job log", method: http.MethodGet, target: "/api/roborev/api/job/log?job_id=7", want: true},
		{name: "roborev sync stream", method: http.MethodPost, target: "/api/roborev/api/sync/now?stream=1", want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequestWithContext(t.Context(), tt.method, tt.target, nil)
			if tt.upgrade {
				req.Header.Set("Upgrade", "websocket")
			}
			assert.Equal(t, tt.want, otelTraceable(tt.basePath)(req))
		})
	}
}
