package server

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/forge/internal/config"
)

func TestMCPHTTPGuardEnforcesLoopbackAuthorityOriginAndAuthentication(t *testing.T) {
	bind, err := config.ParseHostKey("127.0.0.1:8092")
	require.NoError(t, err)
	const token = "daemon-token"

	tests := []struct {
		name        string
		requireAuth bool
		host        string
		remoteAddr  string
		origin      string
		headers     map[string]string
		wantStatus  int
	}{
		{
			name: "authentication disabled", host: "127.0.0.1:8092",
			remoteAddr: "127.0.0.1:42000", wantStatus: http.StatusNoContent,
		},
		{
			name: "authentication required", requireAuth: true, host: "127.0.0.1:8092",
			remoteAddr: "127.0.0.1:42000", wantStatus: http.StatusUnauthorized,
		},
		{
			name: "daemon token accepted", requireAuth: true, host: "localhost:8092",
			remoteAddr: "[::1]:42000", headers: map[string]string{"Authorization": "Bearer " + token},
			wantStatus: http.StatusNoContent,
		},
		{
			name: "different host port", host: "127.0.0.1:8093",
			remoteAddr: "127.0.0.1:42000", wantStatus: http.StatusForbidden,
		},
		{
			name: "malformed host", host: "localhost",
			remoteAddr: "127.0.0.1:42000", wantStatus: http.StatusForbidden,
		},
		{
			name: "non loopback peer", host: "127.0.0.1:8092",
			remoteAddr: "192.0.2.10:42000", wantStatus: http.StatusForbidden,
		},
		{
			name: "forwarded header", host: "127.0.0.1:8092",
			remoteAddr: "127.0.0.1:42000", headers: map[string]string{"Forwarded": "for=127.0.0.1"},
			wantStatus: http.StatusForbidden,
		},
		{
			name: "x forwarded header", host: "127.0.0.1:8092",
			remoteAddr: "127.0.0.1:42000", headers: map[string]string{"X-Forwarded-Host": "localhost:8092"},
			wantStatus: http.StatusForbidden,
		},
		{
			name: "matching origin", host: "127.0.0.1:8092",
			remoteAddr: "127.0.0.1:42000", origin: "http://localhost:8092",
			wantStatus: http.StatusNoContent,
		},
		{
			name: "different origin scheme", host: "127.0.0.1:8092",
			remoteAddr: "127.0.0.1:42000", origin: "https://localhost:8092",
			wantStatus: http.StatusForbidden,
		},
		{
			name: "different origin host", host: "127.0.0.1:8092",
			remoteAddr: "127.0.0.1:42000", origin: "http://example.test:8092",
			wantStatus: http.StatusForbidden,
		},
		{
			name: "different origin port", host: "127.0.0.1:8092",
			remoteAddr: "127.0.0.1:42000", origin: "http://localhost:8093",
			wantStatus: http.StatusForbidden,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusNoContent)
			})
			handler := NewMCPHTTPGuard(next, MCPHTTPGuardOptions{
				Bind: bind, Token: token, RequireAuth: tt.requireAuth,
			})
			req := httptest.NewRequest(http.MethodPost, "http://127.0.0.1:8092/mcp", nil)
			req.Host = tt.host
			req.RemoteAddr = tt.remoteAddr
			if tt.origin != "" {
				req.Header.Set("Origin", tt.origin)
			}
			for name, value := range tt.headers {
				req.Header.Set(name, value)
			}
			rec := httptest.NewRecorder()

			handler.ServeHTTP(rec, req)

			assert.Equal(t, tt.wantStatus, rec.Code)
		})
	}
}
