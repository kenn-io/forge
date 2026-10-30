package github

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"go.kenn.io/forge/platform"
)

// TriggerRun kicks off a non-blocking ad-hoc sync on the Syncer's
// wait group so callers can request an immediate run without
// blocking the caller. Ad-hoc runs bypass the normal nextSyncAfter
// cadence gate, but still respect hard rate-limit pauses and the
// syncer's lifecycle: Stop cancels the merged context so any
// in-flight GitHub call unblocks, then waits for the goroutine to
// exit. The caller's ctx is honored by a run that starts immediately.
// Once an accepted trigger is coalesced behind an active run, the follow-up
// is owned by the syncer's lifecycle so caller completion cannot retract it.
// It returns true only after the request is retained as active or pending work.
func (s *Syncer) TriggerRun(ctx context.Context) bool {
	return s.TriggerRunWithPriority(ctx, nil)
}

// TriggerRunWithPriority kicks off a non-blocking ad-hoc sync and dispatches
// matching repos before the rest of the configured set.
func (s *Syncer) TriggerRunWithPriority(
	ctx context.Context,
	priorityRepos []RepoRef,
) bool {
	return s.triggerRun(ctx, slices.Clone(priorityRepos), nil)
}

// TriggerRunForRepos kicks off a non-blocking ad-hoc sync restricted to the
// matching configured repositories.
func (s *Syncer) TriggerRunForRepos(ctx context.Context, repos []RepoRef) bool {
	return s.triggerRun(ctx, nil, slices.Clone(repos))
}

func (s *Syncer) triggerRun(
	ctx context.Context,
	priorityRepos []RepoRef,
	onlyRepos []RepoRef,
) bool {
	return s.triggerRunWithCadence(ctx, true, priorityRepos, onlyRepos)
}

func (s *Syncer) triggerRunWithCadence(
	ctx context.Context,
	bypassNextSyncAfter bool,
	priorityRepos []RepoRef,
	onlyRepos []RepoRef,
) bool {
	if !s.SyncEnabled() {
		return false
	}

	// Admission and lifecycle registration are one operation from the
	// caller's perspective: a successful return means Stop cannot miss the
	// work and an active run has already retained the request.
	s.lifecycleMu.Lock()
	if s.stopped {
		s.lifecycleMu.Unlock()
		return false
	}
	s.runMu.Lock()
	if s.running.Load() {
		accepted := bypassNextSyncAfter || onlyRepos == nil && s.exclusiveRun
		if accepted {
			s.coalescePendingRunLocked(
				bypassNextSyncAfter, priorityRepos, onlyRepos,
			)
		}
		s.runMu.Unlock()
		s.lifecycleMu.Unlock()
		return accepted
	}
	s.running.Store(true)
	s.exclusiveRun = onlyRepos != nil
	s.runMu.Unlock()

	s.startClaimedRunLocked(
		ctx, bypassNextSyncAfter, priorityRepos, onlyRepos, nil,
	)
	s.lifecycleMu.Unlock()
	return true
}

// startClaimedRunLocked registers and starts work whose single-flight slot is
// already owned. The caller must hold lifecycleMu and must have checked stopped.
func (s *Syncer) startClaimedRunLocked(
	ctx context.Context,
	bypassNextSyncAfter bool,
	priorityRepos []RepoRef,
	onlyRepos []RepoRef,
	bypassRepos []RepoRef,
) {
	merged, cancel := s.mergeWithRunCtx(ctx)
	s.wg.Go(func() {
		defer cancel()
		s.runOnceWithSlot(
			merged,
			bypassNextSyncAfter,
			priorityRepos,
			onlyRepos,
			bypassRepos,
			true,
		)
	})
}

func (s *Syncer) launchClaimedRun(
	ctx context.Context,
	bypassNextSyncAfter bool,
	priorityRepos []RepoRef,
	onlyRepos []RepoRef,
	bypassRepos []RepoRef,
) bool {
	if !s.SyncEnabled() {
		return false
	}
	s.lifecycleMu.Lock()
	if s.stopped {
		s.lifecycleMu.Unlock()
		return false
	}
	s.startClaimedRunLocked(
		ctx, bypassNextSyncAfter, priorityRepos, onlyRepos, bypassRepos,
	)
	s.lifecycleMu.Unlock()
	return true
}

// runState holds the per-RunOnce mutable state shared by the
// worker pool. Extracted into a struct so runWorker can be a
// directly testable method instead of an inline closure.
type runState struct {
	completed               *atomic.Int32
	maxShown                *atomic.Int32
	errMu                   *sync.Mutex
	lastErr                 *string
	lastErrorCode           *SyncErrorCode
	lastErrorCeilingKey     *string
	lastErrorCeilingResetAt *string
	// canceled is latched to true at the moment any goroutine
	// observes ctx cancellation while work is still outstanding.
	// RunOnce uses this flag (rather than a completed-count
	// heuristic) to decide whether the run was canceled, so a
	// misbehaving syncRepo that ignores ctx and returns success
	// cannot mask cancellation.
	canceled *atomic.Bool
	total    int
	// results is a preallocated slice indexed by repo position so
	// OnSyncCompleted receives results in the configured repo order
	// regardless of worker completion order. Each index is written
	// by exactly one worker, so no mutex is needed.
	results []RepoSyncResult
	// exhausted collects buckets that crossed their reserve while the pass
	// was running. Eligibility is computed once before dispatch, so without
	// this the detail and comment drains would keep spending a credential the
	// workers had already stopped using. Written by every worker, so it is a
	// sync.Map rather than the plain eligibility map.
	exhausted *sync.Map
}

// repoWork pairs a repo with its index in the configured repo list
// so workers can write results to the correct preallocated slot.
type repoWork struct {
	index int
	repo  RepoRef
}

func (s *Syncer) recordRunError(
	state *runState,
	repo RepoRef,
	err error,
	errMessage string,
) {
	errorCode := syncErrorCodeFor(err)
	ceilingKey := ""
	ceilingResetAt := ""
	if errorCode == SyncErrorCodeLocalCeilingExhausted {
		identity, identityErr := s.identityForRepo(repo, false)
		if identityErr == nil {
			ceilingKey = RateStatusKey(
				string(repoPlatform(repo)), identity.Host, identity.Principal,
			)
		}
		if exhausted, ok := errors.AsType[*syncBudgetExhaustedError](err); ok {
			ceilingResetAt = exhausted.resetAt.UTC().Format(time.RFC3339)
		}
	}

	state.errMu.Lock()
	defer state.errMu.Unlock()
	if *state.lastErrorCode == SyncErrorCodeLocalCeilingExhausted &&
		errorCode != SyncErrorCodeLocalCeilingExhausted {
		return
	}
	*state.lastErr = errMessage
	*state.lastErrorCode = errorCode
	*state.lastErrorCeilingKey = ceilingKey
	*state.lastErrorCeilingResetAt = ceilingResetAt
}

// runWorker drains the work channel until it is closed or ctx
// is canceled. It is the body of each goroutine spawned by
// RunOnce. Extracted from the inline closure so cancellation
// behavior can be unit-tested directly without racing against
// the dispatch loop.
func (s *Syncer) runWorker(
	ctx context.Context,
	work <-chan repoWork,
	state *runState,
) {
	for item := range work {
		repo := item.repo
		// Defense-in-depth against the dispatch race: the
		// dispatch loop pre-checks ctx before its select, but
		// a cancel can still land in the micro-window between
		// the pre-check and the select, in which case Go's
		// select may pick the send branch and hand this worker
		// a repo that should never have been enqueued. Bail
		// here before logging or starting any work, and latch
		// the canceled flag so RunOnce reports the run as
		// canceled regardless of how many repos happened to
		// finish in parallel.
		if ctx.Err() != nil {
			state.canceled.Store(true)
			return
		}
		bucket, bucketErr := s.bucketKeyForRepo(repo, false)
		if bucketErr != nil {
			errMessage := bucketErr.Error()
			s.recordRunError(state, repo, bucketErr, errMessage)
			s.recordRepoSync(repo, bucketErr)
			state.results[item.index].Error = errMessage
			continue
		}
		// Provider reserve first: this credential's own GitHub pool. The
		// tracker backoff below is a separate signal (secondary limits and
		// Retry-After), so both gates apply.
		//
		// Crossing the reserve mid-pass skips the repository rather than
		// sleeping the worker until reset. Dispatch already reports the same
		// result for buckets that were over their reserve before the pass
		// began, and holding a worker for up to an hour would let one spent
		// credential occupy the whole pool while repositories on healthy
		// credentials wait behind it. The next pass re-admits this one.
		if s.backgroundReserveExhausted(repo, QuotaResourceREST, false) {
			state.results[item.index].Error = "skipped: rate limit throttled"
			if state.exhausted != nil {
				state.exhausted.Store(bucket, struct{}{})
			}
			done := state.completed.Add(1)
			s.publishMonotonicProgress(state, done)
			continue
		}
		if rt := s.rateTrackers[bucket]; rt != nil {
			if backoff, wait := rt.ShouldBackoff(); backoff {
				s.publishStatus(&SyncStatus{
					Running:  true,
					Progress: "rate limited, waiting " + formatRateLimitWait(wait),
				})
				select {
				case <-time.After(wait):
				case <-ctx.Done():
					state.canceled.Store(true)
					return
				}
			}
		}
		repoName := repo.Owner + "/" + repo.Name
		slog.Info("syncing repo", "repo", repoName)
		nativeResultKey := repoFailKey(repo)
		s.nativeStackResults.Delete(nativeResultKey)
		resolved := repo
		err := s.syncRepoResolving(ctx, repo, &resolved)
		if nativeResult, ok := s.nativeStackResults.LoadAndDelete(nativeResultKey); ok {
			state.results[item.index].GitHubNativeStacks = nativeResult.(*GitHubNativeStackSyncResult)
		}
		if err != nil {
			// Bail without counting this repo only when the
			// *run* context itself is canceled and the error
			// reflects that. Per-request timeouts also come
			// back as wrapped context.DeadlineExceeded but
			// must reach the normal error path so they're
			// captured in lastErr instead of being silently
			// dropped.
			if ctx.Err() != nil &&
				(errors.Is(err, context.Canceled) ||
					errors.Is(err, context.DeadlineExceeded)) {
				state.canceled.Store(true)
				return
			}
			errStr := err.Error()
			if syncBudgetWait(err) {
				slog.Info("repo waits for sync budget", "repo", repoName, "err", err)
			} else {
				slog.Error("sync repo failed", "repo", repoName, "err", err)
			}
			s.recordRunError(state, repo, err, errStr)
			// Each index is written by exactly one worker. The partial
			// typing requires the whole error to be the partial failure:
			// a partial joined with any other failure is hard.
			state.results[item.index].Error = errStr
			if partial, ok := ExclusivePartialSyncFailure(err); ok {
				state.results[item.index].PartialFailure = partial
			}
		}
		// Latch the canceled flag if ctx was canceled during
		// syncRepo. A misbehaving Client implementation can
		// ignore ctx and return nil (or a non-context error)
		// even after cancellation; without this check the run
		// would fall through to the success path and fire
		// onSyncCompleted for what the user asked to cancel.
		if ctx.Err() != nil {
			state.canceled.Store(true)
			return
		}
		s.recordRepoSync(resolved, err)
		done := state.completed.Add(1)
		s.publishMonotonicProgress(state, done)
	}
}

func syncErrorCodeFor(err error) SyncErrorCode {
	if errors.Is(err, platform.ErrSyncBudgetExhausted) {
		return SyncErrorCodeLocalCeilingExhausted
	}
	return ""
}

// revokeExhaustedBuckets clears eligibility for credentials that have reached
// their reserve since eligibility was computed. It folds in the markers workers
// recorded and re-reads the current pools, because a credential can cross its
// reserve on the very last repository that uses it, leaving no later worker to
// notice.
func (s *Syncer) revokeExhaustedBuckets(
	eligibleBuckets map[string]bool, state *runState, repos []RepoRef,
) {
	if state != nil && state.exhausted != nil {
		state.exhausted.Range(func(key, _ any) bool {
			eligibleBuckets[key.(string)] = false
			return true
		})
	}
	checked := make(map[string]struct{}, len(eligibleBuckets))
	for _, repo := range repos {
		bucket, err := s.bucketKeyForRepo(repo, false)
		if err != nil || !eligibleBuckets[bucket] {
			continue
		}
		if _, seen := checked[bucket]; seen {
			continue
		}
		checked[bucket] = struct{}{}
		if s.backgroundReserveExhausted(repo, QuotaResourceREST, false) {
			eligibleBuckets[bucket] = false
		}
	}
}

// detailPhaseProgress is the running progress once every repository's index
// has finished, while the detail and comment drains spend the remaining budget.
const detailPhaseProgress = "refreshing details"

// publishMonotonicProgress publishes a progress update only if done
// is the highest value seen so far. The final "total/total" reports
// the detail phase instead, because detail drain still runs after
// index completes.
// Both worker completions and throttled-repo skips use this to
// guarantee SSE progress never regresses.
func (s *Syncer) publishMonotonicProgress(
	state *runState, done int32,
) {
	if int(done) > state.total {
		return
	}
	for {
		cur := state.maxShown.Load()
		if done <= cur {
			return
		}
		if state.maxShown.CompareAndSwap(cur, done) {
			progress := fmt.Sprintf("%d/%d", done, state.total)
			if int(done) == state.total {
				progress = detailPhaseProgress
			}
			s.publishStatus(&SyncStatus{
				Running:  true,
				Progress: progress,
			})
			return
		}
	}
}

// RunOnce performs a single sync pass across all configured repos.
// If a sync is already in progress it returns immediately (single-flight).
//
// Repos are synced in parallel using a bounded worker pool sized by
// SetParallelism (default defaultParallelism). The bound keeps the
// per-host GitHub rate limit and abuse-detection thresholds happy
// while still capturing most of the wall-clock win on network I/O.
func (s *Syncer) RunOnce(ctx context.Context) {
	if !s.AutomaticSyncEnabled() {
		return
	}
	s.runOnce(ctx, false, nil, nil)
}

func (s *Syncer) runOnce(
	ctx context.Context,
	bypassNextSyncAfter bool,
	priorityRepos []RepoRef,
	onlyRepos []RepoRef,
) {
	s.runOnceWithSlot(ctx, bypassNextSyncAfter, priorityRepos, onlyRepos, nil, false)
}

func (s *Syncer) runOnceWithSlot(
	ctx context.Context,
	bypassNextSyncAfter bool,
	priorityRepos []RepoRef,
	onlyRepos []RepoRef,
	bypassRepos []RepoRef,
	slotClaimed bool,
) {
	if !s.SyncEnabled() {
		if slotClaimed {
			s.releaseRunSlot()
		}
		return
	}
	s.runMu.Lock()
	if !slotClaimed {
		if s.running.Load() {
			if bypassNextSyncAfter || onlyRepos == nil && s.exclusiveRun {
				s.coalescePendingRunLocked(
					bypassNextSyncAfter, priorityRepos, onlyRepos,
				)
				s.pendingRun.startupRetryRepos = appendUniqueRepoRefs(
					s.pendingRun.startupRetryRepos, bypassRepos,
				)
			}
			s.runMu.Unlock()
			return
		}
		s.running.Store(true)
		s.exclusiveRun = onlyRepos != nil
	}
	for key, last := range s.startupRepoSyncTimes {
		if _, exists := s.nextSyncAfter[key]; !exists && s.rateTrackers[key] != nil {
			s.nextSyncAfter[key] = last.Add(s.interval)
		}
	}
	s.startupRepoSyncTimes = nil
	s.runMu.Unlock()
	var terminalStatus *SyncStatus
	defer func() {
		// Serialize the terminal snapshot with slot release. A new run may
		// claim runMu as soon as running becomes false, but its Running:true
		// publication must follow this pass's terminal status.
		s.statusMu.Lock()
		if terminalStatus != nil && ctx.Err() != nil {
			// Cancellation cannot establish a fresh backlog count.
			terminalStatus.DetailRefreshOverdue = s.Status().DetailRefreshOverdue
		}
		s.runMu.Lock()
		pending := s.pendingRun
		s.pendingRun = nil
		if pending != nil && !s.AutomaticSyncEnabled() && !pending.bypassNextSyncAfter {
			pending.startupRetryRepos = nil
			if len(pending.bypassRepos) == 0 {
				pending = nil
			} else {
				pending.full = false
				pending.onlyRepos = pending.bypassRepos
			}
		}
		if pending == nil {
			s.running.Store(false)
			s.exclusiveRun = false
		} else {
			// Keep ownership of the single-flight slot while the accepted
			// follow-up is registered. New arrivals coalesce behind it.
			s.exclusiveRun = !pending.full
		}
		s.runMu.Unlock()
		if pending == nil {
			if terminalStatus != nil {
				s.publishStatusLocked(terminalStatus)
			}
			s.statusMu.Unlock()
			// A completed sync can change archive eligibility, for example by
			// clearing a repository feature cooldown, so the idle archive
			// worker must not wait out its backoff to notice.
			s.WakeArchive()
			return
		}
		s.statusMu.Unlock()
		only := pending.onlyRepos
		if pending.full {
			only = nil
		}
		launched := s.launchClaimedRun(
			context.Background(),
			pending.bypassNextSyncAfter,
			pending.priorityRepos,
			only,
			appendUniqueRepoRefs(pending.bypassRepos, pending.startupRetryRepos),
		)
		if !launched {
			s.releaseRunSlotWithStatus(terminalStatus)
		}
	}()

	rateLimitSnapshotCtx := ctx
	if bypassNextSyncAfter {
		ctx = withRepositoryFeatureCooldownBypass(
			ctx, s.featureCooldowns.currentGeneration(),
		)
	} else if len(bypassRepos) > 0 {
		ctx = withRepositoryFeatureCooldownBypassForRepos(
			ctx, s.featureCooldowns.currentGeneration(), bypassRepos,
		)
	}

	// Mark context so the budget transport counts HTTP calls
	// made during background sync. User-initiated server
	// handler paths do not carry this key and are not counted.
	ctx = WithSyncSpendKind(WithSyncBudget(ctx), SyncSpendDetail)
	var pass *syncPass
	if onlyRepos == nil && s.onSyncFailed != nil {
		pass = &syncPass{}
		ctx = context.WithValue(ctx, syncPassKey{}, pass)
	}
	s.convertPendingGitHubRepositories(ctx)

	s.reposMu.Lock()
	repos := slices.Clone(s.repos)
	s.reposMu.Unlock()
	if onlyRepos != nil {
		repos = selectRepos(repos, onlyRepos)
	}
	nextAfter := s.nextSyncAfter
	if bypassNextSyncAfter {
		nextAfter = nil
	}
	// Computed over the selected set before still-archived refs drop
	// out, so buckets holding only archived repositories keep an entry
	// for the cadence advance below.
	archivedEligibility := s.repoEligibility(repos, nextAfter)
	archivedBypassEligibility := s.repoEligibility(
		selectRepos(repos, bypassRepos), nil,
	)
	repos = s.reconcileArchivedRepos(
		ctx,
		repos,
		archivedEligibility,
		bypassRepos,
		archivedBypassEligibility,
	)
	repos = prioritizeRepos(repos, priorityRepos)

	total := len(repos)
	s.publishStatus(&SyncStatus{
		Running:  true,
		Progress: fmt.Sprintf("0/%d", total),
	})
	slog.Info("sync started", "repos", total)
	s.resetPendingCommentSyncs()

	workers := min(max(int(s.parallelism.Load()), 1), total)

	work := make(chan repoWork)
	results := make([]RepoSyncResult, total)
	for i, r := range repos {
		host := r.PlatformHost
		if host == "" {
			host = "github.com"
		}
		results[i] = RepoSyncResult{
			Platform:     repoPlatform(r),
			Owner:        r.Owner,
			Name:         r.Name,
			PlatformHost: host,
		}
	}

	if rateLimitSnapshotCtx.Err() == nil {
		refreshed := s.refreshRateLimitSnapshots(rateLimitSnapshotCtx)
		s.clearRecoveredRateLimitGates(refreshed, nextAfter, s.interval)
	}
	cadenceEligibleBuckets := s.repoEligibility(repos, nextAfter)
	bypassEligibleBuckets := s.repoEligibility(
		selectRepos(repos, bypassRepos), nil,
	)
	eligibleBuckets := maps.Clone(cadenceEligibleBuckets)
	activeRepos := make([]RepoRef, 0, len(repos))

	var (
		completed               atomic.Int32
		maxShown                atomic.Int32
		errMu                   sync.Mutex
		lastErr                 string
		lastErrorCode           SyncErrorCode
		lastErrorCeilingKey     string
		lastErrorCeilingResetAt string
		canceled                atomic.Bool
		wg                      sync.WaitGroup
	)

	state := &runState{
		completed:               &completed,
		maxShown:                &maxShown,
		errMu:                   &errMu,
		lastErr:                 &lastErr,
		lastErrorCode:           &lastErrorCode,
		lastErrorCeilingKey:     &lastErrorCeilingKey,
		lastErrorCeilingResetAt: &lastErrorCeilingResetAt,
		canceled:                &canceled,
		total:                   total,
		results:                 results,
		exhausted:               &sync.Map{},
	}
	for range workers {
		wg.Go(func() {
			s.runWorker(ctx, work, state)
		})
	}

dispatch:
	for i, r := range repos {
		bucket, err := s.bucketKeyForRepo(r, false)
		if err != nil {
			s.observeRepoFailure(ctx, r, err)
			s.recordRepoSync(r, err)
			results[i].Error = err.Error()
			done := completed.Add(1)
			s.publishMonotonicProgress(state, done)
			continue
		}
		eligible := cadenceEligibleBuckets[bucket]
		if !eligible && repoMatchesAnyIntent(r, bypassRepos) {
			eligible = bypassEligibleBuckets[bucket]
			if eligible {
				eligibleBuckets[bucket] = true
			}
		}
		if !eligible {
			results[i].Error = "skipped: rate limit throttled"
			done := completed.Add(1)
			s.publishMonotonicProgress(state, done)
			continue
		}
		activeRepos = append(activeRepos, r)
		// Check ctx before entering the select. Go's select picks
		// pseudo-randomly when both branches are ready, so a naked
		// `select { case work <- r: case <-ctx.Done(): }` can still
		// hand a repo to a ready worker after the run has been
		// canceled. The pre-check biases the loop toward cancel so
		// the dispatch reliably stops once ctx is done.
		if ctx.Err() != nil {
			canceled.Store(true)
			break dispatch
		}
		item := repoWork{index: i, repo: r}
		select {
		case work <- item:
		case <-ctx.Done():
			canceled.Store(true)
			break dispatch
		}
	}
	close(work)
	wg.Wait()

	// Detail drain: fetch full details for highest-priority items
	// within the per-host budget. Runs after index scan completes.
	if !canceled.Load() && ctx.Err() == nil {
		// Eligibility was computed before the pass, so it cannot know which
		// credentials ran out while the pass was in flight. Re-check every
		// bucket rather than trusting only the workers' markers: the last
		// repository on a credential can spend the headroom with no later
		// repository left to observe it.
		s.revokeExhaustedBuckets(eligibleBuckets, state, activeRepos)
		s.drainDetailQueue(ctx, eligibleBuckets, activeRepos)
	}

	if !canceled.Load() && ctx.Err() == nil {
		// The detail drain spends the same credentials, so re-check again
		// before the comment drain rather than reusing a map the drain that
		// just ran may have invalidated.
		s.revokeExhaustedBuckets(eligibleBuckets, state, activeRepos)
		s.drainPendingCommentSyncs(ctx, eligibleBuckets)
	}
	if !canceled.Load() && ctx.Err() == nil {
		s.finishSyncPass(pass)
	}

	// Use a latched flag (set by the dispatch loop and workers at
	// the moment they observe ctx cancellation) rather than a
	// completed-count heuristic. A misbehaving syncRepo that
	// ignores ctx and returns success would otherwise let the
	// run fall through to onSyncCompleted even though the user
	// asked to cancel. A cancel that races in strictly *after*
	// every worker finished and returned never latches the flag,
	// so the late-cancel-after-clean-sync case still reports
	// success.
	if canceled.Load() {
		err := ctx.Err()
		if err == nil {
			err = context.Canceled
		}
		slog.Info("sync canceled", "repos", total, "err", err)
		terminalStatus = &SyncStatus{
			Running:   false,
			LastRunAt: time.Now().UTC(),
			LastError: err.Error(),
		}
		return
	}

	if rateLimitSnapshotCtx.Err() == nil {
		s.RefreshRateLimitSnapshots(rateLimitSnapshotCtx)
	}
	if onlyRepos == nil {
		for bucket, eligible := range cadenceEligibleBuckets {
			if eligible && !eligibleBuckets[bucket] {
				cadenceEligibleBuckets[bucket] = false
			}
		}
		// Archived-only buckets are absent from eligibleBuckets — their
		// refs dropped out before it was computed — but an attempted
		// archived refresh must advance the bucket's cadence gate too,
		// or the refresh would rerun every base interval regardless of
		// throttle factor. The post-reconciliation value wins for
		// buckets present in both maps.
		advance := maps.Clone(archivedEligibility)
		maps.Copy(advance, cadenceEligibleBuckets)
		s.advanceNextSync(advance, s.nextSyncAfter, s.interval)
	}

	slog.Info("sync complete", "repos", total)

	if s.onSyncCompleted != nil {
		s.RunUnderStackProjection(func() {
			s.dropStaleNativeStackResults(results)
			s.onSyncCompleted(results)
		})
	}

	terminalStatus = &SyncStatus{
		Running:                 false,
		LastRunAt:               time.Now().UTC(),
		LastError:               lastErr,
		LastErrorCode:           lastErrorCode,
		LastErrorCeilingKey:     lastErrorCeilingKey,
		LastErrorCeilingResetAt: lastErrorCeilingResetAt,
		DetailRefreshOverdue:    s.countOverdueDetails(ctx),
	}
}

func (s *Syncer) releaseRunSlot() {
	s.runMu.Lock()
	s.running.Store(false)
	s.exclusiveRun = false
	s.pendingRun = nil
	s.runMu.Unlock()
}

func (s *Syncer) releaseRunSlotWithStatus(status *SyncStatus) {
	s.statusMu.Lock()
	defer s.statusMu.Unlock()
	s.releaseRunSlot()
	if status != nil {
		s.publishStatusLocked(status)
	}
}

// coalescePendingRunLocked records work accepted while another pass owns the
// single-flight slot. A full pass subsumes scoped work; the scoped repositories
// become priorities so the stronger request still reaches them first.
func (s *Syncer) coalescePendingRunLocked(
	bypassNextSyncAfter bool,
	priorityRepos []RepoRef,
	onlyRepos []RepoRef,
) {
	full := onlyRepos == nil
	bypassAll := bypassNextSyncAfter && full
	var bypassRepos []RepoRef
	if bypassNextSyncAfter && !full {
		bypassRepos = onlyRepos
	}
	if s.pendingRun == nil {
		s.pendingRun = &pendingSyncRun{
			bypassNextSyncAfter: bypassAll,
			full:                full,
			priorityRepos:       appendUniqueRepoRefs(nil, priorityRepos),
			onlyRepos:           uniqueRepoRefsPreservingNil(onlyRepos),
			bypassRepos:         appendUniqueRepoRefs(nil, bypassRepos),
		}
		return
	}

	pending := s.pendingRun
	pending.bypassNextSyncAfter = pending.bypassNextSyncAfter || bypassAll
	pending.bypassRepos = appendUniqueRepoRefs(pending.bypassRepos, bypassRepos)
	if full {
		if !pending.full {
			pending.priorityRepos = appendUniqueRepoRefs(
				pending.priorityRepos, pending.onlyRepos,
			)
			pending.onlyRepos = nil
			pending.full = true
		}
		pending.priorityRepos = appendUniqueRepoRefs(
			pending.priorityRepos, priorityRepos,
		)
		return
	}

	if pending.full {
		pending.priorityRepos = appendUniqueRepoRefs(
			pending.priorityRepos, onlyRepos,
		)
	} else {
		pending.onlyRepos = appendUniqueRepoRefs(pending.onlyRepos, onlyRepos)
	}
	pending.priorityRepos = appendUniqueRepoRefs(
		pending.priorityRepos, priorityRepos,
	)
}

func uniqueRepoRefsPreservingNil(repos []RepoRef) []RepoRef {
	if repos == nil {
		return nil
	}
	return appendUniqueRepoRefs(make([]RepoRef, 0, len(repos)), repos)
}

func appendUniqueRepoRefs(dst []RepoRef, repos []RepoRef) []RepoRef {
	for _, repo := range repos {
		if slices.ContainsFunc(dst, func(existing RepoRef) bool {
			return sameRepoIntent(existing, repo)
		}) {
			continue
		}
		dst = append(dst, repo)
	}
	return dst
}

func prioritizeRepos(repos, priorityRepos []RepoRef) []RepoRef {
	if len(repos) == 0 || len(priorityRepos) == 0 {
		return repos
	}

	priorityOrder := make(map[RepoRef]int, len(repos))
	for _, repo := range repos {
		for i, priority := range priorityRepos {
			if sameRepoIntent(repo, priority) {
				priorityOrder[repo] = i
				break
			}
		}
	}
	if len(priorityOrder) == 0 {
		return repos
	}

	out := slices.Clone(repos)
	slices.SortStableFunc(out, func(a, b RepoRef) int {
		ai, aOK := priorityOrder[a]
		bi, bOK := priorityOrder[b]
		switch {
		case aOK && bOK:
			return ai - bi
		case aOK:
			return -1
		case bOK:
			return 1
		default:
			return 0
		}
	})
	return out
}

func excludeArchivedRepos(repos []RepoRef) []RepoRef {
	live := make([]RepoRef, 0, len(repos))
	skipped := make([]string, 0)
	for _, repo := range repos {
		if repo.Archived {
			skipped = append(skipped, repo.Owner+"/"+repo.Name)
			continue
		}
		live = append(live, repo)
	}
	if len(skipped) > 0 {
		slog.Debug("skipping archived repos in live sync", "repos", skipped)
	}
	return live
}

func selectRepos(repos, selectedRepos []RepoRef) []RepoRef {
	out := make([]RepoRef, 0, len(selectedRepos))
	for _, repo := range repos {
		if slices.ContainsFunc(selectedRepos, func(selected RepoRef) bool {
			return sameRepoIntent(repo, selected)
		}) {
			out = append(out, repo)
		}
	}
	return out
}

// sameRepoIntent matches stable provider identity whenever both refs have one.
// Route fallback is reserved for refs that have not yet been provider-verified.
func sameRepoIntent(a, b RepoRef) bool {
	if repoPlatform(a) != repoPlatform(b) ||
		!strings.EqualFold(repoHost(a), repoHost(b)) {
		return false
	}
	aKey := a.Key
	bKey := b.Key
	if !aKey.IsZero() && !bKey.IsZero() {
		return aKey == bKey
	}
	aRoute := repoPriorityKey(a)
	return aRoute != "" && aRoute == repoPriorityKey(b)
}

func repoMatchesAnyIntent(repo RepoRef, intents []RepoRef) bool {
	return slices.ContainsFunc(intents, func(intent RepoRef) bool {
		return sameRepoIntent(repo, intent)
	})
}

func repoPriorityKey(repo RepoRef) string {
	repoPath := strings.Trim(strings.TrimSpace(repo.RepoPath), "/")
	if repoPath == "" {
		owner := strings.Trim(strings.TrimSpace(repo.Owner), "/")
		name := strings.Trim(strings.TrimSpace(repo.Name), "/")
		if owner == "" || name == "" {
			return ""
		}
		repoPath = owner + "/" + name
	}
	return strings.ToLower(
		string(repoPlatform(repo)) + "/" + repoHost(repo) + "/" + repoPath,
	)
}
