package server

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"go.kenn.io/middleman/internal/db"
	"go.kenn.io/middleman/internal/fleet"
	"go.kenn.io/middleman/internal/testutil/dbtest"
	"go.kenn.io/middleman/internal/workspace"
)

// TestBuildLocalRawSurfacesWorktreeLinkedIssues proves a registered worktree's
// persisted explicit linked issues reach the raw snapshot, the producer half of
// the host write-through for /api/worktree/set-linked-issues.
func TestBuildLocalRawSurfacesWorktreeLinkedIssues(t *testing.T) {
	require := require.New(t)
	database := dbtest.Open(t)
	ctx := context.Background()

	proj, err := database.CreateProject(ctx, db.CreateProjectInput{
		DisplayName: "app", LocalPath: filepath.Join(t.TempDir(), "app"), DefaultBranch: "main",
	})
	require.NoError(err)
	wtPath := filepath.Join(t.TempDir(), "app-feat")
	wt, err := database.CreateProjectWorktree(ctx, db.CreateProjectWorktreeInput{
		ProjectID: proj.ID, Branch: "feat", Path: wtPath,
	})
	require.NoError(err)
	_, err = database.SetProjectWorktreeLinkedIssues(ctx, proj.ID, wt.ID, []int{42, 57}, time.Now())
	require.NoError(err)

	srv := &Server{db: database, workspaces: workspace.NewManager(database, t.TempDir())}
	raw, err := srv.buildLocalRaw(ctx)
	require.NoError(err)

	got := requireRawWorktree(t, raw.Worktrees, normPath(wtPath))
	require.Equal([]int{42, 57}, got.LinkedIssueNumbers,
		"persisted explicit linked issues surface on the raw worktree")
}

// TestAddWorktreeMergesExplicitAndWorkspaceLinkedIssues proves the producer
// unions a registered worktree's explicit links (from the new column) with the
// workspace-item issue linkage that overlays the same path, deduped and sorted —
// not replacing one with the other.
func TestAddWorktreeMergesExplicitAndWorkspaceLinkedIssues(t *testing.T) {
	require := require.New(t)
	order := []string{}
	byScopedKey := map[string]*fleet.RawWorktree{}

	// Registered worktree carrying explicit linked issues.
	addWorktree(&order, byScopedKey, fleet.RawWorktree{
		ScopedKey: "worktree:/tmp/wt", ProjectKey: "repo:/tmp/app",
		Name: "wt", Path: "/tmp/wt", LinkedIssueNumbers: []int{42, 99},
	})
	// Workspace overlay at the same path linking another issue (and re-linking 42).
	addWorktree(&order, byScopedKey, fleet.RawWorktree{
		ScopedKey: "worktree:/tmp/wt", ProjectKey: "repo:/tmp/app",
		Name: "wt", Path: "/tmp/wt", LinkedIssueNumbers: []int{7, 42},
	})

	require.Len(order, 1)
	require.Equal([]int{7, 42, 99}, byScopedKey["worktree:/tmp/wt"].LinkedIssueNumbers,
		"explicit and workspace-item issue links merge without duplicates, sorted")
}

// TestAddWorktreePROverlayPreservesExplicitLinkedIssues proves a pull-request
// workspace overlay (which carries no issue links of its own) does not drop the
// registered worktree's explicit linked issues.
func TestAddWorktreePROverlayPreservesExplicitLinkedIssues(t *testing.T) {
	require := require.New(t)
	order := []string{}
	byScopedKey := map[string]*fleet.RawWorktree{}
	prNumber := 7

	addWorktree(&order, byScopedKey, fleet.RawWorktree{
		ScopedKey: "worktree:/tmp/wt", ProjectKey: "repo:/tmp/app",
		Name: "wt", Path: "/tmp/wt", LinkedIssueNumbers: []int{42},
	})
	addWorktree(&order, byScopedKey, fleet.RawWorktree{
		ScopedKey: "worktree:/tmp/wt", ProjectKey: "repo:/tmp/app",
		Name: "wt", Path: "/tmp/wt", LinkedPRNumber: &prNumber,
	})

	require.Equal([]int{42}, byScopedKey["worktree:/tmp/wt"].LinkedIssueNumbers,
		"a PR workspace overlay must not drop explicit linked issues")
	require.NotNil(byScopedKey["worktree:/tmp/wt"].LinkedPRNumber)
}
