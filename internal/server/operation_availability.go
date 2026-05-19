package server

import (
	"fmt"
	"time"

	"github.com/wesm/middleman/internal/db"
	"github.com/wesm/middleman/internal/ratelimit"
)

// Operation names used as keys in the operations map on the wire.
// Adding or renaming a constant here is a breaking wire change; the
// frontend depends on these literals to drive button enablement.
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

// operationDescriptor lists the capabilities an operation needs.
// requiredCapabilities is checked in declaration order so the first
// missing capability becomes RequiredCapability, giving deterministic
// behavior when multiple are absent.
type operationDescriptor struct {
	name                 string
	requiredCapabilities []string
}

func operationCatalog() []operationDescriptor {
	return []operationDescriptor{
		{name: operationMergePR, requiredCapabilities: []string{capabilityMergeMutation}},
		{name: operationClosePR, requiredCapabilities: []string{capabilityStateMutation}},
		{name: operationReopenPR, requiredCapabilities: []string{capabilityStateMutation}},
		{name: operationMarkReadyForReview, requiredCapabilities: []string{capabilityReadyForReview}},
		{name: operationSubmitReview, requiredCapabilities: []string{capabilityReviewMutation}},
		{name: operationAddComment, requiredCapabilities: []string{capabilityCommentMutation}},
		{name: operationAddLabel, requiredCapabilities: []string{capabilityReadLabels, capabilityLabelMutation}},
		{name: operationRemoveLabel, requiredCapabilities: []string{capabilityReadLabels, capabilityLabelMutation}},
		{name: operationCloseIssue, requiredCapabilities: []string{capabilityIssueMutation}},
		{name: operationReopenIssue, requiredCapabilities: []string{capabilityIssueMutation}},
		{name: operationApproveWorkflow, requiredCapabilities: []string{capabilityWorkflowApproval}},
	}
}

// repoOperations derives the operation availability map for a repo
// from current provider capabilities, the repo's per-viewer merge
// permission, and the host's REST/GraphQL rate-limit state.
//
// The set of keys is fixed by operationCatalog() so the client can
// rely on every operation appearing in every response.
func (s *Server) repoOperations(repo db.Repo) map[string]OperationAvailability {
	caps := s.capabilitiesForRepo(repo)
	rate := s.rateLimitedReason(repo)
	catalog := operationCatalog()

	out := make(map[string]OperationAvailability, len(catalog))
	for _, op := range catalog {
		out[op.name] = deriveOperationAvailability(op, caps, repo, rate)
	}
	return out
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
