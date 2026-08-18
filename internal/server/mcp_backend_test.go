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
	}
	assert.False(t, page.Capped)
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
		RepoPath: "acme/widget", Owner: "acme", Name: "widget",
	}

	page, err := backend.ListWorkflowStates(ctx, mcpserver.WorkflowQuery{
		Repository: repository, IncludeClosed: true,
	})

	require.NoError(t, err)
	require.Len(t, page.Items, 1)
	assert.Equal(t, 2, page.Items[0].Identity.Number)

	_, err = backend.SetWorkflowState(ctx, mcpserver.ItemIdentity{
		Type: "pr", Provider: "github", PlatformHost: "github.com",
		Owner: "acme", Name: "widget", Number: 1,
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
