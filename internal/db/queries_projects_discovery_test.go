package db

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func createDiscoveryTestProject(t *testing.T, d *DB, name string) *Project {
	t.Helper()
	p, err := d.CreateProject(context.Background(), CreateProjectInput{
		DisplayName: name,
		LocalPath:   filepath.Join(t.TempDir(), name),
	})
	require.NoError(t, err)
	return p
}

func mustListWorktrees(t *testing.T, d *DB, projectID string) []ProjectWorktree {
	t.Helper()
	wts, err := d.ListProjectWorktrees(context.Background(), projectID)
	require.NoError(t, err)
	return wts
}

func worktreeByPath(t *testing.T, wts []ProjectWorktree, path string) ProjectWorktree {
	t.Helper()
	for _, w := range wts {
		if w.Path == path {
			return w
		}
	}
	require.Failf(t, "worktree not found",
		"worktree %s not found among %d rows", path, len(wts))
	return ProjectWorktree{}
}

func mustGetProject(t *testing.T, d *DB, projectID string) *Project {
	t.Helper()
	p, err := d.GetProjectByID(context.Background(), projectID)
	require.NoError(t, err)
	return p
}

// TestReconcileProjectInventory_DiscoversLinkedWorktreeAndProjectFacts verifies
// a discovery pass surfaces a linked worktree without explicit registration and
// refreshes the project's repository kind and default branch.
func TestReconcileProjectInventory_DiscoversLinkedWorktreeAndProjectFacts(t *testing.T) {
	require := require.New(t)
	d := openTestDB(t)
	ctx := context.Background()
	now := time.Date(2026, 6, 4, 12, 0, 0, 0, time.UTC)

	project := createDiscoveryTestProject(t, d, "alpha")
	wtPath := filepath.Join(t.TempDir(), "wt-feature")

	require.NoError(d.ReconcileProjectInventory(ctx, project.ID, ProjectInventory{
		RepositoryKind: "bare",
		DefaultBranch:  "develop",
		Worktrees:      []DiscoveredWorktree{{Path: wtPath, Branch: "feature/x"}},
	}, now))

	refreshed := mustGetProject(t, d, project.ID)
	require.Equal("bare", refreshed.RepositoryKind)
	require.Equal("develop", refreshed.DefaultBranch)
	require.False(refreshed.IsStale)

	wts := mustListWorktrees(t, d, project.ID)
	require.Len(wts, 1)
	require.Equal(wtPath, wts[0].Path)
	require.Equal("feature/x", wts[0].Branch)
	require.False(wts[0].IsStale)
}

// TestReconcileProjectInventory_PreservesWorktreeIDAndTmuxLink verifies that
// re-discovering the same path keeps the row's id (so linked tmux sessions
// survive the ON DELETE CASCADE) while refreshing its branch.
func TestReconcileProjectInventory_PreservesWorktreeIDAndTmuxLink(t *testing.T) {
	require := require.New(t)
	d := openTestDB(t)
	ctx := context.Background()
	now := time.Date(2026, 6, 4, 12, 0, 0, 0, time.UTC)

	project := createDiscoveryTestProject(t, d, "beta")
	wtPath := filepath.Join(t.TempDir(), "wt-1")

	require.NoError(d.ReconcileProjectInventory(ctx, project.ID, ProjectInventory{
		RepositoryKind: "standard",
		DefaultBranch:  "main",
		Worktrees:      []DiscoveredWorktree{{Path: wtPath, Branch: "feature/a"}},
	}, now))

	first := mustListWorktrees(t, d, project.ID)
	require.Len(first, 1)
	worktreeID := first[0].ID

	require.NoError(d.UpsertProjectWorktreeTmuxSession(ctx, &ProjectWorktreeTmuxSession{
		WorktreeID:  worktreeID,
		SessionKey:  "sess-key-1",
		TargetKey:   "codex",
		SessionName: "mm-beta-1",
		CreatedAt:   now,
	}))

	require.NoError(d.ReconcileProjectInventory(ctx, project.ID, ProjectInventory{
		RepositoryKind: "standard",
		DefaultBranch:  "main",
		Worktrees:      []DiscoveredWorktree{{Path: wtPath, Branch: "feature/b"}},
	}, now.Add(time.Minute)))

	second := mustListWorktrees(t, d, project.ID)
	require.Len(second, 1)
	require.Equal(worktreeID, second[0].ID, "stable path keeps the row id")
	require.Equal("feature/b", second[0].Branch, "branch refreshes on rediscovery")

	sessions, err := d.ListProjectWorktreeTmuxSessions(ctx, worktreeID)
	require.NoError(err)
	require.Len(sessions, 1, "tmux session link survives reconcile")
	require.Equal("sess-key-1", sessions[0].SessionKey)
}

// TestReconcileProjectInventory_StaleAndReappear verifies a worktree absent from
// a pass is marked stale (not deleted) and clears its stale flag — keeping its
// id — when the path reappears.
func TestReconcileProjectInventory_StaleAndReappear(t *testing.T) {
	require := require.New(t)
	d := openTestDB(t)
	ctx := context.Background()
	now := time.Date(2026, 6, 4, 12, 0, 0, 0, time.UTC)

	project := createDiscoveryTestProject(t, d, "gamma")
	wtPath := filepath.Join(t.TempDir(), "wt-vanishing")
	inv := ProjectInventory{
		RepositoryKind: "standard",
		DefaultBranch:  "main",
		Worktrees:      []DiscoveredWorktree{{Path: wtPath, Branch: "feature/x"}},
	}

	require.NoError(d.ReconcileProjectInventory(ctx, project.ID, inv, now))
	original := worktreeByPath(t, mustListWorktrees(t, d, project.ID), wtPath)
	require.False(original.IsStale)

	require.NoError(d.ReconcileProjectInventory(ctx, project.ID, ProjectInventory{
		RepositoryKind: "standard",
		DefaultBranch:  "main",
	}, now.Add(time.Minute)))
	stale := worktreeByPath(t, mustListWorktrees(t, d, project.ID), wtPath)
	require.True(stale.IsStale, "missing worktree is marked stale, not deleted")
	require.Equal(original.ID, stale.ID)

	require.NoError(d.ReconcileProjectInventory(ctx, project.ID, inv, now.Add(2*time.Minute)))
	revived := worktreeByPath(t, mustListWorktrees(t, d, project.ID), wtPath)
	require.False(revived.IsStale, "reappeared worktree clears its stale flag")
	require.Equal(original.ID, revived.ID)
}

// TestMarkProjectStaleThenReconcileClears verifies a failed discovery marks the
// project stale and a later successful pass clears it.
func TestMarkProjectStaleThenReconcileClears(t *testing.T) {
	require := require.New(t)
	d := openTestDB(t)
	ctx := context.Background()
	now := time.Date(2026, 6, 4, 12, 0, 0, 0, time.UTC)

	project := createDiscoveryTestProject(t, d, "delta")
	require.False(mustGetProject(t, d, project.ID).IsStale)

	require.NoError(d.MarkProjectStale(ctx, project.ID, now))
	require.True(mustGetProject(t, d, project.ID).IsStale)

	require.NoError(d.ReconcileProjectInventory(ctx, project.ID, ProjectInventory{
		RepositoryKind: "standard",
		DefaultBranch:  "main",
	}, now.Add(time.Minute)))
	require.False(mustGetProject(t, d, project.ID).IsStale)
}
