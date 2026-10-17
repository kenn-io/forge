package server

import (
	"context"

	"go.kenn.io/forge/internal/server/issueapi"
	"go.kenn.io/forge/internal/server/pullapi"
)

// HasReadableProviderWork checks the same provider source as the item lists.
func (s *Server) HasReadableProviderWork(ctx context.Context) (bool, error) {
	if s.options.ExecutionWorker {
		return false, nil
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
