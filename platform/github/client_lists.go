package github

import (
	"context"
	"errors"
	"fmt"
	"strings"

	gh "github.com/google/go-github/v93/github"
)

func (c *Client) ListOpenPullRequests(ctx context.Context, owner, repo string) ([]*gh.PullRequest, error) {
	opts := &gh.PullRequestListOptions{
		State:   "open",
		PerPage: 100,
	}
	progress := c.progress(owner, repo, "pulls")
	all, err := collectPagesWithProgress(ctx, func(pageOpts *gh.ListOptions) ([]*gh.PullRequest, *gh.Response, error) {
		opts.ListOptions = *pageOpts
		page, resp, err := c.gh.PullRequests.List(ctx, owner, repo, opts)
		if err != nil {
			return nil, nil, fmt.Errorf("listing open pull requests for %s/%s: %w", owner, repo, err)
		}
		return page, resp, nil
	}, c.trackRate, progress.recordPage)
	if err != nil {
		return nil, err
	}
	progress.done()
	return all, nil
}

func (c *Client) ListOpenIssues(
	ctx context.Context, owner, repo string,
) ([]*gh.Issue, error) {
	opts := &gh.IssueListByRepoOptions{
		State:       "open",
		Sort:        "updated",
		Direction:   "desc",
		ListOptions: gh.ListOptions{PerPage: 100},
	}
	progress := c.progress(owner, repo, "issues")
	issues, err := collectPagesWithProgress(ctx, func(pageOpts *gh.ListOptions) ([]*gh.Issue, *gh.Response, error) {
		opts.ListOptions = *pageOpts
		issues, resp, err := c.gh.Issues.ListByRepo(
			ctx, owner, repo, opts,
		)
		if err != nil {
			return nil, nil, fmt.Errorf(
				"listing issues for %s/%s: %w", owner, repo, err,
			)
		}
		return issues, resp, nil
	}, c.trackRate, progress.recordPage)
	if err != nil {
		return nil, err
	}
	progress.done()

	var all []*gh.Issue
	// GitHub's Issues API returns PRs too — filter them out. Nil entries
	// pass through so contract validation can reject them typed instead of
	// the filter panicking on the dereference.
	for _, issue := range issues {
		if issue == nil || issue.PullRequestLinks == nil {
			all = append(all, issue)
		}
	}
	return all, nil
}

func (c *Client) ListRepositoriesByOwner(
	ctx context.Context, owner string,
) ([]*gh.Repository, error) {
	if c.splitAuthActiveForOwner(owner) {
		ctx = c.authContext(ctx, owner, false)
		repos, err := collectPages(
			ctx,
			func(opts *gh.ListOptions) ([]*gh.Repository, *gh.Response, error) {
				result, resp, err := c.gh.Apps.ListRepos(ctx, opts)
				if err != nil {
					return nil, resp, err
				}
				return result.Repositories, resp, nil
			},
			c.trackRate,
		)
		if err != nil {
			return nil, fmt.Errorf(
				"listing installation repositories for %s: %w", owner, err,
			)
		}
		filtered := repos[:0]
		for _, repo := range repos {
			if strings.EqualFold(repo.GetOwner().GetLogin(), owner) {
				filtered = append(filtered, repo)
			}
		}
		return filtered, nil
	}

	viewerLogin, viewerErr := c.authenticatedLogin(ctx)
	if viewerErr == nil && strings.EqualFold(owner, viewerLogin) {
		repos, err := collectPages(
			ctx,
			func(opts *gh.ListOptions) ([]*gh.Repository, *gh.Response, error) {
				page, resp, err := c.gh.Repositories.ListByAuthenticatedUser(
					ctx, &gh.RepositoryListByAuthenticatedUserOptions{
						Affiliation: "owner",
						ListOptions: *opts,
					},
				)
				if err != nil {
					return nil, resp, err
				}
				return page, resp, nil
			},
			c.trackRate,
		)
		if err != nil {
			return nil, fmt.Errorf(
				"listing repositories for authenticated owner %s: %w",
				owner, err,
			)
		}
		return repos, nil
	}

	orgRepos, err := collectPages(
		ctx,
		func(opts *gh.ListOptions) ([]*gh.Repository, *gh.Response, error) {
			page, resp, err := c.gh.Repositories.ListByOrg(
				ctx, owner, &gh.RepositoryListByOrgOptions{
					Type:        "all",
					ListOptions: *opts,
				},
			)
			if err != nil {
				return nil, resp, err
			}
			return page, resp, nil
		},
		c.trackRate,
	)
	if err == nil {
		return orgRepos, nil
	}

	userRepos, userErr := collectPages(
		ctx,
		func(opts *gh.ListOptions) ([]*gh.Repository, *gh.Response, error) {
			page, resp, err := c.gh.Repositories.ListByUser(
				ctx, owner, &gh.RepositoryListByUserOptions{
					Type:        "owner",
					ListOptions: *opts,
				},
			)
			if err != nil {
				return nil, resp, err
			}
			return page, resp, nil
		},
		c.trackRate,
	)
	if userErr != nil {
		return nil, fmt.Errorf(
			"listing repositories for %s: org=%w user=%w",
			owner, err, userErr,
		)
	}
	return userRepos, nil
}

func (c *Client) authenticatedLogin(ctx context.Context) (string, error) {
	cacheKey := c.authenticatedViewerCacheKey()
	c.viewerMu.Lock()
	defer c.viewerMu.Unlock()
	if c.viewerLogin != "" &&
		c.viewerLoginCacheKey == cacheKey &&
		c.now().Sub(c.viewerLoginAt) < c.viewerCacheTTL {
		return c.viewerLogin, nil
	}
	user, resp, err := c.writeGH().Users.Get(ctx, "")
	c.trackWriteRate(resp)
	if err != nil {
		return "", fmt.Errorf("getting authenticated user: %w", err)
	}
	login := user.GetLogin()
	if login == "" {
		return "", errors.New("authenticated user login is empty")
	}
	c.viewerLogin = login
	c.viewerLoginAt = c.now()
	c.viewerLoginCacheKey = cacheKey
	return login, nil
}

func (c *Client) AuthenticatedViewerLogin(ctx context.Context) (string, error) {
	return c.authenticatedLogin(ctx)
}

func (c *Client) AuthenticatedViewerCacheKey() string {
	return c.authenticatedViewerCacheKey()
}

func (c *Client) authenticatedViewerCacheKey() string {
	if c.auth.CredentialKey == nil {
		return ""
	}
	return c.auth.CredentialKey()
}

func (c *Client) ListPullRequestsPage(
	ctx context.Context, owner, repo, state string, page int,
) ([]*gh.PullRequest, bool, error) {
	opts := &gh.PullRequestListOptions{
		State:     state,
		Sort:      "updated",
		Direction: "desc",
		Page:      page,
		PerPage:   100,
	}
	prs, resp, err := c.gh.PullRequests.List(
		ctx, owner, repo, opts,
	)
	c.trackRate(resp)
	if err != nil {
		return nil, false, fmt.Errorf(
			"list %s PRs page %d for %s/%s: %w",
			state, page, owner, repo, err,
		)
	}
	hasMore := resp != nil && resp.NextPage > 0
	return prs, hasMore, nil
}

func (c *Client) ListIssuesPage(
	ctx context.Context, owner, repo, state string, page int,
) ([]*gh.Issue, bool, error) {
	opts := &gh.IssueListByRepoOptions{
		State:     state,
		Sort:      "updated",
		Direction: "desc",
		ListOptions: gh.ListOptions{
			Page:    page,
			PerPage: 100,
		},
	}
	issues, resp, err := c.gh.Issues.ListByRepo(
		ctx, owner, repo, opts,
	)
	c.trackRate(resp)
	if err != nil {
		return nil, false, fmt.Errorf(
			"list %s issues page %d for %s/%s: %w",
			state, page, owner, repo, err,
		)
	}
	// Filter out PRs (GitHub Issues API returns them). Nil entries pass
	// through so the canonical normalization rejects them typed instead of
	// the filter panicking on the dereference.
	var filtered []*gh.Issue
	for _, issue := range issues {
		if issue == nil || issue.PullRequestLinks == nil {
			filtered = append(filtered, issue)
		}
	}
	hasMore := resp != nil && resp.NextPage > 0
	return filtered, hasMore, nil
}
