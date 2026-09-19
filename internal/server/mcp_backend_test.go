package server

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
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
	"go.kenn.io/forge/internal/server/workspaceapi"
	"go.kenn.io/forge/internal/testutil/dbtest"
	"go.kenn.io/forge/internal/workspace"
	"go.kenn.io/forge/internal/workspace/localruntime"
)

func TestDaemonPingPublishesMCPURL(t *testing.T) {
	srv := &Server{
		options:   ServerOptions{MCPURL: "http://127.0.0.1:8092/mcp"},
		buildInfo: BuildInfo{Version: "test"},
	}

	output, err := srv.daemonPing(t.Context(), &struct{}{})

	require.NoError(t, err)
	assert.Equal(t, "http://127.0.0.1:8092/mcp", output.Body.MCPURL)
}

func TestMCPBackendAppliesActivityItemTypesBeforeSafetyWindow(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	srv, database := setupTestServer(t)
	ctx := t.Context()
	pullID := seedPR(t, database, "acme", "widget", 42)
	repo, err := database.GetRepoByIdentity(ctx, verifiedGitHubRepoIdentity("github.com", "acme", "widget"))
	require.NoError(err)
	require.NotNil(repo)
	base := time.Now().UTC().Add(-2 * time.Hour).Truncate(time.Second)
	require.NoError(database.UpsertMREvents(ctx, []db.MREvent{{
		MergeRequestID: pullID, EventType: "issue_comment", Author: "reviewer",
		Body: "review this", CreatedAt: base, DedupeKey: "mcp-item-filter-comment",
	}}))
	commits := make([]db.BranchCommit, activitySafetyCap+1)
	for i := range commits {
		at := base.Add(time.Duration(i+1) * time.Millisecond)
		commits[i] = db.BranchCommit{
			RepoID: repo.ID, BranchName: "main", CommitSHA: fmt.Sprintf("%040x", i+1),
			AuthorName: "maintainer", AuthoredAt: at,
			CommitterName: "maintainer", CommittedAt: at,
			Subject: "repository activity", CreatedAt: at, UpdatedAt: at,
		}
	}
	require.NoError(database.UpsertBranchCommits(ctx, commits))

	page, err := srv.MCPBackend().ListActivity(ctx, mcpserver.ActivityQuery{
		Since: base.Add(-time.Minute).Format(time.RFC3339), ItemTypes: []string{"pr"},
	})

	require.NoError(err)
	require.NotEmpty(page.Items)
	for _, item := range page.Items {
		assert.Equal("pr", item.ItemType)
		assert.Equal(repo.PlatformRepoID, item.Repository.PlatformRepoID)
	}
	assert.False(page.Capped)
}

func TestMCPBackendTranslatesInactivePasteModeToRetryableError(t *testing.T) {
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
	srv := &Server{workspaceAPI: workspaceapi.New(workspaceapi.Deps{
		DB: database, Workspaces: workspace.NewManager(database, t.TempDir()),
		Runtime: runtime,
	})}

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
	assert := assert.New(t)
	require := require.New(t)
	_, database, _, _, srv := setupTestServerWithWorkspacesServer(t, nil)
	ctx := t.Context()
	repo, err := database.GetRepoByIdentity(ctx, verifiedGitHubRepoIdentity("github.com", "acme", "widget"))
	require.NoError(err)
	require.NotNil(repo)
	item := mcpserver.ItemIdentity{
		Type: "pr", Provider: "github", PlatformHost: "github.com",
		PlatformRepoID: repo.PlatformRepoID,
		Owner:          "acme", Name: "widget", Number: 1,
	}

	_, err = srv.MCPBackend().CreatePullWorkspace(ctx, item, true)
	require.NoError(err)
	_, err = srv.MCPBackend().CreatePullWorkspace(ctx, item, true)

	var backendErr *mcpserver.Error
	require.ErrorAs(err, &backendErr)
	assert.Equal("conflict", backendErr.Kind)
	assert.Equal(mcpserver.ErrorCodeWorkspaceAlreadyExists, backendErr.Code)
}

func TestMCPBackendRejectsMismatchedStableRepositoryID(t *testing.T) {
	srv, database := setupTestServer(t)
	seedPR(t, database, "acme", "widget", 42)

	_, err := srv.MCPBackend().GetPull(t.Context(), mcpserver.ItemIdentity{
		Type: "pr", Provider: "github", PlatformHost: "github.com",
		PlatformRepoID: "replacement-repository",
		Owner:          "acme", Name: "widget", Number: 42,
	})

	var backendErr *mcpserver.Error
	require.ErrorAs(t, err, &backendErr)
	assert.Equal(t, "not_found", backendErr.Kind)
	assert.Equal(t, string(httpapi.CodeRepoNotFound), backendErr.Code)
}

func TestMCPBackendPreservesCachedPullReadiness(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	srv, database := setupTestServer(t)
	seedPR(t, database, "acme", "widget", 42, func(pr *db.MergeRequest) {
		pr.MergeableState = "dirty"
		pr.ReviewDecision = "CHANGES_REQUESTED"
		pr.CIStatus = "success"
		pr.PlatformHeadSHA = "head-one"
		pr.CIChecksJSON = `[{"name":"unit","status":"completed","conclusion":"success"}]`
	})
	seedPR(t, database, "acme", "widget", 43, withSeedPRLifecycle("closed", nil, new(time.Now().UTC())))
	repo, err := database.GetRepoByIdentity(t.Context(), verifiedGitHubRepoIdentity("github.com", "acme", "widget"))
	require.NoError(err)
	identity := mcpserver.RepositoryIdentity{
		Provider: "github", PlatformHost: "github.com", PlatformRepoID: repo.PlatformRepoID,
		Owner: "acme", Name: "widget", RepoPath: "acme/widget",
	}
	rows, err := srv.MCPBackend().ListPulls(t.Context(), mcpserver.ItemListQuery{Repository: identity, State: "open", Limit: 26})
	require.NoError(err)
	require.Len(rows, 1)
	assert.Equal(42, rows[0].Number)
	assert.Equal("dirty", rows[0].MergeableState)
	assert.Equal("CHANGES_REQUESTED", rows[0].ReviewDecision)
	assert.Equal("success", rows[0].CIStatus)
	assert.Equal("head-one", rows[0].HeadSHA)
	require.Len(rows[0].Checks, 1)
	assert.Equal("success", rows[0].Checks[0].Conclusion)
	detail, err := srv.MCPBackend().GetPull(t.Context(), mcpserver.ItemIdentity{
		Type: "pr", Provider: "github", PlatformHost: "github.com", PlatformRepoID: repo.PlatformRepoID,
		Owner: "acme", Name: "widget", Number: 42,
	})
	require.NoError(err)
	require.NotNil(detail.Pull)
	assert.Equal(rows[0].MergeableState, detail.Pull.MergeableState)
	assert.Equal(rows[0].ReviewDecision, detail.Pull.ReviewDecision)
	assert.Equal(rows[0].Checks, detail.Checks)
	identity.PlatformRepoID = "replacement-repository"
	_, err = srv.MCPBackend().ListPulls(t.Context(), mcpserver.ItemListQuery{Repository: identity, State: "open"})
	var backendErr *mcpserver.Error
	require.ErrorAs(err, &backendErr)
	assert.Equal("not_found", backendErr.Kind)
}

func TestMCPBackendFiltersPullLabelsBeforePagination(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	srv, database := setupTestServer(t)
	for number, name := range []string{"bug", "debug", "bug"} {
		id := seedPR(t, database, "acme", "widget", number+1)
		repo, err := database.GetRepoByIdentity(t.Context(), verifiedGitHubRepoIdentity("github.com", "acme", "widget"))
		require.NoError(err)
		require.NoError(database.ReplaceMergeRequestLabels(t.Context(), repo.ID, id, []db.Label{{Name: name}}))
	}
	repo, err := database.GetRepoByIdentity(t.Context(), verifiedGitHubRepoIdentity("github.com", "acme", "widget"))
	require.NoError(err)
	identity := mcpserver.RepositoryIdentity{
		Provider: "github", PlatformHost: "github.com", PlatformRepoID: repo.PlatformRepoID,
		Owner: "acme", Name: "widget", RepoPath: "acme/widget",
	}
	var numbers []int
	for offset := range 2 {
		rows, err := srv.MCPBackend().ListPulls(t.Context(), mcpserver.ItemListQuery{
			Repository: identity, State: "open", Label: "bug", Limit: 1, Offset: offset,
		})
		require.NoError(err)
		require.Len(rows, 1)
		assert.Equal([]string{"bug"}, rows[0].Labels)
		numbers = append(numbers, rows[0].Number)
	}
	assert.ElementsMatch([]int{1, 3}, numbers)
	rows, err := srv.MCPBackend().ListPulls(t.Context(), mcpserver.ItemListQuery{Repository: identity, Label: "Bug"})
	require.NoError(err)
	assert.Empty(rows)
	detail, err := srv.MCPBackend().GetPull(t.Context(), mcpserver.ItemIdentity{
		Type: "pr", Provider: "github", PlatformHost: "github.com", PlatformRepoID: repo.PlatformRepoID,
		Owner: "acme", Name: "widget", Number: 1,
	})
	require.NoError(err)
	require.NotNil(detail.Pull)
	assert.Equal([]string{"bug"}, detail.Pull.Labels)
}

func TestMCPBackendListsPullsWithMalformedCachedChecks(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	srv, database := setupTestServer(t)
	seedPR(t, database, "acme", "widget", 42, func(pr *db.MergeRequest) {
		pr.CIChecksJSON = `[{"name":"unit","conclusion":"success"}]`
	})
	seedPR(t, database, "acme", "widget", 43, func(pr *db.MergeRequest) {
		pr.MergeableState = "dirty"
		pr.CIChecksJSON = `[{"name":"partial","conclusion":"success"},{"name":42}]`
	})
	repo, err := database.GetRepoByIdentity(t.Context(), verifiedGitHubRepoIdentity("github.com", "acme", "widget"))
	require.NoError(err)
	rows, err := srv.MCPBackend().ListPulls(t.Context(), mcpserver.ItemListQuery{
		Repository: mcpserver.RepositoryIdentity{
			Provider: "github", PlatformHost: "github.com", PlatformRepoID: repo.PlatformRepoID,
			Owner: "acme", Name: "widget", RepoPath: "acme/widget",
		}, State: "open", Limit: 25,
	})
	require.NoError(err)
	require.Len(rows, 2)
	byNumber := make(map[int]mcpserver.Pull)
	for _, row := range rows {
		byNumber[row.Number] = row
	}
	assert.Contains(byNumber, 42)
	assert.Contains(byNumber, 43)
	require.Len(byNumber[42].Checks, 1)
	assert.Equal("success", byNumber[42].Checks[0].Conclusion)
	assert.Empty(byNumber[43].Checks)
	assert.Equal("dirty", byNumber[43].MergeableState)
}

func TestMCPWorkspaceRepositoryFenceReconcilesHubIdentity(t *testing.T) {
	require := require.New(t)
	database := dbtest.Open(t)
	observedAt := time.Date(2026, time.August, 24, 12, 0, 0, 0, time.UTC)
	descriptor := providerplane.RepositoryDescriptor{
		ProtocolVersion: federation.ProtocolVersion,
		Provider:        "github", PlatformHost: "github.com", PlatformRepoID: "repo-new",
		Owner: "acme", Name: "new-repo", CloneURL: "https://github.com/acme/new-repo.git",
		DefaultBranch: "main", SnapshotRevision: 1, ObservedAt: observedAt,
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
	t.Cleanup(func() { gracefulShutdown(t, srv) })
	srv.providerSource = &hubProviderSource{client: client, db: database}
	backend := mcpBackend{server: srv}
	identity := mcpserver.RepositoryIdentity{
		Provider: "github", PlatformHost: "github.com", PlatformRepoID: "repo-new",
		Owner: "acme", Name: "new-repo",
	}

	resolved, err := backend.resolveWorkspaceRepositoryFence(t.Context(), identity)

	require.NoError(err)
	require.NotNil(resolved.repo)
	assert.Equal(t, "repo-new", resolved.repo.PlatformRepoID)
	assert.False(t, resolved.hub)
	observed, err := database.GetRepositoryByProviderID(
		t.Context(), "github", "github.com", "repo-new",
	)
	require.NoError(err)
	require.NotNil(observed)
}

func TestMCPBackendReadFailsClosedWhenRouteReassignedMidRead(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	srv, database := setupTestServer(t)
	ctx := t.Context()
	seedPR(t, database, "acme", "widget", 42)
	repo, err := database.GetRepoByIdentity(ctx, verifiedGitHubRepoIdentity("github.com", "acme", "widget"))
	require.NoError(err)
	require.NotNil(repo)
	backend := mcpBackend{server: srv}

	resolved, err := backend.resolveRepositoryFence(ctx, mcpserver.RepositoryIdentity{
		Provider: "github", PlatformHost: "github.com",
		PlatformRepoID: repo.PlatformRepoID,
		Owner:          "acme", Name: "widget",
	})
	require.NoError(err)

	// Reassign route ownership between stable-identity validation and the
	// route-addressed read, the window the fence exists to police.
	_, accepted, err := database.ReconcileRepositoryObservation(ctx, db.RepoIdentity{
		Platform: "github", PlatformHost: "github.com",
		PlatformRepoID: "replacement-repository",
		Owner:          "acme", Name: "widget", RepoPath: "acme/widget",
	}, time.Now().UTC())
	require.NoError(err)
	require.True(accepted)

	err = backend.confirmRepositoryRoute(ctx, resolved)

	var backendErr *mcpserver.Error
	require.ErrorAs(err, &backendErr)
	assert.Equal("not_found", backendErr.Kind)
	assert.Equal(string(httpapi.CodeRepoNotFound), backendErr.Code)
}

func TestMCPBackendWorkflowDoesNotExposeOrMutateRemovedUpstreamItems(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	srv, database := setupTestServer(t)
	ctx := t.Context()
	seedPR(t, database, "acme", "widget", 1)
	seedPR(t, database, "acme", "widget", 2)
	seedIssue(t, database, "acme", "widget", 3, "open")
	repo, err := database.GetRepoByIdentity(ctx, verifiedGitHubRepoIdentity("github.com", "acme", "widget"))
	require.NoError(err)
	require.NotNil(repo)
	markArchiveItemRemovedUpstreamForServerTest(
		t, database, repo.ID, db.ArchiveItemTypeMergeRequest, 1,
	)
	markArchiveItemRemovedUpstreamForServerTest(
		t, database, repo.ID, db.ArchiveItemTypeIssue, 3,
	)
	backend := srv.MCPBackend()
	repository := mcpserver.RepositoryIdentity{
		Provider: "github", PlatformHost: "github.com",
		PlatformRepoID: repo.PlatformRepoID,
		RepoPath:       "acme/widget", Owner: "acme", Name: "widget",
	}

	page, err := backend.ListWorkflowStates(ctx, mcpserver.WorkflowQuery{
		Repository: repository, IncludeClosed: true,
	})

	require.NoError(err)
	require.Len(page.Items, 1)
	assert.Equal(2, page.Items[0].Identity.Number)
	assert.Equal(repo.PlatformRepoID, page.Items[0].Identity.PlatformRepoID)

	_, err = backend.SetWorkflowState(ctx, mcpserver.ItemIdentity{
		Type: "pr", Provider: "github", PlatformHost: "github.com",
		PlatformRepoID: repo.PlatformRepoID,
		Owner:          "acme", Name: "widget", Number: 1,
	}, mcpserver.WorkflowUpdate{
		Status: "reviewing", ExpectedStatus: "new", Source: "mcp",
	})
	var backendErr *mcpserver.Error
	require.ErrorAs(err, &backendErr)
	assert.Equal("not_found", backendErr.Kind)
	assert.Equal(string(httpapi.CodePullNotFound), backendErr.Code)

	stored, err := database.GetItemWorkflowState(ctx, repo.ID, db.ItemTypePR, 1)
	require.NoError(err)
	require.NotNil(stored)
	assert.Equal("new", stored.Status)
}

func TestSpokePreparationBlocksMCPWorkflowMutation(t *testing.T) {
	require := require.New(t)
	srv, database := setupTestServer(t)
	seedPR(t, database, "acme", "widget", 7)
	repo, err := database.GetRepoByIdentity(
		t.Context(), verifiedGitHubRepoIdentity("github.com", "acme", "widget"),
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
		PlatformRepoID: repo.PlatformRepoID, Owner: "acme", Name: "widget", Number: 7,
	}, mcpserver.WorkflowUpdate{Status: "reviewing", ExpectedStatus: "new"})
	var backendErr *mcpserver.Error
	require.ErrorAs(err, &backendErr)
	assert.Equal(t, string(httpapi.CodeSpokePreparationInProgress), backendErr.Code)
}
