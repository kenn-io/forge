package e2etest

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	gh "github.com/google/go-github/v88/github"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/middleman/internal/apiclient"
	ghclient "go.kenn.io/middleman/internal/github"
	"go.kenn.io/middleman/internal/server"
	"go.kenn.io/middleman/internal/testutil/dbtest"
)

// transferSyncMockGH overrides the issue read surface of the shared mockGH so
// a full sync cycle can first seed an open issue and then observe it vanish
// from the open inventory while the closed-item fetch returns the
// post-transfer shape.
type transferSyncMockGH struct {
	*mockGH
	openIssues []*gh.Issue
	issue      *gh.Issue
}

func (m *transferSyncMockGH) ListOpenIssues(
	context.Context, string, string,
) ([]*gh.Issue, error) {
	return m.openIssues, nil
}

func (m *transferSyncMockGH) GetIssue(
	context.Context, string, string, int,
) (*gh.Issue, error) {
	return m.issue, nil
}

// TestTransferredIssueObservableViaAPIE2E is the HTTP boundary counterpart of
// the sync-engine transfer test: after a real sync cycle discovers that an
// open issue was transferred to another repository, the generated API client
// must observe (a) the source issue unchanged, (b) the failed repo sync
// cycle via the repo listing's sync-health fields, and (c) nothing under the
// destination repository. Sync-engine persistence internals are covered by
// internal/github; this test only asserts what a client can see.
func TestTransferredIssueObservableViaAPIE2E(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	ctx := t.Context()

	now := time.Date(2024, 6, 1, 12, 0, 0, 0, time.UTC)
	issueNumber := 7
	sourceTitle := "issue before transfer"
	sourceURL := "https://github.com/acme/widget/issues/7"
	openState := "open"
	body := ""
	issueID := int64(777)
	sourceIssue := &gh.Issue{
		ID:        &issueID,
		Number:    &issueNumber,
		Title:     &sourceTitle,
		State:     &openState,
		HTMLURL:   &sourceURL,
		Body:      &body,
		CreatedAt: &gh.Timestamp{Time: now},
		UpdatedAt: &gh.Timestamp{Time: now},
	}

	database := dbtest.Open(t)
	mock := &transferSyncMockGH{
		mockGH:     &mockGH{},
		openIssues: []*gh.Issue{sourceIssue},
		issue:      sourceIssue,
	}
	repos := []ghclient.RepoRef{{
		Owner: "acme", Name: "widget", PlatformHost: "github.com",
	}}
	syncer := ghclient.NewSyncer(
		map[string]ghclient.Client{"github.com": mock},
		database, nil, repos, time.Minute, nil, nil,
	)
	t.Cleanup(syncer.Stop)
	srv := server.New(database, syncer, nil, "/", nil, server.ServerOptions{
		HostCheckAllowLoopbackAnyPort: true,
	})
	ts := httptest.NewServer(srv)
	defer ts.Close()
	client, err := apiclient.NewWithHTTPClient(ts.URL, ts.Client())
	require.NoError(err)

	// Seed cycle: the issue is open in the source repo.
	syncer.RunOnce(ctx)

	seeded, err := client.HTTP.GetIssueWithResponse(
		ctx, "gh", "acme", "widget", int64(issueNumber),
	)
	require.NoError(err)
	require.Equal(http.StatusOK, seeded.StatusCode(), string(seeded.Body))
	require.NotNil(seeded.JSON200)
	require.Equal(sourceTitle, seeded.JSON200.Issue.Title)

	// Transfer cycle: the issue is gone from the open inventory and the
	// closed-item fetch returns it as it now exists in the destination
	// repository (GitHub follows the 301 and serves the moved item).
	movedTitle := "issue after transfer"
	movedURL := "https://github.com/newowner/newname/issues/7"
	movedRepositoryURL := "https://api.github.com/repos/newowner/newname"
	mock.openIssues = nil
	mock.issue = &gh.Issue{
		ID:            &issueID,
		Number:        &issueNumber,
		Title:         &movedTitle,
		State:         &openState,
		HTMLURL:       &movedURL,
		RepositoryURL: &movedRepositoryURL,
		Body:          &body,
		CreatedAt:     &gh.Timestamp{Time: now},
		UpdatedAt:     &gh.Timestamp{Time: now.Add(time.Hour)},
	}
	syncer.RunOnce(ctx)

	// (a) The source issue is served unchanged.
	source, err := client.HTTP.GetIssueWithResponse(
		ctx, "gh", "acme", "widget", int64(issueNumber),
	)
	require.NoError(err)
	require.Equal(http.StatusOK, source.StatusCode(), string(source.Body))
	require.NotNil(source.JSON200)
	assert.Equal(sourceTitle, source.JSON200.Issue.Title,
		"source issue must not be rewritten with destination data")
	assert.Equal(sourceURL, source.JSON200.Issue.URL,
		"source issue must keep its repo URL")
	assert.Equal("open", source.JSON200.Issue.State)

	// (b) The failed sync cycle is observable through repo sync health.
	reposResp, err := client.HTTP.ListReposWithResponse(ctx)
	require.NoError(err)
	require.Equal(http.StatusOK, reposResp.StatusCode(), string(reposResp.Body))
	require.NotNil(reposResp.JSON200)
	require.Len(*reposResp.JSON200, 1)
	repo := (*reposResp.JSON200)[0]
	assert.Equal("acme", repo.Owner)
	assert.Equal("widget", repo.Name)
	assert.NotEmpty(repo.LastSyncError,
		"the transferred item must surface as a failed repo sync cycle")

	// (c) Nothing is served under the destination repository.
	destination, err := client.HTTP.GetIssueWithResponse(
		ctx, "gh", "newowner", "newname", int64(issueNumber),
	)
	require.NoError(err)
	assert.Equal(
		http.StatusNotFound, destination.StatusCode(), string(destination.Body),
	)
}
