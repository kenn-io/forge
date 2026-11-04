package github

import (
	"context"

	gh "github.com/google/go-github/v93/github"
)

func (c *Client) ListReleases(
	ctx context.Context, owner, repo string, perPage int,
) ([]*gh.RepositoryRelease, error) {
	if perPage < 1 {
		perPage = 1
	}
	releases, resp, err := c.gh.Repositories.ListReleases(ctx, owner, repo, &gh.ListOptions{
		PerPage: perPage,
	})
	c.trackRate(resp)
	if err != nil {
		return nil, err
	}
	return releases, nil
}

func (c *Client) ListTags(
	ctx context.Context, owner, repo string, perPage int,
) ([]*gh.RepositoryTag, error) {
	if perPage < 1 {
		perPage = 1
	}
	tags, resp, err := c.gh.Repositories.ListTags(ctx, owner, repo, &gh.ListOptions{
		PerPage: perPage,
	})
	c.trackRate(resp)
	if err != nil {
		return nil, err
	}
	return tags, nil
}

func (c *Client) ListRepoLabels(
	ctx context.Context, owner, repo string,
) ([]*gh.Label, error) {
	var all []*gh.Label
	opts := &gh.ListOptions{Page: 1, PerPage: 100}
	for {
		labels, resp, err := c.gh.Issues.ListLabels(ctx, owner, repo, opts)
		c.trackRate(resp)
		if err != nil {
			return nil, err
		}
		all = append(all, labels...)
		// A replayed full page can lack a next link after the catalog grows.
		if resp.NextPage == 0 && len(labels) < opts.PerPage {
			break
		}
		if resp.NextPage > 0 {
			opts.Page = resp.NextPage
		} else {
			opts.Page++
		}
	}
	return all, nil
}
