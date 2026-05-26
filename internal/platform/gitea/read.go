package gitea

import (
	"context"
	"strings"

	giteasdk "code.gitea.io/sdk/gitea"
	"go.kenn.io/middleman/internal/platform"
	"go.kenn.io/middleman/internal/platform/gitealike"
)

func (c *Client) GetRepository(ctx context.Context, ref platform.RepoRef) (platform.Repository, error) {
	return c.provider.GetRepository(ctx, ref)
}

func (c *Client) ListRepositories(
	ctx context.Context,
	owner string,
	opts platform.RepositoryListOptions,
) ([]platform.Repository, error) {
	return c.provider.ListRepositories(ctx, owner, opts)
}

func (c *Client) ListOpenMergeRequests(
	ctx context.Context,
	ref platform.RepoRef,
) ([]platform.MergeRequest, error) {
	return c.provider.ListOpenMergeRequests(ctx, ref)
}

func (c *Client) GetMergeRequest(
	ctx context.Context,
	ref platform.RepoRef,
	number int,
) (platform.MergeRequest, error) {
	return c.provider.GetMergeRequest(ctx, ref, number)
}

func (c *Client) ListMergeRequestEvents(
	ctx context.Context,
	ref platform.RepoRef,
	number int,
) ([]platform.MergeRequestEvent, error) {
	return c.provider.ListMergeRequestEvents(ctx, ref, number)
}

func (c *Client) ListOpenIssues(ctx context.Context, ref platform.RepoRef) ([]platform.Issue, error) {
	return c.provider.ListOpenIssues(ctx, ref)
}

func (c *Client) GetIssue(
	ctx context.Context,
	ref platform.RepoRef,
	number int,
) (platform.Issue, error) {
	return c.provider.GetIssue(ctx, ref, number)
}

func (c *Client) ListIssueEvents(
	ctx context.Context,
	ref platform.RepoRef,
	number int,
) ([]platform.IssueEvent, error) {
	return c.provider.ListIssueEvents(ctx, ref, number)
}

func (c *Client) ListReleases(ctx context.Context, ref platform.RepoRef) ([]platform.Release, error) {
	return c.provider.ListReleases(ctx, ref)
}

func (c *Client) ListTags(ctx context.Context, ref platform.RepoRef) ([]platform.Tag, error) {
	return c.provider.ListTags(ctx, ref)
}

func (c *Client) ListCIChecks(
	ctx context.Context,
	ref platform.RepoRef,
	sha string,
) ([]platform.CICheck, error) {
	return c.provider.ListCIChecks(ctx, ref, sha)
}

func (t *transport) GetRepository(
	ctx context.Context,
	owner, repo string,
) (gitealike.RepositoryDTO, error) {
	t.spendSyncBudget(ctx)
	var repository *giteasdk.Repository
	var resp *giteasdk.Response
	err := t.withRequestContext(ctx, func() error {
		var err error
		repository, resp, err = t.api.GetRepo(owner, repo)
		return err
	})
	if err != nil {
		return gitealike.RepositoryDTO{}, giteaHTTPError(resp, err)
	}
	return convertRepository(repository)
}

func (t *transport) ListUserRepositories(
	ctx context.Context,
	owner string,
	opts gitealike.PageOptions,
) ([]gitealike.RepositoryDTO, gitealike.Page, error) {
	t.spendSyncBudget(ctx)
	var repos []*giteasdk.Repository
	var resp *giteasdk.Response
	err := t.withRequestContext(ctx, func() error {
		var err error
		repos, resp, err = t.api.ListUserRepos(owner, giteasdk.ListReposOptions{
			ListOptions: giteaListOptions(opts),
		})
		return err
	})
	if err != nil {
		return nil, gitealike.Page{}, giteaHTTPError(resp, err)
	}
	return convertRepositories(repos, giteaPage(resp))
}

func (t *transport) ListOrgRepositories(
	ctx context.Context,
	owner string,
	opts gitealike.PageOptions,
) ([]gitealike.RepositoryDTO, gitealike.Page, error) {
	t.spendSyncBudget(ctx)
	var repos []*giteasdk.Repository
	var resp *giteasdk.Response
	err := t.withRequestContext(ctx, func() error {
		var err error
		repos, resp, err = t.api.ListOrgRepos(owner, giteasdk.ListOrgReposOptions{
			ListOptions: giteaListOptions(opts),
		})
		return err
	})
	if err != nil {
		return nil, gitealike.Page{}, giteaHTTPError(resp, err)
	}
	return convertRepositories(repos, giteaPage(resp))
}

func (t *transport) ListOpenPullRequests(
	ctx context.Context,
	ref platform.RepoRef,
	opts gitealike.PageOptions,
) ([]gitealike.PullRequestDTO, gitealike.Page, error) {
	t.spendSyncBudget(ctx)
	var prs []*giteasdk.PullRequest
	var resp *giteasdk.Response
	err := t.withRequestContext(ctx, func() error {
		var err error
		prs, resp, err = t.api.ListRepoPullRequests(ref.Owner, ref.Name, giteasdk.ListPullRequestsOptions{
			ListOptions: giteaListOptions(opts),
			State:       giteasdk.StateOpen,
		})
		return err
	})
	if err != nil {
		return nil, gitealike.Page{}, giteaHTTPError(resp, err)
	}
	return convertPullRequests(prs, t.mergeableForPullRequest), giteaPage(resp), nil
}

func (t *transport) GetPullRequest(
	ctx context.Context,
	ref platform.RepoRef,
	number int,
) (gitealike.PullRequestDTO, error) {
	t.spendSyncBudget(ctx)
	var pr *giteasdk.PullRequest
	var resp *giteasdk.Response
	err := t.withRequestContext(ctx, func() error {
		var err error
		pr, resp, err = t.api.GetPullRequest(ref.Owner, ref.Name, int64(number))
		return err
	})
	if err != nil {
		return gitealike.PullRequestDTO{}, giteaHTTPError(resp, err)
	}
	return convertPullRequest(pr, t.mergeableForPullRequest(pr)), nil
}

func (t *transport) mergeableForPullRequest(pr *giteasdk.PullRequest) *bool {
	if pr == nil {
		return nil
	}
	mergeable, _ := t.mergeability.MergeableForPullRequest(
		pr.HTMLURL,
		prBranchSHA(pr.Head),
		prBranchSHA(pr.Base),
	)
	return mergeable
}

func prBranchSHA(branch *giteasdk.PRBranchInfo) string {
	if branch == nil {
		return ""
	}
	return branch.Sha
}

func (t *transport) ListPullRequestComments(
	ctx context.Context,
	ref platform.RepoRef,
	number int,
	opts gitealike.PageOptions,
) ([]gitealike.CommentDTO, gitealike.Page, error) {
	t.spendSyncBudget(ctx)
	var comments []*giteasdk.Comment
	var resp *giteasdk.Response
	err := t.withRequestContext(ctx, func() error {
		var err error
		comments, resp, err = t.api.ListIssueComments(ref.Owner, ref.Name, int64(number), giteasdk.ListIssueCommentOptions{
			ListOptions: giteaListOptions(opts),
		})
		return err
	})
	if err != nil {
		return nil, gitealike.Page{}, giteaHTTPError(resp, err)
	}
	return convertComments(comments), giteaPage(resp), nil
}

func (t *transport) ListPullRequestReviews(
	ctx context.Context,
	ref platform.RepoRef,
	number int,
	opts gitealike.PageOptions,
) ([]gitealike.ReviewDTO, gitealike.Page, error) {
	t.spendSyncBudget(ctx)
	var reviews []*giteasdk.PullReview
	var resp *giteasdk.Response
	err := t.withRequestContext(ctx, func() error {
		var err error
		reviews, resp, err = t.api.ListPullReviews(ref.Owner, ref.Name, int64(number), giteasdk.ListPullReviewsOptions{
			ListOptions: giteaListOptions(opts),
		})
		return err
	})
	if err != nil {
		return nil, gitealike.Page{}, giteaHTTPError(resp, err)
	}
	return convertReviews(reviews), giteaPage(resp), nil
}

func (t *transport) ListPullRequestCommits(
	ctx context.Context,
	ref platform.RepoRef,
	number int,
	opts gitealike.PageOptions,
) ([]gitealike.CommitDTO, gitealike.Page, error) {
	t.spendSyncBudget(ctx)
	var commits []*giteasdk.Commit
	var resp *giteasdk.Response
	err := t.withRequestContext(ctx, func() error {
		var err error
		commits, resp, err = t.api.ListPullRequestCommits(ref.Owner, ref.Name, int64(number), giteasdk.ListPullRequestCommitsOptions{
			ListOptions: giteaListOptions(opts),
		})
		return err
	})
	if err != nil {
		return nil, gitealike.Page{}, giteaHTTPError(resp, err)
	}
	return convertCommits(commits), giteaPage(resp), nil
}

func (t *transport) ListOpenIssues(
	ctx context.Context,
	ref platform.RepoRef,
	opts gitealike.PageOptions,
) ([]gitealike.IssueDTO, gitealike.Page, error) {
	t.spendSyncBudget(ctx)
	var issues []*giteasdk.Issue
	var resp *giteasdk.Response
	err := t.withRequestContext(ctx, func() error {
		var err error
		issues, resp, err = t.api.ListRepoIssues(ref.Owner, ref.Name, giteasdk.ListIssueOption{
			ListOptions: giteaListOptions(opts),
			State:       giteasdk.StateOpen,
			Type:        giteasdk.IssueTypeIssue,
		})
		return err
	})
	if err != nil {
		return nil, gitealike.Page{}, giteaHTTPError(resp, err)
	}
	return convertIssues(issues), giteaPage(resp), nil
}

func (t *transport) GetIssue(
	ctx context.Context,
	ref platform.RepoRef,
	number int,
) (gitealike.IssueDTO, error) {
	t.spendSyncBudget(ctx)
	var issue *giteasdk.Issue
	var resp *giteasdk.Response
	err := t.withRequestContext(ctx, func() error {
		var err error
		issue, resp, err = t.api.GetIssue(ref.Owner, ref.Name, int64(number))
		return err
	})
	if err != nil {
		return gitealike.IssueDTO{}, giteaHTTPError(resp, err)
	}
	return convertIssue(issue), nil
}

func (t *transport) ListIssueComments(
	ctx context.Context,
	ref platform.RepoRef,
	number int,
	opts gitealike.PageOptions,
) ([]gitealike.CommentDTO, gitealike.Page, error) {
	return t.ListPullRequestComments(ctx, ref, number, opts)
}

func (t *transport) ListIssueTimeline(
	ctx context.Context,
	ref platform.RepoRef,
	number int,
	opts gitealike.PageOptions,
) ([]gitealike.TimelineEventDTO, gitealike.Page, error) {
	t.spendSyncBudget(ctx)
	var comments []*giteasdk.TimelineComment
	var resp *giteasdk.Response
	err := t.withRequestContext(ctx, func() error {
		var err error
		comments, resp, err = t.api.ListIssueTimeline(ref.Owner, ref.Name, int64(number), giteasdk.ListIssueCommentOptions{
			ListOptions: giteaListOptions(opts),
		})
		return err
	})
	if err != nil {
		return nil, gitealike.Page{}, giteaHTTPError(resp, err)
	}
	return convertTimelineEvents(comments), giteaPage(resp), nil
}

func (t *transport) ListReleases(
	ctx context.Context,
	ref platform.RepoRef,
	opts gitealike.PageOptions,
) ([]gitealike.ReleaseDTO, gitealike.Page, error) {
	t.spendSyncBudget(ctx)
	var releases []*giteasdk.Release
	var resp *giteasdk.Response
	err := t.withRequestContext(ctx, func() error {
		var err error
		releases, resp, err = t.api.ListReleases(ref.Owner, ref.Name, giteasdk.ListReleasesOptions{
			ListOptions: giteaListOptions(opts),
		})
		return err
	})
	if err != nil {
		return nil, gitealike.Page{}, giteaHTTPError(resp, err)
	}
	return convertReleases(releases), giteaPage(resp), nil
}

func (t *transport) ListTags(
	ctx context.Context,
	ref platform.RepoRef,
	opts gitealike.PageOptions,
) ([]gitealike.TagDTO, gitealike.Page, error) {
	t.spendSyncBudget(ctx)
	var tags []*giteasdk.Tag
	var resp *giteasdk.Response
	err := t.withRequestContext(ctx, func() error {
		var err error
		tags, resp, err = t.api.ListRepoTags(ref.Owner, ref.Name, giteasdk.ListRepoTagsOptions{
			ListOptions: giteaListOptions(opts),
		})
		return err
	})
	if err != nil {
		return nil, gitealike.Page{}, giteaHTTPError(resp, err)
	}
	return convertTags(tags), giteaPage(resp), nil
}

func (t *transport) ListStatuses(
	ctx context.Context,
	ref platform.RepoRef,
	sha string,
	opts gitealike.PageOptions,
) ([]gitealike.StatusDTO, gitealike.Page, error) {
	t.spendSyncBudget(ctx)
	var statuses []*giteasdk.Status
	var resp *giteasdk.Response
	err := t.withRequestContext(ctx, func() error {
		var err error
		statuses, resp, err = t.api.ListStatuses(ref.Owner, ref.Name, sha, giteasdk.ListStatusesOption{
			ListOptions: giteaListOptions(opts),
		})
		return err
	})
	if err != nil {
		return nil, gitealike.Page{}, giteaHTTPError(resp, err)
	}
	return convertStatuses(statuses), giteaPage(resp), nil
}

func (t *transport) ListActionRuns(
	ctx context.Context,
	ref platform.RepoRef,
	sha string,
	opts gitealike.PageOptions,
) ([]gitealike.ActionRunDTO, gitealike.Page, error) {
	t.spendSyncBudget(ctx)
	var runs *giteasdk.ActionWorkflowRunsResponse
	var resp *giteasdk.Response
	err := t.withRequestContext(ctx, func() error {
		var err error
		runs, resp, err = t.api.ListRepoActionRuns(ref.Owner, ref.Name, giteasdk.ListRepoActionRunsOptions{
			ListOptions: giteaListOptions(opts),
			HeadSHA:     sha,
		})
		return err
	})
	if err != nil {
		if isActionsUnsupportedVersionError(err) {
			return nil, gitealike.Page{}, nil
		}
		return nil, gitealike.Page{}, giteaHTTPError(resp, err)
	}
	if runs == nil {
		return nil, giteaPage(resp), nil
	}
	return convertActionRuns(runs.WorkflowRuns), giteaPage(resp), nil
}

func isActionsUnsupportedVersionError(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "is older than 1.26.0") ||
		strings.Contains(msg, "does not satisfy version constraint >= 1.26.0")
}

func giteaListOptions(opts gitealike.PageOptions) giteasdk.ListOptions {
	return giteasdk.ListOptions{Page: opts.Page, PageSize: opts.PageSize}
}

func giteaPage(resp *giteasdk.Response) gitealike.Page {
	if resp == nil {
		return gitealike.Page{}
	}
	return gitealike.Page{Next: resp.NextPage}
}

func giteaHTTPError(resp *giteasdk.Response, err error) error {
	if err == nil {
		return nil
	}
	if resp != nil && resp.Response != nil {
		return &gitealike.HTTPError{StatusCode: resp.StatusCode, Err: err}
	}
	return err
}
