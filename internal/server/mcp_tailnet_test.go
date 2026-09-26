package server

import (
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"go.kenn.io/forge/internal/config"
	"go.kenn.io/forge/internal/mcpserver"
	"go.kenn.io/forge/internal/server/authapi"
	"go.kenn.io/forge/internal/testutil/dbtest"
)

func tailnetMCPInitialize(
	t *testing.T, url string, decorate func(*http.Request),
) *http.Response {
	t.Helper()
	request, err := http.NewRequestWithContext(t.Context(), http.MethodPost, url+"/mcp", strings.NewReader(
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{`+
			`"protocolVersion":"2025-06-18","capabilities":{},`+
			`"clientInfo":{"name":"test","version":"1"}}}`,
	))
	require.NoError(t, err)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/json, text/event-stream")
	if decorate != nil {
		decorate(request)
	}
	response, err := http.DefaultClient.Do(request)
	require.NoError(t, err)
	t.Cleanup(func() { response.Body.Close() })
	return response
}

// newTailnetMCPTestServer serves the main listener on loopback with a public
// allowed host, matching how Tailscale Serve reaches Forge.
func newTailnetMCPTestServer(t *testing.T) (*httptest.Server, string) {
	t.Helper()
	ts := httptest.NewUnstartedServer(nil)
	bind, err := config.ParseHostKey(ts.Listener.Addr().String())
	require.NoError(t, err)
	port, err := strconv.Atoi(bind.Port)
	require.NoError(t, err)
	publicHost := "forge.example.ts.net:" + bind.Port
	srv := New(dbtest.Open(t), nil, nil, "/", &config.Config{
		Host: "127.0.0.1", Port: port, AllowedHosts: []string{publicHost},
	}, ServerOptions{
		DaemonAccess: authapi.DaemonAccessOptions{
			Token: "local-secret", RequireAPIAuth: true,
			TailscaleServeEnabled: true,
			TailscaleServeUsers:   []string{"user@example.com"},
		},
	})
	mcp, err := mcpserver.New(mcpserver.Options{Backend: srv.MCPBackend(), Version: "test"})
	require.NoError(t, err)
	t.Cleanup(func() { _ = mcp.Close() })
	srv.SetTailnetMCPHandler(mcp.TailnetHTTPHandler())
	ts.Config.Handler = srv
	ts.Start()
	t.Cleanup(ts.Close)
	return ts, publicHost
}

func TestTailnetMCPAcceptsAllowedTailscaleServeUserWithoutBearer(t *testing.T) {
	ts, publicHost := newTailnetMCPTestServer(t)
	sameOrigin := "https://" + publicHost

	tests := []struct {
		name     string
		login    string
		origin   string
		expected int
	}{
		{name: "allowed user", login: "user@example.com", expected: http.StatusOK},
		{name: "allowed user from same origin", login: "user@example.com", origin: sameOrigin, expected: http.StatusOK},
		{name: "missing identity", expected: http.StatusUnauthorized},
		{name: "other user", login: "other@example.com", expected: http.StatusUnauthorized},
		{name: "cross-origin page", login: "user@example.com", origin: "https://attacker.example", expected: http.StatusForbidden},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			response := tailnetMCPInitialize(t, ts.URL, func(request *http.Request) {
				request.Host = publicHost
				if test.login != "" {
					request.Header.Set("Tailscale-User-Login", test.login)
				}
				if test.origin != "" {
					request.Header.Set("Origin", test.origin)
				}
			})
			assert.Equal(t, test.expected, response.StatusCode)
		})
	}
}

func TestTailnetMCPRequiresTailscaleIdentityMode(t *testing.T) {
	ts := newAuthTestServer(t, "secret-token")
	srv := ts.Config.Handler.(*Server)
	mcp, err := mcpserver.New(mcpserver.Options{Backend: srv.MCPBackend(), Version: "test"})
	require.NoError(t, err)
	t.Cleanup(func() { _ = mcp.Close() })
	srv.SetTailnetMCPHandler(mcp.TailnetHTTPHandler())

	response := tailnetMCPInitialize(t, ts.URL, func(request *http.Request) {
		request.Header.Set("Tailscale-User-Login", "user@example.com")
	})

	assert.NotEqual(t, http.StatusOK, response.StatusCode)
}
