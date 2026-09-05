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

// TestFleetWorktreeDiscovererSkipsUnchangedProjects proves a background pass
// leaves a project alone while its git directory fingerprint is unchanged and
// reconciles it again as soon as the worktree registry moves.
func TestFleetWorktreeDiscovererSkipsUnchangedProjects(t *testing.T) {
	repoDir, _ := seedFingerprintRepo(t)
	require := require.New(t)
	database := dbtest.Open(t)
	ctx := context.Background()

	project, err := database.CreateProject(ctx, db.CreateProjectInput{
		DisplayName: "app", LocalPath: repoDir,
	})
	require.NoError(err)
	discoverer := newFleetWorktreeDiscoverer(database, nil)

	discoverer.runOnce(ctx)
	rows, err := database.ListProjectWorktrees(ctx, project.ID)
	require.NoError(err)
	require.Len(rows, 2)

	// Mark the project stale behind the discoverer's back. An unchanged pass
	// skips the project, so the flag is left as is; the flag itself is not a
	// git-directory input.
	require.NoError(database.MarkProjectStale(ctx, project.ID, time.Now()))
	discoverer.runOnce(ctx)
	stale, err := database.GetProjectByID(ctx, project.ID)
	require.NoError(err)
	require.True(stale.IsStale, "an unchanged project is not reconciled")

	// The on-demand path always reconciles.
	discoverer.refreshProject(ctx, project.ID, repoDir)
	fresh, err := database.GetProjectByID(ctx, project.ID)
	require.NoError(err)
	require.False(fresh.IsStale)

	// A registry change is picked up by the next background pass.
	require.NoError(database.MarkProjectStale(ctx, project.ID, time.Now()))
	other := filepath.Join(t.TempDir(), "other")
	runGit(t, repoDir, "worktree", "add", "-q", "-b", "other", other)
	discoverer.runOnce(ctx)
	after, err := database.GetProjectByID(ctx, project.ID)
	require.NoError(err)
	require.False(after.IsStale, "a changed fingerprint reconciles the project")
	rows, err = database.ListProjectWorktrees(ctx, project.ID)
	require.NoError(err)
	require.Len(rows, 3)

	// A vanished checkout is always inspected so it is marked stale, and it
	// recovers on the pass after it returns.
	moved := repoDir + ".moved"
	require.NoError(os.Rename(repoDir, moved))
	discoverer.runOnce(ctx)
	gone, err := database.GetProjectByID(ctx, project.ID)
	require.NoError(err)
	require.True(gone.IsStale, "a missing checkout is marked stale")
	require.NoError(os.Rename(moved, repoDir))
	discoverer.runOnce(ctx)
	back, err := database.GetProjectByID(ctx, project.ID)
	require.NoError(err)
	require.False(back.IsStale, "a returned checkout is reconciled without a forced refresh")
}
