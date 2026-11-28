package github

import (
	"context"

	gh "github.com/google/go-github/v93/github"
)

// ListOrgTeams reads team membership with the user's credential.
func (c *Client) ListOrgTeams(ctx context.Context, org string) ([]*gh.Team, error) {
	rows, err := collectPages(ctx, func(opts *gh.ListOptions) ([]*gh.Team, *gh.Response, error) {
		return c.writeGH().Teams.ListTeams(ctx, org, opts)
	}, c.trackWriteRate)
	return rows, mapGitHubReadError(c.platformHost, c.now, "", err)
}

// ListTeamRepositories includes repositories at every access level.
func (c *Client) ListTeamRepositories(ctx context.Context, org, slug string) ([]*gh.Repository, error) {
	rows, err := collectPages(ctx, func(opts *gh.ListOptions) ([]*gh.Repository, *gh.Response, error) {
		return c.writeGH().Teams.ListTeamReposBySlug(ctx, org, slug, opts)
	}, c.trackWriteRate)
	return rows, mapGitHubReadError(c.platformHost, c.now, "", err)
}
