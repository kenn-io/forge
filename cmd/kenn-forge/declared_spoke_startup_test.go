package main

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"testing/iotest"
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
// client trusting its certificate, before the daemon it serves exists. While
// a fault is set, requests whose path ends with its suffix get its status and
// a plain-text body, as from a proxy in front of a daemon that is down or
// refuses the request.
type lateHandler struct {
	server atomic.Pointer[server.Server]
	fault  atomic.Pointer[httpFault]
}

type httpFault struct {
	suffix string
	status int
}

func (h *lateHandler) failRequests(suffix string, status int) {
	h.fault.Store(&httpFault{suffix: suffix, status: status})
}

func (h *lateHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if fault := h.fault.Load(); fault != nil && strings.HasSuffix(r.URL.Path, fault.suffix) {
		http.Error(w, "request refused", fault.status)
		return
	}
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
	client      *http.Client
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
	// The config has its own directory so a test can make only it read-only.
	path := filepath.Join(t.TempDir(), "config.toml")
	require.NoError(t, cfg.Save(path))
	cfg, err := config.Load(path)
	require.NoError(t, err)
	enrollments, err := federation.Open(federation.DefaultStorePath(dir), federation.StoreOptions{})
	require.NoError(t, err)
	credentials, err := federationauth.Open(federationauth.DefaultStorePath(dir))
	require.NoError(t, err)
	return &declaredSpokeNode{
		client: hub.client, nodeID: nodeID, url: url, configPath: path, cfg: cfg,
		database: dbtest.OpenAt(t, cfg.DBPath()), credentials: credentials,
		enrollments: enrollments, handler: handler,
	}
}

// boot runs kenn-forge serve's federation startup and, when the spoke
// activates, serves its daemon on the spoke listener.
func (s *declaredSpokeNode) boot(t *testing.T, hub *declaredHub) (federationSpokeStartup, error) {
	t.Helper()
	startup, err := startFederationSpoke(t.Context(), declaredSpokeStartup{
		database: s.database, cfg: s.cfg, configPath: s.configPath, nodeID: s.nodeID,
		enrollments: s.enrollments, credentials: s.credentials, httpClient: s.client,
	})
	if err != nil {
		return federationSpokeStartup{}, err
	}
	if startup.Active() {
		s.serve(t, hub)
	}
	return startup, nil
}

// serve starts the spoke daemon on the spoke listener.
func (s *declaredSpokeNode) serve(t *testing.T, hub *declaredHub) {
	t.Helper()
	s.handler.server.Store(servertest.NewWithConfig(t, s.database, nil, nil, nil, s.cfg, s.configPath, server.ServerOptions{
		DaemonAccess:          authapi.DaemonAccessOptions{Token: "spoke-local", RequireAPIAuth: true},
		FederationCredentials: s.credentials, FederationEnrollments: s.enrollments,
		FederationSpokeID: s.nodeID, FederationSpokeActive: true,
		FederationHTTPClient: hub.client,
		WorktreeDir:          t.TempDir(), DisableWorkspaceBackgroundMonitors: true,
		HostCheckAllowLoopbackAnyPort: true,
	}))
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

// dropResponses forwards requests to the hub but loses every reply, as when
// the connection breaks after the hub accepted the request.
type dropResponses struct{ next http.RoundTripper }

func (d dropResponses) RoundTrip(request *http.Request) (*http.Response, error) {
	response, err := d.next.RoundTrip(request)
	if err != nil {
		return nil, err
	}
	_ = response.Body.Close()
	return nil, errors.New("connection reset after the hub replied")
}

func TestDeclaredSpokeRecoversLostJoinReplyWithRotatedSecret(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	spokeListener, spokeHandler := startTLSListener(t)
	hubSecretPath := newDeclaredSecretFile(t, declaredSecret)
	hub := startDeclaredHub(t, []config.FleetDeclaredSpoke{{
		Name: declaredSpokeName, BaseURL: spokeListener.URL, EnrollmentSecretFile: hubSecretPath,
	}}, nil, nil)
	spokeSecretPath := newDeclaredSecretFile(t, declaredSecret)
	spoke := newDeclaredSpokeNode(t, hub, declaredSpokeID, spokeListener.URL, spokeHandler, spokeSecretPath)
	spoke.client = &http.Client{Transport: dropResponses{next: hub.client.Transport}}

	_, err := spoke.boot(t, hub)
	require.Error(err, "a lost reply leaves the spoke unsealed, so it exits for a retry")
	accepted := liveEnrollmentsAt(hub.enrollments, spoke.url)
	require.Len(accepted, 1)

	spoke.client = hub.client
	startup, err := spoke.boot(t, hub)
	require.NoError(err)
	assert.Equal(federationStartupActionRequired, startup.State)
	assert.Contains(startup.Reason, "write a new secret to enrollment_secret_file on the hub and this spoke")

	writeDeclaredSecret(t, hubSecretPath, rotatedSecret)
	writeDeclaredSecret(t, spokeSecretPath, rotatedSecret)
	startup, err = spoke.boot(t, hub)
	require.NoError(err)
	require.Equal(federationStartupActive, startup.State, startup.Reason)
	resumed := liveEnrollmentsAt(hub.enrollments, spoke.url)
	require.Len(resumed, 1)
	assert.Equal(accepted[0].ID, resumed[0].ID, "the rotated secret resumes the enrollment the spoke never heard about")
}

func TestDeclaredFleetSurvivesConfigRewritesWithoutLearnedState(t *testing.T) {
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
	spokeToHub := outboundToken(t, spoke.credentials, declaredHubID)

	hubConfig, err := config.Load(hub.configPath)
	require.NoError(err)
	hubConfig.Fleet.Members = nil
	require.NoError(hubConfig.Save(hub.configPath))
	require.Eventually(func() bool {
		reloaded, err := config.Load(hub.configPath)
		return err == nil && len(reloaded.Fleet.Members) == 1
	}, 10*time.Second, 50*time.Millisecond, "the hub restores the declared spoke's member entry")
	assert.Equal(http.StatusOK, identityStatus(t, hub.client, hub.url, declaredSpokeID, spokeToHub))

	spokeConfig, err := config.Load(spoke.configPath)
	require.NoError(err)
	spokeConfig.Fleet.Hub.NodeID = ""
	require.NoError(spokeConfig.Save(spoke.configPath))
	spoke.cfg, err = config.Load(spoke.configPath)
	require.NoError(err)
	startup, err = spoke.boot(t, hub)
	require.NoError(err)
	assert.Equal(federationStartupActive, startup.State, startup.Reason)
}

func TestDeclaredJoinWithAnotherSpokesNodeIDRevokesNothing(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	firstListener, firstHandler := startTLSListener(t)
	secondListener, secondHandler := startTLSListener(t)
	firstHubSecret := newDeclaredSecretFile(t, declaredSecret)
	secondSecret := newDeclaredSecretFile(t, rotatedSecret)
	hub := startDeclaredHub(t, []config.FleetDeclaredSpoke{
		{Name: declaredSpokeName, BaseURL: firstListener.URL, EnrollmentSecretFile: firstHubSecret},
		{Name: undeclaredSpokeName, BaseURL: secondListener.URL, EnrollmentSecretFile: secondSecret},
	}, nil, nil)
	first := newDeclaredSpokeNode(t, hub, declaredSpokeID, firstListener.URL, firstHandler,
		newDeclaredSecretFile(t, declaredSecret))
	second := newDeclaredSpokeNode(t, hub, reinstalledSpokeID, secondListener.URL, secondHandler, secondSecret)
	for _, spoke := range []*declaredSpokeNode{first, second} {
		startup, err := spoke.boot(t, hub)
		require.NoError(err)
		require.Equal(federationStartupActive, startup.State, startup.Reason)
	}

	const freshSecret = "aaaaaaaaaaaaaaaabbbbbbbbbbbbbbbb"
	writeDeclaredSecret(t, firstHubSecret, freshSecret)
	impostor := newDeclaredSpokeNode(t, hub, reinstalledSpokeID, firstListener.URL, firstHandler,
		newDeclaredSecretFile(t, freshSecret))
	startup, err := impostor.boot(t, hub)
	require.NoError(err)
	assert.Equal(federationStartupActionRequired, startup.State)
	assert.Contains(startup.Reason, "already enrolled at another origin")
	assert.Len(liveEnrollmentsAt(hub.enrollments, firstListener.URL), 1,
		"a rejected join keeps the spoke at the declared origin")
}

func TestDeclaredJoinWithHubsNodeIDRevokesNothing(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	spokeListener, spokeHandler := startTLSListener(t)
	hubSecretPath := newDeclaredSecretFile(t, declaredSecret)
	hub := startDeclaredHub(t, []config.FleetDeclaredSpoke{{
		Name: declaredSpokeName, BaseURL: spokeListener.URL, EnrollmentSecretFile: hubSecretPath,
	}}, nil, nil)
	spoke := newDeclaredSpokeNode(t, hub, declaredSpokeID, spokeListener.URL, spokeHandler,
		newDeclaredSecretFile(t, declaredSecret))
	startup, err := spoke.boot(t, hub)
	require.NoError(err)
	require.Equal(federationStartupActive, startup.State, startup.Reason)

	const freshSecret = "aaaaaaaaaaaaaaaabbbbbbbbbbbbbbbb"
	writeDeclaredSecret(t, hubSecretPath, freshSecret)
	impostor := newDeclaredSpokeNode(t, hub, declaredHubID, spokeListener.URL, spokeHandler,
		newDeclaredSecretFile(t, freshSecret))
	startup, err = impostor.boot(t, hub)
	require.NoError(err)
	assert.Equal(federationStartupActionRequired, startup.State)
	assert.Equal(http.StatusOK, identityStatus(t, hub.client, hub.url, declaredSpokeID, outboundToken(t, spoke.credentials, declaredHubID)),
		"a join the hub rejects keeps the spoke at the declared origin")
}

func TestHubTellsRemovedSpokeOnceItIsReachableAndSpokeCanLeave(t *testing.T) {
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

	// The spoke goes offline, which also closes its tunnel to the hub.
	require.NoError(spokeHandler.server.Swap(nil).Shutdown(t.Context()))
	undeclareAllSpokes(t, hub)
	require.Eventually(func() bool {
		return len(liveEnrollmentsAt(hub.enrollments, spoke.url)) == 0
	}, 10*time.Second, 50*time.Millisecond)
	local, ok := spoke.enrollments.Local()
	require.True(ok)
	require.Equal(federation.EnrollmentActive, local.State, "the offline spoke has not heard of its removal")

	spoke.serve(t, hub)
	hub.server.ReconcileDeclaredSpokes(t.Context())
	local, ok = spoke.enrollments.Local()
	require.True(ok)
	assert.Equal(federation.EnrollmentRevoked, local.State, "the hub retries the notice once the spoke is back")
	_, kept := hub.credentials.Outbound(declaredSpokeID)
	assert.False(kept, "the hub drops its credential for the spoke once the spoke knows")

	// Config management rewrites the spoke's config without the hub node ID
	// Forge learned at join.
	spokeConfig, err := config.Load(spoke.configPath)
	require.NoError(err)
	spokeConfig.Fleet.Hub.NodeID = ""
	require.NoError(spokeConfig.Save(spoke.configPath))
	leave := newFleetCommand(fleetCLIOptions{Stdout: io.Discard})
	leave.SetArgs([]string{"leave", "--config", spoke.configPath})
	require.NoError(leave.ExecuteContext(t.Context()))
	standalone, err := config.Load(spoke.configPath)
	require.NoError(err)
	assert.False(standalone.Fleet.Enabled)
	assert.Nil(standalone.Fleet.Hub)
}

func TestDeclaredSpokeRetriesUntilHubPublishesItsSecret(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	spokeListener, spokeHandler := startTLSListener(t)
	hubSecretPath := filepath.Join(t.TempDir(), "not-yet-published")
	hub := startDeclaredHub(t, []config.FleetDeclaredSpoke{{
		Name: declaredSpokeName, BaseURL: spokeListener.URL, EnrollmentSecretFile: hubSecretPath,
	}}, nil, nil)
	spoke := newDeclaredSpokeNode(t, hub, declaredSpokeID, spokeListener.URL, spokeHandler,
		newDeclaredSecretFile(t, declaredSecret))

	_, err := spoke.boot(t, hub)
	require.Error(err, "an unreadable hub secret is a hub outage, so the spoke exits for a retry")
	assert.Contains(err.Error(), "cannot read the enrollment secret")

	require.NoError(os.WriteFile(hubSecretPath, []byte(declaredSecret), 0o400))
	startup, err := spoke.boot(t, hub)
	require.NoError(err)
	assert.Equal(federationStartupActive, startup.State, startup.Reason)
}

func TestDeclaredSpokeRetriesWhenHubFailsDuringPreparation(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	spokeListener, spokeHandler := startTLSListener(t)
	secretPath := newDeclaredSecretFile(t, declaredSecret)
	hub := startDeclaredHub(t, []config.FleetDeclaredSpoke{{
		Name: declaredSpokeName, BaseURL: spokeListener.URL, EnrollmentSecretFile: secretPath,
	}}, nil, nil)
	spoke := newDeclaredSpokeNode(t, hub, declaredSpokeID, spokeListener.URL, spokeHandler, secretPath)
	hub.handler.failRequests("/preparation/seal", http.StatusServiceUnavailable)

	_, err := spoke.boot(t, hub)
	require.ErrorIs(err, server.ErrPreparationHubOutage)

	hub.handler.fault.Store(nil)
	startup, err := spoke.boot(t, hub)
	require.NoError(err)
	assert.Equal(federationStartupActive, startup.State, startup.Reason)
}

func TestDeclaredSpokeKeepsSecretUnusedWhileConfigIsReadOnly(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores directory permissions")
	}
	assert := assert.New(t)
	require := require.New(t)
	spokeListener, spokeHandler := startTLSListener(t)
	secretPath := newDeclaredSecretFile(t, declaredSecret)
	hub := startDeclaredHub(t, []config.FleetDeclaredSpoke{{
		Name: declaredSpokeName, BaseURL: spokeListener.URL, EnrollmentSecretFile: secretPath,
	}}, nil, nil)
	spoke := newDeclaredSpokeNode(t, hub, declaredSpokeID, spokeListener.URL, spokeHandler, secretPath)
	configDir := filepath.Dir(spoke.configPath)
	require.NoError(os.Chmod(configDir, 0o500))
	t.Cleanup(func() { _ = os.Chmod(configDir, 0o700) })

	_, err := spoke.boot(t, hub)
	require.Error(err)
	assert.Contains(err.Error(), "must be writable")
	assert.Empty(hub.enrollments.List(), "the hub never saw the secret")

	require.NoError(os.Chmod(configDir, 0o700))
	startup, err := spoke.boot(t, hub)
	require.NoError(err)
	assert.Equal(federationStartupActive, startup.State, startup.Reason)
}

func TestDeclaredSpokeRetriesHubTimeoutsAndRateLimits(t *testing.T) {
	for _, suffix := range []string{"/federation/enrollments", "/preparation/seal", "/activate"} {
		for _, status := range []int{http.StatusRequestTimeout, http.StatusTooManyRequests} {
			t.Run(fmt.Sprintf("%s %d", suffix, status), func(t *testing.T) {
				require := require.New(t)
				spokeListener, spokeHandler := startTLSListener(t)
				secretPath := newDeclaredSecretFile(t, declaredSecret)
				hub := startDeclaredHub(t, []config.FleetDeclaredSpoke{{
					Name: declaredSpokeName, BaseURL: spokeListener.URL, EnrollmentSecretFile: secretPath,
				}}, nil, nil)
				spoke := newDeclaredSpokeNode(t, hub, declaredSpokeID, spokeListener.URL, spokeHandler, secretPath)
				hub.handler.failRequests(suffix, status)

				_, err := spoke.boot(t, hub)
				require.Error(err, "the spoke exits so its supervisor retries")

				hub.handler.fault.Store(nil)
				startup, err := spoke.boot(t, hub)
				require.NoError(err)
				assert.Equal(t, federationStartupActive, startup.State, startup.Reason)
			})
		}
	}
}

func TestDeclaredSpokeStartsLocalOnlyWhenProxyRefusesJoin(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	spokeListener, spokeHandler := startTLSListener(t)
	secretPath := newDeclaredSecretFile(t, declaredSecret)
	hub := startDeclaredHub(t, []config.FleetDeclaredSpoke{{
		Name: declaredSpokeName, BaseURL: spokeListener.URL, EnrollmentSecretFile: secretPath,
	}}, nil, nil)
	spoke := newDeclaredSpokeNode(t, hub, declaredSpokeID, spokeListener.URL, spokeHandler, secretPath)
	hub.handler.failRequests("/federation/enrollments", http.StatusForbidden)

	startup, err := spoke.boot(t, hub)
	require.NoError(err, "a refusal that a restart cannot change must not crash-loop")
	assert.Equal(federationStartupActionRequired, startup.State)
	assert.Contains(startup.Reason, "HTTP 403")
}

func TestDeclaredSpokeRetriesWhenHubFailsBeforeFirstActivation(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	spokeListener, spokeHandler := startTLSListener(t)
	secretPath := newDeclaredSecretFile(t, declaredSecret)
	hub := startDeclaredHub(t, []config.FleetDeclaredSpoke{{
		Name: declaredSpokeName, BaseURL: spokeListener.URL, EnrollmentSecretFile: secretPath,
	}}, nil, nil)
	spoke := newDeclaredSpokeNode(t, hub, declaredSpokeID, spokeListener.URL, spokeHandler, secretPath)
	hub.handler.failRequests("/activate", http.StatusServiceUnavailable)

	_, err := spoke.boot(t, hub)
	require.Error(err, "a sealed spoke that never activated exits for a retry")
	assert.Contains(err.Error(), "activation failed")
	local, ok := spoke.enrollments.Local()
	require.True(ok)
	assert.NotNil(local.Preparation, "the seal survives the failed activation")

	hub.handler.fault.Store(nil)
	startup, err := spoke.boot(t, hub)
	require.NoError(err)
	assert.Equal(federationStartupActive, startup.State, startup.Reason)
}

// truncateResponses cuts off the body of every hub reply to a path ending
// with suffix, as when the connection breaks while the reply streams.
type truncateResponses struct {
	next   http.RoundTripper
	suffix string
}

func (d truncateResponses) RoundTrip(request *http.Request) (*http.Response, error) {
	response, err := d.next.RoundTrip(request)
	if err != nil || !strings.HasSuffix(request.URL.Path, d.suffix) {
		return response, err
	}
	_ = response.Body.Close()
	response.Body = io.NopCloser(iotest.ErrReader(io.ErrUnexpectedEOF))
	return response, nil
}

func TestDeclaredSpokeRetriesWhenSealReplyIsCutOff(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	spokeListener, spokeHandler := startTLSListener(t)
	secretPath := newDeclaredSecretFile(t, declaredSecret)
	hub := startDeclaredHub(t, []config.FleetDeclaredSpoke{{
		Name: declaredSpokeName, BaseURL: spokeListener.URL, EnrollmentSecretFile: secretPath,
	}}, nil, nil)
	spoke := newDeclaredSpokeNode(t, hub, declaredSpokeID, spokeListener.URL, spokeHandler, secretPath)
	spoke.client = &http.Client{Transport: truncateResponses{next: hub.client.Transport, suffix: "/preparation/seal"}}

	_, err := spoke.boot(t, hub)
	require.ErrorIs(err, server.ErrPreparationHubOutage)

	spoke.client = hub.client
	startup, err := spoke.boot(t, hub)
	require.NoError(err)
	assert.Equal(federationStartupActive, startup.State, startup.Reason)
}

func TestDeclaredSpokeRestoresHubNodeIDWhileFederationIsDisabled(t *testing.T) {
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

	spokeConfig, err := config.Load(spoke.configPath)
	require.NoError(err)
	spokeConfig.Fleet.Enabled = false
	spokeConfig.Fleet.Hub.NodeID = ""
	require.NoError(spokeConfig.Save(spoke.configPath))
	spoke.cfg, err = config.Load(spoke.configPath)
	require.NoError(err)
	startup, err = spoke.boot(t, hub)
	require.NoError(err)
	assert.Equal(federationStartupActive, startup.State, startup.Reason)
	assert.Equal(declaredHubID, spoke.cfg.Fleet.Hub.NodeID,
		"the server binds its hub client to the restored node ID")
}

func TestHubFinishesRevokingDeclaredSpokeAfterMemberCleanupFailed(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	removedOrigin := "https://removed-spoke.example"
	rejoinedOrigin := "https://rejoined-spoke.example"
	hub := startDeclaredHub(t, []config.FleetDeclaredSpoke{{
		Name: undeclaredSpokeName, BaseURL: rejoinedOrigin,
		EnrollmentSecretFile: newDeclaredSecretFile(t, declaredSecret),
	}}, []config.FleetMember{
		{NodeID: declaredSpokeID, BaseURL: removedOrigin, State: federation.EnrollmentActive, OutboundDisabled: true},
		{NodeID: reinstalledSpokeID, BaseURL: rejoinedOrigin, State: federation.EnrollmentActive, OutboundDisabled: true},
	}, func(store *federation.Store, hubURL string) {
		hubIdentity := federation.Identity{NodeID: declaredHubID, BaseURL: hubURL}
		// Each tombstone was stored, but removing its member entry failed.
		for enrollmentID, spoke := range map[string]federation.Identity{
			"44444444444444444444444444444444": {NodeID: declaredSpokeID, Name: declaredSpokeName, BaseURL: removedOrigin},
			"55555555555555555555555555555555": {NodeID: reinstalledSpokeID, Name: undeclaredSpokeName, BaseURL: rejoinedOrigin},
		} {
			_, err := federationtest.SeedActiveHubEnrollment(t.Context(), store, hubIdentity, spoke, enrollmentID)
			require.NoError(err)
			require.NoError(store.MarkDeclared(t.Context(), enrollmentID))
			require.NoError(store.Revoke(t.Context(), enrollmentID))
		}
		// The rejoined spoke enrolled again with the same node ID.
		_, err := federationtest.SeedActiveHubEnrollment(t.Context(), store, hubIdentity, federation.Identity{
			NodeID: reinstalledSpokeID, Name: undeclaredSpokeName, BaseURL: rejoinedOrigin,
		}, "66666666666666666666666666666666")
		require.NoError(err)
	})

	hub.server.ReconcileDeclaredSpokes(t.Context())
	hubConfig, err := config.Load(hub.configPath)
	require.NoError(err)
	require.Len(hubConfig.Fleet.Members, 1, "the removed spoke's stale member entry is gone")
	assert.Equal(reinstalledSpokeID, hubConfig.Fleet.Members[0].NodeID,
		"the old tombstone leaves the live enrollment's member in place")
	assert.Len(liveEnrollmentsAt(hub.enrollments, rejoinedOrigin), 1)
}
