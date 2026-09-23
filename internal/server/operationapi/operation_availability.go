package operationapi

import (
	"context"
	"errors"
	"fmt"
	"time"

	"go.kenn.io/forge/internal/db"
	ghclient "go.kenn.io/forge/internal/github"
	"go.kenn.io/forge/internal/server/httpapi"
	"go.kenn.io/forge/internal/server/itemapi"
	"go.kenn.io/forge/internal/tokenauth"
	"go.kenn.io/forge/platform"
)

// Operation names. These string literals are the JSON field names
// of httpapi.RepoOperations and are part of the wire contract; renaming one
// is a breaking change for clients pinned to an older schema.
const (
	OperationMergePR               = "merge_pr"
	OperationClosePR               = "close_pr"
	operationReopenPR              = "reopen_pr"
	operationMarkReadyForReview    = "mark_ready_for_review"
	operationMarkDraft             = "mark_draft"
	OperationSubmitReview          = "submit_review"
	operationReviewDraft           = "review_draft"
	operationAddComment            = "add_comment"
	operationEditComment           = "edit_comment"
	operationDeleteComment         = "delete_comment"
	OperationAddLabel              = "add_label"
	operationRemoveLabel           = "remove_label"
	operationSetAssignees          = "set_assignees"
	operationSetReviewers          = "set_reviewers"
	operationCreateIssue           = "create_issue"
	operationCloseIssue            = "close_issue"
	operationReopenIssue           = "reopen_issue"
	operationApproveWorkflow       = "approve_workflow"
	operationDispatchWorkflow      = "dispatch_workflow"
	operationUpdateContent         = "update_content"
	operationReplyReviewThread     = "reply_review_thread"
	operationResolveReviewThread   = "resolve_review_thread"
	operationApplyReviewSuggestion = string(platform.OperationApplyReviewSuggestion)
)

// Availability codes returned to clients. Empty code means available.
const (
	AvailabilityCodeUnsupportedCapability  = "unsupported_capability"
	AvailabilityCodeViewerCannotMerge      = "viewer_cannot_merge"
	AvailabilityCodeRateLimited            = "rate_limited"
	AvailabilityCodeMissingWriteCredential = "missing_write_credential"
	AvailabilityCodeWriteCredentialError   = "write_credential_error"
	AvailabilityCodeSelfApproval           = "self_approval"
)

// apiBucket identifies which API quota an operation consumes. GitHub
// exposes REST and GraphQL as independent budgets, so a pause on one
// must not block operations served by the other.
type ApiBucket int

const (
	ApiBucketREST ApiBucket = iota
	ApiBucketGraphQL
)

// httpapi.OperationAvailability is the wire-level shape describing whether
// a write operation can be invoked against a repository right now.
// It collapses the inputs the UI would otherwise have to mirror
// piecemeal: provider capability flags, per-repo viewer permissions,
// and host-wide rate-limit state.
// httpapi.RepoOperations carries the availability of every supported write
// operation for a repository. Naming each operation as a struct
// field (rather than a free-form string-keyed map) makes the set of
// operations part of the OpenAPI contract: clients get typed access
// to each field and adding a new operation requires an explicit
// server change.
//
// Contract for absent fields: this server always emits every field.
// A client can still observe a missing entry — an older server, or a
// payload that has not loaded yet — and must treat it as "no
// operation-level verdict": the control falls back to its capability
// gating (the pre-operations behavior) rather than disabling. The
// frontend helper frontend/src/lib/components/detail/operation-gates.ts implements this
// side of the contract.
// operationDescriptor lists the capabilities an operation needs and
// the API bucket or buckets it consumes. requiredCapabilities is checked in
// declaration order so the first missing capability becomes
// RequiredCapability, giving deterministic behavior when multiple
// are absent.
type OperationDescriptor struct {
	Name                 string
	RequiredCapabilities []string
	Bucket               ApiBucket
	ExtraBuckets         []ApiBucket
}

type OperationAvailabilityContext struct {
	SelfApproval bool
}

// Mutations are REST-served except ready-for-review, which GitHub
// only exposes as a GraphQL mutation and therefore consumes the
// GraphQL budget. The bucket field keeps each operation gated on the
// rate state of the API it actually calls.
var (
	descMergePR            = OperationDescriptor{Name: OperationMergePR, RequiredCapabilities: []string{itemapi.CapabilityMergeMutation}, Bucket: ApiBucketREST}
	descClosePR            = OperationDescriptor{Name: OperationClosePR, RequiredCapabilities: []string{itemapi.CapabilityStateMutation}, Bucket: ApiBucketREST}
	descReopenPR           = OperationDescriptor{Name: operationReopenPR, RequiredCapabilities: []string{itemapi.CapabilityStateMutation}, Bucket: ApiBucketREST}
	descMarkReadyForReview = OperationDescriptor{Name: operationMarkReadyForReview, RequiredCapabilities: []string{itemapi.CapabilityReadyForReview}, Bucket: ApiBucketGraphQL}
	descMarkDraft          = OperationDescriptor{Name: operationMarkDraft, RequiredCapabilities: []string{itemapi.CapabilityDraftMutation}, Bucket: ApiBucketGraphQL}
	descSubmitReview       = OperationDescriptor{Name: OperationSubmitReview, RequiredCapabilities: []string{itemapi.CapabilityReviewMutation}, Bucket: ApiBucketREST}
	DescReviewDraft        = OperationDescriptor{Name: operationReviewDraft, RequiredCapabilities: []string{itemapi.CapabilityReviewDraftMutation}, Bucket: ApiBucketREST}
	descAddComment         = OperationDescriptor{Name: operationAddComment, RequiredCapabilities: []string{itemapi.CapabilityCommentMutation}, Bucket: ApiBucketREST}
	descEditComment        = OperationDescriptor{Name: operationEditComment, RequiredCapabilities: []string{itemapi.CapabilityCommentMutation}, Bucket: ApiBucketREST}
	descDeleteComment      = OperationDescriptor{Name: operationDeleteComment, RequiredCapabilities: []string{itemapi.CapabilityCommentMutation}, Bucket: ApiBucketREST}
	descAddLabel           = OperationDescriptor{Name: OperationAddLabel, RequiredCapabilities: []string{itemapi.CapabilityReadLabels, itemapi.CapabilityLabelMutation}, Bucket: ApiBucketREST}
	descRemoveLabel        = OperationDescriptor{Name: operationRemoveLabel, RequiredCapabilities: []string{itemapi.CapabilityReadLabels, itemapi.CapabilityLabelMutation}, Bucket: ApiBucketREST}
	descSetAssignees       = OperationDescriptor{Name: operationSetAssignees, RequiredCapabilities: []string{itemapi.CapabilityAssigneeMutation}, Bucket: ApiBucketREST}
	descSetReviewers       = OperationDescriptor{Name: operationSetReviewers, RequiredCapabilities: []string{itemapi.CapabilityReviewerMutation}, Bucket: ApiBucketREST}
	descCreateIssue        = OperationDescriptor{Name: operationCreateIssue, RequiredCapabilities: []string{itemapi.CapabilityIssueMutation}, Bucket: ApiBucketREST}
	descCloseIssue         = OperationDescriptor{Name: operationCloseIssue, RequiredCapabilities: []string{itemapi.CapabilityIssueMutation}, Bucket: ApiBucketREST}
	descReopenIssue        = OperationDescriptor{Name: operationReopenIssue, RequiredCapabilities: []string{itemapi.CapabilityIssueMutation}, Bucket: ApiBucketREST}
	descApproveWorkflow    = OperationDescriptor{Name: operationApproveWorkflow, RequiredCapabilities: []string{itemapi.CapabilityWorkflowApproval}, Bucket: ApiBucketREST}
	DescDispatchWorkflow   = OperationDescriptor{Name: operationDispatchWorkflow, RequiredCapabilities: []string{itemapi.CapabilityReadWorkflows, itemapi.CapabilityWorkflowDispatch}, Bucket: ApiBucketREST}
	// Content edits (PR/issue title, body, task-list writes) ride the
	// state-mutation capability: state_mutation has always meant "can
	// PATCH the item" across providers — state transitions and
	// title/body/content updates share the same provider mutators and
	// the UI has always gated its edit affordances on it (see
	// platform.Capabilities.StateMutation). They consume the REST
	// budget of the routes that serve them; review-thread reply and
	// resolution are REST on every provider that supports them (GitHub
	// replies via REST comments, GitLab discussions via REST).
	descUpdateContent         = OperationDescriptor{Name: operationUpdateContent, RequiredCapabilities: []string{itemapi.CapabilityStateMutation}, Bucket: ApiBucketREST}
	descReplyReviewThread     = OperationDescriptor{Name: operationReplyReviewThread, RequiredCapabilities: []string{itemapi.CapabilityThreadReply}, Bucket: ApiBucketREST}
	descResolveReviewThread   = OperationDescriptor{Name: operationResolveReviewThread, RequiredCapabilities: []string{itemapi.CapabilityReviewThreadResolution}, Bucket: ApiBucketREST}
	DescApplyReviewSuggestion = OperationDescriptor{
		Name: operationApplyReviewSuggestion,
		RequiredCapabilities: []string{
			itemapi.CapabilityReviewSuggestionApplication,
			itemapi.CapabilityMutationHeadBinding,
			itemapi.CapabilityReadReviewThreads,
		},
		Bucket: ApiBucketREST,
	}
)

// repoOperations derives the availability of every operation for a
// repo from current provider capabilities, the repo's per-viewer
// merge permission, and the rate-limit state of the host's API
// buckets. Each operation consults only the bucket it consumes, so
// a paused GraphQL tracker does not block REST-backed operations
// and vice versa.
func (s *Handlers) RepoOperations(repo db.Repo) httpapi.RepoOperations {
	return s.repoOperationsWithContext(repo, OperationAvailabilityContext{})
}

func (s *Handlers) repoOperationsWithContext(
	repo db.Repo,
	opContext OperationAvailabilityContext,
) httpapi.RepoOperations {
	caps := s.RepoResolver.CapabilitiesForRepo(repo)
	writeCred := s.WriteCredentialGateForRepo(repo)
	derive := func(op OperationDescriptor) httpapi.OperationAvailability {
		return DeriveOperationAvailabilityWithContext(
			op, caps, repo, s.mutationOperationRateLimit(repo, op), writeCred, opContext,
		)
	}
	return httpapi.RepoOperations{
		MergePR:               derive(descMergePR),
		ClosePR:               derive(descClosePR),
		ReopenPR:              derive(descReopenPR),
		MarkReadyForReview:    derive(descMarkReadyForReview),
		MarkDraft:             derive(descMarkDraft),
		SubmitReview:          derive(descSubmitReview),
		ReviewDraft:           derive(DescReviewDraft),
		AddComment:            derive(descAddComment),
		EditComment:           derive(descEditComment),
		DeleteComment:         derive(descDeleteComment),
		AddLabel:              derive(descAddLabel),
		RemoveLabel:           derive(descRemoveLabel),
		SetAssignees:          derive(descSetAssignees),
		SetReviewers:          derive(descSetReviewers),
		CreateIssue:           derive(descCreateIssue),
		CloseIssue:            derive(descCloseIssue),
		ReopenIssue:           derive(descReopenIssue),
		ApproveWorkflow:       derive(descApproveWorkflow),
		DispatchWorkflow:      derive(DescDispatchWorkflow),
		UpdateContent:         derive(descUpdateContent),
		ReplyReviewThread:     derive(descReplyReviewThread),
		ResolveReviewThread:   derive(descResolveReviewThread),
		ApplyReviewSuggestion: derive(DescApplyReviewSuggestion),
	}
}

func (s *Handlers) mutationOperationRateLimit(repo db.Repo, op OperationDescriptor) RateLimitAvailability {
	return s.operationRateLimit(repo, op, map[ApiBucket]RateLimitAvailability{
		ApiBucketREST:    s.MutationRateLimitedReason(repo, ApiBucketREST),
		ApiBucketGraphQL: s.MutationRateLimitedReason(repo, ApiBucketGraphQL),
	})
}

func (s *Handlers) RepoOperationsForMergeRequest(
	ctx context.Context,
	repo db.Repo,
	mr db.MergeRequest,
) httpapi.RepoOperations {
	ops := s.repoOperationsWithContext(repo, OperationAvailabilityContext{})
	if !ops.SubmitReview.Available {
		return ops
	}
	if !s.MergeRequestAuthoredByViewer(ctx, repo, mr) {
		return ops
	}
	ops.SubmitReview = selfApprovalUnavailable()
	return ops
}

func DeriveOperationAvailabilityWithContext(
	op OperationDescriptor,
	caps httpapi.ProviderCapabilitiesResponse,
	repo db.Repo,
	rate RateLimitAvailability,
	writeCred WriteCredentialGate,
	opContext OperationAvailabilityContext,
) httpapi.OperationAvailability {
	for _, capability := range op.RequiredCapabilities {
		if !httpapi.CapabilityEnabled(caps, capability) {
			return httpapi.OperationAvailability{
				Code:               AvailabilityCodeUnsupportedCapability,
				UnavailableReason:  fmt.Sprintf("Provider does not support %s", capability),
				RequiredCapability: capability,
			}
		}
	}
	if writeCred.Code != "" {
		return httpapi.OperationAvailability{
			Code:              writeCred.Code,
			UnavailableReason: writeCred.Reason,
		}
	}
	if op.Name == OperationMergePR && !repo.ViewerCanMerge {
		return httpapi.OperationAvailability{
			Code:              AvailabilityCodeViewerCannotMerge,
			UnavailableReason: "You do not have permission to merge in this repository",
		}
	}
	if op.Name == OperationSubmitReview && opContext.SelfApproval {
		return selfApprovalUnavailable()
	}
	if rate.Limited {
		return httpapi.OperationAvailability{
			Code:              AvailabilityCodeRateLimited,
			UnavailableReason: rate.Reason,
			RetryAt:           rate.RetryAt,
		}
	}
	return httpapi.OperationAvailability{Available: true}
}

func selfApprovalUnavailable() httpapi.OperationAvailability {
	return httpapi.OperationAvailability{
		Code:              AvailabilityCodeSelfApproval,
		UnavailableReason: "You cannot approve your own pull request",
	}
}

func (s *Handlers) operationRateLimit(
	repo db.Repo,
	op OperationDescriptor,
	rates map[ApiBucket]RateLimitAvailability,
) RateLimitAvailability {
	buckets, ok := s.OperationRateLimitBuckets(repo, op)
	if !ok {
		return invalidOperationRateLimitBucketReport(op)
	}
	return OperationRateLimitForBuckets(buckets, rates)
}

func ApiBucketsFromPlatform(buckets []platform.RateLimitBucket) ([]ApiBucket, bool) {
	if len(buckets) == 0 {
		return nil, false
	}
	result := make([]ApiBucket, 0, len(buckets))
	for _, bucket := range buckets {
		switch bucket {
		case platform.RateLimitBucketREST:
			result = append(result, ApiBucketREST)
		case platform.RateLimitBucketGraphQL:
			result = append(result, ApiBucketGraphQL)
		default:
			return nil, false
		}
	}
	return result, true
}

func invalidOperationRateLimitBucketReport(op OperationDescriptor) RateLimitAvailability {
	return RateLimitAvailability{
		Limited: true,
		Reason:  fmt.Sprintf("Provider reported invalid rate-limit buckets for %s", op.Name),
	}
}

func (op OperationDescriptor) RateLimitBuckets() []ApiBucket {
	buckets := make([]ApiBucket, 0, 1+len(op.ExtraBuckets))
	buckets = append(buckets, op.Bucket)
	buckets = append(buckets, op.ExtraBuckets...)
	return buckets
}

func OperationRateLimitForBuckets(
	buckets []ApiBucket,
	rates map[ApiBucket]RateLimitAvailability,
) RateLimitAvailability {
	for _, bucket := range buckets {
		if rate := rates[bucket]; rate.Limited {
			return rate
		}
	}
	return RateLimitAvailability{}
}

// rateLimitAvailability is the result of consulting a rate tracker
// for a repo's host. limited is true when the tracker is paused.
type RateLimitAvailability struct {
	Limited bool
	Reason  string
	RetryAt string
}

func OperationRepoRef(repo db.Repo) ghclient.RepoRef {
	return ghclient.RepoRef{
		Platform: httpapi.ProviderKind(repo), PlatformHost: httpapi.ProviderHost(repo),
		Owner: repo.Owner, Name: repo.Name, RepoPath: repo.RepoPath,
	}
}

// writeCredentialProbeTTL bounds how often availability computation
// re-resolves a split host's mutation credential chain. Within the
// TTL the cached verdict answers; afterwards the next request probes
// again, so a user who signs in with the gh CLI or fills a token file
// at runtime sees writes come back without restarting kenn-forge.
const writeCredentialProbeTTL = time.Minute

// writeCredentialProbeErrorTTL caches resolver failures (as opposed
// to a definitively missing credential) for a shorter window: a
// transient gh CLI or filesystem error must not keep writes disabled
// for the full TTL.
const writeCredentialProbeErrorTTL = 15 * time.Second

// writeCredentialProbeTimeout caps a single probe. The chain may shell
// out to the gh CLI, and a hung helper must not stall list endpoints.
const WriteCredentialProbeTimeout = 5 * time.Second

// writeCredentialGate says whether mutations against a host can
// authenticate. An empty code means the mutation chain resolves a
// token.
type WriteCredentialGate struct {
	Code   string
	Reason string
}

type WriteCredentialProbe struct {
	gate      WriteCredentialGate
	checkedAt time.Time
}

func (p WriteCredentialProbe) fresh(now time.Time) bool {
	ttl := writeCredentialProbeTTL
	if p.gate.Code == AvailabilityCodeWriteCredentialError {
		ttl = writeCredentialProbeErrorTTL
	}
	return now.Sub(p.checkedAt) < ttl
}

func (s *Handlers) CachedWriteCredentialGate(
	cacheKey string,
	probe func() WriteCredentialGate,
) WriteCredentialGate {
	for {
		s.WriteCredProbeMu.Lock()
		if probe, ok := (*s.WriteCredProbes)[cacheKey]; ok && probe.fresh((*s.Now)()) {
			s.WriteCredProbeMu.Unlock()
			return probe.gate
		}
		inFlight, ok := (*s.WriteCredProbeInFlight)[cacheKey]
		if !ok {
			break // lock still held; this request runs the probe
		}
		s.WriteCredProbeMu.Unlock()
		<-inFlight
	}
	done := make(chan struct{})
	if (*s.WriteCredProbeInFlight) == nil {
		(*s.WriteCredProbeInFlight) = make(map[string]chan struct{})
	}
	(*s.WriteCredProbeInFlight)[cacheKey] = done
	s.WriteCredProbeMu.Unlock()
	defer func() {
		s.WriteCredProbeMu.Lock()
		delete((*s.WriteCredProbeInFlight), cacheKey)
		s.WriteCredProbeMu.Unlock()
		close(done)
	}()

	gate := probe()

	s.WriteCredProbeMu.Lock()
	if (*s.WriteCredProbes) == nil {
		(*s.WriteCredProbes) = make(map[string]WriteCredentialProbe)
	}
	(*s.WriteCredProbes)[cacheKey] = WriteCredentialProbe{
		gate:      gate,
		checkedAt: (*s.Now)(),
	}
	s.WriteCredProbeMu.Unlock()
	return gate
}

func RouterIdentityRequired(syncer *ghclient.Syncer, repo ghclient.RepoRef) bool {
	return syncer.HasGitHubRouter(repo)
}

// probeWriteCredential resolves the mutation-marked chain once and
// classifies the outcome. A definitively absent credential and a
// resolver failure (gh CLI error, unreadable token file, timeout) get
// distinct codes so the UI never tells the user to configure a PAT
// when the real problem is a broken helper.
func (s *Handlers) ProbeWriteCredential(
	src tokenauth.Source, host string,
) WriteCredentialGate {
	parent := s.BgCtx
	if parent == nil {
		parent = context.Background()
	}
	ctx, cancel := context.WithTimeout(parent, WriteCredentialProbeTimeout)
	defer cancel()
	_, err := src.Token(tokenauth.WithMutationAuth(ctx))
	return WriteCredentialGateFromError(host, err)
}

func WriteCredentialGateFromError(host string, err error) WriteCredentialGate {
	switch {
	case err == nil:
		return WriteCredentialGate{}
	case errors.Is(err, ghclient.ErrIdentityChanged):
		return WriteCredentialGate{
			Code: AvailabilityCodeWriteCredentialError,
			Reason: fmt.Sprintf(
				"The GitHub write credential for %s changed identity; restart kenn-forge to bind the route to the new user.",
				host,
			),
		}
	case errors.Is(err, tokenauth.ErrMissingToken):
		return WriteCredentialGate{
			Code: AvailabilityCodeMissingWriteCredential,
			Reason: fmt.Sprintf(
				"No user credential for writes on %s: the GitHub App token only covers sync reads. Configure a PAT or gh CLI auth.",
				host,
			),
		}
	default:
		return WriteCredentialGate{
			Code: AvailabilityCodeWriteCredentialError,
			Reason: fmt.Sprintf(
				"Resolving the write credential for %s failed: the GitHub App token only covers sync reads. Check the configured token file or gh CLI auth and retry.",
				host,
			),
		}
	}
}

func FormatRateLimit(host string, resetAt *time.Time) RateLimitAvailability {
	res := RateLimitAvailability{
		Limited: true,
		Reason:  fmt.Sprintf("%s rate-limited", host),
	}
	if resetAt != nil {
		res.RetryAt = itemapi.FormatUTCRFC3339(*resetAt)
	}
	return res
}
