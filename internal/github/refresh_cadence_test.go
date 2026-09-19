package github

import (
	"errors"
	"testing"
	"time"

	gh "github.com/google/go-github/v91/github"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/forge/internal/db"
)

func TestDormantCommentRefreshWaitsForDailyDeadline(t *testing.T) {
	require := require.New(t)
	ctx := t.Context()
	d := openTestDB(t)
	repo := RepoRef{Owner: "acme", Name: "widgets", PlatformHost: "github.com"}
	repoID, err := d.UpsertRepo(ctx, verifiedGitHubRepoIdentity("github.com", repo.Owner, repo.Name))
	require.NoError(err)
	now := time.Now().UTC()
	updated := now.Add(-14 * 24 * time.Hour)
	fetched := now.Add(-time.Hour)
	pr := &db.MergeRequest{
		RepoID: repoID, Number: 1, PlatformID: 101, State: "open",
		Title: "Dormant PR", CreatedAt: updated, UpdatedAt: updated,
		LastActivityAt: updated, DetailFetchedAt: &fetched,
	}
	pr.ID, err = d.UpsertMergeRequest(ctx, pr)
	require.NoError(err)
	issue := &db.Issue{
		RepoID: repoID, Number: 2, PlatformID: 102, State: "open",
		Title: "Dormant issue", CreatedAt: updated, UpdatedAt: updated,
		LastActivityAt: updated, DetailFetchedAt: &fetched,
	}
	issue.ID, err = d.UpsertIssue(ctx, issue)
	require.NoError(err)
	mock := &mockClient{}
	syncer := NewSyncer(map[string]Client{"github.com": mock}, d, nil,
		[]RepoRef{repo}, time.Minute, nil, testBudget(1000))

	syncer.refreshRepoPRComments(ctx, repo)
	syncer.refreshRepoIssueComments(ctx, repo)
	syncer.queuePRCommentSync(repo, repoID, 1)
	syncer.queueIssueCommentSync(repo, repoID, 2)
	syncer.drainPendingCommentSyncs(ctx, map[string]bool{"github.com": true})
	assert.Zero(t, mock.listIssueCommentsIfChangedCalls.Load(), "neither comment path should poll dormant items hourly")
}

func TestDailyIssueCheckRefreshesCommentsEvenWhenParentIsUnchanged(t *testing.T) {
	for _, fail := range []bool{false, true} {
		t.Run(map[bool]string{false: "success", true: "failure stays overdue"}[fail], func(t *testing.T) {
			require := require.New(t)
			assert := assert.New(t)
			ctx := t.Context()
			d := openTestDB(t)
			repo := RepoRef{Owner: "acme", Name: "widgets", PlatformHost: "github.com"}
			repoID, err := d.UpsertRepo(ctx, verifiedGitHubRepoIdentity("github.com", repo.Owner, repo.Name))
			require.NoError(err)
			now := time.Now().UTC()
			fetched := now.Add(-25 * time.Hour)
			id, err := d.UpsertIssue(ctx, &db.Issue{
				RepoID: repoID, Number: 1, PlatformID: 101, State: "open", Title: "Dormant",
				CreatedAt: now.Add(-48 * time.Hour), UpdatedAt: now.Add(-48 * time.Hour),
				LastActivityAt: now.Add(-48 * time.Hour), DetailFetchedAt: &fetched,
			})
			require.NoError(err)
			mock := &conditionalIssueTrackingClient{notModified: true}
			mock.comments = []*gh.IssueComment{{ID: new(int64(5)), Body: new("changed comment"),
				CreatedAt: makeTimestamp(now.Add(-48 * time.Hour)), UpdatedAt: makeTimestamp(now)}}
			if fail {
				mock.listIssueCommentsErr = errors.New("unavailable")
			}
			syncer := NewSyncer(map[string]Client{"github.com": mock}, d, nil,
				[]RepoRef{repo}, time.Minute, nil, testBudget(1000))
			_, err = syncer.fetchIssueDetail(ctx, repo, repoID, 1)
			if fail {
				require.Error(err)
				assert.Equal(1, syncer.countOverdueDetails(ctx))
			} else {
				require.NoError(err)
				events, err := d.ListIssueEvents(ctx, id)
				require.NoError(err)
				require.Len(events, 1)
				assert.Equal("changed comment", events[0].Body)
				assert.Zero(syncer.countOverdueDetails(ctx))
			}
		})
	}
}

func TestSyncReportsDailyBacklogWhenBudgetCannotCoverOpenItems(t *testing.T) {
	ctx := t.Context()
	d := openTestDB(t)
	repo := RepoRef{Owner: "acme", Name: "widgets", PlatformHost: "github.com"}
	repoID, err := d.UpsertRepo(ctx, verifiedGitHubRepoIdentity("github.com", repo.Owner, repo.Name))
	require.NoError(t, err)
	now := time.Now().UTC()
	for number, state := range []string{"open", "closed"} {
		_, err = d.UpsertIssue(ctx, &db.Issue{
			RepoID: repoID, PlatformID: int64(number + 1), Number: number + 1, State: state,
			Title: "Old issue", CreatedAt: now.Add(-48 * time.Hour), UpdatedAt: now.Add(-48 * time.Hour),
			LastActivityAt: now.Add(-48 * time.Hour), DetailFetchedAt: new(now.Add(-25 * time.Hour)),
		})
		require.NoError(t, err)
	}
	budgets := testBudget(10)
	budgets["github.com"].Spend(10)
	mock := &mockClient{listOpenPRsErr: notModifiedErr(), listOpenIssuesErr: notModifiedErr()}
	syncer := NewSyncer(map[string]Client{"github.com": mock}, d, nil,
		[]RepoRef{repo}, time.Minute, nil, budgets)
	syncer.RunOnce(ctx)
	assert.Equal(t, 1, syncer.Status().DetailRefreshOverdue)
}
