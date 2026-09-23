package server

import (
	serverfake "go.kenn.io/forge/internal/testutil/serverfake"

	jsonv2 "encoding/json/v2"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/forge/internal/db"
)

func TestRelayWorkflowActivitySignalsOnlyActions(t *testing.T) {
	t.Parallel()
	assert := assert.New(t)
	require := require.New(t)
	srv := newTestServer(t)
	t.Cleanup(func() { serverfake.GracefulShutdown(t, srv) })
	repoID, err := srv.db.UpsertRepo(t.Context(), db.RepoIdentity{
		Platform: "github", PlatformHost: "github.com", PlatformRepoID: "R_project", Owner: "team", Name: "project",
	})
	require.NoError(err)
	srv.syncevents.BroadcastRelayRefresh(t.Context(), repoID, "workflow_runs", 0)
	events, _, stale := srv.Hub().ReplaySnapshotSince(0)
	require.False(stale)
	require.Len(events, 1)
	assert.Equal("workflow_runs_changed", events[0].Event.Type)
	payload, err := jsonv2.Marshal(events[0].Event.Data)
	require.NoError(err)
	assert.JSONEq(`{"provider":"github","platform_host":"github.com","platform_repo_id":"R_project"}`, string(payload))
}
