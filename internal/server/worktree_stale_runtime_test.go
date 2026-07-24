package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/middleman/internal/db"
	"go.kenn.io/middleman/internal/server/workspaceapi"
)

func TestRemoveStaleWorktreeRouteStopsRuntimeSessions(t *testing.T) {
	requirePTYAvailable(t)
	require := require.New(t)
	assert := assert.New(t)

	srv, projectID, worktreeID, recordPath :=
		setupProjectWorktreeCommandSessionTestWithRecord(t)
	ts := httptest.NewServer(srv)
	defer ts.Close()

	worktree, err := srv.db.GetProjectWorktreeByID(t.Context(), worktreeID)
	require.NoError(err)
	tmuxSession := launchCommandSessionForDeleteTest(
		t, ts, projectID, worktreeID, "surface:host:wt:shell:leaf",
	)
	require.NoError(os.RemoveAll(worktree.Path))
	require.NoError(srv.db.ReconcileProjectInventory(
		t.Context(), projectID, db.ProjectInventory{}, time.Now(),
	))

	resp := httpDo(t, ts, http.MethodPost, "/api/v1/worktrees/remove-stale",
		mustMarshal(t, map[string]any{"scopedKey": "worktree:" + worktree.Path}))
	require.Equal(http.StatusOK, resp.StatusCode)
	resp.Body.Close()

	assertFakeTmuxKilledSession(t, recordPath, tmuxSession)
	assert.Empty(srv.runtime.ListSessions(workspaceapi.ProjectWorktreeRuntimeScope(worktreeID)))
	rows, err := srv.db.ListProjectWorktreeTmuxSessions(
		context.Background(), worktreeID,
	)
	require.NoError(err)
	assert.Empty(rows)
}
