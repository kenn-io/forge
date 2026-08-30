package server

import (
	"encoding/json"
	"maps"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/kit/daemon"

	"go.kenn.io/forge/internal/config"
	"go.kenn.io/forge/internal/daemonruntime"
	"go.kenn.io/forge/internal/federation"
	"go.kenn.io/forge/internal/federationauth"
	"go.kenn.io/forge/internal/providerplane"
	"go.kenn.io/forge/internal/server/httpapi"
	"go.kenn.io/forge/internal/testutil/dbtest"
)

func newAuthTestServer(t *testing.T, token string) *httptest.Server {
	t.Helper()
	srv := New(dbtest.Open(t), nil, nil, "/", nil, ServerOptions{
		DaemonAccess: DaemonAccessOptions{
			Token: token, RequireAPIAuth: token != "",
		},
	})
	ts := httptest.NewServer(srv)
	t.Cleanup(ts.Close)
	return ts
}

func newTailscaleAuthTestServer(t *testing.T) (*httptest.Server, *Server) {
	t.Helper()
	srv := New(dbtest.Open(t), nil, nil, "/", nil, ServerOptions{
		DaemonAccess: DaemonAccessOptions{
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
	srv := New(dbtest.Open(t), nil, nil, "/", nil, ServerOptions{
		DaemonAccess: DaemonAccessOptions{
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
	srv := New(dbtest.Open(t), nil, nil, "/", nil, ServerOptions{
		DaemonAccess: DaemonAccessOptions{
			Token: "secret-token", RequireAPIAuth: true,
			ProofHandler: proofHandler,
		},
		HostCheck: HostCheckOptions{
			Bind: bind, Allowed: []config.HostKey{{Host: "forge.example.test"}},
			TrustReverseProxy: true,
		},
	})
	srv.SetBuildInfo(BuildInfo{Name: "kenn-forge", Version: "v-test"})
	ts.Config.Handler = srv
	ts.Start()
	t.Cleanup(ts.Close)

	unauthorized := authGet(t, ts, "/api/ping", func(r *http.Request) {
		r.Header.Set("X-Forwarded-Host", "forge.example.test")
	})
	assert.Equal(http.StatusUnauthorized, unauthorized.StatusCode)
	response := authGet(t, ts, "/api/ping", func(r *http.Request) {
		r.Header.Set("Authorization", "Bearer secret-token")
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
	assert.Equal(http.StatusForbidden, forwarded.StatusCode)
}

func authGet(
	t *testing.T, ts *httptest.Server, path string,
	decorate func(*http.Request),
) *http.Response {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, ts.URL+path, nil)
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
	assert.Equal(http.StatusOK, resp.StatusCode)

	resp = authGet(t, ts, "/api/v1/snapshot", func(r *http.Request) {
		r.Header.Set("Authorization", "Bearer wrong")
	})
	assert.Equal(http.StatusUnauthorized, resp.StatusCode)

	resp = authGet(t, ts, "/api/v1/snapshot", func(r *http.Request) {
		r.Header.Set("Tailscale-User-Login", "user@example.com")
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
	assert.NotEqual(http.StatusUnauthorized, response.StatusCode)
}

func TestTailscaleServeIdentityRejectsUntrustedRequests(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	ts, srv := newTailscaleAuthTestServer(t)

	for _, test := range []struct {
		name   string
		values []string
	}{
		{name: "missing"},
		{name: "wrong user", values: []string{"other@example.com"}},
		{name: "malformed", values: []string{"User <user@example.com>"}},
		{name: "combined", values: []string{"user@example.com,other@example.com"}},
		{name: "repeated", values: []string{"user@example.com", "user@example.com"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			response := authGet(t, ts, "/api/v1/snapshot", func(request *http.Request) {
				for _, value := range test.values {
					request.Header.Add("Tailscale-User-Login", value)
				}
			})
			assert.Equal(http.StatusUnauthorized, response.StatusCode)
		})
	}

	request := httptest.NewRequest(http.MethodGet, "/api/v1/snapshot", nil)
	request.RemoteAddr = "192.0.2.10:43210"
	request.Host = srv.hostOpts.Load().Bind.String()
	request.Header.Set("Tailscale-User-Login", "user@example.com")
	recorder := httptest.NewRecorder()
	srv.ServeHTTP(recorder, request)
	require.Equal(http.StatusUnauthorized, recorder.Code)

	response := authGet(t, ts, "/ws/v1/workspaces/ws-1/terminal", func(request *http.Request) {
		request.Header.Set("Connection", "Upgrade")
		request.Header.Set("Upgrade", "websocket")
		request.Header.Set("Sec-WebSocket-Key", "dGhlIHNhbXBsZSBub25jZQ==")
		request.Header.Set("Sec-WebSocket-Version", "13")
	})
	assert.Equal(http.StatusUnauthorized, response.StatusCode)

	response = authGet(t, ts, "/ws/v1/workspaces/ws-1/terminal", func(request *http.Request) {
		request.Header.Set("Tailscale-User-Login", "user@example.com")
		request.Header.Set("Origin", "https://attacker.example")
		request.Header.Set("Connection", "Upgrade")
		request.Header.Set("Upgrade", "websocket")
		request.Header.Set("Sec-WebSocket-Key", "dGhlIHNhbXBsZSBub25jZQ==")
		request.Header.Set("Sec-WebSocket-Version", "13")
	})
	assert.Equal(http.StatusForbidden, response.StatusCode)

	response = authGet(t, ts, "/ws/v1/workspaces/ws-1/terminal", func(request *http.Request) {
		request.Header.Set("Tailscale-User-Login", "user@example.com")
		request.Header.Set("Origin", "http://"+request.URL.Host)
		request.Header.Set("Connection", "Upgrade")
		request.Header.Set("Upgrade", "websocket")
		request.Header.Set("Sec-WebSocket-Key", "dGhlIHNhbXBsZSBub25jZQ==")
		request.Header.Set("Sec-WebSocket-Version", "13")
	})
	assert.Equal(http.StatusForbidden, response.StatusCode)
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
	srv := New(dbtest.Open(t), nil, nil, "/", nil, ServerOptions{
		DaemonAccess: DaemonAccessOptions{
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
	assert.Equal(http.StatusUnauthorized, resp.StatusCode,
		"unauthenticated terminal WebSocket requests must be rejected")

	resp = authGet(t, ts, "/ws/v1/workspaces/ws-1/terminal",
		func(r *http.Request) {
			r.Header.Set("Authorization", "Bearer secret-token")
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
	assert.Equal(http.StatusOK, resp.StatusCode,
		"the bootstrap cookie authorizes API requests")

	resp = authGet(t, ts, "/?auth_token=wrong", nil)
	assert.Equal(http.StatusForbidden, resp.StatusCode)
	assert.Empty(resp.Cookies())
}

// TestAPIAuthDisabledByDefault pins the default: with no token
// configured, behavior is unchanged and nothing is gated.
func TestAPIAuthDisabledByDefault(t *testing.T) {
	ts := newAuthTestServer(t, "")
	resp := authGet(t, ts, "/api/v1/snapshot", nil)
	assert.Equal(t, http.StatusOK, resp.StatusCode)
}

func TestFederationAuthIsScopedIndependentlyOfLocalAuth(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	ts, _, token := newFederationAuthTestServer(t, federationauth.ScopeSnapshotRead)

	resp := authGet(t, ts, "/api/v1/snapshot/raw", func(r *http.Request) {
		r.Header.Set("Authorization", "Bearer "+token)
	})
	assert.Equal(http.StatusOK, resp.StatusCode)

	resp = authGet(t, ts, "/api/v1/settings", func(r *http.Request) {
		r.Header.Set("Authorization", "Bearer "+token)
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
	assert.NotEqual(http.StatusUnauthorized, resp.StatusCode)
	assert.NotEqual(http.StatusForbidden, resp.StatusCode)
}

func TestFederationAuthTreatsEscapedSlashAsOneRouteParameter(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	ts, _, token := newFederationAuthTestServer(t, federationauth.ScopeWorkspaceWrite)

	req, err := http.NewRequest(
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
	require.Equal(http.StatusOK, resp.StatusCode)
	require.NoError(store.RevokeInbound(token))

	resp = authGet(t, ts, "/api/v1/snapshot/raw", func(r *http.Request) {
		r.Header.Set("Authorization", "Bearer "+token)
	})
	assert.Equal(http.StatusUnauthorized, resp.StatusCode)
}

func TestRemovedFleetMemberCredentialFailsOnNextRequest(t *testing.T) {
	require := require.New(t)
	const (
		hubID        = "0123456789abcdef0123456789abcdef"
		nodeID       = "fedcba9876543210fedcba9876543210"
		enrollmentID = "11111111111111111111111111111111"
	)
	enrollments, err := federation.Open(
		filepath.Join(t.TempDir(), "enrollments.json"), federation.StoreOptions{},
	)
	require.NoError(err)
	oneTime, err := enrollments.CreateOneTimeToken(federation.Identity{
		NodeID: hubID, BaseURL: "https://hub.example",
	}, time.Now().Add(time.Minute))
	require.NoError(err)
	_, err = enrollments.Begin(t.Context(), oneTime.Token, federation.JoinRequest{
		EnrollmentID:    enrollmentID,
		NodeID:          nodeID,
		BaseURL:         "https://spoke.example",
		Platform:        "linux",
		ProtocolVersion: federation.ProtocolVersion,
		HubCredential:   "hub-credential",
	})
	require.NoError(err)
	require.NoError(enrollments.Activate(t.Context(), enrollmentID))

	credentials, err := federationauth.Open(
		filepath.Join(t.TempDir(), "credentials.json"),
	)
	require.NoError(err)
	token, err := credentials.MintInbound(nodeID, []federationauth.Scope{
		federationauth.ScopeSnapshotRead,
	})
	require.NoError(err)
	cfg := &config.Config{Fleet: config.Fleet{
		Enabled: true,
		Role:    config.FleetRoleHub,
		Members: []config.FleetMember{{
			NodeID: nodeID, BaseURL: "https://spoke.example",
			State: federation.EnrollmentActive,
		}},
	}}
	srv := New(dbtest.Open(t), nil, nil, "/", cfg, ServerOptions{
		DaemonAccess: DaemonAccessOptions{
			Token: "local-secret", RequireAPIAuth: true,
		},
		FederationCredentials: credentials,
		FederationEnrollments: enrollments,
		FederationSpokeID:     hubID,
	})
	ts := httptest.NewServer(srv)
	t.Cleanup(ts.Close)
	requestSnapshot := func() *http.Response {
		return authGet(t, ts, "/api/v1/snapshot/raw", func(r *http.Request) {
			r.Header.Set("Authorization", "Bearer "+token)
		})
	}

	response := requestSnapshot()
	require.Equal(http.StatusOK, response.StatusCode)
	response.Body.Close()
	srv.cfgMu.Lock()
	srv.cfg.Fleet.Members = nil
	srv.cfgMu.Unlock()
	response = requestSnapshot()
	response.Body.Close()
	assert.Equal(t, http.StatusForbidden, response.StatusCode)
}

func TestPendingHubCredentialExpiresUntilPreparationIsPinned(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	now := time.Date(2026, 8, 23, 12, 0, 0, 0, time.UTC)
	const (
		hubID        = "0123456789abcdef0123456789abcdef"
		nodeID       = "fedcba9876543210fedcba9876543210"
		enrollmentID = "11111111111111111111111111111111"
	)
	enrollments, err := federation.Open(
		filepath.Join(t.TempDir(), "enrollments.json"), federation.StoreOptions{},
	)
	require.NoError(err)
	require.NoError(enrollments.SaveLocal(t.Context(), federation.LocalEnrollment{
		EnrollmentID: enrollmentID, NodeID: nodeID,
		SpokePlatform: "linux", SpokeBaseURL: "https://spoke.example",
		HubID: hubID, HubURL: "https://hub.example",
		ProtocolVersion: federation.ProtocolVersion, State: federation.EnrollmentPending,
		ExpiresAt: now.Add(time.Minute), PreparationRequired: true,
	}))
	credentials, err := federationauth.Open(filepath.Join(t.TempDir(), "credentials.json"))
	require.NoError(err)
	token, err := credentials.MintInbound(
		hubID, federationauth.PendingHubToSpokeScopes(),
	)
	require.NoError(err)
	cfg := &config.Config{Fleet: config.Fleet{
		Enabled: true, Role: config.FleetRoleSpoke,
		Hub: &config.FleetHub{
			NodeID: hubID, BaseURL: "https://hub.example",
		},
	}}
	srv := New(dbtest.Open(t), nil, nil, "/", cfg, ServerOptions{
		DaemonAccess:          DaemonAccessOptions{Token: "local-secret", RequireAPIAuth: true},
		FederationCredentials: credentials, FederationEnrollments: enrollments,
		FederationSpokeID: nodeID,
	})
	srv.now = func() time.Time { return now }
	ts := httptest.NewServer(srv)
	t.Cleanup(ts.Close)
	t.Cleanup(func() { gracefulShutdown(t, srv) })
	requestIdentity := func() *http.Response {
		return authGet(t, ts, "/api/v1/federation/identity", func(r *http.Request) {
			r.Header.Set("Authorization", "Bearer "+token)
			r.Header.Set(federationauth.NodeIDHeader, hubID)
		})
	}

	response := requestIdentity()
	require.Equal(http.StatusOK, response.StatusCode)
	response.Body.Close()
	now = now.Add(2 * time.Minute)
	response = requestIdentity()
	assert.Equal(http.StatusForbidden, response.StatusCode)
	response.Body.Close()
	require.NoError(enrollments.MarkLocalPreparationStarted(t.Context(), enrollmentID))
	response = requestIdentity()
	assert.Equal(http.StatusOK, response.StatusCode)
	response.Body.Close()
}

func TestPendingHubCredentialCanRevokeLocalEnrollmentBeforeRoleTransition(t *testing.T) {
	require := require.New(t)
	const (
		hubID        = "0123456789abcdef0123456789abcdef"
		nodeID       = "fedcba9876543210fedcba9876543210"
		enrollmentID = "11111111111111111111111111111111"
	)
	enrollments, err := federation.Open(
		filepath.Join(t.TempDir(), "enrollments.json"), federation.StoreOptions{},
	)
	require.NoError(err)
	require.NoError(enrollments.SaveLocal(t.Context(), federation.LocalEnrollment{
		EnrollmentID: enrollmentID, NodeID: nodeID,
		SpokePlatform: "linux", SpokeBaseURL: "https://spoke.example",
		HubID: hubID, HubURL: "https://hub.example",
		ProtocolVersion: federation.ProtocolVersion, State: federation.EnrollmentPending,
		ExpiresAt: time.Now().Add(time.Hour), PreparationRequired: true,
	}))
	credentials, err := federationauth.Open(filepath.Join(t.TempDir(), "credentials.json"))
	require.NoError(err)
	token, err := credentials.MintInbound(hubID, federationauth.PendingHubToSpokeScopes())
	require.NoError(err)
	cfg := &config.Config{Fleet: config.Fleet{Enabled: true, Role: config.FleetRoleHub}}
	srv := New(dbtest.Open(t), nil, nil, "/", cfg, ServerOptions{
		DaemonAccess:          DaemonAccessOptions{Token: "local-secret", RequireAPIAuth: true},
		FederationCredentials: credentials, FederationEnrollments: enrollments,
		FederationSpokeID: nodeID,
	})
	ts := httptest.NewServer(srv)
	t.Cleanup(ts.Close)
	t.Cleanup(func() { gracefulShutdown(t, srv) })

	request, err := http.NewRequest(
		http.MethodDelete, ts.URL+"/api/v1/fleet/enrollments/"+enrollmentID, http.NoBody,
	)
	require.NoError(err)
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set(federationauth.NodeIDHeader, hubID)
	request.Header.Set("Content-Type", "application/json")
	response, err := ts.Client().Do(request)
	require.NoError(err)
	response.Body.Close()
	require.Equal(http.StatusNoContent, response.StatusCode)
	local, ok := enrollments.Local()
	require.True(ok)
	assert.Equal(t, federation.EnrollmentRevoked, local.State)
}

func TestPendingSpokeCredentialCannotRevokeSiblingEnrollment(t *testing.T) {
	require := require.New(t)
	const (
		hubID          = "0123456789abcdef0123456789abcdef"
		requestingNode = "fedcba9876543210fedcba9876543210"
		siblingNode    = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
		requestingID   = "11111111111111111111111111111111"
		siblingID      = "22222222222222222222222222222222"
	)
	enrollments, err := federation.Open(
		filepath.Join(t.TempDir(), "enrollments.json"), federation.StoreOptions{},
	)
	require.NoError(err)
	for _, candidate := range []struct {
		id, nodeID, baseURL string
	}{
		{id: requestingID, nodeID: requestingNode, baseURL: "https://requesting.example"},
		{id: siblingID, nodeID: siblingNode, baseURL: "https://sibling.example"},
	} {
		token, tokenErr := enrollments.CreateOneTimeToken(federation.Identity{
			NodeID: hubID, BaseURL: "https://hub.example",
		}, time.Now().Add(time.Minute))
		require.NoError(tokenErr)
		_, beginErr := enrollments.Begin(t.Context(), token.Token, federation.JoinRequest{
			EnrollmentID: candidate.id, NodeID: candidate.nodeID,
			Platform: "linux", BaseURL: candidate.baseURL,
			ProtocolVersion: federation.ProtocolVersion, HubCredential: "hub-credential",
		})
		require.NoError(beginErr)
	}
	credentials, err := federationauth.Open(filepath.Join(t.TempDir(), "credentials.json"))
	require.NoError(err)
	token, err := credentials.MintInbound(
		requestingNode, federationauth.PendingSpokeToHubScopes(),
	)
	require.NoError(err)
	cfg := &config.Config{Fleet: config.Fleet{Enabled: true, Role: config.FleetRoleHub}}
	srv := New(dbtest.Open(t), nil, nil, "/", cfg, ServerOptions{
		DaemonAccess:          DaemonAccessOptions{Token: "local-secret", RequireAPIAuth: true},
		FederationCredentials: credentials, FederationEnrollments: enrollments,
		FederationSpokeID: hubID,
	})
	ts := httptest.NewServer(srv)
	t.Cleanup(ts.Close)
	t.Cleanup(func() { gracefulShutdown(t, srv) })

	request, err := http.NewRequest(
		http.MethodDelete, ts.URL+"/api/v1/fleet/enrollments/"+siblingID, http.NoBody,
	)
	require.NoError(err)
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set(federationauth.NodeIDHeader, requestingNode)
	request.Header.Set("Content-Type", "application/json")
	response, err := ts.Client().Do(request)
	require.NoError(err)
	response.Body.Close()
	assert.Equal(t, http.StatusNotFound, response.StatusCode)
	sibling, err := enrollments.Get(t.Context(), siblingID)
	require.NoError(err)
	assert.Equal(t, federation.EnrollmentPending, sibling.State)
}

func TestActiveHubCredentialRequiresActiveSpokeStartup(t *testing.T) {
	const (
		hubID        = "0123456789abcdef0123456789abcdef"
		nodeID       = "fedcba9876543210fedcba9876543210"
		enrollmentID = "11111111111111111111111111111111"
	)
	for _, test := range []struct {
		name       string
		nodeActive bool
		wantDenied bool
	}{
		{name: "validated startup", nodeActive: true},
		{name: "local-only startup", wantDenied: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			assert := assert.New(t)
			require := require.New(t)
			enrollments, err := federation.Open(
				filepath.Join(t.TempDir(), "enrollments.json"), federation.StoreOptions{},
			)
			require.NoError(err)
			require.NoError(enrollments.SaveLocal(t.Context(), federation.LocalEnrollment{
				EnrollmentID: enrollmentID, NodeID: nodeID,
				SpokePlatform: "linux", SpokeBaseURL: "https://spoke.example",
				HubID: hubID, HubURL: "https://hub.example",
				ProtocolVersion: federation.ProtocolVersion, State: federation.EnrollmentActive,
				ExpiresAt: time.Now().Add(time.Hour),
			}))
			credentials, err := federationauth.Open(filepath.Join(t.TempDir(), "credentials.json"))
			require.NoError(err)
			token, err := credentials.MintInbound(
				hubID, federationauth.HubToSpokeScopes(),
			)
			require.NoError(err)
			cfg := &config.Config{Fleet: config.Fleet{
				Enabled: true, Role: config.FleetRoleSpoke,
				Hub: &config.FleetHub{
					NodeID: hubID, BaseURL: "https://hub.example",
				},
			}}
			srv := New(dbtest.Open(t), nil, nil, "/", cfg, ServerOptions{
				DaemonAccess:          DaemonAccessOptions{Token: "local-secret", RequireAPIAuth: true},
				FederationCredentials: credentials, FederationEnrollments: enrollments,
				FederationSpokeID: nodeID, FederationSpokeActive: test.nodeActive,
			})
			ts := httptest.NewServer(srv)
			t.Cleanup(ts.Close)
			t.Cleanup(func() { gracefulShutdown(t, srv) })

			request, err := http.NewRequest(
				http.MethodPost, ts.URL+"/api/v1/runtime/sessions", strings.NewReader(`{}`),
			)
			require.NoError(err)
			request.Header.Set("Authorization", "Bearer "+token)
			request.Header.Set(federationauth.NodeIDHeader, hubID)
			request.Header.Set("Content-Type", "application/json")
			response, err := ts.Client().Do(request)
			require.NoError(err)
			defer response.Body.Close()
			if test.wantDenied {
				assert.Equal(http.StatusForbidden, response.StatusCode)
			} else {
				assert.NotEqual(http.StatusForbidden, response.StatusCode)
				assert.NotEqual(http.StatusUnauthorized, response.StatusCode)
			}
		})
	}
}

func TestFederationAuthenticationKeepsBootTopologyUntilRestart(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	const (
		hubID        = "0123456789abcdef0123456789abcdef"
		nodeID       = "fedcba9876543210fedcba9876543210"
		enrollmentID = "11111111111111111111111111111111"
	)
	enrollments, err := federation.Open(
		filepath.Join(t.TempDir(), "enrollments.json"), federation.StoreOptions{},
	)
	require.NoError(err)
	require.NoError(enrollments.SaveLocal(t.Context(), federation.LocalEnrollment{
		EnrollmentID: enrollmentID, NodeID: nodeID,
		SpokePlatform: "linux", SpokeBaseURL: "https://spoke.example",
		HubID: hubID, HubURL: "https://hub.example",
		ProtocolVersion: federation.ProtocolVersion, State: federation.EnrollmentActive,
		ExpiresAt: time.Now().Add(time.Hour),
	}))
	credentials, err := federationauth.Open(filepath.Join(t.TempDir(), "credentials.json"))
	require.NoError(err)
	token, err := credentials.MintInbound(hubID, federationauth.HubToSpokeScopes())
	require.NoError(err)
	cfg := &config.Config{Fleet: config.Fleet{
		Enabled: true, Role: config.FleetRoleSpoke,
		Hub: &config.FleetHub{NodeID: hubID, BaseURL: "https://hub.example"},
	}}
	srv := New(dbtest.Open(t), nil, nil, "/", cfg, ServerOptions{
		DaemonAccess:          DaemonAccessOptions{Token: "local-secret", RequireAPIAuth: true},
		FederationCredentials: credentials, FederationEnrollments: enrollments,
		FederationSpokeID: nodeID, FederationSpokeActive: true,
	})
	ts := httptest.NewServer(srv)
	t.Cleanup(ts.Close)
	t.Cleanup(func() { gracefulShutdown(t, srv) })

	srv.cfgMu.Lock()
	srv.cfg.Fleet.Role = config.FleetRoleHub
	srv.cfg.Fleet.Hub = &config.FleetHub{
		NodeID:  "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		BaseURL: "https://replacement.example",
	}
	srv.cfgMu.Unlock()
	response := authGet(t, ts, "/api/v1/federation/identity", func(request *http.Request) {
		request.Header.Set("Authorization", "Bearer "+token)
		request.Header.Set(federationauth.NodeIDHeader, hubID)
	})
	response.Body.Close()
	assert.Equal(http.StatusOK, response.StatusCode)
}

func TestRevokedSpokeCredentialOnlyRetriesRevocation(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	const (
		hubID        = "0123456789abcdef0123456789abcdef"
		nodeID       = "fedcba9876543210fedcba9876543210"
		enrollmentID = "11111111111111111111111111111111"
	)
	enrollments, err := federation.Open(
		filepath.Join(t.TempDir(), "enrollments.json"), federation.StoreOptions{},
	)
	require.NoError(err)
	require.NoError(enrollments.SaveLocal(t.Context(), federation.LocalEnrollment{
		EnrollmentID: enrollmentID, NodeID: nodeID,
		SpokePlatform: "linux", SpokeBaseURL: "https://spoke.example",
		HubID: hubID, HubURL: "https://hub.example",
		ProtocolVersion: federation.ProtocolVersion,
		State:           federation.EnrollmentRevoked,
		ExpiresAt:       time.Now().Add(time.Hour),
	}))
	credentials, err := federationauth.Open(filepath.Join(t.TempDir(), "credentials.json"))
	require.NoError(err)
	token, err := credentials.MintInbound(
		hubID, []federationauth.Scope{federationauth.ScopeEnrollmentActivate},
	)
	require.NoError(err)
	cfg := &config.Config{Fleet: config.Fleet{
		Enabled: true, Role: config.FleetRoleSpoke,
		Hub: &config.FleetHub{NodeID: hubID, BaseURL: "https://hub.example"},
	}}
	srv := New(dbtest.Open(t), nil, nil, "/", cfg, ServerOptions{
		DaemonAccess:          DaemonAccessOptions{Token: "local-secret", RequireAPIAuth: true},
		FederationCredentials: credentials, FederationEnrollments: enrollments,
		FederationSpokeID: nodeID,
	})
	ts := httptest.NewServer(srv)
	t.Cleanup(ts.Close)
	t.Cleanup(func() { gracefulShutdown(t, srv) })

	identity := authGet(t, ts, "/api/v1/federation/identity", func(r *http.Request) {
		r.Header.Set("Authorization", "Bearer "+token)
		r.Header.Set(federationauth.NodeIDHeader, hubID)
	})
	assert.Equal(http.StatusForbidden, identity.StatusCode)
	identity.Body.Close()

	revoke, err := http.NewRequest(
		http.MethodDelete,
		ts.URL+"/api/v1/fleet/enrollments/"+enrollmentID,
		http.NoBody,
	)
	require.NoError(err)
	revoke.Header.Set("Authorization", "Bearer "+token)
	revoke.Header.Set(federationauth.NodeIDHeader, hubID)
	revoke.Header.Set("Content-Type", "application/json")
	response, err := ts.Client().Do(revoke)
	require.NoError(err)
	assert.Equal(http.StatusNoContent, response.StatusCode)
	response.Body.Close()
}

func TestPendingSpokeCredentialExpiresUntilPreparationIsPinned(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	now := time.Date(2026, 8, 23, 12, 0, 0, 0, time.UTC)
	const (
		hubID        = "0123456789abcdef0123456789abcdef"
		nodeID       = "fedcba9876543210fedcba9876543210"
		enrollmentID = "11111111111111111111111111111111"
	)
	enrollments, err := federation.Open(
		filepath.Join(t.TempDir(), "enrollments.json"),
		federation.StoreOptions{Now: func() time.Time { return now }},
	)
	require.NoError(err)
	oneTime, err := enrollments.CreateOneTimeToken(federation.Identity{
		NodeID: hubID, BaseURL: "https://hub.example",
	}, now.Add(time.Minute))
	require.NoError(err)
	_, err = enrollments.Begin(t.Context(), oneTime.Token, federation.JoinRequest{
		EnrollmentID: enrollmentID, NodeID: nodeID,
		Platform: "linux", BaseURL: "https://spoke.example",
		ProtocolVersion: federation.ProtocolVersion,
		HubCredential:   "hub-credential",
	})
	require.NoError(err)
	credentials, err := federationauth.Open(filepath.Join(t.TempDir(), "credentials.json"))
	require.NoError(err)
	token, err := credentials.MintInbound(
		nodeID, federationauth.PendingSpokeToHubScopes(),
	)
	require.NoError(err)
	cfg := &config.Config{Fleet: config.Fleet{
		Enabled: true, Role: config.FleetRoleHub,
	}}
	srv := New(dbtest.Open(t), nil, nil, "/", cfg, ServerOptions{
		DaemonAccess:          DaemonAccessOptions{Token: "local-secret", RequireAPIAuth: true},
		FederationCredentials: credentials, FederationEnrollments: enrollments,
		FederationSpokeID: hubID,
	})
	srv.now = func() time.Time { return now }
	ts := httptest.NewServer(srv)
	t.Cleanup(ts.Close)
	t.Cleanup(func() { gracefulShutdown(t, srv) })
	requestIdentity := func() *http.Response {
		return authGet(t, ts, "/api/v1/federation/identity", func(r *http.Request) {
			r.Header.Set("Authorization", "Bearer "+token)
			r.Header.Set(federationauth.NodeIDHeader, nodeID)
		})
	}

	response := requestIdentity()
	require.Equal(http.StatusOK, response.StatusCode)
	response.Body.Close()
	now = now.Add(2 * time.Minute)
	response = requestIdentity()
	assert.Equal(http.StatusForbidden, response.StatusCode)
	response.Body.Close()
	require.NoError(enrollments.MarkPreparationStarted(t.Context(), enrollmentID))
	response = requestIdentity()
	assert.Equal(http.StatusOK, response.StatusCode)
	response.Body.Close()
}

func TestPendingSpokeCredentialOnlyAccessesPreparationProviderRoutes(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	now := time.Date(2026, 8, 25, 12, 0, 0, 0, time.UTC)
	const (
		hubID        = "0123456789abcdef0123456789abcdef"
		nodeID       = "fedcba9876543210fedcba9876543210"
		enrollmentID = "11111111111111111111111111111111"
	)
	enrollments, err := federation.Open(
		filepath.Join(t.TempDir(), "enrollments.json"),
		federation.StoreOptions{Now: func() time.Time { return now }},
	)
	require.NoError(err)
	oneTime, err := enrollments.CreateOneTimeToken(federation.Identity{
		NodeID: hubID, BaseURL: "https://hub.example",
	}, now.Add(time.Minute))
	require.NoError(err)
	_, err = enrollments.Begin(t.Context(), oneTime.Token, federation.JoinRequest{
		EnrollmentID: enrollmentID, NodeID: nodeID,
		Platform: "linux", BaseURL: "https://spoke.example",
		ProtocolVersion: federation.ProtocolVersion,
		HubCredential:   "hub-credential",
	})
	require.NoError(err)
	credentials, err := federationauth.Open(filepath.Join(t.TempDir(), "credentials.json"))
	require.NoError(err)
	token, err := credentials.MintInbound(
		nodeID, federationauth.PendingSpokeToHubScopes(),
	)
	require.NoError(err)
	srv := New(dbtest.Open(t), nil, nil, "/", &config.Config{Fleet: config.Fleet{
		Enabled: true, Role: config.FleetRoleHub,
	}}, ServerOptions{
		DaemonAccess:          DaemonAccessOptions{Token: "local-secret", RequireAPIAuth: true},
		FederationCredentials: credentials, FederationEnrollments: enrollments,
		FederationSpokeID: hubID,
	})
	srv.now = func() time.Time { return now }
	ts := httptest.NewServer(srv)
	t.Cleanup(ts.Close)
	t.Cleanup(func() { gracefulShutdown(t, srv) })
	request := func(method, path, body string) *http.Response {
		req, requestErr := http.NewRequest(method, ts.URL+path, strings.NewReader(body))
		require.NoError(requestErr)
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set(federationauth.NodeIDHeader, nodeID)
		req.Header.Set(
			providerplane.ProtocolVersionHeader,
			providerplane.ProtocolVersionHeaderValue(),
		)
		if body != "" {
			req.Header.Set("Content-Type", "application/json")
		}
		response, requestErr := ts.Client().Do(req)
		require.NoError(requestErr)
		return response
	}

	for _, path := range []string{
		"/api/v1/repos",
		"/api/v1/pulls",
		"/api/v1/issues",
		"/api/v1/federation/provider/settings",
	} {
		response := request(http.MethodGet, path, "")
		require.Equal(http.StatusForbidden, response.StatusCode, path)
		var problem httpapi.ProblemError
		require.NoError(json.NewDecoder(response.Body).Decode(&problem))
		response.Body.Close()
		assert.Equal("federationEnrollmentPending", problem.Details["reason"], path)
	}

	response := request(
		http.MethodPost,
		"/api/v1/federation/provider/repository-descriptor",
		`{"provider":"github","platform_host":"github.com","owner":"acme","name":"widget"}`,
	)
	assert.NotEqual(http.StatusUnauthorized, response.StatusCode)
	assert.NotEqual(http.StatusForbidden, response.StatusCode)
	response.Body.Close()

	require.NoError(enrollments.MarkPreparationStarted(t.Context(), enrollmentID))
	response = request(http.MethodGet, "/api/v1/federation/provider/settings", "")
	assert.Equal(http.StatusForbidden, response.StatusCode)
	response.Body.Close()
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
	require.Equal(http.StatusConflict, response.StatusCode)
	var problem httpapi.ProblemError
	require.NoError(json.NewDecoder(response.Body).Decode(&problem))
	assert.Equal("protocolMismatch", problem.Details["reason"])

	response = requestProviderRead("2")
	require.Equal(http.StatusConflict, response.StatusCode)

	response = requestProviderRead(providerplane.ProtocolVersionHeaderValue())
	assert.NotEqual(http.StatusUnauthorized, response.StatusCode)
	assert.NotEqual(http.StatusForbidden, response.StatusCode)
	assert.NotEqual(http.StatusConflict, response.StatusCode)

	write, err := http.NewRequest(
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
	assert.Equal(http.StatusUnauthorized, response.StatusCode)
}

func TestFederationProviderSettingsUseDedicatedProjection(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	credentials, err := federationauth.Open(filepath.Join(t.TempDir(), "credentials.json"))
	require.NoError(err)
	token, err := credentials.MintInbound(
		"fedcba9876543210fedcba9876543210",
		[]federationauth.Scope{federationauth.ScopeProviderRead},
	)
	require.NoError(err)
	srv := New(dbtest.Open(t), nil, nil, "/", &config.Config{}, ServerOptions{
		DaemonAccess:                       DaemonAccessOptions{Token: "local-secret", RequireAPIAuth: true},
		FederationCredentials:              credentials,
		DisableWorkspaceBackgroundMonitors: true,
	})
	t.Cleanup(func() { gracefulShutdown(t, srv) })
	ts := httptest.NewServer(srv)
	t.Cleanup(ts.Close)
	request := func(path string) *http.Response {
		return authGet(t, ts, path, func(r *http.Request) {
			r.Header.Set("Authorization", "Bearer "+token)
			r.Header.Set(
				providerplane.ProtocolVersionHeader,
				providerplane.ProtocolVersionHeaderValue(),
			)
		})
	}

	response := request("/api/v1/settings")
	assert.Equal(http.StatusForbidden, response.StatusCode)
	response.Body.Close()

	response = request("/api/v1/federation/provider/settings")
	require.Equal(http.StatusOK, response.StatusCode)
	defer response.Body.Close()
	var body map[string]json.RawMessage
	require.NoError(json.NewDecoder(response.Body).Decode(&body))
	delete(body, "$schema")
	assert.ElementsMatch([]string{
		"activity", "detail", "issues", "notifications",
		"pull_requests", "repo_presets", "repos", "repository_observations",
	}, slices.Collect(maps.Keys(body)))
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
	srv := New(dbtest.Open(t), nil, nil, "/", cfg, ServerOptions{
		DaemonAccess: DaemonAccessOptions{
			Token: "local-secret", RequireAPIAuth: true,
		},
		FederationCredentials:         credentials,
		FederationEnrollments:         enrollments,
		FederationSpokeID:             hubID,
		HostCheckAllowLoopbackAnyPort: true,
	})
	ts := httptest.NewServer(srv)
	t.Cleanup(ts.Close)
	const joinJSON = `{
		"enrollment_id":"11111111111111111111111111111111",
		"node_id":"fedcba9876543210fedcba9876543210",
		"platform":"linux",
		"base_url":"https://spoke.example",
		"protocol_version":3,
		"hub_credential":"hub-calls-spoke-token"
	}`
	body := strings.NewReader(joinJSON)
	request, err := http.NewRequest(
		http.MethodPost, ts.URL+"/api/v1/federation/enrollments", body,
	)
	require.NoError(err)
	request.Header.Set("Authorization", "Bearer "+oneTime.Token)
	request.Header.Set("Content-Type", "application/json")
	response, err := ts.Client().Do(request)
	require.NoError(err)
	defer response.Body.Close()
	assert.Equal(http.StatusCreated, response.StatusCode)

	missingRequest, err := http.NewRequest(
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

// TestRedactedQueryMasksBootstrapToken pins the log-redaction
// contract: the bootstrap token never appears in the logged query.
func TestRedactedQueryMasksBootstrapToken(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	u, err := url.Parse("/?auth_token=secret&tab=pulls")
	require.NoError(err)
	redacted := redactedQuery(u)
	assert.NotContains(redacted, "secret")
	assert.Contains(redacted, "auth_token=REDACTED")
	assert.Contains(redacted, "tab=pulls")

	plain, err := url.Parse("/?tab=pulls")
	require.NoError(err)
	assert.Equal("tab=pulls", redactedQuery(plain))
}
