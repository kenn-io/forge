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
	for _, childFailure := range []bool{false, true} {
		name := "refreshes children"
		if childFailure {
			name = "child failure keeps freshness"
		}
		t.Run(name, func(t *testing.T) {
			assert := assert.New(t)
			require := require.New(t)
			ctx := t.Context()
			f := newPageSyncFixture(t)

			require.NoError(os.Rename(f.repo.CloneURL, filepath.Join(gitfixture.ScratchDir(t), "unavailable.git")))
			if childFailure {
				f.client.listIssueCommentsErr = errors.New("comments unavailable")
				require.ErrorContains(f.syncer.SyncMROnProviderIfChanged(ctx, platform.KindGitHub, "github.com", "owner", "repo", 1), "comments unavailable")
				failed, err := f.syncer.db.GetMergeRequestByRepoIDAndNumber(ctx, f.repoID, 1)
				require.NoError(err)
				assert.Equal(f.baseline, *failed.DetailFetchedAt)
			} else {
				f.client.reviews = []*gh.PullRequestReview{{ID: new(int64(5)), User: &gh.User{Login: new("alice")}, State: new("APPROVED")}}
				f.client.comments = []*gh.IssueComment{{ID: new(int64(6)), Body: new("New comment"), CreatedAt: f.client.singlePR.UpdatedAt}}
				f.client.checkRuns[0].Conclusion = new("failure")
				f.client.workflowRuns = []*gh.WorkflowRun{{ID: new(int64(7)), HeadSHA: &f.head, Event: new("pull_request"), PullRequests: []*gh.PullRequest{{Number: new(1)}}}}
				f.client.commits = []*gh.RepositoryCommit{{SHA: &f.head}}
				require.NoError(f.syncer.SyncMROnProviderIfChanged(ctx, platform.KindGitHub, "github.com", "owner", "repo", 1))
				events, err := f.syncer.db.ListMREvents(ctx, f.before.ID)
				require.NoError(err)
				var bodies []string
				for _, event := range events {
					if event.EventType == "issue_comment" {
						bodies = append(bodies, event.Body)
					}
				}
				assert.Equal([]string{"New comment"}, bodies)
				stored, err := f.syncer.db.GetMergeRequestByRepoIDAndNumber(ctx, f.repoID, 1)
				require.NoError(err)
				assert.Equal("failure", stored.CIStatus)
				assert.Equal("approved", stored.ReviewDecision)
				assert.True(stored.WorkflowApprovalRequired)
				assert.True(stored.DetailFetchedAt.After(f.baseline))
			}
			assert.Equal(`"page-v1"`, f.client.receivedETag)
			assert.Zero(f.client.getPRCalls.Load(), "unchanged page polls do not download the PR")
		})
	}
}

func TestPageSyncMRRepairsStaleSHAs(t *testing.T) {
	for _, name := range []string{"stale SHAs", "missing clone", "merged", "closed unmerged"} {
		t.Run(name, func(t *testing.T) {
			assert := assert.New(t)
			require := require.New(t)
			ctx := t.Context()
			f := newPageSyncFixture(t)
			f.client.notModified = false

			switch name {
			case "stale SHAs":
				require.NoError(f.syncer.db.UpdateDiffSHAs(ctx, f.repoID, 1, "", "", ""))
			case "missing clone":
				clonePath, err := f.syncer.clones.ClonePathForContext(withCloneRepositoryIdentity(ctx, f.repo), "github", "github.com", "owner", "repo")
				require.NoError(err)
				require.NoError(os.RemoveAll(clonePath))
			case "merged", "closed unmerged":
				f.client.singlePR.State = new("closed")
				if name == "merged" {
					syncActivityGitRun(t, f.work, "checkout", "-B", "main", f.base)
					syncActivityGitRun(t, f.work, "merge", "--no-ff", f.head, "-m", "Merge feature")
					merge := syncActivityGitRun(t, f.work, "rev-parse", "HEAD")
					syncActivityGitRun(t, f.work, "push", "origin", "main", f.head+":refs/pull/1/head")
					f.client.singlePR.Merged = new(true)
					f.client.singlePR.MergedAt = makeTimestamp(time.Now().UTC())
					f.client.singlePR.MergeCommitSHA = &merge
					f.client.singlePR.Base.SHA = &merge
				}
				f.client.nextETag = `"merged-v1"`
				require.NoError(f.syncer.ensureClone(withCloneRepositoryIdentity(ctx, f.repo), f.repo))
				_, err := f.syncer.fetchMRDetail(ctx, f.repo, f.repoID, 1, true)
				require.NoError(err)
			}
			f.client.receivedETag = ""
			f.client.getPRCalls.Store(0)
			require.NoError(f.syncer.SyncMROnProviderIfChanged(ctx, platform.KindGitHub, "github.com", "owner", "repo", 1))
			assert.Empty(f.client.receivedETag, "unsafe page polls send no If-None-Match")
			assert.Positive(f.client.getPRCalls.Load())
			diff, err := f.syncer.db.GetDiffSHAsByRepoID(ctx, f.repoID, 1)
			require.NoError(err)
			assert.Equal(f.head, diff.DiffHeadSHA)
			assert.Equal(f.base, diff.DiffBaseSHA)
			assert.Equal(f.base, diff.MergeBaseSHA)
			recoveredDiff, err := f.syncer.clones.Diff(withCloneRepositoryIdentity(ctx, f.repo), "github", "github.com", "owner", "repo", diff.MergeBaseSHA, diff.DiffHeadSHA, false)
			require.NoError(err)
			require.Len(recoveredDiff.Files, 1)
			assert.Equal("feature.txt", recoveredDiff.Files[0].Path)
		})
	}
}

func TestPageSyncMRStoresETagOnColdPageFetch(t *testing.T) {
	for _, existingParent := range []bool{true, false} {
		name := "existing parent"
		if !existingParent {
			name = "missing parent"
		}
		t.Run(name, func(t *testing.T) {
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
			if existingParent {
				normalized, err := NormalizePR(repoID, mc.singlePR)
				require.NoError(err)
				_, err = d.UpsertMergeRequest(ctx, normalized)
				require.NoError(err)
			}
			clones := gitclone.New(gitfixture.ScratchDir(t), nil)
			t.Cleanup(clones.Wait)
			syncer := NewSyncer(map[string]Client{"github.com": mc}, d, clones, []RepoRef{repo}, time.Minute, nil, nil)
			require.NoError(syncer.ensureClone(withCloneRepositoryIdentity(ctx, repo), repo))
			before, err := d.GetMergeRequestByRepoIDAndNumber(ctx, repoID, 1)
			require.NoError(err)
			if existingParent {
				require.NotNil(before)
				require.Empty(before.DiffHeadSHA)
			} else {
				require.Nil(before)
			}
			etag, err := d.GetHTTPEtag(ctx, "github", "github.com", "owner", "repo", "pull_request", 1)
			require.NoError(err)
			require.Empty(etag)

			require.NoError(syncer.SyncMROnProviderIfChanged(ctx, platform.KindGitHub, "github.com", "owner", "repo", 1))
			assert.Empty(mc.receivedETag)
			stored, err := d.GetMergeRequestByRepoIDAndNumber(ctx, repoID, 1)
			require.NoError(err)
			require.NotNil(stored)
			assert.Equal(mc.singlePR.GetTitle(), stored.Title)
			assert.NotNil(stored.DetailFetchedAt)
			require.Equal(int32(1), mc.getPRCalls.Load())
			etag, err = d.GetHTTPEtag(ctx, "github", "github.com", "owner", "repo", "pull_request", 1)
			require.NoError(err)
			require.Equal(`"page-v1"`, etag)
			mc.notModified = true
			require.NoError(syncer.SyncMROnProviderIfChanged(ctx, platform.KindGitHub, "github.com", "owner", "repo", 1))
			assert.Equal(`"page-v1"`, mc.receivedETag)
			assert.Equal(int32(2), mc.conditionalCalls.Load())
			assert.Equal(int32(1), mc.getPRCalls.Load(), "the second page poll gets a 304")
		})
	}
}
