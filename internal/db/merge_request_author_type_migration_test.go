package db

import (
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestMergeRequestAuthorTypeMigrationReopensEligibleLookups(t *testing.T) {
	path := filepath.Join(t.TempDir(), "upgrade.db")
	openAtVersionForTest(t, path, 95, func(raw *sql.DB) {
		_, err := raw.ExecContext(t.Context(), `
INSERT INTO forge_repos (id,platform,platform_repo_id,owner,name,repo_path,owner_key,name_key,repo_path_key) VALUES
 (1,'github',1,'acme','one','acme/one','acme','one','acme/one'),
 (2,'gitlab',2,'acme','two','acme/two','acme','two','acme/two'),
 (3,'github',3,'acme','three','acme/three','acme','three','acme/three');
INSERT INTO forge_archive_repos (repo_id,collection_mode,operator_state,created_at,updated_at) VALUES
 (1,'full','active',datetime('now'),datetime('now')),
 (2,'full','active',datetime('now'),datetime('now')),
 (3,'discovery','active',datetime('now'),datetime('now'));
INSERT INTO forge_merge_requests (id,repo_id,platform_id,number,state,created_at,updated_at,last_activity_at,closed_at) VALUES
 (1,1,1,1,'closed',datetime('now','-40 days'),datetime('now'),datetime('now'),NULL),
 (2,1,2,2,'closed',datetime('now','-60 days'),datetime('now'),datetime('now'),NULL),
 (3,2,3,3,'open',datetime('now'),datetime('now'),datetime('now'),NULL),
 (4,1,4,4,'open',datetime('now'),datetime('now'),datetime('now'),NULL),
 (5,1,5,5,'open',datetime('now'),datetime('now'),datetime('now'),NULL),
 (6,3,6,6,'open',datetime('now'),datetime('now'),datetime('now'),NULL),
 (7,1,7,7,'open',datetime('now'),datetime('now'),datetime('now'),NULL),
 (8,1,8,8,'merged',datetime('now','-40 days'),datetime('now'),datetime('now'),NULL);
INSERT INTO forge_archive_items (repo_id,item_type,item_number,provider_item_id,provider_created_at,provider_updated_at,lifecycle_state) VALUES
 (1,'merge_request',1,'1',datetime('now'),datetime('now'),'active'),
 (1,'merge_request',2,'2',datetime('now'),datetime('now'),'active'),
 (2,'merge_request',3,'3',datetime('now'),datetime('now'),'active'),
 (1,'merge_request',4,'4',datetime('now'),datetime('now'),'active'),
 (1,'merge_request',5,'5',datetime('now'),datetime('now'),'active'),
 (3,'merge_request',6,'6',datetime('now'),datetime('now'),'active'),
 (1,'merge_request',7,'7',datetime('now'),datetime('now'),'active'),
 (1,'merge_request',8,'8',datetime('now'),datetime('now'),'active');
INSERT INTO forge_archive_dataset_progress (repo_id,item_type,item_number,dataset,scan_generation,status,updated_at) VALUES
 (1,'merge_request',1,'lookup',3,'complete',datetime('now')),
 (1,'merge_request',2,'lookup',3,'complete',datetime('now')),
 (2,'merge_request',3,'lookup',3,'complete',datetime('now')),
 (1,'merge_request',4,'lookup',3,'blocked',datetime('now')),
 (1,'merge_request',5,'lookup',3,'unsupported',datetime('now')),
 (3,'merge_request',6,'lookup',3,'complete',datetime('now')),
 (1,'merge_request',7,'lookup',3,'complete',datetime('now')),
 (1,'merge_request',8,'lookup',3,'complete',datetime('now'));
UPDATE forge_archive_dataset_progress SET next_cursor='cursor',page_count=7,observed_count=8,attempt_count=2 WHERE repo_id=3;`)
		require.NoError(t, err)
		_, err = raw.ExecContext(t.Context(), `UPDATE forge_merge_requests SET closed_at=? WHERE id=1`, time.Now().UTC().Add(-15*24*time.Hour))
		require.NoError(t, err)
		_, err = raw.ExecContext(t.Context(), `UPDATE forge_merge_requests SET closed_at=? WHERE id=2`, time.Now().UTC().Add(-45*24*time.Hour))
		require.NoError(t, err)
		_, err = raw.ExecContext(t.Context(), `UPDATE forge_merge_requests SET closed_at=?, merged_at=? WHERE id=8`, time.Now().UTC().Add(-15*24*time.Hour), time.Now().UTC().Add(-15*24*time.Hour))
		require.NoError(t, err)
	})
	d, err := Open(path)
	require.NoError(t, err)
	t.Cleanup(func() { d.Close() })
	for _, tc := range []struct {
		repo, number int
		status       string
		generation   int
	}{
		{1, 1, "pending", 4}, {1, 2, "complete", 3}, {2, 3, "complete", 3}, {1, 4, "blocked", 3}, {1, 5, "unsupported", 3}, {3, 6, "complete", 3}, {1, 7, "pending", 4}, {1, 8, "pending", 4},
	} {
		var status string
		var generation int
		require.NoError(t, d.ReadDB().QueryRowContext(t.Context(), `SELECT status,scan_generation FROM forge_archive_dataset_progress WHERE repo_id=? AND item_number=? AND dataset='lookup'`, tc.repo, tc.number).Scan(&status, &generation))
		require.Equal(t, tc.status, status)
		require.Equal(t, tc.generation, generation)
	}
	var preserved string
	require.NoError(t, d.ReadDB().QueryRowContext(t.Context(), `SELECT next_cursor||'/'||page_count||'/'||observed_count||'/'||attempt_count FROM forge_archive_dataset_progress WHERE repo_id=3`).Scan(&preserved))
	require.Equal(t, "cursor/7/8/2", preserved, "discovery lookups keep their cursor and counters")
}
