package server

import (
	"context"
	"net/http"

	"github.com/danielgtaylor/huma/v2"
	"go.kenn.io/forge/internal/server/workspaceapi"
)

type federationProviderWorkspaceItemInput struct {
	Body workspaceapi.ProviderWorkspaceItemRequest
}

func (s *Server) registerFederationProviderWorkspaceAPI(api huma.API) {
	huma.Register(api, huma.Operation{
		OperationID: "federation-auto-assign-workspace-item",
		Method:      http.MethodPost,
		Path:        "/federation/provider/workspace-auto-assign",
		Summary:     "Apply hub assignment policy to a workspace item",
		Tags:        []string{"Fleet"},
	}, s.federationAutoAssignWorkspaceItem)
}

func (s *Server) federationAutoAssignWorkspaceItem(
	ctx context.Context, input *federationProviderWorkspaceItemInput,
) (*struct{}, error) {
	if err := s.workspaceAPI.AutoAssignProviderWorkspaceItem(ctx, input.Body); err != nil {
		return nil, err
	}
	return &struct{}{}, nil
}
