package db

import (
	"database/sql"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestMigration86MergeRequestListIndexes(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "list-indexes-v85.db")
	openAtVersionForTest(t, path, 85, func(raw *sql.DB) {
		_, err := raw.ExecContext(t.Context(), `
			INSERT INTO forge_repos (id, platform_repo_id, owner, name, repo_path, owner_key, name_key, repo_path_key)
			VALUES (1, 1001, 'acme', 'widgets', 'acme/widgets', 'acme', 'widgets', 'acme/widgets');
			INSERT INTO forge_merge_requests (repo_id, platform_id, number, state, created_at, updated_at, last_activity_at)
			VALUES (1, 101, 1, 'open', '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z');
		`)
		require.NoError(t, err)
	})
	d, err := Open(path)
	require.NoError(t, err)
	for _, index := range []struct {
		name    string
		columns []string
		partial bool
	}{
		{"idx_mr_repo_created", []string{"repo_id", "created_at", "last_activity_at", "id"}, false},
		{"idx_mr_repo_head", []string{"repo_id", "head_branch", "created_at", "last_activity_at", "id"}, false},
		{"idx_mr_repo_open_created", []string{"repo_id", "created_at", "last_activity_at", "id"}, true},
		{"idx_mr_repo_merged_created", []string{"repo_id", "created_at", "last_activity_at", "id"}, true},
	} {
		assertIndexForTest(t, d.ReadDB(), "forge_merge_requests", index.name, index.columns, index.partial)
	}
	mrs, err := d.ListMergeRequests(t.Context(), ListMergeRequestsOpts{RepoID: 1, State: "open", ProviderState: true, ByCreated: true, Limit: 30})
	require.NoError(t, err)
	require.Len(t, mrs, 1)
	assert.Equal(t, 1, mrs[0].Number)
	assertDatabaseIntegrityForTest(t, d.ReadDB())
	require.NoError(t, d.Close())

	raw, migrator := openMigratorForTest(t, path)
	t.Cleanup(func() { raw.Close() })
	require.NoError(t, migrator.Migrate(85))
	var indexes int
	require.NoError(t, raw.QueryRowContext(t.Context(), `SELECT COUNT(*) FROM sqlite_schema
		WHERE type = 'index' AND name IN ('idx_mr_repo_created', 'idx_mr_repo_head', 'idx_mr_repo_open_created', 'idx_mr_repo_merged_created')`).Scan(&indexes))
	assert.Zero(t, indexes)
	var number int
	require.NoError(t, raw.QueryRowContext(t.Context(), `SELECT number FROM forge_merge_requests WHERE repo_id = 1`).Scan(&number))
	assert.Equal(t, 1, number)
	assertDatabaseIntegrityForTest(t, raw)
}
