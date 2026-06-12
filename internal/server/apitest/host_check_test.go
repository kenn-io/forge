package apitest

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	Assert "github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/middleman/internal/apiclient"
	"go.kenn.io/middleman/internal/apiclient/generated"
	"go.kenn.io/middleman/internal/config"
	"go.kenn.io/middleman/internal/db"
	ghclient "go.kenn.io/middleman/internal/github"
	"go.kenn.io/middleman/internal/server"
	"go.kenn.io/middleman/internal/testutil/dbtest"
)

// TestHostValidationE2E exercises the Host validation middleware
// through the full apitest stack (full Huma router, SQLite) with
// an explicit HostCheckOptions override. The override bypasses the
// test-friendly fallback (AllowLoopbackAnyPort) so the test pins
// the production contract: only the configured bind and the
// loopback synonyms at the bind port are accepted; the
// `middleman.test` hostname the apitest helpers normally use is
// NOT in the allowlist.
//
// Case 1: a request whose Host is attacker.example:8091 is
// rejected with the documented JSON 403 body before any handler
// runs (the PR list is seeded but never reached, since the
// returned body contains the host validation error, not pulls).
//
// Case 2: a request whose Host matches the configured bind
// (127.0.0.1:8091) reaches the handler and returns 200 with the
// seeded PR list.
func TestHostValidationE2E(t *testing.T) {
	srv, database := setupHostValidationServer(t)
	seedPR(t, database, "acme", "widget", 1)

	t.Run("rejects DNS-rebound hostname", func(t *testing.T) {
		client := newHostValidationClient(t, srv, "http://attacker.example:8091")
		resp, err := client.HTTP.ListPullsWithResponse(t.Context(), nil)
		require.NoError(t, err)
		require.Equal(t, http.StatusForbidden, resp.StatusCode())

		var payload struct {
			Error string `json:"error"`
		}
		require.NoError(t, json.Unmarshal(resp.Body, &payload))
		assert := Assert.New(t)
		assert.Contains(payload.Error, "allowed_hosts")
		assert.Contains(payload.Error, "trust_reverse_proxy")
	})

	t.Run("accepts configured bind", func(t *testing.T) {
		require := require.New(t)
		client := newHostValidationClient(t, srv, "http://127.0.0.1:8091")
		resp, err := client.HTTP.ListPullsWithResponse(t.Context(), nil)
		require.NoError(err)
		require.Equal(http.StatusOK, resp.StatusCode())
		require.NotNil(resp.JSON200)
		require.Len(*resp.JSON200, 1)
		assert := Assert.New(t)
		assert.Equal("acme", (*resp.JSON200)[0].RepoOwner)
		assert.Equal("widget", (*resp.JSON200)[0].RepoName)
		assert.EqualValues(1, (*resp.JSON200)[0].Number)
	})
}

func TestHostValidationUsesConfigDerivedTrustedProxyE2E(t *testing.T) {
	srv, database := setupHostValidationServerFromConfig(t, `host = "127.0.0.1"
port = 8091
allowed_hosts = ["proxy.local:8091", "middleman.example"]
trust_reverse_proxy = true
`)
	seedPR(t, database, "acme", "widget", 1)

	t.Run("accepts allowed forwarded host from config", func(t *testing.T) {
		require := require.New(t)
		client := newHostValidationClient(t, srv, "http://proxy.local:8091")
		resp, err := client.HTTP.ListPullsWithResponse(
			t.Context(), nil,
			requestHeader("X-Forwarded-Host", "middleman.example"),
		)
		require.NoError(err)
		require.Equal(http.StatusOK, resp.StatusCode())
		require.NotNil(resp.JSON200)
		require.Len(*resp.JSON200, 1)
	})

	t.Run("rejects disallowed forwarded host from config", func(t *testing.T) {
		client := newHostValidationClient(t, srv, "http://proxy.local:8091")
		resp, err := client.HTTP.ListPullsWithResponse(
			t.Context(), nil,
			requestHeader("X-Forwarded-Host", "attacker.example"),
		)
		require.NoError(t, err)
		require.Equal(t, http.StatusForbidden, resp.StatusCode())
	})
}

func TestHostValidationTrustedProxyE2E(t *testing.T) {
	srv, database := setupHostValidationServer(t, server.HostCheckOptions{
		Bind: config.HostKey{Host: "127.0.0.1", Port: "8091"},
		Allowed: []config.HostKey{
			{Host: "proxy.local", Port: "8091"},
			{Host: "middleman.example", Port: ""},
		},
		TrustReverseProxy: true,
	})
	seedPR(t, database, "acme", "widget", 1)

	t.Run("accepts allowed forwarded public host", func(t *testing.T) {
		require := require.New(t)
		client := newHostValidationClient(t, srv, "http://proxy.local:8091")
		resp, err := client.HTTP.ListPullsWithResponse(
			t.Context(), nil,
			requestHeader("X-Forwarded-Host", "middleman.example"),
		)
		require.NoError(err)
		require.Equal(http.StatusOK, resp.StatusCode())
		require.NotNil(resp.JSON200)
		require.Len(*resp.JSON200, 1)
	})

	t.Run("rejects disallowed forwarded public host", func(t *testing.T) {
		client := newHostValidationClient(t, srv, "http://proxy.local:8091")
		resp, err := client.HTTP.ListPullsWithResponse(
			t.Context(), nil,
			requestHeader("X-Forwarded-Host", "attacker.example"),
		)
		require.NoError(t, err)
		require.Equal(t, http.StatusForbidden, resp.StatusCode())
	})

	t.Run("rejects mismatched forwarded headers", func(t *testing.T) {
		client := newHostValidationClient(t, srv, "http://proxy.local:8091")
		resp, err := client.HTTP.ListPullsWithResponse(
			t.Context(), nil,
			requestHeader("X-Forwarded-Host", "middleman.example"),
			requestHeader("Forwarded", "host=attacker.example"),
		)
		require.NoError(t, err)
		require.Equal(t, http.StatusForbidden, resp.StatusCode())
	})
}

// setupHostValidationServer builds a Server with an explicit
// HostCheckOptions so the production contract — strict bind match
// plus no any-port relaxation — is what the test exercises.
func setupHostValidationServer(t *testing.T, opts ...server.HostCheckOptions) (*server.Server, *db.DB) {
	t.Helper()
	database := dbtest.Open(t)
	syncer := ghclient.NewSyncer(nil, database, nil, defaultTestRepos, time.Minute, nil, nil)
	t.Cleanup(syncer.Stop)
	hostCheck := server.HostCheckOptions{
		Bind: config.HostKey{Host: "127.0.0.1", Port: "8091"},
	}
	if len(opts) > 0 {
		hostCheck = opts[0]
	}
	srv := server.New(database, syncer, nil, "/", nil, server.ServerOptions{
		HostCheck: hostCheck,
	})
	return srv, database
}

func setupHostValidationServerFromConfig(t *testing.T, content string) (*server.Server, *db.DB) {
	t.Helper()
	cfgPath := writeHostValidationConfig(t, content)
	cfg, err := config.Load(cfgPath)
	require.NoError(t, err)

	database := dbtest.Open(t)
	syncer := ghclient.NewSyncer(nil, database, nil, defaultTestRepos, time.Minute, nil, nil)
	t.Cleanup(syncer.Stop)

	srv := server.NewWithConfig(database, syncer, nil, nil, cfg, cfgPath, server.ServerOptions{})
	return srv, database
}

func writeHostValidationConfig(t *testing.T, content string) string {
	t.Helper()
	path := t.TempDir() + "/config.toml"
	require.NoError(t, os.WriteFile(path, []byte(content), 0o600))
	return path
}

// newHostValidationClient builds an apiclient.Client whose base
// URL drives req.URL.Host (and therefore the server's req.Host)
// per the row. Reuses the apitest round-tripper pattern from
// setupTestClient but isolates this test from the default
// "middleman.test" base URL.
func newHostValidationClient(t *testing.T, srv *server.Server, baseURL string) *apiclient.Client {
	t.Helper()
	httpClient := &http.Client{
		Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			var body io.Reader = http.NoBody
			if req.Body != nil {
				payload, err := io.ReadAll(req.Body)
				if err != nil {
					return nil, err
				}
				_ = req.Body.Close()
				body = strings.NewReader(string(payload))
			}
			serverReq := httptest.NewRequest(req.Method, req.URL.String(), body)
			serverReq.Header = req.Header.Clone()
			if req.Method != http.MethodGet && serverReq.Header.Get("Content-Type") == "" {
				serverReq.Header.Set("Content-Type", "application/json")
			}
			serverReq = serverReq.WithContext(req.Context())

			rr := httptest.NewRecorder()
			srv.ServeHTTP(rr, serverReq)
			return rr.Result(), nil
		}),
	}
	client, err := apiclient.NewWithHTTPClient(baseURL, httpClient)
	require.NoError(t, err)
	return client
}

func requestHeader(name, value string) generated.RequestEditorFn {
	return func(_ context.Context, req *http.Request) error {
		req.Header.Set(name, value)
		return nil
	}
}
