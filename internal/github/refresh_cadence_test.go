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

	syncer.queueRepoPRComments(ctx, repo)
	syncer.queueRepoIssueComments(ctx, repo)
	syncer.queuePRCommentSync(repo, repoID, 1, &fetched)
	syncer.queueIssueCommentSync(repo, repoID, 2, &fetched)
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

func TestSyncChecksCommentsOnceAfterUnchangedDetail(t *testing.T) {
	for _, kind := range []string{"pull request", "issue"} {
		for _, listUnchanged := range []bool{false, true} {
			listResult := map[bool]string{false: "list 200", true: "list 304"}[listUnchanged]
			t.Run(kind+"/"+listResult, func(t *testing.T) {
				require := require.New(t)
				ctx := t.Context()
				d := openTestDB(t)
				repo := RepoRef{Owner: "acme", Name: "widgets", PlatformHost: "github.com"}
				repoID, err := d.UpsertRepo(ctx, verifiedGitHubRepoIdentity("github.com", repo.Owner, repo.Name))
				require.NoError(err)
				now := time.Now().UTC().Truncate(time.Second)
				updated := now.Add(-2 * time.Hour)
				var client Client
				var mock *mockClient
				var id int64
				if kind == "pull request" {
					id, err = d.UpsertMergeRequest(ctx, &db.MergeRequest{
						RepoID: repoID, Number: 1, PlatformID: 101, State: "open", Title: "Active PR",
						CreatedAt: updated, UpdatedAt: updated, LastActivityAt: updated, DetailFetchedAt: &updated,
					})
					require.NoError(err)
					prClient := &conditionalPRTrackingClient{notModified: true}
					mock = &prClient.mockClient
					client = prClient
					mock.openPRs = []*gh.PullRequest{{ID: new(int64(101)), Number: new(1),
						Title: new("Active PR"), State: new("open"), CreatedAt: makeTimestamp(updated),
						UpdatedAt: makeTimestamp(updated)}}
					if listUnchanged {
						mock.listOpenPRsErr = notModifiedErr()
					}
				} else {
					id, err = d.UpsertIssue(ctx, &db.Issue{
						RepoID: repoID, Number: 1, PlatformID: 101, State: "open", Title: "Active issue",
						CreatedAt: updated, UpdatedAt: updated, LastActivityAt: updated, DetailFetchedAt: &updated,
					})
					require.NoError(err)
					issueClient := &conditionalIssueTrackingClient{notModified: true}
					mock = &issueClient.mockClient
					client = issueClient
					mock.openIssues = []*gh.Issue{{ID: new(int64(101)), Number: new(1),
						Title: new("Active issue"), State: new("open"), CreatedAt: makeTimestamp(updated),
						UpdatedAt: makeTimestamp(updated)}}
					if listUnchanged {
						mock.listOpenIssuesErr = notModifiedErr()
					}
				}
				mock.comments = []*gh.IssueComment{{ID: new(int64(5)), Body: new("edited comment"),
					CreatedAt: makeTimestamp(updated), UpdatedAt: makeTimestamp(now)}}
				syncer := NewSyncer(map[string]Client{"github.com": client}, d, nil,
					[]RepoRef{repo}, time.Minute, nil, testBudget(1000))

				syncer.RunOnce(ctx)

				assert.Equal(t, int32(1), mock.listIssueCommentsCalled.Load(), "comments must be fetched once per cycle")
				if kind == "pull request" {
					events, err := d.ListMREvents(ctx, id)
					require.NoError(err)
					require.Len(events, 1)
					assert.Equal(t, "edited comment", events[0].Body)
				} else {
					events, err := d.ListIssueEvents(ctx, id)
					require.NoError(err)
					require.Len(events, 1)
					assert.Equal(t, "edited comment", events[0].Body)
				}
			})
		}
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
