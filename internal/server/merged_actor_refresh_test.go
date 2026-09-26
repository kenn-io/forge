package server

import (
	jsonv2 "encoding/json/v2"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"go.kenn.io/forge/internal/db"
	"go.kenn.io/forge/internal/testutil/reposeed"
)

func TestRelayWorkflowActivitySignalsOnlyActions(t *testing.T) {
	t.Parallel()
	assert := assert.New(t)
	require := require.New(t)
	srv := newTestServer(t)
	t.Cleanup(func() { gracefulShutdown(t, srv) })
	repoID, err := reposeed.Seed(t.Context(), srv.db, db.RepoIdentity{
		Platform: "github", PlatformHost: "github.com", PlatformRepoID: 1001, Owner: "team", Name: "project",
	})
	require.NoError(err)
	srv.broadcastRelayRefresh(t.Context(), repoID, "workflow_runs", 0)
	events, _, stale := srv.Hub().ReplaySnapshotSince(0)
	require.False(stale)
	require.Len(events, 1)
	assert.Equal("workflow_runs_changed", events[0].Event.Type)
	payload, err := jsonv2.Marshal(events[0].Event.Data)
	require.NoError(err)
	assert.JSONEq(`{"provider":"github","platform_host":"github.com","platform_repo_id":1001}`, string(payload))
}
