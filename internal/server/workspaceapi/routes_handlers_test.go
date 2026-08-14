package workspaceapi

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
	"go.kenn.io/forge/internal/db"
	"go.kenn.io/forge/internal/gitclone"
	"go.kenn.io/forge/internal/testutil/dbtest"
	"go.kenn.io/forge/internal/workspace"
)

func TestGetWorkspaceFilesPropagatesCanceledRequest(t *testing.T) {
	require := require.New(t)
	database := dbtest.Open(t)
	manager := workspace.NewManager(database, t.TempDir())
	require.NoError(database.InsertWorkspace(t.Context(), &db.Workspace{
		ID:           "ws-canceled-diff",
		Platform:     "github",
		PlatformHost: "github.com",
		RepoOwner:    "acme",
		RepoName:     "widget",
		ItemType:     db.WorkspaceItemTypePullRequest,
		ItemNumber:   1,
		WorktreePath: t.TempDir(),
		Status:       "ready",
	}))

	requestCtx, cancelRequest := context.WithCancel(t.Context())
	releasePreparation := make(chan struct{})
	cacheCtx, cancelCache := context.WithCancel(context.Background())
	cache := newWorkspaceDiffCache(cacheCtx, workspaceDiffCacheDeps{
		resolve: func(context.Context, workspace.DiffSnapshotSpec) (workspace.ResolvedDiffSnapshotSpec, bool, error) {
			return workspaceDiffTestResolved(), true, nil
		},
		fingerprint: func(context.Context, workspace.ResolvedDiffSnapshotSpec) (workspace.DiffFingerprint, error) {
			return "v1", nil
		},
		prepare: func(ctx context.Context, _ workspace.ResolvedDiffSnapshotSpec) (*gitclone.DiffResult, error) {
			select {
			case <-releasePreparation:
				return workspaceDiffTestResult("one.txt"), nil
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		},
		onColdWait: cancelRequest,
	})
	t.Cleanup(func() {
		close(releasePreparation)
		cancelCache()
		cache.Wait()
	})
	handler := &Handler{workspaces: manager, workspaceDiffCache: cache}

	_, err := handler.getWorkspaceFiles(requestCtx, &getWorkspaceFilesInput{
		ID:   "ws-canceled-diff",
		Base: "head",
	})

	require.ErrorIs(err, context.Canceled)
}
