package apitest

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/forge/internal/apiclient/generated"
	"go.kenn.io/forge/internal/db"
)

func TestGHShimReadsStoredPullWithoutProviderAndSeesUpdatesImmediately(t *testing.T) {
	t.Parallel()
	require := require.New(t)
	assert := assert.New(t)
	// This fixture has no provider client. All answers must come from SQLite.
	srv, database := setupTestServer(t)
	old := time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC)
	seedPR(t, database, "acme", "widget", 7, func(pr *db.MergeRequest) { pr.UpdatedAt = old })
	client := setupTestClient(t, srv)
	body := generated.QueryGhBody{Command: "view", Host: "github.com", Owner: "acme", Repo: "widget", Number: 7, State: "open", Limit: 30, Fields: []string{"number", "title", "state"}}
	response, err := client.HTTP.QueryGhWithResponse(t.Context(), &generated.QueryGhRequestOptions{Body: &body})
	require.NoError(err)
	require.NotNil(response.JSON200)
	assert.True(response.JSON200.Handled)
	assert.JSONEq(`{"number":7,"state":"OPEN","title":"Test PR #7"}`, response.JSON200.Output)
	stored, err := database.GetMergeRequest(t.Context(), "github", "github.com", "acme", "widget", 7)
	require.NoError(err)
	require.NotNil(stored)
	stored.Title = "Updated in storage"
	stored.UpdatedAt = old.Add(time.Second)
	_, err = database.UpsertMergeRequest(t.Context(), stored)
	require.NoError(err)
	response, err = client.HTTP.QueryGhWithResponse(t.Context(), &generated.QueryGhRequestOptions{Body: &body})
	require.NoError(err)
	require.NotNil(response.JSON200)
	assert.True(response.JSON200.Handled)
	assert.Contains(response.JSON200.Output, "Updated in storage")
	for _, tc := range []struct {
		change func(*generated.QueryGhBody)
		reason string
	}{
		{func(q *generated.QueryGhBody) { q.Repo = "other" }, "untracked"},
		{func(q *generated.QueryGhBody) { q.Host = "code.example" }, "untracked"},
		{func(q *generated.QueryGhBody) { q.Fields = []string{"reviews"} }, "unsupported"},
		{func(q *generated.QueryGhBody) { q.Number = 99 }, "data_unavailable"},
		{func(q *generated.QueryGhBody) { q.Fields = []string{"headRefOid"} }, "data_unavailable"},
	} {
		candidate := body
		tc.change(&candidate)
		result, err := client.HTTP.QueryGhWithResponse(t.Context(), &generated.QueryGhRequestOptions{Body: &candidate})
		require.NoError(err)
		require.NotNil(result.JSON200)
		assert.False(result.JSON200.Handled)
		assert.Empty(result.JSON200.Output)
		assert.Equal(tc.reason, result.JSON200.Reason)
	}
}

func TestGHShimStoredListsRequireInventoryAndPreserveGHFiltering(t *testing.T) {
	t.Parallel()
	require := require.New(t)
	assert := assert.New(t)
	srv, database := setupTestServer(t)
	old := time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC)
	seedPR(t, database, "acme", "widget", 1, func(pr *db.MergeRequest) { pr.CreatedAt = old; pr.UpdatedAt = old.Add(10 * time.Hour) })
	seedPR(t, database, "acme", "widget", 2, func(pr *db.MergeRequest) { pr.CreatedAt = old.Add(time.Hour); pr.IsLocked = true })
	seedPR(t, database, "acme", "widget", 3, func(pr *db.MergeRequest) { pr.CreatedAt = old.Add(2 * time.Hour); pr.HeadBranch = "other" })
	seedPR(t, database, "acme", "widget", 4, withSeedPRState(db.MergeRequestStateMerged), func(pr *db.MergeRequest) { pr.CreatedAt = old.Add(3 * time.Hour) })
	repo, err := database.GetRepoByIdentity(t.Context(), verifiedGitHubRepoIdentity("github.com", "acme", "widget"))
	require.NoError(err)
	require.NotNil(repo)
	client := setupTestClient(t, srv)
	body := generated.QueryGhBody{Command: "list", Host: "github.com", Owner: "acme", Repo: "widget", State: "open", Head: "feature", Limit: 1, Fields: []string{"number", "state"}}
	result, err := client.HTTP.QueryGhWithResponse(t.Context(), &generated.QueryGhRequestOptions{Body: &body})
	require.NoError(err)
	require.NotNil(result.JSON200)
	assert.False(result.JSON200.Handled)
	require.NoError(database.UpdateRepoSyncCompleted(t.Context(), repo.ID, old, ""))
	result, err = client.HTTP.QueryGhWithResponse(t.Context(), &generated.QueryGhRequestOptions{Body: &body})
	require.NoError(err)
	require.NotNil(result.JSON200)
	assert.True(result.JSON200.Handled)
	assert.JSONEq(`[{"number":2,"state":"OPEN"}]`, result.JSON200.Output)
	body.State = "all"
	result, err = client.HTTP.QueryGhWithResponse(t.Context(), &generated.QueryGhRequestOptions{Body: &body})
	require.NoError(err)
	require.NotNil(result.JSON200)
	assert.False(result.JSON200.Handled)
	assert.Empty(result.JSON200.Output)
	require.NoError(database.EnsureDiscoveryArchives(t.Context(), []int64{repo.ID}, old))
	_, err = database.WriteDB().ExecContext(t.Context(), `UPDATE forge_archive_repo_scans SET status='complete' WHERE repo_id=? AND scan='merge_request_inventory'`, repo.ID)
	require.NoError(err)
	for _, state := range []string{"all", "closed", "merged"} {
		body.State = state
		result, err = client.HTTP.QueryGhWithResponse(t.Context(), &generated.QueryGhRequestOptions{Body: &body})
		require.NoError(err)
		require.NotNil(result.JSON200)
		assert.True(result.JSON200.Handled)
		assert.JSONEq(`[{"number":4,"state":"MERGED"}]`, result.JSON200.Output)
	}
	body.Head = "missing"
	result, err = client.HTTP.QueryGhWithResponse(t.Context(), &generated.QueryGhRequestOptions{Body: &body})
	require.NoError(err)
	require.NotNil(result.JSON200)
	assert.True(result.JSON200.Handled)
	assert.Equal("[]\n", result.JSON200.Output)
}
