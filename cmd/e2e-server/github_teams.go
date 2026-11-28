package main

import (
	"context"
	"strings"

	gh "github.com/google/go-github/v93/github"
	"go.kenn.io/forge/internal/testutil"
)

func (c *e2eWorkflowClient) ListOrgTeams(_ context.Context, org string) ([]*gh.Team, error) {
	if !strings.EqualFold(org, "acme") {
		return nil, nil
	}
	return []*gh.Team{{Slug: new("platform"), Name: new("Platform")}}, nil
}

func (c *e2eWorkflowClient) ListTeamRepositories(_ context.Context, org, slug string) ([]*gh.Repository, error) {
	if !strings.EqualFold(org, "acme") || slug != "platform" {
		return nil, nil
	}
	repos := make([]*gh.Repository, 0, 2)
	for _, name := range []string{"widgets", "tools"} {
		repos = append(repos, &gh.Repository{ID: new(testutil.FixtureRepoID("acme", name)), FullName: new("acme/" + name)})
	}
	return repos, nil
}
