package workspaceapi

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"go.kenn.io/forge/internal/db"
	"go.kenn.io/forge/internal/gitclone"
	"go.kenn.io/forge/internal/testutil/dbtest"
	"go.kenn.io/forge/internal/testutil/reposeed"
)

func TestWorkspaceSetupLeavesCallerWorkspaceUntouched(t *testing.T) {
	t.Parallel()
	require := require.New(t)
	root := t.TempDir()
	database := dbtest.Open(t)
	repoID, err := reposeed.Seed(t.Context(), database, db.GitHubRepoIdentity("github.example.test", "acme", "widget"))
	require.NoError(err)
	require.NoError(database.UpdateRepoProviderObservation(t.Context(), repoID, db.RepoProviderMetadata{
		CloneURL: filepath.Join(root, "missing", "widget.git"), DefaultBranch: "main",
	}, nil, nil))
	manager := newWorkspaceTestManager(t, database, filepath.Join(root, "worktrees"))
	manager.SetClones(gitclone.New(filepath.Join(root, "clones"), nil))
	parent, cancelParent := context.WithCancel(t.Context())
	handler := New(Deps{DB: database, Workspaces: manager, EnrichmentDisabled: true})
	handler.Start(parent, false)
	ws := &db.Workspace{
		ID: "ws-setup-copy", Platform: "github", PlatformHost: "github.example.test",
		RepoID: repoID, RepoOwner: "acme", RepoName: "widget",
		ItemType: db.WorkspaceItemTypeAdHoc, ItemKey: "adhoc:feature", GitHeadRef: "feature",
		WorktreePath: filepath.Join(root, "worktrees", "feature"), Status: "creating",
	}
	require.NoError(database.InsertWorkspace(t.Context(), ws))
	// A caller-only value: setup reloading into the caller's struct would erase it.
	ws.GitHeadRef = "caller-only-ref"
	before := *ws
	handler.runWorkspaceSetup(ws)
	require.Eventually(func() bool {
		stored, getErr := database.GetWorkspace(t.Context(), "ws-setup-copy")
		return getErr == nil && stored != nil && stored.Status != "creating"
	}, 10*time.Second, 20*time.Millisecond)

	cancelParent()
	shutdownCtx, cancelShutdown := context.WithTimeout(context.WithoutCancel(t.Context()), 5*time.Second)
	defer cancelShutdown()
	require.NoError(handler.Shutdown(shutdownCtx))
	require.Equal(before, *ws)
}
