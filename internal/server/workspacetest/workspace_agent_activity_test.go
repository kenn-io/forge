package workspacetest

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/middleman/internal/agentactivity"
	"go.kenn.io/middleman/internal/apiclient/generated"
	"go.kenn.io/middleman/internal/config"
)

func TestWorkspaceAgentActivityFlowsThroughHTTPResponsesE2E(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	cfg := &config.Config{Agents: []config.Agent{{
		Key: "hook-agent", Label: "Hook agent",
		Command: []string{"/bin/sh", "-c", "while :; do sleep 1; done"},
	}}}
	fixture := setupWorkspaceServerFixture(t, cfg)
	ctx := t.Context()
	ws := createReadyWorkspace(t, ctx, fixture.client)

	launch, err := fixture.client.HTTP.LaunchWorkspaceRuntimeSessionWithResponse(
		ctx, ws.Id,
		generated.LaunchWorkspaceRuntimeSessionInputBody{TargetKey: "hook-agent"},
	)
	require.NoError(err)
	require.Equal(http.StatusOK, launch.StatusCode())
	require.NotNil(launch.JSON200)

	store := agentactivity.NewStore(fixture.agentActivityDir)
	for _, report := range []struct {
		sessionID  string
		runtimeKey string
		event      string
	}{
		{sessionID: "wrong-agent", runtimeKey: "wrong-runtime", event: "PermissionRequest"},
		{sessionID: "wrong-worktree", runtimeKey: launch.JSON200.Key, event: "PermissionRequest"},
		{sessionID: "live-agent", runtimeKey: launch.JSON200.Key, event: "UserPromptSubmit"},
	} {
		cwd := ws.WorktreePath
		if report.sessionID == "wrong-worktree" {
			cwd = t.TempDir()
		}
		payload, marshalErr := json.Marshal(map[string]string{
			"session_id":      report.sessionID,
			"cwd":             cwd,
			"hook_event_name": report.event,
		})
		require.NoError(marshalErr)
		require.NoError(store.HandleHook(
			strings.NewReader(string(payload)), report.runtimeKey,
		))
	}

	getResponse, err := fixture.client.HTTP.GetWorkspaceWithResponse(ctx, ws.Id)
	require.NoError(err)
	require.Equal(http.StatusOK, getResponse.StatusCode())
	require.NotNil(getResponse.JSON200)
	require.NotNil(getResponse.JSON200.AgentState)
	require.NotNil(getResponse.JSON200.AgentStateUpdatedAt)
	assert.Equal(generated.Working, *getResponse.JSON200.AgentState)
	assert.Equal(time.UTC, getResponse.JSON200.AgentStateUpdatedAt.Location())

	require.NoError(os.WriteFile(
		filepath.Join(ws.WorktreePath, "activity.txt"), []byte("activity\n"), 0o644,
	))
	runGit(t, ws.WorktreePath, "add", "activity.txt")
	runGit(t, ws.WorktreePath, "commit", "-m", "add activity fixture")
	pushResponse, err := fixture.client.HTTP.PushWorkspaceBranchWithResponse(ctx, ws.Id)
	require.NoError(err)
	require.Equal(http.StatusOK, pushResponse.StatusCode(), string(pushResponse.Body))
	require.NotNil(pushResponse.JSON200)
	require.NotNil(pushResponse.JSON200.AgentState)
	assert.Equal(generated.Working, *pushResponse.JSON200.AgentState)

	payload, err := json.Marshal(map[string]string{
		"session_id":      "live-agent",
		"cwd":             ws.WorktreePath,
		"hook_event_name": "Stop",
	})
	require.NoError(err)
	require.NoError(store.HandleHook(
		strings.NewReader(string(payload)), launch.JSON200.Key,
	))
	getResponse, err = fixture.client.HTTP.GetWorkspaceWithResponse(ctx, ws.Id)
	require.NoError(err)
	require.Equal(http.StatusOK, getResponse.StatusCode())
	require.NotNil(getResponse.JSON200)
	require.NotNil(getResponse.JSON200.AgentState)
	assert.Equal(generated.Idle, *getResponse.JSON200.AgentState)

	stopResponse, err := fixture.client.HTTP.StopWorkspaceRuntimeSessionWithResponse(
		ctx, ws.Id, launch.JSON200.Key,
	)
	require.NoError(err)
	require.Equal(http.StatusNoContent, stopResponse.StatusCode())
	_, ok := store.SnapshotForWorkspace(ws.WorktreePath, []string{launch.JSON200.Key})
	assert.False(ok)

	getResponse, err = fixture.client.HTTP.GetWorkspaceWithResponse(ctx, ws.Id)
	require.NoError(err)
	require.Equal(http.StatusOK, getResponse.StatusCode())
	require.NotNil(getResponse.JSON200)
	assert.Nil(getResponse.JSON200.AgentState)
}
