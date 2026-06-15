package server

import (
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestRefreshWorktreeStatsRoute covers the targeted stats refresh: after a
// worktree is registered, POST .../refresh-stats samples that one worktree's git
// stats now so the fleet snapshot surfaces its diff/sync counts immediately,
// without waiting for the 30s background sampler. It asserts the counts were nil
// before the refresh (the route did the work, not the sampler), that only the
// targeted worktree is sampled (a sibling worktree stays nil), and that an
// unknown worktree under the project is a 404.
func TestRefreshWorktreeStatsRoute(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	require := require.New(t)

	srv, _ := setupTestServer(t)
	ts := httptest.NewServer(srv)
	defer ts.Close()
	ctx := t.Context()

	// A real repo with a feature worktree two lines ahead of main, so the
	// sampled diff is non-zero and observable.
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

	projectID := registerProjectForTest(t, ts, repoDir)
	featID := registerWorktreeForTest(
		t, ts, projectID, "feature", featDir, http.StatusCreated,
	)
	// A sibling worktree the refresh must not touch (proves targeting). Its
	// path need not be a real worktree: it is registered but never sampled.
	otherDir := filepath.Join(t.TempDir(), "other")
	otherID := registerWorktreeForTest(
		t, ts, projectID, "other", otherDir, http.StatusCreated,
	)
	require.NotEqual(featID, otherID)

	// Before the targeted refresh neither worktree has a stats sample.
	rawBefore, err := srv.buildLocalRaw(ctx)
	require.NoError(err)
	require.Nil(
		requireRawWorktree(t, rawBefore.Worktrees, normPath(featDir)).DiffAdded,
		"diff counts are nil until something samples the worktree",
	)

	resp := httpDo(t, ts, http.MethodPost,
		"/api/v1/projects/"+projectID+"/worktrees/"+featID+"/refresh-stats", nil,
	)
	require.Equal(http.StatusOK, resp.StatusCode)
	resp.Body.Close()

	// After the targeted refresh the feature worktree surfaces its live diff,
	// while the sibling stays nil — the refresh sampled only the target.
	rawAfter, err := srv.buildLocalRaw(ctx)
	require.NoError(err)
	feat := requireRawWorktree(t, rawAfter.Worktrees, normPath(featDir))
	require.NotNil(feat.DiffAdded, "the targeted refresh populates diff counts now")
	require.Equal(2, *feat.DiffAdded, "feature.txt is two added lines vs main")
	require.NotNil(feat.SyncAhead, "a sampled worktree surfaces all four counts")
	require.Nil(
		requireRawWorktree(t, rawAfter.Worktrees, normPath(otherDir)).DiffAdded,
		"the sibling worktree was not part of the targeted refresh",
	)

	// An unknown worktree under the valid project is a 404.
	resp = httpDo(t, ts, http.MethodPost,
		"/api/v1/projects/"+projectID+"/worktrees/wtr_missing/refresh-stats", nil,
	)
	require.Equal(http.StatusNotFound, resp.StatusCode)
	resp.Body.Close()
}

// TestRefreshFleetStatsRoute covers the fleet-wide stats refresh: POST
// /api/v1/snapshot/refresh-stats samples every worktree's git stats now,
// including each project's synthesized PRIMARY worktree. The primary has no
// registry row, so the per-worktree refresh route cannot reach it; only a
// fleet-wide pass keeps its diff/sync counts coherent after a mutation or an
// explicit refresh action. It asserts the primary and a linked worktree both
// have nil counts before the refresh (the route did the work, not the 30s
// background sampler) and both carry live counts after a single POST.
func TestRefreshFleetStatsRoute(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	require := require.New(t)

	srv, _ := setupTestServer(t)
	ts := httptest.NewServer(srv)
	defer ts.Close()
	ctx := t.Context()

	// A real repo whose main branch has one commit, plus a feature worktree two
	// lines ahead so its sampled diff is non-zero and observable.
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

	projectID := registerProjectForTest(t, ts, repoDir)
	registerWorktreeForTest(t, ts, projectID, "feature", featDir, http.StatusCreated)

	// Before the refresh neither the primary (repoDir) nor the feature worktree
	// has a stats sample.
	rawBefore, err := srv.buildLocalRaw(ctx)
	require.NoError(err)
	require.True(
		requireRawWorktree(t, rawBefore.Worktrees, normPath(repoDir)).IsPrimary,
		"the project root is the synthesized primary worktree",
	)
	require.Nil(
		requireRawWorktree(t, rawBefore.Worktrees, normPath(repoDir)).DiffAdded,
		"the primary has no stats until something samples it",
	)
	require.Nil(
		requireRawWorktree(t, rawBefore.Worktrees, normPath(featDir)).DiffAdded,
		"the feature worktree has no stats until something samples it",
	)

	resp := httpDo(t, ts, http.MethodPost, "/api/v1/snapshot/refresh-stats", nil)
	require.Equal(http.StatusOK, resp.StatusCode)
	resp.Body.Close()

	// A single fleet refresh samples both the synthesized primary — unreachable
	// via the per-worktree route — and the linked worktree.
	rawAfter, err := srv.buildLocalRaw(ctx)
	require.NoError(err)
	primary := requireRawWorktree(t, rawAfter.Worktrees, normPath(repoDir))
	require.NotNil(primary.DiffAdded,
		"the fleet refresh populates the primary's diff counts now")
	require.NotNil(primary.SyncAhead,
		"a sampled worktree surfaces all four counts")
	feat := requireRawWorktree(t, rawAfter.Worktrees, normPath(featDir))
	require.NotNil(feat.DiffAdded,
		"the fleet refresh populates the feature's diff counts now")
	require.Equal(2, *feat.DiffAdded, "feature.txt is two added lines vs main")
}
