package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	Assert "github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/middleman/internal/workspace"
)

func TestWorkspacePushBranchRoutePushesAheadBranch(t *testing.T) {
	require := require.New(t)
	assert := Assert.New(t)
	client, _, _, _, srv := setupTestServerWithWorkspacesServer(t, nil)
	ctx := context.Background()
	ws := createReadyWorkspace(t, ctx, client)
	runGit(t, ws.WorktreePath, "config", "user.email", "test@test.com")
	runGit(t, ws.WorktreePath, "config", "user.name", "Test")
	require.NoError(os.WriteFile(
		filepath.Join(ws.WorktreePath, "ahead.txt"), []byte("ahead\n"), 0o644,
	))
	runGit(t, ws.WorktreePath, "add", ".")
	runGit(t, ws.WorktreePath, "commit", "-m", "ahead")

	rr := doJSON(t, srv, http.MethodPost, "/api/v1/workspaces/"+ws.Id+"/push", nil)

	require.Equal(http.StatusOK, rr.Code, rr.Body.String())
	div, ok, err := workspace.WorktreeDivergence(ctx, ws.WorktreePath)
	require.NoError(err)
	require.True(ok)
	assert.Equal(workspace.Divergence{}, div)
}

func TestWorkspacePullBranchRouteFastForwardsBehindBranch(t *testing.T) {
	require := require.New(t)
	assert := Assert.New(t)
	client, _, _, _, srv := setupTestServerWithWorkspacesServer(t, nil)
	ctx := context.Background()
	ws := createReadyWorkspace(t, ctx, client)
	err := srv.workspaces.PushWorktreeBranch(ctx, ws.PlatformHost, ws.WorktreePath)
	if err != nil && !errors.Is(err, workspace.ErrWorktreeInSync) {
		require.NoError(err)
	}
	div, ok, err := workspace.WorktreeDivergence(ctx, ws.WorktreePath)
	require.NoError(err)
	require.True(ok)
	require.Equal(workspace.Divergence{}, div)
	upstreamRef := gitOutput(
		t, ws.WorktreePath,
		"rev-parse", "--abbrev-ref", "--symbolic-full-name", "@{upstream}",
	)
	upstreamBranch := strings.TrimPrefix(upstreamRef, "origin/")

	other := filepath.Join(t.TempDir(), "other")
	originURL := gitOutput(t, ws.WorktreePath, "remote", "get-url", "origin")
	runGit(t, t.TempDir(), "clone", originURL, other)
	runGit(t, other, "config", "user.email", "test@test.com")
	runGit(t, other, "config", "user.name", "Test")
	runGit(t, other, "checkout", "-b", upstreamBranch, upstreamRef)
	require.NoError(os.WriteFile(
		filepath.Join(other, "remote.txt"), []byte("remote\n"), 0o644,
	))
	runGit(t, other, "add", ".")
	runGit(t, other, "commit", "-m", "remote")
	runGit(t, other, "push", "origin", upstreamBranch)

	rr := doJSON(t, srv, http.MethodPost, "/api/v1/workspaces/"+ws.Id+"/pull", nil)

	require.Equal(http.StatusOK, rr.Code, rr.Body.String())
	contents, err := os.ReadFile(filepath.Join(ws.WorktreePath, "remote.txt"))
	require.NoError(err)
	assert.Equal("remote\n", string(contents))
}

func TestWorkspacePullBranchRouteRejectsDirtyWorktree(t *testing.T) {
	require := require.New(t)
	client, _, _, _, srv := setupTestServerWithWorkspacesServer(t, nil)
	ctx := context.Background()
	ws := createReadyWorkspace(t, ctx, client)
	require.NoError(os.WriteFile(
		filepath.Join(ws.WorktreePath, "dirty.txt"), []byte("dirty\n"), 0o644,
	))

	rr := doJSON(t, srv, http.MethodPost, "/api/v1/workspaces/"+ws.Id+"/pull", nil)

	require.Equal(http.StatusConflict, rr.Code, rr.Body.String())
	var problem rawProblemDetail
	require.NoError(json.NewDecoder(rr.Body).Decode(&problem))
	Assert.New(t).Equal(string(CodeWorktreeDirty), problem.Code)
}

func TestWorkspaceRevealRouteOpensWorkspacePath(t *testing.T) {
	require := require.New(t)
	assert := Assert.New(t)
	client, _, _, _, srv := setupTestServerWithWorkspacesServer(t, nil)
	ctx := context.Background()
	ws := createReadyWorkspace(t, ctx, client)
	previous := revealWorkspacePath
	var opened string
	revealWorkspacePath = func(_ context.Context, path string) error {
		opened = path
		return nil
	}
	t.Cleanup(func() {
		revealWorkspacePath = previous
	})

	rr := doJSON(t, srv, http.MethodPost, "/api/v1/workspaces/"+ws.Id+"/reveal", nil)

	require.Equal(http.StatusNoContent, rr.Code, rr.Body.String())
	assert.Equal(ws.WorktreePath, opened)
}
