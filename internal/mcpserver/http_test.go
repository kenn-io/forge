package mcpserver

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRunHTTPRejectsNonLoopbackBind(t *testing.T) {
	t.Setenv("KENN_FORGE_MCP_TOKEN", "sekrit")
	s, err := New(Options{
		Transport:    "http",
		Addr:         "0.0.0.0:0",
		HTTPTokenEnv: "KENN_FORGE_MCP_TOKEN",
		Version:      "test",
	})
	require.NoError(t, err)

	err = s.RunHTTP(t.Context())

	require.Error(t, err)
	assert.Contains(t, err.Error(), "loopback")
}

func TestRunHTTPRequiresTokenEnv(t *testing.T) {
	tests := []struct {
		name string
		env  string
		set  bool
		want string
	}{
		{name: "missing flag", want: "--http-token-env"},
		{name: "unset variable", env: "MISSING_MCP_TOKEN", want: "unset or blank"},
		{name: "blank variable", env: "BLANK_MCP_TOKEN", set: true, want: "unset or blank"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.set {
				t.Setenv(tt.env, " \t")
			}
			s, err := New(Options{
				Transport:    "http",
				Addr:         "127.0.0.1:0",
				HTTPTokenEnv: tt.env,
				Version:      "test",
			})
			require.NoError(t, err)

			err = s.RunHTTP(t.Context())

			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.want)
		})
	}
}

func TestHTTPGuardChecks(t *testing.T) {
	assert := assert.New(t)
	const token = "sekrit-mcp-token"
	const daemonToken = "daemon-auth-token"
	const port = 8123
	nextCalls := 0
	s := &Server{}
	next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		nextCalls++
		w.WriteHeader(http.StatusNoContent)
	})
	handler := s.httpGuard(next, token, port)

	tests := []struct {
		name   string
		host   string
		auth   string
		origin string
		want   int
		pass   bool
	}{
		{name: "no authorization", host: "127.0.0.1:8123", want: http.StatusUnauthorized},
		{
			name: "wrong bearer", host: "127.0.0.1:8123",
			auth: "Bearer wrong", want: http.StatusUnauthorized,
		},
		{
			name: "host not loopback", host: "example.com:8123",
			auth: "Bearer " + token, want: http.StatusForbidden,
		},
		{
			name: "host wrong port", host: "127.0.0.1:8124",
			auth: "Bearer " + token, want: http.StatusForbidden,
		},
		{
			name: "localhost alias", host: "localhost:8123",
			auth: "Bearer " + token, want: http.StatusNoContent, pass: true,
		},
		{
			name: "ipv4 loopback", host: "127.0.0.1:8123",
			auth: "Bearer " + token, want: http.StatusNoContent, pass: true,
		},
		{
			name: "ipv6 loopback", host: "[::1]:8123",
			auth: "Bearer " + token, want: http.StatusNoContent, pass: true,
		},
		{
			name: "foreign origin", host: "127.0.0.1:8123",
			auth: "Bearer " + token, origin: "http://example.com:8123",
			want: http.StatusForbidden,
		},
		{
			name: "wrong origin scheme", host: "127.0.0.1:8123",
			auth: "Bearer " + token, origin: "https://127.0.0.1:8123",
			want: http.StatusForbidden,
		},
		{
			name: "matching loopback origin", host: "127.0.0.1:8123",
			auth: "Bearer " + token, origin: "http://localhost:8123",
			want: http.StatusNoContent, pass: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			before := nextCalls
			req := httptest.NewRequest(http.MethodPost, "http://127.0.0.1:8123/mcp", nil)
			req.Host = tt.host
			if tt.auth != "" {
				req.Header.Set("Authorization", tt.auth)
			}
			if tt.origin != "" {
				req.Header.Set("Origin", tt.origin)
			}
			w := httptest.NewRecorder()

			handler.ServeHTTP(w, req)
			resp := w.Result()
			t.Cleanup(func() {
				require.NoError(t, resp.Body.Close())
			})
			body, err := io.ReadAll(resp.Body)
			require.NoError(t, err)

			assert.Equal(tt.want, resp.StatusCode)
			assert.Empty(resp.Header.Get("Access-Control-Allow-Origin"))
			assert.NotContains(string(body), token)
			assert.NotContains(string(body), daemonToken)
			if tt.pass {
				assert.Equal(before+1, nextCalls)
			} else {
				assert.Equal(before, nextCalls)
			}
		})
	}
}

func TestRunHTTPServesTokenizedStreamableEndpoint(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	t.Setenv("KENN_FORGE_MCP_TOKEN", "sekrit")
	s, err := New(Options{
		Transport:    "http",
		Addr:         "127.0.0.1:0",
		HTTPTokenEnv: "KENN_FORGE_MCP_TOKEN",
		Version:      "test",
	})
	require.NoError(err)
	ctx, cancel := context.WithCancel(t.Context())
	errc := make(chan error, 1)
	go func() {
		errc <- s.RunHTTP(ctx)
	}()
	t.Cleanup(func() {
		cancel()
		require.NoError(<-errc)
	})

	endpoint := "http://" + waitForHTTPAddr(t, s)
	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, endpoint, nil)
	require.NoError(err)
	req.Header.Set("Accept", "text/event-stream")
	resp, err := http.DefaultClient.Do(req)
	require.NoError(err)
	assert.Equal(http.StatusUnauthorized, resp.StatusCode)
	require.NoError(resp.Body.Close())

	client := mcp.NewClient(&mcp.Implementation{Name: "kenn-forge-test-client", Version: "test"}, nil)
	session, err := client.Connect(t.Context(), &mcp.StreamableClientTransport{
		Endpoint:             endpoint,
		HTTPClient:           &http.Client{Transport: bearerRoundTripper{token: "sekrit"}},
		DisableStandaloneSSE: true,
	}, nil)
	require.NoError(err)
	tools, err := session.ListTools(t.Context(), nil)
	require.NoError(err)
	assert.NotEmpty(tools.Tools)
	require.NoError(session.Close())
}

type bearerRoundTripper struct {
	token string
	base  http.RoundTripper
}

func (rt bearerRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	base := rt.base
	if base == nil {
		base = http.DefaultTransport
	}
	req = req.Clone(req.Context())
	req.Header.Set("Authorization", "Bearer "+rt.token)
	return base.RoundTrip(req)
}

func waitForHTTPAddr(t *testing.T, s *Server) string {
	t.Helper()
	deadline := time.After(5 * time.Second)
	tick := time.NewTicker(10 * time.Millisecond)
	defer tick.Stop()
	for {
		addr := s.httpListenAddr()
		if strings.TrimSpace(addr) != "" {
			return addr
		}
		select {
		case <-deadline:
			require.Fail(t, "timed out waiting for HTTP listener")
		case <-tick.C:
		}
	}
}
