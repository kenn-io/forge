package db

import (
	"database/sql"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestWorkspaceTargetVisibilityMigrationPreservesExistingLinks(t *testing.T) {
	t.Parallel()
	dbPath := filepath.Join(t.TempDir(), "targets-v64.db")
	openAtVersionForTest(t, dbPath, 64, func(raw *sql.DB) {
		_, err := raw.ExecContext(t.Context(), `
			INSERT INTO forge_repos (id, platform, platform_host, platform_repo_id, owner, name, repo_path,
				owner_key, name_key, repo_path_key, lifecycle_state)
			VALUES (1, 'github', 'github.com', 101, 'acme', 'widgets', 'acme/widgets', 'acme', 'widgets', 'acme/widgets', 'active');
			INSERT INTO forge_workspaces (id, repo_id, platform_host, repo_owner, repo_name, item_number,
				git_head_ref, worktree_path, tmux_session, item_key)
			VALUES ('ws-existing', 1, 'github.com', 'acme', 'widgets', 7, 'feature', '/tmp/ws-existing', '', '7');
			INSERT INTO forge_workspace_targets (id, workspace_id, repo_id, item_type, item_number, url, created_at)
			VALUES (1, 'ws-existing', 1, 'issue', 42, 'https://github.com/acme/widgets/issues/42', '2026-01-01 00:00:00');
		`)
		require.NoError(t, err)
	})
	database, err := Open(dbPath)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, database.Close()) })
	targets, err := database.ListWorkspaceTargets(t.Context(), "ws-existing")
	require.NoError(t, err)
	require.Len(t, targets, 1)
	assert.Equal(t, 42, targets[0].ItemNumber)
	assert.False(t, targets[0].Hidden)
	assert.Equal(t, "https://github.com/acme/widgets/issues/42", targets[0].URL)
}
