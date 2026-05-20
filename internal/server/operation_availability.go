package server

import (
	"fmt"
	"time"

	"github.com/wesm/middleman/internal/db"
	"github.com/wesm/middleman/internal/ratelimit"
)

// Operation names. These string literals are the JSON field names
// of RepoOperations and are part of the wire contract; renaming one
// is a breaking change for clients pinned to an older schema.
const (
	operationMergePR            = "merge_pr"
	operationClosePR            = "close_pr"
	operationReopenPR           = "reopen_pr"
	operationMarkReadyForReview = "mark_ready_for_review"
	operationSubmitReview       = "submit_review"
	operationAddComment         = "add_comment"
	operationAddLabel           = "add_label"
	operationRemoveLabel        = "remove_label"
	operationCloseIssue         = "close_issue"
	operationReopenIssue        = "reopen_issue"
	operationApproveWorkflow    = "approve_workflow"
)

// Availability codes returned to clients. Empty code means available.
const (
	availabilityCodeUnsupportedCapability = "unsupported_capability"
	availabilityCodeViewerCannotMerge     = "viewer_cannot_merge"
	availabilityCodeRateLimited           = "rate_limited"
)

// OperationAvailability is the wire-level shape describing whether
// a write operation can be invoked against a repository right now.
// It collapses the inputs the UI would otherwise have to mirror
// piecemeal: provider capability flags, per-repo viewer permissions,
// and host-wide rate-limit state.
type OperationAvailability struct {
	Available          bool   `json:"available"`
	Code               string `json:"code,omitempty"`
	UnavailableReason  string `json:"unavailable_reason,omitempty"`
	RequiredCapability string `json:"required_capability,omitempty"`
	RetryAt            string `json:"retry_at,omitempty"`
}

// RepoOperations carries the availability of every supported write
// operation for a repository. Naming each operation as a struct
// field (rather than a free-form string-keyed map) makes the set of
// operations part of the OpenAPI contract: clients get typed access
// to each field and adding a new operation requires an explicit
// server change.
type RepoOperations struct {
	MergePR            OperationAvailability `json:"merge_pr"`
	ClosePR            OperationAvailability `json:"close_pr"`
	ReopenPR           OperationAvailability `json:"reopen_pr"`
	MarkReadyForReview OperationAvailability `json:"mark_ready_for_review"`
	SubmitReview       OperationAvailability `json:"submit_review"`
	AddComment         OperationAvailability `json:"add_comment"`
	AddLabel           OperationAvailability `json:"add_label"`
	RemoveLabel        OperationAvailability `json:"remove_label"`
	CloseIssue         OperationAvailability `json:"close_issue"`
	ReopenIssue        OperationAvailability `json:"reopen_issue"`
	ApproveWorkflow    OperationAvailability `json:"approve_workflow"`
}

// operationDescriptor lists the capabilities an operation needs.
// requiredCapabilities is checked in declaration order so the first
// missing capability becomes RequiredCapability, giving deterministic
// behavior when multiple are absent.
type operationDescriptor struct {
	name                 string
	requiredCapabilities []string
}

var (
	descMergePR            = operationDescriptor{name: operationMergePR, requiredCapabilities: []string{capabilityMergeMutation}}
	descClosePR            = operationDescriptor{name: operationClosePR, requiredCapabilities: []string{capabilityStateMutation}}
	descReopenPR           = operationDescriptor{name: operationReopenPR, requiredCapabilities: []string{capabilityStateMutation}}
	descMarkReadyForReview = operationDescriptor{name: operationMarkReadyForReview, requiredCapabilities: []string{capabilityReadyForReview}}
	descSubmitReview       = operationDescriptor{name: operationSubmitReview, requiredCapabilities: []string{capabilityReviewMutation}}
	descAddComment         = operationDescriptor{name: operationAddComment, requiredCapabilities: []string{capabilityCommentMutation}}
	descAddLabel           = operationDescriptor{name: operationAddLabel, requiredCapabilities: []string{capabilityReadLabels, capabilityLabelMutation}}
	descRemoveLabel        = operationDescriptor{name: operationRemoveLabel, requiredCapabilities: []string{capabilityReadLabels, capabilityLabelMutation}}
	descCloseIssue         = operationDescriptor{name: operationCloseIssue, requiredCapabilities: []string{capabilityIssueMutation}}
	descReopenIssue        = operationDescriptor{name: operationReopenIssue, requiredCapabilities: []string{capabilityIssueMutation}}
	descApproveWorkflow    = operationDescriptor{name: operationApproveWorkflow, requiredCapabilities: []string{capabilityWorkflowApproval}}
)

// repoOperations derives the availability of every operation for a
// repo from current provider capabilities, the repo's per-viewer
// merge permission, and the host's REST/GraphQL rate-limit state.
func (s *Server) repoOperations(repo db.Repo) RepoOperations {
	caps := s.capabilitiesForRepo(repo)
	rate := s.rateLimitedReason(repo)
	derive := func(op operationDescriptor) OperationAvailability {
		return deriveOperationAvailability(op, caps, repo, rate)
	}
	return RepoOperations{
		MergePR:            derive(descMergePR),
		ClosePR:            derive(descClosePR),
		ReopenPR:           derive(descReopenPR),
		MarkReadyForReview: derive(descMarkReadyForReview),
		SubmitReview:       derive(descSubmitReview),
		AddComment:         derive(descAddComment),
		AddLabel:           derive(descAddLabel),
		RemoveLabel:        derive(descRemoveLabel),
		CloseIssue:         derive(descCloseIssue),
		ReopenIssue:        derive(descReopenIssue),
		ApproveWorkflow:    derive(descApproveWorkflow),
	}
}

func deriveOperationAvailability(
	op operationDescriptor,
	caps providerCapabilitiesResponse,
	repo db.Repo,
	rate rateLimitAvailability,
) OperationAvailability {
	for _, capability := range op.requiredCapabilities {
		if !capabilityEnabled(caps, capability) {
			return OperationAvailability{
				Code:               availabilityCodeUnsupportedCapability,
				UnavailableReason:  fmt.Sprintf("Provider does not support %s", capability),
				RequiredCapability: capability,
			}
		}
	}
	if op.name == operationMergePR && !repo.ViewerCanMerge {
		return OperationAvailability{
			Code:              availabilityCodeViewerCannotMerge,
			UnavailableReason: "You do not have permission to merge in this repository",
		}
	}
	if rate.limited {
		return OperationAvailability{
			Code:              availabilityCodeRateLimited,
			UnavailableReason: rate.reason,
			RetryAt:           rate.retryAt,
		}
	}
	return OperationAvailability{Available: true}
}

// rateLimitAvailability is the result of consulting the rate
// trackers for a repo's host. limited is true when either the REST
// or GraphQL tracker for the host is paused.
type rateLimitAvailability struct {
	limited bool
	reason  string
	retryAt string
}

func (s *Server) rateLimitedReason(repo db.Repo) rateLimitAvailability {
	if s == nil || s.syncer == nil {
		return rateLimitAvailability{}
	}
	host := repoProviderHost(repo)
	providerName := string(repoProviderKind(repo))
	key := ratelimit.RateBucketKey(providerName, host)

	// Either tracker being paused blocks operations: REST handles
	// the mutation calls themselves, but GraphQL shares the same
	// API budget on GitHub, so its pause also signals that mutating
	// the repo is likely to fail.
	if rt, ok := s.syncer.RateTrackers()[key]; ok && rt != nil && rt.IsPaused() {
		return formatRateLimit(host, rt.ResetAt())
	}
	if rt, ok := s.syncer.GQLRateTrackers()[key]; ok && rt != nil && rt.IsPaused() {
		return formatRateLimit(host, rt.ResetAt())
	}
	return rateLimitAvailability{}
}

func formatRateLimit(host string, resetAt *time.Time) rateLimitAvailability {
	res := rateLimitAvailability{limited: true}
	if resetAt != nil {
		res.retryAt = formatUTCRFC3339(*resetAt)
		res.reason = fmt.Sprintf(
			"%s rate-limited; retry at %s",
			host, resetAt.UTC().Format("15:04"),
		)
		return res
	}
	res.reason = fmt.Sprintf("%s rate-limited", host)
	return res
}
