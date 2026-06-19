package server

import (
	"context"
	"errors"
	"fmt"
	"time"

	"go.kenn.io/middleman/internal/db"
	"go.kenn.io/middleman/internal/ratelimit"
	"go.kenn.io/middleman/internal/tokenauth"
)

// Operation names. These string literals are the JSON field names
// of RepoOperations and are part of the wire contract; renaming one
// is a breaking change for clients pinned to an older schema.
const (
	operationMergePR             = "merge_pr"
	operationClosePR             = "close_pr"
	operationReopenPR            = "reopen_pr"
	operationMarkReadyForReview  = "mark_ready_for_review"
	operationMarkDraft           = "mark_draft"
	operationSubmitReview        = "submit_review"
	operationReviewDraft         = "review_draft"
	operationAddComment          = "add_comment"
	operationEditComment         = "edit_comment"
	operationAddLabel            = "add_label"
	operationRemoveLabel         = "remove_label"
	operationSetAssignees        = "set_assignees"
	operationSetReviewers        = "set_reviewers"
	operationCreateIssue         = "create_issue"
	operationCloseIssue          = "close_issue"
	operationReopenIssue         = "reopen_issue"
	operationApproveWorkflow     = "approve_workflow"
	operationUpdateContent       = "update_content"
	operationReplyReviewThread   = "reply_review_thread"
	operationResolveReviewThread = "resolve_review_thread"
)

// Availability codes returned to clients. Empty code means available.
const (
	availabilityCodeUnsupportedCapability  = "unsupported_capability"
	availabilityCodeViewerCannotMerge      = "viewer_cannot_merge"
	availabilityCodeRateLimited            = "rate_limited"
	availabilityCodeMissingWriteCredential = "missing_write_credential"
	availabilityCodeWriteCredentialError   = "write_credential_error"
)

// apiBucket identifies which API quota an operation consumes. GitHub
// exposes REST and GraphQL as independent budgets, so a pause on one
// must not block operations served by the other.
type apiBucket int

const (
	apiBucketREST apiBucket = iota
	apiBucketGraphQL
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
//
// Contract for absent fields: this server always emits every field.
// A client can still observe a missing entry — an older server, or a
// payload that has not loaded yet — and must treat it as "no
// operation-level verdict": the control falls back to its capability
// gating (the pre-operations behavior) rather than disabling. The
// frontend helper packages/ui/.../operation-gates.ts implements this
// side of the contract.
type RepoOperations struct {
	MergePR             OperationAvailability `json:"merge_pr"`
	ClosePR             OperationAvailability `json:"close_pr"`
	ReopenPR            OperationAvailability `json:"reopen_pr"`
	MarkReadyForReview  OperationAvailability `json:"mark_ready_for_review"`
	MarkDraft           OperationAvailability `json:"mark_draft"`
	SubmitReview        OperationAvailability `json:"submit_review"`
	ReviewDraft         OperationAvailability `json:"review_draft"`
	AddComment          OperationAvailability `json:"add_comment"`
	EditComment         OperationAvailability `json:"edit_comment"`
	AddLabel            OperationAvailability `json:"add_label"`
	RemoveLabel         OperationAvailability `json:"remove_label"`
	SetAssignees        OperationAvailability `json:"set_assignees"`
	SetReviewers        OperationAvailability `json:"set_reviewers"`
	CreateIssue         OperationAvailability `json:"create_issue"`
	CloseIssue          OperationAvailability `json:"close_issue"`
	ReopenIssue         OperationAvailability `json:"reopen_issue"`
	ApproveWorkflow     OperationAvailability `json:"approve_workflow"`
	UpdateContent       OperationAvailability `json:"update_content"`
	ReplyReviewThread   OperationAvailability `json:"reply_review_thread"`
	ResolveReviewThread OperationAvailability `json:"resolve_review_thread"`
}

// operationDescriptor lists the capabilities an operation needs and
// the API bucket it consumes. requiredCapabilities is checked in
// declaration order so the first missing capability becomes
// RequiredCapability, giving deterministic behavior when multiple
// are absent.
type operationDescriptor struct {
	name                 string
	requiredCapabilities []string
	bucket               apiBucket
}

// Mutations are REST-served except ready-for-review, which GitHub
// only exposes as a GraphQL mutation and therefore consumes the
// GraphQL budget. The bucket field keeps each operation gated on the
// rate state of the API it actually calls.
var (
	descMergePR            = operationDescriptor{name: operationMergePR, requiredCapabilities: []string{capabilityMergeMutation}, bucket: apiBucketREST}
	descClosePR            = operationDescriptor{name: operationClosePR, requiredCapabilities: []string{capabilityStateMutation}, bucket: apiBucketREST}
	descReopenPR           = operationDescriptor{name: operationReopenPR, requiredCapabilities: []string{capabilityStateMutation}, bucket: apiBucketREST}
	descMarkReadyForReview = operationDescriptor{name: operationMarkReadyForReview, requiredCapabilities: []string{capabilityReadyForReview}, bucket: apiBucketGraphQL}
	descMarkDraft          = operationDescriptor{name: operationMarkDraft, requiredCapabilities: []string{capabilityDraftMutation}, bucket: apiBucketGraphQL}
	descSubmitReview       = operationDescriptor{name: operationSubmitReview, requiredCapabilities: []string{capabilityReviewMutation}, bucket: apiBucketREST}
	descReviewDraft        = operationDescriptor{name: operationReviewDraft, requiredCapabilities: []string{capabilityReviewDraftMutation}, bucket: apiBucketREST}
	descAddComment         = operationDescriptor{name: operationAddComment, requiredCapabilities: []string{capabilityCommentMutation}, bucket: apiBucketREST}
	descEditComment        = operationDescriptor{name: operationEditComment, requiredCapabilities: []string{capabilityCommentMutation}, bucket: apiBucketREST}
	descAddLabel           = operationDescriptor{name: operationAddLabel, requiredCapabilities: []string{capabilityReadLabels, capabilityLabelMutation}, bucket: apiBucketREST}
	descRemoveLabel        = operationDescriptor{name: operationRemoveLabel, requiredCapabilities: []string{capabilityReadLabels, capabilityLabelMutation}, bucket: apiBucketREST}
	descSetAssignees       = operationDescriptor{name: operationSetAssignees, requiredCapabilities: []string{capabilityAssigneeMutation}, bucket: apiBucketREST}
	descSetReviewers       = operationDescriptor{name: operationSetReviewers, requiredCapabilities: []string{capabilityReviewerMutation}, bucket: apiBucketREST}
	descCreateIssue        = operationDescriptor{name: operationCreateIssue, requiredCapabilities: []string{capabilityIssueMutation}, bucket: apiBucketREST}
	descCloseIssue         = operationDescriptor{name: operationCloseIssue, requiredCapabilities: []string{capabilityIssueMutation}, bucket: apiBucketREST}
	descReopenIssue        = operationDescriptor{name: operationReopenIssue, requiredCapabilities: []string{capabilityIssueMutation}, bucket: apiBucketREST}
	descApproveWorkflow    = operationDescriptor{name: operationApproveWorkflow, requiredCapabilities: []string{capabilityWorkflowApproval}, bucket: apiBucketREST}
	// Content edits (PR/issue title, body, task-list writes) ride the
	// state-mutation capability: state_mutation has always meant "can
	// PATCH the item" across providers — state transitions and
	// title/body/content updates share the same provider mutators and
	// the UI has always gated its edit affordances on it (see
	// platform.Capabilities.StateMutation). They consume the REST
	// budget of the routes that serve them; review-thread reply and
	// resolution are REST on every provider that supports them (GitHub
	// replies via REST comments, GitLab discussions via REST).
	descUpdateContent       = operationDescriptor{name: operationUpdateContent, requiredCapabilities: []string{capabilityStateMutation}, bucket: apiBucketREST}
	descReplyReviewThread   = operationDescriptor{name: operationReplyReviewThread, requiredCapabilities: []string{capabilityThreadReply}, bucket: apiBucketREST}
	descResolveReviewThread = operationDescriptor{name: operationResolveReviewThread, requiredCapabilities: []string{capabilityReviewThreadResolution}, bucket: apiBucketREST}
)

// repoOperations derives the availability of every operation for a
// repo from current provider capabilities, the repo's per-viewer
// merge permission, and the rate-limit state of the host's API
// buckets. Each operation consults only the bucket it consumes, so
// a paused GraphQL tracker does not block REST-backed operations
// and vice versa.
func (s *Server) repoOperations(repo db.Repo) RepoOperations {
	caps := s.capabilitiesForRepo(repo)
	rates := map[apiBucket]rateLimitAvailability{
		apiBucketREST:    s.mutationRateLimitedReason(repo, apiBucketREST),
		apiBucketGraphQL: s.mutationRateLimitedReason(repo, apiBucketGraphQL),
	}
	writeCred := s.writeCredentialGateForRepo(repo)
	derive := func(op operationDescriptor) OperationAvailability {
		return deriveOperationAvailability(
			op, caps, repo, rates[op.bucket], writeCred,
		)
	}
	return RepoOperations{
		MergePR:             derive(descMergePR),
		ClosePR:             derive(descClosePR),
		ReopenPR:            derive(descReopenPR),
		MarkReadyForReview:  derive(descMarkReadyForReview),
		MarkDraft:           derive(descMarkDraft),
		SubmitReview:        derive(descSubmitReview),
		ReviewDraft:         derive(descReviewDraft),
		AddComment:          derive(descAddComment),
		EditComment:         derive(descEditComment),
		AddLabel:            derive(descAddLabel),
		RemoveLabel:         derive(descRemoveLabel),
		SetAssignees:        derive(descSetAssignees),
		SetReviewers:        derive(descSetReviewers),
		CreateIssue:         derive(descCreateIssue),
		CloseIssue:          derive(descCloseIssue),
		ReopenIssue:         derive(descReopenIssue),
		ApproveWorkflow:     derive(descApproveWorkflow),
		UpdateContent:       derive(descUpdateContent),
		ReplyReviewThread:   derive(descReplyReviewThread),
		ResolveReviewThread: derive(descResolveReviewThread),
	}
}

func deriveOperationAvailability(
	op operationDescriptor,
	caps providerCapabilitiesResponse,
	repo db.Repo,
	rate rateLimitAvailability,
	writeCred writeCredentialGate,
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
	if writeCred.code != "" {
		return OperationAvailability{
			Code:              writeCred.code,
			UnavailableReason: writeCred.reason,
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

// rateLimitAvailability is the result of consulting a rate tracker
// for a repo's host. limited is true when the tracker is paused.
type rateLimitAvailability struct {
	limited bool
	reason  string
	retryAt string
}

// mutationRateLimitedReason resolves the rate state gating a write
// operation. Every operation in RepoOperations is a mutation, and
// mutations authenticate with the host's write credential (the user's
// PAT when a GitHub App handles sync reads). When the host has a
// dedicated write tracker for the bucket the operation consumes, that
// tracker alone decides: an exhausted app budget must not disable
// PAT-backed writes, and PAT exhaustion must surface even though sync
// reads still flow. Hosts without write trackers share one credential
// across reads and writes, so the sync bucket tracker keeps gating.
func (s *Server) mutationRateLimitedReason(
	repo db.Repo, bucket apiBucket,
) rateLimitAvailability {
	if s == nil || s.syncer == nil {
		return rateLimitAvailability{}
	}
	host := repoProviderHost(repo)
	key := ratelimit.RateBucketKey(string(repoProviderKind(repo)), host)
	var writeTrackers map[string]*ratelimit.RateTracker
	switch bucket {
	case apiBucketREST:
		writeTrackers = s.syncer.WriteRateTrackers()
	case apiBucketGraphQL:
		writeTrackers = s.syncer.WriteGQLRateTrackers()
	default:
		return rateLimitAvailability{}
	}
	if wt, ok := writeTrackers[key]; ok && wt != nil {
		if wt.IsPaused() {
			return formatRateLimit(host, wt.ResetAt())
		}
		return rateLimitAvailability{}
	}
	return s.rateLimitedReason(repo, bucket)
}

func (s *Server) rateLimitedReason(repo db.Repo, bucket apiBucket) rateLimitAvailability {
	if s == nil || s.syncer == nil {
		return rateLimitAvailability{}
	}
	host := repoProviderHost(repo)
	providerName := string(repoProviderKind(repo))
	key := ratelimit.RateBucketKey(providerName, host)

	var trackers map[string]*ratelimit.RateTracker
	switch bucket {
	case apiBucketREST:
		trackers = s.syncer.RateTrackers()
	case apiBucketGraphQL:
		trackers = s.syncer.GQLRateTrackers()
	default:
		return rateLimitAvailability{}
	}
	if rt, ok := trackers[key]; ok && rt != nil && rt.IsPaused() {
		return formatRateLimit(host, rt.ResetAt())
	}
	return rateLimitAvailability{}
}

// writeCredentialProbeTTL bounds how often availability computation
// re-resolves a split host's mutation credential chain. Within the
// TTL the cached verdict answers; afterwards the next request probes
// again, so a user who signs in with the gh CLI or fills a token file
// at runtime sees writes come back without restarting middleman.
const writeCredentialProbeTTL = time.Minute

// writeCredentialProbeErrorTTL caches resolver failures (as opposed
// to a definitively missing credential) for a shorter window: a
// transient gh CLI or filesystem error must not keep writes disabled
// for the full TTL.
const writeCredentialProbeErrorTTL = 15 * time.Second

// writeCredentialProbeTimeout caps a single probe. The chain may shell
// out to the gh CLI, and a hung helper must not stall list endpoints.
const writeCredentialProbeTimeout = 5 * time.Second

// writeCredentialGate says whether mutations against a host can
// authenticate. An empty code means the mutation chain resolves a
// token.
type writeCredentialGate struct {
	code   string
	reason string
}

type writeCredentialProbe struct {
	gate      writeCredentialGate
	checkedAt time.Time
}

func (p writeCredentialProbe) fresh(now time.Time) bool {
	ttl := writeCredentialProbeTTL
	if p.gate.code == availabilityCodeWriteCredentialError {
		ttl = writeCredentialProbeErrorTTL
	}
	return now.Sub(p.checkedAt) < ttl
}

// writeCredentialGateForRepo reports why mutations against repo's
// host cannot authenticate, or the zero gate when they can. Only
// split hosts can be in this state: when a GitHub App serves sync
// reads, mutations deliberately skip the app candidate so writes stay
// attributed to the user, and a host configured with only the app
// would accept the operation in the UI and then fail auth at request
// time. Probing the mutation-marked chain surfaces that before the UI
// offers the action. Shared-credential hosts are exempt — sync reads
// already exercise the same credential mutations would use.
//
// Results are cached per (host, canonical chain): a config reload
// that re-points the host's credential chain misses the cache
// immediately, while the TTL covers changes behind an unchanged chain
// (a token file rewritten in place, a gh CLI login). Cold-cache
// probes are single-flighted so concurrent list requests share one
// resolution instead of each shelling out to the gh CLI.
func (s *Server) writeCredentialGateForRepo(repo db.Repo) writeCredentialGate {
	if s == nil || s.tokenSources == nil {
		return writeCredentialGate{}
	}
	key := tokenauth.Key{
		Platform: string(repoProviderKind(repo)),
		Host:     repoProviderHost(repo),
	}
	src, ok := s.tokenSources.Get(key)
	if !ok || src == nil {
		return writeCredentialGate{}
	}
	desc := src.Descriptor()
	if !desc.HasActiveGitHubApp() {
		return writeCredentialGate{}
	}
	cacheKey := key.String() + "\x00" + desc.CanonicalSourceString()

	for {
		s.writeCredProbeMu.Lock()
		if probe, ok := s.writeCredProbes[cacheKey]; ok && probe.fresh(s.now()) {
			s.writeCredProbeMu.Unlock()
			return probe.gate
		}
		inFlight, ok := s.writeCredProbeInFlight[cacheKey]
		if !ok {
			break // lock still held; this request runs the probe
		}
		s.writeCredProbeMu.Unlock()
		<-inFlight
	}
	done := make(chan struct{})
	if s.writeCredProbeInFlight == nil {
		s.writeCredProbeInFlight = make(map[string]chan struct{})
	}
	s.writeCredProbeInFlight[cacheKey] = done
	s.writeCredProbeMu.Unlock()
	defer func() {
		s.writeCredProbeMu.Lock()
		delete(s.writeCredProbeInFlight, cacheKey)
		s.writeCredProbeMu.Unlock()
		close(done)
	}()

	gate := s.probeWriteCredential(src, key.Host)

	s.writeCredProbeMu.Lock()
	if s.writeCredProbes == nil {
		s.writeCredProbes = make(map[string]writeCredentialProbe)
	}
	s.writeCredProbes[cacheKey] = writeCredentialProbe{
		gate:      gate,
		checkedAt: s.now(),
	}
	s.writeCredProbeMu.Unlock()
	return gate
}

// probeWriteCredential resolves the mutation-marked chain once and
// classifies the outcome. A definitively absent credential and a
// resolver failure (gh CLI error, unreadable token file, timeout) get
// distinct codes so the UI never tells the user to configure a PAT
// when the real problem is a broken helper.
func (s *Server) probeWriteCredential(
	src tokenauth.Source, host string,
) writeCredentialGate {
	parent := s.bgCtx
	if parent == nil {
		parent = context.Background()
	}
	ctx, cancel := context.WithTimeout(parent, writeCredentialProbeTimeout)
	defer cancel()
	_, err := src.Token(tokenauth.WithMutationAuth(ctx))
	switch {
	case err == nil:
		return writeCredentialGate{}
	case errors.Is(err, tokenauth.ErrMissingToken):
		return writeCredentialGate{
			code: availabilityCodeMissingWriteCredential,
			reason: fmt.Sprintf(
				"No user credential for writes on %s: the GitHub App token only covers sync reads. Configure a PAT or gh CLI auth.",
				host,
			),
		}
	default:
		return writeCredentialGate{
			code: availabilityCodeWriteCredentialError,
			reason: fmt.Sprintf(
				"Resolving the write credential for %s failed: the GitHub App token only covers sync reads. Check the configured token file or gh CLI auth and retry.",
				host,
			),
		}
	}
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
