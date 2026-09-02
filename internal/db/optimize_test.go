package db

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func plannerStatisticsRows(t *testing.T, d *DB, table string) int {
	t.Helper()
	var rows int
	require.NoError(t, d.ReadDB().QueryRowContext(t.Context(),
		"SELECT COUNT(*) FROM sqlite_stat1 WHERE tbl = ?", table).Scan(&rows))
	return rows
}

func TestOpenCreatesPlannerStatistics(t *testing.T) {
	d := openDBWithMigrations(t)
	var tables int
	require.NoError(t, d.ReadDB().QueryRowContext(t.Context(),
		"SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = 'sqlite_stat1'").Scan(&tables))
	assert.Equal(t, 1, tables, "opening a store must leave planner statistics behind")
}

func TestOptimizeRecordsStatisticsForPopulatedTables(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	d := openTestDB(t)
	ctx := t.Context()
	require.Zero(plannerStatisticsRows(t, d, "forge_merge_requests"),
		"the copied template fixture starts without merge request statistics")

	repoID, err := d.UpsertRepo(ctx, verifiedTestRepoIdentity("github", "github.com", "acme", "widget"))
	require.NoError(err)
	for number := 1; number <= 20; number++ {
		insertTestMR(t, d, repoID, number, "change", baseTime())
	}
	require.NoError(d.Optimize(ctx))

	assert.Positive(plannerStatisticsRows(t, d, "forge_merge_requests"))
	assert.Positive(plannerStatisticsRows(t, d, "forge_repos"))
}
