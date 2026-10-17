package server

import (
	"context"

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
	prs, err := s.pullAPI.ListService(ctx, pullapi.ListQuery{State: "all", Limit: 1})
	if err != nil || len(prs) > 0 {
		return len(prs) > 0, err
	}
	issues, err := s.issueAPI.ListService(ctx, issueapi.ListQuery{State: "all", Limit: 1})
	return len(issues) > 0, err
}
