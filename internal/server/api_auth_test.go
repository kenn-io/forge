package server

import (
	"encoding/json"
	"maps"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"go.kenn.io/forge/internal/config"
	"go.kenn.io/forge/internal/federation"
	"go.kenn.io/forge/internal/federationauth"
	"go.kenn.io/forge/internal/providerplane"
	"go.kenn.io/forge/internal/server/authapi"
	"go.kenn.io/forge/internal/server/httpapi"
	"go.kenn.io/forge/internal/testutil/dbtest"
)

func newAuthTestServer(t *testing.T, token string) *httptest.Server {
	t.Helper()
	srv := New(dbtest.Open(t), nil, nil, "/", nil, ServerOptions{
		DaemonAccess: authapi.DaemonAccessOptions{
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
			t.Cleanup(func() {
				if response != nil && response.Body != nil {
					_ = response.Body.Close()
				}
			})
			assert.Equal(http.StatusUnauthorized, response.StatusCode)
		})
	}

	request := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/api/v1/snapshot", nil)
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
	t.Cleanup(func() {
		if response != nil && response.Body != nil {
			_ = response.Body.Close()
		}
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
	t.Cleanup(func() {
		if response != nil && response.Body != nil {
			_ = response.Body.Close()
		}
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
	t.Cleanup(func() {
		if response != nil && response.Body != nil {
			_ = response.Body.Close()
		}
	})
	assert.Equal(http.StatusForbidden, response.StatusCode)
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
	require.NoError(enrollments.Activate(
		t.Context(), enrollmentID, time.Now().Add(time.Hour),
	))

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
		DaemonAccess: authapi.DaemonAccessOptions{
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
	srv.cfg.Fleet.Members[0].BaseURL = "https://replacement-spoke.example"
	srv.cfgMu.Unlock()
	response = requestSnapshot()
	response.Body.Close()
	require.Equal(http.StatusForbidden, response.StatusCode)
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
		DaemonAccess:          authapi.DaemonAccessOptions{Token: "local-secret", RequireAPIAuth: true},
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
		DaemonAccess:          authapi.DaemonAccessOptions{Token: "local-secret", RequireAPIAuth: true},
		FederationCredentials: credentials, FederationEnrollments: enrollments,
		FederationSpokeID: nodeID,
	})
	ts := httptest.NewServer(srv)
	t.Cleanup(ts.Close)
	t.Cleanup(func() { gracefulShutdown(t, srv) })

	request, err := http.NewRequestWithContext(t.Context(),
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
		DaemonAccess:          authapi.DaemonAccessOptions{Token: "local-secret", RequireAPIAuth: true},
		FederationCredentials: credentials, FederationEnrollments: enrollments,
		FederationSpokeID: hubID,
	})
	ts := httptest.NewServer(srv)
	t.Cleanup(ts.Close)
	t.Cleanup(func() { gracefulShutdown(t, srv) })

	request, err := http.NewRequestWithContext(t.Context(),
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

func TestLeaseUnawareHubEnrollmentCredentialIsInactive(t *testing.T) {
	require := require.New(t)
	const (
		hubID        = "0123456789abcdef0123456789abcdef"
		nodeID       = "fedcba9876543210fedcba9876543210"
		enrollmentID = "11111111111111111111111111111111"
	)
	path := filepath.Join(t.TempDir(), "enrollments.json")
	contents, err := json.Marshal(map[string]any{
		"version": 1, "tokens": []any{},
		"enrollments": []federation.Enrollment{{
			ID: enrollmentID, NodeID: nodeID,
			SpokePlatform: "linux", SpokeBaseURL: "https://spoke.example",
			HubID: hubID, HubURL: "https://hub.example",
			ProtocolVersion: federation.ProtocolVersion,
			State:           federation.EnrollmentActive,
			ExpiresAt:       time.Now().Add(time.Hour),
		}},
	})
	require.NoError(err)
	require.NoError(os.WriteFile(path, contents, 0o600))
	enrollments, err := federation.Open(path, federation.StoreOptions{})
	require.NoError(err)
	credentials, err := federationauth.Open(filepath.Join(t.TempDir(), "credentials.json"))
	require.NoError(err)
	token, err := credentials.MintInbound(
		nodeID, []federationauth.Scope{
			federationauth.ScopeSnapshotRead,
			federationauth.ScopeEnrollmentActivate,
		},
	)
	require.NoError(err)
	cfg := &config.Config{Fleet: config.Fleet{
		Enabled: true, Role: config.FleetRoleHub,
		Members: []config.FleetMember{{
			NodeID: nodeID, BaseURL: "https://spoke.example",
			State: federation.EnrollmentActive,
		}},
	}}
	srv := New(dbtest.Open(t), nil, nil, "/", cfg, ServerOptions{
		DaemonAccess:          authapi.DaemonAccessOptions{Token: "local-secret", RequireAPIAuth: true},
		FederationCredentials: credentials, FederationEnrollments: enrollments,
		FederationSpokeID: hubID,
	})
	ts := httptest.NewServer(srv)
	t.Cleanup(ts.Close)
	t.Cleanup(func() { gracefulShutdown(t, srv) })

	requestIdentity := func() *http.Response {
		return authGet(t, ts, "/api/v1/federation/identity", func(r *http.Request) {
			r.Header.Set("Authorization", "Bearer "+token)
			r.Header.Set(federationauth.NodeIDHeader, nodeID)
		})
	}
	requestActivation := func() *http.Response {
		request, requestErr := http.NewRequestWithContext(t.Context(),
			http.MethodPost,
			ts.URL+"/api/v1/federation/enrollments/"+enrollmentID+"/activate",
			strings.NewReader(`{"protocol_version":3,"preparation_seal":"legacy"}`),
		)
		require.NoError(requestErr)
		request.Header.Set("Authorization", "Bearer "+token)
		request.Header.Set("Content-Type", "application/json")
		response, requestErr := ts.Client().Do(request)
		require.NoError(requestErr)
		return response
	}

	identityResponse := requestIdentity()
	identityResponse.Body.Close()
	require.Equal(http.StatusOK, identityResponse.StatusCode,
		"lease-unaware peers need identity preflight access before activation")

	response := authGet(t, ts, "/api/v1/snapshot/raw", func(r *http.Request) {
		r.Header.Set("Authorization", "Bearer "+token)
	})
	defer response.Body.Close()
	require.Equal(http.StatusForbidden, response.StatusCode)

	activationResponse := requestActivation()
	activationResponse.Body.Close()
	require.Equal(http.StatusUnprocessableEntity, activationResponse.StatusCode,
		"lease-unaware peers may reach only the activation handshake")

	srv.cfgMu.Lock()
	srv.cfg.Fleet.Enabled = false
	srv.cfgMu.Unlock()
	identityResponse = requestIdentity()
	identityResponse.Body.Close()
	require.Equal(http.StatusForbidden, identityResponse.StatusCode)
	activationResponse = requestActivation()
	activationResponse.Body.Close()
	require.Equal(http.StatusForbidden, activationResponse.StatusCode)
}

func TestActiveHubCredentialRequiresActiveSpokeStartup(t *testing.T) {
	const (
		hubID        = "0123456789abcdef0123456789abcdef"
		nodeID       = "fedcba9876543210fedcba9876543210"
		enrollmentID = "11111111111111111111111111111111"
	)
	for _, test := range []struct {
		name            string
		nodeActive      bool
		leaseVersion    int
		leaseValidUntil time.Time
		wantDenied      bool
	}{
		{
			name: "validated startup with current lease", nodeActive: true,
			leaseVersion:    federation.ActivationLeaseVersion,
			leaseValidUntil: time.Now().Add(time.Hour),
		},
		{
			name: "validated startup with expired lease", nodeActive: true,
			leaseVersion:    federation.ActivationLeaseVersion,
			leaseValidUntil: time.Now().Add(-time.Hour), wantDenied: true,
		},
		{
			name: "local-only startup", leaseValidUntil: time.Now().Add(time.Hour),
			leaseVersion: federation.ActivationLeaseVersion,
			wantDenied:   true,
		},
		{
			name: "lease-unaware enrollment", nodeActive: true,
			leaseValidUntil: time.Time{}, wantDenied: true,
		},
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
				ActivationLeaseVersion: test.leaseVersion,
				ExpiresAt:              time.Now().Add(time.Hour),
				ActivationValidUntil:   test.leaseValidUntil,
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
				DaemonAccess:          authapi.DaemonAccessOptions{Token: "local-secret", RequireAPIAuth: true},
				FederationCredentials: credentials, FederationEnrollments: enrollments,
				FederationSpokeID: nodeID, FederationSpokeActive: test.nodeActive,
			})
			ts := httptest.NewServer(srv)
			t.Cleanup(ts.Close)
			t.Cleanup(func() { gracefulShutdown(t, srv) })

			request, err := http.NewRequestWithContext(t.Context(),
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
		ActivationLeaseVersion: federation.ActivationLeaseVersion,
		ExpiresAt:              time.Now().Add(time.Hour),
		ActivationValidUntil:   time.Now().Add(time.Hour),
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
		DaemonAccess:          authapi.DaemonAccessOptions{Token: "local-secret", RequireAPIAuth: true},
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
		DaemonAccess:          authapi.DaemonAccessOptions{Token: "local-secret", RequireAPIAuth: true},
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

	revoke, err := http.NewRequestWithContext(t.Context(),
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
		DaemonAccess:          authapi.DaemonAccessOptions{Token: "local-secret", RequireAPIAuth: true},
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
		DaemonAccess:          authapi.DaemonAccessOptions{Token: "local-secret", RequireAPIAuth: true},
		FederationCredentials: credentials, FederationEnrollments: enrollments,
		FederationSpokeID: hubID,
	})
	srv.now = func() time.Time { return now }
	ts := httptest.NewServer(srv)
	t.Cleanup(ts.Close)
	t.Cleanup(func() { gracefulShutdown(t, srv) })
	request := func(method, path, body string) *http.Response {
		req, requestErr := http.NewRequestWithContext(t.Context(), method, ts.URL+path, strings.NewReader(body))
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
		DaemonAccess:                       authapi.DaemonAccessOptions{Token: "local-secret", RequireAPIAuth: true},
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
		"pull_requests", "repo_presets", "repos", "repository_observations", "sync",
	}, slices.Collect(maps.Keys(body)))
}
