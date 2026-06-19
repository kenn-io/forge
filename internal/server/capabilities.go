package server

import (
	"context"

	"github.com/danielgtaylor/huma/v2"
	"go.kenn.io/middleman/internal/db"
)

const (
	capabilityCommentMutation        = "comment_mutation"
	capabilityStateMutation          = "state_mutation"
	capabilityMergeMutation          = "merge_mutation"
	capabilityReviewMutation         = "review_mutation"
	capabilityWorkflowApproval       = "workflow_approval"
	capabilityReadyForReview         = "ready_for_review"
	capabilityDraftMutation          = "draft_mutation"
	capabilityIssueMutation          = "issue_mutation"
	capabilityReadLabels             = "read_labels"
	capabilityLabelMutation          = "label_mutation"
	capabilityAssigneeMutation       = "assignee_mutation"
	capabilityReviewerMutation       = "reviewer_mutation"
	capabilityThreadReply            = "thread_reply"
	capabilityThreadResolve          = "thread_resolve"
	capabilityReviewDraftMutation    = "review_draft_mutation"
	capabilityReviewThreadResolution = "review_thread_resolution"
	capabilityReadReviewThreads      = "read_review_threads"
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
	case capabilityDraftMutation:
		return caps.DraftMutation
	case capabilityIssueMutation:
		return caps.IssueMutation
	case capabilityReadLabels:
		return caps.ReadLabels
	case capabilityLabelMutation:
		return caps.LabelMutation
	case capabilityAssigneeMutation:
		return caps.AssigneeMutation
	case capabilityReviewerMutation:
		return caps.ReviewerMutation
	case capabilityThreadReply:
		return caps.ThreadReply
	case capabilityThreadResolve:
		return caps.ThreadResolve
	case capabilityReviewDraftMutation:
		return caps.ReviewDraftMutation
	case capabilityReviewThreadResolution:
		return caps.ReviewThreadResolution
	case capabilityReadReviewThreads:
		return caps.ReadReviewThreads
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
