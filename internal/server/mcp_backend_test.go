package server

import (
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/forge/internal/db"
	"go.kenn.io/forge/internal/mcpserver"
	"go.kenn.io/forge/internal/server/httpapi"
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
	srv, database := setupTestServer(t)
	ctx := t.Context()
	pullID := seedPR(t, database, "acme", "widget", 42)
	repo, err := database.GetRepoByIdentity(ctx, verifiedGitHubRepoIdentity("github.com", "acme", "widget"))
	require.NoError(t, err)
	require.NotNil(t, repo)
	base := time.Now().UTC().Add(-2 * time.Hour).Truncate(time.Second)
	require.NoError(t, database.UpsertMREvents(ctx, []db.MREvent{{
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
	require.NoError(t, database.UpsertBranchCommits(ctx, commits))

	page, err := srv.MCPBackend().ListActivity(ctx, mcpserver.ActivityQuery{
		Since: base.Add(-time.Minute).Format(time.RFC3339), ItemTypes: []string{"pr"},
	})

	require.NoError(t, err)
	require.NotEmpty(t, page.Items)
	for _, item := range page.Items {
		assert.Equal(t, "pr", item.ItemType)
		assert.Equal(t, repo.PlatformRepoID, item.Repository.PlatformRepoID)
	}
	assert.False(t, page.Capped)
}

func TestMCPPullWorkspaceDuplicateUsesStableConflictCode(t *testing.T) {
	_, database, _, _, srv := setupTestServerWithWorkspacesServer(t, nil)
	ctx := t.Context()
	repo, err := database.GetRepoByIdentity(ctx, verifiedGitHubRepoIdentity("github.com", "acme", "widget"))
	require.NoError(t, err)
	require.NotNil(t, repo)
	item := mcpserver.ItemIdentity{
		Type: "pr", Provider: "github", PlatformHost: "github.com",
		PlatformRepoID: repo.PlatformRepoID,
		Owner:          "acme", Name: "widget", Number: 1,
	}

	_, err = srv.MCPBackend().CreatePullWorkspace(ctx, item, true)
	require.NoError(t, err)
	_, err = srv.MCPBackend().CreatePullWorkspace(ctx, item, true)

	var backendErr *mcpserver.Error
	require.ErrorAs(t, err, &backendErr)
	assert.Equal(t, "conflict", backendErr.Kind)
	assert.Equal(t, mcpserver.ErrorCodeWorkspaceAlreadyExists, backendErr.Code)
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

func TestMCPBackendReadFailsClosedWhenRouteReassignedMidRead(t *testing.T) {
	srv, database := setupTestServer(t)
	ctx := t.Context()
	seedPR(t, database, "acme", "widget", 42)
	repo, err := database.GetRepoByIdentity(ctx, verifiedGitHubRepoIdentity("github.com", "acme", "widget"))
	require.NoError(t, err)
	require.NotNil(t, repo)
	backend, ok := srv.MCPBackend().(mcpBackend)
	require.True(t, ok)

	resolved, err := backend.resolveRepositoryFence(ctx, mcpserver.RepositoryIdentity{
		Provider: "github", PlatformHost: "github.com",
		PlatformRepoID: repo.PlatformRepoID,
		Owner:          "acme", Name: "widget",
	})
	require.NoError(t, err)

	// Reassign route ownership between stable-identity validation and the
	// route-addressed read, the window the fence exists to police.
	_, accepted, err := database.ReconcileRepositoryObservation(ctx, db.RepoIdentity{
		Platform: "github", PlatformHost: "github.com",
		PlatformRepoID: "replacement-repository",
		Owner:          "acme", Name: "widget", RepoPath: "acme/widget",
	}, time.Now().UTC())
	require.NoError(t, err)
	require.True(t, accepted)

	err = backend.confirmRepositoryRoute(ctx, resolved)

	var backendErr *mcpserver.Error
	require.ErrorAs(t, err, &backendErr)
	assert.Equal(t, "not_found", backendErr.Kind)
	assert.Equal(t, string(httpapi.CodeRepoNotFound), backendErr.Code)
}

func TestMCPBackendWorkflowDoesNotExposeOrMutateRemovedUpstreamItems(t *testing.T) {
	srv, database := setupTestServer(t)
	ctx := t.Context()
	seedPR(t, database, "acme", "widget", 1)
	seedPR(t, database, "acme", "widget", 2)
	seedIssue(t, database, "acme", "widget", 3, "open")
	repo, err := database.GetRepoByIdentity(ctx, verifiedGitHubRepoIdentity("github.com", "acme", "widget"))
	require.NoError(t, err)
	require.NotNil(t, repo)
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

	require.NoError(t, err)
	require.Len(t, page.Items, 1)
	assert.Equal(t, 2, page.Items[0].Identity.Number)
	assert.Equal(t, repo.PlatformRepoID, page.Items[0].Identity.PlatformRepoID)

	_, err = backend.SetWorkflowState(ctx, mcpserver.ItemIdentity{
		Type: "pr", Provider: "github", PlatformHost: "github.com",
		PlatformRepoID: repo.PlatformRepoID,
		Owner:          "acme", Name: "widget", Number: 1,
	}, mcpserver.WorkflowUpdate{
		Status: "reviewing", ExpectedStatus: "new", Source: "mcp",
	})
	var backendErr *mcpserver.Error
	require.ErrorAs(t, err, &backendErr)
	assert.Equal(t, "not_found", backendErr.Kind)
	assert.Equal(t, string(httpapi.CodePullNotFound), backendErr.Code)

	stored, err := database.GetItemWorkflowState(ctx, repo.ID, db.ItemTypePR, 1)
	require.NoError(t, err)
	require.NotNil(t, stored)
	assert.Equal(t, "new", stored.Status)
}
