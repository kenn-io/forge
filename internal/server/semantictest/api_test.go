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

func TestListsRankSyncedItemsByMeaningWhenSemanticIsSet(t *testing.T) {
	serverfake.RunParallelServerTest(t)
	assert := assert.New(t)
	ctx := t.Context()
	database := dbtest.Open(t)
	serverfake.SeedPR(t, database, "acme", "widget", 1, serverfake.WithSeedPRTitle("Warm the cache after deploys"))
	loginPR := serverfake.SeedPR(t, database, "acme", "widget", 2, serverfake.WithSeedPRTitle("Fix login redirect"))
	serverfake.SeedPR(t, database, "acme", "other", 4, serverfake.WithSeedPRTitle("Flaky flaky flaky"))
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
	indexer, err := semantic.Open(ctx, filepath.Join(t.TempDir(), semantic.SidecarFile), encoder, database, time.Hour)
	require.NoError(t, err)
	t.Cleanup(func() { _ = indexer.Close() })
	require.NoError(t, indexer.Refresh(ctx))

	client := servertest.SetupTestClient(t, newSemanticServer(t, database, indexer)).HTTP

	pulls, err := client.ListPullsWithResponse(ctx, &generated.ListPullsRequestOptions{
		Query: &generated.ListPullsQuery{Q: new("flaky tests"), Semantic: new(true), Repo: new("github|github.com/acme/widget")},
	})
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, pulls.StatusCode, string(pulls.Body))
	require.Len(t, *pulls.JSON200, 2, "the repository filter applies before ranking")
	top := (*pulls.JSON200)[0]
	assert.Equal(int64(2), top.Number, "the comment ranks its pull request first")
	require.NotNil(t, top.SearchScore)
	assert.Greater(*top.SearchScore, *(*pulls.JSON200)[1].SearchScore)

	issues, err := client.ListIssuesWithResponse(ctx, &generated.ListIssuesRequestOptions{
		Query: &generated.ListIssuesQuery{Q: new("cache"), Semantic: new(true)},
	})
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, issues.StatusCode, string(issues.Body))
	require.Len(t, *issues.JSON200, 1)
	assert.Equal(int64(3), (*issues.JSON200)[0].Number)

	pulls, err = client.ListPullsWithResponse(ctx, &generated.ListPullsRequestOptions{
		Query: &generated.ListPullsQuery{Semantic: new(true)},
	})
	require.Error(t, err)
	assert.Equal(http.StatusBadRequest, pulls.StatusCode, "semantic ranking needs a query")
}

func TestSemanticListWithoutEmbeddingsIsUnavailable(t *testing.T) {
	serverfake.RunParallelServerTest(t)
	database := dbtest.Open(t)
	client := servertest.SetupTestClient(t, newSemanticServer(t, database, nil)).HTTP

	resp, err := client.ListPullsWithResponse(t.Context(), &generated.ListPullsRequestOptions{
		Query: &generated.ListPullsQuery{Q: new("anything"), Semantic: new(true)},
	})
	require.Error(t, err)
	assert.Equal(t, http.StatusServiceUnavailable, resp.StatusCode)
}
