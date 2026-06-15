package db

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestDeleteProjectCascadesWorktrees proves a project delete removes the
// project row and, via ON DELETE CASCADE, its registered worktrees. This is the
// storage half of the host write-through for /api/project/remove.
func TestDeleteProjectCascadesWorktrees(t *testing.T) {
	require := require.New(t)
	d := openTestDB(t)
	ctx := context.Background()
	proj := createDiscoveryTestProject(t, d, "app")
	wt, err := d.CreateProjectWorktree(ctx, CreateProjectWorktreeInput{
		ProjectID: proj.ID, Branch: "feat", Path: filepath.Join(t.TempDir(), "wt"),
	})
	require.NoError(err)

	require.NoError(d.DeleteProject(ctx, proj.ID))

	_, err = d.GetProjectByID(ctx, proj.ID)
	require.ErrorIs(err, ErrProjectNotFound, "the project row is gone")
	_, err = d.GetProjectWorktreeByID(ctx, wt.ID)
	require.ErrorIs(err, ErrProjectNotFound, "ON DELETE CASCADE removes the worktree row")
}

// TestDeleteProjectNotFound proves deleting an unknown project id reports
// ErrProjectNotFound rather than silently succeeding, so the host write-through
// can surface a 404 instead of a misleading 204.
func TestDeleteProjectNotFound(t *testing.T) {
	require := require.New(t)
	d := openTestDB(t)
	require.ErrorIs(d.DeleteProject(context.Background(), "prj_missing"), ErrProjectNotFound,
		"deleting an unknown project reports not found")
}

// TestDeleteProjectWorktreeRemovesRow proves a worktree delete drops only the
// worktree row, leaving the owning project intact. This is the storage half of
// the host write-through for /api/worktree/delete.
func TestDeleteProjectWorktreeRemovesRow(t *testing.T) {
	require := require.New(t)
	d := openTestDB(t)
	ctx := context.Background()
	proj := createDiscoveryTestProject(t, d, "app")
	wt, err := d.CreateProjectWorktree(ctx, CreateProjectWorktreeInput{
		ProjectID: proj.ID, Branch: "feat", Path: filepath.Join(t.TempDir(), "wt"),
	})
	require.NoError(err)

	require.NoError(d.DeleteProjectWorktree(ctx, proj.ID, wt.ID))

	_, err = d.GetProjectWorktreeByID(ctx, wt.ID)
	require.ErrorIs(err, ErrProjectNotFound, "the worktree row is gone")
	_, err = d.GetProjectByID(ctx, proj.ID)
	require.NoError(err, "the owning project survives a worktree delete")
}

// TestDeleteProjectWorktreeNotFound proves deleting an unknown worktree id
// reports ErrProjectNotFound so the host write-through can surface a 404.
func TestDeleteProjectWorktreeNotFound(t *testing.T) {
	require := require.New(t)
	d := openTestDB(t)
	proj := createDiscoveryTestProject(t, d, "app")
	require.ErrorIs(
		d.DeleteProjectWorktree(context.Background(), proj.ID, "wtr_missing"),
		ErrProjectNotFound,
		"deleting an unknown worktree reports not found",
	)
}

// TestDeleteProjectWorktreeScopedToProject proves the delete is scoped to the
// owning project: a worktree id under a different project is treated as not
// found and left in place.
func TestDeleteProjectWorktreeScopedToProject(t *testing.T) {
	require := require.New(t)
	d := openTestDB(t)
	ctx := context.Background()
	projA := createDiscoveryTestProject(t, d, "a")
	projB := createDiscoveryTestProject(t, d, "b")
	wt, err := d.CreateProjectWorktree(ctx, CreateProjectWorktreeInput{
		ProjectID: projA.ID, Branch: "feat", Path: filepath.Join(t.TempDir(), "wt"),
	})
	require.NoError(err)

	require.ErrorIs(
		d.DeleteProjectWorktree(ctx, projB.ID, wt.ID),
		ErrProjectNotFound,
		"a worktree id under a different project is not found",
	)
	_, err = d.GetProjectWorktreeByID(ctx, wt.ID)
	require.NoError(err, "the worktree survives a mismatched-project delete")
}
