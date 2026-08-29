package workspaceapi

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/forge/internal/db"
	"go.kenn.io/forge/internal/gitclone"
	"go.kenn.io/forge/internal/providerplane"
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

func TestLaunchSpecBranchActionMapsHubOutage(t *testing.T) {
	err := workspaceBranchActionProblem(&workspace.LaunchSpecRefreshError{
		Cause: providerplane.ErrHubUnavailable,
	})

	problem, ok := err.(*httpapi.ProblemError)
	require.True(t, ok)
	assert.Equal(t, httpapi.CodeHubUnavailable, problem.Code)
}

func TestBranchActionMapsMissingGitCredential(t *testing.T) {
	err := workspaceBranchActionProblem(gitclone.ErrCredentialUnavailable)

	problem, ok := err.(*httpapi.ProblemError)
	require.True(t, ok)
	assert.Equal(t, httpapi.CodeGitCredentialUnavailable, problem.Code)
}
