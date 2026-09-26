package db

import (
	"database/sql"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestIntegerRepositoryIdentityMigration59(t *testing.T) {
	t.Parallel()
	assert := assert.New(t)
	require := require.New(t)
	dbPath := filepath.Join(t.TempDir(), "repository-identity-v58.db")

	openAtVersionForTest(t, dbPath, 58, func(raw *sql.DB) {
		_, err := raw.ExecContext(t.Context(), `
			INSERT INTO forge_repos (
				id, platform, platform_host, platform_repo_id,
				owner, name, repo_path, owner_key, name_key, repo_path_key,
				lifecycle_state
			) VALUES
				(1, 'github', 'github.com', 'R_kgDOexample',
				 'org-a', 'project-a', 'org-a/project-a',
				 'org-a', 'project-a', 'org-a/project-a', 'active'),
				(2, 'gitlab', 'gitlab.com', '250833',
				 'Group-B', 'Project-B', 'Group-B/Project-B',
				 'group-b', 'project-b', 'group-b/project-b', 'active'),
				(3, 'forgejo', 'codeberg.org', 'forgejo-project-c',
				 'org-c', 'project-c', 'org-c/project-c',
				 'org-c', 'project-c', 'org-c/project-c', 'active'),
				(4, 'gitea', 'gitea.com', '',
				 'org-d', 'project-d', 'org-d/project-d',
				 'org-d', 'project-d', 'org-d/project-d', 'inactive');
			INSERT INTO forge_repo_routes (
				repo_id, platform, platform_host, owner, name, repo_path,
				owner_key, name_key, repo_path_key, is_current,
				first_seen_at, last_seen_at
			) VALUES
				(1, 'github', 'github.com', 'org-a', 'project-a', 'org-a/project-a',
				 'org-a', 'project-a', 'org-a/project-a', 1,
				 datetime('now'), datetime('now'));
			INSERT INTO forge_merge_requests (
				id, repo_id, platform_id, number,
				created_at, updated_at, last_activity_at
			) VALUES
				(10, 1, 9001, 7, datetime('now'), datetime('now'), datetime('now')),
				(11, 3, 9002, 8, datetime('now'), datetime('now'), datetime('now'));
			INSERT INTO forge_workspaces (
				id, platform, platform_host, repo_owner, repo_name,
				repo_owner_key, repo_name_key, repo_path_key, repo_id,
				item_type, item_number, item_key, git_head_ref,
				workspace_branch, worktree_path, tmux_session, status
			) VALUES
				('ws-gitlab', 'gitlab', 'gitlab.com', 'Group-B', 'Project-B',
				 'group-b', 'project-b', 'group-b/project-b', 2,
				 'pull_request', 5, '5', 'feature/five',
				 'feature/five', '/tmp/ws-gitlab', 'ws-gitlab', 'ready'),
				('ws-github', 'github', 'github.com', 'org-a', 'project-a',
				 'org-a', 'project-a', 'org-a/project-a', 1,
				 'pull_request', 7, '7', 'feature/seven',
				 'feature/seven', '/tmp/ws-github', 'ws-github', 'ready'),
				('ws-legacy', 'gitea', 'gitea.com', 'org-d', 'project-d',
				 'org-d', 'project-d', 'org-d/project-d', 4,
				 'issue', 6, '6', 'work/issue-6',
				 'work/issue-6', '/tmp/ws-legacy', 'ws-legacy', 'ready');
			INSERT INTO forge_workspace_launch_specs (
				workspace_id, version, spec_json, source_visible_until, created_at
			) VALUES (
				'ws-gitlab', 1,
				'{"repository":{"provider":"gitlab","platform_host":"gitlab.com","platform_repo_id":"250833"},"pull":{"base_repo_id":"250833"}}',
				datetime('now'), datetime('now')
			), (
				'ws-github', 1,
				'{"repository":{"provider":"github","platform_host":"github.com","platform_repo_id":"R_kgDOexample"},"pull":{"base_repo_id":"R_kgDOexample"}}',
				datetime('now'), datetime('now')
			);
		`)
		require.NoError(err)
	})

	database, err := Open(dbPath)
	require.NoError(err)
	t.Cleanup(func() { _ = database.Close() })
	read := database.ReadDB()

	type row struct {
		id             int64
		platformRepoID int64
		githubNodeID   string
		lifecycle      string
	}
	readRepos := func() []row {
		rows, err := read.QueryContext(t.Context(), `
			SELECT id, platform_repo_id, github_node_id, lifecycle_state
			FROM forge_repos ORDER BY id`)
		require.NoError(err)
		defer rows.Close()
		var repos []row
		for rows.Next() {
			var r row
			require.NoError(rows.Scan(&r.id, &r.platformRepoID, &r.githubNodeID, &r.lifecycle))
			repos = append(repos, r)
		}
		require.NoError(rows.Err())
		return repos
	}
	assert.Equal([]row{
		{id: 1, platformRepoID: 0, githubNodeID: "R_kgDOexample", lifecycle: "inactive"},
		{id: 2, platformRepoID: 250833, githubNodeID: "", lifecycle: "active"},
	}, readRepos(), "decimal IDs convert, GitHub node IDs wait for conversion, unkeyable rows are deleted")

	readMergeRequests := func() []int64 {
		rows, err := read.QueryContext(t.Context(), `SELECT id FROM forge_merge_requests ORDER BY id`)
		require.NoError(err)
		defer rows.Close()
		var ids []int64
		for rows.Next() {
			var id int64
			require.NoError(rows.Scan(&id))
			ids = append(ids, id)
		}
		require.NoError(rows.Err())
		return ids
	}
	assert.Equal([]int64{10}, readMergeRequests(), "a pending GitHub repository keeps its data; a deleted row takes its own")

	var legacyRepoID sql.NullInt64
	require.NoError(read.QueryRowContext(t.Context(),
		`SELECT repo_id FROM forge_workspaces WHERE id = 'ws-legacy'`,
	).Scan(&legacyRepoID))
	assert.False(legacyRepoID.Valid, "a workspace of a deleted repository is detached, not deleted")

	specIDTypes := func(workspaceID string) (string, string) {
		var repoIDType, baseRepoIDType string
		require.NoError(read.QueryRowContext(t.Context(), `
			SELECT json_type(spec_json, '$.repository.platform_repo_id'),
			       json_type(spec_json, '$.pull.base_repo_id')
			FROM forge_workspace_launch_specs WHERE workspace_id = ?`, workspaceID,
		).Scan(&repoIDType, &baseRepoIDType))
		return repoIDType, baseRepoIDType
	}
	var specRepoID, specBaseRepoID any
	require.NoError(read.QueryRowContext(t.Context(), `
		SELECT json_extract(spec_json, '$.repository.platform_repo_id'),
		       json_extract(spec_json, '$.pull.base_repo_id')
		FROM forge_workspace_launch_specs WHERE workspace_id = 'ws-gitlab'`,
	).Scan(&specRepoID, &specBaseRepoID))
	repoIDType, baseRepoIDType := specIDTypes("ws-gitlab")
	assert.Equal("integer", repoIDType)
	assert.Equal("integer", baseRepoIDType)
	assert.EqualValues(250833, specRepoID)
	assert.EqualValues(250833, specBaseRepoID)
	repoIDType, baseRepoIDType = specIDTypes("ws-github")
	assert.Equal("integer", repoIDType, "GitHub node IDs become 0 until their repository converts")
	assert.Equal("integer", baseRepoIDType)

	var routeTables int
	require.NoError(read.QueryRowContext(t.Context(), `
		SELECT COUNT(*) FROM sqlite_master WHERE name = 'forge_repo_routes'`,
	).Scan(&routeTables))
	assert.Zero(routeTables)
	assertDatabaseIntegrityForTest(t, read)
}
