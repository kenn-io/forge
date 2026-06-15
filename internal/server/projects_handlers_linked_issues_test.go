package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestSetWorktreeLinkedIssuesRoute drives the
// PUT /projects/{id}/worktrees/{id}/linked-issues route end to end: an unsorted
// list with a duplicate is stored normalized (sorted, deduped) and surfaces on
// the worktree list; a missing worktree returns 404.
func TestSetWorktreeLinkedIssuesRoute(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	require := require.New(t)

	srv, _ := setupTestServer(t)
	ts := httptest.NewServer(srv)
	defer ts.Close()

	repoDir := t.TempDir()
	require.NoError(initLocalOnlyGitRepo(t.Context(), repoDir))
	projectID := registerProjectForTest(t, ts, repoDir)
	wtPath := filepath.Join(t.TempDir(), "wt-feat")
	worktreeID := registerWorktreeForTest(
		t, ts, projectID, "feat", wtPath, http.StatusCreated,
	)

	body := mustMarshal(t, map[string]any{"linked_issue_numbers": []int{57, 42, 42}})
	resp := httpDo(t, ts, http.MethodPut,
		"/api/v1/projects/"+projectID+"/worktrees/"+worktreeID+"/linked-issues",
		body,
	)
	require.Equal(http.StatusOK, resp.StatusCode)
	var updated struct {
		LinkedIssueNumbers []int `json:"linked_issue_numbers"`
	}
	require.NoError(json.NewDecoder(resp.Body).Decode(&updated))
	resp.Body.Close()
	require.Equal([]int{42, 57}, updated.LinkedIssueNumbers)

	resp = httpDo(t, ts, http.MethodGet,
		"/api/v1/projects/"+projectID+"/worktrees", nil,
	)
	require.Equal(http.StatusOK, resp.StatusCode)
	var wtList struct {
		Worktrees []struct {
			Branch             string `json:"branch"`
			LinkedIssueNumbers []int  `json:"linked_issue_numbers"`
		} `json:"worktrees"`
	}
	require.NoError(json.NewDecoder(resp.Body).Decode(&wtList))
	resp.Body.Close()
	require.Len(wtList.Worktrees, 2, "root checkout row plus the worktree")
	var featLinked []int
	featFound := false
	for _, wt := range wtList.Worktrees {
		if wt.Branch == "feat" {
			featLinked = wt.LinkedIssueNumbers
			featFound = true
		}
	}
	require.True(featFound, "the feat worktree is listed")
	require.Equal([]int{42, 57}, featLinked)

	resp = httpDo(t, ts, http.MethodPut,
		"/api/v1/projects/"+projectID+"/worktrees/wtr_missing/linked-issues",
		body,
	)
	require.Equal(http.StatusNotFound, resp.StatusCode)
	resp.Body.Close()
}
