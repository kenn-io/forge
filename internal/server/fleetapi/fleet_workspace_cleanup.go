package fleetapi

import (
	"context"
	"fmt"
	"net/http"
	"net/url"

	"go.kenn.io/forge/internal/server/httpapi"
)

type queueFederationWorkspaceCleanupInput struct {
	ID string `path:"id"`
}

func (s *Handler) queueFederationWorkspaceCleanup(
	_ context.Context, input *queueFederationWorkspaceCleanupInput,
) (*struct{}, error) {
	if s.queueWorkspaceDeletion == nil {
		return nil, httpapi.ServiceUnavailable("workspace cleanup is unavailable")
	}
	if err := s.queueWorkspaceDeletion(input.ID); err != nil {
		return nil, httpapi.Conflict(httpapi.CodeConflict, err.Error(), nil)
	}
	return nil, nil
}

// RequestWorkspaceCleanup asks the owning spoke to durably admit workspace
// cleanup after the hub completes a provider merge on that spoke's behalf.
func (s *Handler) RequestWorkspaceCleanup(
	ctx context.Context, hostKey, workspaceID string,
) error {
	target, ok := s.resolveFleetHostTarget(hostKey)
	if !ok || target.self {
		return fmt.Errorf("fleet host %q is unavailable", hostKey)
	}
	request, err := http.NewRequestWithContext(
		ctx,
		http.MethodPost,
		remoteHTTPURL(
			target.member.BaseURL,
			"/api/v1/federation/workspaces/"+url.PathEscape(workspaceID)+"/cleanup",
			"",
		),
		nil,
	)
	if err != nil {
		return fmt.Errorf("build spoke workspace cleanup request: %w", err)
	}
	request.Header.Set("Content-Type", "application/json")
	s.authorizeFederationRequest(request.Header, target.credential)
	response, err := target.clients.rest.Do(request)
	if err != nil {
		return fmt.Errorf("request spoke workspace cleanup: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return fmt.Errorf("spoke workspace cleanup returned HTTP %d", response.StatusCode)
	}
	return nil
}
