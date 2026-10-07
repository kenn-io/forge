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
	"go.kenn.io/forge/internal/server/mcpapi"
	"go.kenn.io/forge/internal/testutil/dbtest"
	serverfake "go.kenn.io/forge/internal/testutil/serverfake"
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
	return newMainMCPTestServer(t, authapi.DaemonAccessOptions{
		Token: "local-secret", RequireAPIAuth: true,
		TailscaleServeEnabled: true,
		TailscaleServeUsers:   []string{"user@example.com"},
	}, true)
}

// newMainMCPTestServer serves the main listener with the given daemon access
// policy, optionally installing the MCP handler.
func newMainMCPTestServer(
	t *testing.T, access authapi.DaemonAccessOptions, withMCP bool,
) (*httptest.Server, string) {
	t.Helper()
	ts := httptest.NewUnstartedServer(nil)
	bind, err := config.ParseHostKey(ts.Listener.Addr().String())
	require.NoError(t, err)
	port, err := strconv.Atoi(bind.Port)
	require.NoError(t, err)
	publicHost := "forge.example.ts.net:" + bind.Port
	srv := New(dbtest.Open(t), nil, nil, "/", &config.Config{
		Host: "127.0.0.1", Port: port, AllowedHosts: []string{publicHost},
	}, ServerOptions{DaemonAccess: access})
	if withMCP {
		mcp, err := mcpserver.New(mcpserver.Options{Backend: srv.MCPBackend(), Version: "test"})
		require.NoError(t, err)
		t.Cleanup(func() { _ = mcp.Close() })
		srv.SetTailnetMCPHandler(mcp.TailnetHTTPHandler())
	}
	ts.Config.Handler = srv
	ts.Start()
	t.Cleanup(ts.Close)
	return ts, publicHost
}

func TestTailnetMCPAcceptsAllowedTailscaleServeUserWithoutBearer(t *testing.T) {
	serverfake.RunParallelServerTest(t)
	ts, publicHost := newTailnetMCPTestServer(t)
	sameOrigin := "https://" + publicHost

	tests := []struct {
		name     string
		login    string
		bearer   string
		origin   string
		expected int
	}{
		{name: "allowed user", login: "user@example.com", expected: http.StatusOK},
		{name: "allowed user from same origin", login: "user@example.com", origin: sameOrigin, expected: http.StatusOK},
		{name: "missing identity", expected: http.StatusUnauthorized},
		{name: "other user", login: "other@example.com", expected: http.StatusUnauthorized},
		{name: "cross-origin page", login: "user@example.com", origin: "https://attacker.example", expected: http.StatusForbidden},
		{name: "allowed user with wrong bearer", login: "user@example.com", bearer: "wrong", expected: http.StatusOK},
		{name: "daemon bearer without identity", bearer: "local-secret", expected: http.StatusOK},
		{name: "daemon bearer with other user", login: "other@example.com", bearer: "local-secret", expected: http.StatusOK},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			response := tailnetMCPInitialize(t, ts.URL, func(request *http.Request) {
				request.Host = publicHost
				if test.login != "" {
					request.Header.Set("Tailscale-User-Login", test.login)
				}
				if test.bearer != "" {
					request.Header.Set("Authorization", "Bearer "+test.bearer)
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
	serverfake.RunParallelServerTest(t)
	ts := newAuthTestServer(t, "secret-token")
	srv := ts.Config.Handler.(*Server)
	mcp, err := mcpserver.New(mcpserver.Options{Backend: srv.MCPBackend(), Version: "test"})
	require.NoError(t, err)
	t.Cleanup(func() { _ = mcp.Close() })
	srv.SetTailnetMCPHandler(mcp.TailnetHTTPHandler())

	response := tailnetMCPInitialize(t, ts.URL, func(request *http.Request) {
		request.Header.Set("Tailscale-User-Login", "user@example.com")
	})

	assert.Equal(t, http.StatusUnauthorized, response.StatusCode)
}

func TestMainListenerMCPAcceptsDaemonBearer(t *testing.T) {
	for _, requireAuth := range []bool{true, false} {
		t.Run("require_auth="+strconv.FormatBool(requireAuth), func(t *testing.T) {
			ts, publicHost := newMainMCPTestServer(t, authapi.DaemonAccessOptions{
				Token: "local-secret", RequireAPIAuth: requireAuth,
			}, true)

			tests := []struct {
				name     string
				header   string
				origin   string
				expected int
			}{
				{name: "valid bearer", header: "Bearer local-secret", expected: http.StatusOK},
				{name: "same-origin bearer", header: "Bearer local-secret", origin: "https://" + publicHost, expected: http.StatusOK},
				{name: "missing bearer", expected: http.StatusUnauthorized},
				{name: "wrong bearer", header: "Bearer other-secret", expected: http.StatusUnauthorized},
				{name: "basic auth", header: "Basic local-secret", expected: http.StatusUnauthorized},
				{name: "cross-origin bearer", header: "Bearer local-secret", origin: "https://attacker.example", expected: http.StatusForbidden},
				{name: "plain-http origin bearer", header: "Bearer local-secret", origin: "http://" + publicHost, expected: http.StatusForbidden},
			}
			for _, test := range tests {
				t.Run(test.name, func(t *testing.T) {
					response := tailnetMCPInitialize(t, ts.URL, func(request *http.Request) {
						request.Host = publicHost
						if test.header != "" {
							request.Header.Set("Authorization", test.header)
						}
						if test.origin != "" {
							request.Header.Set("Origin", test.origin)
						}
					})
					assert.Equal(t, test.expected, response.StatusCode)
				})
			}
		})
	}
}

func TestMainListenerMCPRejectsBearerWhenDaemonTokenEmpty(t *testing.T) {
	ts, publicHost := newMainMCPTestServer(t, authapi.DaemonAccessOptions{}, true)

	response := tailnetMCPInitialize(t, ts.URL, func(request *http.Request) {
		request.Host = publicHost
		request.Header.Set("Authorization", "Bearer ")
	})

	assert.Equal(t, http.StatusUnauthorized, response.StatusCode)
}

func TestMainListenerMCPRejectsDisallowedHostBeforeBearer(t *testing.T) {
	ts, _ := newMainMCPTestServer(t, authapi.DaemonAccessOptions{Token: "local-secret"}, true)

	response := tailnetMCPInitialize(t, ts.URL, func(request *http.Request) {
		request.Host = "rebound.example:80"
		request.Header.Set("Authorization", "Bearer local-secret")
	})

	assert.Equal(t, http.StatusForbidden, response.StatusCode)
}

func TestMainListenerMCPAcceptsDaemonBearerUnderBasePath(t *testing.T) {
	ts := httptest.NewUnstartedServer(nil)
	bind, err := config.ParseHostKey(ts.Listener.Addr().String())
	require.NoError(t, err)
	port, err := strconv.Atoi(bind.Port)
	require.NoError(t, err)
	srv := New(dbtest.Open(t), nil, nil, "/forge/", &config.Config{
		Host: "127.0.0.1", Port: port,
	}, ServerOptions{DaemonAccess: authapi.DaemonAccessOptions{Token: "local-secret"}})
	mcp, err := mcpserver.New(mcpserver.Options{Backend: srv.MCPBackend(), Version: "test"})
	require.NoError(t, err)
	t.Cleanup(func() { _ = mcp.Close() })
	srv.SetTailnetMCPHandler(mcp.TailnetHTTPHandler())
	ts.Config.Handler = srv
	ts.Start()
	t.Cleanup(ts.Close)

	accepted := tailnetMCPInitialize(t, ts.URL+"/forge", func(request *http.Request) {
		request.Header.Set("Authorization", "Bearer local-secret")
	})
	rejected := tailnetMCPInitialize(t, ts.URL+"/forge", nil)

	assert.Equal(t, http.StatusOK, accepted.StatusCode)
	assert.Equal(t, http.StatusUnauthorized, rejected.StatusCode)
}

func TestMainListenerMCPWithoutHandlerReturnsNotFound(t *testing.T) {
	ts, publicHost := newMainMCPTestServer(t, authapi.DaemonAccessOptions{Token: "local-secret"}, false)

	response := tailnetMCPInitialize(t, ts.URL, func(request *http.Request) {
		request.Host = publicHost
		request.Header.Set("Authorization", "Bearer local-secret")
	})

	assert.Equal(t, http.StatusNotFound, response.StatusCode)
}

func newSharedPortMCPTestServer(
	t *testing.T, trustReverseProxy bool,
) (*httptest.Server, config.HostKey, string) {
	t.Helper()
	ts := httptest.NewUnstartedServer(nil)
	bind, err := config.ParseHostKey(ts.Listener.Addr().String())
	require.NoError(t, err)
	port, err := strconv.Atoi(bind.Port)
	require.NoError(t, err)
	publicHost := "forge.example.ts.net:" + bind.Port
	srv := New(dbtest.Open(t), nil, nil, "/forge/", &config.Config{
		Host: "127.0.0.1", Port: port, AllowedHosts: []string{publicHost},
		TrustReverseProxy: trustReverseProxy,
	}, ServerOptions{DaemonAccess: authapi.DaemonAccessOptions{Token: "local-secret"}})
	mcp, err := mcpserver.New(mcpserver.Options{Backend: srv.MCPBackend(), Version: "test"})
	require.NoError(t, err)
	t.Cleanup(func() { _ = mcp.Close() })
	srv.SetTailnetMCPHandler(mcp.TailnetHTTPHandler())
	srv.SetLocalMCPHandler(mcp.HTTPHandler(), mcpapi.MCPHTTPGuardOptions{
		Bind: bind, Token: "local-secret",
	})
	ts.Config.Handler = srv
	ts.Start()
	t.Cleanup(ts.Close)
	return ts, bind, publicHost
}

func TestMainListenerMCPAppliesCompanionPolicyWhenSharingPort(t *testing.T) {
	serverfake.RunParallelServerTest(t)
	ts, bind, publicHost := newSharedPortMCPTestServer(t, false)

	tests := []struct {
		name     string
		host     string
		headers  map[string]string
		expected int
	}{
		{name: "loopback without bearer", expected: http.StatusOK},
		{name: "loopback origin", headers: map[string]string{"Origin": "http://" + bind.String()}, expected: http.StatusOK},
		{name: "loopback bearer with loopback origin", headers: map[string]string{
			"Authorization": "Bearer local-secret", "Origin": "http://" + bind.String(),
		}, expected: http.StatusOK},
		{name: "loopback cross-origin page", headers: map[string]string{"Origin": "http://attacker.example"}, expected: http.StatusForbidden},
		{name: "public host without bearer", host: publicHost, expected: http.StatusUnauthorized},
		{name: "forwarded without bearer", headers: map[string]string{"X-Forwarded-For": "192.0.2.1"}, expected: http.StatusUnauthorized},
		{name: "public host with bearer", host: publicHost, headers: map[string]string{"Authorization": "Bearer local-secret"}, expected: http.StatusOK},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			response := tailnetMCPInitialize(t, ts.URL+"/forge", func(request *http.Request) {
				if test.host != "" {
					request.Host = test.host
				}
				for name, value := range test.headers {
					request.Header.Set(name, value)
				}
			})
			assert.Equal(t, test.expected, response.StatusCode)
		})
	}
}

// Reverse-proxy mode requires forwarding headers on the main listener, which
// the companion forbids; direct loopback MCP must not need them.
func TestMainListenerSharedMCPWorksWithTrustedReverseProxy(t *testing.T) {
	serverfake.RunParallelServerTest(t)
	ts, _, _ := newSharedPortMCPTestServer(t, true)

	response := tailnetMCPInitialize(t, ts.URL+"/forge", nil)

	assert.Equal(t, http.StatusOK, response.StatusCode)
}
