package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	Assert "github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	dbpkg "go.kenn.io/middleman/internal/db"
)

// TestListProjectBranches covers GET /api/v1/projects/{id}/branches: the
// project repository's local branch names, sorted.
func TestListProjectBranches(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	require := require.New(t)
	assert := Assert.New(t)

	srv, _ := setupTestServer(t)
	ts := httptest.NewServer(srv)
	defer ts.Close()

	repo := initLifecycleRouteRepo(t)
	runGit(t, repo, "branch", "feat/one")
	runGit(t, repo, "branch", "feat/two")
	projectID := registerProjectForTest(t, ts, repo)

	resp := httpDo(t, ts, http.MethodGet,
		"/api/v1/projects/"+projectID+"/branches", nil)
	require.Equal(http.StatusOK, resp.StatusCode)
	var body struct {
		Branches []string `json:"branches"`
	}
	require.NoError(json.NewDecoder(resp.Body).Decode(&body))
	resp.Body.Close()
	assert.Equal([]string{"feat/one", "feat/two", "main"}, body.Branches)

	missing := httpDo(t, ts, http.MethodGet,
		"/api/v1/projects/prj_nope/branches", nil)
	assert.Equal(http.StatusNotFound, missing.StatusCode)
	missing.Body.Close()
}

// TestInspectProjectWorktree covers
// GET /api/v1/projects/{pid}/worktrees/{wid}/inspect: dirty state, live
// session count, and branch-delete eligibility for delete confirmation UIs.
func TestInspectProjectWorktree(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	require := require.New(t)
	assert := Assert.New(t)

	srv, _ := setupTestServer(t)
	ts := httptest.NewServer(srv)
	defer ts.Close()

	repo := initLifecycleRouteRepo(t)
	projectID := registerProjectForTest(t, ts, repo)

	// Create a linked worktree on its own branch through the API.
	body := mustMarshal(t, map[string]any{
		"branch":         "feat/inspect",
		"create_on_disk": true,
	})
	created := httpDo(t, ts, http.MethodPost,
		"/api/v1/projects/"+projectID+"/worktrees", body)
	require.Equal(http.StatusCreated, created.StatusCode)
	created.Body.Close()

	rows := listWorktreeRows(t, ts, projectID)
	linked := worktreeRowByBranch(rows, "feat/inspect")
	require.NotNil(linked)
	root := worktreeRowByPathBase(rows, filepath.Base(repo))
	require.NotNil(root)

	type inspection struct {
		IsDirty                   bool     `json:"is_dirty"`
		DirtyFileCount            int      `json:"dirty_file_count"`
		AliveSessionCount         int      `json:"alive_session_count"`
		CanDeleteBranch           bool     `json:"can_delete_branch"`
		BranchDeleteBlockedReason string   `json:"branch_delete_blocked_reason"`
		SiblingWorktreeIDs        []string `json:"sibling_worktree_ids"`
	}
	inspect := func(worktreeID string) inspection {
		resp := httpDo(t, ts, http.MethodGet,
			"/api/v1/projects/"+projectID+"/worktrees/"+
				worktreeID+"/inspect", nil)
		require.Equal(http.StatusOK, resp.StatusCode)
		var got inspection
		require.NoError(json.NewDecoder(resp.Body).Decode(&got))
		resp.Body.Close()
		return got
	}

	// A clean linked worktree on its own branch is fully deletable.
	got := inspect(linked["id"].(string))
	assert.False(got.IsDirty)
	assert.Zero(got.DirtyFileCount)
	assert.Zero(got.AliveSessionCount)
	assert.True(got.CanDeleteBranch)
	assert.Empty(got.BranchDeleteBlockedReason)

	// Dirty the worktree: two untracked files are counted.
	require.NoError(os.WriteFile(
		filepath.Join(linked["path"].(string), "a.txt"), []byte("x"), 0o644))
	require.NoError(os.WriteFile(
		filepath.Join(linked["path"].(string), "b.txt"), []byte("y"), 0o644))
	got = inspect(linked["id"].(string))
	assert.True(got.IsDirty)
	assert.Equal(2, got.DirtyFileCount)

	// The primary root row protects its branch (the default branch).
	got = inspect(root["id"].(string))
	assert.False(got.CanDeleteBranch)
	assert.NotEmpty(got.BranchDeleteBlockedReason)
}

// TestInspectProjectWorktreeCountsStoredTmuxSessions proves a durable
// tmux session that survived a daemon restart (stored row, no in-memory
// runtime session) still counts as alive — otherwise delete confirmation
// under-reports live work after a restart.
func TestInspectProjectWorktreeCountsStoredTmuxSessions(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	require := require.New(t)
	assert := Assert.New(t)

	srv, database := setupTestServer(t)
	ts := httptest.NewServer(srv)
	defer ts.Close()

	repo := initLifecycleRouteRepo(t)
	projectID := registerProjectForTest(t, ts, repo)
	rows := listWorktreeRows(t, ts, projectID)
	root := worktreeRowByPathBase(rows, filepath.Base(repo))
	require.NotNil(root)
	worktreeID := root["id"].(string)

	require.NoError(database.UpsertProjectWorktreeTmuxSession(
		t.Context(), &dbpkg.ProjectWorktreeTmuxSession{
			WorktreeID:  worktreeID,
			SessionKey:  "restart-survivor",
			SessionName: "middleman-restart-survivor",
			Label:       "Survivor",
			CreatedAt:   time.Now().UTC(),
		},
	))

	resp := httpDo(t, ts, http.MethodGet,
		"/api/v1/projects/"+projectID+"/worktrees/"+worktreeID+"/inspect", nil)
	require.Equal(http.StatusOK, resp.StatusCode)
	var got struct {
		AliveSessionCount int `json:"alive_session_count"`
	}
	require.NoError(json.NewDecoder(resp.Body).Decode(&got))
	resp.Body.Close()
	assert.Equal(1, got.AliveSessionCount,
		"stored tmux sessions count as alive after a restart")
}

// TestInspectProjectWorktreeCountsStoredTmuxSessionsWithRuntime is the
// restart case with a runtime manager configured: the in-memory runtime
// has no entry for the stored row (the daemon restarted), and the
// merged listing must still count the durable session as alive.
func TestInspectProjectWorktreeCountsStoredTmuxSessionsWithRuntime(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	require := require.New(t)
	assert := Assert.New(t)

	srv, projectID, worktreeID, _ :=
		setupProjectWorktreeCommandSessionTestWithRecord(t)
	require.NotNil(srv.runtime, "fixture must configure a runtime manager")
	ts := httptest.NewServer(srv)
	defer ts.Close()

	require.NoError(srv.db.UpsertProjectWorktreeTmuxSession(
		t.Context(), &dbpkg.ProjectWorktreeTmuxSession{
			WorktreeID:  worktreeID,
			SessionKey:  "restart-survivor-runtime",
			SessionName: "middleman-restart-survivor-runtime",
			Label:       "Survivor",
			CreatedAt:   time.Now().UTC(),
		},
	))

	resp := httpDo(t, ts, http.MethodGet,
		"/api/v1/projects/"+projectID+"/worktrees/"+worktreeID+"/inspect", nil)
	require.Equal(http.StatusOK, resp.StatusCode)
	var got struct {
		AliveSessionCount int `json:"alive_session_count"`
	}
	require.NoError(json.NewDecoder(resp.Body).Decode(&got))
	resp.Body.Close()
	assert.Equal(1, got.AliveSessionCount,
		"stored tmux sessions count as alive when the runtime has no in-memory entry")
}
