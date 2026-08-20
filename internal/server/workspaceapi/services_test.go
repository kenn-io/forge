package workspaceapi

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/forge/internal/db"
	ghclient "go.kenn.io/forge/internal/github"
	"go.kenn.io/forge/internal/platform"
	"go.kenn.io/forge/internal/server/httpapi"
	"go.kenn.io/forge/internal/testutil/dbtest"
	"go.kenn.io/forge/internal/workspace"
	"go.kenn.io/forge/internal/workspace/localruntime"
)

func TestCreatePullWorkspaceServiceSuppressesAutoAssign(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	database := dbtest.Open(t)
	ctx := t.Context()
	repoIdentity := db.RepoIdentity{
		Platform: "gitlab", PlatformHost: "git.example.test",
		PlatformRepoID: "repo-acme-widget", Owner: "acme", Name: "widget",
	}
	repoID, err := database.UpsertRepo(ctx, repoIdentity)
	require.NoError(err)
	now := time.Now().UTC().Truncate(time.Second)
	_, err = database.UpsertMergeRequest(ctx, &db.MergeRequest{
		RepoID: repoID, PlatformID: 7000, Number: 7,
		URL:   "https://git.example.test/acme/widget/merge_requests/7",
		Title: "Improve widget", Author: "author", State: db.MergeRequestStateOpen,
		HeadBranch: "feature", BaseBranch: "main",
		CreatedAt: now, UpdatedAt: now, LastActivityAt: now,
	})
	require.NoError(err)
	provider := &autoAssignProvider{pull: platform.MergeRequest{}}
	registry, err := platform.NewRegistry(provider)
	require.NoError(err)
	syncer := ghclient.NewSyncerWithRegistry(registry, database, nil, nil, time.Hour, nil, nil)
	t.Cleanup(syncer.Stop)
	handler := New(Deps{
		DB:       database,
		Resolver: httpapi.NewRepositoryResolver(httpapi.RepositoryResolverDeps{DB: database}),
		Syncer:   syncer, Config: ConfigSnapshot{AutoAssignOnCreate: true},
		Workspaces:         workspace.NewManager(database, t.TempDir()),
		EnrichmentDisabled: true,
	})
	t.Cleanup(func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		require.NoError(handler.Shutdown(shutdownCtx))
	})

	result, err := handler.CreatePullWorkspace(ctx, CreatePullWorkspaceRequest{
		Provider: "gitlab", PlatformHost: "git.example.test",
		Owner: "acme", Name: "widget", Number: 7, SuppressAutoAssign: true,
	})

	require.NoError(err)
	assert.NotEmpty(result.Workspace.ID)
	assert.True(result.Workspace.Created)
	assert.Empty(provider.pullAssigned)
}

func TestLaunchWorkspaceRuntimeServiceReturnsSession(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	database := dbtest.Open(t)
	ctx := t.Context()
	worktree := t.TempDir()
	workspaceID := "ws-runtime-service"
	require.NoError(database.InsertWorkspace(ctx, &db.Workspace{
		ID: workspaceID, Platform: "github", PlatformHost: "github.com",
		RepoOwner: "acme", RepoName: "widget", ItemType: db.WorkspaceItemTypeAdHoc,
		ItemKey: db.AdHocWorkspaceItemKey("work/service"), GitHeadRef: "work/service",
		WorkspaceBranch: "work/service", WorktreePath: worktree,
		TmuxSession: "forge-runtime-service", Status: "ready",
	}))
	owner := newInitialMessagePTYOwner()
	runtime := localruntime.NewManager(localruntime.Options{
		Targets: []localruntime.LaunchTarget{{
			Key: string(localruntime.LaunchTargetPlainShell), Label: "Shell",
			Kind: localruntime.LaunchTargetPlainShell, Source: "system", Available: true,
		}},
		PtyOwnerRuntime: owner,
	})
	t.Cleanup(runtime.Shutdown)
	workspaceManager := workspace.NewManager(database, t.TempDir())
	handler := New(Deps{
		DB: database, Workspaces: workspaceManager,
		Runtime: runtime, EnrichmentDisabled: true,
	})

	session, err := handler.LaunchWorkspaceRuntimeService(
		ctx, workspaceID, string(localruntime.LaunchTargetPlainShell),
	)

	require.NoError(err)
	assert.NotEmpty(session.Key)
	assert.Equal(workspaceID, session.WorkspaceID)
	assert.Equal(string(localruntime.LaunchTargetPlainShell), session.TargetKey)
	stored, err := workspaceManager.RuntimeSessionsForWorkspace(ctx, workspaceID)
	require.NoError(err)
	require.Len(stored, 1)
	assert.Equal(session.Key, stored[0].SessionKey)
}
