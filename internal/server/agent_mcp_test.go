package server

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/forge/internal/config"
	"go.kenn.io/forge/internal/mcpserver"
	"go.kenn.io/forge/internal/testutil/dbtest"
)

func TestAgentMCPWithoutCompanionListener(t *testing.T) {
	ts := httptest.NewUnstartedServer(nil)
	bind, err := config.ParseHostKey(ts.Listener.Addr().String())
	require.NoError(t, err)
	port, err := strconv.Atoi(bind.Port)
	require.NoError(t, err)
	srv := New(dbtest.Open(t), nil, nil, "/", &config.Config{Host: "127.0.0.1", Port: port}, ServerOptions{
		AgentMCPURL:  "http://" + ts.Listener.Addr().String() + "/agent-mcp",
		DaemonAccess: DaemonAccessOptions{Token: "test-agent-token"},
	})
	mcp, err := mcpserver.New(mcpserver.Options{Backend: srv.MCPBackend(), Version: "test"})
	require.NoError(t, err)
	t.Cleanup(func() { _ = mcp.Close() })
	srv.SetAgentMCPHandler(mcp.HTTPHandler())
	ts.Config.Handler = srv
	ts.Start()
	t.Cleanup(ts.Close)
	for _, tc := range []struct {
		name, token string
		status      int
	}{
		{"agent", "test-agent-token", http.StatusOK},
		{"missing token", "", http.StatusUnauthorized},
		{"wrong token", "wrong-token", http.StatusUnauthorized},
	} {
		t.Run(tc.name, func(t *testing.T) {
			request, err := http.NewRequestWithContext(t.Context(), http.MethodPost, ts.URL+"/agent-mcp", strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/list","params":{}}`))
			require.NoError(t, err)
			request.Header.Set("Content-Type", "application/json")
			request.Header.Set("Accept", "application/json, text/event-stream")
			request.Header.Set("Authorization", "Bearer "+tc.token)
			response, err := ts.Client().Do(request)
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
