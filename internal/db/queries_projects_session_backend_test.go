package db

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestSetProjectWorktreeSessionBackendRoundTrip(t *testing.T) {
	require := require.New(t)
	d := openTestDB(t)
	ctx := context.Background()
	proj := createDiscoveryTestProject(t, d, "app")
	wt, err := d.CreateProjectWorktree(ctx, CreateProjectWorktreeInput{
		ProjectID: proj.ID, Branch: "feat", Path: filepath.Join(t.TempDir(), "wt"),
	})
	require.NoError(err)
	require.Empty(wt.SessionBackend, "a fresh worktree carries no override")

	set, err := d.SetProjectWorktreeSessionBackend(ctx, proj.ID, wt.ID, "localTmux", time.Now())
	require.NoError(err)
	require.Equal("localTmux", set.SessionBackend)

	got, err := d.GetProjectWorktreeByID(ctx, wt.ID)
	require.NoError(err)
	require.Equal("localTmux", got.SessionBackend, "the backend override persists")

	cleared, err := d.SetProjectWorktreeSessionBackend(ctx, proj.ID, wt.ID, "", time.Now())
	require.NoError(err)
	require.Empty(cleared.SessionBackend, "an empty value clears the override back to default")
}

func TestSetProjectWorktreeSessionBackendWrongProjectIsNotFound(t *testing.T) {
	require := require.New(t)
	d := openTestDB(t)
	ctx := context.Background()
	proj := createDiscoveryTestProject(t, d, "app")
	wt, err := d.CreateProjectWorktree(ctx, CreateProjectWorktreeInput{
		ProjectID: proj.ID, Branch: "feat", Path: filepath.Join(t.TempDir(), "wt"),
	})
	require.NoError(err)

	_, err = d.SetProjectWorktreeSessionBackend(ctx, "prj_other", wt.ID, "localTmux", time.Now())
	require.ErrorIs(err, ErrProjectNotFound,
		"a worktree id under a different project must not be mutated")
}

// TestReconcileProjectInventoryPreservesSessionBackend is the cut regression
// guard mirroring the hidden-flag guard: a user-set session backend must
// survive discovery reconciliation, which refreshes branch/staleness but must
// never clear the override.
func TestReconcileProjectInventoryPreservesSessionBackend(t *testing.T) {
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

	_, err := d.SetProjectWorktreeSessionBackend(ctx, proj.ID, wt.ID, "localTmux", time.Now())
	require.NoError(err)

	require.NoError(d.ReconcileProjectInventory(ctx, proj.ID, ProjectInventory{
		RepositoryKind: "standard", DefaultBranch: "main",
		Worktrees: []DiscoveredWorktree{{Path: wtPath, Branch: "feature-renamed"}},
	}, time.Now()))

	refreshed := worktreeByPath(t, mustListWorktrees(t, d, proj.ID), wtPath)
	require.Equal("localTmux", refreshed.SessionBackend,
		"discovery reconciliation must preserve the session backend override")
	require.Equal("feature-renamed", refreshed.Branch, "discovery still refreshes the branch")
}
