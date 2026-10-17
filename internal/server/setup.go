package server

import (
	"context"
	"errors"

	"go.kenn.io/forge/internal/server/issueapi"
	"go.kenn.io/forge/internal/server/pullapi"
)

// HasSetupWork checks for ready local workspaces and readable provider items.
func (s *Server) HasSetupWork(ctx context.Context) (bool, error) {
	if s.options.ExecutionWorker {
		return false, nil
	}
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
	pullList := s.pullAPI.ListProviderService
	issueList := s.issueAPI.ListProviderService
	if s.providerSource != nil {
		pullList = s.providerSource.ListPulls
		issueList = s.providerSource.ListIssues
	}
	prs, pullErr := pullList(ctx, pullapi.ListQuery{State: "all", Limit: 1})
	if pullErr == nil && len(prs) > 0 {
		return true, nil
	}
	if err := ctx.Err(); err != nil {
		return false, errors.Join(pullErr, err)
	}
	issues, issueErr := issueList(ctx, issueapi.ListQuery{State: "all", Limit: 1})
	if issueErr == nil && len(issues) > 0 {
		return true, nil
	}
	return false, errors.Join(pullErr, issueErr)
}
