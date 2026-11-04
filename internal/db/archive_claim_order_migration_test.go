package db

import (
	"database/sql"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestMigration88BackfillsArchiveClaimOrder(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "archive-v87.db")
	openAtVersionForTest(t, path, 87, func(raw *sql.DB) {
		_, err := raw.ExecContext(t.Context(), `
			INSERT INTO forge_repos (id, platform_repo_id, owner, name, repo_path, owner_key, name_key, repo_path_key)
			VALUES (1, 1001, 'acme', 'widgets', 'acme/widgets', 'acme', 'widgets', 'acme/widgets');
			INSERT INTO forge_archive_repos (repo_id, collection_mode, operator_state, created_at, updated_at)
			VALUES (1, 'full', 'active', '2026-01-01', '2026-01-01');
			INSERT INTO forge_archive_items
			(repo_id, item_type, item_number, provider_item_id, provider_created_at, provider_updated_at, lifecycle_state) VALUES
			(1, 'issue', 1, 'issue-1', '2026-01-01', '2026-01-01', 'active'),
			(1, 'issue', 2, 'issue-2', '2026-01-02', '2026-01-02', 'active'),
			(1, 'issue', 3, 'issue-3', '2026-01-03', '2026-01-03', 'active');
			INSERT INTO forge_archive_dataset_progress (repo_id, item_type, item_number, dataset, status, updated_at) VALUES
			(1, 'issue', 1, 'lookup', 'complete', '2026-01-01'),
			(1, 'issue', 2, 'lookup', 'pending', '2026-01-02'),
			(1, 'issue', 3, 'lookup', 'failed', '2026-01-03'),
			(1, 'issue', 2, 'comments', 'pending', '2026-01-02');
		`)
		require.NoError(t, err)
	})
	d, err := Open(path)
	require.NoError(t, err)
	t.Cleanup(func() { d.Close() })
	var copied, comments int
	require.NoError(t, d.ReadDB().QueryRowContext(t.Context(), `
		SELECT COUNT(*) FROM forge_archive_dataset_progress p JOIN forge_archive_items ai
		ON ai.repo_id = p.repo_id AND ai.item_type = p.item_type AND ai.item_number = p.item_number
		WHERE p.dataset = 'lookup' AND p.item_created_at = ai.provider_created_at`).Scan(&copied))
	assert.Equal(t, 3, copied)
	require.NoError(t, d.ReadDB().QueryRowContext(t.Context(), `
		SELECT COUNT(*) FROM forge_archive_dataset_progress
		WHERE dataset = 'comments' AND item_created_at IS NULL`).Scan(&comments))
	assert.Equal(t, 1, comments)
	work, err := d.ClaimArchiveItem(t.Context(), ClaimArchiveItemOpts{RepoIDs: []int64{1}, Now: archiveTestTime()})
	require.NoError(t, err)
	require.NotNil(t, work)
	assert.Equal(t, 2, work.ItemNumber)
	assertDatabaseIntegrityForTest(t, d.ReadDB())
}
