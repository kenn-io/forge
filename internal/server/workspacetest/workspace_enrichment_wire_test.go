package workspacetest

import (
	"context"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/forge/internal/apiclient/generated"
	"go.kenn.io/forge/internal/server"
	"go.kenn.io/forge/internal/server/workspaceapi"
)

func TestWorkspaceListReportsCommitsAheadBehindE2E(t *testing.T) {
	t.Parallel()
	acquireWorkspaceGitSlot(t)

	require := require.New(t)
	assert := assert.New(t)
	now := time.Date(2026, 7, 14, 12, 0, 0, 0, time.UTC)
	var clockNow atomic.Int64
	clockNow.Store(now.UnixNano())
	fixture := setupWorkspaceServerFixture(t, nil, server.ServerOptions{
		DisableWorkspaceBackgroundMonitors: true,
		PtyOwnerInProcess:                  true,
		WorkspaceNow: func() time.Time {
			return time.Unix(0, clockNow.Load()).UTC()
		},
	})
	ws := createReadyWorkspace(t, context.Background(), fixture.client)
	workspaceByID := func() *generated.WorkspaceResponse {
		resp, err := fixture.client.HTTP.ListWorkspacesWithResponse(t.Context())
		if err != nil || resp.JSON200 == nil || resp.JSON200.Workspaces == nil {
			return nil
		}
		for i := range *resp.JSON200.Workspaces {
			candidate := &(*resp.JSON200.Workspaces)[i]
			if candidate.Id == ws.Id {
				return candidate
			}
		}
		return nil
	}
	require.Eventually(func() bool {
		initial := workspaceByID()
		return initial != nil && initial.CommitsAhead != nil && initial.CommitsBehind != nil &&
			*initial.CommitsAhead == 0 && *initial.CommitsBehind == 0
	}, 10*time.Second, 10*time.Millisecond)

	runGit(t, ws.WorktreePath, "config", "user.email", "test@test.com")
	runGit(t, ws.WorktreePath, "config", "user.name", "Test")
	for _, name := range []string{"ahead-1.txt", "ahead-2.txt"} {
		require.NoError(os.WriteFile(filepath.Join(ws.WorktreePath, name), []byte(name+"\n"), 0o644))
		runGit(t, ws.WorktreePath, "add", ".")
		runGit(t, ws.WorktreePath, "commit", "-m", name)
	}
	clockNow.Store(now.Add(workspaceapi.EnrichmentTTL + time.Second).UnixNano())

	var found *generated.WorkspaceResponse
	require.Eventually(func() bool {
		found = workspaceByID()
		return found != nil && found.CommitsAhead != nil && found.CommitsBehind != nil &&
			*found.CommitsAhead == 2 && *found.CommitsBehind == 0
	}, 10*time.Second, 10*time.Millisecond)
	require.NotNil(found)
	assert.Equal(int64(2), *found.CommitsAhead)
	assert.Equal(int64(0), *found.CommitsBehind)
}
