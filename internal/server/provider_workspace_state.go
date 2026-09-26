package server

import (
	"context"
	"net/http"
	"strings"

	"github.com/danielgtaylor/huma/v2"

	"go.kenn.io/forge/internal/apiclient/generated"
	"go.kenn.io/forge/internal/federationauth"
	"go.kenn.io/forge/internal/fleet"
	"go.kenn.io/forge/internal/server/httpapi"
)

const maxWorkspaceProviderStateSubjects = 500

type federationWorkspaceProviderStateRequest struct {
	Workspaces []federationWorkspaceProviderSubject `json:"workspaces" nullable:"false" maxItems:"500"`
}

type federationWorkspaceProviderStateInput struct {
	Body federationWorkspaceProviderStateRequest
}

type federationWorkspaceProviderSubject struct {
	ID                 string                               `json:"id"`
	Repository         federationActivityRepositoryIdentity `json:"repository"`
	ItemType           string                               `json:"item_type"`
	ItemNumber         int                                  `json:"item_number"`
	AssociatedPRNumber *int                                 `json:"associated_pr_number,omitempty"`
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

// WorkspaceProviderState returns a copy of workspaces with the hub's provider
// state applied. Workspaces without a stable repository identity have no
// provider item the hub can resolve, so they are returned unchanged.
func (s *hubProviderSource) WorkspaceProviderState(
	ctx context.Context, workspaces []fleet.RawWorkspace,
) ([]fleet.RawWorkspace, error) {
	out := append([]fleet.RawWorkspace(nil), workspaces...)
	subjects := make([]generated.FederationWorkspaceProviderSubject, 0, len(workspaces))
	for _, workspace := range workspaces {
		if workspace.Repository.PlatformRepoID == 0 {
			continue
		}
		subject := generated.FederationWorkspaceProviderSubject{
			ID: workspace.ID,
			Repository: generated.FederationActivityRepositoryIdentity{
				Provider:       workspace.Repository.Provider,
				PlatformHost:   workspace.Repository.PlatformHost,
				PlatformRepoID: workspace.Repository.PlatformRepoID,
			},
			ItemType: workspace.ItemType, ItemNumber: int64(workspace.ItemNumber),
		}
		if workspace.AssociatedPRNumber != nil {
			subject.AssociatedPrNumber = new(int64(*workspace.AssociatedPRNumber))
		}
		subjects = append(subjects, subject)
	}
	states := make(map[string]federationWorkspaceProviderState, len(subjects))
	for start := 0; start < len(subjects); start += maxWorkspaceProviderStateSubjects {
		end := min(start+maxWorkspaceProviderStateSubjects, len(subjects))
		body := &generated.FederationWorkspaceProviderStateRequest{Workspaces: subjects[start:end]}
		httpRequest, err := generated.NewFederationQueryWorkspaceProviderStateRequest(ctx, "/api/v1", &generated.FederationQueryWorkspaceProviderStateRequestOptions{Body: body})
		if err != nil {
			return nil, err
		}
		var response federationWorkspaceProviderStateResponse
		if err := s.exchange(ctx, federationauth.ScopeProviderRead, httpRequest, &response); err != nil {
			return nil, err
		}
		for _, state := range response.Workspaces {
			states[state.ID] = state
		}
	}
	for index := range out {
		state, ok := states[out[index].ID]
		if !ok {
			continue
		}
		workspace := &out[index]
		workspace.ItemLastActivityAt = state.ItemLastActivityAt
		workspace.MRTitle, workspace.MRState = state.MRTitle, state.MRState
		workspace.MRIsDraft, workspace.MRCIStatus = state.MRIsDraft, state.MRCIStatus
		workspace.MRReviewDecision = state.MRReviewDecision
		workspace.MRAdditions, workspace.MRDeletions = state.MRAdditions, state.MRDeletions
	}
	return out, nil
}
