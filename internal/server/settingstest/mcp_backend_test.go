package settingstest

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/forge/internal/db"
	"go.kenn.io/forge/internal/mcpserver"
	"go.kenn.io/forge/internal/server/httpapi"
)

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
