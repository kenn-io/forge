package server

import (
	"context"
	"errors"

	"go.kenn.io/forge/internal/db"
	"go.kenn.io/forge/internal/server/issueapi"
	"go.kenn.io/forge/internal/server/pullapi"
)

// filterTrackedRepos limits stored repositories to those the pull and issue
// lists show, so setup counts the same work the UI can read.
func (s *Server) filterTrackedRepos(repos []db.Repo) []db.Repo {
	if s.cfg == nil {
		return repos
	}
	return s.repoapi.FilterConfiguredRepos(repos)
}

// HasSetupWork checks for ready local workspaces and readable provider items.
func (s *Server) HasSetupWork(ctx context.Context) (bool, error) {
	workspaces, err := s.db.ListWorkspaces(ctx)
	if err != nil {
		return false, err
	}
	for _, workspace := range workspaces {
		if workspace.Status == "ready" {
			return true, nil
		}
	}
	if s.providerSource != nil && (!s.options.FederationSpokeActive || !s.streamapi.FederationEnabled()) {
		return false, nil
	}
	var allowedRepoIDs []int64
	if s.providerSource == nil {
		repos, err := s.db.ListRepos(ctx)
		if err != nil {
			return false, err
		}
		repos = s.filterTrackedRepos(repos)
		allowedRepoIDs = make([]int64, 0, len(repos))
		for _, repo := range repos {
			allowedRepoIDs = append(allowedRepoIDs, repo.ID)
		}
	}
	var hasPulls bool
	var pullErr error
	if s.providerSource != nil {
		prs, err := s.providerSource.ListPulls(ctx, pullapi.ListQuery{State: "all", Limit: 1})
		hasPulls, pullErr = len(prs) > 0, err
	} else {
		prs, err := s.db.ListMergeRequests(ctx, db.ListMergeRequestsOpts{State: "all", Limit: 1, AllowedRepoIDs: allowedRepoIDs})
		hasPulls, pullErr = len(prs) > 0, err
	}
	if pullErr == nil && hasPulls {
		return true, nil
	}
	if err := ctx.Err(); err != nil {
		return false, errors.Join(pullErr, err)
	}
	var hasIssues bool
	var issueErr error
	if s.providerSource != nil {
		issues, err := s.providerSource.ListIssues(ctx, issueapi.ListQuery{State: "all", Limit: 1})
		hasIssues, issueErr = len(issues) > 0, err
	} else {
		issues, err := s.db.ListIssues(ctx, db.ListIssuesOpts{State: "all", Limit: 1, AllowedRepoIDs: allowedRepoIDs})
		hasIssues, issueErr = len(issues) > 0, err
	}
	if issueErr == nil && hasIssues {
		return true, nil
	}
	return false, errors.Join(pullErr, issueErr)
}
