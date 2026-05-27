package gitea

import (
	"testing"
	"time"

	giteasdk "code.gitea.io/sdk/gitea"
	Assert "github.com/stretchr/testify/assert"
	Require "github.com/stretchr/testify/require"
)

func TestConvertGiteaSDKRecords(t *testing.T) {
	assert := Assert.New(t)
	require := Require.New(t)
	created := time.Date(2026, 5, 1, 2, 3, 4, 0, time.UTC)
	updated := created.Add(time.Hour)
	closed := created.Add(2 * time.Hour)

	repo, err := convertRepository(&giteasdk.Repository{
		ID:            1,
		Owner:         &giteasdk.User{ID: 2, UserName: "gitea", FullName: "Gitea"},
		Name:          "tea",
		FullName:      "gitea/tea",
		HTMLURL:       "https://gitea.com/gitea/tea",
		CloneURL:      "https://gitea.com/gitea/tea.git",
		DefaultBranch: "main",
		Private:       true,
		Archived:      true,
		Description:   "cli",
		AllowSquash:   true,
		AllowMerge:    true,
		AllowRebase:   true,
		Permissions:   &giteasdk.Permission{Push: false, Admin: false, Pull: true},
		Created:       created,
		Updated:       updated,
	})
	require.NoError(err)
	assert.Equal(int64(1), repo.ID)
	assert.Equal("gitea", repo.Owner.UserName)
	assert.Equal("gitea/tea", repo.FullName)
	assert.Equal("main", repo.DefaultBranch)
	assert.True(repo.Private)
	assert.True(repo.Archived)
	assert.True(repo.AllowSquash)
	assert.True(repo.AllowMerge)
	assert.True(repo.AllowRebase)
	require.NotNil(repo.CanPush)
	assert.False(*repo.CanPush)
	require.NotNil(repo.CanAdmin)
	assert.False(*repo.CanAdmin)

	mergeable := true
	pr := convertPullRequest(&giteasdk.PullRequest{
		ID:        3,
		Index:     4,
		Poster:    &giteasdk.User{UserName: "alice", FullName: "Alice"},
		Title:     "Add thing",
		Body:      "body",
		State:     giteasdk.StateType("open"),
		Draft:     true,
		IsLocked:  true,
		Comments:  5,
		Mergeable: true,
		HTMLURL:   "https://gitea.com/gitea/tea/pulls/4",
		Head: &giteasdk.PRBranchInfo{
			Ref:        "feature",
			Sha:        "abc",
			Repository: &giteasdk.Repository{CloneURL: "https://example/head.git"},
		},
		Base:    &giteasdk.PRBranchInfo{Ref: "main", Sha: "def"},
		Labels:  []*giteasdk.Label{{ID: 6, Name: "feature", Color: "00ff00", Description: "desc"}},
		Created: &created,
		Updated: &updated,
		Closed:  &closed,
		Merged:  &closed,
	}, &mergeable)
	assert.Equal(4, pr.Index)
	assert.Equal("alice", pr.User.UserName)
	assert.Equal("open", pr.State)
	assert.True(pr.IsLocked)
	assert.True(pr.Draft)
	assert.NotNil(pr.Mergeable)
	assert.True(*pr.Mergeable)
	assert.Equal("feature", pr.Head.Ref)
	assert.Equal("abc", pr.Head.SHA)
	assert.Equal("https://example/head.git", pr.Head.RepoCloneURL)
	assert.Equal("main", pr.Base.Ref)
	assert.Equal("feature", pr.Labels[0].Name)
	assert.Equal(&closed, pr.MergedAt)

	issue := convertIssue(&giteasdk.Issue{
		ID:          7,
		Index:       8,
		Poster:      &giteasdk.User{UserName: "bob"},
		Title:       "Bug",
		Body:        "issue body",
		State:       giteasdk.StateType("closed"),
		Comments:    9,
		HTMLURL:     "https://gitea.com/gitea/tea/issues/8",
		Labels:      []*giteasdk.Label{{ID: 10, Name: "bug"}},
		Assignees:   []*giteasdk.User{{ID: 20, UserName: "alice"}, nil, {ID: 21, UserName: "carol"}},
		Created:     created,
		Updated:     updated,
		Closed:      &closed,
		PullRequest: &giteasdk.PullRequestMeta{},
	})
	assert.Equal(8, issue.Index)
	assert.Equal("bob", issue.User.UserName)
	assert.True(issue.IsPullRequest)
	assert.Equal("bug", issue.Labels[0].Name)
	require.Len(issue.Assignees, 2)
	assert.Equal("alice", issue.Assignees[0].UserName)
	assert.Equal("carol", issue.Assignees[1].UserName)

	comment := convertComment(&giteasdk.Comment{ID: 11, Poster: &giteasdk.User{UserName: "carol"}, Body: "comment", Created: created, Updated: updated})
	assert.Equal("carol", comment.User.UserName)
	assert.Equal("comment", comment.Body)

	review := convertReview(&giteasdk.PullReview{ID: 12, Reviewer: &giteasdk.User{UserName: "dave"}, State: giteasdk.ReviewStateType("APPROVED"), Body: "review", Submitted: created})
	assert.Equal("APPROVED", review.State)
	assert.Equal("dave", review.User.UserName)

	release := convertRelease(&giteasdk.Release{ID: 13, TagName: "v1", Title: "One", HTMLURL: "https://release", Target: "main", IsPrerelease: true, CreatedAt: created, PublishedAt: updated})
	assert.Equal("v1", release.TagName)
	assert.Equal(&updated, release.PublishedAt)

	tag := convertTag(&giteasdk.Tag{Name: "v1", Commit: &giteasdk.CommitMeta{SHA: "abc", URL: "https://commit", Created: created}})
	assert.Equal("abc", tag.Commit.SHA)

	status := convertStatus(&giteasdk.Status{ID: 14, Context: "ci", State: giteasdk.StatusState("success"), TargetURL: "https://ci", Description: "ok", Created: created, Updated: updated})
	assert.Equal("success", status.State)
	assert.Equal("ci", status.Context)
	assert.Equal("https://ci", status.TargetURL)

	unsafeStatus := convertStatus(&giteasdk.Status{TargetURL: "javascript:alert(1)"})
	assert.Empty(unsafeStatus.TargetURL)
}

func TestConvertGiteaActionRun(t *testing.T) {
	assert := Assert.New(t)
	started := time.Date(2026, 5, 1, 2, 3, 4, 0, time.UTC)
	completed := started.Add(time.Minute)

	run := convertActionRun(&giteasdk.ActionWorkflowRun{
		ID:           15,
		RunNumber:    16,
		Path:         ".gitea/workflows/build.yaml",
		DisplayTitle: "Build",
		Status:       "completed",
		Conclusion:   "failure",
		HeadSha:      "abc",
		HTMLURL:      "https://actions/run",
		StartedAt:    started,
		CompletedAt:  completed,
	})

	assert.Equal(int64(15), run.ID)
	assert.Equal(int64(16), run.RunNumber)
	assert.Equal(".gitea/workflows/build.yaml", run.WorkflowID)
	assert.Equal("Build", run.Title)
	assert.Equal("completed", run.Status)
	assert.Equal("failure", run.Conclusion)
	assert.Equal("abc", run.CommitSHA)
	assert.Equal("https://actions/run", run.HTMLURL)
	assert.Equal(started, run.Created)
	assert.Equal(completed, run.Updated)
	assert.Equal(&started, run.Started)
	assert.Equal(&completed, run.Stopped)
}
