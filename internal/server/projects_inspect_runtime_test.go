package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	dbpkg "go.kenn.io/forge/internal/db"
)

func TestInspectProjectWorktreeCountsStoredTmuxSessionsWithRuntime(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	require := require.New(t)
	assert := assert.New(t)

	srv, projectID, worktreeID, _ :=
		setupProjectWorktreeCommandSessionTestWithRecord(t)
	require.NotNil(srv.runtime, "fixture must configure a runtime manager")
	ts := httptest.NewServer(srv)
	defer ts.Close()

	require.NoError(srv.db.UpsertProjectWorktreeTmuxSession(
		t.Context(), &dbpkg.ProjectWorktreeTmuxSession{
			WorktreeID:  worktreeID,
			SessionKey:  "restart-survivor-runtime",
			SessionName: "kenn-forge-restart-survivor-runtime",
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
