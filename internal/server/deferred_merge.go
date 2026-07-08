package server

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strconv"
	"strings"
	"sync"
	"time"

	"go.kenn.io/middleman/internal/db"
	ghclient "go.kenn.io/middleman/internal/github"
	"go.kenn.io/middleman/internal/platform"
)

const (
	deferredMergePollInterval   = time.Minute
	defaultDeferredMergeMaxWait = 24 * time.Hour
)

type deferredMergeCheckKey struct {
	App  string
	Name string
}

// deferredMergeHandle tracks one queued background merge. A successful
// user-initiated immediate merge supersedes the queued worker: the worker
// must stand down silently instead of later broadcasting a misleading
// "no longer open" failure for a pull request the maintainer just merged.
type deferredMergeHandle struct {
	superseded chan struct{}
	once       sync.Once
}

func newDeferredMergeHandle() *deferredMergeHandle {
	return &deferredMergeHandle{superseded: make(chan struct{})}
}

func (h *deferredMergeHandle) supersede() {
	h.once.Do(func() { close(h.superseded) })
}

func (h *deferredMergeHandle) isSuperseded() bool {
	select {
	case <-h.superseded:
		return true
	default:
		return false
	}
}

type deferredMergeTargetSnapshot struct {
	HeadSHA    string
	BaseBranch string
	BaseSHA    string
}

type deferredMergeCompletedPayload struct {
	Provider     string `json:"provider"`
	PlatformHost string `json:"platform_host"`
	RepoPath     string `json:"repo_path"`
	Owner        string `json:"owner"`
	Name         string `json:"name"`
	Number       int    `json:"number"`
	HeadSHA      string `json:"head_sha"`
	Status       string `json:"status"`
	Merged       bool   `json:"merged,omitempty"`
	SHA          string `json:"sha,omitempty"`
	Message      string `json:"message,omitempty"`
	Error        string `json:"error,omitempty"`
	CompletedAt  string `json:"completed_at"`
}

func (s *Server) deferMergePR(
	ctx context.Context,
	input *deferMergePRInput,
) (*deferMergePROutput, error) {
	body, err := s.enqueueDeferredMerge(ctx, input.Provider, input.PlatformHost, input.Owner, input.Name, input.Number, input.Body, deferredMergePollInterval, s.deferredMergeMaxWait)
	if err != nil {
		return nil, err
	}
	return &deferMergePROutput{Status: 202, Body: body}, nil
}

func (s *Server) enqueueDeferredMerge(
	ctx context.Context,
	provider string,
	platformHost string,
	owner string,
	name string,
	number int,
	body mergePRInputBody,
	pollInterval time.Duration,
	maxWait time.Duration,
) (deferMergePRBody, error) {
	if pollInterval <= 0 {
		pollInterval = deferredMergePollInterval
	}
	if maxWait <= 0 {
		maxWait = defaultDeferredMergeMaxWait
	}
	repo, err := s.requireRepoRouteCapability(
		ctx,
		provider, platformHost, owner, name,
		capabilityMergeMutation,
	)
	if err != nil {
		return deferMergePRBody{}, err
	}
	if err := s.requireSyncerCapability(*repo, capabilityMergeMutation); err != nil {
		return deferMergePRBody{}, err
	}
	mr, err := s.db.GetMergeRequestByRepoIDAndNumber(ctx, repo.ID, number)
	if err != nil {
		return deferMergePRBody{}, problemInternal("get pull request failed")
	}
	if mr == nil {
		return deferMergePRBody{}, problemNotFound(CodePullNotFound, "pull request not found", nil)
	}
	if mr.State != db.MergeRequestStateOpen {
		return deferMergePRBody{}, problemConflict(
			CodeConflict,
			"pull request is not open",
			map[string]any{"reason": "not_open"},
		)
	}
	expectedHeadSHA, err := s.preflightMergePR(repo, mr, number, body)
	if err != nil {
		return deferMergePRBody{}, err
	}
	queuedTarget := deferredMergeTargetSnapshotFromMR(mr)
	if strings.TrimSpace(expectedHeadSHA) != "" {
		queuedTarget.HeadSHA = expectedHeadSHA
	}
	if strings.TrimSpace(queuedTarget.BaseSHA) == "" {
		return deferMergePRBody{}, problemConflict(
			CodeConflict,
			"target base commit has not been synced; refresh and retry",
			map[string]any{"reason": "base_unknown"},
		)
	}
	pendingKeys, err := pendingDeferredMergeCheckKeys(mr.CIChecksJSON)
	if err != nil {
		return deferMergePRBody{}, problemValidation("ci_checks", err.Error())
	}
	aggregateState := deferredMergeAggregateState(mr.CIStatus)
	if aggregateState == "failed" {
		return deferMergePRBody{}, problemConflict(
			CodeConflict,
			"CI checks have already failed",
			map[string]any{"reason": "ci_failed"},
		)
	}
	if len(pendingKeys) == 0 && aggregateState != "pending" {
		refreshed, refreshedKeys, err := s.refreshPendingDeferredMergeCheckKeys(ctx, *repo, number, queuedTarget)
		if err != nil {
			return deferMergePRBody{}, err
		}
		pendingKeys = refreshedKeys
		aggregateState = deferredMergeAggregateState(refreshed.CIStatus)
		if aggregateState == "failed" {
			return deferMergePRBody{}, problemConflict(
				CodeConflict,
				"CI checks have already failed",
				map[string]any{"reason": "ci_failed"},
			)
		}
	}
	if len(pendingKeys) == 0 && aggregateState != "pending" {
		return deferMergePRBody{}, problemConflict(
			CodeConflict,
			"no pending CI checks to wait for",
			map[string]any{"reason": "no_pending_checks"},
		)
	}
	key := deferredMergeKey(*repo, number)
	handle, marked := s.markDeferredMergeInFlight(key)
	if !marked {
		return deferMergePRBody{}, problemConflict(
			CodeConflict,
			"a deferred merge is already waiting for this pull request",
			map[string]any{"reason": "already_pending"},
		)
	}
	started := s.runBackground(func(bgCtx context.Context) {
		defer s.clearDeferredMergeInFlight(key, handle)
		s.runDeferredMerge(bgCtx, *repo, number, body, pendingKeys, queuedTarget, pollInterval, maxWait, handle)
	})
	if !started {
		s.clearDeferredMergeInFlight(key, handle)
		return deferMergePRBody{}, problemServiceUnavailable("server is shutting down")
	}
	return deferMergePRBody{
		Status:        "queued",
		PendingChecks: len(pendingKeys),
	}, nil
}

func (s *Server) runDeferredMerge(
	ctx context.Context,
	repo db.Repo,
	number int,
	body mergePRInputBody,
	pendingKeys []deferredMergeCheckKey,
	queuedTarget deferredMergeTargetSnapshot,
	pollInterval time.Duration,
	maxWait time.Duration,
	handle *deferredMergeHandle,
) {
	if maxWait <= 0 {
		maxWait = defaultDeferredMergeMaxWait
	}
	ticker := time.NewTicker(pollInterval)
	defer ticker.Stop()
	timeout := time.NewTimer(maxWait)
	defer timeout.Stop()
	for {
		state, err := s.refreshDeferredMergeCI(ctx, repo, number, pendingKeys, queuedTarget)
		if err != nil {
			if errors.Is(err, errDeferredMergeTargetMerged) {
				return
			}
			s.broadcastDeferredMergeFailure(repo, number, deferredMergeHeadSHA(body, queuedTarget.HeadSHA), err.Error(), handle)
			return
		}
		switch state {
		case "passed":
			s.completeDeferredMerge(ctx, repo, number, body, queuedTarget, handle)
			return
		case "failed":
			s.broadcastDeferredMergeFailure(repo, number, deferredMergeHeadSHA(body, queuedTarget.HeadSHA), "a current CI check failed; merge was not performed", handle)
			return
		case "unknown":
			s.broadcastDeferredMergeFailure(repo, number, deferredMergeHeadSHA(body, queuedTarget.HeadSHA), "aggregate CI status is unavailable after refresh; merge was not performed", handle)
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-handle.superseded:
			// The pull request was merged through the immediate path while
			// this worker waited; there is nothing left to do and no failure
			// to report.
			return
		case <-timeout.C:
			s.broadcastDeferredMergeFailure(repo, number, deferredMergeHeadSHA(body, queuedTarget.HeadSHA), "timed out waiting for pending CI checks to finish; merge was not performed", handle)
			return
		case <-ticker.C:
		}
	}
}

func (s *Server) refreshDeferredMergeCI(
	ctx context.Context,
	repo db.Repo,
	number int,
	pendingKeys []deferredMergeCheckKey,
	queuedTarget deferredMergeTargetSnapshot,
) (string, error) {
	mr, err := s.db.GetMergeRequestByRepoIDAndNumber(ctx, repo.ID, number)
	if err != nil {
		return "", err
	}
	if mr == nil {
		return "", errors.New("pull request no longer exists")
	}
	if err := deferredMergeTargetMatchesDB(mr, queuedTarget); err != nil {
		return "", err
	}
	if err := deferredMergeRequireOpenDB(mr); err != nil {
		return "", err
	}
	warnings, err := s.syncer.RefreshMRCIStatusOnProvider(
		ctx,
		deferredMergeRepoRef(repo),
		repo.ID,
		number,
		queuedTarget.HeadSHA,
	)
	if err != nil {
		return "", err
	}
	if len(warnings) > 0 {
		return "", errors.New("could not refresh CI checks; deferred merge was not performed: " + strings.Join(warnings, "; "))
	}
	refreshed, err := s.db.GetMergeRequestByRepoIDAndNumber(ctx, repo.ID, number)
	if err != nil {
		return "", err
	}
	if refreshed == nil {
		return "", errors.New("pull request no longer exists after CI refresh")
	}
	if err := deferredMergeTargetMatchesDB(refreshed, queuedTarget); err != nil {
		return "", err
	}
	if err := deferredMergeRequireOpenDB(refreshed); err != nil {
		return "", err
	}
	s.hub.Broadcast(Event{
		Type: "pr_ci_refreshed",
		Data: prCIRefreshedPayload{
			Provider:     string(repoProviderKind(repo)),
			PlatformHost: repoProviderHost(repo),
			RepoPath:     repo.RepoPath,
			Owner:        repo.Owner,
			Name:         repo.Name,
			Number:       number,
			HeadSHA:      refreshed.PlatformHeadSHA,
			RefreshedAt:  formatUTCRFC3339(s.now().UTC()),
			Warnings:     []string{},
		},
	})
	return deferredMergeCheckState(refreshed.CIStatus, pendingKeys, refreshed.CIChecksJSON)
}

func (s *Server) refreshPendingDeferredMergeCheckKeys(
	ctx context.Context,
	repo db.Repo,
	number int,
	queuedTarget deferredMergeTargetSnapshot,
) (*db.MergeRequest, []deferredMergeCheckKey, error) {
	warnings, err := s.syncer.RefreshMRCIStatusOnProvider(
		ctx,
		deferredMergeRepoRef(repo),
		repo.ID,
		number,
		queuedTarget.HeadSHA,
	)
	if err != nil {
		return nil, nil, providerCallProblemWithDetail(
			err,
			string(repoProviderKind(repo)), repoProviderHost(repo),
			"refresh PR CI before deferring merge: "+err.Error(),
		)
	}
	if len(warnings) > 0 {
		return nil, nil, problemConflict(
			CodeConflict,
			"could not refresh CI checks before deferring merge",
			map[string]any{"reason": "ci_refresh_unavailable", "warnings": warnings},
		)
	}
	refreshed, err := s.db.GetMergeRequestByRepoIDAndNumber(ctx, repo.ID, number)
	if err != nil {
		return nil, nil, problemInternal("get pull request after CI refresh failed")
	}
	if refreshed == nil {
		return nil, nil, problemNotFound(CodePullNotFound, "pull request not found after CI refresh", nil)
	}
	if err := deferredMergeTargetMatchesDB(refreshed, queuedTarget); err != nil {
		return nil, nil, problemConflict(
			CodeConflict,
			err.Error(),
			map[string]any{"reason": "stale_state"},
		)
	}
	keys, err := pendingDeferredMergeCheckKeys(refreshed.CIChecksJSON)
	if err != nil {
		return nil, nil, problemValidation("ci_checks", err.Error())
	}
	return refreshed, keys, nil
}

func deferredMergeRepoRef(repo db.Repo) ghclient.RepoRef {
	return ghclient.RepoRef{
		Platform:           repoProviderKind(repo),
		Owner:              repo.Owner,
		Name:               repo.Name,
		PlatformHost:       repoProviderHost(repo),
		RepoPath:           repo.RepoPath,
		PlatformExternalID: repo.PlatformRepoID,
		WebURL:             repo.WebURL,
		CloneURL:           repo.CloneURL,
		DefaultBranch:      repo.DefaultBranch,
	}
}

func (s *Server) completeDeferredMerge(
	ctx context.Context,
	repo db.Repo,
	number int,
	body mergePRInputBody,
	queuedTarget deferredMergeTargetSnapshot,
	handle *deferredMergeHandle,
) {
	if err := s.ensureDeferredMergeTargetUnchanged(ctx, repo, number, queuedTarget); err != nil {
		if errors.Is(err, errDeferredMergeTargetMerged) {
			return
		}
		s.broadcastDeferredMergeFailure(repo, number, deferredMergeHeadSHA(body, queuedTarget.HeadSHA), err.Error(), handle)
		return
	}
	result, err := s.mergePRWithBody(ctx, string(repoProviderKind(repo)), repoProviderHost(repo), repo.Owner, repo.Name, number, body)
	if err != nil {
		s.broadcastDeferredMergeFailure(repo, number, deferredMergeHeadSHA(body, queuedTarget.HeadSHA), err.Error(), handle)
		return
	}
	// Clear pending before announcing completion: clients refresh detail the
	// moment they see deferred_merge_completed, and that refresh must not
	// read a stale deferred_merge_pending=true.
	s.clearDeferredMergeInFlight(deferredMergeKey(repo, number), handle)
	s.hub.Broadcast(Event{Type: "data_changed", Data: struct{}{}})
	s.hub.Broadcast(Event{
		Type: "deferred_merge_completed",
		Data: deferredMergeCompletedPayload{
			Provider:     string(repoProviderKind(repo)),
			PlatformHost: repoProviderHost(repo),
			RepoPath:     repo.RepoPath,
			Owner:        repo.Owner,
			Name:         repo.Name,
			Number:       number,
			HeadSHA:      deferredMergeHeadSHA(body, queuedTarget.HeadSHA),
			Status:       "merged",
			Merged:       result.Merged,
			SHA:          result.SHA,
			Message:      result.Message,
			CompletedAt:  formatUTCRFC3339(s.now().UTC()),
		},
	})
}

func (s *Server) ensureDeferredMergeTargetUnchanged(ctx context.Context, repo db.Repo, number int, queuedTarget deferredMergeTargetSnapshot) error {
	mr, err := s.db.GetMergeRequestByRepoIDAndNumber(ctx, repo.ID, number)
	if err != nil {
		return err
	}
	if mr == nil {
		return errors.New("pull request no longer exists")
	}
	if err := deferredMergeTargetMatchesDB(mr, queuedTarget); err != nil {
		return err
	}
	if err := deferredMergeRequireOpenDB(mr); err != nil {
		return err
	}
	reader, err := s.syncer.Registry().MergeRequestReader(repoProviderKind(repo), repoProviderHost(repo))
	if err != nil {
		return err
	}
	current, err := reader.GetMergeRequest(ctx, platformRepoRefFromDB(repo), number)
	if err != nil {
		return err
	}
	if err := deferredMergeTargetMatchesProvider(current, queuedTarget); err != nil {
		return err
	}
	if err := deferredMergeRequireOpenProvider(current); err != nil {
		return err
	}
	return nil
}

func deferredMergeTargetSnapshotFromMR(mr *db.MergeRequest) deferredMergeTargetSnapshot {
	if mr == nil {
		return deferredMergeTargetSnapshot{}
	}
	return deferredMergeTargetSnapshot{
		HeadSHA:    strings.TrimSpace(mr.PlatformHeadSHA),
		BaseBranch: strings.TrimSpace(mr.BaseBranch),
		BaseSHA:    strings.TrimSpace(mr.PlatformBaseSHA),
	}
}

func deferredMergeTargetMatchesDB(mr *db.MergeRequest, queued deferredMergeTargetSnapshot) error {
	if mr == nil {
		return errors.New("pull request no longer exists")
	}
	return deferredMergeTargetMatches(
		queued,
		strings.TrimSpace(mr.PlatformHeadSHA),
		strings.TrimSpace(mr.BaseBranch),
		strings.TrimSpace(mr.PlatformBaseSHA),
	)
}

func deferredMergeTargetMatchesProvider(mr platform.MergeRequest, queued deferredMergeTargetSnapshot) error {
	return deferredMergeTargetMatches(
		queued,
		strings.TrimSpace(mr.HeadSHA),
		strings.TrimSpace(mr.BaseBranch),
		strings.TrimSpace(mr.BaseSHA),
	)
}

// errDeferredMergeTargetMerged marks a queued deferred merge whose pull
// request was already merged through another path (an immediate merge or an
// external merge observed via sync). The worker stands down silently on it: a
// "failed" event for a pull request that ended up merged is misleading. The
// supersede handle cannot cover this alone — the worker syncs provider state
// independently, so it can observe the merge before supersedeDeferredMerge
// runs in the immediate-merge path.
var errDeferredMergeTargetMerged = errors.New("pull request was already merged; deferred merge has nothing left to do")

// deferredMergeRequireOpenDB fails a deferred merge whose target is no longer
// open in the local snapshot. Closing a pull request is the only cancel a user
// has for a queued deferred merge, so the background worker must abort once the
// close has synced rather than merge a pull request the maintainer retracted.
// A merged target returns errDeferredMergeTargetMerged instead of a failure.
func deferredMergeRequireOpenDB(mr *db.MergeRequest) error {
	if mr == nil {
		return errors.New("pull request no longer exists")
	}
	if mr.State == db.MergeRequestStateMerged {
		return errDeferredMergeTargetMerged
	}
	if mr.State != db.MergeRequestStateOpen {
		return errors.New("pull request is no longer open; deferred merge was not performed")
	}
	return nil
}

// deferredMergeRequireOpenProvider re-checks open state against the provider
// immediately before merging. This is the authoritative gate: the local row can
// lag a close until the next sync, and a closed pull request that is reopened
// with the same head must not be silently merged by the queued worker.
// A merged target returns errDeferredMergeTargetMerged instead of a failure.
func deferredMergeRequireOpenProvider(mr platform.MergeRequest) error {
	state := strings.TrimSpace(mr.State)
	if strings.EqualFold(state, string(db.MergeRequestStateMerged)) {
		return errDeferredMergeTargetMerged
	}
	if !strings.EqualFold(state, string(db.MergeRequestStateOpen)) {
		return errors.New("pull request is no longer open; deferred merge was not performed")
	}
	return nil
}

func deferredMergeTargetMatches(queued deferredMergeTargetSnapshot, headSHA, baseBranch, baseSHA string) error {
	if strings.TrimSpace(queued.HeadSHA) != "" && headSHA != queued.HeadSHA {
		return errors.New("target changed since deferred merge was queued; refresh and retry")
	}
	if strings.TrimSpace(queued.BaseBranch) != "" && baseBranch != queued.BaseBranch {
		return errors.New("target changed since deferred merge was queued; refresh and retry")
	}
	if strings.TrimSpace(queued.BaseSHA) != "" && baseSHA != queued.BaseSHA {
		return errors.New("target changed since deferred merge was queued; refresh and retry")
	}
	return nil
}

func deferredMergeHeadSHA(body mergePRInputBody, queuedHeadSHA string) string {
	if strings.TrimSpace(body.ExpectedHeadSHA) != "" {
		return body.ExpectedHeadSHA
	}
	return queuedHeadSHA
}

func (s *Server) broadcastDeferredMergeFailure(repo db.Repo, number int, headSHA string, message string, handle *deferredMergeHandle) {
	// A superseded worker lost its pull request to a successful immediate
	// merge; reporting a deferred-merge failure for it would be misleading.
	if handle != nil && handle.isSuperseded() {
		return
	}
	// Clear pending before announcing the failure, for the same
	// refresh-on-event ordering reason as the success path.
	s.clearDeferredMergeInFlight(deferredMergeKey(repo, number), handle)
	slog.Warn("deferred merge failed",
		"provider", repoProviderKind(repo),
		"platform_host", repoProviderHost(repo),
		"repo_path", repo.RepoPath,
		"number", number,
		"err", message,
	)
	s.hub.Broadcast(Event{
		Type: "deferred_merge_completed",
		Data: deferredMergeCompletedPayload{
			Provider:     string(repoProviderKind(repo)),
			PlatformHost: repoProviderHost(repo),
			RepoPath:     repo.RepoPath,
			Owner:        repo.Owner,
			Name:         repo.Name,
			Number:       number,
			HeadSHA:      headSHA,
			Status:       "failed",
			Error:        message,
			CompletedAt:  formatUTCRFC3339(s.now().UTC()),
		},
	})
}

// decodeCIChecks decodes a merge request's cached ci_checks_json into CICheck
// values. An empty or whitespace-only string yields no checks and no error.
func decodeCIChecks(checksJSON string) ([]db.CICheck, error) {
	if strings.TrimSpace(checksJSON) == "" {
		return nil, nil
	}
	var checks []db.CICheck
	if err := json.Unmarshal([]byte(checksJSON), &checks); err != nil {
		return nil, err
	}
	return checks, nil
}

func pendingDeferredMergeCheckKeys(checksJSON string) ([]deferredMergeCheckKey, error) {
	checks, err := decodeCIChecks(checksJSON)
	if err != nil {
		return nil, err
	}
	keys := make([]deferredMergeCheckKey, 0)
	for _, check := range checks {
		if check.Status != "completed" {
			keys = append(keys, deferredMergeCheckKey{App: check.App, Name: check.Name})
		}
	}
	return keys, nil
}

func deferredMergeCheckState(aggregateStatus string, keys []deferredMergeCheckKey, checksJSON string) (string, error) {
	aggregateState := deferredMergeAggregateState(aggregateStatus)
	if aggregateState == "failed" {
		return "failed", nil
	}
	checks, err := decodeCIChecks(checksJSON)
	if err != nil {
		return "", err
	}
	// Middleman does not have a provider-neutral required-check model. Deferred
	// merge therefore fails closed: the checks that were pending when queued
	// must pass, and the current refreshed snapshot must contain no failed or
	// still-pending checks before the merge is attempted.
	byKey := make(map[deferredMergeCheckKey]db.CICheck, len(checks))
	currentPending := false
	for _, check := range checks {
		byKey[deferredMergeCheckKey{App: check.App, Name: check.Name}] = check
		if check.Status != "completed" {
			currentPending = true
			continue
		}
		switch check.Conclusion {
		case "success", "neutral", "skipped":
		default:
			return "failed", nil
		}
	}
	if aggregateState == "unknown" {
		return "unknown", nil
	}
	pending := false
	for _, key := range keys {
		check, ok := byKey[key]
		if !ok {
			pending = true
			continue
		}
		if check.Status != "completed" {
			pending = true
			continue
		}
		switch check.Conclusion {
		case "success", "neutral", "skipped":
		default:
			return "failed", nil
		}
	}
	if pending {
		return "pending", nil
	}
	if currentPending {
		return "pending", nil
	}
	if aggregateState != "passed" {
		return "pending", nil
	}
	return "passed", nil
}

func deferredMergeAggregateState(status string) string {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "":
		return "unknown"
	case "success", "passed":
		return "passed"
	case "failure", "failed", "error", "cancelled", "canceled", "timed_out":
		return "failed"
	case "pending", "in_progress", "queued", "running", "waiting":
		return "pending"
	default:
		return "unknown"
	}
}

func deferredMergeKey(repo db.Repo, number int) string {
	return string(repoProviderKind(repo)) + ":" + repoProviderHost(repo) + ":" + repo.RepoPath + "#" + strconv.Itoa(number)
}

func (s *Server) markDeferredMergeInFlight(key string) (*deferredMergeHandle, bool) {
	s.deferredMergeMu.Lock()
	defer s.deferredMergeMu.Unlock()
	if s.deferredMergeInFlight == nil {
		s.deferredMergeInFlight = make(map[string]*deferredMergeHandle)
	}
	if _, ok := s.deferredMergeInFlight[key]; ok {
		return nil, false
	}
	handle := newDeferredMergeHandle()
	s.deferredMergeInFlight[key] = handle
	return handle, true
}

func (s *Server) isDeferredMergePending(repo db.Repo, number int) bool {
	s.deferredMergeMu.Lock()
	defer s.deferredMergeMu.Unlock()
	_, ok := s.deferredMergeInFlight[deferredMergeKey(repo, number)]
	return ok
}

// clearDeferredMergeInFlight removes the key only while it still maps to
// handle. Terminal paths clear before broadcasting, so a new deferred merge
// can be queued for the same key before the old worker goroutine runs its
// deferred cleanup; that cleanup must not delete the newer handle.
func (s *Server) clearDeferredMergeInFlight(key string, handle *deferredMergeHandle) {
	s.deferredMergeMu.Lock()
	defer s.deferredMergeMu.Unlock()
	if s.deferredMergeInFlight[key] == handle {
		delete(s.deferredMergeInFlight, key)
	}
}

// supersedeDeferredMerge stands down any queued deferred merge for the key
// after a merge landed through another path. Pending state clears here, before
// callers observe the merge result, so a detail refresh triggered by the merge
// never reports a queued merge that no longer exists.
func (s *Server) supersedeDeferredMerge(key string) {
	s.deferredMergeMu.Lock()
	defer s.deferredMergeMu.Unlock()
	handle, ok := s.deferredMergeInFlight[key]
	if !ok {
		return
	}
	handle.supersede()
	delete(s.deferredMergeInFlight, key)
}
