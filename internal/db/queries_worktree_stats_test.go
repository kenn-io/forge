package db

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestUpsertAndListWorktreeStatsRoundTrip(t *testing.T) {
	require := require.New(t)
	d := openTestDB(t)
	ctx := context.Background()
	sampledAt := time.Date(2026, 6, 4, 9, 0, 0, 0, time.UTC)

	_, err := d.UpsertWorktreeStats(ctx, "/repo/wt", WorktreeGitStats{
		DiffAdded: 12, DiffRemoved: 3, SyncAhead: 2, SyncBehind: 1,
	}, sampledAt)
	require.NoError(err)

	stats, err := d.ListWorktreeStats(ctx)
	require.NoError(err)
	require.Len(stats, 1)
	got, ok := stats["/repo/wt"]
	require.True(ok)
	require.Equal(12, got.DiffAdded)
	require.Equal(3, got.DiffRemoved)
	require.Equal(2, got.SyncAhead)
	require.Equal(1, got.SyncBehind)
	require.Equal(sampledAt, got.SampledAt)
}

func TestUpsertWorktreeStatsReplacesPriorSample(t *testing.T) {
	require := require.New(t)
	d := openTestDB(t)
	ctx := context.Background()

	_, err := d.UpsertWorktreeStats(ctx, "/repo/wt", WorktreeGitStats{
		DiffAdded: 1, SyncAhead: 1,
	}, time.Date(2026, 6, 4, 9, 0, 0, 0, time.UTC))
	require.NoError(err)
	_, err = d.UpsertWorktreeStats(ctx, "/repo/wt", WorktreeGitStats{
		DiffAdded: 40, DiffRemoved: 5, SyncAhead: 0, SyncBehind: 7,
	}, time.Date(2026, 6, 4, 10, 0, 0, 0, time.UTC))
	require.NoError(err)

	stats, err := d.ListWorktreeStats(ctx)
	require.NoError(err)
	require.Len(stats, 1, "the same path upserts in place")
	require.Equal(40, stats["/repo/wt"].DiffAdded)
	require.Equal(5, stats["/repo/wt"].DiffRemoved)
	require.Equal(0, stats["/repo/wt"].SyncAhead)
	require.Equal(7, stats["/repo/wt"].SyncBehind)
}

func TestUpsertWorktreeStatsReportsChange(t *testing.T) {
	require := require.New(t)
	d := openTestDB(t)
	ctx := context.Background()
	t0 := time.Date(2026, 6, 4, 9, 0, 0, 0, time.UTC)

	changed, err := d.UpsertWorktreeStats(ctx, "/repo/wt", WorktreeGitStats{
		DiffAdded: 5, SyncAhead: 1,
	}, t0)
	require.NoError(err)
	require.True(changed, "a freshly inserted path counts as changed")

	// Identical counts at a later time: SampledAt advances but the sample is
	// not reported as changed.
	changed, err = d.UpsertWorktreeStats(ctx, "/repo/wt", WorktreeGitStats{
		DiffAdded: 5, SyncAhead: 1,
	}, t0.Add(time.Minute))
	require.NoError(err)
	require.False(changed, "re-upsert with identical counts is not a change")

	changed, err = d.UpsertWorktreeStats(ctx, "/repo/wt", WorktreeGitStats{
		DiffAdded: 5, DiffRemoved: 2, SyncAhead: 1,
	}, t0.Add(2*time.Minute))
	require.NoError(err)
	require.True(changed, "a differing count is reported as changed")
}

func TestUpsertWorktreeStatsRequiresPath(t *testing.T) {
	d := openTestDB(t)
	_, err := d.UpsertWorktreeStats(
		context.Background(), "  ", WorktreeGitStats{}, time.Now(),
	)
	require.Error(t, err)
}

func TestPruneWorktreeStatsDropsAbsentPaths(t *testing.T) {
	require := require.New(t)
	d := openTestDB(t)
	ctx := context.Background()
	now := time.Date(2026, 6, 4, 9, 0, 0, 0, time.UTC)

	for _, path := range []string{"/repo/a", "/repo/b", "/repo/c"} {
		_, err := d.UpsertWorktreeStats(ctx, path, WorktreeGitStats{}, now)
		require.NoError(err)
	}

	require.NoError(d.PruneWorktreeStats(ctx, []string{"/repo/a", "/repo/c"}))

	stats, err := d.ListWorktreeStats(ctx)
	require.NoError(err)
	require.Len(stats, 2)
	_, hasA := stats["/repo/a"]
	_, hasB := stats["/repo/b"]
	_, hasC := stats["/repo/c"]
	require.True(hasA)
	require.False(hasB, "a path absent from the keep set is pruned")
	require.True(hasC)
}

func TestPruneWorktreeStatsEmptyKeepClearsTable(t *testing.T) {
	require := require.New(t)
	d := openTestDB(t)
	ctx := context.Background()
	_, err := d.UpsertWorktreeStats(
		ctx, "/repo/a", WorktreeGitStats{}, time.Now(),
	)
	require.NoError(err)

	require.NoError(d.PruneWorktreeStats(ctx, nil))

	stats, err := d.ListWorktreeStats(ctx)
	require.NoError(err)
	require.Empty(stats)
}
