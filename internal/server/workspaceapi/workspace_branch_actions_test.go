package workspaceapi

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/forge/internal/db"
	"go.kenn.io/forge/internal/server/httpapi"
	"go.kenn.io/forge/internal/testutil/dbtest"
	"go.kenn.io/forge/internal/workspace"
)

func TestRevealWorkspaceOpensWorkspacePath(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	database := dbtest.Open(t)
	manager := workspace.NewManager(database, t.TempDir())
	path := t.TempDir()
	require.NoError(database.InsertWorkspace(t.Context(), &db.Workspace{
		ID: "ws-reveal", Platform: "github", PlatformHost: "github.com",
		RepoOwner: "acme", RepoName: "widget", ItemType: db.WorkspaceItemTypePullRequest,
		ItemNumber: 1, WorktreePath: path, Status: "ready",
	}))
	h := New(Deps{DB: database, Workspaces: manager})
	previous := revealWorkspacePath
	t.Cleanup(func() { revealWorkspacePath = previous })
	var opened string
	revealWorkspacePath = func(_ context.Context, got string) error {
		opened = got
		return nil
	}

	_, err := h.revealWorkspace(t.Context(), &revealWorkspaceInput{ID: "ws-reveal"})

	require.NoError(err)
	assert.Equal(path, opened)
}

func TestWorkspaceBranchActionRejectsRetiredRepositoryIncarnation(t *testing.T) {
	require := require.New(t)
	database := dbtest.Open(t)
	ctx := t.Context()
	repoID, err := database.UpsertRepoByProviderID(ctx, db.RepoIdentity{
		Platform: "github", PlatformHost: "github.com",
		PlatformRepoID: "R_old", Owner: "acme", Name: "widget",
		RepoPath: "acme/widget",
	})
	require.NoError(err)
	manager := workspace.NewManager(database, t.TempDir())
	require.NoError(database.InsertWorkspace(ctx, &db.Workspace{
		ID: "ws-retired", RepoID: &repoID, Platform: "github",
		PlatformHost: "github.com", RepoOwner: "acme", RepoName: "widget",
		ItemType: db.WorkspaceItemTypePullRequest, ItemNumber: 1,
		WorktreePath: t.TempDir(), Status: "ready",
	}))
	_, err = database.UpsertRepoByProviderID(ctx, db.RepoIdentity{
		Platform: "github", PlatformHost: "github.com",
		PlatformRepoID: "R_new", Owner: "acme", Name: "widget",
		RepoPath: "acme/widget",
	})
	require.NoError(err)
	h := New(Deps{
		DB: database, Workspaces: manager,
		Resolver: httpapi.NewRepositoryResolver(httpapi.RepositoryResolverDeps{DB: database}),
	})
	called := false

	_, err = h.runWorkspaceBranchAction(ctx, "ws-retired", func(
		context.Context, string, string, string, string, string,
	) error {
		called = true
		return nil
	})

	require.Error(err)
	require.False(called)
}
