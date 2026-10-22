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
	"go.kenn.io/forge/internal/gitclone"
	"go.kenn.io/forge/internal/testutil/gitfixture"
	"go.kenn.io/forge/internal/testutil/reposeed"
	"go.kenn.io/forge/platform"
)

func TestPageSyncMRRefreshesChildrenOnParent304(t *testing.T) {
	for _, name := range []string{"304 children refresh", "child failure keeps freshness", "stale SHAs repair", "missing clone repair", "merged page"} {
		t.Run(name, func(t *testing.T) {
			assert := assert.New(t)
			require := require.New(t)
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

			mc.getPRCalls.Store(0)
			mc.notModified = true
			switch name {
			case "304 children refresh":
				require.NoError(os.Rename(remote, remote+".unavailable"))
				mc.reviews = []*gh.PullRequestReview{{ID: new(int64(5)), User: &gh.User{Login: new("alice")}, State: new("APPROVED")}}
				mc.comments = []*gh.IssueComment{{ID: new(int64(6)), CreatedAt: mc.singlePR.UpdatedAt}}
				mc.checkRuns[0].Conclusion = new("failure")
				mc.workflowRuns = []*gh.WorkflowRun{{ID: new(int64(7)), HeadSHA: &head, Event: new("pull_request"), PullRequests: []*gh.PullRequest{{Number: new(1)}}}}
				mc.commits = []*gh.RepositoryCommit{{SHA: &head}}
				for _, body := range []string{"New comment", "Edited comment"} {
					_, err = d.WriteDB().ExecContext(ctx, "UPDATE forge_merge_requests SET detail_fetched_at = ? WHERE id = ?", baseline, before.ID)
					require.NoError(err)
					mc.comments[0].Body = &body
					require.NoError(syncer.SyncMROnProviderIfChanged(ctx, platform.KindGitHub, "github.com", "owner", "repo", 1))
					events, err := d.ListMREvents(ctx, before.ID)
					require.NoError(err)
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
				assert.Zero(mc.getPRCalls.Load(), "unchanged page polls do not download the PR")

			case "child failure keeps freshness":
				mc.listIssueCommentsErr = errors.New("comments unavailable")
				require.ErrorContains(syncer.SyncMROnProviderIfChanged(ctx, platform.KindGitHub, "github.com", "owner", "repo", 1), "comments unavailable")
				failed, err := d.GetMergeRequestByRepoIDAndNumber(ctx, repoID, 1)
				require.NoError(err)
				assert.Equal(baseline, *failed.DetailFetchedAt)
				assert.Zero(mc.getPRCalls.Load())

			case "stale SHAs repair":
				require.NoError(d.UpdateDiffSHAs(ctx, repoID, 1, "", "", ""))
				require.NoError(syncer.SyncMROnProviderIfChanged(ctx, platform.KindGitHub, "github.com", "owner", "repo", 1))
				diff, err := d.GetDiffSHAsByRepoID(ctx, repoID, 1)
				require.NoError(err)
				assert.Equal(head, diff.DiffHeadSHA)
				assert.Equal(base, diff.DiffBaseSHA)
				assert.Equal(base, diff.MergeBaseSHA)
				assert.Positive(mc.getPRCalls.Load())

			case "missing clone repair":
				cloneCtx := withCloneRepositoryIdentity(ctx, repo)
				clonePath, err := clones.ClonePathForContext(cloneCtx, "github", "github.com", "owner", "repo")
				require.NoError(err)
				require.NoError(os.RemoveAll(clonePath))
				require.NoError(syncer.SyncMROnProviderIfChanged(ctx, platform.KindGitHub, "github.com", "owner", "repo", 1))
				recoveredDiff, err := clones.Diff(cloneCtx, "github", "github.com", "owner", "repo", base, head, false)
				require.NoError(err)
				require.Len(recoveredDiff.Files, 1)
				assert.Equal("feature.txt", recoveredDiff.Files[0].Path)
				assert.Positive(mc.getPRCalls.Load())

			case "merged page":
				mergedAt := time.Now().UTC()
				_, err = d.WriteDB().ExecContext(ctx, "UPDATE forge_merge_requests SET state = 'merged', merged_at = ?, closed_at = ? WHERE id = ?", mergedAt, mergedAt, before.ID)
				require.NoError(err)
				require.NoError(os.Rename(remote, remote+".unavailable"))
				mc.comments = []*gh.IssueComment{{ID: new(int64(6)), Body: new("Comment after merging"), CreatedAt: mc.singlePR.UpdatedAt}}
				require.NoError(syncer.SyncMROnProviderIfChanged(ctx, platform.KindGitHub, "github.com", "owner", "repo", 1))
				events, err := d.ListMREvents(ctx, before.ID)
				require.NoError(err)
				var bodies []string
				for _, event := range events {
					if event.EventType == "issue_comment" {
						bodies = append(bodies, event.Body)
					}
				}
				assert.Equal([]string{"Comment after merging"}, bodies)
				assert.Zero(mc.getPRCalls.Load(), "unchanged merged page polls do not download the PR")
			}
		})
	}
}

func TestPageSyncMRFullySyncsMissingParent(t *testing.T) {
	require := require.New(t)
	ctx := t.Context()
	d := openTestDB(t)
	repo := RepoRef{Owner: "owner", Name: "repo", PlatformHost: "github.com"}
	repoID, err := reposeed.Seed(ctx, d, verifiedGitHubRepoIdentity("github.com", "owner", "repo"))
	require.NoError(err)
	mc := &conditionalPRTrackingClient{notModified: true}
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
	remote := filepath.Join(gitfixture.ScratchDir(t), "remote.git")
	gitfixture.Run(t, filepath.Dir(remote), "init", "--bare", "--initial-branch=main", remote)
	work := filepath.Join(gitfixture.ScratchDir(t), "work")
	syncActivityGitRun(t, filepath.Dir(work), "clone", remote, work)
	syncActivityGitRun(t, work, "config", "user.email", "fixture@example.com")
	syncActivityGitRun(t, work, "config", "user.name", "Fixture")
	base := syncActivityCommitAndPush(t, work, "base.txt", "base", "base", "main")
	head := syncActivityCommitAndPush(t, work, "feature.txt", "feature", "feature", "feature")
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
