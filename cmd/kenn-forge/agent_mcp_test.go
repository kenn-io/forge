package main

import (
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/forge/internal/config"
	"go.kenn.io/forge/internal/mcpserver"
	"go.kenn.io/forge/internal/server"
	"go.kenn.io/forge/internal/server/authapi"
	"go.kenn.io/forge/internal/testutil/dbtest"
)

func TestAgentMCPWithoutCompanionListener(t *testing.T) {
	listener, httpServer, switcher, err := newAgentMCPHTTP(t.Context(), "test-agent-token")
	require.NoError(t, err)
	t.Cleanup(func() { _ = httpServer.Close() })
	served := make(chan error, 1)
	go func() { served <- httpServer.Serve(listener) }()
	t.Cleanup(func() { _ = httpServer.Close(); require.ErrorIs(t, <-served, http.ErrServerClosed) })

	// The backend's public Host/proxy policy must not affect the local agent listener.
	bind, err := config.ParseHostKey("192.0.2.10:8091")
	require.NoError(t, err)
	srv := server.New(dbtest.Open(t), nil, nil, "/forge", nil, server.ServerOptions{
		HostCheck:    authapi.HostCheckOptions{Bind: bind, TrustReverseProxy: true},
		DaemonAccess: authapi.DaemonAccessOptions{Token: "test-agent-token"},
	})
	mcp, err := mcpserver.New(mcpserver.Options{Backend: srv.MCPBackend(), Version: "test"})
	require.NoError(t, err)
	t.Cleanup(func() { _ = mcp.Close() })
	switcher.Swap(mcp.HTTPHandler())
	client := &http.Client{Transport: &http.Transport{Proxy: nil}}
	t.Cleanup(client.CloseIdleConnections)
	for _, tc := range []struct {
		name, token string
		forwarded   bool
		status      int
	}{
		{"agent", "test-agent-token", false, http.StatusOK},
		{"missing token", "", false, http.StatusUnauthorized},
		{"wrong token", "wrong-token", false, http.StatusUnauthorized},
		{"forwarded request", "test-agent-token", true, http.StatusForbidden},
	} {
		t.Run(tc.name, func(t *testing.T) {
			request, err := http.NewRequestWithContext(t.Context(), http.MethodPost, "http://"+listener.Addr().String()+"/mcp", strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/list","params":{}}`))
			require.NoError(t, err)
			request.Header.Set("Content-Type", "application/json")
			request.Header.Set("Accept", "application/json, text/event-stream")
			request.Header.Set("Authorization", "Bearer "+tc.token)
			if tc.forwarded {
				request.Header.Set("X-Forwarded-Host", "forge.example")
			}
			response, err := client.Do(request)
			require.NoError(t, err)
			defer response.Body.Close()
			assert.Equal(t, tc.status, response.StatusCode)
			data, err := io.ReadAll(response.Body)
			require.NoError(t, err)
			if tc.status == http.StatusOK {
				assert.Contains(t, string(data), "kenn_forge_")
			}
		})
	}
}
