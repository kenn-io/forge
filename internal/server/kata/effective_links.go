package kata

import (
	"context"
	"sort"
	"time"

	"go.kenn.io/forge/internal/db"
)

const (
	kataLinkHydrationBatchSize            = 200
	kataLinkHydrationGlobalConcurrency    = 4
	kataLinkHydrationPerDaemonConcurrency = 2
	kataLinkHydrationTimeout              = 5 * time.Second
)

type kataLinkProvenance string

const (
	kataLinkDirect    kataLinkProvenance = "direct"
	kataLinkInherited kataLinkProvenance = "inherited"
	kataLinkIntrinsic kataLinkProvenance = "intrinsic"
)

type kataEffectiveLink struct {
	DaemonID          string                       `json:"daemon_id"`
	ProjectUID        string                       `json:"project_uid"`
	ProjectName       string                       `json:"project_name,omitempty"`
	IssueUID          string                       `json:"issue_uid"`
	Reference         string                       `json:"reference,omitempty"`
	Title             string                       `json:"title,omitempty"`
	Status            string                       `json:"status,omitempty"`
	Provenance        []kataLinkProvenance         `json:"provenance"`
	DirectLinkID      *int64                       `json:"direct_link_id,omitempty"`
	DaemonHealth      string                       `json:"daemon_health"`
	APISchemaVersion  string                       `json:"api_schema_version,omitempty"`
	Workspace         *KataWorkspaceTargetResponse `json:"workspace,omitempty"`
	UnavailableReason string                       `json:"unavailable_reason,omitempty"`
}

type kataLinkDiagnostic struct {
	DaemonID string `json:"daemon_id"`
	Reason   string `json:"reason"`
}

type kataEffectiveLinksResponse struct {
	State       string               `json:"state" enum:"complete,partial,unavailable"`
	Links       []kataEffectiveLink  `json:"links" nullable:"false"`
	Diagnostics []kataLinkDiagnostic `json:"diagnostics" nullable:"false"`
}

type kataLinkCandidate struct {
	DaemonID     string
	ProjectUID   string
	IssueUID     string
	Provenance   map[kataLinkProvenance]struct{}
	DirectLinkID *int64
}

type kataLinkHydrationResult struct {
	links       []kataEffectiveLink
	diagnostics []kataLinkDiagnostic
	hydrated    int
}

func (h *Handler) hydrateDirectKataLinks(
	ctx context.Context,
	links []db.KataIssueLink,
) kataEffectiveLinksResponse {
	candidates := make(map[string]*kataLinkCandidate)
	addStoredKataLinkCandidates(candidates, links, kataLinkDirect, true)
	return h.hydrateKataLinkCandidates(ctx, candidates)
}

func (h *Handler) hydrateKataLinkCandidates(
	ctx context.Context,
	candidates map[string]*kataLinkCandidate,
) kataEffectiveLinksResponse {
	response := kataEffectiveLinksResponse{
		State: "complete", Links: []kataEffectiveLink{}, Diagnostics: []kataLinkDiagnostic{},
	}
	byDaemon := make(map[string][]kataLinkCandidate)
	for _, candidate := range candidates {
		byDaemon[candidate.DaemonID] = append(byDaemon[candidate.DaemonID], *candidate)
	}
	daemonIDs := make([]string, 0, len(byDaemon))
	for daemonID := range byDaemon {
		daemonIDs = append(daemonIDs, daemonID)
	}
	sort.Strings(daemonIDs)
	results := make(chan kataLinkHydrationResult, len(candidates)/kataLinkHydrationBatchSize+len(daemonIDs))
	jobCount := 0
	hydratedCount := 0
	for _, daemonID := range daemonIDs {
		sort.Slice(byDaemon[daemonID], func(i, j int) bool {
			return byDaemon[daemonID][i].IssueUID < byDaemon[daemonID][j].IssueUID
		})
		globalSlots, daemonSlots := h.kataLinkHydrationSlots(daemonID)
		for start := 0; start < len(byDaemon[daemonID]); start += kataLinkHydrationBatchSize {
			end := min(start+kataLinkHydrationBatchSize, len(byDaemon[daemonID]))
			batch := append([]kataLinkCandidate(nil), byDaemon[daemonID][start:end]...)
			jobCount++
			go h.hydrateKataLinkBatch(
				ctx, daemonID, batch, globalSlots, daemonSlots, results,
			)
		}
	}
	for range jobCount {
		result := <-results
		response.Links = append(response.Links, result.links...)
		response.Diagnostics = append(response.Diagnostics, result.diagnostics...)
		hydratedCount += result.hydrated
	}
	if len(response.Diagnostics) > 0 {
		if len(candidates) > 0 && hydratedCount == 0 {
			response.State = "unavailable"
		} else {
			response.State = "partial"
		}
	}
	sort.Slice(response.Links, func(i, j int) bool {
		left, right := response.Links[i], response.Links[j]
		if left.DaemonID != right.DaemonID {
			return left.DaemonID < right.DaemonID
		}
		if left.Reference != right.Reference {
			return left.Reference < right.Reference
		}
		return left.IssueUID < right.IssueUID
	})
	sort.Slice(response.Diagnostics, func(i, j int) bool {
		left, right := response.Diagnostics[i], response.Diagnostics[j]
		if left.DaemonID != right.DaemonID {
			return left.DaemonID < right.DaemonID
		}
		return left.Reason < right.Reason
	})
	response.Diagnostics = deduplicateKataLinkDiagnostics(response.Diagnostics)
	return response
}

func (h *Handler) kataLinkHydrationSlots(daemonID string) (chan struct{}, chan struct{}) {
	h.kataLinkHydrationMu.Lock()
	defer h.kataLinkHydrationMu.Unlock()
	daemonSlots := h.kataLinkHydrationDaemonSlots[daemonID]
	if daemonSlots == nil {
		daemonSlots = make(chan struct{}, kataLinkHydrationPerDaemonConcurrency)
		h.kataLinkHydrationDaemonSlots[daemonID] = daemonSlots
	}
	return h.kataLinkHydrationGlobalSlots, daemonSlots
}

func deduplicateKataLinkDiagnostics(
	diagnostics []kataLinkDiagnostic,
) []kataLinkDiagnostic {
	if len(diagnostics) < 2 {
		return diagnostics
	}
	deduplicated := diagnostics[:1]
	for _, diagnostic := range diagnostics[1:] {
		previous := deduplicated[len(deduplicated)-1]
		if diagnostic != previous {
			deduplicated = append(deduplicated, diagnostic)
		}
	}
	return deduplicated
}

func (h *Handler) hydrateKataLinkBatch(
	ctx context.Context,
	daemonID string,
	links []kataLinkCandidate,
	globalSlots, daemonSlots chan struct{},
	results chan<- kataLinkHydrationResult,
) {
	if !acquireKataHydrationSlot(ctx, daemonSlots) {
		results <- h.unavailableKataHydrationResult(ctx, links, "request cancelled", "")
		return
	}
	defer func() { <-daemonSlots }()
	if !acquireKataHydrationSlot(ctx, globalSlots) {
		results <- h.unavailableKataHydrationResult(ctx, links, "request cancelled", "")
		return
	}
	defer func() { <-globalSlots }()

	batchCtx, cancel := context.WithTimeout(ctx, kataLinkHydrationTimeout)
	defer cancel()
	response := kataEffectiveLinksResponse{
		State: "complete", Links: []kataEffectiveLink{}, Diagnostics: []kataLinkDiagnostic{},
	}
	hydrated := h.hydrateKataDaemonLinks(ctx, batchCtx, daemonID, links, &response)
	results <- kataLinkHydrationResult{
		links: response.Links, diagnostics: response.Diagnostics, hydrated: hydrated,
	}
}

func acquireKataHydrationSlot(ctx context.Context, slots chan struct{}) bool {
	select {
	case slots <- struct{}{}:
		return true
	case <-ctx.Done():
		return false
	}
}

func (h *Handler) unavailableKataHydrationResult(
	ctx context.Context,
	links []kataLinkCandidate,
	reason, health string,
) kataLinkHydrationResult {
	response := kataEffectiveLinksResponse{
		State: "partial", Links: []kataEffectiveLink{}, Diagnostics: []kataLinkDiagnostic{},
	}
	h.appendUnavailableKataLinks(ctx, &response, links, reason, health)
	return kataLinkHydrationResult{links: response.Links, diagnostics: response.Diagnostics}
}

func (h *Handler) hydrateKataDaemonLinks(
	ctx, upstreamCtx context.Context,
	daemonID string,
	links []kataLinkCandidate,
	response *kataEffectiveLinksResponse,
) int {
	client, problem := h.kataClientForDaemon(daemonID)
	if problem != nil {
		reason := "daemon unavailable"
		if problem.Detail != "" {
			reason = problem.Detail
		}
		h.appendUnavailableKataLinks(ctx, response, links, reason, "")
		return 0
	}
	health, err := client.Health(upstreamCtx)
	if err != nil || health.State != "connected" {
		reason := "daemon unavailable"
		if health.State == "incompatible" {
			reason = kataDaemonCompatibilityMessage(health.APISchemaVersion)
		}
		h.appendUnavailableKataLinks(ctx, response, links, reason, health.State)
		return 0
	}
	issueUIDs := make([]string, 0, len(links))
	for _, link := range links {
		issueUIDs = append(issueUIDs, link.IssueUID)
	}
	references, err := client.References(upstreamCtx, kataReferenceQuery{IssueUIDs: issueUIDs, Limit: len(issueUIDs)})
	if err != nil {
		h.appendUnavailableKataLinks(ctx, response, links, "reference read failed", health.State)
		return 0
	}
	byUID := make(map[string]kataIssueReference, len(references))
	for _, reference := range references {
		byUID[reference.UID] = reference
	}
	hydrated := 0
	for _, stored := range links {
		link := kataEffectiveLink{
			DaemonID: daemonID, ProjectUID: stored.ProjectUID, IssueUID: stored.IssueUID,
			Provenance: sortedKataLinkProvenance(stored.Provenance), DirectLinkID: stored.DirectLinkID,
			DaemonHealth: health.State, APISchemaVersion: health.APISchemaVersion,
		}
		reference, ok := byUID[stored.IssueUID]
		if !ok {
			link.UnavailableReason = "task unavailable"
			workspace, workspaceErr := h.kataExistingWorkspaceTargetForIssue(ctx, daemonID, stored.IssueUID)
			if workspaceErr == nil {
				link.Workspace = workspace
			}
			response.State = "partial"
			response.Diagnostics = append(response.Diagnostics, kataLinkDiagnostic{
				DaemonID: daemonID, Reason: "task unavailable",
			})
			response.Links = append(response.Links, link)
			continue
		}
		hydrated++
		link.ProjectUID = reference.ProjectUID
		link.ProjectName = reference.ProjectName
		link.Reference = reference.QualifiedID
		link.Title = reference.Title
		link.Status = reference.Status
		workspace, err := h.kataWorkspaceTargetForMetadata(ctx, db.WorkspaceKataMetadata{
			DaemonID: daemonID, ProjectUID: reference.ProjectUID,
			ProjectName: reference.ProjectName, IssueUID: reference.UID,
			ShortID: reference.ShortID, QualifiedID: reference.QualifiedID, Title: reference.Title,
		})
		if err != nil {
			if workspace.ResolutionStatus == "" {
				workspace = unavailableKataWorkspaceTarget("error", "")
			}
			response.State = "partial"
			response.Diagnostics = append(response.Diagnostics, kataLinkDiagnostic{
				DaemonID: daemonID, Reason: "workspace resolution failed",
			})
		}
		link.Workspace = &workspace
		response.Links = append(response.Links, link)
	}
	return hydrated
}

func (h *Handler) appendUnavailableKataLinks(
	ctx context.Context,
	response *kataEffectiveLinksResponse,
	links []kataLinkCandidate,
	reason, health string,
) {
	response.State = "partial"
	if len(links) > 0 {
		response.Diagnostics = append(response.Diagnostics, kataLinkDiagnostic{
			DaemonID: links[0].DaemonID, Reason: reason,
		})
	}
	for _, stored := range links {
		link := kataEffectiveLink{
			DaemonID: stored.DaemonID, ProjectUID: stored.ProjectUID, IssueUID: stored.IssueUID,
			Provenance: sortedKataLinkProvenance(stored.Provenance), DirectLinkID: stored.DirectLinkID,
			DaemonHealth: health, UnavailableReason: reason,
		}
		workspace, err := h.kataExistingWorkspaceTargetForIssue(ctx, stored.DaemonID, stored.IssueUID)
		if err == nil {
			link.Workspace = workspace
		}
		response.Links = append(response.Links, link)
	}
}

func addStoredKataLinkCandidates(
	candidates map[string]*kataLinkCandidate,
	links []db.KataIssueLink,
	provenance kataLinkProvenance,
	direct bool,
) {
	for _, link := range links {
		candidate := mergeKataLinkCandidate(candidates, link.DaemonID, link.ProjectUID, link.IssueUID, provenance)
		if direct {
			linkID := link.ID
			candidate.DirectLinkID = &linkID
		}
	}
}

func mergeKataLinkCandidate(
	candidates map[string]*kataLinkCandidate,
	daemonID, projectUID, issueUID string,
	provenance kataLinkProvenance,
) *kataLinkCandidate {
	key := daemonID + "\x00" + issueUID
	candidate := candidates[key]
	if candidate == nil {
		candidate = &kataLinkCandidate{
			DaemonID: daemonID, ProjectUID: projectUID, IssueUID: issueUID,
			Provenance: make(map[kataLinkProvenance]struct{}),
		}
		candidates[key] = candidate
	}
	if candidate.ProjectUID == "" {
		candidate.ProjectUID = projectUID
	}
	candidate.Provenance[provenance] = struct{}{}
	return candidate
}

func sortedKataLinkProvenance(values map[kataLinkProvenance]struct{}) []kataLinkProvenance {
	ordered := make([]kataLinkProvenance, 0, len(values))
	for _, provenance := range []kataLinkProvenance{kataLinkIntrinsic, kataLinkDirect, kataLinkInherited} {
		if _, ok := values[provenance]; ok {
			ordered = append(ordered, provenance)
		}
	}
	return ordered
}
