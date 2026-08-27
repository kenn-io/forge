package db

import (
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestWorkspaceAgentLaunchesMigrationUpgradesV52(t *testing.T) {
	databasePath := filepath.Join(t.TempDir(), "forge-v52.db")
	openAtVersionForTest(t, databasePath, 52, func(*sql.DB) {})

	database, err := Open(databasePath)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, database.Close()) })

	_, found, err := database.PreferredWorkspaceAgentTarget(
		t.Context(), time.Time{}, []string{"codex"},
	)
	require.NoError(t, err)
	assert.False(t, found)
	assertDatabaseIntegrityForTest(t, database.ReadDB())
}

func TestPreferredWorkspaceAgentTargetUsesRecentDistinctLaunches(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	database := openTestDB(t)
	ctx := t.Context()
	require.NoError(database.InsertWorkspace(ctx, &Workspace{
		ID: "ws-agent-usage", PlatformHost: "github.com",
		RepoOwner: "acme", RepoName: "widget",
		ItemType: WorkspaceItemTypePullRequest, ItemNumber: 42,
		GitHeadRef: "feature/agent-usage", WorkspaceBranch: "kenn-forge/pr-42",
		WorktreePath: "/tmp/ws-agent-usage", TmuxSession: "ws-agent-usage",
		Status: "ready",
	}))

	cutoff := time.Date(2026, 8, 13, 12, 0, 0, 0, time.UTC)
	record := func(key, target string, createdAt time.Time) {
		t.Helper()
		require.NoError(database.RecordWorkspaceRuntimeSession(ctx, &WorkspaceRuntimeSession{
			WorkspaceID: "ws-agent-usage", SessionKey: key,
			TargetKey: target, Label: target, Kind: "agent", Scope: "session",
			CreatedAt: createdAt,
		}))
	}

	record("old-opencode", "opencode", cutoff.Add(-time.Minute))
	record("codex-a", "codex", cutoff.Add(24*time.Hour))
	record("codex-a", "codex", cutoff.Add(24*time.Hour))
	record("codex-b", "codex", cutoff.Add(25*time.Hour))
	record("claude", "claude", cutoff.Add(48*time.Hour))
	record("gemini-a", "gemini", cutoff.Add(20*time.Hour))
	record("gemini-b", "gemini", cutoff.Add(30*time.Hour))
	require.NoError(database.DeleteWorkspaceRuntimeSession(ctx, "ws-agent-usage", "claude"))

	target, found, err := database.PreferredWorkspaceAgentTarget(
		ctx, cutoff, []string{"codex", "claude", "gemini"},
	)
	require.NoError(err)
	assert.True(found)
	assert.Equal("gemini", target)

	target, found, err = database.PreferredWorkspaceAgentTarget(
		ctx, cutoff, []string{"codex", "claude"},
	)
	require.NoError(err)
	assert.True(found)
	assert.Equal("codex", target)

	_, found, err = database.PreferredWorkspaceAgentTarget(
		ctx, cutoff, []string{"opencode"},
	)
	require.NoError(err)
	assert.False(found)
}
