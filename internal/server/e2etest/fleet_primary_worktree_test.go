package e2etest

import (
	"context"
	"encoding/json"
	"net/http"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"go.kenn.io/middleman/internal/fleet"
	"go.kenn.io/middleman/internal/procutil"
)

// gitInitRepoWithWorktree creates a real git repo with one empty commit and a
// linked worktree, returning the repo root and the worktree path. Skips the
// test when git is unavailable.
func gitInitRepoWithWorktree(t *testing.T, worktreeName string) (string, string) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	repoDir := t.TempDir()
	gitRun(t, repoDir, "init", "-q", "-b", "main")
	gitRun(t, repoDir, "-c", "user.email=t@e.st", "-c", "user.name=Tester",
		"commit", "--allow-empty", "-m", "init")
	wtDir := filepath.Join(t.TempDir(), worktreeName)
	gitRun(t, repoDir, "worktree", "add", "-q", "-b", "feature/live", wtDir)
	return repoDir, wtDir
}

func gitRun(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := procutil.Command("git", append([]string{"-C", dir}, args...)...)
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "git %v: %s", args, out)
}

// registerProjectE2E registers a local checkout over HTTP and returns its
// registry id.
func registerProjectE2E(t *testing.T, ts string, client *http.Client, path string) string {
	t.Helper()
	status, body := postJSON(t, client, ts+"/api/v1/projects",
		map[string]any{"local_path": path})
	require.Equal(t, http.StatusCreated, status, body)
	id := jsonField(t, body, "id")
	require.NotEmpty(t, id)
	return id
}

// TestPrimaryRootWorktreeIsRegisteredE2E proves the project root checkout is a
// first-class worktree row: it carries a registry id in the snapshot, stays
// flagged primary, and its runtime routes are addressable like any linked
// worktree's.
func TestPrimaryRootWorktreeIsRegisteredE2E(t *testing.T) {
	require := require.New(t)
	ts, _ := bootFleetServer(t, nil)
	repoDir, _ := gitInitRepoWithWorktree(t, "wt-live")

	projectID := registerProjectE2E(t, ts.URL, ts.Client(), repoDir)

	var raw fleet.RawSnapshot
	getJSON(t, ts, "/api/v1/snapshot/raw", &raw)

	rootKey := "worktree:" + filepath.Clean(repoDir)
	var root *fleet.RawWorktree
	for i := range raw.Worktrees {
		if raw.Worktrees[i].ScopedKey == rootKey {
			root = &raw.Worktrees[i]
		}
	}
	require.NotNil(root, "root checkout must appear in the snapshot")
	require.True(root.IsPrimary, "root row keeps the primary flag")
	require.NotEmpty(root.RegistryID, "root row carries its registry id")
	require.Equal("main", root.Branch, "root branch comes from git, not the project default")

	// The root row's runtime surface is addressable: GET .../runtime serves
	// launch targets for it instead of 404ing on a missing registry row.
	status, body := getRaw(t, ts.Client(), ts.URL+
		"/api/v1/projects/"+projectID+"/worktrees/"+root.RegistryID+"/runtime")
	require.Equal(http.StatusOK, status, body)
	require.Contains(body, "launch_targets")
}

// TestRemovePrimaryRootWorktreeRefusedE2E proves both worktree removal routes
// refuse the primary root row — deleting it would orphan the project's own
// checkout registration.
func TestRemovePrimaryRootWorktreeRefusedE2E(t *testing.T) {
	require := require.New(t)
	ts, _ := bootFleetServer(t, nil)
	repoDir, _ := gitInitRepoWithWorktree(t, "wt-rm")

	projectID := registerProjectE2E(t, ts.URL, ts.Client(), repoDir)

	var raw fleet.RawSnapshot
	getJSON(t, ts, "/api/v1/snapshot/raw", &raw)
	rootKey := "worktree:" + filepath.Clean(repoDir)
	var rootID string
	for _, w := range raw.Worktrees {
		if w.ScopedKey == rootKey {
			rootID = w.RegistryID
		}
	}
	require.NotEmpty(rootID)

	status, body := postJSON(t, ts.Client(),
		ts.URL+"/api/v1/projects/"+projectID+"/worktrees/"+rootID+"/delete",
		map[string]any{"remove_from_disk": false})
	require.Equal(http.StatusConflict, status, body)
	require.Contains(body, "primary")

	status, body = deleteJSON(t, ts.Client(),
		ts.URL+"/api/v1/projects/"+projectID+"/worktrees/"+rootID)
	require.Equal(http.StatusConflict, status, body)
	require.Contains(body, "primary")
}

// jsonField extracts a top-level string field from a JSON object body.
func jsonField(t *testing.T, body, field string) string {
	t.Helper()
	var decoded map[string]any
	require.NoError(t, json.Unmarshal([]byte(body), &decoded), body)
	value, _ := decoded[field].(string)
	return value
}

// TestPrimaryRootWorktreeListsAsPrimaryE2E proves the project worktree list
// labels the root row so clients can distinguish the non-removable primary
// from linked worktrees.
func TestPrimaryRootWorktreeListsAsPrimaryE2E(t *testing.T) {
	require := require.New(t)
	ts, _ := bootFleetServer(t, nil)
	repoDir, wtDir := gitInitRepoWithWorktree(t, "wt-list")

	projectID := registerProjectE2E(t, ts.URL, ts.Client(), repoDir)

	var listed struct {
		Worktrees []struct {
			Path      string `json:"path"`
			Branch    string `json:"branch"`
			IsPrimary bool   `json:"is_primary"`
		} `json:"worktrees"`
	}
	getJSON(t, ts, "/api/v1/projects/"+projectID+"/worktrees", &listed)
	require.Len(listed.Worktrees, 2)
	for _, w := range listed.Worktrees {
		if w.Branch == "feature/live" {
			require.False(w.IsPrimary, "linked worktree %s", wtDir)
		} else {
			require.True(w.IsPrimary, "root row %s", w.Path)
		}
	}
}

// TestPrimaryRootWorktreeInheritsProjectStaleE2E proves a stale project
// (discovery failed) reports its primary root row stale in the snapshot,
// not as a healthy checkout.
func TestPrimaryRootWorktreeInheritsProjectStaleE2E(t *testing.T) {
	require := require.New(t)
	ts, database := bootFleetServer(t, nil)
	repoDir, _ := gitInitRepoWithWorktree(t, "wt-stale")

	registerProjectE2E(t, ts.URL, ts.Client(), repoDir)

	var raw fleet.RawSnapshot
	getJSON(t, ts, "/api/v1/snapshot/raw", &raw)
	rootKey := "worktree:" + filepath.Clean(repoDir)
	var projectID string
	for _, w := range raw.Worktrees {
		if w.ScopedKey == rootKey {
			for _, p := range raw.Projects {
				if p.ScopedKey == w.ProjectKey {
					projectID = p.RegistryID
				}
			}
		}
	}
	require.NotEmpty(projectID)

	require.NoError(database.MarkProjectStale(
		context.Background(), projectID, time.Now(),
	))

	getJSON(t, ts, "/api/v1/snapshot/raw", &raw)
	for _, w := range raw.Worktrees {
		if w.ScopedKey == rootKey {
			require.True(w.IsStale,
				"primary root must inherit project staleness")
			return
		}
	}
	require.Fail("root worktree missing from snapshot")
}
