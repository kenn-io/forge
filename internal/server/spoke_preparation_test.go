package server

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/forge/internal/config"
	"go.kenn.io/forge/internal/db"
	"go.kenn.io/forge/internal/federation"
	"go.kenn.io/forge/internal/federationauth"
	"go.kenn.io/forge/internal/gitclone"
	"go.kenn.io/forge/internal/providerplane"
	"go.kenn.io/forge/internal/testutil/dbtest"
	"go.kenn.io/forge/internal/tokenauth"
)

const (
	preparationHubNodeID    = "0123456789abcdef0123456789abcdef"
	preparationLocalNodeID  = "fedcba9876543210fedcba9876543210"
	preparationEnrollmentID = "11111111111111111111111111111111"
)

func openFederationPreparationStores(
	t *testing.T, name string,
) (*federation.Store, *federationauth.Store) {
	t.Helper()
	dir := t.TempDir()
	enrollments, err := federation.Open(
		filepath.Join(dir, name+"-enrollments.json"), federation.StoreOptions{},
	)
	require.NoError(t, err)
	credentials, err := federationauth.Open(filepath.Join(dir, name+"-credentials.json"))
	require.NoError(t, err)
	return enrollments, credentials
}

func prepareSpokeRequest(
	t *testing.T, server *httptest.Server,
) (SpokePreparationReport, int) {
	t.Helper()
	request, err := http.NewRequestWithContext(
		t.Context(), http.MethodPost, server.URL+"/api/v1/fleet/prepare-spoke",
		bytes.NewReader([]byte(`{}`)),
	)
	require.NoError(t, err)
	request.Header.Set("Authorization", "Bearer local-secret")
	request.Header.Set("Content-Type", "application/json")
	response, err := server.Client().Do(request)
	require.NoError(t, err)
	defer response.Body.Close()
	var report SpokePreparationReport
	if response.StatusCode == http.StatusOK {
		require.NoError(t, json.NewDecoder(response.Body).Decode(&report))
	}
	return report, response.StatusCode
}

func TestAbortPreparationFromNodeShapedServerRequiresRestart(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	enrollments, credentials := openFederationPreparationStores(t, "abort-spoke")
	require.NoError(enrollments.SaveLocal(t.Context(), federation.LocalEnrollment{
		EnrollmentID: preparationEnrollmentID, NodeID: preparationLocalNodeID,
		SpokeBaseURL:    "https://spoke.example",
		HubID:           preparationHubNodeID,
		HubURL:          "https://hub.example",
		ProtocolVersion: federation.ProtocolVersion, State: federation.EnrollmentPending,
		ExpiresAt: time.Now().Add(-time.Minute),
	}))
	srv, _, _ := setupTestServerWithConfigContentAndOptions(t, `
host = "127.0.0.1"
port = 8091

[api]
require_auth = true

[fleet]
enabled = true
role = "spoke"
base_url = "https://spoke.example"

[fleet.hub]
node_id = "0123456789abcdef0123456789abcdef"
base_url = "https://hub.example"
`, &mockGH{}, ServerOptions{
		DaemonAccess:          DaemonAccessOptions{Token: "local-secret", RequireAPIAuth: true},
		FederationCredentials: credentials, FederationEnrollments: enrollments,
		FederationSpokeID: preparationLocalNodeID, HostCheckAllowLoopbackAnyPort: true,
	})
	daemon := httptest.NewServer(srv)
	t.Cleanup(daemon.Close)
	request, err := http.NewRequestWithContext(
		t.Context(), http.MethodPost, daemon.URL+"/api/v1/fleet/prepare-spoke/abort",
		bytes.NewReader([]byte(`{}`)),
	)
	require.NoError(err)
	request.Header.Set("Authorization", "Bearer local-secret")
	request.Header.Set("Content-Type", "application/json")
	response, err := daemon.Client().Do(request)
	require.NoError(err)
	t.Cleanup(func() { require.NoError(response.Body.Close()) })
	var report struct {
		ProviderWritesOpen bool `json:"provider_writes_open"`
		RestartRequired    bool `json:"restart_required"`
	}
	require.NoError(json.NewDecoder(response.Body).Decode(&report))

	assert.Equal(http.StatusOK, response.StatusCode)
	assert.False(report.ProviderWritesOpen)
	assert.True(report.RestartRequired)
}

func TestForcedAbortPreservesHubRevocationPath(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	enrollments, credentials := openFederationPreparationStores(t, "forced-abort")
	require.NoError(enrollments.SaveLocal(t.Context(), federation.LocalEnrollment{
		EnrollmentID: preparationEnrollmentID, NodeID: preparationLocalNodeID,
		SpokePlatform: "linux", SpokeBaseURL: "https://spoke.example",
		HubID: preparationHubNodeID, HubURL: "https://hub.example",
		ProtocolVersion: federation.ProtocolVersion, State: federation.EnrollmentPending,
		ExpiresAt: time.Now().Add(time.Hour), PreparationRequired: true,
	}))
	require.NoError(credentials.StoreOutbound(
		preparationHubNodeID, "spoke-to-hub-token",
		federationauth.PendingSpokeToHubScopes(),
	))
	require.NoError(credentials.StoreInbound(
		preparationHubNodeID, "hub-to-spoke-token",
		federationauth.PendingHubToSpokeScopes(),
	))
	srv, _, _ := setupTestServerWithConfigContentAndOptions(t, `
host = "127.0.0.1"
port = 8091

[api]
require_auth = true

[fleet]
enabled = true
role = "spoke"
base_url = "https://spoke.example"

[fleet.hub]
node_id = "0123456789abcdef0123456789abcdef"
base_url = "https://hub.example"
`, &mockGH{}, ServerOptions{
		DaemonAccess:          DaemonAccessOptions{Token: "local-secret", RequireAPIAuth: true},
		FederationCredentials: credentials, FederationEnrollments: enrollments,
		FederationSpokeID: preparationLocalNodeID,
		FederationHTTPClient: &http.Client{Transport: roundTripFunc(func(
			*http.Request,
		) (*http.Response, error) {
			return nil, fmt.Errorf("hub offline")
		})},
		HostCheckAllowLoopbackAnyPort: true,
	})
	server := httptest.NewServer(srv)
	t.Cleanup(server.Close)
	t.Cleanup(func() { gracefulShutdown(t, srv) })

	abort, err := http.NewRequestWithContext(
		t.Context(), http.MethodPost,
		server.URL+"/api/v1/fleet/prepare-spoke/abort",
		bytes.NewReader([]byte(`{"force":true}`)),
	)
	require.NoError(err)
	abort.Header.Set("Authorization", "Bearer local-secret")
	abort.Header.Set("Content-Type", "application/json")
	response, err := server.Client().Do(abort)
	require.NoError(err)
	responseBody, err := io.ReadAll(response.Body)
	require.NoError(err)
	response.Body.Close()
	require.Equal(http.StatusOK, response.StatusCode, string(responseBody))
	local, ok := enrollments.Local()
	require.True(ok)
	assert.Equal(federation.EnrollmentRevoked, local.State)
	_, ok = credentials.Outbound(preparationHubNodeID)
	assert.False(ok)
	principal, ok := credentials.Authenticate("hub-to-spoke-token")
	require.True(ok)
	assert.Equal(
		map[federationauth.Scope]struct{}{federationauth.ScopeEnrollmentActivate: {}},
		principal.Scopes,
	)

	revoke, err := http.NewRequestWithContext(
		t.Context(), http.MethodDelete,
		server.URL+"/api/v1/fleet/enrollments/"+preparationEnrollmentID,
		http.NoBody,
	)
	require.NoError(err)
	revoke.Header.Set("Authorization", "Bearer hub-to-spoke-token")
	revoke.Header.Set(federationauth.NodeIDHeader, preparationHubNodeID)
	revoke.Header.Set("Content-Type", "application/json")
	response, err = server.Client().Do(revoke)
	require.NoError(err)
	response.Body.Close()
	assert.Equal(http.StatusNoContent, response.StatusCode)
}

func TestPrepareFederationSpokeSealsAndPersistsRoleThroughDaemon(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	hubDB := dbtest.Open(t)
	hubEnrollments, hubCredentials := openFederationPreparationStores(t, "hub")
	hubConfig := &config.Config{Fleet: config.Fleet{
		Enabled: true, Role: config.FleetRoleHub,
	}}
	hub := New(hubDB, nil, nil, "/", hubConfig, ServerOptions{
		DaemonAccess:                  DaemonAccessOptions{Token: "hub-local", RequireAPIAuth: true},
		FederationCredentials:         hubCredentials,
		FederationEnrollments:         hubEnrollments,
		FederationSpokeID:             preparationHubNodeID,
		HostCheckAllowLoopbackAnyPort: true,
	})
	t.Cleanup(func() { gracefulShutdown(t, hub) })
	hubHTTP := httptest.NewTLSServer(hub)
	t.Cleanup(hubHTTP.Close)

	spokeEnrollments, spokeCredentials := openFederationPreparationStores(t, "spoke")
	spokeToHub := "spoke-to-hub-preparation-token"
	hubToSpoke := "hub-to-spoke-preparation-token"
	require.NoError(hubCredentials.StoreInbound(
		preparationLocalNodeID, spokeToHub,
		federationauth.PendingSpokeToHubScopes(),
	))
	require.NoError(spokeCredentials.StoreOutbound(
		preparationHubNodeID, spokeToHub,
		federationauth.PendingSpokeToHubScopes(),
	))
	require.NoError(spokeCredentials.StoreInbound(
		preparationHubNodeID, hubToSpoke,
		federationauth.PendingHubToSpokeScopes(),
	))
	require.NoError(hubCredentials.StoreOutbound(
		preparationLocalNodeID, hubToSpoke,
		federationauth.PendingHubToSpokeScopes(),
	))

	token, err := hubEnrollments.CreateOneTimeToken(federation.Identity{
		NodeID: preparationHubNodeID, BaseURL: hubHTTP.URL,
	}, time.Now().Add(time.Minute))
	require.NoError(err)
	_, err = hubEnrollments.Begin(t.Context(), token.Token, federation.JoinRequest{
		EnrollmentID: preparationEnrollmentID, NodeID: preparationLocalNodeID,
		Platform: "linux", BaseURL: "https://spoke.example",
		ProtocolVersion: federation.ProtocolVersion,
		HubCredential:   hubToSpoke,
	})
	require.NoError(err)
	require.NoError(spokeEnrollments.SaveLocal(t.Context(), federation.LocalEnrollment{
		EnrollmentID: preparationEnrollmentID, NodeID: preparationLocalNodeID,
		SpokePlatform: "linux", SpokeBaseURL: "https://spoke.example",
		HubID:           preparationHubNodeID,
		HubURL:          hubHTTP.URL,
		ProtocolVersion: federation.ProtocolVersion, State: federation.EnrollmentPending,
		ExpiresAt: token.ExpiresAt, PreparationRequired: true,
	}))

	spokeDB := dbtest.Open(t)
	spokeMRID := seedPR(t, spokeDB, "acme", "widget", 7)
	seedPR(t, hubDB, "acme", "widget", 7)
	seedPR(t, hubDB, "acme", "project-only", 8)
	projectRepoID, err := spokeDB.UpsertRepo(t.Context(), db.RepoIdentity{
		Platform: "github", PlatformHost: "github.com",
		Owner: "acme", Name: "project-only",
	})
	require.NoError(err)
	_, err = spokeDB.CreateProject(t.Context(), db.CreateProjectInput{
		DisplayName: "Project only", LocalPath: t.TempDir(),
		RepoID: sql.NullInt64{Int64: projectRepoID, Valid: true}, DefaultBranch: "main",
	})
	require.NoError(err)
	_, err = hubDB.WriteDB().ExecContext(
		t.Context(), "DELETE FROM forge_item_workflow_state",
	)
	require.NoError(err)
	require.NoError(spokeDB.SetKanbanState(t.Context(), spokeMRID, "reviewing"))
	spokeConfigPath := filepath.Join(t.TempDir(), "forge.toml")
	writeConfigToml(t, spokeConfigPath, fmt.Sprintf(`
host = "127.0.0.1"
port = 8091
data_dir = %q

[api]
require_auth = true

[fleet]
enabled = true
role = "hub"
base_url = "https://hub.example"

[fleet.hub]
node_id = %q
base_url = %q
`, t.TempDir(), preparationHubNodeID, hubHTTP.URL))
	spokeConfig, err := config.Load(spokeConfigPath)
	require.NoError(err)
	spoke := NewWithConfig(spokeDB, nil, nil, nil, spokeConfig, spokeConfigPath, ServerOptions{
		DaemonAccess:                  DaemonAccessOptions{Token: "local-secret", RequireAPIAuth: true},
		FederationCredentials:         spokeCredentials,
		FederationEnrollments:         spokeEnrollments,
		FederationSpokeID:             preparationLocalNodeID,
		FederationHTTPClient:          hubHTTP.Client(),
		HostCheckAllowLoopbackAnyPort: true,
	})
	t.Cleanup(func() { gracefulShutdown(t, spoke) })
	spokeHTTP := httptest.NewServer(spoke)
	t.Cleanup(spokeHTTP.Close)

	releaseWrite, err := spoke.providerWriteGate.Admit(t.Context())
	require.NoError(err)
	t.Cleanup(releaseWrite)
	draining, status := prepareSpokeRequest(t, spokeHTTP)
	require.Equal(http.StatusOK, status)
	assert.False(draining.ReadyToActivate)
	assert.Equal(1, draining.InFlightProviderWrites)
	receipts, err := spokeDB.ListSpokePreparationReceipts(t.Context())
	require.NoError(err)
	assert.Empty(receipts, "provider state must not be handed off while an admitted write is active")

	require.NoError(spokeDB.SetKanbanState(t.Context(), spokeMRID, "waiting"))
	releaseWrite()
	first, status := prepareSpokeRequest(t, spokeHTTP)
	require.Equal(http.StatusOK, status)
	assert.True(first.ReadyToActivate)
	assert.NotEmpty(first.PreparationSeal)
	assert.Empty(first.HandoffConflicts)
	assert.Empty(first.HandoffErrors)
	assert.True(first.RestartRequired)
	receipts, err = spokeDB.ListSpokePreparationReceipts(t.Context())
	require.NoError(err)
	assert.NotEmpty(receipts, "non-empty provider state must traverse the pending handoff client")
	persistedConfig, err := config.Load(spokeConfigPath)
	require.NoError(err)
	assert.Equal(config.FleetRoleSpoke, persistedConfig.Fleet.RoleOrDefault())
	projectRepo, err := spokeDB.GetRepoByID(t.Context(), projectRepoID)
	require.NoError(err)
	require.NotNil(projectRepo)
	assert.NotEmpty(projectRepo.PlatformRepoID)

	localState, err := spokeDB.GetSpokePreparation(t.Context())
	require.NoError(err)
	assert.Equal(db.SpokePreparationSealed, localState.Phase)
	assert.Equal(first.PreparationSeal, localState.PreparationSeal)
	localEnrollment, ok := spokeEnrollments.Local()
	require.True(ok)
	require.NotNil(localEnrollment.Preparation)
	assert.Equal(first.PreparationSeal, localEnrollment.Preparation.Seal)
	assert.Equal(localState.PreparationDigest, localEnrollment.Preparation.PreparationDigest)
	hubSeal, err := hubDB.GetSpokePreparationSeal(
		t.Context(), preparationEnrollmentID,
	)
	require.NoError(err)
	require.NotNil(hubSeal)
	assert.Equal(first.PreparationSeal, hubSeal.Seal)

	retry, status := prepareSpokeRequest(t, spokeHTTP)
	require.Equal(http.StatusOK, status)
	assert.True(retry.ReadyToActivate)
	assert.Equal(first.PreparationSeal, retry.PreparationSeal)
	assert.Equal(config.FleetRoleSpoke, spokeConfig.Fleet.Role)
}

func TestPersistPreparedSpokeRoleKeepsSealAndMembershipGuards(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	enrollments, credentials := openFederationPreparationStores(t, "persist-role")
	local := federation.LocalEnrollment{
		EnrollmentID: preparationEnrollmentID, NodeID: preparationLocalNodeID,
		SpokeBaseURL: "https://spoke.example",
		HubID:        preparationHubNodeID, HubURL: "https://hub.example",
		ProtocolVersion: federation.ProtocolVersion, State: federation.EnrollmentPending,
		ExpiresAt: time.Now().Add(time.Minute), PreparationStarted: true,
		PreparationRequired: true,
	}
	require.NoError(enrollments.SaveLocal(t.Context(), local))
	require.NoError(enrollments.SaveLocalPreparationSeal(
		t.Context(), federation.LocalPreparationSeal{
			EnrollmentID: local.EnrollmentID, NodeID: local.NodeID,
			HubID: local.HubID, ProtocolVersion: local.ProtocolVersion,
			PreparationDigest: "digest", Seal: "sealed-proof",
		},
	))
	configPath := filepath.Join(t.TempDir(), "forge.toml")
	writeConfigToml(t, configPath, fmt.Sprintf(`
host = "127.0.0.1"
port = 8091
data_dir = %q

[api]
require_auth = true

[fleet]
enabled = true
role = "hub"
base_url = "https://hub.example"

[fleet.hub]
node_id = %q
base_url = %q

[[fleet.members]]
node_id = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
base_url = "https://member.example"
state = "active"
`, t.TempDir(), local.HubID, local.HubURL))
	cfg, err := config.Load(configPath)
	require.NoError(err)
	srv := NewWithConfig(dbtest.Open(t), nil, nil, nil, cfg, configPath, ServerOptions{
		FederationEnrollments: enrollments, FederationCredentials: credentials,
		FederationSpokeID: local.NodeID, DisableWorkspaceBackgroundMonitors: true,
	})
	t.Cleanup(func() { gracefulShutdown(t, srv) })
	sealRequest := db.SpokePreparationSealRequest{
		EnrollmentID: local.EnrollmentID, NodeID: local.NodeID,
		HubNodeID: local.HubID, ProtocolVersion: local.ProtocolVersion,
	}
	seal := db.SpokePreparationSeal{
		SpokePreparationSealRequest: sealRequest,
		Seal:                        "sealed-proof",
	}

	mismatched := seal
	mismatched.Seal = "different-proof"
	require.ErrorIs(
		srv.persistPreparedSpokeRole(t.Context(), local, mismatched),
		federation.ErrPreparationSealMismatch,
	)
	require.ErrorContains(
		srv.persistPreparedSpokeRole(t.Context(), local, seal),
		"revoke or migrate hub members",
	)
	unchanged, err := config.Load(configPath)
	require.NoError(err)
	assert.Equal(config.FleetRoleHub, unchanged.Fleet.RoleOrDefault())
	assert.Len(unchanged.Fleet.Members, 1)
}

func TestPersistPreparedSpokeRoleKeepsEnrollmentHubBinding(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	enrollments, credentials := openFederationPreparationStores(t, "joined-spoke")
	local := federation.LocalEnrollment{
		EnrollmentID: preparationEnrollmentID, NodeID: preparationLocalNodeID,
		SpokeBaseURL: "https://spoke.example",
		HubID:        preparationHubNodeID, HubURL: "https://hub.example",
		ProtocolVersion: federation.ProtocolVersion, State: federation.EnrollmentPending,
		ExpiresAt: time.Now().Add(time.Minute), PreparationStarted: true,
		PreparationRequired: true,
	}
	require.NoError(enrollments.SaveLocal(t.Context(), local))
	require.NoError(enrollments.SaveLocalPreparationSeal(
		t.Context(), federation.LocalPreparationSeal{
			EnrollmentID: local.EnrollmentID, NodeID: local.NodeID,
			HubID: local.HubID, ProtocolVersion: local.ProtocolVersion,
			PreparationDigest: "digest", Seal: "sealed-proof",
		},
	))
	configPath := filepath.Join(t.TempDir(), "forge.toml")
	writeConfigToml(t, configPath, fmt.Sprintf(`
host = "127.0.0.1"
port = 8091
data_dir = %q

[api]
require_auth = true

[fleet]
enabled = true
role = "hub"
base_url = "https://spoke.example"
`, t.TempDir()))
	cfg, err := config.Load(configPath)
	require.NoError(err)
	srv := NewWithConfig(dbtest.Open(t), nil, nil, nil, cfg, configPath, ServerOptions{
		FederationEnrollments: enrollments, FederationCredentials: credentials,
		FederationSpokeID: local.NodeID, DisableWorkspaceBackgroundMonitors: true,
	})
	t.Cleanup(func() { gracefulShutdown(t, srv) })
	hub := config.FleetHub{NodeID: local.HubID, Name: "Hub", BaseURL: local.HubURL}
	require.NoError(srv.persistHubBinding(t.Context(), hub))
	seal := db.SpokePreparationSeal{
		EnrollmentID: local.EnrollmentID, NodeID: local.NodeID,
		HubNodeID: local.HubID, ProtocolVersion: local.ProtocolVersion,
		Seal: "sealed-proof",
	}

	require.NoError(srv.persistPreparedSpokeRole(t.Context(), local, seal))

	persisted, err := config.Load(configPath)
	require.NoError(err)
	assert.Equal(config.FleetRoleSpoke, persisted.Fleet.RoleOrDefault())
	assert.Equal(&hub, persisted.Fleet.Hub)
}

func TestSpokePreparationRejectsFilesystemLaunchSpecBeforePersistence(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	database := dbtest.Open(t)
	seedWorkspace(t, database, "invalid-launch-spec", "acme", "widget", db.WorkspaceItemTypePullRequest, 42)
	issuedAt := time.Now().UTC().Truncate(time.Second)
	spec := db.WorkspaceLaunchSpec{
		Version: db.WorkspaceLaunchSpecVersion,
		Repository: db.WorkspaceLaunchRepository{
			Provider: "github", PlatformHost: "github.com", PlatformRepoID: "repo-acme-widget",
			Owner: "acme", Name: "widget", CloneURL: "file:///tmp/acme/widget.git",
			DefaultBranch: "main",
		},
		ItemType: db.WorkspaceItemTypePullRequest, ItemNumber: 42,
		ItemKey: "42", GitHeadRef: "feature/invalid-launch-spec",
		Pull: &db.WorkspaceLaunchPull{
			HeadBranch: "feature/invalid-launch-spec", HeadRepoKind: "same_repo",
			SnapshotRevision: 1,
		},
		SourceVisible: true, IssuedAt: issuedAt,
		SourceVisibleUntil: issuedAt.Add(db.WorkspaceLaunchSpecVisibilityLease),
	}
	encoded, err := json.Marshal(spec)
	require.NoError(err)
	client := providerPlaneClientFunc(func(
		_ context.Context, scope federationauth.Scope, _ *http.Request,
	) (*http.Response, error) {
		assert.Equal(federationauth.ScopeProviderRead, scope)
		return &http.Response{
			StatusCode: http.StatusOK,
			Body:       io.NopCloser(bytes.NewReader(encoded)),
		}, nil
	})
	report := SpokePreparationReport{HandoffErrors: []string{}}
	server := &Server{db: database, now: time.Now}

	server.refreshSpokePreparationLaunchSpecs(t.Context(), client, &report)

	require.Len(report.HandoffErrors, 1)
	assert.Contains(report.HandoffErrors[0], "invalid hub launch specification")
	persisted, err := database.GetWorkspaceLaunchSpec(t.Context(), "invalid-launch-spec")
	require.NoError(err)
	assert.Nil(persisted)
}

func TestSpokePreparationRequiresCredentialBeforePersistingLaunchSpec(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	database := dbtest.Open(t)
	seedWorkspace(t, database, "missing-credential", "acme", "widget", db.WorkspaceItemTypePullRequest, 42)
	issuedAt := time.Now().UTC().Truncate(time.Second)
	spec := db.WorkspaceLaunchSpec{
		Version: db.WorkspaceLaunchSpecVersion,
		Repository: db.WorkspaceLaunchRepository{
			Provider: "github", PlatformHost: "github.com", PlatformRepoID: "repo-acme-widget",
			Owner: "acme", Name: "widget", CloneURL: "https://github.com/acme/widget.git",
			DefaultBranch: "main",
		},
		ItemType: db.WorkspaceItemTypePullRequest, ItemNumber: 42,
		ItemKey: "42", GitHeadRef: "feature/missing-credential",
		Pull: &db.WorkspaceLaunchPull{
			HeadBranch: "feature/missing-credential", HeadRepoKind: "same_repo",
			SnapshotRevision: 1,
		},
		SourceVisible: true, IssuedAt: issuedAt,
		SourceVisibleUntil: issuedAt.Add(db.WorkspaceLaunchSpecVisibilityLease),
	}
	encoded, err := json.Marshal(spec)
	require.NoError(err)
	client := providerPlaneClientFunc(func(
		_ context.Context, _ federationauth.Scope, _ *http.Request,
	) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusOK, Body: io.NopCloser(bytes.NewReader(encoded)),
		}, nil
	})
	report := SpokePreparationReport{HandoffErrors: []string{}}
	server := &Server{
		db: database, now: time.Now, clones: gitclone.New(t.TempDir(), nil),
	}

	server.refreshSpokePreparationLaunchSpecs(t.Context(), client, &report)

	require.Len(report.HandoffErrors, 1)
	assert.Contains(report.HandoffErrors[0], "git credential unavailable")
	persisted, err := database.GetWorkspaceLaunchSpec(t.Context(), "missing-credential")
	require.NoError(err)
	assert.Nil(persisted)
}

func TestSpokePreparationRequiresForkCredentialBeforePersistingLaunchSpec(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	database := dbtest.Open(t)
	seedWorkspace(t, database, "missing-fork-credential", "acme", "widget", db.WorkspaceItemTypePullRequest, 42)
	issuedAt := time.Now().UTC().Truncate(time.Second)
	spec := db.WorkspaceLaunchSpec{
		Version: db.WorkspaceLaunchSpecVersion,
		Repository: db.WorkspaceLaunchRepository{
			Provider: "github", PlatformHost: "github.com", PlatformRepoID: "repo-acme-widget",
			Owner: "acme", Name: "widget", CloneURL: "https://github.com/acme/widget.git",
			DefaultBranch: "main",
		},
		ItemType: db.WorkspaceItemTypePullRequest, ItemNumber: 42,
		ItemKey: "42", GitHeadRef: "feature/missing-fork-credential",
		Pull: &db.WorkspaceLaunchPull{
			HeadBranch: "feature/missing-fork-credential", HeadRepoKind: "fork",
			HeadRepoCloneURL: "https://github.com/contributor/widget.git",
			SnapshotRevision: 1,
		},
		SourceVisible: true, IssuedAt: issuedAt,
		SourceVisibleUntil: issuedAt.Add(db.WorkspaceLaunchSpecVisibilityLease),
	}
	encoded, err := json.Marshal(spec)
	require.NoError(err)
	client := providerPlaneClientFunc(func(
		_ context.Context, _ federationauth.Scope, _ *http.Request,
	) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusOK,
			Body:       io.NopCloser(bytes.NewReader(encoded)),
		}, nil
	})
	report := SpokePreparationReport{HandoffErrors: []string{}}
	server := &Server{
		db: database, now: time.Now,
		clones: gitclone.New(t.TempDir(), descriptorCloneRoutes{
			source: testTokenSource("spoke-git-token"),
		}),
	}

	server.refreshSpokePreparationLaunchSpecs(t.Context(), client, &report)

	require.Len(report.HandoffErrors, 1)
	assert.Contains(report.HandoffErrors[0], "git credential unavailable")
	assert.Contains(report.HandoffErrors[0], "contributor/widget")
	persisted, err := database.GetWorkspaceLaunchSpec(t.Context(), "missing-fork-credential")
	require.NoError(err)
	assert.Nil(persisted)
}

func TestSpokePreparationRefreshFollowsStableRepositoryRename(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	database := dbtest.Open(t)
	const workspaceID = "renamed-launch-spec"
	seedWorkspace(t, database, workspaceID, "acme", "widget", db.WorkspaceItemTypePullRequest, 42)
	now := time.Now().UTC().Truncate(time.Second)
	current := db.WorkspaceLaunchSpec{
		Version: db.WorkspaceLaunchSpecVersion,
		Repository: db.WorkspaceLaunchRepository{
			Provider: "github", PlatformHost: "github.com", PlatformRepoID: "repo-acme-widget",
			Owner: "acme", Name: "widget", CloneURL: "https://github.com/acme/widget.git",
			DefaultBranch: "main",
		},
		ItemType: db.WorkspaceItemTypePullRequest, ItemNumber: 42,
		ItemKey: "42", GitHeadRef: "feature/" + workspaceID,
		Pull: &db.WorkspaceLaunchPull{
			HeadBranch: "feature/" + workspaceID, HeadRepoKind: "same_repo",
			SnapshotRevision: 1,
		},
		SourceVisible: true,
		IssuedAt:      now.Add(-db.WorkspaceLaunchSpecVisibilityLease - time.Minute),
	}
	current.SourceVisibleUntil = current.IssuedAt.Add(db.WorkspaceLaunchSpecVisibilityLease)
	_, accepted, err := database.ReconcileRepositoryObservation(
		t.Context(), db.RepoIdentity{
			Platform: "github", PlatformHost: "github.com",
			PlatformRepoID: current.Repository.PlatformRepoID,
			Owner:          current.Repository.Owner, Name: current.Repository.Name,
		}, current.IssuedAt,
	)
	require.NoError(err)
	require.True(accepted)
	require.NoError(database.PutWorkspaceLaunchSpec(t.Context(), workspaceID, current))

	refreshed := current
	refreshed.Repository.Owner = "acme-renamed"
	refreshed.Repository.Name = "widget-renamed"
	refreshed.Repository.CloneURL = "https://github.com/acme-renamed/widget-renamed.git"
	refreshed.IssuedAt = now
	refreshed.SourceVisibleUntil = now.Add(db.WorkspaceLaunchSpecVisibilityLease)
	_, accepted, err = database.ReconcileRepositoryObservation(
		t.Context(), db.RepoIdentity{
			Platform: "github", PlatformHost: "github.com",
			PlatformRepoID: refreshed.Repository.PlatformRepoID,
			Owner:          refreshed.Repository.Owner, Name: refreshed.Repository.Name,
		}, now,
	)
	require.NoError(err)
	require.True(accepted)
	encoded, err := json.Marshal(refreshed)
	require.NoError(err)
	client := providerPlaneClientFunc(func(
		_ context.Context, _ federationauth.Scope, request *http.Request,
	) (*http.Response, error) {
		var body providerplane.WorkspaceLaunchRequest
		require.NoError(json.NewDecoder(request.Body).Decode(&body))
		assert.Equal(current.Repository.PlatformRepoID, body.PlatformRepoID)
		return &http.Response{
			StatusCode: http.StatusOK,
			Body:       io.NopCloser(bytes.NewReader(encoded)),
		}, nil
	})
	report := SpokePreparationReport{HandoffErrors: []string{}}
	const tokenEnv = "KENN_FORGE_TEST_PREPARATION_GIT_TOKEN"
	t.Setenv(tokenEnv, "token")
	source := tokenauth.NewManagedSource(tokenauth.Descriptor{
		Key: tokenauth.Key{Platform: "github", Host: "github.com"},
		Candidates: []tokenauth.Candidate{{
			Kind: tokenauth.SourceKindEnv, EnvName: tokenEnv,
		}},
	}, tokenauth.Options{})
	server := &Server{
		db: database, now: func() time.Time { return now },
		clones: gitclone.New(t.TempDir(), gitclone.HostSources{"github.com": source}),
	}

	server.refreshSpokePreparationLaunchSpecs(t.Context(), client, &report)

	assert.Empty(report.HandoffErrors)
	workspace, err := database.GetWorkspace(t.Context(), workspaceID)
	require.NoError(err)
	require.NotNil(workspace)
	assert.Equal("acme-renamed", workspace.RepoOwner)
	assert.Equal("widget-renamed", workspace.RepoName)
}

func TestHubPreparationSealMustMatchRequestedBinding(t *testing.T) {
	require := require.New(t)
	request := db.SpokePreparationSealRequest{
		EnrollmentID: preparationEnrollmentID, NodeID: preparationLocalNodeID,
		HubNodeID:        preparationHubNodeID,
		ProtocolVersion:  federation.ProtocolVersion,
		MigrationVersion: db.WorkspaceLaunchSpecMigrationVersion,
		ReceiptsDigest:   "receipts", DrainedAckGeneration: 1,
	}
	var err error
	request.PreparationDigest, err = db.SpokePreparationSealDigest(request)
	require.NoError(err)
	valid := db.SpokePreparationSeal{
		SpokePreparationSealRequest: request,
		Seal:                        "opaque-seal", CreatedAt: time.Now().UTC(),
	}
	require.NoError(validateHubPreparationSeal(request, valid))

	different := valid
	different.ReceiptsDigest = "different"
	require.Error(validateHubPreparationSeal(request, different))
	incomplete := valid
	incomplete.Seal = ""
	require.Error(validateHubPreparationSeal(request, incomplete))
}
