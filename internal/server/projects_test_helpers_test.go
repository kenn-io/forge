package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"
)

func registerProjectForTest(t *testing.T, ts *httptest.Server, localPath string) string {
	t.Helper()
	body := mustMarshal(t, map[string]any{"local_path": localPath})
	resp := httpDo(t, ts, http.MethodPost, "/api/v1/projects", body)
	require.Equal(t, http.StatusCreated, resp.StatusCode)
	defer resp.Body.Close()
	var project struct {
		ID string `json:"id"`
	}
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&project))
	require.NotEmpty(t, project.ID)
	return project.ID
}

func registerWorktreeForTest(
	t *testing.T, ts *httptest.Server, projectID, branch, path string, wantStatus int,
) string {
	t.Helper()
	body := mustMarshal(t, map[string]any{"branch": branch, "path": path})
	resp := httpDo(
		t, ts, http.MethodPost,
		"/api/v1/projects/"+projectID+"/worktrees", body,
	)
	require.Equal(t, wantStatus, resp.StatusCode)
	defer resp.Body.Close()
	if wantStatus < 200 || wantStatus >= 300 {
		return ""
	}
	var worktree struct {
		ID string `json:"id"`
	}
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&worktree))
	require.NotEmpty(t, worktree.ID)
	return worktree.ID
}
