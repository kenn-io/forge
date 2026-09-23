package accesstest

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/forge/internal/config"
	"go.kenn.io/forge/internal/daemonruntime"
	"go.kenn.io/forge/internal/federation"
	"go.kenn.io/forge/internal/federationauth"
	"go.kenn.io/forge/internal/providerplane"
	"go.kenn.io/forge/internal/server"
	"go.kenn.io/forge/internal/server/authapi"
	"go.kenn.io/forge/internal/server/httpapi"
	"go.kenn.io/forge/internal/testutil/dbtest"
	"go.kenn.io/kit/daemon"
)

func newAuthTestServer(t *testing.T, token string) *httptest.Server {
	t.Helper()
	srv := server.New(dbtest.Open(t), nil, nil, "/", nil, server.ServerOptions{
		DaemonAccess: authapi.DaemonAccessOptions{
			Token: token, RequireAPIAuth: token != "",
		},
	})
	ts := httptest.NewServer(srv)
	t.Cleanup(ts.Close)
	return ts
}

func newTailscaleAuthTestServer(t *testing.T) (*httptest.Server, *server.Server) {
	t.Helper()
	srv := server.New(dbtest.Open(t), nil, nil, "/", nil, server.ServerOptions{
		DaemonAccess: authapi.DaemonAccessOptions{
			Token: "local-secret", RequireAPIAuth: true,
			TailscaleServeEnabled: true,
			TailscaleServeUsers:   []string{"user@example.com"},
		},
	})
	ts := httptest.NewServer(srv)
	t.Cleanup(ts.Close)
	return ts, srv
}

func newFederationAuthTestServer(
	t *testing.T, scopes ...federationauth.Scope,
) (*httptest.Server, *federationauth.Store, string) {
	t.Helper()
	store, err := federationauth.Open(
		filepath.Join(t.TempDir(), "federation-credentials.json"),
	)
	require.NoError(t, err)
	token, err := store.MintInbound(
		"fedcba9876543210fedcba9876543210", scopes,
	)
	require.NoError(t, err)
	srv := server.New(dbtest.Open(t), nil, nil, "/", nil, server.ServerOptions{
		DaemonAccess: authapi.DaemonAccessOptions{
			Token: "local-secret", RequireAPIAuth: true,
		},
		FederationCredentials: store,
	})
	ts := httptest.NewServer(srv)
	t.Cleanup(ts.Close)
	return ts, store, token
}

// TestDaemonPingContract protects authenticated readiness and the private
// identity proof used before lifecycle discovery trusts a recorded endpoint.
func TestDaemonPingContract(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	ts := httptest.NewUnstartedServer(nil)
	bind, err := config.ParseHostKey(ts.Listener.Addr().String())
	require.NoError(err)
	identity, err := daemonruntime.NewIdentity(
		ts.Listener.Addr(), daemonruntime.IdentityOptions{
			Version: "v-test", DataDir: t.TempDir(),
			ConfigPath: filepath.Join(t.TempDir(), "config.toml"), RequireAuth: true,
		},
	)
	require.NoError(err)
	proof, err := daemon.NewProof([]byte("secret-token"))
	require.NoError(err)
	proofHandler, err := proof.NewPingHandler(identity.Record)
	require.NoError(err)
	srv := server.New(dbtest.Open(t), nil, nil, "/", nil, server.ServerOptions{
		DaemonAccess: authapi.DaemonAccessOptions{
			Token: "secret-token", RequireAPIAuth: true,
			ProofHandler: proofHandler,
		},
		HostCheck: authapi.HostCheckOptions{
			Bind: bind, Allowed: []config.HostKey{{Host: "forge.example.test"}},
			TrustReverseProxy: true,
		},
	})
	srv.SetBuildInfo(server.BuildInfo{Name: "kenn-forge", Version: "v-test"})
	ts.Config.Handler = srv
	ts.Start()
	t.Cleanup(ts.Close)

	unauthorized := authGet(t, ts, "/api/ping", func(r *http.Request) {
		r.Header.Set("X-Forwarded-Host", "forge.example.test")
	})
	t.Cleanup(func() {
		if unauthorized != nil && unauthorized.Body != nil {
			_ = unauthorized.Body.Close()
		}
	})
	assert.Equal(http.StatusUnauthorized, unauthorized.StatusCode)
	response := authGet(t, ts, "/api/ping", func(r *http.Request) {
		r.Header.Set("Authorization", "Bearer secret-token")
	})
	t.Cleanup(func() {
		if response != nil && response.Body != nil {
			_ = response.Body.Close()
		}
	})
	require.Equal(http.StatusOK, response.StatusCode)
	var ping daemon.PingInfo
	require.NoError(json.NewDecoder(response.Body).Decode(&ping))
	assert.Equal(daemon.PingInfo{
		OK: true, Service: daemonruntime.Service,
		Version: "v-test", PID: os.Getpid(),
	}, ping)

	_, err = proof.Probe(t.Context(), identity.Record, daemon.ProbeOptions{
		Path: daemonruntime.ProofPingPath,
	})
	require.NoError(err)

	forwarded := authGet(t, ts, daemonruntime.ProofPingPath, func(r *http.Request) {
		r.Header.Set("X-Forwarded-Host", "forge.example.test")
	})
	t.Cleanup(func() {
		if forwarded != nil && forwarded.Body != nil {
			_ = forwarded.Body.Close()
		}
	})
	assert.Equal(http.StatusForbidden, forwarded.StatusCode)
}

func authGet(
	t *testing.T, ts *httptest.Server, path string,
	decorate func(*http.Request),
) *http.Response {
	t.Helper()
	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, ts.URL+path, nil)
	require.NoError(t, err)
	if decorate != nil {
		decorate(req)
	}
	client := &http.Client{
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	resp, err := client.Do(req)
	require.NoError(t, err)
	t.Cleanup(func() { resp.Body.Close() })
	return resp
}

// TestAPIAuthGatesAPIRoutes pins the gate: with a token configured,
// API routes 401 (problem+json, unauthorized code) without a
// credential and serve normally with the bearer header.
func TestAPIAuthGatesAPIRoutes(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	ts := newAuthTestServer(t, "secret-token")

	resp := authGet(t, ts, "/api/v1/snapshot", nil)
	t.Cleanup(func() {
		if resp != nil && resp.Body != nil {
			_ = resp.Body.Close()
		}
	})
	require.Equal(http.StatusUnauthorized, resp.StatusCode)
	assert.Equal(`Bearer realm="kenn-forge"`,
		resp.Header.Get("WWW-Authenticate"))
	var problem struct {
		Code string `json:"code"`
	}
	require.NoError(json.NewDecoder(resp.Body).Decode(&problem))
	assert.Equal("unauthorized", problem.Code)

	resp = authGet(t, ts, "/api/v1/snapshot", func(r *http.Request) {
		r.Header.Set("Authorization", "Bearer secret-token")
	})
	t.Cleanup(func() {
		if resp != nil && resp.Body != nil {
			_ = resp.Body.Close()
		}
	})
	assert.Equal(http.StatusOK, resp.StatusCode)

	resp = authGet(t, ts, "/api/v1/snapshot", func(r *http.Request) {
		r.Header.Set("Authorization", "Bearer wrong")
	})
	t.Cleanup(func() {
		if resp != nil && resp.Body != nil {
			_ = resp.Body.Close()
		}
	})
	assert.Equal(http.StatusUnauthorized, resp.StatusCode)

	resp = authGet(t, ts, "/api/v1/snapshot", func(r *http.Request) {
		r.Header.Set("Tailscale-User-Login", "user@example.com")
	})
	t.Cleanup(func() {
		if resp != nil && resp.Body != nil {
			_ = resp.Body.Close()
		}
	})
	assert.Equal(http.StatusUnauthorized, resp.StatusCode,
		"Tailscale identity is opt-in")
}

func TestTailscaleServeIdentityAuthorizesGatedTransports(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	ts, _ := newTailscaleAuthTestServer(t)

	response := authGet(t, ts, "/api/v1/snapshot", func(request *http.Request) {
		request.Header.Set("Tailscale-User-Login", " USER@EXAMPLE.COM ")
	})
	t.Cleanup(func() {
		if response != nil && response.Body != nil {
			_ = response.Body.Close()
		}
	})
	assert.Equal(http.StatusOK, response.StatusCode)

	request, err := http.NewRequestWithContext(
		t.Context(), http.MethodGet, ts.URL+"/api/v1/events", nil,
	)
	require.NoError(err)
	request.Header.Set("Tailscale-User-Login", "user@example.com")
	response, err = ts.Client().Do(request)
	require.NoError(err)
	assert.Equal(http.StatusOK, response.StatusCode)
	assert.Equal("text/event-stream", response.Header.Get("Content-Type"))
	response.Body.Close()

	response = authGet(t, ts, "/ws/v1/workspaces/ws-1/terminal", func(request *http.Request) {
		request.Header.Set("Tailscale-User-Login", "user@example.com")
		request.Header.Set("Origin", "https://"+request.URL.Host)
		request.Header.Set("Connection", "Upgrade")
		request.Header.Set("Upgrade", "websocket")
		request.Header.Set("Sec-WebSocket-Key", "dGhlIHNhbXBsZSBub25jZQ==")
		request.Header.Set("Sec-WebSocket-Version", "13")
	})
	t.Cleanup(func() {
		if response != nil && response.Body != nil {
			_ = response.Body.Close()
		}
	})
	assert.NotEqual(http.StatusUnauthorized, response.StatusCode)
}

func TestFederationCredentialTakesPrecedenceOverTailscaleServeIdentity(t *testing.T) {
	require := require.New(t)
	credentials, err := federationauth.Open(
		filepath.Join(t.TempDir(), "federation-credentials.json"),
	)
	require.NoError(err)
	const nodeID = "fedcba9876543210fedcba9876543210"
	token, err := credentials.MintInbound(
		nodeID, []federationauth.Scope{federationauth.ScopeEnrollmentActivate},
	)
	require.NoError(err)
	srv := server.New(dbtest.Open(t), nil, nil, "/", nil, server.ServerOptions{
		DaemonAccess: authapi.DaemonAccessOptions{
			Token: "local-secret", RequireAPIAuth: true,
			TailscaleServeEnabled: true,
			TailscaleServeUsers:   []string{"user@example.com"},
		},
		FederationCredentials: credentials,
		FederationSpokeID:     "0123456789abcdef0123456789abcdef",
	})
	ts := httptest.NewServer(srv)
	t.Cleanup(ts.Close)
	t.Cleanup(func() { gracefulShutdown(t, srv) })

	response := authGet(t, ts, "/api/v1/federation/identity", func(request *http.Request) {
		request.Header.Set("Authorization", "Bearer "+token)
		request.Header.Set(federationauth.NodeIDHeader, nodeID)
		request.Header.Set("Tailscale-User-Login", "user@example.com")
	})
	t.Cleanup(func() {
		if response != nil && response.Body != nil {
			_ = response.Body.Close()
		}
	})
	require.Equal(http.StatusOK, response.StatusCode)
	var identity struct {
		NodeID string `json:"node_id"`
	}
	require.NoError(json.NewDecoder(response.Body).Decode(&identity))
	assert.Equal(t, "0123456789abcdef0123456789abcdef", identity.NodeID)
}

// TestAPIAuthGatesTerminalWebSocketRoutes pins that the /ws/ terminal
// routes are gated alongside /api/. These open interactive shells, so an
// unauthenticated request must be rejected before routing; a valid
// credential clears the gate (the route itself may then 404 in this
// minimal server, but it is no longer a 401).
func TestAPIAuthGatesTerminalWebSocketRoutes(t *testing.T) {
	assert := assert.New(t)
	ts := newAuthTestServer(t, "secret-token")

	resp := authGet(t, ts, "/ws/v1/workspaces/ws-1/terminal", nil)
	t.Cleanup(func() {
		if resp != nil && resp.Body != nil {
			_ = resp.Body.Close()
		}
	})
	assert.Equal(http.StatusUnauthorized, resp.StatusCode,
		"unauthenticated terminal WebSocket requests must be rejected")

	resp = authGet(t, ts, "/ws/v1/workspaces/ws-1/terminal",
		func(r *http.Request) {
			r.Header.Set("Authorization", "Bearer secret-token")
		})
	t.Cleanup(func() {
		if resp != nil && resp.Body != nil {
			_ = resp.Body.Close()
		}
	})
	assert.NotEqual(http.StatusUnauthorized, resp.StatusCode,
		"a valid credential clears the gate")
}

// TestAPIAuthHealthAndAssetsStayOpen pins the exemptions: health
// probes (supervisors poll before reading the token file) and
// non-API paths (SPA assets) are not gated.
func TestAPIAuthHealthAndAssetsStayOpen(t *testing.T) {
	assert := assert.New(t)
	ts := newAuthTestServer(t, "secret-token")

	for _, path := range []string{"/healthz", "/livez"} {
		resp := authGet(t, ts, path, nil)
		t.Cleanup(func() {
			if resp != nil && resp.Body != nil {
				_ = resp.Body.Close()
			}
		})
		assert.Equal(http.StatusOK, resp.StatusCode, path)
	}
}

// TestAPIAuthCookieBootstrap pins the browser flow: loading any URL
// with ?auth_token=<token> sets the session cookie and redirects to
// the same URL without the token; the cookie then authorizes API
// requests; a wrong bootstrap token is rejected outright.
func TestAPIAuthCookieBootstrap(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	ts := newAuthTestServer(t, "secret-token")

	resp := authGet(t, ts, "/?auth_token=secret-token", nil)
	t.Cleanup(func() {
		if resp != nil && resp.Body != nil {
			_ = resp.Body.Close()
		}
	})
	require.Equal(http.StatusSeeOther, resp.StatusCode)
	assert.Equal("/", resp.Header.Get("Location"),
		"token must be stripped from the redirect target")
	cookies := resp.Cookies()
	require.Len(cookies, 1)
	assert.Equal("forge_auth", cookies[0].Name)
	assert.True(cookies[0].HttpOnly)

	resp = authGet(t, ts, "/api/v1/snapshot", func(r *http.Request) {
		r.AddCookie(cookies[0])
	})
	t.Cleanup(func() {
		if resp != nil && resp.Body != nil {
			_ = resp.Body.Close()
		}
	})
	assert.Equal(http.StatusOK, resp.StatusCode,
		"the bootstrap cookie authorizes API requests")

	resp = authGet(t, ts, "/?auth_token=wrong", nil)
	t.Cleanup(func() {
		if resp != nil && resp.Body != nil {
			_ = resp.Body.Close()
		}
	})
	assert.Equal(http.StatusForbidden, resp.StatusCode)
	assert.Empty(resp.Cookies())
}

// TestAPIAuthDisabledByDefault pins the default: with no token
// configured, behavior is unchanged and nothing is gated.
func TestAPIAuthDisabledByDefault(t *testing.T) {
	ts := newAuthTestServer(t, "")
	resp := authGet(t, ts, "/api/v1/snapshot", nil)
	t.Cleanup(func() {
		if resp != nil && resp.Body != nil {
			_ = resp.Body.Close()
		}
	})
	assert.Equal(t, http.StatusOK, resp.StatusCode)
}

func TestFederationAuthIsScopedIndependentlyOfLocalAuth(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	ts, _, token := newFederationAuthTestServer(t, federationauth.ScopeSnapshotRead)

	resp := authGet(t, ts, "/api/v1/snapshot/raw", func(r *http.Request) {
		r.Header.Set("Authorization", "Bearer "+token)
	})
	t.Cleanup(func() {
		if resp != nil && resp.Body != nil {
			_ = resp.Body.Close()
		}
	})
	assert.Equal(http.StatusOK, resp.StatusCode)

	resp = authGet(t, ts, "/api/v1/settings", func(r *http.Request) {
		r.Header.Set("Authorization", "Bearer "+token)
	})
	t.Cleanup(func() {
		if resp != nil && resp.Body != nil {
			_ = resp.Body.Close()
		}
	})
	require.Equal(http.StatusForbidden, resp.StatusCode)
	var problem httpapi.ProblemError
	require.NoError(json.NewDecoder(resp.Body).Decode(&problem))
	assert.Equal(httpapi.CodeForbidden, problem.Code)
	assert.Equal("federationRouteNotAllowed", problem.Details["reason"])
	assert.NotContains(problem.Details, "required_scope")

	resp = authGet(t, ts, "/api/v1/settings", func(r *http.Request) {
		r.Header.Set("Authorization", "Bearer local-secret")
	})
	t.Cleanup(func() {
		if resp != nil && resp.Body != nil {
			_ = resp.Body.Close()
		}
	})
	assert.NotEqual(http.StatusUnauthorized, resp.StatusCode)
	assert.NotEqual(http.StatusForbidden, resp.StatusCode)
}

func TestFederationAuthTreatsEscapedSlashAsOneRouteParameter(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	ts, _, token := newFederationAuthTestServer(t, federationauth.ScopeWorkspaceWrite)

	req, err := http.NewRequestWithContext(t.Context(),
		http.MethodPost,
		ts.URL+"/api/v1/issues/gitlab/group%2Fsubgroup/widget/7/workspace",
		strings.NewReader(`{}`),
	)
	require.NoError(err)
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := ts.Client().Do(req)
	require.NoError(err)
	t.Cleanup(func() { resp.Body.Close() })

	assert.NotEqual(http.StatusUnauthorized, resp.StatusCode)
	assert.NotEqual(http.StatusForbidden, resp.StatusCode,
		"an encoded nested owner must remain one authorized route parameter")
}

func TestFederationAuthRejectsInsufficientScopeAndSubjectMismatch(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	ts, _, token := newFederationAuthTestServer(t, federationauth.ScopeProviderRead)

	resp := authGet(t, ts, "/api/v1/snapshot/raw", func(r *http.Request) {
		r.Header.Set("Authorization", "Bearer "+token)
	})
	t.Cleanup(func() {
		if resp != nil && resp.Body != nil {
			_ = resp.Body.Close()
		}
	})
	require.Equal(http.StatusForbidden, resp.StatusCode)
	var problem httpapi.ProblemError
	require.NoError(json.NewDecoder(resp.Body).Decode(&problem))
	assert.Equal("federationScopeDenied", problem.Details["reason"])
	assert.Equal(string(federationauth.ScopeSnapshotRead), problem.Details["required_scope"])

	ts, _, token = newFederationAuthTestServer(t, federationauth.ScopeSnapshotRead)
	resp = authGet(t, ts, "/api/v1/snapshot/raw", func(r *http.Request) {
		r.Header.Set("Authorization", "Bearer "+token)
		r.Header.Set(federationauth.NodeIDHeader,
			"0123456789abcdef0123456789abcdef")
	})
	t.Cleanup(func() {
		if resp != nil && resp.Body != nil {
			_ = resp.Body.Close()
		}
	})
	require.Equal(http.StatusForbidden, resp.StatusCode)
	problem = httpapi.ProblemError{}
	require.NoError(json.NewDecoder(resp.Body).Decode(&problem))
	assert.Equal("federationSubjectMismatch", problem.Details["reason"])
}

func TestRevokedFederationCredentialFailsOnNextRequest(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	ts, store, token := newFederationAuthTestServer(t, federationauth.ScopeSnapshotRead)

	resp := authGet(t, ts, "/api/v1/snapshot/raw", func(r *http.Request) {
		r.Header.Set("Authorization", "Bearer "+token)
	})
	t.Cleanup(func() {
		if resp != nil && resp.Body != nil {
			_ = resp.Body.Close()
		}
	})
	require.Equal(http.StatusOK, resp.StatusCode)
	require.NoError(store.RevokeInbound(token))

	resp = authGet(t, ts, "/api/v1/snapshot/raw", func(r *http.Request) {
		r.Header.Set("Authorization", "Bearer "+token)
	})
	t.Cleanup(func() {
		if resp != nil && resp.Body != nil {
			_ = resp.Body.Close()
		}
	})
	assert.Equal(http.StatusUnauthorized, resp.StatusCode)
}

func TestFederationProviderAuthRequiresExactProtocolAndScope(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	ts, store, token := newFederationAuthTestServer(
		t, federationauth.ScopeProviderRead,
	)

	requestProviderRead := func(protocol string) *http.Response {
		return authGet(t, ts, "/api/v1/pulls", func(r *http.Request) {
			r.Header.Set("Authorization", "Bearer "+token)
			if protocol != "" {
				r.Header.Set(providerplane.ProtocolVersionHeader, protocol)
			}
		})
	}

	response := requestProviderRead("")
	t.Cleanup(func() {
		if response != nil && response.Body != nil {
			_ = response.Body.Close()
		}
	})
	require.Equal(http.StatusConflict, response.StatusCode)
	var problem httpapi.ProblemError
	require.NoError(json.NewDecoder(response.Body).Decode(&problem))
	assert.Equal("protocolMismatch", problem.Details["reason"])

	response = requestProviderRead("2")
	t.Cleanup(func() {
		if response != nil && response.Body != nil {
			_ = response.Body.Close()
		}
	})
	require.Equal(http.StatusConflict, response.StatusCode)

	response = requestProviderRead(providerplane.ProtocolVersionHeaderValue())
	t.Cleanup(func() {
		if response != nil && response.Body != nil {
			_ = response.Body.Close()
		}
	})
	assert.NotEqual(http.StatusUnauthorized, response.StatusCode)
	assert.NotEqual(http.StatusForbidden, response.StatusCode)
	assert.NotEqual(http.StatusConflict, response.StatusCode)

	write, err := http.NewRequestWithContext(t.Context(),
		http.MethodPost, ts.URL+"/api/v1/sync", strings.NewReader(`{}`),
	)
	require.NoError(err)
	write.Header.Set("Authorization", "Bearer "+token)
	write.Header.Set("Content-Type", "application/json")
	write.Header.Set(
		providerplane.ProtocolVersionHeader,
		providerplane.ProtocolVersionHeaderValue(),
	)
	writeResponse, err := ts.Client().Do(write)
	require.NoError(err)
	t.Cleanup(func() { writeResponse.Body.Close() })
	assert.Equal(http.StatusForbidden, writeResponse.StatusCode)

	require.NoError(store.RevokeInbound(token))
	response = requestProviderRead(providerplane.ProtocolVersionHeaderValue())
	t.Cleanup(func() {
		if response != nil && response.Body != nil {
			_ = response.Body.Close()
		}
	})
	assert.Equal(http.StatusUnauthorized, response.StatusCode)
}

func TestPreEnrollmentEndpointUsesOneTimeTokenInsteadOfLocalAPIAuth(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	credentials, err := federationauth.Open(
		filepath.Join(t.TempDir(), "credentials.json"),
	)
	require.NoError(err)
	enrollments, err := federation.Open(
		filepath.Join(t.TempDir(), "enrollments.json"), federation.StoreOptions{},
	)
	require.NoError(err)
	const hubID = "0123456789abcdef0123456789abcdef"
	oneTime, err := enrollments.CreateOneTimeToken(federation.Identity{
		NodeID: hubID, BaseURL: "https://hub.example",
	}, time.Now().Add(time.Minute))
	require.NoError(err)
	cfg := &config.Config{
		Host: "127.0.0.1", Port: 8091, API: config.API{RequireAuth: true},
		Fleet: config.Fleet{Enabled: true, Role: config.FleetRoleHub},
	}
	srv := server.New(dbtest.Open(t), nil, nil, "/", cfg, server.ServerOptions{
		DaemonAccess: authapi.DaemonAccessOptions{
			Token: "local-secret", RequireAPIAuth: true,
		},
		FederationCredentials:         credentials,
		FederationEnrollments:         enrollments,
		FederationSpokeID:             hubID,
		HostCheckAllowLoopbackAnyPort: true,
	})
	ts := httptest.NewServer(srv)
	t.Cleanup(ts.Close)
	joinJSON := fmt.Sprintf(`{
		"enrollment_id":"11111111111111111111111111111111",
		"node_id":"fedcba9876543210fedcba9876543210",
		"platform":"linux",
		"base_url":"https://spoke.example",
		"protocol_version":%d,
		"hub_credential":"hub-calls-spoke-token"
	}`, federation.ProtocolVersion)
	body := strings.NewReader(joinJSON)
	request, err := http.NewRequestWithContext(t.Context(),
		http.MethodPost, ts.URL+"/api/v1/federation/enrollments", body,
	)
	require.NoError(err)
	request.Header.Set("Authorization", "Bearer "+oneTime.Token)
	request.Header.Set("Content-Type", "application/json")
	response, err := ts.Client().Do(request)
	require.NoError(err)
	defer response.Body.Close()
	assert.Equal(http.StatusCreated, response.StatusCode)

	missingRequest, err := http.NewRequestWithContext(t.Context(),
		http.MethodPost, ts.URL+"/api/v1/federation/enrollments",
		strings.NewReader(joinJSON),
	)
	require.NoError(err)
	missingRequest.Header.Set("Content-Type", "application/json")
	missing, err := ts.Client().Do(missingRequest)
	require.NoError(err)
	defer missing.Body.Close()
	assert.Equal(http.StatusUnauthorized, missing.StatusCode,
		"the closed exception still requires its one-time token")
}
