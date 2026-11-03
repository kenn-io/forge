package db

import (
	"database/sql"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestMigration84BackfillsEventRepository(t *testing.T) {
	t.Parallel()
	require := require.New(t)
	assert := assert.New(t)
	path := filepath.Join(t.TempDir(), "mention-authors-v83.db")
	openAtVersionForTest(t, path, 83, func(raw *sql.DB) {
		_, err := raw.ExecContext(t.Context(), `
			INSERT INTO forge_repos (id, platform_repo_id, owner, name, repo_path, owner_key, name_key, repo_path_key) VALUES
			(1, 1001, 'acme', 'widgets', 'acme/widgets', 'acme', 'widgets', 'acme/widgets'),
			(2, 1002, 'acme', 'tools', 'acme/tools', 'acme', 'tools', 'acme/tools');
			INSERT INTO forge_merge_requests (id, repo_id, platform_id, number, created_at, updated_at, last_activity_at) VALUES
			(1, 1, 101, 1, datetime('now'), datetime('now'), datetime('now')),
			(2, 2, 102, 1, datetime('now'), datetime('now'), datetime('now'));
			INSERT INTO forge_issues (id, repo_id, platform_id, number, created_at, updated_at, last_activity_at) VALUES
			(1, 1, 201, 1, datetime('now'), datetime('now'), datetime('now')),
			(2, 2, 202, 1, datetime('now'), datetime('now'), datetime('now'));
			INSERT INTO forge_mr_events (merge_request_id, event_type, author, created_at, dedupe_key) VALUES
			(1, 'comment', 'widget-reviewer', datetime('now'), 'comment'),
			(2, 'comment', 'tool-reviewer', datetime('now'), 'comment');
			INSERT INTO forge_issue_events (issue_id, event_type, author, created_at, dedupe_key) VALUES
			(1, 'comment', 'widget-commenter', datetime('now'), 'comment'),
			(2, 'comment', 'tool-commenter', datetime('now'), 'comment');
		`)
		require.NoError(err)
	})
	d, err := Open(path)
	require.NoError(err)
	t.Cleanup(func() { d.Close() })
	for _, query := range []string{
		`SELECT COUNT(*) FROM forge_mr_events e JOIN forge_merge_requests mr ON mr.id = e.merge_request_id WHERE e.repo_id <> mr.repo_id`,
		`SELECT COUNT(*) FROM forge_issue_events e JOIN forge_issues i ON i.id = e.issue_id WHERE e.repo_id <> i.repo_id`,
	} {
		var mismatches int
		require.NoError(d.ReadDB().QueryRowContext(t.Context(), query).Scan(&mismatches))
		assert.Zero(mismatches)
	}
	for _, tc := range []struct {
		repo  string
		users []string
	}{
		{"widgets", []string{"widget-commenter", "widget-reviewer"}},
		{"tools", []string{"tool-commenter", "tool-reviewer"}},
	} {
		users, err := d.ListCommentAutocompleteUsers(t.Context(), "github", "github.com", "acme", tc.repo, "", nil, 10)
		require.NoError(err)
		assert.Equal(tc.users, users)
	}
	assertDatabaseIntegrityForTest(t, d.ReadDB())
}
