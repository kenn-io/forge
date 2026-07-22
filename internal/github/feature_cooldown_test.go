package github

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sync/atomic"
	"testing"
	"time"

	gh "github.com/google/go-github/v88/github"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"go.kenn.io/middleman/internal/db"
	"go.kenn.io/middleman/internal/platform"
)

func TestDisabledIssueScopeUsesDailyBackgroundProbeAndManualBypass(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	database := openTestDB(t)
	now := time.Date(2026, 7, 21, 12, 0, 0, 0, time.UTC)
	repo := RepoRef{
		Platform: platform.KindGitHub, PlatformHost: "github.com",
		Owner: "acme", Name: "widget",
	}
	client := &partialFailureMock{}
	disabledCause := errors.New("repository issues disabled")
	client.listOpenIssuesErr = platform.RepositoryFeatureDisabled(
		platform.KindGitHub, "github.com", platform.RepositoryFeatureIssues,
		disabledCause,
	)
	var issueListCalls atomic.Int32
	client.listOpenIssuesFn = func(context.Context, string, string) ([]*gh.Issue, error) {
		issueListCalls.Add(1)
		return nil, client.listOpenIssuesErr
	}

	syncer := NewSyncer(
		map[string]Client{"github.com": client}, database, nil,
		[]RepoRef{repo}, time.Minute, nil, nil,
	)
	syncer.now = func() time.Time { return now }

	syncer.RunOnce(t.Context())
	syncer.RunOnce(t.Context())
	assert.Equal(int32(1), issueListCalls.Load())
	_, failed := syncer.failedRepos.Load(repoFailKey(repo))
	assert.False(failed)

	require.NoError(syncer.SyncRepoOnProvider(
		t.Context(), platform.KindGitHub, "github.com", "acme", "widget",
	))
	assert.Equal(int32(2), issueListCalls.Load())

	now = now.Add(24*time.Hour - time.Second)
	syncer.RunOnce(t.Context())
	assert.Equal(int32(2), issueListCalls.Load())

	now = now.Add(time.Second)
	syncer.RunOnce(t.Context())
	assert.Equal(int32(3), issueListCalls.Load())

	client.listOpenIssuesErr = nil
	require.NoError(syncer.SyncRepoOnProvider(
		t.Context(), platform.KindGitHub, "github.com", "acme", "widget",
	))
	assert.Equal(int32(4), issueListCalls.Load())
	client.listOpenIssuesErr = platform.RepositoryFeatureDisabled(
		platform.KindGitHub, "github.com", platform.RepositoryFeatureIssues,
		disabledCause,
	)
	syncer.RunOnce(t.Context())
	assert.Equal(int32(5), issueListCalls.Load(), "successful manual probe must clear the cooldown")
}

func TestExpiredMergeRequestCooldownAllowsOneConcurrentBackgroundProbe(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	database := openTestDB(t)
	now := time.Date(2026, 7, 21, 12, 0, 0, 0, time.UTC)
	repo := RepoRef{
		Platform: platform.KindGitHub, PlatformHost: "github.com",
		Owner: "acme", Name: "widget",
	}
	disabledErr := platform.RepositoryFeatureDisabled(
		platform.KindGitHub, "github.com", platform.RepositoryFeatureMergeRequests,
		errors.New("repository pull requests disabled"),
	)
	indexProbeStarted := make(chan struct{})
	releaseIndexProbe := make(chan struct{})
	var providerCalls atomic.Int32
	client := &detailTrackingClient{}
	client.listOpenPRsFn = func(context.Context, string, string) ([]*gh.PullRequest, error) {
		providerCalls.Add(1)
		close(indexProbeStarted)
		<-releaseIndexProbe
		return nil, disabledErr
	}
	client.getPullRequestFn = func(context.Context, string, string, int) (*gh.PullRequest, error) {
		providerCalls.Add(1)
		return nil, disabledErr
	}

	syncer := NewSyncer(
		map[string]Client{"github.com": client}, database, nil,
		[]RepoRef{repo}, time.Minute, nil, nil,
	)
	syncer.now = func() time.Time { return now }
	syncer.SetWatchedMRs([]WatchedMR{{
		Platform: platform.KindGitHub, PlatformHost: "github.com",
		Owner: "acme", Name: "widget", Number: 7,
	}})
	require.True(syncer.recordRepositoryFeatureDisabled(
		repo, platform.RepositoryFeatureMergeRequests, disabledErr,
	))
	now = now.Add(repositoryFeatureProbeInterval)

	indexDone := make(chan struct{})
	go func() {
		defer close(indexDone)
		syncer.RunOnce(t.Context())
	}()
	<-indexProbeStarted

	syncer.syncWatchedMRs(t.Context())
	close(releaseIndexProbe)
	<-indexDone

	assert.Equal(int32(1), providerCalls.Load())
}

func TestSuccessfulProbeDoesNotClearConcurrentDisabledRenewal(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	database := openTestDB(t)
	now := time.Date(2026, 7, 21, 12, 0, 0, 0, time.UTC)
	repo := RepoRef{
		Platform: platform.KindGitHub, PlatformHost: "github.com",
		Owner: "acme", Name: "widget",
	}
	disabledErr := platform.RepositoryFeatureDisabled(
		platform.KindGitHub, "github.com", platform.RepositoryFeatureIssues,
		errors.New("repository issues disabled"),
	)
	backgroundProbeStarted := make(chan struct{})
	releaseBackgroundProbe := make(chan struct{})
	var issueListCalls atomic.Int32
	client := &partialFailureMock{listOpenPRsErr: notModifiedErr()}
	client.listOpenIssuesFn = func(ctx context.Context, _, _ string) ([]*gh.Issue, error) {
		call := issueListCalls.Add(1)
		if repositoryFeatureCooldownBypassed(ctx) {
			return nil, disabledErr
		}
		if call == 1 {
			close(backgroundProbeStarted)
			<-releaseBackgroundProbe
		}
		return nil, nil
	}

	syncer := NewSyncer(
		map[string]Client{"github.com": client}, database, nil,
		[]RepoRef{repo}, time.Minute, nil, nil,
	)
	syncer.now = func() time.Time { return now }
	require.True(syncer.recordRepositoryFeatureDisabled(
		repo, platform.RepositoryFeatureIssues, disabledErr,
	))
	now = now.Add(repositoryFeatureProbeInterval)

	backgroundDone := make(chan struct{})
	go func() {
		defer close(backgroundDone)
		syncer.RunOnce(t.Context())
	}()
	<-backgroundProbeStarted
	require.NoError(syncer.SyncRepoOnProvider(
		t.Context(), platform.KindGitHub, "github.com", "acme", "widget",
	))
	close(releaseBackgroundProbe)
	<-backgroundDone

	syncer.RunOnce(t.Context())
	assert.Equal(int32(2), issueListCalls.Load(),
		"the concurrent disabled result must keep the renewed cooldown")
}

func TestDisabledIssueCooldownSkipsDetailDrain(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	ctx := t.Context()
	database := openTestDB(t)
	now := time.Date(2026, 7, 21, 12, 0, 0, 0, time.UTC)
	repo := RepoRef{
		Platform: platform.KindGitHub, PlatformHost: "github.com",
		Owner: "acme", Name: "widget",
	}
	repoID, err := database.UpsertRepo(ctx, platform.DBRepoIdentity(platformRepoRef(repo)))
	require.NoError(err)
	_, err = database.UpsertIssue(ctx, &db.Issue{
		RepoID: repoID, PlatformID: 1001, Number: 1,
		URL: "https://github.com/acme/widget/issues/1", Title: "needs detail",
		Author: "ada", State: "open", CreatedAt: now, UpdatedAt: now, LastActivityAt: now,
	})
	require.NoError(err)

	client := &conditionalIssueTrackingClient{}
	syncer := NewSyncer(
		map[string]Client{"github.com": client}, database, nil, []RepoRef{repo},
		time.Minute, nil, testBudget(1000),
	)
	syncer.now = func() time.Time { return now }
	require.True(syncer.recordRepositoryFeatureDisabled(
		repo, platform.RepositoryFeatureIssues,
		platform.RepositoryFeatureDisabled(
			platform.KindGitHub, "github.com", platform.RepositoryFeatureIssues,
			errors.New("repository issues disabled"),
		),
	))

	syncer.drainDetailQueue(
		ctx, map[string]bool{"github.com": true}, syncer.TrackedRepos(),
	)

	assert.Zero(int(client.conditionalCalls.Load()))
}

func TestWrappedRawDisabledIssueResponseStopsDetailDrainAndStartsCooldown(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	ctx := t.Context()
	database := openTestDB(t)
	now := time.Date(2026, 7, 21, 12, 0, 0, 0, time.UTC)
	repo := RepoRef{
		Platform: platform.KindGitHub, PlatformHost: "github.com",
		Owner: "acme", Name: "widget",
	}
	repoID, err := database.UpsertRepo(ctx, platform.DBRepoIdentity(platformRepoRef(repo)))
	require.NoError(err)
	for _, number := range []int{1, 2} {
		_, err = database.UpsertIssue(ctx, &db.Issue{
			RepoID: repoID, PlatformID: int64(1000 + number), Number: number,
			URL:   fmt.Sprintf("https://github.com/acme/widget/issues/%d", number),
			Title: fmt.Sprintf("needs detail %d", number), Author: "ada", State: "open",
			CreatedAt: now, UpdatedAt: now, LastActivityAt: now,
		})
		require.NoError(err)
	}

	rawDisabledErr := fmt.Errorf("list issue comments: %w", &gh.ErrorResponse{
		Response: &http.Response{StatusCode: http.StatusGone},
		Message:  "Issues are disabled for this repo",
	})
	client := &mockClient{
		getIssueFn: func(_ context.Context, _, _ string, number int) (*gh.Issue, error) {
			issue := buildOpenIssue(number, now)
			platformID := int64(1000 + number)
			issue.ID = &platformID
			return issue, nil
		},
		listIssueCommentsFn: func(context.Context, string, string, int) ([]*gh.IssueComment, error) {
			return nil, rawDisabledErr
		},
	}
	syncer := NewSyncer(
		map[string]Client{"github.com": client}, database, nil,
		[]RepoRef{repo}, time.Minute, nil, testBudget(1000),
	)
	syncer.now = func() time.Time { return now }

	syncer.drainDetailQueue(
		ctx, map[string]bool{"github.com": true}, syncer.TrackedRepos(),
	)

	assert.Equal(int32(1), client.listIssueCommentsCalled.Load())
	_, due := syncer.beginRepositoryFeatureProbe(ctx, repo, platform.RepositoryFeatureIssues)
	assert.False(due)
}

func TestDisabledPRCooldownDoesNotExhaustIssueDetailBudget(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	ctx := t.Context()
	database := openTestDB(t)
	now := time.Date(2026, 7, 21, 12, 0, 0, 0, time.UTC)
	repo := RepoRef{
		Platform: platform.KindGitHub, PlatformHost: "github.com",
		Owner: "acme", Name: "widget",
	}
	repoID, err := database.UpsertRepo(ctx, platform.DBRepoIdentity(platformRepoRef(repo)))
	require.NoError(err)
	_, err = database.UpsertMergeRequest(ctx, &db.MergeRequest{
		RepoID: repoID, PlatformID: 1001, Number: 7,
		URL: "https://github.com/acme/widget/pull/7", Title: "cooled PR",
		Author: "ada", State: "open", HeadBranch: "feature", BaseBranch: "main",
		CreatedAt: now, UpdatedAt: now, LastActivityAt: now,
	})
	require.NoError(err)
	_, err = database.UpsertIssue(ctx, &db.Issue{
		RepoID: repoID, PlatformID: 2001, Number: 8,
		URL: "https://github.com/acme/widget/issues/8", Title: "eligible issue",
		Author: "ada", State: "open", CreatedAt: now.Add(-time.Hour),
		UpdatedAt: now.Add(-time.Hour), LastActivityAt: now.Add(-time.Hour),
	})
	require.NoError(err)

	var issueCalls atomic.Int32
	client := &detailTrackingClient{}
	client.getIssueFn = func(context.Context, string, string, int) (*gh.Issue, error) {
		issueCalls.Add(1)
		return &gh.Issue{
			ID: new(int64(2001)), Number: new(8), Title: new("eligible issue"),
			State: new("open"), HTMLURL: new("https://github.com/acme/widget/issues/8"),
			User: &gh.User{Login: new("ada")}, CreatedAt: &gh.Timestamp{Time: now},
			UpdatedAt: &gh.Timestamp{Time: now},
		}, nil
	}
	budget := testBudget(IssueDetailWorstCase)
	client.budget = budget["github.com"]
	syncer := NewSyncer(
		map[string]Client{"github.com": client}, database, nil,
		[]RepoRef{repo}, time.Minute, nil, budget,
	)
	syncer.now = func() time.Time { return now }
	require.True(syncer.recordRepositoryFeatureDisabled(
		repo, platform.RepositoryFeatureMergeRequests,
		platform.RepositoryFeatureDisabled(
			platform.KindGitHub, "github.com", platform.RepositoryFeatureMergeRequests,
			errors.New("repository pull requests disabled"),
		),
	))

	syncer.drainDetailQueue(
		ctx, map[string]bool{"github.com": true}, syncer.TrackedRepos(),
	)

	assert.Zero(int(client.getPRCalls.Load()))
	assert.Equal(int32(1), issueCalls.Load())
}

func TestDisabledMergeRequestCooldownSkipsWatchedSync(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	database := openTestDB(t)
	now := time.Date(2026, 7, 21, 12, 0, 0, 0, time.UTC)
	repo := RepoRef{
		Platform: platform.KindGitHub, PlatformHost: "github.com",
		Owner: "acme", Name: "widget",
	}
	client := &detailTrackingClient{}
	client.singlePR = buildOpenPR(7, now)
	client.comments = []*gh.IssueComment{}
	client.reviews = []*gh.PullRequestReview{}
	client.commits = []*gh.RepositoryCommit{}
	syncer := NewSyncer(
		map[string]Client{"github.com": client}, database, nil,
		[]RepoRef{repo}, time.Minute, nil, nil,
	)
	syncer.now = func() time.Time { return now }
	syncer.SetWatchedMRs([]WatchedMR{{
		Platform: platform.KindGitHub, PlatformHost: "github.com",
		Owner: "acme", Name: "widget", Number: 7,
	}})
	require.True(syncer.recordRepositoryFeatureDisabled(
		repo, platform.RepositoryFeatureMergeRequests,
		platform.RepositoryFeatureDisabled(
			platform.KindGitHub, "github.com", platform.RepositoryFeatureMergeRequests,
			errors.New("repository pull requests disabled"),
		),
	))

	syncer.syncWatchedMRs(t.Context())

	assert.Zero(int(client.getPRCalls.Load()))
}

func TestDisabledIssueCooldownSkipsQueuedComments(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	ctx := t.Context()
	database := openTestDB(t)
	now := time.Date(2026, 7, 21, 12, 0, 0, 0, time.UTC)
	repo := RepoRef{
		Platform: platform.KindGitHub, PlatformHost: "github.com",
		Owner: "acme", Name: "widget",
	}
	repoID, err := database.UpsertRepo(ctx, platform.DBRepoIdentity(platformRepoRef(repo)))
	require.NoError(err)
	_, err = database.UpsertIssue(ctx, &db.Issue{
		RepoID: repoID, PlatformID: 1001, Number: 1,
		URL: "https://github.com/acme/widget/issues/1", Title: "needs comments",
		Author: "ada", State: "open", CreatedAt: now, UpdatedAt: now, LastActivityAt: now,
		DetailFetchedAt: &now,
	})
	require.NoError(err)

	var commentCalls atomic.Int32
	client := &mockClient{
		comments: []*gh.IssueComment{},
		listIssueCommentsFn: func(context.Context, string, string, int) ([]*gh.IssueComment, error) {
			commentCalls.Add(1)
			return []*gh.IssueComment{}, nil
		},
	}
	syncer := NewSyncer(
		map[string]Client{"github.com": client}, database, nil,
		[]RepoRef{repo}, time.Minute, nil, nil,
	)
	syncer.now = func() time.Time { return now }
	require.True(syncer.recordRepositoryFeatureDisabled(
		repo, platform.RepositoryFeatureIssues,
		platform.RepositoryFeatureDisabled(
			platform.KindGitHub, "github.com", platform.RepositoryFeatureIssues,
			errors.New("repository issues disabled"),
		),
	))
	syncer.queueIssueCommentSync(repo, 1)

	syncer.drainPendingCommentSyncs(ctx, map[string]bool{"github.com": true})

	assert.Zero(int(commentCalls.Load()))
}

func TestExpiredIssueCommentProbeRenewsDisabledCooldown(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	ctx := t.Context()
	database := openTestDB(t)
	now := time.Date(2026, 7, 21, 12, 0, 0, 0, time.UTC)
	repo := RepoRef{
		Platform: platform.KindGitHub, PlatformHost: "github.com",
		Owner: "acme", Name: "widget",
	}
	repoID, err := database.UpsertRepo(ctx, platform.DBRepoIdentity(platformRepoRef(repo)))
	require.NoError(err)
	_, err = database.UpsertIssue(ctx, &db.Issue{
		RepoID: repoID, PlatformID: 1001, Number: 1,
		URL: "https://github.com/acme/widget/issues/1", Title: "needs comments",
		Author: "ada", State: "open", CreatedAt: now, UpdatedAt: now, LastActivityAt: now,
		DetailFetchedAt: &now,
	})
	require.NoError(err)
	disabledErr := platform.RepositoryFeatureDisabled(
		platform.KindGitHub, "github.com", platform.RepositoryFeatureIssues,
		errors.New("repository issues disabled"),
	)
	rawDisabledErr := fmt.Errorf("list issue comments: %w", &gh.ErrorResponse{
		Response: &http.Response{StatusCode: http.StatusGone},
		Message:  "Issues are disabled for this repo",
	})

	var commentCalls atomic.Int32
	client := &mockClient{
		comments: []*gh.IssueComment{},
		listIssueCommentsFn: func(context.Context, string, string, int) ([]*gh.IssueComment, error) {
			commentCalls.Add(1)
			return nil, rawDisabledErr
		},
	}
	syncer := NewSyncer(
		map[string]Client{"github.com": client}, database, nil,
		[]RepoRef{repo}, time.Minute, nil, nil,
	)
	syncer.now = func() time.Time { return now }
	require.True(syncer.recordRepositoryFeatureDisabled(
		repo, platform.RepositoryFeatureIssues, disabledErr,
	))
	now = now.Add(repositoryFeatureProbeInterval)

	syncer.queueIssueCommentSync(repo, 1)
	syncer.drainPendingCommentSyncs(ctx, map[string]bool{"github.com": true})
	syncer.queueIssueCommentSync(repo, 1)
	syncer.drainPendingCommentSyncs(ctx, map[string]bool{"github.com": true})

	assert.Equal(int32(1), commentCalls.Load())
}

func TestDisabledIssueCooldownDoesNotCrossProviderHostOrScope(t *testing.T) {
	assert := assert.New(t)
	now := time.Date(2026, 7, 21, 12, 0, 0, 0, time.UTC)
	syncer := &Syncer{now: func() time.Time { return now }}
	disabled := RepoRef{
		Platform: platform.KindGitHub, PlatformHost: "github.com",
		Owner: "acme", Name: "widget",
	}
	assert.True(syncer.recordRepositoryFeatureDisabled(
		disabled, platform.RepositoryFeatureIssues,
		platform.RepositoryFeatureDisabled(
			platform.KindGitHub, "github.com", platform.RepositoryFeatureIssues,
			errors.New("repository issues disabled"),
		),
	))

	_, due := syncer.beginRepositoryFeatureProbe(
		t.Context(), disabled, platform.RepositoryFeatureIssues,
	)
	assert.False(due)
	_, due = syncer.beginRepositoryFeatureProbe(
		t.Context(), disabled, platform.RepositoryFeatureMergeRequests,
	)
	assert.True(due)
	_, due = syncer.beginRepositoryFeatureProbe(t.Context(), RepoRef{
		Platform: platform.KindGitHub, PlatformHost: "ghe.example.com",
		Owner: "acme", Name: "widget",
	}, platform.RepositoryFeatureIssues)
	assert.True(due)
	_, due = syncer.beginRepositoryFeatureProbe(t.Context(), RepoRef{
		Platform: platform.KindGitLab, PlatformHost: "github.com",
		Owner: "acme", Name: "widget",
	}, platform.RepositoryFeatureIssues)
	assert.True(due)
	_, due = syncer.beginRepositoryFeatureProbe(t.Context(), RepoRef{
		Platform: platform.KindGitHub, PlatformHost: "github.com",
		Owner: "acme", Name: "other-widget",
	}, platform.RepositoryFeatureIssues)
	assert.True(due)
}
