package server

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"go.kenn.io/forge/internal/db"
	"go.kenn.io/forge/internal/federation"
	"go.kenn.io/forge/internal/federationauth"
	"go.kenn.io/forge/internal/mcpserver"
	"go.kenn.io/forge/internal/providerplane"
	"go.kenn.io/forge/internal/server/httpapi"
	"go.kenn.io/forge/internal/server/spokeapi"
	"go.kenn.io/forge/internal/server/workspaceapi"
	"go.kenn.io/forge/internal/testutil"
	"go.kenn.io/forge/internal/testutil/dbtest"
	serverfake "go.kenn.io/forge/internal/testutil/serverfake"
	"go.kenn.io/forge/internal/workspace/localruntime"
	"go.kenn.io/forge/platform"
)

func TestDaemonPingPublishesMCPURL(t *testing.T) {
	serverfake.RunParallelServerTest(t)
	srv := wiredServer(&Server{
		options:   ServerOptions{MCPURL: "http://127.0.0.1:8092/mcp"},
		buildInfo: BuildInfo{Version: "test"},
	})

	output, err := srv.daemonPing(t.Context(), &struct{}{})

	require.NoError(t, err)
	assert.Equal(t, "http://127.0.0.1:8092/mcp", output.Body.MCPURL)
}

func TestMCPBackendTranslatesInactivePasteModeToRetryableError(t *testing.T) {
	serverfake.RunParallelServerTest(t)
	assert := assert.New(t)
	require := require.New(t)
	ctx := t.Context()
	database := dbtest.Open(t)
	worktree := t.TempDir()
	workspaceID := "ws-mcp-initial-message"
	require.NoError(database.InsertWorkspace(ctx, &db.Workspace{
		ID: workspaceID, Platform: "github", PlatformHost: "github.com",
		RepoOwner: "acme", RepoName: "widgets",
		ItemType: db.WorkspaceItemTypePullRequest, ItemNumber: 42,
		GitHeadRef: "feature/message", WorkspaceBranch: "feature/message",
		WorktreePath: worktree, TmuxSession: "forge-mcp-initial-message", Status: "ready",
	}))

	// The fake PTY owner never emits the bracketed-paste enable sequence, so
	// the real runtime manager rejects the write and the real workspace
	// service raises its input-mode-not-ready signal across this boundary.
	owner := &fakeRuntimeOwner{}
	runtime := localruntime.NewManager(localruntime.Options{
		Targets: []localruntime.LaunchTarget{{
			Key: "codex", Label: "Codex", Kind: localruntime.LaunchTargetAgent,
			Source: "test", Command: []string{"unused"}, Available: true,
		}},
		PtyOwnerRuntime: owner,
	})
	t.Cleanup(runtime.Shutdown)
	session, err := runtime.Launch(ctx, workspaceID, worktree, "codex")
	require.NoError(err)
	srv := wiredServer(&Server{workspaceAPI: workspaceapi.New(workspaceapi.Deps{
		DB: database, Workspaces: newWorkspaceTestManager(t, database, t.TempDir()),
		Runtime: runtime,
	})})

	_, err = srv.MCPBackend().SubmitInitialMessage(ctx, mcpserver.InitialMessageRequest{
		WorkspaceID: workspaceID, RuntimeSessionKey: session.Key,
		TargetKey: "codex", Message: "first\nsecond",
	})

	var backendErr *mcpserver.Error
	require.ErrorAs(err, &backendErr)
	assert.Equal(mcpserver.ErrorCodeInitialMessageInputModeNotReady, backendErr.Code)
	assert.Equal("unavailable", backendErr.Kind)
	assert.True(backendErr.Retryable)
	assert.False(backendErr.Ambiguous)
}

func TestMCPPullWorkspaceDuplicateUsesStableConflictCode(t *testing.T) {
	serverfake.RunParallelServerTest(t)
	assert := assert.New(t)
	require := require.New(t)
	_, database, _, _, srv := setupTestServerWithWorkspacesServer(t, nil)
	ctx := t.Context()
	repo, err := database.GetRepoByIdentity(ctx, serverfake.VerifiedGitHubRepoIdentity("github.com", "acme", "widget"))
	require.NoError(err)
	require.NotNil(repo)
	item := mcpserver.ItemIdentity{
		Type: "pr", Provider: "github", PlatformHost: "github.com",
		RepoKey: repo.Key,
		Owner:   "acme", Name: "widget", Number: 1,
	}

	_, err = srv.MCPBackend().CreatePullWorkspace(ctx, item, true)
	require.NoError(err)
	_, err = srv.MCPBackend().CreatePullWorkspace(ctx, item, true)

	var backendErr *mcpserver.Error
	require.ErrorAs(err, &backendErr)
	assert.Equal("conflict", backendErr.Kind)
	assert.Equal(mcpserver.ErrorCodeWorkspaceAlreadyExists, backendErr.Code)
}

type recordingMCPLaunchResolver struct {
	*Server
	requests []providerplane.WorkspaceLaunchRequest
}

func (r *recordingMCPLaunchResolver) ResolveWorkspaceLaunchSpec(
	ctx context.Context, request providerplane.WorkspaceLaunchRequest,
) (db.WorkspaceLaunchSpec, error) {
	r.requests = append(r.requests, request)
	return r.Server.ResolveWorkspaceLaunchSpec(ctx, request)
}

func TestMCPWorkspaceReusePreservesRepositoryIdentity(t *testing.T) {
	serverfake.RunParallelServerTest(t)
	for _, itemType := range []string{db.WorkspaceItemTypePullRequest, db.WorkspaceItemTypeIssue} {
		t.Run(itemType, func(t *testing.T) {
			require := require.New(t)
			assert := assert.New(t)
			database := dbtest.Open(t)
			serverfake.SeedPR(t, database, "acme", "widget", 42)
			serverfake.SeedIssue(t, database, "acme", "widget", 42, "open")
			serverfake.SeedWorkspace(t, database, "ws-existing", "acme", "widget", itemType, 42)
			resolver := httpapi.NewRepositoryResolver(httpapi.RepositoryResolverDeps{DB: database})
			srv := &Server{db: database, repoResolver: resolver, now: time.Now}
			spec, err := srv.ResolveWorkspaceLaunchSpec(t.Context(), providerplane.WorkspaceLaunchRequest{
				Repository: providerplane.RepositoryRoute{
					Provider: "github", PlatformHost: "github.com", Owner: "acme", Name: "widget",
				},
				RepoKey: platform.RepositoryIDKey(testutil.FixtureRepoID("acme", "widget")), ItemType: itemType, ItemNumber: 42,
				GitHeadRef: "feature/ws-existing",
			})
			require.NoError(err)
			require.NoError(database.PutWorkspaceLaunchSpec(t.Context(), "ws-existing", spec))
			launchResolver := &recordingMCPLaunchResolver{Server: srv}
			srv.workspaceAPI = workspaceapi.New(workspaceapi.Deps{
				DB: database, Resolver: resolver, Workspaces: newWorkspaceTestManager(t, database, t.TempDir()),
				LaunchSpecResolver: launchResolver, EnrichmentDisabled: true,
			})
			t.Cleanup(func() {
				require.NoError(srv.workspaceAPI.Shutdown(context.WithoutCancel(t.Context())))
			})
			item := mcpserver.ItemIdentity{
				Provider: "github", PlatformHost: "github.com", RepoKey: platform.RepositoryIDKey(testutil.FixtureRepoID("acme", "widget")),
				Owner: "acme", Name: "widget", Number: 42,
			}
			if itemType == db.WorkspaceItemTypePullRequest {
				_, err := srv.MCPBackend().CreatePullWorkspace(t.Context(), item, true)
				var backendErr *mcpserver.Error
				require.ErrorAs(err, &backendErr)
				assert.Equal(mcpserver.ErrorCodeWorkspaceAlreadyExists, backendErr.Code)
			} else {
				result, err := srv.MCPBackend().CreateIssueWorkspace(t.Context(), item, true)
				require.NoError(err)
				assert.Equal("ws-existing", result.ID)
			}
			// Reuse must pass through admission with the identity MCP validated,
			// even when an existing workspace means no new row is written.
			require.Len(launchResolver.requests, 1)
			assert.Equal(platform.RepositoryIDKey(testutil.FixtureRepoID("acme", "widget")), launchResolver.requests[0].RepoKey)
		})
	}
}

func TestMCPAdHocWorkspaceRejectsRouteReplacementBeforeReuse(t *testing.T) {
	serverfake.RunParallelServerTest(t)
	require := require.New(t)
	assert := assert.New(t)
	ctx := t.Context()
	database := dbtest.Open(t)
	identity := serverfake.VerifiedGitHubRepoIdentity("github.com", "acme", "widget")
	_, err := database.ObserveRepository(ctx, identity)
	require.NoError(err)
	resolver := httpapi.NewRepositoryResolver(httpapi.RepositoryResolverDeps{DB: database})
	srv := &Server{db: database, repoResolver: resolver}
	srv.workspaceAPI = workspaceapi.New(workspaceapi.Deps{
		DB: database, Resolver: resolver, Workspaces: newWorkspaceTestManager(t, database, t.TempDir()),
		EnrichmentDisabled: true,
		ResolveRepository: func(requestCtx context.Context, route providerplane.RepositoryRoute, repoKey platform.RepositoryKey) (*db.Repo, error) {
			// Provider sync can reassign the route after MCP validates it.
			identity.Key = platform.RepositoryIDKey(1002)
			replacement, observeErr := database.ObserveRepository(ctx, identity)
			require.NoError(observeErr)
			require.NoError(database.InsertWorkspace(ctx, &db.Workspace{
				ID: "ws-replacement", RepoID: replacement.Repository.ID,
				Platform: "github", PlatformHost: "github.com", RepoOwner: "acme", RepoName: "widget",
				ItemType: db.WorkspaceItemTypeAdHoc, ItemKey: "adhoc:feature/work",
				GitHeadRef: "feature/work", WorkspaceBranch: "feature/work",
				WorktreePath: t.TempDir(), Status: "ready",
			}))
			repo, lookupErr := resolver.LookupSelection(requestCtx, route.Provider, route.PlatformHost, route.Owner, route.Name, repoKey)
			if lookupErr != nil {
				return nil, httpapi.ProviderRouteLookupError(lookupErr)
			}
			return repo.Row(), nil
		},
	})
	t.Cleanup(func() {
		require.NoError(srv.workspaceAPI.Shutdown(context.WithoutCancel(ctx)))
	})

	result, err := srv.MCPBackend().CreateAdHocWorkspace(ctx, mcpserver.RepositoryIdentity{
		Provider: "github", PlatformHost: "github.com", Key: platform.RepositoryIDKey(testutil.FixtureRepoID("acme", "widget")),
		Owner: "acme", Name: "widget",
	}, "feature/work")

	var backendErr *mcpserver.Error
	require.ErrorAs(err, &backendErr)
	assert.Equal(string(httpapi.CodeRepoNotFound), backendErr.Code)
	assert.Empty(result.ID)
}

func TestMCPWorkspaceRepositoryRejectsHubDescriptorWithAnotherID(t *testing.T) {
	serverfake.RunParallelServerTest(t)
	require := require.New(t)
	database := dbtest.Open(t)
	descriptor := providerplane.RepositoryDescriptor{
		ProtocolVersion: federation.ProtocolVersion,
		Provider:        "github", PlatformHost: "github.com", Key: platform.RepositoryIDKey(1002),
		Owner: "acme", Name: "widget", CloneURL: "https://github.com/acme/widget.git",
		DefaultBranch: "main", ObservedAt: time.Date(2026, time.August, 24, 12, 0, 0, 0, time.UTC),
	}
	encoded, err := json.Marshal(descriptor)
	require.NoError(err)
	client := providerPlaneClientFunc(func(
		_ context.Context, scope federationauth.Scope, request *http.Request,
	) (*http.Response, error) {
		require.Equal(federationauth.ScopeProviderRead, scope)
		require.Equal("/api/v1/federation/provider/repository-descriptor", request.URL.Path)
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body:       io.NopCloser(bytes.NewReader(encoded)),
			Request:    request,
		}, nil
	})
	srv := New(database, nil, nil, "/", nil, ServerOptions{
		DisableWorkspaceBackgroundMonitors: true,
	})
	t.Cleanup(func() { serverfake.GracefulShutdown(t, srv) })
	srv.providerSource = &spokeapi.HubProviderSource{Client: client, Db: database}
	backend := mcpBackend{server: srv}

	_, err = backend.resolveWorkspaceRepository(t.Context(), mcpserver.RepositoryIdentity{
		Provider: "github", PlatformHost: "github.com", Key: platform.RepositoryIDKey(1001),
		Owner: "acme", Name: "widget",
	})

	var backendErr *mcpserver.Error
	require.ErrorAs(err, &backendErr)
	assert.Equal(t, "not_found", backendErr.Kind)
	assert.Equal(t, string(httpapi.CodeRepoNotFound), backendErr.Code)
}

func TestMCPBackendResolvesRepositoryByProviderIDAcrossRename(t *testing.T) {
	serverfake.RunParallelServerTest(t)
	assert := assert.New(t)
	require := require.New(t)
	srv, database, _ := setupTestServer(t)
	ctx := t.Context()
	serverfake.SeedPR(t, database, "acme", "widget", 42, serverfake.WithSeedPRTitle("renamed repository pull"))
	repo, err := database.GetRepoByIdentity(ctx, serverfake.VerifiedGitHubRepoIdentity("github.com", "acme", "widget"))
	require.NoError(err)
	require.NotNil(repo)
	renamed := db.GitHubRepoIdentity("github.com", "acme", "gadget")
	renamed.Key = repo.Key
	_, err = database.ObserveRepository(ctx, renamed)
	require.NoError(err)
	backend := srv.MCPBackend()

	// The request still names the old route; the provider ID decides which
	// repository is read, and the read follows it to its current route.
	detail, err := backend.GetPull(ctx, mcpserver.ItemIdentity{
		Type: "pr", Provider: "github", PlatformHost: "github.com",
		RepoKey: repo.Key, Owner: "acme", Name: "widget", Number: 42,
	})
	require.NoError(err)
	require.NotNil(detail.Pull)
	assert.Equal("renamed repository pull", detail.Pull.Title)
	assert.Equal("gadget", detail.Pull.Repository.Name)
	assert.Equal(repo.Key, detail.Pull.Repository.Key)

	repoID, ok := repo.Key.ID()
	require.True(ok)
	_, err = backend.GetPull(ctx, mcpserver.ItemIdentity{
		Type: "pr", Provider: "github", PlatformHost: "github.com",
		RepoKey: platform.RepositoryIDKey(repoID + 1), Owner: "acme", Name: "gadget", Number: 42,
	})
	var backendErr *mcpserver.Error
	require.ErrorAs(err, &backendErr)
	assert.Equal("not_found", backendErr.Kind)
	assert.Equal(string(httpapi.CodeRepoNotFound), backendErr.Code)
}

func TestMCPBackendRejectsIDWhoseRouteWasReusedByAnotherRepository(t *testing.T) {
	serverfake.RunParallelServerTest(t)
	assert := assert.New(t)
	require := require.New(t)
	srv, database, _ := setupTestServer(t)
	ctx := t.Context()
	serverfake.SeedPR(t, database, "acme", "widget", 42)
	repo, err := database.GetRepoByIdentity(ctx, serverfake.VerifiedGitHubRepoIdentity("github.com", "acme", "widget"))
	require.NoError(err)
	require.NotNil(repo)
	_, err = database.ObserveRepository(ctx, db.RepoIdentity{
		Platform: "github", PlatformHost: "github.com", Key: platform.RepositoryIDKey(1002),
		Owner: "acme", Name: "widget", RepoPath: "acme/widget",
	})
	require.NoError(err)

	_, err = srv.MCPBackend().GetPull(ctx, mcpserver.ItemIdentity{
		Type: "pr", Provider: "github", PlatformHost: "github.com",
		RepoKey: repo.Key, Owner: "acme", Name: "widget", Number: 42,
	})

	var backendErr *mcpserver.Error
	require.ErrorAs(err, &backendErr)
	assert.Equal("not_found", backendErr.Kind)
	assert.Equal(string(httpapi.CodeRepoNotFound), backendErr.Code)
}

func TestSpokePreparationBlocksMCPWorkflowMutation(t *testing.T) {
	serverfake.RunParallelServerTest(t)
	require := require.New(t)
	srv, database, _ := setupTestServer(t)
	serverfake.SeedPR(t, database, "acme", "widget", 7)
	repo, err := database.GetRepoByIdentity(
		t.Context(), serverfake.VerifiedGitHubRepoIdentity("github.com", "acme", "widget"),
	)
	require.NoError(err)
	require.NotNil(repo)
	_, err = srv.providerWriteGate.BeginQuiesce(t.Context(), db.SpokePreparationBinding{
		EnrollmentID: "enrollment-1", HubNodeID: "hub-1",
		LocalNodeID: "spoke-1", ProtocolVersion: 3,
	})
	require.NoError(err)

	_, err = srv.MCPBackend().SetWorkflowState(t.Context(), mcpserver.ItemIdentity{
		Type: "pr", Provider: "github", PlatformHost: "github.com",
		RepoKey: repo.Key, Owner: "acme", Name: "widget", Number: 7,
	}, mcpserver.WorkflowUpdate{Status: "reviewing", ExpectedStatus: "new"})
	var backendErr *mcpserver.Error
	require.ErrorAs(err, &backendErr)
	assert.Equal(t, string(httpapi.CodeSpokePreparationInProgress), backendErr.Code)
}
