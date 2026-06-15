package db

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestSetProjectWorktreeHiddenRoundTrip(t *testing.T) {
	require := require.New(t)
	d := openTestDB(t)
	ctx := context.Background()
	proj := createDiscoveryTestProject(t, d, "app")
	wt, err := d.CreateProjectWorktree(ctx, CreateProjectWorktreeInput{
		ProjectID: proj.ID, Branch: "feat", Path: filepath.Join(t.TempDir(), "wt"),
	})
	require.NoError(err)
	require.False(wt.IsHidden)

	hidden, err := d.SetProjectWorktreeHidden(ctx, proj.ID, wt.ID, true, time.Now())
	require.NoError(err)
	require.True(hidden.IsHidden)

	got, err := d.GetProjectWorktreeByID(ctx, wt.ID)
	require.NoError(err)
	require.True(got.IsHidden, "the hidden flag persists")

	shown, err := d.SetProjectWorktreeHidden(ctx, proj.ID, wt.ID, false, time.Now())
	require.NoError(err)
	require.False(shown.IsHidden)
}

func TestSetProjectWorktreeHiddenWrongProjectIsNotFound(t *testing.T) {
	require := require.New(t)
	d := openTestDB(t)
	ctx := context.Background()
	proj := createDiscoveryTestProject(t, d, "app")
	wt, err := d.CreateProjectWorktree(ctx, CreateProjectWorktreeInput{
		ProjectID: proj.ID, Branch: "feat", Path: filepath.Join(t.TempDir(), "wt"),
	})
	require.NoError(err)

	_, err = d.SetProjectWorktreeHidden(ctx, "prj_other", wt.ID, true, time.Now())
	require.ErrorIs(err, ErrProjectNotFound,
		"a worktree id under a different project must not be hidden")
}

// TestReconcileProjectInventoryPreservesHiddenFlag is the regression guard for
// the cut: a user-hidden worktree must survive discovery reconciliation, which
// refreshes the branch and clears staleness but must never unhide it.
func TestReconcileProjectInventoryPreservesHiddenFlag(t *testing.T) {
	require := require.New(t)
	d := openTestDB(t)
	ctx := context.Background()
	proj := createDiscoveryTestProject(t, d, "app")
	wtPath := filepath.Join(t.TempDir(), "feature")

	require.NoError(d.ReconcileProjectInventory(ctx, proj.ID, ProjectInventory{
		RepositoryKind: "standard", DefaultBranch: "main",
		Worktrees: []DiscoveredWorktree{{Path: wtPath, Branch: "feature"}},
	}, time.Now()))
	wt := worktreeByPath(t, mustListWorktrees(t, d, proj.ID), wtPath)

	_, err := d.SetProjectWorktreeHidden(ctx, proj.ID, wt.ID, true, time.Now())
	require.NoError(err)

	// A later discovery pass renames the branch but must not unhide the row.
	require.NoError(d.ReconcileProjectInventory(ctx, proj.ID, ProjectInventory{
		RepositoryKind: "standard", DefaultBranch: "main",
		Worktrees: []DiscoveredWorktree{{Path: wtPath, Branch: "feature-renamed"}},
	}, time.Now()))

	refreshed := worktreeByPath(t, mustListWorktrees(t, d, proj.ID), wtPath)
	require.True(refreshed.IsHidden, "discovery reconciliation must preserve hidden")
	require.Equal("feature-renamed", refreshed.Branch, "discovery still refreshes the branch")
	require.False(refreshed.IsStale)
}
