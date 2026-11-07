package pullapi

import (
	"context"

	"go.kenn.io/forge/internal/server/httpapi"
)

type getPullsInput struct {
	Body struct {
		Items []ItemIdentity `json:"items" nullable:"false"`
	}
}

func (s *Handler) getPulls(ctx context.Context, input *getPullsInput) (*httpapi.BodyOutput[[]MergeRequestDetailResponse], error) {
	details, err := s.getProviderPulls(ctx, input.Body.Items)
	if err != nil {
		return nil, err
	}
	return &httpapi.BodyOutput[[]MergeRequestDetailResponse]{Body: details}, nil
}
