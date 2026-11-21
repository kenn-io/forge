package db

import (
	"database/sql"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestWorkspaceViewStateUpgradeAndPersistence(t *testing.T) {
	t.Parallel()
	require := require.New(t)
	assert := assert.New(t)
	dbPath := filepath.Join(t.TempDir(), "test.db")
	raw, migrator := openMigratorForTest(t, dbPath)
	require.NoError(migrator.Migrate(60))
	require.NoError(raw.Close())
	previous, err := OpenPreparedForTest(dbPath)
	require.NoError(err)
	// Schema 60 lacks columns the current catalog and workspace helpers write,
	// so they cannot seed this upgrade fixture.
	_, err = previous.WriteDB().ExecContext(t.Context(), `
		INSERT INTO forge_repos (
			platform, platform_host, platform_repo_id,
			owner, name, repo_path, owner_key, name_key, repo_path_key,
			lifecycle_state
		) VALUES (
			'github', 'github.com', 1001,
			'acme', 'widget', 'acme/widget', 'acme', 'widget', 'acme/widget',
			'active'
		)`)
	require.NoError(err)
	workspace := &Workspace{
		ID: "work-a", Platform: "github", PlatformHost: "github.com",
		RepoOwner: "acme", RepoName: "widget",
		ItemType: WorkspaceItemTypePullRequest, ItemNumber: 7,
		ItemKey: "7", GitHeadRef: "feature/seven", WorkspaceBranch: "feature/seven",
		WorktreePath: "/tmp/work-a", TmuxSession: "work-a", Status: "ready",
	}
	_, err = previous.WriteDB().ExecContext(t.Context(), `
		INSERT INTO forge_workspaces (
			id, platform, platform_host, repo_owner, repo_name,
			item_type, item_number, item_key, git_head_ref,
			workspace_branch, worktree_path, tmux_session, status
		) VALUES (
			'work-a', 'github', 'github.com', 'acme', 'widget',
			'pull_request', 7, '7', 'feature/seven',
			'feature/seven', '/tmp/work-a', 'work-a', 'ready'
		)`)
	require.NoError(err)
	require.NoError(previous.Close())

	database, err := Open(dbPath)
	require.NoError(err)
	t.Cleanup(func() { _ = database.Close() })
	activeTab, err := database.GetWorkspaceActiveTab(t.Context(), "work-a")
	require.NoError(err)
	assert.Empty(activeTab)
	require.NoError(database.SetWorkspaceActiveTab(t.Context(), "work-a", "session:agent-a"))
	require.NoError(database.Close())
	database, err = Open(dbPath)
	require.NoError(err)
	activeTab, err = database.GetWorkspaceActiveTab(t.Context(), "work-a")
	require.NoError(err)
	assert.Equal("session:agent-a", activeTab)

	require.NoError(database.DeleteWorkspace(t.Context(), "work-a"))
	_, err = database.GetWorkspaceActiveTab(t.Context(), "work-a")
	require.ErrorIs(err, sql.ErrNoRows)
	require.ErrorIs(database.SetWorkspaceActiveTab(t.Context(), "missing", "home"), sql.ErrNoRows)
	require.NoError(database.InsertWorkspace(t.Context(), workspace))
	activeTab, err = database.GetWorkspaceActiveTab(t.Context(), "work-a")
	require.NoError(err)
	assert.Empty(activeTab, "deleting a workspace removes its selected tab")
	assertDatabaseIntegrityForTest(t, database.ReadDB())
}
