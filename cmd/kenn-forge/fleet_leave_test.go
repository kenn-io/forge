package main

import (
	"io"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/forge/internal/config"
	"go.kenn.io/forge/internal/db"
	"go.kenn.io/forge/internal/federation"
	"go.kenn.io/forge/internal/federationauth"
	"go.kenn.io/forge/internal/providerplane"
	"go.kenn.io/forge/internal/runtimelock"
	"go.kenn.io/forge/internal/testutil/dbtest"
)

func TestFleetLeavePreservesLocalStateAndRestoresStandaloneStartup(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	path, cfg, enrollments := fleetLeaveFixture(t, federation.EnrollmentRevoked)
	nodeID, err := runtimelock.EnsureNodeID(cfg.DataDir)
	require.NoError(err)
	database := dbtest.OpenAt(t, cfg.DBPath())
	workspace := &db.Workspace{
		ID: "local-work", PlatformHost: "github.com", RepoOwner: "acme", RepoName: "widget",
		ItemType: db.WorkspaceItemTypePullRequest, ItemNumber: 42,
		WorktreePath: t.TempDir(), TmuxSession: "forge-local-work", Status: "ready",
	}
	require.NoError(database.InsertWorkspace(t.Context(), workspace))
	session := db.WorkspaceRuntimeSession{
		WorkspaceID: workspace.ID, SessionKey: "agent-1", Kind: "agent",
		TmuxSession: "forge-agent-1", CreatedAt: time.Now().UTC(),
	}
	require.NoError(database.UpsertWorkspaceRuntimeSession(t.Context(), &session))
	_, err = database.BeginSpokePreparation(t.Context(), db.SpokePreparationBinding{
		EnrollmentID: startupEnrollmentID, LocalNodeID: nodeID,
		HubNodeID: startupHubID, ProtocolVersion: federation.ProtocolVersion,
	})
	require.NoError(err)
	require.NoError(database.Close())
	credentials, err := federationauth.Open(federationauth.DefaultStorePath(cfg.DataDir))
	require.NoError(err)
	require.NoError(credentials.StoreInbound(startupHubID, "old-hub-token", federationauth.HubToSpokeScopes()))
	require.NoError(credentials.StoreOutbound(startupHubID, "old-spoke-token", federationauth.SpokeToHubScopes()))
	_, authenticated := credentials.Authenticate("old-hub-token")
	require.True(authenticated)

	for range 2 { // Retrying an interrupted leave must not need the old hub.
		command := newFleetCommand(fleetCLIOptions{Stdout: io.Discard})
		command.SetArgs([]string{"leave", "--config", path})
		require.NoError(command.ExecuteContext(t.Context()))
	}

	standalone, err := config.Load(path)
	require.NoError(err)
	assert.False(standalone.Fleet.Enabled)
	assert.Equal(config.FleetRoleHub, standalone.Fleet.RoleOrDefault())
	assert.Nil(standalone.Fleet.Hub)
	assert.Equal(cfg.DataDir, standalone.DataDir)
	retainedID, err := runtimelock.EnsureNodeID(standalone.DataDir)
	require.NoError(err)
	assert.Equal(nodeID, retainedID)
	database = dbtest.OpenPreparedAt(t, standalone.DBPath())
	retained, err := database.GetWorkspace(t.Context(), workspace.ID)
	require.NoError(err)
	require.NotNil(retained)
	assert.Equal(workspace.WorktreePath, retained.WorktreePath)
	assert.Equal(workspace.TmuxSession, retained.TmuxSession)
	assert.Equal("ready", retained.Status)
	sessions, err := database.ListWorkspaceRuntimeSessions(t.Context(), workspace.ID)
	require.NoError(err)
	require.Len(sessions, 1)
	assert.Equal(session.SessionKey, sessions[0].SessionKey)
	assert.Equal(session.TmuxSession, sessions[0].TmuxSession)
	credentials, err = federationauth.Open(credentials.Path())
	require.NoError(err)
	_, authenticated = credentials.Authenticate("old-hub-token")
	assert.False(authenticated)
	_, outbound := credentials.Outbound(startupHubID)
	assert.False(outbound)
	startup := activateFederationSpokeAtStartup(t.Context(), database, standalone, nodeID, enrollments, credentials, nil)
	assert.Equal(federationStartupHub, startup.State)
	release, err := providerplane.NewProviderWriteGate(database, true).Admit(t.Context())
	require.NoError(err)
	release()
}

func TestFleetLeaveRejectsUnrevokedEnrollmentOrRunningDaemon(t *testing.T) {
	for _, state := range []federation.EnrollmentState{federation.EnrollmentPending, federation.EnrollmentActive, federation.EnrollmentRevoked} {
		t.Run(string(state), func(t *testing.T) {
			assert := assert.New(t)
			require := require.New(t)
			path, cfg, _ := fleetLeaveFixture(t, state)
			if state == federation.EnrollmentRevoked {
				lock, err := runtimelock.Acquire(cfg.DataDir)
				require.NoError(err)
				t.Cleanup(func() { require.NoError(lock.Release()) })
			}
			command := newFleetCommand(fleetCLIOptions{Stdout: io.Discard})
			command.SetArgs([]string{"leave", "--config", path})
			err := command.ExecuteContext(t.Context())
			if state == federation.EnrollmentRevoked {
				require.ErrorContains(err, "stop the Forge daemon")
			} else {
				require.ErrorContains(err, "revoke")
			}
			unchanged, err := config.Load(path)
			require.NoError(err)
			assert.Equal(cfg.Fleet, unchanged.Fleet)
			_, err = os.Stat(cfg.DBPath())
			assert.ErrorIs(err, os.ErrNotExist)
		})
	}
}

func fleetLeaveFixture(t *testing.T, state federation.EnrollmentState) (string, *config.Config, *federation.Store) {
	t.Helper()
	require := require.New(t)
	directory := t.TempDir()
	path := filepath.Join(directory, "config.toml")
	require.NoError(os.WriteFile(path, []byte("data_dir = "+strconv.Quote(directory)+"\n"), 0o600))
	cfg, err := config.Load(path)
	require.NoError(err)
	cfg.API.RequireAuth = true
	cfg.Fleet = config.Fleet{
		Enabled: true, Role: config.FleetRoleSpoke, BaseURL: "https://spoke.example",
		Hub: &config.FleetHub{NodeID: startupHubID, BaseURL: "https://hub.example"},
	}
	require.NoError(cfg.Save(path))
	nodeID, err := runtimelock.EnsureNodeID(directory)
	require.NoError(err)
	enrollments, err := federation.Open(federation.DefaultStorePath(directory), federation.StoreOptions{})
	require.NoError(err)
	require.NoError(enrollments.SaveLocal(t.Context(), federation.LocalEnrollment{
		EnrollmentID: startupEnrollmentID, NodeID: nodeID,
		SpokeBaseURL: cfg.Fleet.BaseURL, HubID: startupHubID, HubURL: cfg.Fleet.Hub.BaseURL,
		ProtocolVersion: federation.ProtocolVersion, State: state, ExpiresAt: time.Now().Add(time.Hour),
	}))
	return path, cfg, enrollments
}
