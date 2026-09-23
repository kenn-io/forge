package server

import (
	"context"
	"net/http"
	"strings"

	"github.com/danielgtaylor/huma/v2"

	"go.kenn.io/forge/internal/fleet"
	"go.kenn.io/forge/internal/server/httpapi"
	"go.kenn.io/forge/internal/server/spokeapi"
)

const maxWorkspaceProviderStateSubjects = 500

type federationWorkspaceProviderStateRequest struct {
	Workspaces []federationWorkspaceProviderSubject `json:"workspaces" nullable:"false" maxItems:"500"`
}

type federationWorkspaceProviderStateInput struct {
	Body federationWorkspaceProviderStateRequest
}

type federationWorkspaceProviderSubject struct {
	ID                 string                                        `json:"id"`
	Repository         spokeapi.FederationActivityRepositoryIdentity `json:"repository"`
	ItemType           string                                        `json:"item_type"`
	ItemNumber         int                                           `json:"item_number"`
	AssociatedPRNumber *int                                          `json:"associated_pr_number,omitempty"`
}

type federationWorkspaceProviderStateResponse struct {
	Workspaces []federationWorkspaceProviderState `json:"workspaces" nullable:"false"`
}

type federationWorkspaceProviderState struct {
	ID                 string  `json:"id"`
	ItemLastActivityAt *string `json:"item_last_activity_at,omitempty"`
	MRTitle            *string `json:"mr_title,omitempty"`
	MRState            *string `json:"mr_state,omitempty"`
	MRIsDraft          *bool   `json:"mr_is_draft,omitempty"`
	MRCIStatus         *string `json:"mr_ci_status,omitempty"`
	MRReviewDecision   *string `json:"mr_review_decision,omitempty"`
	MRAdditions        *int    `json:"mr_additions,omitempty"`
	MRDeletions        *int    `json:"mr_deletions,omitempty"`
}

type federationWorkspaceProviderStateOutput = httpapi.BodyOutput[federationWorkspaceProviderStateResponse]

func (s *Server) registerProviderWorkspaceStateAPI(api huma.API) {
	huma.Register(api, huma.Operation{
		OperationID: "federation-query-workspace-provider-state",
		Method:      http.MethodPost,
		Path:        "/federation/provider/workspace-state/query",
		Summary:     "Read hub provider state for a spoke's workspaces",
		Tags:        []string{"Fleet"},
	}, s.federationQueryWorkspaceProviderState)
}

// federationQueryWorkspaceProviderState lets a spoke pull the provider state
// for its own workspaces. It applies the same enrichment the hub uses for
// member snapshots, so a spoke the hub cannot reach still sees its PR state.
func (s *Server) federationQueryWorkspaceProviderState(
	ctx context.Context,
	input *federationWorkspaceProviderStateInput,
) (*federationWorkspaceProviderStateOutput, error) {
	workspaces := make([]fleet.RawWorkspace, 0, len(input.Body.Workspaces))
	for _, subject := range input.Body.Workspaces {
		if strings.TrimSpace(subject.ID) == "" {
			return nil, httpapi.Validation("body.workspaces", "each workspace must have an id")
		}
		workspaces = append(workspaces, fleet.RawWorkspace{
			ID: subject.ID,
			Repository: fleet.RepositoryIdentity{
				Provider:       subject.Repository.Provider,
				PlatformHost:   subject.Repository.PlatformHost,
				PlatformRepoID: subject.Repository.PlatformRepoID,
			},
			ItemType: subject.ItemType, ItemNumber: subject.ItemNumber,
			AssociatedPRNumber: subject.AssociatedPRNumber,
		})
	}
	enriched, err := fleet.EnrichProviderState(
		ctx, s.db, fleet.NeutralSnapshot{Workspaces: workspaces},
	)
	if err != nil {
		return nil, httpapi.Internal("query workspace provider state failed")
	}
	states := make([]federationWorkspaceProviderState, 0, len(enriched.Workspaces))
	for _, workspace := range enriched.Workspaces {
		states = append(states, federationWorkspaceProviderState{
			ID:                 workspace.ID,
			ItemLastActivityAt: workspace.ItemLastActivityAt,
			MRTitle:            workspace.MRTitle, MRState: workspace.MRState,
			MRIsDraft: workspace.MRIsDraft, MRCIStatus: workspace.MRCIStatus,
			MRReviewDecision: workspace.MRReviewDecision,
			MRAdditions:      workspace.MRAdditions, MRDeletions: workspace.MRDeletions,
		})
	}
	return &federationWorkspaceProviderStateOutput{Body: federationWorkspaceProviderStateResponse{
		Workspaces: states,
	}}, nil
}
