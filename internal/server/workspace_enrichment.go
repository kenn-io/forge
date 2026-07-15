package server

import (
	"context"
	"log/slog"
	"time"

	"go.kenn.io/middleman/internal/db"
)

const (
	workspaceEnrichmentTTL            = 5 * time.Second
	workspaceEnrichmentRefreshTimeout = 2 * time.Second
	workspaceTmuxPruneInterval        = 5 * time.Second
	workspaceEnrichmentNotApplicable  = "not_applicable"
	workspaceEnrichmentPending        = "pending"
	workspaceEnrichmentFresh          = "fresh"
	workspaceEnrichmentStale          = "stale"
	workspaceEnrichmentFailed         = "failed"
)

type workspaceEnrichmentCacheEntry struct {
	response              workspaceResponse
	hasDivergence         bool
	hasTmux               bool
	divergenceRefreshedAt time.Time
	tmuxRefreshedAt       time.Time
	lastAttemptAt         time.Time
	lastError             string
}

type workspaceEnrichmentJob struct {
	summary    db.WorkspaceSummary
	generation uint64
}

type workspaceEnrichmentProbeResult struct {
	response           workspaceResponse
	divergenceComplete bool
	tmuxComplete       bool
	err                error
}

func (s *Server) toCachedWorkspaceResponse(
	summary *db.WorkspaceSummary,
) workspaceResponse {
	resp := toWorkspaceResponse(summary)
	resp.Repo = s.repoRefFromParts(
		summary.Platform, summary.PlatformHost, summary.RepoOwner, summary.RepoName,
	)
	if s.workspaceEnrichmentDisabled {
		return resp
	}
	if s.workspaces == nil || summary.Status != "ready" {
		return resp
	}

	entry, refreshDue := s.cachedWorkspaceEnrichment(summary.ID)
	resp = s.workspaceResponseFromEnrichmentCacheEntry(summary, entry)
	if refreshDue {
		s.scheduleWorkspaceEnrichment(*summary)
	}
	return resp
}

func (s *Server) workspaceResponseFromEnrichmentCacheEntry(
	summary *db.WorkspaceSummary,
	entry *workspaceEnrichmentCacheEntry,
) workspaceResponse {
	resp := toWorkspaceResponse(summary)
	resp.Repo = s.repoRefFromParts(
		summary.Platform, summary.PlatformHost, summary.RepoOwner, summary.RepoName,
	)
	resp.EnrichmentStatus = workspaceEnrichmentPending
	if entry == nil {
		return resp
	}

	applyWorkspaceEnrichmentCacheEntry(&resp, *entry)
	hasResponse := entry.hasDivergence || entry.hasTmux
	switch {
	case entry.lastError != "":
		resp.EnrichmentStatus = workspaceEnrichmentFailed
		errMessage := entry.lastError
		resp.EnrichmentError = &errMessage
	case hasResponse:
		refreshedAt, _ := entry.oldestRefreshedAt()
		if s.now().Sub(refreshedAt) < workspaceEnrichmentTTL {
			resp.EnrichmentStatus = workspaceEnrichmentFresh
		} else {
			resp.EnrichmentStatus = workspaceEnrichmentStale
		}
	}
	return resp
}

func (entry workspaceEnrichmentCacheEntry) oldestRefreshedAt() (time.Time, bool) {
	var oldest time.Time
	if entry.hasDivergence {
		oldest = entry.divergenceRefreshedAt
	}
	if entry.hasTmux && (oldest.IsZero() || entry.tmuxRefreshedAt.Before(oldest)) {
		oldest = entry.tmuxRefreshedAt
	}
	return oldest, !oldest.IsZero()
}

func applyCachedWorkspaceTmux(
	resp *workspaceResponse,
	cached workspaceResponse,
) {
	resp.TmuxPaneTitle = cached.TmuxPaneTitle
	resp.TmuxWorking = cached.TmuxWorking
	resp.TmuxActivitySource = cached.TmuxActivitySource
	resp.TmuxLastOutputAt = cached.TmuxLastOutputAt
}

func applyWorkspaceEnrichmentCacheEntry(
	resp *workspaceResponse,
	entry workspaceEnrichmentCacheEntry,
) {
	if entry.hasDivergence {
		resp.CommitsAhead = entry.response.CommitsAhead
		resp.CommitsBehind = entry.response.CommitsBehind
	}
	if entry.hasTmux {
		applyCachedWorkspaceTmux(resp, entry.response)
	}
	if refreshedAt, ok := entry.oldestRefreshedAt(); ok {
		formatted := refreshedAt.UTC().Format(time.RFC3339)
		resp.EnrichmentRefreshedAt = &formatted
	}
}

func (s *Server) cachedWorkspaceEnrichment(
	workspaceID string,
) (*workspaceEnrichmentCacheEntry, bool) {
	s.workspaceEnrichmentMu.Lock()
	defer s.workspaceEnrichmentMu.Unlock()

	entry, ok := s.workspaceEnrichmentCache[workspaceID]
	if !ok {
		return nil, true
	}
	copy := entry
	latestAttempt := entry.lastAttemptAt
	if entry.divergenceRefreshedAt.After(latestAttempt) {
		latestAttempt = entry.divergenceRefreshedAt
	}
	if entry.tmuxRefreshedAt.After(latestAttempt) {
		latestAttempt = entry.tmuxRefreshedAt
	}
	return &copy, latestAttempt.IsZero() ||
		s.now().Sub(latestAttempt) >= workspaceEnrichmentTTL
}

func (s *Server) refreshWorkspaceResponse(
	ctx context.Context,
	summary *db.WorkspaceSummary,
) workspaceResponse {
	generation := s.supersedeWorkspaceEnrichment(summary.ID)
	result := s.workspaceResponseWithEnrichment(ctx, summary)
	if summary.Status == "ready" {
		entry, recorded := s.recordWorkspaceEnrichmentResult(
			summary.ID, generation, result,
		)
		return s.workspaceResponseAfterEnrichmentAttempt(
			summary, result, entry, recorded,
		)
	}
	return result.response
}

func (s *Server) workspaceResponseAfterEnrichmentAttempt(
	summary *db.WorkspaceSummary,
	result workspaceEnrichmentProbeResult,
	entry workspaceEnrichmentCacheEntry,
	recorded bool,
) workspaceResponse {
	if !recorded {
		return s.workspaceResponseFromEnrichmentCacheEntry(summary, &entry)
	}
	applyWorkspaceEnrichmentCacheEntry(&result.response, entry)
	return result.response
}

func (s *Server) scheduleWorkspaceEnrichment(summary db.WorkspaceSummary) {
	s.workspaceEnrichmentMu.Lock()
	defer s.workspaceEnrichmentMu.Unlock()
	if s.workspaceEnrichmentGenerations == nil {
		s.workspaceEnrichmentGenerations = make(map[string]uint64)
	}
	if _, ok := s.workspaceEnrichmentGenerations[summary.ID]; !ok {
		s.workspaceEnrichmentGenerations[summary.ID] = 0
	}
	generation := s.workspaceEnrichmentGenerations[summary.ID]
	if inFlight, ok := s.workspaceEnrichmentInFlight[summary.ID]; ok &&
		inFlight == generation {
		return
	}
	if pending, ok := s.workspaceEnrichmentPending[summary.ID]; ok &&
		pending.generation == generation {
		pending.summary = summary
		s.workspaceEnrichmentPending[summary.ID] = pending
		return
	}
	if s.workspaceEnrichmentPending == nil {
		s.workspaceEnrichmentPending = make(map[string]workspaceEnrichmentJob)
	}
	s.workspaceEnrichmentPending[summary.ID] = workspaceEnrichmentJob{
		summary:    summary,
		generation: generation,
	}
	s.startWorkspaceEnrichmentWorkersLocked()
}

func (s *Server) startWorkspaceEnrichmentWorkersLocked() {
	pending := len(s.workspaceEnrichmentPending)
	if s.workspaceTmuxPrunePending {
		pending++
	}
	for s.workspaceEnrichmentWorkers < cap(s.workspaceEnrichmentSlots) &&
		s.workspaceEnrichmentWorkers < pending {
		s.workspaceEnrichmentWorkers++
		if !s.runBackground(s.runWorkspaceEnrichmentWorker) {
			s.workspaceEnrichmentWorkers--
			return
		}
	}
}

func (s *Server) runWorkspaceEnrichmentWorker(ctx context.Context) {
	select {
	case s.workspaceEnrichmentSlots <- struct{}{}:
		defer func() { <-s.workspaceEnrichmentSlots }()
	case <-ctx.Done():
		s.workspaceEnrichmentMu.Lock()
		s.workspaceEnrichmentWorkers--
		s.workspaceEnrichmentMu.Unlock()
		return
	}
	for {
		job, prune, ok := s.nextWorkspaceEnrichmentJob()
		if !ok {
			return
		}
		if prune {
			s.runWorkspaceTmuxPrune(ctx)
			continue
		}
		s.runWorkspaceEnrichmentJob(ctx, job)
	}
}

func (s *Server) nextWorkspaceEnrichmentJob() (
	workspaceEnrichmentJob,
	bool,
	bool,
) {
	s.workspaceEnrichmentMu.Lock()
	defer s.workspaceEnrichmentMu.Unlock()
	if s.workspaceTmuxPrunePending {
		s.workspaceTmuxPrunePending = false
		s.workspaceTmuxPruneInFlight = true
		return workspaceEnrichmentJob{}, true, true
	}
	for workspaceID, job := range s.workspaceEnrichmentPending {
		delete(s.workspaceEnrichmentPending, workspaceID)
		if s.workspaceEnrichmentGenerations[workspaceID] != job.generation {
			continue
		}
		s.workspaceEnrichmentInFlight[workspaceID] = job.generation
		return job, false, true
	}
	s.workspaceEnrichmentWorkers--
	return workspaceEnrichmentJob{}, false, false
}

func (s *Server) runWorkspaceEnrichmentJob(
	ctx context.Context,
	job workspaceEnrichmentJob,
) {
	defer s.finishWorkspaceEnrichment(job.summary.ID, job.generation)
	probeCtx, cancel := context.WithTimeout(
		ctx, workspaceEnrichmentRefreshTimeout,
	)
	defer cancel()
	result := s.workspaceResponseWithEnrichment(probeCtx, &job.summary)
	if _, recorded := s.recordWorkspaceEnrichmentResult(
		job.summary.ID, job.generation, result,
	); recorded {
		s.broadcastWorkspaceStatus(job.summary.ID)
	}
}

func (s *Server) workspaceEnrichmentGeneration(workspaceID string) uint64 {
	s.workspaceEnrichmentMu.Lock()
	defer s.workspaceEnrichmentMu.Unlock()
	return s.workspaceEnrichmentGenerations[workspaceID]
}

func (s *Server) invalidateWorkspaceEnrichment(workspaceID string) uint64 {
	return s.advanceWorkspaceEnrichmentGeneration(workspaceID, false)
}

func (s *Server) supersedeWorkspaceEnrichment(workspaceID string) uint64 {
	return s.advanceWorkspaceEnrichmentGeneration(workspaceID, true)
}

func (s *Server) advanceWorkspaceEnrichmentGeneration(
	workspaceID string,
	preserveCache bool,
) uint64 {
	s.workspaceEnrichmentMu.Lock()
	defer s.workspaceEnrichmentMu.Unlock()
	if s.workspaceEnrichmentGenerations == nil {
		s.workspaceEnrichmentGenerations = make(map[string]uint64)
	}
	generation := s.workspaceEnrichmentGenerations[workspaceID] + 1
	s.workspaceEnrichmentGenerations[workspaceID] = generation
	if !preserveCache {
		delete(s.workspaceEnrichmentCache, workspaceID)
	}
	return generation
}

func (s *Server) recordWorkspaceEnrichmentResult(
	workspaceID string,
	generation uint64,
	result workspaceEnrichmentProbeResult,
) (workspaceEnrichmentCacheEntry, bool) {
	s.workspaceEnrichmentMu.Lock()
	defer s.workspaceEnrichmentMu.Unlock()
	currentGeneration, ok := s.workspaceEnrichmentGenerations[workspaceID]
	if !ok || currentGeneration != generation {
		return s.workspaceEnrichmentCache[workspaceID], false
	}
	now := s.now()
	entry := s.workspaceEnrichmentCache[workspaceID]
	if result.divergenceComplete {
		entry.response.CommitsAhead = result.response.CommitsAhead
		entry.response.CommitsBehind = result.response.CommitsBehind
		entry.hasDivergence = true
		entry.divergenceRefreshedAt = now
	}
	if result.tmuxComplete {
		applyCachedWorkspaceTmux(&entry.response, result.response)
		entry.hasTmux = true
		entry.tmuxRefreshedAt = now
	}
	entry.lastAttemptAt = now
	entry.lastError = ""
	if result.err != nil {
		entry.lastError = result.err.Error()
	}
	s.workspaceEnrichmentCache[workspaceID] = entry
	return entry, true
}

func (s *Server) finishWorkspaceEnrichment(
	workspaceID string,
	generation uint64,
) {
	s.workspaceEnrichmentMu.Lock()
	if s.workspaceEnrichmentInFlight[workspaceID] == generation {
		delete(s.workspaceEnrichmentInFlight, workspaceID)
	}
	s.workspaceEnrichmentMu.Unlock()
}

func (s *Server) trimWorkspaceEnrichmentCache(
	summaries []db.WorkspaceSummary,
) {
	valid := make(map[string]struct{}, len(summaries))
	for i := range summaries {
		valid[summaries[i].ID] = struct{}{}
	}

	s.workspaceEnrichmentMu.Lock()
	defer s.workspaceEnrichmentMu.Unlock()
	for workspaceID := range s.workspaceEnrichmentCache {
		if _, ok := valid[workspaceID]; !ok {
			delete(s.workspaceEnrichmentCache, workspaceID)
			delete(s.workspaceEnrichmentGenerations, workspaceID)
			delete(s.workspaceEnrichmentPending, workspaceID)
		}
	}
	for workspaceID := range s.workspaceEnrichmentPending {
		if _, ok := valid[workspaceID]; !ok {
			delete(s.workspaceEnrichmentPending, workspaceID)
			delete(s.workspaceEnrichmentGenerations, workspaceID)
		}
	}
	for workspaceID := range s.workspaceEnrichmentGenerations {
		if _, ok := valid[workspaceID]; !ok {
			delete(s.workspaceEnrichmentGenerations, workspaceID)
		}
	}
}

func (s *Server) scheduleWorkspaceTmuxPrune() {
	if s.workspaces == nil || s.workspaceEnrichmentDisabled {
		return
	}
	now := s.now()
	s.workspaceEnrichmentMu.Lock()
	if s.workspaceTmuxPrunePending || s.workspaceTmuxPruneInFlight ||
		(!s.workspaceTmuxPrunedAt.IsZero() &&
			now.Sub(s.workspaceTmuxPrunedAt) < workspaceTmuxPruneInterval) {
		s.workspaceEnrichmentMu.Unlock()
		return
	}
	s.workspaceTmuxPrunePending = true
	s.workspaceTmuxPrunedAt = now
	s.startWorkspaceEnrichmentWorkersLocked()
	s.workspaceEnrichmentMu.Unlock()
}

func (s *Server) runWorkspaceTmuxPrune(ctx context.Context) {
	defer func() {
		s.workspaceEnrichmentMu.Lock()
		s.workspaceTmuxPruneInFlight = false
		s.workspaceEnrichmentMu.Unlock()
	}()
	pruneCtx, cancel := context.WithTimeout(
		ctx, workspaceEnrichmentRefreshTimeout,
	)
	defer cancel()
	if err := s.workspaces.PruneMissingTmuxSessions(pruneCtx); err != nil {
		slog.Debug("prune missing tmux sessions", "err", err)
		return
	}
	s.hub.Broadcast(Event{Type: "workspace_status", Data: map[string]string{}})
}
