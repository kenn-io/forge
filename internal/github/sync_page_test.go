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

func pageSyncGitFixture(t *testing.T) (remote, work, base, head string) {
	t.Helper()
	fixture := gitfixture.NewRepository(t, true)
	remote, work = fixture.Remote, fixture.Dir
	base = syncActivityCommitAndPush(t, work, "base.txt", "base", "base", "main")
	head = syncActivityCommitAndPush(t, work, "feature.txt", "feature", "feature", "feature")
	return remote, work, base, head
}

type pageSyncFixture struct {
	syncer   *Syncer
	client   *conditionalPRTrackingClient
	repo     RepoRef
	repoID   int64
	work     string
	base     string
	head     string
	before   *db.MergeRequest
	baseline time.Time
}

func newPageSyncFixture(t *testing.T) pageSyncFixture {
	t.Helper()
	require := require.New(t)
	ctx := t.Context()
	d := openTestDB(t)
	remote, work, base, head := pageSyncGitFixture(t)
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

	mc.getPRCalls.Store(0)
	mc.notModified = true
	return pageSyncFixture{syncer, mc, repo, repoID, work, base, head, before, baseline}
}

func TestPageSyncMRRefreshesChildrenOnParent304(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	ctx := t.Context()
	f := newPageSyncFixture(t)

	require.NoError(os.Rename(f.repo.CloneURL, filepath.Join(gitfixture.ScratchDir(t), "unavailable.git")))
	f.client.reviews = []*gh.PullRequestReview{{ID: new(int64(5)), User: &gh.User{Login: new("alice")}, State: new("APPROVED")}}
	f.client.comments = []*gh.IssueComment{{ID: new(int64(6)), CreatedAt: f.client.singlePR.UpdatedAt}}
	f.client.checkRuns[0].Conclusion = new("failure")
	f.client.workflowRuns = []*gh.WorkflowRun{{ID: new(int64(7)), HeadSHA: &f.head, Event: new("pull_request"), PullRequests: []*gh.PullRequest{{Number: new(1)}}}}
	f.client.commits = []*gh.RepositoryCommit{{SHA: &f.head}}
	for _, body := range []string{"New comment", "Edited comment"} {
		_, err := f.syncer.db.WriteDB().ExecContext(ctx, "UPDATE forge_merge_requests SET detail_fetched_at = ? WHERE id = ?", f.baseline, f.before.ID)
		require.NoError(err)
		f.client.comments[0].Body = &body
		require.NoError(f.syncer.SyncMROnProviderIfChanged(ctx, platform.KindGitHub, "github.com", "owner", "repo", 1))
		events, err := f.syncer.db.ListMREvents(ctx, f.before.ID)
		require.NoError(err)
		var bodies []string
		for _, event := range events {
			if event.EventType == "issue_comment" {
				bodies = append(bodies, event.Body)
			}
		}
		assert.Equal([]string{body}, bodies)
		stored, err := f.syncer.db.GetMergeRequestByRepoIDAndNumber(ctx, f.repoID, 1)
		require.NoError(err)
		assert.Equal("failure", stored.CIStatus)
		assert.Equal("approved", stored.ReviewDecision)
		assert.True(stored.WorkflowApprovalRequired)
		assert.True(stored.DetailFetchedAt.After(f.baseline))
	}
	assert.Equal(`"page-v1"`, f.client.receivedETag)
	assert.Zero(f.client.getPRCalls.Load(), "unchanged page polls do not download the PR")
}

func TestPageSyncMRChildFailureKeepsFreshness(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	ctx := t.Context()
	f := newPageSyncFixture(t)

	f.client.listIssueCommentsErr = errors.New("comments unavailable")
	require.ErrorContains(f.syncer.SyncMROnProviderIfChanged(ctx, platform.KindGitHub, "github.com", "owner", "repo", 1), "comments unavailable")
	failed, err := f.syncer.db.GetMergeRequestByRepoIDAndNumber(ctx, f.repoID, 1)
	require.NoError(err)
	assert.Equal(f.baseline, *failed.DetailFetchedAt)
	assert.Zero(f.client.getPRCalls.Load())
}

func TestPageSyncMRRepairsStaleSHAs(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	ctx := t.Context()
	f := newPageSyncFixture(t)
	f.client.notModified = false

	require.NoError(f.syncer.db.UpdateDiffSHAs(ctx, f.repoID, 1, "", "", ""))
	require.NoError(f.syncer.SyncMROnProviderIfChanged(ctx, platform.KindGitHub, "github.com", "owner", "repo", 1))
	diff, err := f.syncer.db.GetDiffSHAsByRepoID(ctx, f.repoID, 1)
	require.NoError(err)
	assert.Equal(f.head, diff.DiffHeadSHA)
	assert.Equal(f.base, diff.DiffBaseSHA)
	assert.Equal(f.base, diff.MergeBaseSHA)
	assert.Positive(f.client.getPRCalls.Load())
}

func TestPageSyncMRRepairsMissingClone(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	ctx := t.Context()
	f := newPageSyncFixture(t)
	f.client.notModified = false

	cloneCtx := withCloneRepositoryIdentity(ctx, f.repo)
	clonePath, err := f.syncer.clones.ClonePathForContext(cloneCtx, "github", "github.com", "owner", "repo")
	require.NoError(err)
	require.NoError(os.RemoveAll(clonePath))
	require.NoError(f.syncer.SyncMROnProviderIfChanged(ctx, platform.KindGitHub, "github.com", "owner", "repo", 1))
	recoveredDiff, err := f.syncer.clones.Diff(cloneCtx, "github", "github.com", "owner", "repo", f.base, f.head, false)
	require.NoError(err)
	require.Len(recoveredDiff.Files, 1)
	assert.Equal("feature.txt", recoveredDiff.Files[0].Path)
	assert.Positive(f.client.getPRCalls.Load())
}

func TestPageSyncMRRecomputesMergedDiff(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	ctx := t.Context()
	f := newPageSyncFixture(t)

	syncActivityGitRun(t, f.work, "checkout", "-B", "main", f.base)
	syncActivityGitRun(t, f.work, "merge", "--no-ff", f.head, "-m", "Merge feature")
	merge := syncActivityGitRun(t, f.work, "rev-parse", "HEAD")
	syncActivityGitRun(t, f.work, "push", "origin", "main", f.head+":refs/pull/1/head")
	f.client.singlePR.State = new("closed")
	f.client.singlePR.Merged = new(true)
	f.client.singlePR.MergedAt = makeTimestamp(time.Now().UTC())
	f.client.singlePR.MergeCommitSHA = &merge
	f.client.singlePR.Base.SHA = &merge
	f.client.notModified = false
	f.client.nextETag = `"merged-v1"`
	require.NoError(f.syncer.ensureClone(withCloneRepositoryIdentity(ctx, f.repo), f.repo))
	_, err := f.syncer.fetchMRDetail(ctx, f.repo, f.repoID, 1, true)
	require.NoError(err)
	diff, err := f.syncer.db.GetDiffSHAsByRepoID(ctx, f.repoID, 1)
	require.NoError(err)
	require.Equal("merged", diff.State)
	require.Equal(f.head, diff.MergeBaseSHA)
	require.Equal(f.head, diff.DiffHeadSHA)
	require.False(diff.Stale())
	etag, err := f.syncer.db.GetHTTPEtag(ctx, "github", "github.com", "owner", "repo", "pull_request", 1)
	require.NoError(err)
	require.Equal(`"merged-v1"`, etag)
	f.client.receivedETag = ""
	f.client.getPRCalls.Store(0)
	require.NoError(f.syncer.SyncMROnProviderIfChanged(ctx, platform.KindGitHub, "github.com", "owner", "repo", 1))
	assert.Empty(f.client.receivedETag, "merged page polls send no If-None-Match")
	assert.Positive(f.client.getPRCalls.Load())
	diff, err = f.syncer.db.GetDiffSHAsByRepoID(ctx, f.repoID, 1)
	require.NoError(err)
	assert.Equal(f.head, diff.DiffHeadSHA)
	assert.Equal(f.base, diff.DiffBaseSHA)
	assert.Equal(f.base, diff.MergeBaseSHA)
	recoveredDiff, err := f.syncer.clones.Diff(withCloneRepositoryIdentity(ctx, f.repo), "github", "github.com", "owner", "repo", diff.MergeBaseSHA, diff.DiffHeadSHA, false)
	require.NoError(err)
	require.Len(recoveredDiff.Files, 1)
	assert.Equal("feature.txt", recoveredDiff.Files[0].Path)
}

func TestPageSyncMRFullySyncsMissingParent(t *testing.T) {
	require := require.New(t)
	ctx := t.Context()
	d := openTestDB(t)
	repo := RepoRef{Owner: "owner", Name: "repo", PlatformHost: "github.com"}
	repoID, err := reposeed.Seed(ctx, d, verifiedGitHubRepoIdentity("github.com", "owner", "repo"))
	require.NoError(err)
	mc := &conditionalPRTrackingClient{nextETag: `"page-v1"`}
	mc.singlePR = buildOpenPR(1, time.Now().UTC())
	syncer := NewSyncer(map[string]Client{"github.com": mc}, d, nil, []RepoRef{repo}, time.Minute, nil, nil)
	require.NoError(syncer.SyncMROnProviderIfChanged(ctx, platform.KindGitHub, "github.com", "owner", "repo", 1))
	stored, err := d.GetMergeRequestByRepoIDAndNumber(ctx, repoID, 1)
	require.NoError(err)
	require.NotNil(stored)
	require.Equal(mc.singlePR.GetTitle(), stored.Title)
	require.NotNil(stored.DetailFetchedAt)
}

func TestPageSyncMRPersistsChangedParentResponse(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	ctx := t.Context()
	d := openTestDB(t)
	remote, _, base, head := pageSyncGitFixture(t)
	repo := RepoRef{Owner: "owner", Name: "repo", PlatformHost: "github.com", CloneURL: remote, Key: platform.RepositoryIDKey(testRepoID("owner", "repo"))}
	repoID, err := reposeed.Seed(ctx, d, verifiedGitHubRepoIdentity("github.com", "owner", "repo"))
	require.NoError(err)
	mc := &conditionalPRTrackingClient{nextETag: `"page-v1"`}
	mc.singlePR = buildOpenPR(1, time.Now().UTC())
	mc.singlePR.Head.SHA, mc.singlePR.Base.SHA = &head, &base
	clones := gitclone.New(gitfixture.ScratchDir(t), nil)
	t.Cleanup(clones.Wait)
	syncer := NewSyncer(map[string]Client{"github.com": mc}, d, clones, []RepoRef{repo}, time.Minute, nil, nil)
	require.NoError(syncer.ensureClone(withCloneRepositoryIdentity(ctx, repo), repo))
	_, err = syncer.fetchMRDetail(ctx, repo, repoID, 1, true)
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
}

func TestPageSyncMRStoresETagOnColdPageFetch(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	ctx := t.Context()
	d := openTestDB(t)
	remote, _, base, head := pageSyncGitFixture(t)
	repo := RepoRef{Owner: "owner", Name: "repo", PlatformHost: "github.com", CloneURL: remote, Key: platform.RepositoryIDKey(testRepoID("owner", "repo"))}
	repoID, err := reposeed.Seed(ctx, d, verifiedGitHubRepoIdentity("github.com", "owner", "repo"))
	require.NoError(err)
	mc := &conditionalPRTrackingClient{nextETag: `"page-v1"`}
	mc.singlePR = buildOpenPR(1, time.Now().UTC())
	mc.singlePR.Head.SHA, mc.singlePR.Base.SHA = &head, &base
	normalized, err := NormalizePR(repoID, mc.singlePR)
	require.NoError(err)
	_, err = d.UpsertMergeRequest(ctx, normalized)
	require.NoError(err)
	clones := gitclone.New(gitfixture.ScratchDir(t), nil)
	t.Cleanup(clones.Wait)
	syncer := NewSyncer(map[string]Client{"github.com": mc}, d, clones, []RepoRef{repo}, time.Minute, nil, nil)
	require.NoError(syncer.ensureClone(withCloneRepositoryIdentity(ctx, repo), repo))
	diff, err := d.GetDiffSHAsByRepoID(ctx, repoID, 1)
	require.NoError(err)
	require.Empty(diff.DiffHeadSHA)
	etag, err := d.GetHTTPEtag(ctx, "github", "github.com", "owner", "repo", "pull_request", 1)
	require.NoError(err)
	require.Empty(etag)

	require.NoError(syncer.SyncMROnProviderIfChanged(ctx, platform.KindGitHub, "github.com", "owner", "repo", 1))
	assert.Empty(mc.receivedETag)
	require.Equal(int32(1), mc.getPRCalls.Load())
	etag, err = d.GetHTTPEtag(ctx, "github", "github.com", "owner", "repo", "pull_request", 1)
	require.NoError(err)
	require.Equal(`"page-v1"`, etag)
	mc.notModified = true
	require.NoError(syncer.SyncMROnProviderIfChanged(ctx, platform.KindGitHub, "github.com", "owner", "repo", 1))
	assert.Equal(`"page-v1"`, mc.receivedETag)
	assert.Equal(int32(2), mc.conditionalCalls.Load())
	assert.Equal(int32(1), mc.getPRCalls.Load(), "the second page poll gets a 304")
}
