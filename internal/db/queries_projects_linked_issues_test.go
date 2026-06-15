package db

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestSetProjectWorktreeLinkedIssuesRoundTrip(t *testing.T) {
	require := require.New(t)
	d := openTestDB(t)
	ctx := context.Background()
	proj := createDiscoveryTestProject(t, d, "app")
	wt, err := d.CreateProjectWorktree(ctx, CreateProjectWorktreeInput{
		ProjectID: proj.ID, Branch: "feat", Path: filepath.Join(t.TempDir(), "wt"),
	})
	require.NoError(err)
	require.Empty(wt.LinkedIssueNumbers, "a fresh worktree carries no linked issues")

	// Unsorted input with a duplicate normalizes to a sorted, deduped list.
	set, err := d.SetProjectWorktreeLinkedIssues(ctx, proj.ID, wt.ID, []int{57, 42, 42}, time.Now())
	require.NoError(err)
	require.Equal([]int{42, 57}, set.LinkedIssueNumbers)

	got, err := d.GetProjectWorktreeByID(ctx, wt.ID)
	require.NoError(err)
	require.Equal([]int{42, 57}, got.LinkedIssueNumbers, "the linked issues persist")

	cleared, err := d.SetProjectWorktreeLinkedIssues(ctx, proj.ID, wt.ID, []int{}, time.Now())
	require.NoError(err)
	require.Empty(cleared.LinkedIssueNumbers, "an empty list clears the explicit links")
}

func TestSetProjectWorktreeLinkedIssuesWrongProjectIsNotFound(t *testing.T) {
	require := require.New(t)
	d := openTestDB(t)
	ctx := context.Background()
	proj := createDiscoveryTestProject(t, d, "app")
	wt, err := d.CreateProjectWorktree(ctx, CreateProjectWorktreeInput{
		ProjectID: proj.ID, Branch: "feat", Path: filepath.Join(t.TempDir(), "wt"),
	})
	require.NoError(err)

	_, err = d.SetProjectWorktreeLinkedIssues(ctx, "prj_other", wt.ID, []int{1}, time.Now())
	require.ErrorIs(err, ErrProjectNotFound,
		"a worktree id under a different project must not be mutated")
}

// TestReconcileProjectInventoryPreservesLinkedIssues mirrors the hidden-flag and
// session-backend guards: user-set linked issues must survive discovery
// reconciliation, which refreshes branch/staleness but must never clear them.
func TestReconcileProjectInventoryPreservesLinkedIssues(t *testing.T) {
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

	_, err := d.SetProjectWorktreeLinkedIssues(ctx, proj.ID, wt.ID, []int{42, 57}, time.Now())
	require.NoError(err)

	require.NoError(d.ReconcileProjectInventory(ctx, proj.ID, ProjectInventory{
		RepositoryKind: "standard", DefaultBranch: "main",
		Worktrees: []DiscoveredWorktree{{Path: wtPath, Branch: "feature-renamed"}},
	}, time.Now()))

	refreshed := worktreeByPath(t, mustListWorktrees(t, d, proj.ID), wtPath)
	require.Equal([]int{42, 57}, refreshed.LinkedIssueNumbers,
		"discovery reconciliation must preserve the linked issues")
	require.Equal("feature-renamed", refreshed.Branch, "discovery still refreshes the branch")
}
