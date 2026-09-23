package providerapi

import (
	"context"
	"errors"

	"go.kenn.io/forge/internal/db"
	"go.kenn.io/forge/internal/providerplane"
	"go.kenn.io/forge/internal/server/httpapi"
)

type federationImportReviewDraftInput struct {
	Body db.ProviderStateReviewDraftPayload
}

type federationImportWorkflowStateInput struct {
	Body db.ProviderStateWorkflowPayload
}

type federationProviderStateImportOutput = httpapi.BodyOutput[db.ProviderStateImportResult]

type FederationResolveWorkspaceLaunchSpecInput struct {
	Body providerplane.WorkspaceLaunchRequest
}

type FederationResolveWorkspaceLaunchSpecOutput = httpapi.BodyOutput[db.WorkspaceLaunchSpec]

type FederationRefreshWorkspaceLaunchSpecInput struct {
	Body providerplane.WorkspaceLaunchRequest
}

func (s *Handlers) FederationImportReviewDraft(
	ctx context.Context,
	input *federationImportReviewDraftInput,
) (*federationProviderStateImportOutput, error) {
	result, err := s.Db.ImportProviderReviewDraft(ctx, input.Body)
	if err != nil {
		return nil, providerStateHandoffProblem(err)
	}
	return &federationProviderStateImportOutput{Body: result}, nil
}

func (s *Handlers) FederationImportWorkflowState(
	ctx context.Context,
	input *federationImportWorkflowStateInput,
) (*federationProviderStateImportOutput, error) {
	result, err := s.Db.ImportProviderWorkflowState(ctx, input.Body)
	if err != nil {
		return nil, providerStateHandoffProblem(err)
	}
	return &federationProviderStateImportOutput{Body: result}, nil
}

func providerStateHandoffProblem(err error) error {
	if errors.Is(err, db.ErrSpokePreparationConflict) {
		return httpapi.Conflict(httpapi.CodeConflict, err.Error(), map[string]any{
			"reason": "providerStateConflict",
		})
	}
	return httpapi.BadRequest(httpapi.CodeBadRequest, err.Error(), nil)
}
