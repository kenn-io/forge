package pullapi

import (
	"context"
	"encoding/json/v2"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"go.kenn.io/forge/internal/db"
	ghclient "go.kenn.io/forge/internal/github"
	"go.kenn.io/forge/internal/providerplane"
	"go.kenn.io/forge/internal/server/httpapi"
	"go.kenn.io/forge/internal/server/workspaceapi"
	"go.kenn.io/forge/platform"
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
	superseded  chan struct{}
	once        sync.Once
	release     func()
	releaseOnce sync.Once
}

func newDeferredMergeHandle() *deferredMergeHandle {
	return &deferredMergeHandle{superseded: make(chan struct{})}
}

func (h *deferredMergeHandle) finish() {
	if h == nil || h.release == nil {
		return
	}
	h.releaseOnce.Do(h.release)
}

func (h *deferredMergeHandle) supersede() {
	h.once.Do(func() { close(h.superseded) })
}

func (h *deferredMergeHandle) isSuperseded() bool {
	if h == nil {
		return false
	}
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
	// NoCI is set when the target had no CI when the merge was queued.
	NoCI bool
}

type DeferredMergeCompletedPayload struct {
	Provider     string `json:"provider"`
	PlatformHost string `json:"platform_host"`
	RepoPath     string `json:"repo_path"`
	Owner        string `json:"owner"`
	Name         string `json:"name"`
	Number       int    `json:"number"`
	HeadSHA      string `json:"head_sha"`
	// Status is "merged", "failed", or "queued" when the merge went to the
	// target branch's merge queue.
	Status                  string `json:"status"`
	Merged                  bool   `json:"merged,omitempty"`
	SHA                     string `json:"sha,omitempty"`
	Message                 string `json:"message,omitempty"`
	Error                   string `json:"error,omitempty"`
	CompletedAt             string `json:"completed_at"`
	Warning                 string `json:"workspace_cleanup_warning,omitempty"`
	WorkspaceCleanupPending bool   `json:"workspace_cleanup_pending,omitempty"`
}

func (s *Handler) deferMergePR(
	ctx context.Context,
	input *deferMergePRInput,
) (*deferMergePROutput, error) {
	body, err := s.enqueueDeferredMerge(ctx, input.Provider, input.PlatformHost, input.Owner, input.Name, input.Number, input.Body, deferredMergePollInterval, s.deferredMergeMaxWait)
	if err != nil {
		return nil, err
	}
	return &deferMergePROutput{Status: 202, Body: body}, nil
}

func (s *Handler) enqueueDeferredMerge(
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
	body.bindWorkspaceHost(ctx)
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
	if err := s.requireSyncerCapability(repo.Repo, capabilityMergeMutation); err != nil {
		return deferMergePRBody{}, err
	}
	mr, err := s.visibleMergeRequest(ctx, repo.ID, number)
	if err != nil {
		return deferMergePRBody{}, httpapi.Internal("get pull request failed")
	}
	if mr == nil {
		return deferMergePRBody{}, httpapi.NotFound(httpapi.CodePullNotFound, "pull request not found", nil)
	}
	if mr.State != db.MergeRequestStateOpen {
		return deferMergePRBody{}, httpapi.Conflict(
			httpapi.CodeConflict,
			"pull request is not open",
			map[string]any{"reason": "not_open"},
		)
	}
	if err := s.requireMidStackMergeAllowed(ctx, repo.ID, number); err != nil {
		return deferMergePRBody{}, err
	}
	stack, members, err := s.db.GetStackForPRByRepoID(ctx, repo.ID, number)
	if err != nil {
		return deferMergePRBody{}, httpapi.Internal("get stack for pull request failed")
	}
	if stack != nil && stack.Native {
		queue, err := s.mergeQueueTarget(ctx, repo.Repo, mr, stack, members)
		if err != nil {
			return deferMergePRBody{}, err
		}
		if queue != nil {
			// GitHub has no auto-merge for a stack. Its "Enqueue stack" adds
			// the stack to the merge queue at once, and the queue waits for
			// the required checks, so Forge enqueues now as well.
			if _, err := s.mergePRWithBody(ctx, provider, platformHost, owner, name, number, body); err != nil {
				return deferMergePRBody{}, err
			}
			return deferMergePRBody{Status: "queued"}, nil
		}
	}
	expectedHeadSHA, err := s.preflightMergePR(repo.Row(), mr, number, body)
	if err != nil {
		return deferMergePRBody{}, err
	}
	queuedTarget := deferredMergeTargetSnapshotFromMR(mr)
	if strings.TrimSpace(expectedHeadSHA) != "" {
		queuedTarget.HeadSHA = expectedHeadSHA
	}
	if strings.TrimSpace(queuedTarget.BaseSHA) == "" {
		return deferMergePRBody{}, httpapi.Conflict(
			httpapi.CodeConflict,
			"target base commit has not been synced; refresh and retry",
			map[string]any{"reason": "base_unknown"},
		)
	}
	pendingKeys, err := pendingDeferredMergeCheckKeys(mr.CIChecksJSON)
	if err != nil {
		return deferMergePRBody{}, httpapi.Validation("ci_checks", err.Error())
	}
	key := deferredMergeKey(repo.Repo, number)
	// One deferral at a time chooses between provider auto-merge and the
	// worker, so concurrent requests never start both.
	release := s.mergeLocks.Lock(key)
	defer release()
	// A worker already waiting keeps the merge; the admission below rejects
	// the duplicate without asking the provider.
	var autoMerge *providerAutoMerge
	if !s.isDeferredMergePending(repo.Repo, number) {
		autoMerge, err = s.readProviderAutoMerge(ctx, repo.Repo, number)
		if err != nil {
			return deferMergePRBody{}, err
		}
	}
	stack, members, err = s.db.GetStackForPRByRepoID(ctx, repo.ID, number)
	if err != nil {
		return deferMergePRBody{}, httpapi.Internal("get stack for pull request failed")
	}
	below, nativeStack := nativeStackMembersBelow(stack, members, number)
	if autoMerge != nil && autoMerge.state.Enabled {
		if body.BypassRules {
			return deferMergePRBody{}, httpapi.Conflict(
				httpapi.CodeConflict,
				"auto-merge is already on for this pull request",
				map[string]any{"reason": "already_pending"},
			)
		}
		// A retry after a lost answer finds auto-merge on whatever CI did
		// since, and keeps the workspace deletion it asks for.
		if err := s.recordProviderAutoMerge(ctx, repo.Repo, number, body); err != nil {
			return deferMergePRBody{}, err
		}
		return deferMergePRBody{Status: "queued", PendingChecks: len(pendingKeys)}, nil
	}
	aggregateState := deferredMergeAggregateState(mr.CIStatus)
	if aggregateState == "failed" {
		return deferMergePRBody{}, httpapi.Conflict(
			httpapi.CodeConflict,
			"CI checks have already failed",
			map[string]any{"reason": "ci_failed"},
		)
	}
	stackPending, belowWaits, err := s.deferredMergeStackAdmission(ctx, repo.ID, below)
	if err != nil {
		return deferMergePRBody{}, err
	}
	if len(pendingKeys) == 0 && aggregateState != "pending" && !stackPending {
		refreshed, refreshedKeys, err := s.refreshPendingDeferredMergeCheckKeys(ctx, repo.Repo, number, queuedTarget)
		if err != nil {
			return deferMergePRBody{}, err
		}
		pendingKeys = refreshedKeys
		queuedTarget.NoCI = queuedDeferredMergeNoCI(refreshed)
		aggregateState = deferredMergeAggregateState(refreshed.CIStatus)
		if aggregateState == "failed" {
			return deferMergePRBody{}, httpapi.Conflict(
				httpapi.CodeConflict,
				"CI checks have already failed",
				map[string]any{"reason": "ci_failed"},
			)
		}
	}
	if len(pendingKeys) == 0 && aggregateState != "pending" && !stackPending {
		return deferMergePRBody{}, httpapi.Conflict(
			httpapi.CodeConflict,
			"no pending CI checks to wait for",
			map[string]any{"reason": "no_pending_checks"},
		)
	}
	// GitHub refuses to turn auto-merge on for a pull request in a stack, so
	// the worker merges it through the stack-aware merge API. A
	// branch-inferred stack counts: with the native stack preview off, Forge
	// records a GitHub native stack as one.
	useAutoMerge := false
	if stack == nil {
		useAutoMerge, err = s.useProviderAutoMerge(ctx, repo.ID, mr, autoMerge, body.BypassRules)
		if err != nil {
			return deferMergePRBody{}, err
		}
	}
	if useAutoMerge && autoMerge != nil {
		err := autoMerge.mutator.EnableAutoMerge(
			ctx, platformRepoRefFromDB(repo.Repo), number,
			body.CommitTitle, body.CommitMessage, body.Method, expectedHeadSHA,
		)
		if err != nil {
			// Never fall back to the worker: it could merge beside an
			// auto-merge the provider accepted.
			return deferMergePRBody{}, httpapi.ProviderMutationProblem(
				err, string(repoProviderKind(repo.Repo)), repoProviderHost(repo.Repo),
			)
		}
		if err := s.recordProviderAutoMerge(ctx, repo.Repo, number, body); err != nil {
			return deferMergePRBody{}, err
		}
		return deferMergePRBody{Status: "queued", PendingChecks: len(pendingKeys)}, nil
	}
	var releaseDeferred func()
	if s.providerWriteGate != nil {
		releaseDeferred, err = s.providerWriteGate.BeginDeferredMerge(ctx)
		if err != nil {
			if errors.Is(err, providerplane.ErrSpokePreparationInProgress) {
				return deferMergePRBody{}, httpapi.NewProblem(
					http.StatusConflict, httpapi.CodeSpokePreparationInProgress,
					"spoke preparation has sealed deferred merge admission",
					map[string]any{"reason": "spokePreparationInProgress"},
				)
			}
			return deferMergePRBody{}, httpapi.Internal("admit deferred merge: " + err.Error())
		}
	}
	handle, marked := s.markDeferredMergeInFlight(key, releaseDeferred)
	if !marked {
		if releaseDeferred != nil {
			releaseDeferred()
		}
		return deferMergePRBody{}, httpapi.Conflict(
			httpapi.CodeConflict,
			"a deferred merge is already waiting for this pull request",
			map[string]any{"reason": "already_pending"},
		)
	}
	// The worker carries this deferral's own deletion choice and acts on it
	// when it merges, so it replaces a request kept by an earlier one. Drop
	// it only once the worker is admitted, so a rejected deferral keeps it,
	// and start the worker only once it is dropped.
	if err := s.db.CancelMergeWorkspaceCleanup(ctx, repo.ID, number); err != nil {
		s.clearDeferredMergeInFlight(key, handle)
		slog.Error("drop earlier merge workspace cleanup failed", "repo_id", repo.ID, "number", number, "err", err)
		return deferMergePRBody{}, httpapi.Internal("dropping the earlier workspace deletion failed; retry")
	}
	started := s.runBackground(func(bgCtx context.Context) {
		defer s.clearDeferredMergeInFlight(key, handle)
		s.runDeferredMerge(bgCtx, repo.Repo, number, body, pendingKeys, belowWaits, queuedTarget, pollInterval, maxWait, handle)
	})
	if !started {
		s.clearDeferredMergeInFlight(key, handle)
		return deferMergePRBody{}, httpapi.ServiceUnavailable("server is shutting down")
	}
	return deferMergePRBody{
		Status:        "queued",
		PendingChecks: len(pendingKeys),
	}, nil
}

// providerAutoMerge is the provider's auto-merge and its state for one pull
// request, read before Forge sends anything.
type providerAutoMerge struct {
	mutator platform.AutoMergeMutator
	state   platform.AutoMergeState
}

// readProviderAutoMerge reads whether the provider's own auto-merge can run a
// deferred merge, which outlives a Forge restart and shows on the provider.
// It returns nil when the provider has no auto-merge, and Forge's worker runs
// the merge. Forge decides between them before it sends anything, so its
// worker never runs beside an auto-merge the provider may have accepted.
func (s *Handler) readProviderAutoMerge(
	ctx context.Context, repo db.Repo, number int,
) (*providerAutoMerge, error) {
	kind, host := repoProviderKind(repo), repoProviderHost(repo)
	mutator, err := s.syncer.AutoMergeMutator(kind, host)
	if errors.Is(err, platform.ErrUnsupportedCapability) {
		return nil, nil
	}
	if err != nil {
		return nil, httpapi.ProviderCallProblemWithDetail(err, string(kind), host, "read auto-merge state: "+err.Error())
	}
	state, err := mutator.AutoMergeState(ctx, platformRepoRefFromDB(repo), number)
	if errors.Is(err, platform.ErrUnsupportedCapability) {
		return nil, nil
	}
	if err != nil {
		return nil, httpapi.ProviderCallProblemWithDetail(err, string(kind), host, "read auto-merge state: "+err.Error())
	}
	return &providerAutoMerge{mutator: mutator, state: state}, nil
}

// useProviderAutoMerge reports whether the provider's auto-merge runs a new
// deferred merge. keepWorker keeps Forge's worker: only it merges over the
// rules, so bypass consent keeps it, and GitHub's public API refuses
// auto-merge for every pull request in a native stack, which only its web UI
// can turn on. GitHub refuses auto-merge for a pull request it could merge
// now, so the worker waits for those checks. A branch with a merge queue
// takes auto-merge even where the repository setting is off, and enqueues
// once the checks pass.
func (s *Handler) useProviderAutoMerge(
	ctx context.Context, repoID int64, mr *db.MergeRequest, autoMerge *providerAutoMerge, keepWorker bool,
) (bool, error) {
	if autoMerge == nil || keepWorker || autoMerge.state.MergeableNow {
		return false, nil
	}
	if autoMerge.state.RepositoryAllows {
		return true, nil
	}
	queue, err := s.db.GetMergeQueueState(ctx, repoID, mr.Number)
	if err == nil && queue == nil {
		// Forge has not read this pull request's queue state yet, as in
		// deferredMergeRules; the merge path relies on a stored read too.
		s.syncer.RefreshPullRequestMergeQueueForRepository(ctx, repoID, mr.Number)
		queue, err = s.db.GetMergeQueueState(ctx, repoID, mr.Number)
	}
	if err != nil {
		return false, httpapi.Internal("load merge queue state failed")
	}
	return queue != nil && queue.Enabled && queue.BaseBranch == mr.BaseBranch, nil
}

// recordProviderAutoMerge records auto-merge that is on and keeps the
// requested workspace deletion until sync records the provider's merge. It
// runs even if the call that turned auto-merge on was cancelled.
func (s *Handler) recordProviderAutoMerge(
	ctx context.Context, repo db.Repo, number int, body mergePRInputBody,
) error {
	ctx = context.WithoutCancel(ctx)
	if err := s.db.SetMergeRequestAutoMergeEnabled(ctx, repo.ID, number, true); err != nil {
		slog.Warn("record provider auto-merge failed", "repo_id", repo.ID, "number", number, "err", err)
	}
	s.publish(Event{Type: "data_changed", Data: struct{}{}})
	if body.DeleteWorkspaceID == "" {
		// The latest choice declines deletion, so it replaces an earlier
		// request, as an immediate merge's does.
		if err := s.db.CancelMergeWorkspaceCleanup(ctx, repo.ID, number); err != nil {
			slog.Error("drop earlier merge workspace cleanup failed", "repo_id", repo.ID, "number", number, "err", err)
			return httpapi.Internal("auto-merge is on, but dropping the earlier workspace deletion failed; retry to drop it")
		}
		return nil
	}
	if err := s.db.SaveMergeWorkspaceCleanup(ctx, db.MergeWorkspaceCleanup{
		RepoID: repo.ID, Number: number,
		WorkspaceID: body.DeleteWorkspaceID, HostKey: body.workspaceHostKey,
		RequestedAt: s.now().UTC(),
	}); err != nil {
		slog.Error("keep workspace cleanup for auto-merge failed",
			"repo_id", repo.ID, "number", number,
			"workspace_id", body.DeleteWorkspaceID, "err", err)
		return httpapi.Internal("auto-merge is on, but keeping the workspace deletion failed; retry to keep it")
	}
	return nil
}

// providerAutoMergePending reports that the provider will merge the open
// pull request itself once its requirements pass.
func providerAutoMergePending(mr *db.MergeRequest) bool {
	return mr.AutoMergeEnabled && mr.State == db.MergeRequestStateOpen
}

func (s *Handler) cancelDeferredMergePR(ctx context.Context, input *repoNumberInput) (*actionStatusOutput, error) {
	repo, err := s.requireRepoRouteCapability(ctx, input.Provider, input.PlatformHost, input.Owner, input.Name, capabilityMergeMutation)
	if err != nil {
		return nil, err
	}
	if err := s.requireSyncerCapability(repo.Repo, capabilityMergeMutation); err != nil {
		return nil, err
	}
	release := s.mergeLocks.Lock(deferredMergeKey(repo.Repo, input.Number))
	defer release()
	mr, err := s.visibleMergeRequest(ctx, repo.ID, input.Number)
	if err != nil {
		return nil, httpapi.Internal("get pull request failed")
	}
	if mr == nil {
		return nil, httpapi.NotFound(httpapi.CodePullNotFound, "pull request not found", nil)
	}
	if mr.State != db.MergeRequestStateOpen {
		return nil, httpapi.Conflict(httpapi.CodeConflict, "pull request is not open", map[string]any{"reason": "not_open"})
	}
	// A pull request already in the merge queue is the queue's to merge, and
	// turning auto-merge off does not take it out. Keep its workspace cleanup
	// rather than report a cancel that did not happen. A stored read that
	// shows it queued stands: a fresh read could drop that row once the
	// queue merges it, before sync records the merge.
	if queued, err := s.mergeQueueHolds(ctx, repo.ID, input.Number); err != nil || queued {
		return nil, mergeQueuedConflict(err)
	}
	// Check the provider even while Forge's worker waits: someone can turn
	// auto-merge on at the provider beside it.
	autoMerge, err := s.readProviderAutoMerge(ctx, repo.Repo, input.Number)
	if err != nil {
		return nil, err
	}
	if autoMerge != nil && autoMerge.state.Enabled {
		if err := autoMerge.mutator.DisableAutoMerge(ctx, platformRepoRefFromDB(repo.Repo), input.Number); err != nil {
			return nil, httpapi.ProviderMutationProblem(err, string(repoProviderKind(repo.Repo)), repoProviderHost(repo.Repo))
		}
	}
	// The provider's auto-merge or a waiting worker may have enqueued it
	// while auto-merge was still on, so read the queue now, after turning it
	// off and before forgetting anything. A read that cannot run keeps the
	// stored state.
	s.syncer.RefreshPullRequestMergeQueueForRepository(ctx, repo.ID, input.Number)
	if queued, err := s.mergeQueueHolds(ctx, repo.ID, input.Number); err != nil || queued {
		return nil, mergeQueuedConflict(err)
	}
	s.supersedeDeferredMerge(deferredMergeKey(repo.Repo, input.Number))
	ctx = context.WithoutCancel(ctx)
	if err := s.db.SetMergeRequestAutoMergeEnabled(ctx, repo.ID, input.Number, false); err != nil {
		return nil, httpapi.Internal("merge cancelled, but recording its state failed")
	}
	// A kept workspace deletion is the user's durable choice and stays: the
	// pull request's merge deletes the workspace, its close drops the
	// request, and a later merge replaces it with its own choice.
	s.publish(Event{Type: "data_changed", Data: struct{}{}})
	return &actionStatusOutput{Body: ActionStatusBody{Status: "cancelled"}}, nil
}

// mergeQueueHolds reports that the stored merge queue state shows the pull
// request in the queue.
func (s *Handler) mergeQueueHolds(ctx context.Context, repoID int64, number int) (bool, error) {
	queue, err := s.db.GetMergeQueueState(ctx, repoID, number)
	if err != nil {
		return false, err
	}
	return queue != nil && queue.Queued, nil
}

// mergeQueuedConflict refuses a cancel for a pull request the merge queue
// holds, or reports that its queue state could not be read.
func mergeQueuedConflict(err error) error {
	if err != nil {
		return httpapi.Internal("get merge queue state failed")
	}
	return httpapi.Conflict(
		httpapi.CodeConflict,
		"pull request is already in the merge queue",
		map[string]any{"reason": "merge_queued"},
	)
}

func (s *Handler) runDeferredMerge(
	ctx context.Context,
	repo db.Repo,
	number int,
	body mergePRInputBody,
	pendingKeys []deferredMergeCheckKey,
	belowWaits map[int]deferredMergeWait,
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
	waitingOnRules := false
	for {
		ci, err := s.refreshDeferredMergeCI(ctx, repo, number, pendingKeys, queuedTarget)
		if err != nil {
			if errors.Is(err, errDeferredMergeTargetMerged) {
				return
			}
			s.broadcastDeferredMergeFailure(repo, number, deferredMergeHeadSHA(body, queuedTarget.HeadSHA), err.Error(), handle)
			return
		}
		if ci == "passed" || ci == "pending" {
			// A native stack merge lands every unmerged member below the
			// target with it, so their CI must pass too, and a failure there
			// ends the wait as one on the target does.
			stackCI, err := s.deferredMergeStackCI(ctx, repo, number, belowWaits)
			if err != nil {
				s.broadcastDeferredMergeFailure(repo, number, deferredMergeHeadSHA(body, queuedTarget.HeadSHA), err.Error(), handle)
				return
			}
			if ci == "passed" {
				ci = stackCI
			}
		}
		rules, queue := "", false
		if ci == "passed" {
			current, err := s.ensureDeferredMergeTargetUnchanged(ctx, repo, number, queuedTarget)
			if err != nil {
				if !errors.Is(err, errDeferredMergeTargetMerged) {
					s.broadcastDeferredMergeFailure(repo, number, deferredMergeHeadSHA(body, queuedTarget.HeadSHA), err.Error(), handle)
				}
				return
			}
			rules, queue = s.deferredMergeRules(ctx, repo, current)
		}
		failure := ""
		switch deferredMergeNextStep(ci, rules, queue, body.BypassRules) {
		case deferredMergeMerge:
			s.completeDeferredMerge(ctx, repo, number, body, queuedTarget, handle)
			return
		case deferredMergeFailCI:
			failure = "a current CI check failed; merge was not performed"
		case deferredMergeFailCIUnavailable:
			failure = "aggregate CI status is unavailable after refresh; merge was not performed"
		case deferredMergeFailNeedsBypass:
			failure = "merge needs a bypass of branch protection or repository rules; it was not performed"
		case deferredMergeRetryRules:
			waitingOnRules = true
		case deferredMergeWaitForCI:
		}
		if failure != "" {
			s.broadcastDeferredMergeFailure(repo, number, deferredMergeHeadSHA(body, queuedTarget.HeadSHA), failure, handle)
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
			message := "timed out waiting for pending CI checks to finish; merge was not performed"
			if waitingOnRules {
				message = "timed out because Forge could not confirm the pull request's merge state; merge was not performed"
			}
			s.broadcastDeferredMergeFailure(repo, number, deferredMergeHeadSHA(body, queuedTarget.HeadSHA), message, handle)
			return
		case <-ticker.C:
		}
	}
}

// nativeStackMembersBelow returns the unmerged members that a merge of number
// lands with it, bottom first, and whether number belongs to a provider-native
// stack. Members must be ordered by position.
func nativeStackMembersBelow(
	stack *db.Stack, members []db.StackMemberWithPR, number int,
) ([]db.StackMemberWithPR, bool) {
	if stack == nil || !stack.Native {
		return nil, false
	}
	var below []db.StackMemberWithPR
	for _, member := range members {
		if member.Number == number {
			return below, true
		}
		if member.State != string(db.MergeRequestStateMerged) {
			below = append(below, member)
		}
	}
	return nil, false
}

// nativeStackMemberNotReady says why the provider's stack merge would refuse a
// member below the target, as GitHub's own stack auto-merge does for a closed,
// draft, or conflicting member, or returns "" when nothing does.
func nativeStackMemberNotReady(number int, state string, isDraft bool, mergeableState string) string {
	switch {
	case state != string(db.MergeRequestStateOpen):
		return fmt.Sprintf("#%d is %s", number, state)
	case isDraft:
		return fmt.Sprintf("#%d is a draft", number)
	case mergeableState == "dirty":
		return fmt.Sprintf("#%d has merge conflicts", number)
	}
	return ""
}

// deferredMergeWait is what a deferred merge waits for on a member below its
// target: the checks pending when the merge was queued, and whether the member
// had no CI then.
type deferredMergeWait struct {
	keys []deferredMergeCheckKey
	noCI bool
}

// deferredMergeStackAdmission rejects a deferral that a member below the
// target already rules out, from the stored state, and reports whether any of
// them still runs CI. It returns what the merge waits for on each member, as
// it does on the target.
func (s *Handler) deferredMergeStackAdmission(
	ctx context.Context, repoID int64, below []db.StackMemberWithPR,
) (bool, map[int]deferredMergeWait, error) {
	pending := false
	waits := make(map[int]deferredMergeWait, len(below))
	for _, member := range below {
		if reason := nativeStackMemberNotReady(
			member.Number, member.State, member.IsDraft, member.MergeableState,
		); reason != "" {
			return false, nil, httpapi.Conflict(
				httpapi.CodeConflict,
				reason+", and a stack merge lands every pull request below this one with it",
				map[string]any{"reason": "stack_member_not_ready", "blocking_number": member.Number},
			)
		}
		switch deferredMergeAggregateState(member.CIStatus) {
		case "failed":
			return false, nil, httpapi.Conflict(
				httpapi.CodeConflict,
				fmt.Sprintf("CI failed on #%d, which a stack merge lands with this pull request", member.Number),
				map[string]any{"reason": "ci_failed", "blocking_number": member.Number},
			)
		case "pending":
			pending = true
		}
		mr, err := s.visibleMergeRequest(ctx, repoID, member.Number)
		if err != nil {
			return false, nil, httpapi.Internal("get stack member failed")
		}
		if mr == nil {
			continue
		}
		memberKeys, err := pendingDeferredMergeCheckKeys(mr.CIChecksJSON)
		if err != nil {
			return false, nil, httpapi.Validation("ci_checks", err.Error())
		}
		if len(memberKeys) > 0 {
			pending = true
		}
		waits[member.Number] = deferredMergeWait{keys: memberKeys, noCI: queuedDeferredMergeNoCI(mr)}
	}
	return pending, waits, nil
}

// deferredMergeStackCI refreshes the CI of every unmerged member a native
// stack merge lands below the target. It reports "passed" once all of them
// passed, including the checks each had pending when the merge was queued,
// and "pending" while any still runs. A member whose CI failed or is
// unavailable, or that the provider's stack merge would refuse, fails the
// deferred merge. Outside a native stack it reports "passed".
func (s *Handler) deferredMergeStackCI(
	ctx context.Context, repo db.Repo, number int, belowWaits map[int]deferredMergeWait,
) (string, error) {
	stack, members, err := s.db.GetStackForPRByRepoID(ctx, repo.ID, number)
	if err != nil {
		return "", err
	}
	below, _ := nativeStackMembersBelow(stack, members, number)
	state := "passed"
	for _, member := range below {
		warnings, err := s.syncer.RefreshMRCIStatusOnProvider(
			ctx, mergeRequestRepoRef(repo), repo.ID, member.Number, member.PlatformHeadSHA,
		)
		if err != nil {
			return "", fmt.Errorf("refreshing CI on #%d: %w", member.Number, err)
		}
		if len(warnings) > 0 {
			return "", fmt.Errorf(
				"could not refresh CI checks on #%d; deferred merge was not performed: %s",
				member.Number, strings.Join(warnings, "; "),
			)
		}
		mr, err := s.visibleMergeRequest(ctx, repo.ID, member.Number)
		if err != nil {
			return "", err
		}
		if mr == nil {
			return "", fmt.Errorf("#%d no longer exists; merge was not performed", member.Number)
		}
		if reason := nativeStackMemberNotReady(
			mr.Number, string(mr.State), mr.IsDraft, mr.MergeableState,
		); reason != "" {
			return "", errors.New(reason + ", and a stack merge lands it with this pull request; merge was not performed")
		}
		wait := belowWaits[mr.Number]
		ci, err := deferredMergeCheckState(mr.CIStatus, wait.keys, mr.CIChecksJSON, wait.noCI)
		if err != nil {
			return "", err
		}
		switch ci {
		case "failed":
			return "", fmt.Errorf("CI failed on #%d, which a stack merge lands with this pull request; merge was not performed", mr.Number)
		case "unknown":
			return "", fmt.Errorf("CI status of #%d is unavailable after refresh; merge was not performed", mr.Number)
		case "pending":
			state = "pending"
		}
	}
	return state, nil
}

func (s *Handler) refreshDeferredMergeCI(
	ctx context.Context,
	repo db.Repo,
	number int,
	pendingKeys []deferredMergeCheckKey,
	queuedTarget deferredMergeTargetSnapshot,
) (string, error) {
	mr, err := s.visibleMergeRequest(ctx, repo.ID, number)
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
		mergeRequestRepoRef(repo),
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
	refreshed, err := s.visibleMergeRequest(ctx, repo.ID, number)
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
	s.publish(Event{
		Type: "pr_ci_refreshed",
		Data: workspaceapi.PRCIRefreshedPayload{
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
	return deferredMergeCheckState(refreshed.CIStatus, pendingKeys, refreshed.CIChecksJSON, queuedTarget.NoCI)
}

func (s *Handler) refreshPendingDeferredMergeCheckKeys(
	ctx context.Context,
	repo db.Repo,
	number int,
	queuedTarget deferredMergeTargetSnapshot,
) (*db.MergeRequest, []deferredMergeCheckKey, error) {
	warnings, err := s.syncer.RefreshMRCIStatusOnProvider(
		ctx,
		mergeRequestRepoRef(repo),
		repo.ID,
		number,
		queuedTarget.HeadSHA,
	)
	if err != nil {
		return nil, nil, httpapi.ProviderCallProblemWithDetail(
			err,
			string(repoProviderKind(repo)), repoProviderHost(repo),
			"refresh PR CI before deferring merge: "+err.Error(),
		)
	}
	if len(warnings) > 0 {
		return nil, nil, httpapi.Conflict(
			httpapi.CodeConflict,
			"could not refresh CI checks before deferring merge",
			map[string]any{"reason": "ci_refresh_unavailable", "warnings": warnings},
		)
	}
	refreshed, err := s.visibleMergeRequest(ctx, repo.ID, number)
	if err != nil {
		return nil, nil, httpapi.Internal("get pull request after CI refresh failed")
	}
	if refreshed == nil {
		return nil, nil, httpapi.NotFound(httpapi.CodePullNotFound, "pull request not found after CI refresh", nil)
	}
	if err := deferredMergeTargetMatchesDB(refreshed, queuedTarget); err != nil {
		return nil, nil, httpapi.Conflict(
			httpapi.CodeConflict,
			err.Error(),
			map[string]any{"reason": "stale_state"},
		)
	}
	keys, err := pendingDeferredMergeCheckKeys(refreshed.CIChecksJSON)
	if err != nil {
		return nil, nil, httpapi.Validation("ci_checks", err.Error())
	}
	return refreshed, keys, nil
}

func mergeRequestRepoRef(repo db.Repo) ghclient.RepoRef {
	return ghclient.RepoRef{
		Platform:      repoProviderKind(repo),
		Owner:         repo.Owner,
		Name:          repo.Name,
		PlatformHost:  repoProviderHost(repo),
		RepoPath:      repo.RepoPath,
		Key:           repo.Key,
		WebURL:        repo.WebURL,
		CloneURL:      repo.CloneURL,
		DefaultBranch: repo.DefaultBranch,
	}
}

// completeDeferredMerge merges once Recheck admitted the merge.
func (s *Handler) completeDeferredMerge(
	ctx context.Context,
	repo db.Repo,
	number int,
	body mergePRInputBody,
	queuedTarget deferredMergeTargetSnapshot,
	handle *deferredMergeHandle,
) {
	// An immediate enqueue can supersede this worker while its CI or target
	// reads block. Enqueueing leaves the pull request open, so the target
	// checks in Recheck cannot detect it. The supersession check runs under
	// the pull request's merge lock, so it cannot race an immediate merge or
	// enqueue that is still talking to the provider.
	result, err := s.mergePRWithBodyAdmitted(
		ctx, string(repoProviderKind(repo)), repoProviderHost(repo), repo.Owner, repo.Name, number, body,
		func() bool { return !handle.isSuperseded() },
	)
	if errors.Is(err, errMergeNotAdmitted) {
		return
	}
	if err != nil {
		s.broadcastDeferredMergeFailure(repo, number, deferredMergeHeadSHA(body, queuedTarget.HeadSHA), err.Error(), handle)
		return
	}
	if result.Queued {
		// The enqueue above superseded this worker's own handle; that is
		// expected and must not suppress its completion.
		// The target branch's merge queue now owns the merge; it reports
		// its own outcome through the pull request's queue state.
		s.clearDeferredMergeInFlight(deferredMergeKey(repo, number), handle)
		s.publish(Event{Type: "data_changed", Data: struct{}{}})
		s.publish(Event{
			Type: "deferred_merge_completed",
			Data: DeferredMergeCompletedPayload{
				Provider:     string(repoProviderKind(repo)),
				PlatformHost: repoProviderHost(repo),
				RepoPath:     repo.RepoPath,
				Owner:        repo.Owner,
				Name:         repo.Name,
				Number:       number,
				HeadSHA:      deferredMergeHeadSHA(body, queuedTarget.HeadSHA),
				Status:       "queued",
				Message:      result.Message,
				CompletedAt:  formatUTCRFC3339(s.now().UTC()),
			},
		})
		return
	}
	if !result.Merged {
		message := strings.TrimSpace(result.Message)
		if message == "" {
			message = "provider did not merge the pull request"
		}
		s.broadcastDeferredMergeFailure(repo, number, deferredMergeHeadSHA(body, queuedTarget.HeadSHA), message, handle)
		return
	}
	// Clear pending before announcing completion: clients refresh detail the
	// moment they see deferred_merge_completed, and that refresh must not
	// read a stale deferred_merge_pending=true.
	s.clearDeferredMergeInFlight(deferredMergeKey(repo, number), handle)
	s.publish(Event{Type: "data_changed", Data: struct{}{}})
	s.publish(Event{
		Type: "deferred_merge_completed",
		Data: DeferredMergeCompletedPayload{
			Provider:                string(repoProviderKind(repo)),
			PlatformHost:            repoProviderHost(repo),
			RepoPath:                repo.RepoPath,
			Owner:                   repo.Owner,
			Name:                    repo.Name,
			Number:                  number,
			HeadSHA:                 deferredMergeHeadSHA(body, queuedTarget.HeadSHA),
			Status:                  "merged",
			Merged:                  result.Merged,
			SHA:                     result.SHA,
			Message:                 result.Message,
			CompletedAt:             formatUTCRFC3339(s.now().UTC()),
			Warning:                 result.WorkspaceCleanupWarning,
			WorkspaceCleanupPending: result.WorkspaceCleanupPending,
		},
	})
}

// deferredMergeStep is the deferred merge worker's next state.
type deferredMergeStep int

const (
	deferredMergeWaitForCI deferredMergeStep = iota
	deferredMergeFailCI
	deferredMergeFailCIUnavailable
	deferredMergeMerge
	deferredMergeFailNeedsBypass
	deferredMergeRetryRules
)

// deferredMergeNextStep is the deferred merge worker's state machine
// (context/deferred-merge.md). rules is "" when unknown. A merge queue target
// is exempt from the rules; consent, fixed at queue time, merges over them.
func deferredMergeNextStep(ci, rules string, queue, consent bool) deferredMergeStep {
	switch {
	case ci == "failed":
		return deferredMergeFailCI
	case ci == "unknown":
		return deferredMergeFailCIUnavailable
	case ci != "passed":
		return deferredMergeWaitForCI
	case queue || rules == ghclient.MergeRulesClear:
		return deferredMergeMerge
	case rules == "":
		return deferredMergeRetryRules
	case consent:
		return deferredMergeMerge
	default:
		return deferredMergeFailNeedsBypass
	}
}

// deferredMergeRules reads Recheck's inputs: how the rules treat the merge
// credential's merge, and whether the base branch's merge queue exempts it.
// A pull request the provider read reports blocked is read again as the merge
// credential, because the sync credential can see it blocked where the merge
// credential does not; hosts that cannot answer keep the provider state.
func (s *Handler) deferredMergeRules(
	ctx context.Context, repo db.Repo, current platform.MergeRequest,
) (rules string, queue bool) {
	rules = ghclient.MergeRulesClear
	if current.MergeableState == "blocked" {
		rules = ghclient.MergeRulesBlocked
		if repoProviderKind(repo) == platform.KindGitHub {
			if read, readable := s.syncer.ReadMergeRules(ctx, mergeRequestRepoRef(repo), current.Number); readable {
				rules = read
			}
		}
	}
	if rules == ghclient.MergeRulesClear {
		return rules, false
	}
	stack, members, err := s.db.GetStackForPRByRepoID(ctx, repo.ID, current.Number)
	if err != nil {
		return rules, false
	}
	owner, baseBranch := mergeQueueOwner(current.Number, current.BaseBranch, stack, members)
	stored, err := s.db.GetMergeQueueState(ctx, repo.ID, owner)
	if err == nil && stored == nil {
		// Forge has not read this pull request's queue state, and a branch
		// that requires a merge queue also reports direct merges blocked.
		s.syncer.RefreshPullRequestMergeQueueForRepository(ctx, repo.ID, owner)
		stored, err = s.db.GetMergeQueueState(ctx, repo.ID, owner)
	}
	return rules, err == nil && stored != nil && stored.Enabled && stored.BaseBranch == baseBranch
}

func (s *Handler) ensureDeferredMergeTargetUnchanged(
	ctx context.Context, repo db.Repo, number int, queuedTarget deferredMergeTargetSnapshot,
) (platform.MergeRequest, error) {
	mr, err := s.visibleMergeRequest(ctx, repo.ID, number)
	if err != nil {
		return platform.MergeRequest{}, err
	}
	if mr == nil {
		return platform.MergeRequest{}, errors.New("pull request no longer exists")
	}
	if err := deferredMergeTargetMatchesDB(mr, queuedTarget); err != nil {
		return platform.MergeRequest{}, err
	}
	if err := deferredMergeRequireOpenDB(mr); err != nil {
		return platform.MergeRequest{}, err
	}
	reader, err := s.syncer.SyncRegistry().MergeRequestReader(repoProviderKind(repo), repoProviderHost(repo))
	if err != nil {
		return platform.MergeRequest{}, err
	}
	current, err := reader.GetMergeRequest(ctx, platformRepoRefFromDB(repo), number)
	if err != nil {
		return platform.MergeRequest{}, err
	}
	if err := deferredMergeTargetMatchesProvider(current, queuedTarget); err != nil {
		return platform.MergeRequest{}, err
	}
	if err := deferredMergeRequireOpenProvider(current); err != nil {
		return platform.MergeRequest{}, err
	}
	return current, nil
}

func deferredMergeTargetSnapshotFromMR(mr *db.MergeRequest) deferredMergeTargetSnapshot {
	if mr == nil {
		return deferredMergeTargetSnapshot{}
	}
	return deferredMergeTargetSnapshot{
		HeadSHA:    strings.TrimSpace(mr.PlatformHeadSHA),
		BaseBranch: strings.TrimSpace(mr.BaseBranch),
		BaseSHA:    strings.TrimSpace(mr.PlatformBaseSHA),
		NoCI:       queuedDeferredMergeNoCI(mr),
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

// errDeferredMergeTargetClosed fails a queued deferred merge whose pull
// request was closed.
var errDeferredMergeTargetClosed = errors.New("pull request is no longer open; deferred merge was not performed")

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
		return errDeferredMergeTargetClosed
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
		return errDeferredMergeTargetClosed
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

func (s *Handler) broadcastDeferredMergeFailure(repo db.Repo, number int, headSHA string, message string, handle *deferredMergeHandle) {
	// A superseded worker lost its pull request to a successful immediate
	// merge; reporting a deferred-merge failure for it would be misleading.
	if handle != nil && handle.isSuperseded() {
		return
	}
	// Clear pending before announcing the failure, for the same
	// refresh-on-event ordering reason as the success path.
	s.clearDeferredMergeInFlight(deferredMergeKey(repo, number), handle)
	s.publishDeferredMergeFailure(repo, number, headSHA, message)
}

func (s *Handler) publishDeferredMergeFailure(repo db.Repo, number int, headSHA string, message string) {
	slog.Warn("deferred merge failed",
		"provider", repoProviderKind(repo),
		"platform_host", repoProviderHost(repo),
		"repo_path", repo.RepoPath,
		"number", number,
		"err", message,
	)
	s.publish(Event{
		Type: "deferred_merge_completed",
		Data: DeferredMergeCompletedPayload{
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
		// An entry with an origin stands in for a check that does not exist yet.
		// The check reports under its own app and name, so a key would never
		// match; the current snapshot still holds the merge while it is pending.
		if check.Status != "completed" && check.Origin == "" {
			keys = append(keys, deferredMergeCheckKey{App: check.App, Name: check.Name})
		}
	}
	return keys, nil
}

func deferredMergeCheckState(
	aggregateStatus string, keys []deferredMergeCheckKey, checksJSON string, queuedNoCI bool,
) (string, error) {
	aggregateState := deferredMergeAggregateState(aggregateStatus)
	if aggregateState == "failed" {
		return "failed", nil
	}
	checks, err := decodeCIChecks(checksJSON)
	if err != nil {
		return "", err
	}
	// Kenn Forge does not have a provider-neutral required-check model. Deferred
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
		// A pull request that had no CI at all when the merge was queued, and
		// still has none, leaves nothing to wait for. Only a native stack
		// merge queues one, for a target or member below without CI of its
		// own. A check captured as pending must still pass, so any other
		// empty refresh fails closed.
		if queuedNoCI && len(keys) == 0 && deferredMergeNoCI(aggregateStatus, checks) {
			return "passed", nil
		}
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

// deferredMergeNoCI reports a pull request with no CI: no aggregate status and
// no checks.
func deferredMergeNoCI(aggregateStatus string, checks []db.CICheck) bool {
	return strings.TrimSpace(aggregateStatus) == "" && len(checks) == 0
}

// queuedDeferredMergeNoCI reports whether the stored pull request had no CI
// when the merge was queued.
func queuedDeferredMergeNoCI(mr *db.MergeRequest) bool {
	checks, err := decodeCIChecks(mr.CIChecksJSON)
	return err == nil && deferredMergeNoCI(mr.CIStatus, checks)
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

func (s *Handler) markDeferredMergeInFlight(
	key string,
	releases ...func(),
) (*deferredMergeHandle, bool) {
	s.deferredMergeMu.Lock()
	defer s.deferredMergeMu.Unlock()
	if s.deferredMergeInFlight == nil {
		s.deferredMergeInFlight = make(map[string]*deferredMergeHandle)
	}
	if _, ok := s.deferredMergeInFlight[key]; ok {
		return nil, false
	}
	handle := newDeferredMergeHandle()
	if len(releases) > 0 {
		handle.release = releases[0]
	}
	s.deferredMergeInFlight[key] = handle
	return handle, true
}

func (s *Handler) isDeferredMergePending(repo db.Repo, number int) bool {
	s.deferredMergeMu.Lock()
	defer s.deferredMergeMu.Unlock()
	_, ok := s.deferredMergeInFlight[deferredMergeKey(repo, number)]
	return ok
}

// clearDeferredMergeInFlight removes the key only while it still maps to
// handle. Terminal paths clear before broadcasting, so a new deferred merge
// can be queued for the same key before the old worker goroutine runs its
// deferred cleanup; that cleanup must not delete the newer handle.
func (s *Handler) clearDeferredMergeInFlight(key string, handle *deferredMergeHandle) {
	s.deferredMergeMu.Lock()
	defer s.deferredMergeMu.Unlock()
	if s.deferredMergeInFlight[key] == handle {
		delete(s.deferredMergeInFlight, key)
		handle.finish()
	}
}

// supersedeDeferredMerge stands down any queued deferred merge for the key
// after a merge landed through another path. Pending state clears here, before
// callers observe the merge result, so a detail refresh triggered by the merge
// never reports a queued merge that no longer exists.
// It reports whether a queued merge was stood down.
func (s *Handler) supersedeDeferredMerge(key string) bool {
	s.deferredMergeMu.Lock()
	defer s.deferredMergeMu.Unlock()
	handle, ok := s.deferredMergeInFlight[key]
	if !ok {
		return false
	}
	handle.supersede()
	delete(s.deferredMergeInFlight, key)
	handle.finish()
	return true
}

// StopDeferredMergeForTerminalPull ends a queued deferred merge as soon as an
// accepted snapshot, from Forge's own close or from sync, records the pull
// request closed or merged. GitHub disables auto-merge at the close and a
// reopen does not restore it; waiting for the worker's next poll would let a
// close and reopen between polls merge anyway. A close publishes the failure
// the worker would have published; a merge stands down silently.
func (s *Handler) StopDeferredMergeForTerminalPull(ctx context.Context, repoID int64, number int) {
	s.deferredMergeMu.Lock()
	queued := len(s.deferredMergeInFlight) > 0
	s.deferredMergeMu.Unlock()
	if !queued {
		return
	}
	repo, err := s.db.GetRepoByID(ctx, repoID)
	if err != nil {
		slog.Warn("stop deferred merge: get repo failed", "repo_id", repoID, "number", number, "err", err)
		return
	}
	if repo == nil {
		return
	}
	mr, err := s.visibleMergeRequest(ctx, repoID, number)
	if err != nil {
		slog.Warn("stop deferred merge: get pull request failed", "repo_id", repoID, "number", number, "err", err)
		return
	}
	if mr == nil || mr.State == db.MergeRequestStateOpen {
		return
	}
	if !s.supersedeDeferredMerge(deferredMergeKey(*repo, number)) || mr.State == db.MergeRequestStateMerged {
		return
	}
	s.publishDeferredMergeFailure(*repo, number, strings.TrimSpace(mr.PlatformHeadSHA), errDeferredMergeTargetClosed.Error())
}
