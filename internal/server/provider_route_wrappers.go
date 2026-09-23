package server

import (
	"context"
	"net/http"

	"github.com/danielgtaylor/huma/v2"
	"go.kenn.io/forge/internal/mcpserver"
	"go.kenn.io/forge/internal/server/providerapi"
)

func (s *Server) registerProviderFederationAPI(api huma.API) {
	s.providerapi.RegisterProviderDescriptorAPI(api)
	s.registerProviderStateHandoffAPI(api)
	s.registerFederationProviderSettingsAPI(api)
	s.syncevents.RegisterFederationProviderWorkspaceAPI(api)
	s.providerapi.RegisterProviderActivitySubjectAPI(api)
	huma.Register(api, huma.Operation{
		OperationID: "federation-list-workflow-states",
		Method:      http.MethodPost,
		Path:        "/federation/provider/workflow-states/query",
		Summary:     "List hub workflow states for a Forge spoke",
		Tags:        []string{"Fleet"},
	}, s.federationListWorkflowStates)
	huma.Register(api, huma.Operation{
		OperationID: "federation-set-workflow-state",
		Method:      http.MethodPut,
		Path:        "/federation/provider/workflow-state",
		Summary:     "Set hub workflow state for a Forge spoke",
		Tags:        []string{"Fleet"},
	}, s.federationSetWorkflowState)
}

func (s *Server) federationListWorkflowStates(
	ctx context.Context,
	input *providerapi.FederationListWorkflowStatesInput,
) (*providerapi.FederationListWorkflowStatesOutput, error) {
	page, err := (mcpBackend{server: s}).listWorkflowStatesLocal(ctx, input.Body.Mcp())
	if err != nil {
		return nil, providerapi.FederationWorkflowProblem(err)
	}
	return &providerapi.FederationListWorkflowStatesOutput{Body: providerapi.FederationWorkflowPageFromMCP(page)}, nil
}

func (s *Server) federationSetWorkflowState(
	ctx context.Context,
	input *providerapi.FederationSetWorkflowStateInput,
) (*providerapi.FederationSetWorkflowStateOutput, error) {
	mutation, err := (mcpBackend{server: s}).setWorkflowStateLocal(
		ctx,
		mcpserver.ItemIdentity(input.Body.Item),
		mcpserver.WorkflowUpdate(input.Body.Update),
	)
	if err != nil {
		return nil, providerapi.FederationWorkflowProblem(err)
	}
	return &providerapi.FederationSetWorkflowStateOutput{Body: providerapi.FederationWorkflowMutationFromMCP(mutation)}, nil
}
