package e2etest

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/forge/internal/apiclient"
	"go.kenn.io/forge/internal/db"
	ghclient "go.kenn.io/forge/internal/github"
	"go.kenn.io/forge/internal/server"
	"go.kenn.io/forge/internal/testutil/dbtest"
	"go.kenn.io/forge/internal/testutil/servertest"
)

func TestPullDetailReportsPausedRateTrackerE2E(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)

	database := dbtest.Open(t)
	tracker := ghclient.NewRateTracker(database, "github.com", "host", "rest")
	syncer := ghclient.NewSyncer(
		map[string]ghclient.Client{"github.com": &mockGH{}},
		database,
		nil,
		[]ghclient.RepoRef{{Owner: "acme", Name: "widget", PlatformHost: "github.com"}},
		time.Minute,
		map[string]*ghclient.RateTracker{ghclient.RateBucketKey("github", "github.com", "host"): tracker},
		nil,
	)
	t.Cleanup(syncer.Stop)

	repoID, err := database.UpsertRepo(t.Context(), db.GitHubRepoIdentity("github.com", "acme", "widget"))
	require.NoError(err)
	_, err = database.UpsertMergeRequest(t.Context(), &db.MergeRequest{
		RepoID:         repoID,
		PlatformID:     7001,
		Number:         7,
		URL:            "https://github.com/acme/widget/pull/7",
		Title:          "Update widget",
		Author:         "ada",
		State:          "open",
		CreatedAt:      time.Date(2026, time.July, 22, 12, 0, 0, 0, time.UTC),
		UpdatedAt:      time.Date(2026, time.July, 22, 12, 5, 0, 0, time.UTC),
		LastActivityAt: time.Date(2026, time.July, 22, 12, 5, 0, 0, time.UTC),
	})
	require.NoError(err)

	resetAt := time.Now().UTC().Truncate(time.Second).Add(30 * time.Minute)
	tracker.UpdateFromRate(ghclient.Rate{Limit: 5000, Remaining: 0, Reset: resetAt})

	srv := servertest.New(t, database, syncer, nil, "/", nil, server.ServerOptions{
		HostCheckAllowLoopbackAnyPort: true,
	})
	forge := httptest.NewServer(srv)
	t.Cleanup(forge.Close)

	client, err := apiclient.NewWithHTTPClient(forge.URL, forge.Client())
	require.NoError(err)
	response, err := client.HTTP.GetPullWithResponse(t.Context(), "github", "acme", "widget", 7)
	require.NoError(err)
	require.Equal(http.StatusOK, response.StatusCode(), string(response.Body))
	require.NotNil(response.JSON200)
	require.NotNil(response.JSON200.Repo.Operations)

	merge := response.JSON200.Repo.Operations.MergePr
	assert.False(merge.Available)
	require.NotNil(merge.Code)
	assert.Equal("rate_limited", *merge.Code)
	require.NotNil(merge.UnavailableReason)
	assert.Equal("github.com rate-limited", *merge.UnavailableReason)
	require.NotNil(merge.RetryAt)
	assert.Equal(resetAt.Format(time.RFC3339), *merge.RetryAt)
}
