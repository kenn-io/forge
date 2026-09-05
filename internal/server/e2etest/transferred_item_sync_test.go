package e2etest

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	gh "github.com/google/go-github/v90/github"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/forge/internal/apiclient"
	"go.kenn.io/forge/internal/db"
	ghclient "go.kenn.io/forge/internal/github"
	"go.kenn.io/forge/internal/server"
	"go.kenn.io/forge/internal/testutil/dbtest"
)

// transferSyncMockGH overrides the issue read surface of the shared mockGH so
// a full sync cycle can first seed an open issue and then observe it vanish
// from the open inventory while the closed-item fetch returns the
// post-transfer shape.
type transferSyncMockGH struct {
	*mockGH
	openIssues    []*gh.Issue
	issue         *gh.Issue
	getIssueCalls atomic.Int32
}

func (m *transferSyncMockGH) ListOpenIssues(
	context.Context, string, string,
) ([]*gh.Issue, error) {
	return m.openIssues, nil
}

func (m *transferSyncMockGH) GetIssue(
	context.Context, string, string, int,
) (*gh.Issue, error) {
	m.getIssueCalls.Add(1)
	return m.issue, nil
}

// TestRepositorySyncTombstonesPRShapedStaleIssueE2E guards the complete
// closure-detection path: a repository sync must close a SQLite issue whose
// vanished number now resolves to a pull request, then leave it ineligible for
// the same provider lookup on later syncs.
func TestRepositorySyncTombstonesPRShapedStaleIssueE2E(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	ctx := t.Context()

	now := time.Date(2026, 8, 4, 12, 0, 0, 0, time.UTC)
	issueNumber := 7
	issueID := int64(777)
	openState := "open"
	body := ""
	pullURL := "https://api.github.com/repos/acme/widget/pulls/7"
	mock := &transferSyncMockGH{
		mockGH: &mockGH{},
		issue: &gh.Issue{
			ID:               &issueID,
			Number:           &issueNumber,
			Title:            new("now a pull request"),
			State:            &openState,
			HTMLURL:          new("https://github.com/acme/widget/pull/7"),
			RepositoryURL:    new("https://api.github.com/repos/acme/widget"),
			Body:             &body,
			CreatedAt:        &gh.Timestamp{Time: now},
			UpdatedAt:        &gh.Timestamp{Time: now},
			PullRequestLinks: &gh.PullRequestLinks{URL: &pullURL},
		},
	}
	database := dbtest.Open(t)
	repoID, err := database.UpsertRepo(ctx, db.RepoIdentity{
		Platform: "github", PlatformHost: "github.com",
		PlatformRepoID: "repo-acme-widget",
		Owner:          "acme", Name: "widget", RepoPath: "acme/widget",
	})
	require.NoError(err)
	_, err = database.UpsertIssue(ctx, &db.Issue{
		RepoID: repoID, PlatformID: issueID, PlatformExternalID: "I_777",
		Number: issueNumber, URL: "https://github.com/acme/widget/issues/7",
		Title: "stale issue", Author: "author", State: "open",
		CreatedAt: now, UpdatedAt: now, LastActivityAt: now,
	})
	require.NoError(err)

	syncer := ghclient.NewSyncer(
		map[string]ghclient.Client{"github.com": mock}, database, nil,
		[]ghclient.RepoRef{{
			Owner: "acme", Name: "widget", PlatformHost: "github.com",
			PlatformExternalID: "repo-acme-widget",
		}},
		time.Minute, nil, nil,
	)
	t.Cleanup(syncer.Stop)
	srv := server.New(database, syncer, nil, "/", nil, server.ServerOptions{
		HostCheckAllowLoopbackAnyPort: true,
	})
	forge := httptest.NewServer(srv)
	t.Cleanup(forge.Close)
	api, err := apiclient.NewWithHTTPClient(forge.URL, forge.Client())
	require.NoError(err)

	triggerSync := func(after *time.Time) time.Time {
		t.Helper()
		response, err := api.HTTP.TriggerSyncWithResponse(
			ctx, nil,
			func(_ context.Context, req *http.Request) error {
				req.Header.Set("Content-Type", "application/json")
				return nil
			},
		)
		require.NoError(err)
		require.Equal(http.StatusAccepted, response.StatusCode(), string(response.Body))

		var completedAt time.Time
		require.Eventually(func() bool {
			status, err := api.HTTP.GetSyncStatusWithResponse(ctx)
			if err != nil || status.StatusCode() != http.StatusOK || status.JSON200 == nil ||
				status.JSON200.Running || status.JSON200.LastRunAt == nil {
				return false
			}
			completedAt = *status.JSON200.LastRunAt
			return after == nil || completedAt.After(*after)
		}, 5*time.Second, 10*time.Millisecond)
		return completedAt
	}

	firstSync := triggerSync(nil)
	issue, err := api.HTTP.GetIssueWithResponse(
		ctx, "gh", "acme", "widget", int64(issueNumber),
	)
	require.NoError(err)
	require.Equal(http.StatusOK, issue.StatusCode(), string(issue.Body))
	require.NotNil(issue.JSON200)
	assert.Equal("closed", issue.JSON200.Issue.State)
	assert.Equal(int32(1), mock.getIssueCalls.Load())

	triggerSync(&firstSync)
	assert.Equal(int32(1), mock.getIssueCalls.Load(),
		"a tombstoned issue must not be fetched by the next repository sync")
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
