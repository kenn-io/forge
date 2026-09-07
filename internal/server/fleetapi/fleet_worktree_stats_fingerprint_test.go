package fleetapi

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"go.kenn.io/forge/internal/db"
	"go.kenn.io/forge/internal/testutil/dbtest"
)

// TestFleetWorktreeStatsSamplerSkipsUnchangedWorktrees proves the background
// pass neither re-measures nor rewrites a worktree whose git directory has not
// moved: a row altered behind the sampler's back survives an unchanged pass
// and is corrected only once the fingerprint changes.
func TestFleetWorktreeStatsSamplerSkipsUnchangedWorktrees(t *testing.T) {
	repoDir, featDir := seedFingerprintRepo(t)
	require := require.New(t)
	database := dbtest.Open(t)
	ctx := context.Background()

	require.NoError(os.WriteFile(
		filepath.Join(featDir, "feature.txt"), []byte("x\ny\n"), 0o644,
	))
	runGit(t, featDir, "add", ".")
	runGit(t, featDir, "commit", "-q", "-m", "feature work")

	proj, err := database.CreateProject(ctx, db.CreateProjectInput{
		DisplayName: "app", LocalPath: repoDir, DefaultBranch: "main",
	})
	require.NoError(err)
	_, err = database.CreateProjectWorktree(ctx, db.CreateProjectWorktreeInput{
		ProjectID: proj.ID, Branch: "feature", Path: featDir,
	})
	require.NoError(err)

	fires := 0
	sampler := &fleetWorktreeStatsSampler{
		db:        database,
		onChanged: func() { fires++ },
	}
	sampler.runOnce(ctx)
	require.Equal(1, fires)
	stats, err := database.ListWorktreeStats(ctx)
	require.NoError(err)
	require.Equal(2, stats[normPath(featDir)].DiffAdded)

	// Tamper with the stored row. An unchanged pass must not touch it.
	tampered := db.WorktreeGitStats{DiffAdded: 99, DiffRemoved: 99}
	_, err = database.UpsertWorktreeStats(ctx, normPath(featDir), tampered, time.Now())
	require.NoError(err)
	sampler.runOnce(ctx)
	stats, err = database.ListWorktreeStats(ctx)
	require.NoError(err)
	require.Equal(99, stats[normPath(featDir)].DiffAdded,
		"an unchanged worktree is skipped, so the tampered row survives")
	require.Equal(1, fires)

	// A commit in the worktree moves its branch ref, so the next pass
	// re-measures and repairs the row.
	require.NoError(os.WriteFile(
		filepath.Join(featDir, "more.txt"), []byte("z\n"), 0o644,
	))
	runGit(t, featDir, "add", ".")
	runGit(t, featDir, "commit", "-q", "-m", "more")
	sampler.runOnce(ctx)
	stats, err = database.ListWorktreeStats(ctx)
	require.NoError(err)
	require.Equal(3, stats[normPath(featDir)].DiffAdded,
		"a changed fingerprint re-samples the worktree")
	require.Equal(2, fires)

	// The on-demand refresh ignores the fingerprint entirely.
	_, err = database.UpsertWorktreeStats(ctx, normPath(featDir), tampered, time.Now())
	require.NoError(err)
	require.NoError(sampler.refreshWorktreeStats(ctx, featDir, "main"))
	stats, err = database.ListWorktreeStats(ctx)
	require.NoError(err)
	require.Equal(3, stats[normPath(featDir)].DiffAdded,
		"the on-demand refresh always re-measures")
}
