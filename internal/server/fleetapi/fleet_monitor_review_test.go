package fleetapi

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"go.kenn.io/forge/internal/config"
	"go.kenn.io/forge/internal/db"
	"go.kenn.io/forge/internal/procutil"
	"go.kenn.io/forge/internal/testutil/dbtest"
)

// seedStatsProject registers a repository with one feature worktree that is two
// lines ahead of main and returns the database, project, and worktree path.
func seedStatsProject(t *testing.T) (*db.DB, *db.Project, string) {
	t.Helper()
	repoDir, featDir := seedFingerprintRepo(t)
	database := dbtest.Open(t)
	ctx := context.Background()
	require.NoError(t, os.WriteFile(
		filepath.Join(featDir, "feature.txt"), []byte("x\ny\n"), 0o644,
	))
	runGit(t, featDir, "add", ".")
	runGit(t, featDir, "commit", "-q", "-m", "feature work")
	proj, err := database.CreateProject(ctx, db.CreateProjectInput{
		DisplayName: "app", LocalPath: repoDir, DefaultBranch: "main",
	})
	require.NoError(t, err)
	_, err = database.CreateProjectWorktree(ctx, db.CreateProjectWorktreeInput{
		ProjectID: proj.ID, Branch: "feature", Path: featDir,
	})
	require.NoError(t, err)
	return database, proj, featDir
}

func TestFleetWorktreeStatsForcedPassMeasuresUnstagedEdits(t *testing.T) {
	require := require.New(t)
	database, _, featDir := seedStatsProject(t)
	ctx := context.Background()
	sampler := &fleetWorktreeStatsSampler{db: database}

	sampler.runOnce(ctx)
	stats, err := database.ListWorktreeStats(ctx)
	require.NoError(err)
	require.Equal(2, stats[normPath(featDir)].DiffAdded)

	// An unstaged edit leaves the git directory untouched.
	require.NoError(os.WriteFile(
		filepath.Join(featDir, "feature.txt"), []byte("x\ny\nz\n"), 0o644,
	))
	sampler.runOnce(ctx)
	stats, err = database.ListWorktreeStats(ctx)
	require.NoError(err)
	require.Equal(2, stats[normPath(featDir)].DiffAdded,
		"the background pass cannot see an unstaged edit")

	sampler.runOnceForced(ctx)
	stats, err = database.ListWorktreeStats(ctx)
	require.NoError(err)
	require.Equal(3, stats[normPath(featDir)].DiffAdded,
		"the forced pass behind the refresh route measures it")
}

func TestFleetWorktreeStatsResamplesWhenDefaultBranchChanges(t *testing.T) {
	require := require.New(t)
	database, proj, featDir := seedStatsProject(t)
	ctx := context.Background()
	sampler := &fleetWorktreeStatsSampler{db: database}
	sampler.runOnce(ctx)
	stats, err := database.ListWorktreeStats(ctx)
	require.NoError(err)
	require.Equal(2, stats[normPath(featDir)].DiffAdded)

	// Discovery moving the project's default branch changes the diff base
	// without touching any git file: measured against itself the worktree
	// has no diff.
	require.NoError(database.ReconcileProjectInventory(ctx, proj.ID, db.ProjectInventory{
		RepositoryKind: "standard", DefaultBranch: "feature",
		Worktrees: []db.DiscoveredWorktree{{Path: featDir, Branch: "feature"}},
	}, time.Now()))
	sampler.runOnce(ctx)
	stats, err = database.ListWorktreeStats(ctx)
	require.NoError(err)
	require.Equal(0, stats[normPath(featDir)].DiffAdded,
		"a changed default branch invalidates the sample")
}

func TestFleetWorktreeStatsFingerprintAgeBoundsStaleness(t *testing.T) {
	require := require.New(t)
	database, _, featDir := seedStatsProject(t)
	ctx := context.Background()
	now := time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)
	sampler := &fleetWorktreeStatsSampler{db: database, now: func() time.Time { return now }}
	sampler.runOnce(ctx)

	require.NoError(os.WriteFile(
		filepath.Join(featDir, "feature.txt"), []byte("x\ny\nz\n"), 0o644,
	))
	now = now.Add(fleetWorktreeStatsFingerprintMaxAge - time.Second)
	sampler.runOnce(ctx)
	stats, err := database.ListWorktreeStats(ctx)
	require.NoError(err)
	require.Equal(2, stats[normPath(featDir)].DiffAdded, "inside the age bound the sample is reused")

	now = now.Add(2 * time.Second)
	sampler.runOnce(ctx)
	stats, err = database.ListWorktreeStats(ctx)
	require.NoError(err)
	require.Equal(3, stats[normPath(featDir)].DiffAdded, "past the age bound the worktree is re-measured")
}

func TestFleetWorktreeDiscoveryFingerprintAgeBoundsStaleness(t *testing.T) {
	require := require.New(t)
	repoDir, _ := seedFingerprintRepo(t)
	database := dbtest.Open(t)
	ctx := context.Background()
	project, err := database.CreateProject(ctx, db.CreateProjectInput{
		DisplayName: "app", LocalPath: repoDir,
	})
	require.NoError(err)
	now := time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)
	discoverer := newFleetWorktreeDiscoverer(database, nil)
	discoverer.now = func() time.Time { return now }
	discoverer.runOnce(ctx)

	require.NoError(database.MarkProjectStale(ctx, project.ID, now))
	now = now.Add(fleetWorktreeDiscoveryFingerprintMaxAge - time.Second)
	discoverer.runOnce(ctx)
	stale, err := database.GetProjectByID(ctx, project.ID)
	require.NoError(err)
	require.True(stale.IsStale, "inside the age bound the project is skipped")

	now = now.Add(2 * time.Second)
	discoverer.runOnce(ctx)
	fresh, err := database.GetProjectByID(ctx, project.ID)
	require.NoError(err)
	require.False(fresh.IsStale, "past the age bound the project is reconciled")
}

func TestFleetMonitorsStayActiveAfterSnapshotReadWithoutSubscribers(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	now := time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)
	handler := New(Deps{
		DB:              dbtest.Open(t),
		Config:          ConfigSnapshot{Fleet: config.Fleet{}},
		Now:             func() time.Time { return now },
		SubscriberCount: func() int { return 0 },
	})

	assert.False(handler.fleetWorktreeStatsSampler.gate.active(),
		"no subscribers and no reads means idle")

	// A hub consuming this daemon as a spoke reads the raw snapshot without
	// opening a local event stream; that read is demand.
	_, err := handler.getSnapshotRaw(context.Background(), nil)
	require.NoError(err)
	assert.True(handler.fleetWorktreeStatsSampler.gate.active())
	assert.True(handler.fleetWorktreeDiscoverer.gate.active())
	assert.True(handler.fleetTmuxMonitor.gate.active())

	now = now.Add(fleetMonitorDemandWindow + time.Second)
	assert.False(handler.fleetWorktreeStatsSampler.gate.active(),
		"demand lapses once the window passes without another read")
}

func TestFleetProcessProbeOutputExitStatusSemantics(t *testing.T) {
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("sh not available")
	}
	assert := assert.New(t)
	require := require.New(t)
	dir := t.TempDir()
	write := func(name, body string) string {
		path := filepath.Join(dir, name)
		require.NoError(os.WriteFile(path, []byte("#!/bin/sh\n"+body), 0o755))
		return path
	}
	noMatch := write("nomatch", "exit 1\n")
	usage := write("usage", "echo 'usage: bad flags' >&2\nexit 2\n")

	out, err := fleetProcessProbeOutput(context.Background(), "test", noMatch)
	require.NoError(err, "exit status 1 is the documented no-match outcome")
	assert.Empty(out)

	_, err = fleetProcessProbeOutput(context.Background(), "test", usage)
	assert.Error(err, "a usage failure must not be published as an empty tree")
}

func TestProbeFleetProcessTreesFollowsDescendants(t *testing.T) {
	for _, tool := range []string{"ps", "pgrep", "sh"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skipf("%s not available", tool)
		}
	}
	require := require.New(t)
	// A shell whose child is another sleeping shell: two generations below
	// the test process.
	cmd := procutil.Command("sh", "-c", "sh -c 'sleep 30; true' & wait")
	require.NoError(cmd.Start())
	t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait() })

	var processes map[int]fleetProcessInfo
	require.Eventually(func() bool {
		var err error
		processes, err = probeFleetProcessTrees(context.Background(), []int{cmd.Process.Pid})
		return err == nil && len(processes) >= 3
	}, 5*time.Second, 100*time.Millisecond,
		"the root, its shell child, and the sleep grandchild are all collected")
	_, hasRoot := processes[cmd.Process.Pid]
	require.True(hasRoot)
}
