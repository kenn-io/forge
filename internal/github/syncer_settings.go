package github

import (
	"context"
	"slices"
	"time"

	"go.kenn.io/forge/internal/db"
	"go.kenn.io/forge/platform"
)

// SetWatchInterval sets the fast-sync interval for watched MRs.
func (s *Syncer) SetWatchInterval(d time.Duration) {
	s.watchMu.Lock()
	defer s.watchMu.Unlock()
	s.watchInterval = d
}

// SetActiveMRWindow sets the recency window used to add open, recently
// active MRs to the fast-sync watch list.
func (s *Syncer) SetActiveMRWindow(d time.Duration) {
	s.watchMu.Lock()
	defer s.watchMu.Unlock()
	s.activeMRWindow = d
}

// SetActiveMRRefreshPolicy configures recency-based hot admission and warm cadence.
// A zero hot window preserves view-only hot admission.
func (s *Syncer) SetActiveMRRefreshPolicy(hotWindow, warmInterval time.Duration) {
	s.watchMu.Lock()
	defer s.watchMu.Unlock()
	s.activeMRHotWindow = hotWindow
	s.activeMRWarmInterval = warmInterval
}

// SetPreferGitHubNativeStacks enables GitHub's read-only preview metadata for
// subsequent repository syncs. Existing branch inference remains the fallback.
// It returns the previous value so a caller reconciles on the transition it
// actually performed: two concurrent config writers reading the preference from
// their own config snapshots could otherwise both believe they turned it off.
func (s *Syncer) SetPreferGitHubNativeStacks(enabled bool) bool {
	// The swap takes the projection lock so a transition cannot land while a
	// reconciliation or a sync hook is mid-projection. Rechecking the preference
	// inside that lock is only sound if no swap can interleave with the check.
	s.stackProjectionMu.Lock()
	defer s.stackProjectionMu.Unlock()
	previous := s.preferGitHubNativeStacks.Swap(enabled)
	if previous != enabled {
		// Invalidate results captured under the old preference before any
		// caller reconciles projections.
		s.nativeStackGeneration.Add(1)
	}
	if !enabled || previous {
		return previous
	}
	s.nativeStackConfirmations.Range(func(key, _ any) bool {
		s.nativeStackConfirmations.Delete(key)
		return true
	})
	// A cached validator may describe a PR response fetched before native
	// metadata was enabled. Force one fresh list response so the first enabled
	// sync can seed hints; later syncs retain the normal ETag optimization.
	for _, repo := range s.TrackedRepos() {
		if repoPlatform(repo) != platform.KindGitHub {
			continue
		}
		if client, ok := s.optionalGitHubClientFor(repo); ok {
			client.InvalidateListETagsForRepo(repo.Owner, repo.Name, "pulls")
		}
	}
	return previous
}

func (s *Syncer) watchSettings() (time.Duration, time.Duration) {
	s.watchMu.Lock()
	defer s.watchMu.Unlock()
	return s.watchSettingsLocked()
}

// HasDiffSync reports whether the syncer has a clone manager configured
// and is therefore expected to populate diff SHAs for tracked PRs. The
// HTTP layer uses this to decide whether a missing diff is a sync issue
// worth warning about, or simply a deployment that opted out of diffs.
func (s *Syncer) HasDiffSync() bool {
	return s.clones != nil
}

// SetWatchedMRs replaces the fast-sync watch list. Each watched
// MR is synced on the watch interval via SyncMR, independent of
// the bulk sync cycle.
func (s *Syncer) SetWatchedMRs(mrs []WatchedMR) {
	s.watchMu.Lock()
	s.watchedMRs = slices.Clone(mrs)
	s.watchMu.Unlock()
}

// SetOnMRSynced registers a callback invoked after each MR
// is upserted during a sync pass.
//
// Concurrency: RunOnce processes repos in parallel (see
// SetParallelism), so the callback may be invoked from up to
// `parallelism` goroutines concurrently. Implementations must
// be safe for concurrent use. The callback also runs on the
// goroutine that is mid-sync for a repo, so it must not block
// indefinitely or it will stall sync progress.
//
// Call SetOnMRSynced before Start/RunOnce. Mutating the hook
// while a sync is in flight is not safe.
func (s *Syncer) SetOnMRSynced(
	fn func(owner, name string, mr *db.MergeRequest),
) {
	s.onMRSynced = fn
}

// SetOnSyncFailed registers a callback for sources that start failing in a full pass.
func (s *Syncer) SetOnSyncFailed(fn func(source, reason string)) {
	s.onSyncFailed = fn
}

// SetOnSyncCompleted registers a callback invoked at the end
// of each RunOnce pass with per-repo sync results.
//
// Concurrency: this hook fires once per RunOnce pass on the
// goroutine that drives RunOnce, so it is not invoked
// concurrently with itself. Call SetOnSyncCompleted before
// Start/RunOnce; mutating the hook while a sync is in flight
// is not safe.
func (s *Syncer) SetOnSyncCompleted(
	fn func(results []RepoSyncResult),
) {
	s.onSyncCompleted = fn
}

// SetOnWatchedMRSyncCompleted registers a callback invoked once after a
// watched-MR fast-sync pass refreshes at least one MR.
func (s *Syncer) SetOnWatchedMRSyncCompleted(fn func()) {
	s.onWatchedMRSyncCompleted = fn
}

// SetOnMergedActorRepaired registers a callback invoked after scheduled
// reconciliation persists an authored merged event. RunOnce can process
// repositories in parallel, so the callback must be concurrency-safe.
// Register it before Start or RunOnce.
func (s *Syncer) SetOnMergedActorRepaired(
	fn func(context.Context, int64, int),
) {
	s.onMergedActorRepaired = fn
}

// SetOnMergeQueueChanged registers a callback invoked for each pull request
// whose merge queue membership, position, or state changed. RunOnce can
// process repositories in parallel, so the callback must be concurrency-safe.
// Register it before Start or RunOnce.
func (s *Syncer) SetOnMergeQueueChanged(
	fn func(context.Context, int64, int),
) {
	s.onMergeQueueChanged = fn
}

// SetOnMergeBypassChanged registers a callback invoked for each pull request
// whose recorded merge bypass permission changed. Register it before Start or
// RunOnce; it must be concurrency-safe.
func (s *Syncer) SetOnMergeBypassChanged(
	fn func(context.Context, int64, int),
) {
	s.onMergeBypassChanged = fn
}

// SetOnMergeRequestTerminal registers a callback invoked after an accepted
// provider snapshot records a merge request merged or closed. RunOnce can
// process repositories in parallel, so the callback must be concurrency-safe
// and must not block. Register it before Start or RunOnce.
func (s *Syncer) SetOnMergeRequestTerminal(
	fn func(context.Context, int64, int),
) {
	s.onMergeRequestTerminal = fn
}

// SetParallelism sets the maximum number of repos synced
// concurrently in RunOnce. Values <= 0 are clamped to 1
// (sequential).
func (s *Syncer) SetParallelism(n int) {
	if n < 1 {
		n = 1
	}
	s.parallelism.Store(int32(n))
}

// SetBranchActivityLimits configures how much default-branch commit
// activity the syncer persists.
func (s *Syncer) SetBranchActivityLimits(
	retention time.Duration,
	maxCommits int,
) {
	if retention <= 0 {
		retention = defaultBranchActivityRetention
	}
	if maxCommits <= 0 {
		maxCommits = defaultBranchActivityMaxCommits
	}
	s.branchActivityMu.Lock()
	s.branchActivityRetention = retention
	s.branchActivityMaxCommits = maxCommits
	s.branchActivityMu.Unlock()
}

func (s *Syncer) branchActivityLimits() (time.Duration, int) {
	s.branchActivityMu.RLock()
	retention := s.branchActivityRetention
	maxCommits := s.branchActivityMaxCommits
	s.branchActivityMu.RUnlock()
	if retention <= 0 {
		retention = defaultBranchActivityRetention
	}
	if maxCommits <= 0 {
		maxCommits = defaultBranchActivityMaxCommits
	}
	return retention, maxCommits
}

// BranchActivityLimits reports the configured default-branch activity
// retention and per-branch commit cap.
func (s *Syncer) BranchActivityLimits() (time.Duration, int) {
	return s.branchActivityLimits()
}

// SetOnStatusChange registers a callback invoked whenever the
// sync status transitions (start, per-repo progress, rate-limit
// wait, completion). Used by the server to broadcast live sync
// state over SSE.
func (s *Syncer) SetOnStatusChange(fn func(status *SyncStatus)) {
	s.onStatusChange = fn
}

// SetOnNotificationSyncComplete registers a callback invoked after each
// notification sync run finishes. Register it before Start so it is never
// assigned concurrently with a running sidecar sync.
func (s *Syncer) SetOnNotificationSyncComplete(fn func()) {
	s.onNotificationSyncComplete = fn
}

// SetFetchers registers fallback GitHub GraphQL fetchers keyed by platform host.
func (s *Syncer) SetFetchers(fetchers map[string]*GraphQLFetcher) {
	s.fetchers = fetchers
}

// PrefersGitHubNativeStacks reports the preference currently in force. Callers
// reconciling a past transition recheck it under the projection lock: a newer
// transition may have already landed and projected, and replaying the older
// decision would overwrite it.
func (s *Syncer) PrefersGitHubNativeStacks() bool {
	return s.preferGitHubNativeStacks.Load()
}

// SetClock replaces the syncer's time source. Cache aging bounds -- notably the
// native-stack observation window -- span hours, so callers that need to observe
// those transitions inject a clock instead of waiting. The field is read without
// synchronization on the sync path, so this must not be called while a sync is
// running.
func (s *Syncer) SetClock(now func() time.Time) {
	if now == nil {
		return
	}
	s.now = now
}

// SetGitHubRouters registers configuration-bounded credential routers keyed by
// GitHub host. Route-specific GraphQL fetchers take precedence over legacy
// host fallback fetchers.
func (s *Syncer) SetGitHubRouters(routers map[string]*HostRouter) {
	s.routers = routers
}

// fetcherFor returns the GitHub GraphQL fetcher selected for a repository's
// credential route, or the host fallback fetcher when no router is configured.
func (s *Syncer) fetcherFor(repo RepoRef) *GraphQLFetcher {
	return s.fetcherForContext(context.Background(), repo)
}

func (s *Syncer) fetcherForContext(ctx context.Context, repo RepoRef) *GraphQLFetcher {
	if repoPlatform(repo) != platform.KindGitHub {
		return nil
	}
	host := repoHost(repo)
	if router := s.routers[host]; router != nil {
		fetcher, err := router.FetcherForRepo(ctx, repo.Owner, repo.Name)
		if err == nil {
			return fetcher
		}
		return nil
	}
	return s.fetchers[host]
}
