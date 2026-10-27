package pulltest

import (
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"go.kenn.io/forge/internal/apiclient/generated"
	"go.kenn.io/forge/internal/db"
	"go.kenn.io/forge/internal/testutil/reposeed"
	"go.kenn.io/forge/internal/testutil/serverfake"
	"go.kenn.io/forge/internal/testutil/servertest"
)

func TestAPIPRSizeHistoryInListAndDetail(t *testing.T) {
	serverfake.RunParallelServerTest(t)
	srv, database := servertest.SetupTestServer(t)
	repoID, err := reposeed.Seed(t.Context(), database, serverfake.VerifiedGitHubRepoIdentity("github.com", "acme", "widget"))
	require.NoError(t, err)
	mr := &db.MergeRequest{
		RepoID: repoID, Number: 7, Title: "Improve search", State: db.MergeRequestStateOpen,
		PlatformHeadSHA: "first-head", PlatformBaseSHA: "base", Additions: 100, Deletions: 20,
		AdditionsKnown: true, DeletionsKnown: true, UpdatedAt: time.Now().UTC(),
	}
	_, err = database.UpsertMergeRequest(t.Context(), mr)
	require.NoError(t, err)
	mr.PlatformHeadSHA, mr.Additions = "second-head", 50
	_, err = database.UpsertMergeRequest(t.Context(), mr)
	require.NoError(t, err)
	client := servertest.SetupTestClient(t, srv)
	list, err := client.HTTP.ListPullsWithResponse(t.Context(), &generated.ListPullsRequestOptions{})
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, list.StatusCode)
	require.NotNil(t, list.JSON200)
	require.Len(t, *list.JSON200, 1)
	points := (*list.JSON200)[0].DiffSizeHistory
	require.Len(t, points, 2)
	require.Equal(t, int64(100), points[0].Additions)
	require.Equal(t, int64(50), points[1].Additions)
	require.Equal(t, time.UTC, points[1].ObservedAt.Location())
	detail, err := client.HTTP.GetPullWithResponse(t.Context(), &generated.GetPullRequestOptions{
		PathParams: &generated.GetPullPath{Provider: "github", Owner: "acme", Name: "widget", Number: 7},
	})
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, detail.StatusCode)
	require.NotNil(t, detail.JSON200)
	require.Equal(t, points, detail.JSON200.MergeRequest.DiffSizeHistory)
}
