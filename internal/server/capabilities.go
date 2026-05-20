package server

import (
	"context"

	"github.com/danielgtaylor/huma/v2"
	"github.com/wesm/middleman/internal/db"
)

const (
	capabilityCommentMutation  = "comment_mutation"
	capabilityStateMutation    = "state_mutation"
	capabilityMergeMutation    = "merge_mutation"
	capabilityReviewMutation   = "review_mutation"
	capabilityWorkflowApproval = "workflow_approval"
	capabilityReadyForReview   = "ready_for_review"
	capabilityIssueMutation    = "issue_mutation"
	capabilityReadLabels       = "read_labels"
	capabilityLabelMutation    = "label_mutation"
)

func capabilityEnabled(
	caps providerCapabilitiesResponse,
	capability string,
) bool {
	switch capability {
	case capabilityCommentMutation:
		return caps.CommentMutation
	case capabilityStateMutation:
		return caps.StateMutation
	case capabilityMergeMutation:
		return caps.MergeMutation
	case capabilityReviewMutation:
		return caps.ReviewMutation
	case capabilityWorkflowApproval:
		return caps.WorkflowApproval
	case capabilityReadyForReview:
		return caps.ReadyForReview
	case capabilityIssueMutation:
		return caps.IssueMutation
	case capabilityReadLabels:
		return caps.ReadLabels
	case capabilityLabelMutation:
		return caps.LabelMutation
	default:
		return false
	}
}

// unsupportedCapabilityProblem is a thin alias for
// problemUnsupportedCapability so that handler files which already use
// this name from outside requireRepoRouteCapability don't need to import
// problems.go's helper by its new name. Both spellings are kept for
// readability at the call sites.
func unsupportedCapabilityProblem(repo db.Repo, capability string) huma.StatusError {
	return problemUnsupportedCapability(repo, capability)
}

func (s *Server) requireSyncerCapability(repo db.Repo, capability string) error {
	if s.syncer == nil {
		return unsupportedCapabilityProblem(repo, capability)
	}
	return nil
}

func (s *Server) requireRepoRouteCapability(
	ctx context.Context,
	provider, platformHost, owner, name, capability string,
) (*db.Repo, error) {
	repo, err := s.lookupRepoByProviderRoute(
		ctx, provider, platformHost, owner, name,
	)
	if err != nil {
		return nil, providerRouteLookupError(err)
	}
	if !capabilityEnabled(s.capabilitiesForRepo(*repo), capability) {
		return nil, problemUnsupportedCapability(*repo, capability)
	}
	return repo, nil
}

// Compile-time guard that huma is imported even after the migration
// removed the direct ErrorDetail/StatusError usage from this file. The
// huma_routes.go file still imports huma so this is belt-and-suspenders.
var _ huma.StatusError = (*ProblemError)(nil)
