package semantictest

import (
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"go.kenn.io/forge/internal/apiclient/generated"
	"go.kenn.io/forge/internal/db"
	ghclient "go.kenn.io/forge/internal/github"
	"go.kenn.io/forge/internal/semantic"
	"go.kenn.io/forge/internal/server"
	"go.kenn.io/forge/internal/testutil/dbtest"
	"go.kenn.io/forge/internal/testutil/embedfake"
	"go.kenn.io/forge/internal/testutil/gitsafe"
	serverfake "go.kenn.io/forge/internal/testutil/serverfake"
	servertest "go.kenn.io/forge/internal/testutil/servertest"
)

func TestMain(m *testing.M) {
	os.Exit(serverfake.RunMain(m, func() int { return gitsafe.RunIsolatedMain(m) }))
}

func newSemanticServer(t *testing.T, database *db.DB, indexer *semantic.Indexer) *server.Server {
	t.Helper()
	syncer := ghclient.NewSyncer(
		map[string]ghclient.Client{"github.com": &serverfake.MockGH{}}, database, nil, nil, time.Minute, nil, nil,
	)
	t.Cleanup(syncer.Stop)
	return servertest.New(t, database, syncer, nil, "/", nil, server.ServerOptions{SemanticSearch: indexer})
}

func TestSemanticSearchRanksSyncedItemsByDescriptionAndComments(t *testing.T) {
	serverfake.RunParallelServerTest(t)
	assert := assert.New(t)
	ctx := t.Context()
	database := dbtest.Open(t)
	serverfake.SeedPR(t, database, "acme", "widget", 1, serverfake.WithSeedPRTitle("Warm the cache after deploys"))
	loginPR := serverfake.SeedPR(t, database, "acme", "widget", 2, serverfake.WithSeedPRTitle("Fix login redirect"))
	issueID := serverfake.SeedIssue(t, database, "acme", "widget", 3, "open")
	_, err := database.WriteDB().ExecContext(ctx,
		`UPDATE forge_issues SET title = 'Cache misses on cold start' WHERE id = ?`, issueID)
	require.NoError(t, err)
	require.NoError(t, database.UpsertMREvents(ctx, []db.MREvent{{
		MergeRequestID: loginPR, EventType: "issue_comment",
		Body: "This test is flaky in CI, flaky again today.", CreatedAt: time.Now().UTC(), DedupeKey: "comment-1",
	}}))

	endpoint := embedfake.New(t, "cache", "login", "flaky")
	encoder, err := semantic.NewEncoder(endpoint.Embedder("test-model"))
	require.NoError(t, err)
	index, err := semantic.Open(ctx, filepath.Join(t.TempDir(), semantic.SidecarFile))
	require.NoError(t, err)
	t.Cleanup(func() { _ = index.Close() })
	indexer := semantic.NewIndexer(index, encoder, database, time.Hour)
	require.NoError(t, indexer.Refresh(ctx))

	client := servertest.SetupTestClient(t, newSemanticServer(t, database, indexer)).HTTP

	resp, err := client.SemanticSearchWithResponse(ctx, &generated.SemanticSearchRequestOptions{
		Query: &generated.SemanticSearchQuery{Q: "flaky tests"},
	})
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, resp.StatusCode, string(resp.Body))
	require.NotNil(t, resp.JSON200)
	assert.True(resp.JSON200.Ready)
	require.NotEmpty(t, resp.JSON200.Items)
	top := resp.JSON200.Items[0]
	assert.Equal(int64(2), top.Number, "the comment ranks its pull request first")
	assert.Equal(generated.SemanticSearchMatchResponseKind("comment"), top.Match.Kind)
	assert.NotNil(top.Match.CommentID)
	assert.Contains(top.Match.Excerpt, "flaky in CI")
	assert.Equal("Fix login redirect", top.Title)
	require.NotNil(t, top.Pull, "a pull request carries its list row")
	assert.Nil(top.Issue)
	assert.Equal("acme/widget", top.Repo.RepoPath)

	issueType := generated.SemanticSearchQueryItemType("issue")
	resp, err = client.SemanticSearchWithResponse(ctx, &generated.SemanticSearchRequestOptions{
		Query: &generated.SemanticSearchQuery{Q: "cache", ItemType: &issueType},
	})
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, resp.StatusCode, string(resp.Body))
	require.Len(t, resp.JSON200.Items, 1)
	assert.Equal(int64(3), resp.JSON200.Items[0].Number)
	assert.Equal(generated.SemanticSearchMatchResponseKind("description"), resp.JSON200.Items[0].Match.Kind)
	assert.NotNil(resp.JSON200.Items[0].Issue)

	repo := "github|github.com/acme/widget"
	resp, err = client.SemanticSearchWithResponse(ctx, &generated.SemanticSearchRequestOptions{
		Query: &generated.SemanticSearchQuery{Q: "login", Repo: &repo},
	})
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, resp.StatusCode, string(resp.Body))
	assert.Len(resp.JSON200.Items, 3)

	unknown := "github|github.com/acme/missing"
	resp, err = client.SemanticSearchWithResponse(ctx, &generated.SemanticSearchRequestOptions{
		Query: &generated.SemanticSearchQuery{Q: "login", Repo: &unknown},
	})
	require.Error(t, err)
	assert.Equal(http.StatusBadRequest, resp.StatusCode, "an untracked repository is rejected, not searched as empty")

	status, err := client.GetSemanticSearchStatusWithResponse(ctx)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, status.StatusCode)
	assert.True(status.JSON200.Enabled)
	assert.True(status.JSON200.Ready)
	assert.Equal("test-model", status.JSON200.Model)
	assert.Equal(int64(4), status.JSON200.Embedded)
}

func TestSemanticSearchWithoutEmbeddingsIsUnavailable(t *testing.T) {
	serverfake.RunParallelServerTest(t)
	database := dbtest.Open(t)
	client := servertest.SetupTestClient(t, newSemanticServer(t, database, nil)).HTTP

	resp, err := client.SemanticSearchWithResponse(t.Context(), &generated.SemanticSearchRequestOptions{
		Query: &generated.SemanticSearchQuery{Q: "anything"},
	})
	require.Error(t, err)
	assert.Equal(t, http.StatusServiceUnavailable, resp.StatusCode)

	status, err := client.GetSemanticSearchStatusWithResponse(t.Context())
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, status.StatusCode)
	assert.False(t, status.JSON200.Enabled)
}
