package db

import (
	"database/sql"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestMergeRequestAuthorTypeMigration(t *testing.T) {
	path := filepath.Join(t.TempDir(), "migration.db")
	openAtVersionForTest(t, path, 95, func(raw *sql.DB) {
		_, err := raw.ExecContext(t.Context(), `INSERT INTO forge_repos (id, platform_repo_id, owner, name, repo_path, owner_key, name_key, repo_path_key) VALUES (1, 1, 'acme', 'repo', 'acme/repo', 'acme', 'repo', 'acme/repo'); INSERT INTO forge_merge_requests (id, repo_id, platform_id, number, created_at, updated_at, last_activity_at) VALUES (1, 1, 1, 1, datetime('now'), datetime('now'), datetime('now'))`)
		require.NoError(t, err)
	})
	d, err := Open(path)
	require.NoError(t, err)
	t.Cleanup(func() { d.Close() })
	var value *string
	require.NoError(t, d.ReadDB().QueryRowContext(t.Context(), `SELECT author_account_type FROM forge_merge_requests WHERE id=1`).Scan(&value))
	require.Nil(t, value)
}

func TestMergeRequestReferenceMigrationPinsMatchingSources(t *testing.T) {
	path := filepath.Join(t.TempDir(), "references.db")
	openAtVersionForTest(t, path, 95, func(raw *sql.DB) {
		_, err := raw.ExecContext(t.Context(), `
INSERT INTO forge_repos (id,platform_repo_id,owner,name,repo_path,owner_key,name_key,repo_path_key) VALUES
 (1,1,'acme','one','acme/one','acme','one','acme/one'),
 (2,2,'acme','two','acme/two','acme','two','acme/two'),
 (3,3,'acme','ambiguous','acme/ambiguous','acme','ambiguous','acme/ambiguous');
INSERT INTO forge_repos (id,platform_repo_id,owner,name,repo_path,owner_key,name_key,repo_path_key,lifecycle_state) VALUES
 (4,4,'acme','ambiguous','acme/ambiguous','acme','ambiguous','acme/ambiguous','inactive');
INSERT INTO forge_merge_requests (id,repo_id,platform_id,number,url,created_at,updated_at,last_activity_at) VALUES
 (1,1,1,1,'https://example.com/1','2026-01-01','2026-01-10','2026-01-10'),
 (2,2,2,2,'https://example.com/2','2026-01-01','2026-01-10','2026-01-10'),
 (3,3,3,3,'https://example.com/3','2026-01-01','2026-01-10','2026-01-10');
INSERT INTO forge_mr_events (merge_request_id,repo_id,event_type,created_at,dedupe_key,metadata_json) VALUES
 (1,1,'cross_referenced','2026-01-05','same','{"source_type":"PullRequest","source_owner":"acme","source_repo":"one","source_number":1,"source_url":"https://example.com/1","is_cross_repository":false}'),
 (1,1,'cross_referenced','2026-01-05','cross','{"source_type":"PullRequest","source_owner":"acme","source_repo":"two","source_number":2,"source_url":"https://example.com/2","is_cross_repository":true}'),
 (1,1,'cross_referenced','2026-01-05','ambiguous','{"source_type":"PullRequest","source_owner":"acme","source_repo":"ambiguous","source_number":3,"source_url":"https://example.com/3","is_cross_repository":true}'),
 (1,1,'cross_referenced','2026-01-05','wrong-url','{"source_type":"PullRequest","source_owner":"acme","source_repo":"two","source_number":2,"source_url":"https://example.com/wrong","is_cross_repository":true}'),
 (1,1,'cross_referenced','2026-01-11','wrong-time','{"source_type":"PullRequest","source_owner":"acme","source_repo":"two","source_number":2,"source_url":"https://example.com/2","is_cross_repository":true}'),
 (1,1,'cross_referenced','2026-01-05','missing','{"source_type":"PullRequest","source_owner":"acme","source_repo":"missing","source_number":4,"source_url":"https://example.com/4","is_cross_repository":true}');`)
		require.NoError(t, err)
	})
	d, err := Open(path)
	require.NoError(t, err)
	t.Cleanup(func() { d.Close() })
	for key, want := range map[string]*int64{"same": new(int64(1)), "cross": new(int64(2)), "ambiguous": nil, "wrong-url": nil, "wrong-time": nil, "missing": nil} {
		var got *int64
		require.NoError(t, d.ReadDB().QueryRowContext(t.Context(), `SELECT source_repo_id FROM forge_mr_events WHERE dedupe_key=?`, key).Scan(&got))
		require.Equal(t, want, got, key)
	}
	tx, err := d.ReadDB().BeginTx(t.Context(), &sql.TxOptions{ReadOnly: true})
	require.NoError(t, err)
	defer func() { _ = tx.Rollback() }()
	_, refs, err := LoadArchiveSnapshotPullEvidence(t.Context(), tx, []int64{1, 2})
	require.NoError(t, err)
	var cross *int64
	for _, ref := range refs {
		if ref.Key == "cross" {
			cross = ref.SourceID
			require.Equal(t, int64(1), ref.TargetID)
		}
	}
	require.Equal(t, new(int64(2)), cross)
}
