package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	Assert "github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"go.kenn.io/middleman/internal/db"
)

func launchCommandSessionForDeleteTest(
	t *testing.T,
	ts *httptest.Server,
	projectID string,
	worktreeID string,
	sessionKey string,
) (tmuxSession string) {
	t.Helper()
	body := mustMarshal(t, map[string]any{
		"session_key": sessionKey,
		"command":     []string{"/bin/sh", "-lc", "exec sleep 60"},
		"label":       "Delete Test Shell",
	})
	resp := httpDo(t, ts, http.MethodPost,
		"/api/v1/projects/"+projectID+"/worktrees/"+worktreeID+
			"/runtime/sessions",
		body,
	)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	var session map[string]any
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&session))
	resp.Body.Close()
	tmuxSession, _ = session["tmux_session"].(string)
	require.NotEmpty(t, tmuxSession)
	return tmuxSession
}

func assertFakeTmuxKilledSession(
	t *testing.T,
	recordPath string,
	tmuxSession string,
) {
	t.Helper()
	record, err := os.ReadFile(recordPath)
	require.NoError(t, err)
	Assert.Contains(
		t, string(record), "kill-session\x00-t\x00"+tmuxSession,
	)
}

func TestRemoveProjectWorktreeStopsRuntimeSessions(t *testing.T) {
	requirePTYAvailable(t)
	require := require.New(t)
	assert := Assert.New(t)

	srv, projectID, worktreeID, recordPath :=
		setupProjectWorktreeCommandSessionTestWithRecord(t)
	ts := httptest.NewServer(srv)
	defer ts.Close()

	tmuxSession := launchCommandSessionForDeleteTest(
		t, ts, projectID, worktreeID, "surface:host:wt:shell:leaf",
	)

	resp := httpDo(t, ts, http.MethodPost,
		"/api/v1/projects/"+projectID+"/worktrees/"+worktreeID+"/delete",
		mustMarshal(t, map[string]any{}),
	)
	require.Equal(http.StatusNoContent, resp.StatusCode)
	resp.Body.Close()

	assertFakeTmuxKilledSession(t, recordPath, tmuxSession)
	scope := projectWorktreeRuntimeScope(worktreeID)
	assert.Empty(srv.runtime.ListSessions(scope))
	rows, err := srv.db.ListProjectWorktreeTmuxSessions(
		context.Background(), worktreeID,
	)
	require.NoError(err)
	assert.Empty(rows)
}

func TestDeleteProjectStopsWorktreeRuntimeSessions(t *testing.T) {
	requirePTYAvailable(t)
	require := require.New(t)
	assert := Assert.New(t)

	srv, projectID, worktreeID, recordPath :=
		setupProjectWorktreeCommandSessionTestWithRecord(t)
	ts := httptest.NewServer(srv)
	defer ts.Close()

	tmuxSession := launchCommandSessionForDeleteTest(
		t, ts, projectID, worktreeID, "surface:host:wt:shell:leaf",
	)

	resp := httpDo(t, ts, http.MethodDelete,
		"/api/v1/projects/"+projectID, nil,
	)
	require.Equal(http.StatusNoContent, resp.StatusCode)
	resp.Body.Close()

	assertFakeTmuxKilledSession(t, recordPath, tmuxSession)
	scope := projectWorktreeRuntimeScope(worktreeID)
	assert.Empty(srv.runtime.ListSessions(scope))
}

func TestDeleteProjectWorktreeKillsStoredTmuxSession(t *testing.T) {
	require := require.New(t)

	srv, projectID, worktreeID, recordPath :=
		setupProjectWorktreeCommandSessionTestWithRecord(t)
	ts := httptest.NewServer(srv)
	defer ts.Close()

	// A stored row without a live runtime session models a tmux session
	// surviving from before a middleman restart.
	require.NoError(srv.db.UpsertProjectWorktreeTmuxSession(
		context.Background(), &db.ProjectWorktreeTmuxSession{
			WorktreeID:  worktreeID,
			SessionKey:  "surface:host:wt:shell:leaf",
			SessionName: "middleman-stored-command",
			Label:       "Stored Shell",
		},
	))

	resp := httpDo(t, ts, http.MethodDelete,
		"/api/v1/projects/"+projectID+"/worktrees/"+worktreeID, nil,
	)
	require.Equal(http.StatusNoContent, resp.StatusCode)
	resp.Body.Close()

	assertFakeTmuxKilledSession(
		t, recordPath, "middleman-stored-command",
	)
}
