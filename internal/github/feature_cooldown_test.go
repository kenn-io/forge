package github

import (
	"context"
	"errors"
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

	syncer.drainDetailQueue(ctx, map[string]bool{"github.com": true})

	assert.Zero(int(client.conditionalCalls.Load()))
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

	assert.False(syncer.repositoryFeatureDue(t.Context(), disabled, platform.RepositoryFeatureIssues))
	assert.True(syncer.repositoryFeatureDue(t.Context(), disabled, platform.RepositoryFeatureMergeRequests))
	assert.True(syncer.repositoryFeatureDue(t.Context(), RepoRef{
		Platform: platform.KindGitHub, PlatformHost: "ghe.example.com",
		Owner: "acme", Name: "widget",
	}, platform.RepositoryFeatureIssues))
	assert.True(syncer.repositoryFeatureDue(t.Context(), RepoRef{
		Platform: platform.KindGitLab, PlatformHost: "github.com",
		Owner: "acme", Name: "widget",
	}, platform.RepositoryFeatureIssues))
	assert.True(syncer.repositoryFeatureDue(t.Context(), RepoRef{
		Platform: platform.KindGitHub, PlatformHost: "github.com",
		Owner: "acme", Name: "other-widget",
	}, platform.RepositoryFeatureIssues))
}
