package db

import (
	"database/sql"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSetupFinishedMigrationMarksExistingInstalls(t *testing.T) {
	const repo = `INSERT INTO forge_repos (
		id, platform, platform_host, platform_repo_id,
		owner, name, repo_path, owner_key, name_key, repo_path_key, lifecycle_state
	) VALUES (1, 'github', 'github.com', 101, 'org-a', 'project-a', 'org-a/project-a',
		'org-a', 'project-a', 'org-a/project-a', 'active');`
	workspace := func(status string) string {
		return repo + `INSERT INTO forge_workspaces (
			id, platform, platform_host, repo_owner, repo_name,
			repo_owner_key, repo_name_key, repo_path_key, repo_id,
			item_type, item_number, item_key, git_head_ref,
			workspace_branch, worktree_path, tmux_session, status
		) VALUES ('ws-a', 'github', 'github.com', 'org-a', 'project-a',
			'org-a', 'project-a', 'org-a/project-a', 1,
			'pull_request', 7, '7', 'feature', 'feature', '/tmp/ws-a', 'ws-a', '` + status + `');`
	}
	for _, test := range []struct {
		name string
		seed string
		want bool
	}{
		{name: "fresh install"},
		{name: "stored pull", seed: repo + `INSERT INTO forge_merge_requests (
			id, repo_id, platform_id, number, created_at, updated_at, last_activity_at
		) VALUES (10, 1, 9001, 7, datetime('now'), datetime('now'), datetime('now'));`, want: true},
		{name: "stored issue", seed: repo + `INSERT INTO forge_issues (
			id, repo_id, platform_id, number, created_at, updated_at, last_activity_at
		) VALUES (20, 1, 9101, 9, datetime('now'), datetime('now'), datetime('now'));`, want: true},
		{name: "ready workspace", seed: workspace("ready"), want: true},
		{name: "creating workspace", seed: workspace("creating")},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			require := require.New(t)
			dbPath := filepath.Join(t.TempDir(), "forge.db")
			openAtVersionForTest(t, dbPath, 68, func(raw *sql.DB) {
				if test.seed != "" {
					_, err := raw.ExecContext(t.Context(), test.seed)
					require.NoError(err)
				}
			})
			database, err := Open(dbPath)
			require.NoError(err)
			t.Cleanup(func() { _ = database.Close() })
			value, found, err := database.AppMetadataValue(t.Context(), "telemetry.setup_finished")
			require.NoError(err)
			assert.Equal(t, test.want, found)
			if test.want {
				assert.Equal(t, "existing", value)
			}
		})
	}
}
