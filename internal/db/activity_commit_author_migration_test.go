package db

import (
	"database/sql"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestMigration76BackfillsCommitAuthor(t *testing.T) {
	t.Parallel()
	require := require.New(t)
	assert := assert.New(t)
	path := filepath.Join(t.TempDir(), "commit-author-v75.db")
	openAtVersionForTest(t, path, 75, func(raw *sql.DB) {
		_, err := raw.ExecContext(t.Context(), `
			INSERT INTO forge_repos (id, platform_repo_id, owner, name, repo_path, owner_key, name_key, repo_path_key)
			VALUES (1, 1001, 'acme', 'widgets', 'acme/widgets', 'acme', 'widgets', 'acme/widgets');
			INSERT INTO forge_merge_requests (id, repo_id, platform_id, number, created_at, updated_at, last_activity_at)
			VALUES (1, 1, 101, 1, datetime('now'), datetime('now'), datetime('now'));
			INSERT INTO forge_mr_events (merge_request_id, event_type, author, summary, metadata_json, created_at, dedupe_key) VALUES
			(1, 'commit', 'integrator', 'aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa', '{"commit_author":"original-login","commit_order":1}', datetime('now'), 'original'),
			(1, 'commit', 'fallback', 'bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb', '', datetime('now'), 'empty'),
			(1, 'issue_comment', 'commenter', '', '{"commit_author":"original-login"}', datetime('now'), 'comment');
		`)
		require.NoError(err)
	})
	d, err := Open(path)
	require.NoError(err)
	t.Cleanup(func() { d.Close() })
	for _, tc := range []struct{ key, author string }{
		{"original", "original-login"},
		{"empty", ""},
		{"comment", ""},
	} {
		var author string
		require.NoError(d.ReadDB().QueryRowContext(t.Context(), `SELECT commit_author FROM forge_mr_events WHERE dedupe_key = ?`, tc.key).Scan(&author))
		assert.Equal(tc.author, author, tc.key)
	}
	assertDatabaseIntegrityForTest(t, d.ReadDB())
}
