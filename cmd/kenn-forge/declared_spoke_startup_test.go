package main

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"go.kenn.io/forge/internal/config"
	"go.kenn.io/forge/internal/db"
	"go.kenn.io/forge/internal/federation"
	"go.kenn.io/forge/internal/federationauth"
	"go.kenn.io/forge/internal/server"
	"go.kenn.io/forge/internal/server/authapi"
	"go.kenn.io/forge/internal/testutil/dbtest"
	"go.kenn.io/forge/internal/testutil/federationtest"
	"go.kenn.io/forge/internal/testutil/servertest"
)

const (
	declaredHubID       = "11111111111111111111111111111111"
	declaredSpokeID     = "22222222222222222222222222222222"
	reinstalledSpokeID  = "33333333333333333333333333333333"
	declaredSecret      = "0123456789abcdef0123456789abcdef"
	rotatedSecret       = "fedcba9876543210fedcba9876543210"
	declaredSpokeName   = "build-box"
	undeclaredSpokeName = "laptop"
)

// lateHandler lets a TLS listener start, and so publish its origin and a
// client trusting its certificate, before the daemon it serves exists.
type lateHandler struct{ server atomic.Pointer[server.Server] }

func (h *lateHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	srv := h.server.Load()
	if srv == nil {
		http.Error(w, "daemon not started", http.StatusServiceUnavailable)
		return
	}
	srv.ServeHTTP(w, r)
}

type declaredHub struct {
	url         string
	configPath  string
	client      *http.Client
	credentials *federationauth.Store
	enrollments *federation.Store
	handler     *lateHandler
	server      *server.Server
}

type declaredSpokeNode struct {
	nodeID      string
	url         string
	configPath  string
	cfg         *config.Config
	database    *db.DB
	credentials *federationauth.Store
	enrollments *federation.Store
	handler     *lateHandler
}

func writeDeclaredSecret(t *testing.T, path, secret string) {
	t.Helper()
	require.NoError(t, os.Remove(path))
	require.NoError(t, os.WriteFile(path, []byte(secret+"\n"), 0o400))
}

func newDeclaredSecretFile(t *testing.T, secret string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "enrollment-secret")
	require.NoError(t, os.WriteFile(path, []byte(secret+"\n"), 0o400))
	return path
}

func startTLSListener(t *testing.T) (*httptest.Server, *lateHandler) {
	t.Helper()
	handler := &lateHandler{}
	listener := httptest.NewUnstartedServer(handler)
	listener.StartTLS()
	t.Cleanup(listener.Close)
	return listener, handler
}

// startDeclaredHub starts a hub daemon from a config that declares the given
// spokes. seed runs against the hub's enrollment store before the daemon
// starts, as enrollments made by an earlier daemon would.
func startDeclaredHub(
	t *testing.T,
	declared []config.FleetDeclaredSpoke,
	members []config.FleetMember,
	seed func(*federation.Store, string),
) *declaredHub {
	t.Helper()
	listener, handler := startTLSListener(t)
	dir := t.TempDir()
	cfg := &config.Config{
		DataDir: dir, BasePath: "/", Host: "127.0.0.1", Port: 8091,
		Fleet: config.Fleet{
			Enabled: true, Role: config.FleetRoleHub, BaseURL: listener.URL,
			DeclaredSpokes: declared, Members: members,
		},
	}
	cfg.API.RequireAuth = true
	cfg.Tmux.Command = []string{"kenn-forge-no-such-tmux"}
	path := filepath.Join(dir, "config.toml")
	require.NoError(t, cfg.Save(path))
	cfg, err := config.Load(path)
	require.NoError(t, err)
	enrollments, err := federation.Open(federation.DefaultStorePath(dir), federation.StoreOptions{})
	require.NoError(t, err)
	credentials, err := federationauth.Open(federationauth.DefaultStorePath(dir))
	require.NoError(t, err)
	if seed != nil {
		seed(enrollments, listener.URL)
	}
	hub := &declaredHub{
		url: listener.URL, configPath: path, client: listener.Client(),
		credentials: credentials, enrollments: enrollments, handler: handler,
	}
	hub.server = servertest.NewWithConfig(t, dbtest.OpenAt(t, cfg.DBPath()), nil, nil, nil, cfg, path, server.ServerOptions{
		DaemonAccess:          authapi.DaemonAccessOptions{Token: "hub-local", RequireAPIAuth: true},
		FederationCredentials: credentials, FederationEnrollments: enrollments,
		FederationSpokeID: declaredHubID, FederationHTTPClient: listener.Client(),
		WorktreeDir: t.TempDir(), DisableWorkspaceBackgroundMonitors: true,
		HostCheckAllowLoopbackAnyPort: true,
	})
	handler.server.Store(hub.server)
	return hub
}

// newDeclaredSpokeNode writes a spoke config that declares its hub and opens
// the spoke's stores, as a freshly installed spoke has them.
func newDeclaredSpokeNode(
	t *testing.T, hub *declaredHub, nodeID, url string, handler *lateHandler, secretPath string,
) *declaredSpokeNode {
	t.Helper()
	dir := t.TempDir()
	cfg := &config.Config{
		DataDir: dir, BasePath: "/", Host: "127.0.0.1", Port: 8091,
		Fleet: config.Fleet{
			Enabled: true, Role: config.FleetRoleSpoke, BaseURL: url,
			Hub: &config.FleetHub{BaseURL: hub.url, EnrollmentSecretFile: secretPath},
		},
	}
	cfg.API.RequireAuth = true
	cfg.Tmux.Command = []string{"kenn-forge-no-such-tmux"}
	path := filepath.Join(dir, "config.toml")
	require.NoError(t, cfg.Save(path))
	cfg, err := config.Load(path)
	require.NoError(t, err)
	enrollments, err := federation.Open(federation.DefaultStorePath(dir), federation.StoreOptions{})
	require.NoError(t, err)
	credentials, err := federationauth.Open(federationauth.DefaultStorePath(dir))
	require.NoError(t, err)
	return &declaredSpokeNode{
		nodeID: nodeID, url: url, configPath: path, cfg: cfg,
		database: dbtest.OpenAt(t, cfg.DBPath()), credentials: credentials,
		enrollments: enrollments, handler: handler,
	}
}

// boot runs the same startup steps as kenn-forge serve and, when the spoke
// activates, serves its daemon on the spoke listener.
func (s *declaredSpokeNode) boot(t *testing.T, hub *declaredHub) (federationSpokeStartup, error) {
	t.Helper()
	reason, err := enrollDeclaredFederationSpoke(t.Context(), declaredSpokeStartup{
		database: s.database, cfg: s.cfg, configPath: s.configPath, nodeID: s.nodeID,
		enrollments: s.enrollments, credentials: s.credentials, httpClient: hub.client,
	})
	if err != nil {
		return federationSpokeStartup{}, err
	}
	startup := activateFederationSpokeAtStartup(
		t.Context(), s.database, s.cfg, s.nodeID, s.enrollments, s.credentials, hub.client,
	)
	if reason != "" && !startup.Active() {
		startup.Reason = reason
	}
	if startup.Active() {
		s.handler.server.Store(servertest.NewWithConfig(t, s.database, nil, nil, nil, s.cfg, s.configPath, server.ServerOptions{
			DaemonAccess:          authapi.DaemonAccessOptions{Token: "spoke-local", RequireAPIAuth: true},
			FederationCredentials: s.credentials, FederationEnrollments: s.enrollments,
			FederationSpokeID: s.nodeID, FederationSpokeActive: true,
			FederationHTTPClient: hub.client,
			WorktreeDir:          t.TempDir(), DisableWorkspaceBackgroundMonitors: true,
			HostCheckAllowLoopbackAnyPort: true,
		}))
	}
	return startup, nil
}

func outboundToken(t *testing.T, credentials *federationauth.Store, peerNodeID string) string {
	t.Helper()
	credential, ok := credentials.Outbound(peerNodeID)
	require.True(t, ok, "no outbound credential for %s", peerNodeID)
	return credential.Token
}

// identityStatus calls a peer's federation identity route with a bearer.
func identityStatus(
	t *testing.T, client *http.Client, baseURL, localNodeID, token string,
) int {
	t.Helper()
	request, err := http.NewRequestWithContext(
		t.Context(), http.MethodGet, baseURL+"/api/v1/federation/identity", http.NoBody,
	)
	require.NoError(t, err)
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set(federationauth.NodeIDHeader, localNodeID)
	response, err := client.Do(request)
	require.NoError(t, err)
	require.NoError(t, response.Body.Close())
	return response.StatusCode
}

func undeclareAllSpokes(t *testing.T, hub *declaredHub) {
	t.Helper()
	cfg, err := config.Load(hub.configPath)
	require.NoError(t, err)
	cfg.Fleet.DeclaredSpokes = nil
	require.NoError(t, cfg.Save(hub.configPath))
}

func liveEnrollmentsAt(store *federation.Store, origin string) []federation.Enrollment {
	var live []federation.Enrollment
	for _, enrollment := range store.List() {
		if enrollment.SpokeBaseURL == origin && enrollment.State != federation.EnrollmentRevoked {
			live = append(live, enrollment)
		}
	}
	return live
}

func TestDeclaredSpokeJoinsHubAndLosesAccessWhenUndeclared(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	spokeListener, spokeHandler := startTLSListener(t)
	secretPath := newDeclaredSecretFile(t, declaredSecret)
	hub := startDeclaredHub(t, []config.FleetDeclaredSpoke{{
		Name: declaredSpokeName, BaseURL: spokeListener.URL, EnrollmentSecretFile: secretPath,
	}}, nil, nil)
	spoke := newDeclaredSpokeNode(t, hub, declaredSpokeID, spokeListener.URL, spokeHandler, secretPath)

	startup, err := spoke.boot(t, hub)
	require.NoError(err)
	require.Equal(federationStartupActive, startup.State, startup.Reason)
	assert.Equal(http.StatusOK, identityStatus(t, hub.client, hub.url, declaredSpokeID, outboundToken(t, spoke.credentials, declaredHubID)))
	assert.Equal(http.StatusOK, identityStatus(t, hub.client, spoke.url, declaredHubID, outboundToken(t, hub.credentials, declaredSpokeID)))
	persisted, err := config.Load(spoke.configPath)
	require.NoError(err)
	assert.Equal(&config.FleetHub{
		NodeID: declaredHubID, BaseURL: hub.url, EnrollmentSecretFile: secretPath,
	}, persisted.Fleet.Hub)
	hubConfig, err := config.Load(hub.configPath)
	require.NoError(err)
	assert.Equal([]config.FleetMember{{
		NodeID: declaredSpokeID, Name: declaredSpokeName, BaseURL: spoke.url,
		State: federation.EnrollmentActive,
	}}, hubConfig.Fleet.Members)
	enrolled := liveEnrollmentsAt(hub.enrollments, spoke.url)
	require.Len(enrolled, 1)
	assert.True(enrolled[0].Declared)

	restarted, err := spoke.boot(t, hub)
	require.NoError(err)
	require.Equal(federationStartupActive, restarted.State, restarted.Reason)
	assert.Equal(enrolled[0].ID, liveEnrollmentsAt(hub.enrollments, spoke.url)[0].ID,
		"a restart with the same config keeps the enrollment")

	spokeToHub := outboundToken(t, spoke.credentials, declaredHubID)
	undeclareAllSpokes(t, hub)
	require.Eventually(func() bool {
		return identityStatus(t, hub.client, hub.url, declaredSpokeID, spokeToHub) ==
			http.StatusUnauthorized
	}, 10*time.Second, 50*time.Millisecond)
	local, ok := spoke.enrollments.Local()
	require.True(ok)
	assert.Equal(federation.EnrollmentRevoked, local.State, "the hub tells a reachable spoke it was removed")
	assert.Empty(liveEnrollmentsAt(hub.enrollments, spoke.url))
	// Revocation removes the member from config after the credential.
	require.Eventually(func() bool {
		hubConfig, err := config.Load(hub.configPath)
		return err == nil && len(hubConfig.Fleet.Members) == 0
	}, 10*time.Second, 50*time.Millisecond)
}

func TestDeclaredSpokeSecretAdmitsOneEnrollment(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	spokeListener, spokeHandler := startTLSListener(t)
	hubSecretPath := newDeclaredSecretFile(t, declaredSecret)
	hub := startDeclaredHub(t, []config.FleetDeclaredSpoke{{
		Name: declaredSpokeName, BaseURL: spokeListener.URL, EnrollmentSecretFile: hubSecretPath,
	}}, nil, nil)
	original := newDeclaredSpokeNode(t, hub, declaredSpokeID, spokeListener.URL, spokeHandler,
		newDeclaredSecretFile(t, declaredSecret))
	startup, err := original.boot(t, hub)
	require.NoError(err)
	require.Equal(federationStartupActive, startup.State, startup.Reason)

	reinstalledSecretPath := newDeclaredSecretFile(t, declaredSecret)
	reinstalled := newDeclaredSpokeNode(t, hub, reinstalledSpokeID, spokeListener.URL, spokeHandler,
		reinstalledSecretPath)
	startup, err = reinstalled.boot(t, hub)
	require.NoError(err)
	assert.Equal(federationStartupActionRequired, startup.State)
	assert.Contains(startup.Reason, "consumed")
	assert.Equal(http.StatusOK, identityStatus(t, hub.client, hub.url, declaredSpokeID, outboundToken(t, original.credentials, declaredHubID)),
		"a consumed secret must not displace the enrolled spoke")

	originalToHub := outboundToken(t, original.credentials, declaredHubID)
	writeDeclaredSecret(t, hubSecretPath, rotatedSecret)
	writeDeclaredSecret(t, reinstalledSecretPath, rotatedSecret)
	startup, err = reinstalled.boot(t, hub)
	require.NoError(err)
	require.Equal(federationStartupActive, startup.State, startup.Reason)
	assert.Equal(http.StatusOK, identityStatus(t, hub.client, hub.url, reinstalledSpokeID, outboundToken(t, reinstalled.credentials, declaredHubID)))
	assert.Equal(http.StatusUnauthorized, identityStatus(t, hub.client, hub.url, declaredSpokeID, originalToHub),
		"a fresh secret replaces the enrollment at the declared origin")
	live := liveEnrollmentsAt(hub.enrollments, spokeListener.URL)
	require.Len(live, 1)
	assert.Equal(reinstalledSpokeID, live[0].NodeID)
}

func TestDeclaredSpokeRejectsSecretFromAnotherOrigin(t *testing.T) {
	spokeListener, spokeHandler := startTLSListener(t)
	otherListener, _ := startTLSListener(t)
	secretPath := newDeclaredSecretFile(t, declaredSecret)
	hub := startDeclaredHub(t, []config.FleetDeclaredSpoke{{
		Name: declaredSpokeName, BaseURL: otherListener.URL, EnrollmentSecretFile: secretPath,
	}}, nil, nil)
	spoke := newDeclaredSpokeNode(t, hub, declaredSpokeID, spokeListener.URL, spokeHandler, secretPath)

	startup, err := spoke.boot(t, hub)
	require.NoError(t, err)
	assert.Equal(t, federationStartupActionRequired, startup.State)
	assert.Contains(t, startup.Reason, "invalid one-time enrollment token")
	assert.Empty(t, hub.enrollments.List())
}

func TestDeclaredSpokeStartupFailsWhileHubIsUnreachable(t *testing.T) {
	spokeListener, spokeHandler := startTLSListener(t)
	secretPath := newDeclaredSecretFile(t, declaredSecret)
	hub := startDeclaredHub(t, []config.FleetDeclaredSpoke{{
		Name: declaredSpokeName, BaseURL: spokeListener.URL, EnrollmentSecretFile: secretPath,
	}}, nil, nil)
	spoke := newDeclaredSpokeNode(t, hub, declaredSpokeID, spokeListener.URL, spokeHandler, secretPath)
	spoke.cfg.Fleet.Hub.BaseURL = "https://" + closedListenerAddr(t)

	_, err := spoke.boot(t, hub)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "join fleet hub")
}

func closedListenerAddr(t *testing.T) string {
	t.Helper()
	listener := httptest.NewUnstartedServer(http.NotFoundHandler())
	addr := listener.Listener.Addr().String()
	listener.Close()
	return addr
}

func TestHubAdoptsCLIEnrolledSpokeAtDeclaredOrigin(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	declaredOrigin := "https://declared-spoke.example"
	undeclaredOrigin := "https://undeclared-spoke.example"
	hub := startDeclaredHub(t, []config.FleetDeclaredSpoke{{
		Name: declaredSpokeName, BaseURL: declaredOrigin,
		EnrollmentSecretFile: newDeclaredSecretFile(t, declaredSecret),
	}}, []config.FleetMember{
		{NodeID: declaredSpokeID, BaseURL: declaredOrigin, State: federation.EnrollmentActive, OutboundDisabled: true},
		{NodeID: reinstalledSpokeID, BaseURL: undeclaredOrigin, State: federation.EnrollmentActive, OutboundDisabled: true},
	}, func(store *federation.Store, hubURL string) {
		for enrollmentID, spoke := range map[string]federation.Identity{
			"44444444444444444444444444444444": {NodeID: declaredSpokeID, Name: declaredSpokeName, BaseURL: declaredOrigin},
			"55555555555555555555555555555555": {NodeID: reinstalledSpokeID, Name: undeclaredSpokeName, BaseURL: undeclaredOrigin},
		} {
			_, err := federationtest.SeedActiveHubEnrollment(t.Context(), store,
				federation.Identity{NodeID: declaredHubID, BaseURL: hubURL}, spoke, enrollmentID)
			require.NoError(err)
		}
	})

	hub.server.ReconcileDeclaredSpokes(t.Context())
	adopted := liveEnrollmentsAt(hub.enrollments, declaredOrigin)
	require.Len(adopted, 1)
	assert.True(adopted[0].Declared)
	cliEnrolled := liveEnrollmentsAt(hub.enrollments, undeclaredOrigin)
	require.Len(cliEnrolled, 1)
	assert.False(cliEnrolled[0].Declared)

	undeclareAllSpokes(t, hub)
	require.Eventually(func() bool {
		return len(liveEnrollmentsAt(hub.enrollments, declaredOrigin)) == 0
	}, 10*time.Second, 50*time.Millisecond)
	assert.Len(liveEnrollmentsAt(hub.enrollments, undeclaredOrigin), 1,
		"removing declarations leaves CLI-enrolled members")
}
