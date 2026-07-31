package e2etest

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	gh "github.com/google/go-github/v89/github"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/forge/internal/apiclient"
	ghclient "go.kenn.io/forge/internal/github"
	"go.kenn.io/forge/internal/server"
	"go.kenn.io/forge/internal/testutil/dbtest"
)

type pullRequestIssueMockGH struct {
	*mockGH
	issues []*gh.Issue
}

func (m *pullRequestIssueMockGH) ListOpenIssues(
	context.Context, string, string,
) ([]*gh.Issue, error) {
	return m.issues, nil
}

func TestPullRequestShapedIssueIsNotPersistedOrExposed(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	ctx := t.Context()

	issueNumber := 41
	issueID := int64(4100)
	issueTitle := "issue returned by issues endpoint"
	pullNumber := 42
	pullID := int64(4200)
	pullTitle := "pull request returned by issues endpoint"
	state := "open"
	issueURL := "https://github.com/acme/widget/issues/41"
	pullURL := "https://github.com/acme/widget/pull/42"
	now := time.Date(2026, 7, 29, 12, 0, 0, 0, time.UTC)
	mock := &pullRequestIssueMockGH{
		mockGH: &mockGH{},
		issues: []*gh.Issue{
			{
				ID:        &issueID,
				Number:    &issueNumber,
				Title:     &issueTitle,
				State:     &state,
				HTMLURL:   &issueURL,
				CreatedAt: &gh.Timestamp{Time: now},
				UpdatedAt: &gh.Timestamp{Time: now},
			},
			{
				ID:               &pullID,
				Number:           &pullNumber,
				Title:            &pullTitle,
				State:            &state,
				HTMLURL:          &pullURL,
				CreatedAt:        &gh.Timestamp{Time: now},
				UpdatedAt:        &gh.Timestamp{Time: now},
				PullRequestLinks: &gh.PullRequestLinks{},
			},
		},
	}
	database := dbtest.Open(t)
	syncer := ghclient.NewSyncer(
		map[string]ghclient.Client{"github.com": mock},
		database, nil, []ghclient.RepoRef{{
			Owner: "acme", Name: "widget", PlatformHost: "github.com",
		}}, time.Minute, nil, nil,
	)
	t.Cleanup(syncer.Stop)
	srv := server.New(database, syncer, nil, "/", nil, server.ServerOptions{
		HostCheckAllowLoopbackAnyPort: true,
	})
	ts := httptest.NewServer(srv)
	defer ts.Close()
	client, err := apiclient.NewWithHTTPClient(ts.URL, ts.Client())
	require.NoError(err)

	syncer.RunOnce(ctx)

	repos, err := database.ListRepos(ctx)
	require.NoError(err)
	require.Len(repos, 1)
	storedIssue, err := database.GetIssueByRepoIDAndNumber(
		ctx, repos[0].ID, issueNumber,
	)
	require.NoError(err)
	require.NotNil(storedIssue)
	assert.Equal(issueTitle, storedIssue.Title)

	storedPull, err := database.GetIssueByRepoIDAndNumber(
		ctx, repos[0].ID, pullNumber,
	)
	require.NoError(err)
	assert.Nil(storedPull)

	issueResponse, err := client.HTTP.GetIssueWithResponse(
		ctx, "gh", "acme", "widget", int64(issueNumber),
	)
	require.NoError(err)
	require.Equal(
		http.StatusOK,
		issueResponse.StatusCode(),
		string(issueResponse.Body),
	)
	require.NotNil(issueResponse.JSON200)
	assert.Equal(issueTitle, issueResponse.JSON200.Issue.Title)

	pullResponse, err := client.HTTP.GetIssueWithResponse(
		ctx, "gh", "acme", "widget", int64(pullNumber),
	)
	require.NoError(err)
	assert.Equal(
		http.StatusNotFound,
		pullResponse.StatusCode(),
		string(pullResponse.Body),
	)
}
