package github

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	gh "github.com/google/go-github/v92/github"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/forge/internal/db"
	"go.kenn.io/forge/internal/gitclone"
	"go.kenn.io/forge/internal/testutil/gitfixture"
	"go.kenn.io/forge/internal/testutil/reposeed"
	"go.kenn.io/forge/platform"
)

func TestPageSyncMRRefreshesChildrenOnParent304(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	ctx := t.Context()
	d := openTestDB(t)
	remote := filepath.Join(gitfixture.ScratchDir(t), "remote.git")
	gitfixture.Run(t, filepath.Dir(remote), "init", "--bare", "--initial-branch=main", remote)
	work := filepath.Join(gitfixture.ScratchDir(t), "work")
	syncActivityGitRun(t, filepath.Dir(work), "clone", remote, work)
	syncActivityGitRun(t, work, "config", "user.email", "fixture@example.com")
	syncActivityGitRun(t, work, "config", "user.name", "Fixture")
	base := syncActivityCommitAndPush(t, work, "base.txt", "base", "base", "main")
	head := syncActivityCommitAndPush(t, work, "feature.txt", "feature", "feature", "feature")
	repo := RepoRef{Owner: "owner", Name: "repo", PlatformHost: "github.com", CloneURL: remote, Key: platform.RepositoryIDKey(testRepoID("owner", "repo"))}
	repoID, err := reposeed.Seed(ctx, d, verifiedGitHubRepoIdentity("github.com", repo.Owner, repo.Name))
	require.NoError(err)
	mc := &conditionalPRTrackingClient{nextETag: `"page-v1"`}
	mc.singlePR = buildOpenPR(1, time.Now().UTC())
	mc.singlePR.Head.SHA, mc.singlePR.Base.SHA = &head, &base
	mc.checkRuns = []*gh.CheckRun{{Name: new("tests"), Status: new("completed"), Conclusion: new("success")}}
	clones := gitclone.New(gitfixture.ScratchDir(t), nil)
	t.Cleanup(clones.Wait)
	syncer := NewSyncer(map[string]Client{"github.com": mc}, d, clones, []RepoRef{repo}, time.Minute, nil, nil)
	require.NoError(syncer.ensureClone(withCloneRepositoryIdentity(ctx, repo), repo))
	_, err = syncer.fetchMRDetail(ctx, repo, repoID, 1, true)
	require.NoError(err)
	before, err := d.GetMergeRequestByRepoIDAndNumber(ctx, repoID, 1)
	require.NoError(err)
	require.Equal("success", before.CIStatus)
	require.False(before.CIHadPending)
	baseline := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	_, err = d.WriteDB().ExecContext(ctx, "UPDATE forge_merge_requests SET detail_fetched_at = ? WHERE id = ?", baseline, before.ID)
	require.NoError(err)
	// Complete local diffs must survive repeated polls without remote Git access.
	require.NoError(os.Rename(remote, remote+".unavailable"))
	mc.conditionalCalls.Store(0)
	mc.notModified = true
	mc.reviews = []*gh.PullRequestReview{{ID: new(int64(5)), User: &gh.User{Login: new("alice")}, State: new("APPROVED")}}
	mc.comments = []*gh.IssueComment{{ID: new(int64(6)), CreatedAt: mc.singlePR.UpdatedAt}}
	mc.checkRuns[0].Conclusion = new("failure")
	mc.workflowRuns = []*gh.WorkflowRun{{ID: new(int64(7)), HeadSHA: &head, Event: new("pull_request"), PullRequests: []*gh.PullRequest{{Number: new(1)}}}}
	mc.commits = []*gh.RepositoryCommit{{SHA: &head}}
	synced := 0
	syncer.SetOnMRSynced(func(_, _ string, _ *db.MergeRequest) { synced++ })
	for _, body := range []string{"New comment", "Edited comment", "Edited comment", "Edited again", "Edited again"} {
		_, err = d.WriteDB().ExecContext(ctx, "UPDATE forge_merge_requests SET detail_fetched_at = ? WHERE id = ?", baseline, before.ID)
		require.NoError(err)
		mc.comments[0].Body = &body
		require.NoError(syncer.SyncMROnProviderIfChanged(ctx, platform.KindGitHub, "github.com", "owner", "repo", 1))
		events, err := d.ListMREvents(ctx, before.ID)
		require.NoError(err)
		require.Len(events, 3)
		var bodies []string
		for _, event := range events {
			if event.EventType == "issue_comment" {
				bodies = append(bodies, event.Body)
			}
		}
		assert.Equal([]string{body}, bodies)
		stored, err := d.GetMergeRequestByRepoIDAndNumber(ctx, repoID, 1)
		require.NoError(err)
		assert.Equal("failure", stored.CIStatus)
		assert.Equal("approved", stored.ReviewDecision)
		assert.True(stored.WorkflowApprovalRequired)
		assert.True(stored.DetailFetchedAt.After(baseline))
	}
	assert.Equal(`"page-v1"`, mc.receivedETag)
	assert.Equal(int32(5), mc.conditionalCalls.Load())
	assert.Equal(int32(1), mc.getPRCalls.Load(), "only hydration downloads the PR")
	assert.Zero(mc.listIssueCommentsIfChangedCalls.Load())
	assert.Equal(int32(6), mc.listIssueCommentsCalled.Load())
	assert.Equal(5, synced)

	// Child failures leave detail freshness unchanged on a 304.
	_, err = d.WriteDB().ExecContext(ctx, "UPDATE forge_merge_requests SET detail_fetched_at = ? WHERE id = ?", baseline, before.ID)
	require.NoError(err)
	fresh, err := d.GetMergeRequestByRepoIDAndNumber(ctx, repoID, 1)
	require.NoError(err)
	mc.listIssueCommentsErr = errors.New("comments unavailable")
	require.ErrorContains(syncer.SyncMROnProviderIfChanged(ctx, platform.KindGitHub, "github.com", "owner", "repo", 1), "comments unavailable")
	failed, err := d.GetMergeRequestByRepoIDAndNumber(ctx, repoID, 1)
	require.NoError(err)
	assert.Equal(fresh.DetailFetchedAt, failed.DetailFetchedAt)
	mc.listIssueCommentsErr = nil

	// Stale SHAs force the full path and recover through the existing diff sync.
	require.NoError(os.Rename(remote+".unavailable", remote))
	require.NoError(d.UpdateDiffSHAs(ctx, repoID, 1, "", "", ""))
	mc.conditionalCalls.Store(0)
	require.NoError(syncer.SyncMROnProviderIfChanged(ctx, platform.KindGitHub, "github.com", "owner", "repo", 1))
	assert.Zero(mc.conditionalCalls.Load())
	diff, err := d.GetDiffSHAsByRepoID(ctx, repoID, 1)
	require.NoError(err)
	assert.Equal(head, diff.DiffHeadSHA)
	assert.Equal(base, diff.DiffBaseSHA)
	assert.Equal(base, diff.MergeBaseSHA)

	// A page left open after merging also skips unchanged parent and diff work.
	_, err = d.WriteDB().ExecContext(ctx, "UPDATE forge_merge_requests SET state = 'merged' WHERE id = ?", before.ID)
	require.NoError(err)
	require.NoError(os.Rename(remote, remote+".unavailable"))
	require.NoError(syncer.SyncMROnProviderIfChanged(ctx, platform.KindGitHub, "github.com", "owner", "repo", 1))
	assert.Equal(int32(1), mc.conditionalCalls.Load())
}

func TestPageSyncMRPersistsChangedParentResponse(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	ctx := t.Context()
	d := openTestDB(t)
	repo := RepoRef{Owner: "owner", Name: "repo", PlatformHost: "github.com"}
	repoID, err := reposeed.Seed(ctx, d, verifiedGitHubRepoIdentity("github.com", "owner", "repo"))
	require.NoError(err)
	mc := &conditionalPRTrackingClient{nextETag: `"page-v1"`}
	mc.singlePR = buildOpenPR(1, time.Now().UTC())
	syncer := NewSyncer(map[string]Client{"github.com": mc}, d, nil, []RepoRef{repo}, time.Minute, nil, nil)
	_, err = syncer.fetchMRDetail(ctx, repo, repoID, 1, false)
	require.NoError(err)
	mc.conditionalCalls.Store(0)
	mc.singlePR.Title = new("Updated parent")
	mc.singlePR.UpdatedAt = makeTimestamp(mc.singlePR.UpdatedAt.Add(time.Minute))
	mc.nextETag = `"page-v2"`
	require.NoError(syncer.SyncMROnProviderIfChanged(ctx, platform.KindGitHub, "github.com", "owner", "repo", 1))
	stored, err := d.GetMergeRequestByRepoIDAndNumber(ctx, repoID, 1)
	require.NoError(err)
	assert.Equal("Updated parent", stored.Title)
	etag, err := d.GetHTTPEtag(ctx, "github", "github.com", "owner", "repo", "pull_request", 1)
	require.NoError(err)
	assert.Equal(`"page-v2"`, etag)
	assert.Equal(`"page-v1"`, mc.receivedETag)
	assert.Equal(int32(1), mc.conditionalCalls.Load())
	mc.notModified = true
	mc.conditionalCalls.Store(0)
	require.NoError(syncer.SyncMROnProvider(ctx, platform.KindGitHub, "github.com", "owner", "repo", 1))
	assert.Zero(mc.conditionalCalls.Load(), "manual refresh remains unconditional")
	assert.Equal(int32(3), mc.getPRCalls.Load())
}
