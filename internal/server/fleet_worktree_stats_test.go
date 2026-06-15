package server

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"go.kenn.io/middleman/internal/db"
	"go.kenn.io/middleman/internal/fleet"
	"go.kenn.io/middleman/internal/testutil/dbtest"
	"go.kenn.io/middleman/internal/workspace"
)

func TestApplyWorktreeStatsOverlaysAllFourCounts(t *testing.T) {
	require := require.New(t)
	stats := map[string]db.WorktreeGitStats{
		"/repo/wt": {DiffAdded: 5, DiffRemoved: 2, SyncAhead: 3, SyncBehind: 1},
	}

	sampled := &fleet.RawWorktree{Path: "/repo/wt"}
	applyWorktreeStats(sampled, stats)
	require.NotNil(sampled.DiffAdded)
	require.Equal(5, *sampled.DiffAdded)
	require.NotNil(sampled.DiffRemoved)
	require.Equal(2, *sampled.DiffRemoved)
	require.NotNil(sampled.SyncAhead)
	require.Equal(3, *sampled.SyncAhead)
	require.NotNil(sampled.SyncBehind)
	require.Equal(1, *sampled.SyncBehind)
}

func TestApplyWorktreeStatsZeroSampleStillSurfaces(t *testing.T) {
	require := require.New(t)
	// A sampled-but-zero worktree reports non-nil zero pointers (it was
	// measured), distinct from an unsampled worktree which stays nil.
	zero := &fleet.RawWorktree{Path: "/repo/clean"}
	applyWorktreeStats(zero, map[string]db.WorktreeGitStats{"/repo/clean": {}})
	require.NotNil(zero.DiffAdded)
	require.Equal(0, *zero.DiffAdded)
	require.NotNil(zero.SyncBehind)
	require.Equal(0, *zero.SyncBehind)

	unsampled := &fleet.RawWorktree{Path: "/repo/none"}
	applyWorktreeStats(unsampled, map[string]db.WorktreeGitStats{"/repo/other": {}})
	require.Nil(unsampled.DiffAdded)
	require.Nil(unsampled.SyncAhead)
}

func TestFleetWorktreeStatsCollectTargetsDedupesByPath(t *testing.T) {
	require := require.New(t)
	database := dbtest.Open(t)
	ctx := context.Background()

	appPath := filepath.Join(t.TempDir(), "app")
	proj, err := database.CreateProject(ctx, db.CreateProjectInput{
		DisplayName: "app", LocalPath: appPath, DefaultBranch: "main",
	})
	require.NoError(err)
	featPath := filepath.Join(t.TempDir(), "app-feat")
	_, err = database.CreateProjectWorktree(ctx, db.CreateProjectWorktreeInput{
		ProjectID: proj.ID, Branch: "feat", Path: featPath,
	})
	require.NoError(err)

	// A workspace whose path coincides with the registered worktree must dedupe
	// to one target carrying the project's default branch.
	require.NoError(database.InsertWorkspace(ctx, &db.Workspace{
		ID: "ws-feat", Platform: "github", PlatformHost: "github.com",
		RepoOwner: "o", RepoName: "app",
		ItemType: db.WorkspaceItemTypePullRequest, ItemNumber: 1,
		WorktreePath: featPath, Status: "ready",
	}))
	// An orphan workspace with no registered project and no synced repo yields a
	// distinct target with an empty default branch.
	orphanPath := filepath.Join(t.TempDir(), "orphan")
	require.NoError(database.InsertWorkspace(ctx, &db.Workspace{
		ID: "ws-orphan", Platform: "github", PlatformHost: "github.com",
		RepoOwner: "o", RepoName: "orphan",
		ItemType: db.WorkspaceItemTypePullRequest, ItemNumber: 2,
		WorktreePath: orphanPath, Status: "ready",
	}))

	sampler := &fleetWorktreeStatsSampler{db: database}
	targets, err := sampler.collectTargets(ctx)
	require.NoError(err)

	byPath := map[string]string{}
	for _, target := range targets {
		byPath[target.path] = target.defaultBranch
	}
	require.Len(targets, 3, "registered root + registered worktree + orphan")
	require.Equal("main", byPath[normPath(appPath)])
	require.Equal("main", byPath[normPath(featPath)],
		"registered worktree wins over the workspace at the same path")
	require.Empty(byPath[normPath(orphanPath)],
		"orphan workspace with an unsynced repo has no default branch")
}

func TestFleetWorktreeStatsSamplerSurfacesLiveDiffInSnapshot(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	require := require.New(t)
	database := dbtest.Open(t)
	ctx := context.Background()

	repoDir := t.TempDir()
	runGit(t, repoDir, "init", "-q")
	runGit(t, repoDir, "config", "user.email", "t@e.st")
	runGit(t, repoDir, "config", "user.name", "Tester")
	require.NoError(os.WriteFile(
		filepath.Join(repoDir, "base.txt"), []byte("base\n"), 0o644,
	))
	runGit(t, repoDir, "add", ".")
	runGit(t, repoDir, "commit", "-m", "base")

	featDir := filepath.Join(t.TempDir(), "feat")
	runGit(t, repoDir, "worktree", "add", "-b", "feature", featDir)
	require.NoError(os.WriteFile(
		filepath.Join(featDir, "feature.txt"), []byte("x\ny\n"), 0o644,
	))
	runGit(t, featDir, "add", ".")
	runGit(t, featDir, "commit", "-m", "feature work")

	proj, err := database.CreateProject(ctx, db.CreateProjectInput{
		DisplayName: "app", LocalPath: repoDir, DefaultBranch: "main",
	})
	require.NoError(err)
	_, err = database.CreateProjectWorktree(ctx, db.CreateProjectWorktreeInput{
		ProjectID: proj.ID, Branch: "feature", Path: featDir,
	})
	require.NoError(err)

	sampler := &fleetWorktreeStatsSampler{db: database}
	sampler.runOnce(ctx)

	srv := &Server{db: database, workspaces: workspace.NewManager(database, t.TempDir())}
	raw, err := srv.buildLocalRaw(ctx)
	require.NoError(err)

	wt := requireRawWorktree(t, raw.Worktrees, normPath(featDir))
	require.NotNil(wt.DiffAdded, "the sampled feature worktree surfaces diff counts")
	require.Equal(2, *wt.DiffAdded, "feature.txt is two added lines vs main")
	require.NotNil(wt.SyncAhead, "a sampled worktree surfaces all four counts")
}

func TestFleetWorktreeStatsSamplerFiresOnChange(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	require := require.New(t)
	database := dbtest.Open(t)
	ctx := context.Background()

	repoDir := t.TempDir()
	runGit(t, repoDir, "init", "-q")
	runGit(t, repoDir, "config", "user.email", "t@e.st")
	runGit(t, repoDir, "config", "user.name", "Tester")
	require.NoError(os.WriteFile(
		filepath.Join(repoDir, "base.txt"), []byte("base\n"), 0o644,
	))
	runGit(t, repoDir, "add", ".")
	runGit(t, repoDir, "commit", "-m", "base")

	featDir := filepath.Join(t.TempDir(), "feat")
	runGit(t, repoDir, "worktree", "add", "-b", "feature", featDir)
	require.NoError(os.WriteFile(
		filepath.Join(featDir, "feature.txt"), []byte("x\ny\n"), 0o644,
	))
	runGit(t, featDir, "add", ".")
	runGit(t, featDir, "commit", "-m", "feature work")

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
	require.Equal(1, fires, "first pass inserts new stats and fires once")

	sampler.runOnce(ctx)
	require.Equal(1, fires, "an unchanged pass does not fire again")
}

func TestFleetWorktreeStatsRefreshFiresOnChange(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	require := require.New(t)
	database := dbtest.Open(t)
	ctx := context.Background()

	repoDir := t.TempDir()
	runGit(t, repoDir, "init", "-q")
	runGit(t, repoDir, "config", "user.email", "t@e.st")
	runGit(t, repoDir, "config", "user.name", "Tester")
	require.NoError(os.WriteFile(
		filepath.Join(repoDir, "base.txt"), []byte("base\n"), 0o644,
	))
	runGit(t, repoDir, "add", ".")
	runGit(t, repoDir, "commit", "-m", "base")

	featDir := filepath.Join(t.TempDir(), "feat")
	runGit(t, repoDir, "worktree", "add", "-b", "feature", featDir)
	require.NoError(os.WriteFile(
		filepath.Join(featDir, "feature.txt"), []byte("x\ny\n"), 0o644,
	))
	runGit(t, featDir, "add", ".")
	runGit(t, featDir, "commit", "-m", "feature work")

	fires := 0
	sampler := &fleetWorktreeStatsSampler{
		db:        database,
		onChanged: func() { fires++ },
	}

	require.NoError(sampler.refreshWorktreeStats(ctx, featDir, "main"))
	require.Equal(1, fires, "first refresh inserts new stats and fires once")

	require.NoError(sampler.refreshWorktreeStats(ctx, featDir, "main"))
	require.Equal(1, fires, "an unchanged refresh does not fire again")
}

func requireRawWorktree(
	t *testing.T, worktrees []fleet.RawWorktree, path string,
) fleet.RawWorktree {
	t.Helper()
	for _, wt := range worktrees {
		if wt.Path == path {
			return wt
		}
	}
	require.Failf(t, "raw worktree not found", "no worktree at %s", path)
	return fleet.RawWorktree{}
}
