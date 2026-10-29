package github

import (
	"container/list"
	"context"
	"errors"
	"log/slog"
	"slices"
	"sync"
	"sync/atomic"
	"time"

	"go.kenn.io/forge/internal/platformdb"

	"go.kenn.io/forge/internal/archive"
	"go.kenn.io/forge/internal/db"
	"go.kenn.io/forge/internal/gitclone"
	"go.kenn.io/forge/internal/shutdownbudget"
	"go.kenn.io/forge/platform"
	"golang.org/x/sync/singleflight"
)

// defaultParallelism is the worker pool size used by RunOnce when
// SetParallelism has not been called. Bounded so we don't burst the
// per-host GitHub rate limit / abuse-detection thresholds.
const (
	defaultParallelism               = 4
	rateLimitSnapshotRefreshInterval = 3 * time.Minute
	activeMRWarmRefreshInterval      = 10 * time.Minute
)

type pendingSyncRun struct {
	bypassNextSyncAfter bool
	full                bool
	priorityRepos       []RepoRef
	onlyRepos           []RepoRef
	bypassRepos         []RepoRef
	startupRetryRepos   []RepoRef
}

// Syncer periodically pulls PR data from GitHub into SQLite.
type Syncer struct {
	clients                  *platform.Registry
	directClients            *platform.Registry
	db                       *db.DB
	archiveRunner            archiveRunner
	archiveLifecycle         archiveRepositoryLifecycle
	archiveWake              chan struct{}
	archivePollInterval      time.Duration
	now                      func() time.Time
	clones                   *gitclone.Manager
	livenessMemoMu           sync.Mutex
	livenessMemos            map[int64]*list.Element // memoized verified reachability (LRU); see computeCommitLiveness
	livenessMemoLRU          *list.List              // recency order for livenessMemos; front = most recent
	livenessMemoLimit        int                     // max memo entries; 0 = defaultLivenessMemoLimit
	livenessCandidateLimit   int                     // max memoizable candidate SHAs; 0 = defaultLivenessCandidateLimit
	rateTrackers             map[string]*RateTracker // provider/host bucket -> tracker
	writeRateTrackers        map[string]*RateTracker // provider/host bucket -> mutation-credential REST tracker
	writeGQLRateTrackers     map[string]*RateTracker // provider/host bucket -> mutation-credential GraphQL tracker
	budgets                  map[string]*SyncBudget  // provider/host bucket -> budget
	quotaRegistry            *QuotaRegistry          // GitHub principal -> live provider quota
	reserveMu                sync.RWMutex
	reserves                 map[string]reserveVerdict  // bucket+resource -> cadence-cached reserve decision
	fetchers                 map[string]*GraphQLFetcher // host -> fallback GraphQL fetcher
	routers                  map[string]*HostRouter     // GitHub host -> credential router
	ratePrincipalLabels      map[string]string
	rateLimitSnapshotMu      sync.Mutex
	rateLimitSnapshotRefresh map[string]time.Time
	repos                    []RepoRef
	reposMu                  sync.Mutex
	mergedActorCursorMu      sync.Mutex
	mergedActorCursors       map[int64]mergedActorSweepState
	interval                 time.Duration
	watchInterval            time.Duration
	watchedMRs               []WatchedMR
	activeMRWindow           time.Duration
	activeMRHotWindow        time.Duration
	activeMRWarmInterval     time.Duration
	watchMu                  sync.Mutex
	watchSyncMu              sync.Mutex
	branchActivityMu         sync.RWMutex
	branchActivityRetention  time.Duration
	branchActivityMaxCommits int
	preferGitHubNativeStacks atomic.Bool
	syncDisabled             atomic.Bool
	airplaneMode             atomic.Bool
	parallelism              atomic.Int32
	runMu                    sync.Mutex
	running                  atomic.Bool
	exclusiveRun             bool                 // guarded by runMu
	pendingRun               *pendingSyncRun      // guarded by runMu
	startupRepoSyncTimes     map[string]time.Time // one-shot credential completions, guarded by runMu
	providerWorkMu           sync.Mutex
	providerWork             map[string]map[archive.WorkPriority]int
	// archiveDeniedHosts records provider hosts whose live work denied or
	// preempted an archive request; releasing such a host wakes the archive
	// worker, while hosts that never turned archive work away stay quiet.
	archiveDeniedHosts      map[string]struct{}
	archiveProviderRequests map[string]archiveProviderRequest
	status                  atomic.Value // stores *SyncStatus
	stopCh                  chan struct{}
	notificationSyncMu      sync.RWMutex
	notificationSync        NotificationSyncStatus
	// notificationValidators holds the Last-Modified validator of the last
	// complete notification pass per user identity; guarded by
	// notificationSyncMu.
	notificationValidators map[string]string
	stopOnce               sync.Once
	wg                     sync.WaitGroup
	// lifecycleMu serializes TriggerRun registration with Stop so
	// no wg.Add can happen after Stop begins wg.Wait.
	lifecycleMu        sync.Mutex
	stopped            bool                 // guarded by lifecycleMu
	nextSyncAfter      map[string]time.Time // provider/host bucket -> next eligible background sync time
	nextWatchSyncAfter map[string]time.Time // provider/host bucket -> next eligible watch-sync time
	// displayNames is a bounded TTL + LRU cache for resolved
	// GitHub display names. It spans the Syncer's lifetime so
	// cache hits survive across sync runs; per-entry TTL
	// handles profile-name changes without an explicit flush.
	displayNames     *displayNameCache
	displayNameGroup singleflight.Group // dedups concurrent GetUser calls
	onMRSynced       func(owner, name string, mr *db.MergeRequest)
	onRelayRefresh   func(context.Context, int64, string, int)
	onSyncCompleted  func(results []RepoSyncResult)
	onSyncFailed     func(source, reason string)
	syncFailures     map[syncFailureKey]bool // guarded by the run slot
	// onWatchedMRSyncCompleted fires once after a watched-MR fast-sync
	// pass refreshes at least one MR.
	onWatchedMRSyncCompleted func()
	// onMergedActorRepaired fires after scheduled reconciliation persists an
	// authored merge event so consumers can refresh the affected detail.
	onMergedActorRepaired func(context.Context, int64, int)
	// onMergeQueueChanged fires for each pull request whose merge queue
	// membership, position, or state changed.
	onMergeQueueChanged func(context.Context, int64, int)
	// onMergeBypassChanged fires for each pull request whose recorded merge
	// bypass permission changed.
	onMergeBypassChanged func(context.Context, int64, int)
	// onMergeRequestTerminal fires after an accepted snapshot records a merge
	// request merged or closed.
	onMergeRequestTerminal func(context.Context, int64, int)
	// mergeQueueMethods caches, per repository and branch, the merge queue's
	// merge method and when it was read.
	mergeQueueMethods sync.Map
	// mergeQueueBranchAbsent records, per repository and branch, when a read
	// last found that branch without a merge queue.
	mergeQueueBranchAbsent sync.Map
	// mergeQueueReadDenied records, per repository, when the read
	// credential was refused the merge queue, so reads use the write
	// credential until mergeQueueReadRecheck passes.
	mergeQueueReadDenied sync.Map
	// mergeQueueBulkChecked records, per repository and pull request, when
	// bulk hydration last tried to read its queue state, so a read that
	// keeps failing waits out mergeQueueBulkRecheck like a successful one.
	mergeQueueBulkChecked sync.Map
	onStatusChange        func(status *SyncStatus)
	// onNotificationSyncComplete fires after each notification sync run
	// (periodic, manual, or sidecar) so the server can broadcast the same
	// data-change signal the normal sync uses. Notification sync can insert
	// rows with older timestamps than the feed's top cursor, which the
	// feed's incremental poll would miss without a full reload nudge.
	onNotificationSyncComplete func()
	// statusMu serializes status publication and terminal run-slot release so
	// worker goroutines and later runs cannot deliver out-of-order snapshots.
	// When both locks are needed, statusMu must be acquired before runMu.
	statusMu sync.Mutex

	gitAccessMu sync.Mutex
	gitAccess   map[string]GitAccessProblem

	repoSyncMu sync.Mutex
	repoSync   map[string]RepoSyncProblem

	// failedRepos tracks repos whose last sync had a partial failure
	// (a per-PR, per-issue, or closure-detection step failed after
	// the ETag cache was populated by a successful 200 list fetch).
	// Values are failScope bitmasks indicating which path(s) failed.
	// The next sync cycle consults this set at the top of doSyncRepo
	// and forces an unconditional refetch of the list endpoints so
	// the failed items get re-applied from a fresh 200 response
	// instead of being skipped by a silent 304. Keyed by
	// "host/owner/name". Cleared on the next successful sync.
	failedRepos sync.Map
	// nativeStackResults transfers per-repository preview confirmation from the
	// index sync to the ordered completion hook without persisting run state.
	nativeStackResults sync.Map // map[string]*GitHubNativeStackSyncResult
	// nativeStackConfirmations pairs the in-memory PR-list ETag lifecycle with
	// the last native stack set confirmed from that exact representation.
	nativeStackConfirmations sync.Map // map[string][]int
	// nativeStackGeneration advances on every native-stack preference change.
	// A sync that captured an older generation must not project its native
	// result, or an in-flight run could reinstate native ordering after the
	// user turned the preview off.
	nativeStackGeneration atomic.Uint64
	// stackProjectionMu serializes stack projection between the sync
	// completion hook and preference-change reconciliation so the last write
	// always reflects the current preference.
	stackProjectionMu sync.Mutex

	featureCooldowns repositoryFeatureCooldowns

	// runCtx is the syncer's lifetime context. It is canceled in
	// Stop so in-flight RunOnce / TriggerRun goroutines observe
	// cancellation and unblock any long-running GitHub calls. Both
	// Start and TriggerRun derive their goroutine context from
	// runCtx (merged with any caller context), so Stop can unblock
	// the work it spawned regardless of whether the caller's ctx
	// is still live. runCtxMu guards lazy init and the Stop
	// handoff.
	runCtx    context.Context
	runCancel context.CancelFunc
	runCtxMu  sync.Mutex

	commentRefreshMu         sync.Mutex
	pendingPRCommentSyncs    []queuedPRCommentSync
	pendingIssueCommentSyncs []queuedIssueCommentSync

	afterMergeRequestParentSnapshotCommit   func()
	afterMergedMRMetricsRepair              func()
	afterHeadRepoSnapshotRead               func()
	afterNotificationRepoIdentityReconciled func()
}

// DisableSync permanently prevents this Syncer from starting provider refresh
// work. It must be called during startup, before the Syncer is exposed.
func (s *Syncer) DisableSync() {
	if s != nil {
		s.syncDisabled.Store(true)
	}
}

func (s *Syncer) SyncEnabled() bool {
	return s != nil && !s.syncDisabled.Load()
}

// SetAirplaneMode pauses scheduled sync after the current pass. Explicit
// refreshes, relay hints, and notification read propagation remain available.
func (s *Syncer) SetAirplaneMode(enabled bool) {
	s.airplaneMode.Store(enabled)
}

func (s *Syncer) AutomaticSyncEnabled() bool {
	return s.SyncEnabled() && !s.airplaneMode.Load()
}

func (s *Syncer) syncDisabledError() error {
	if !s.SyncEnabled() {
		return platform.ErrSyncDisabled
	}
	return nil
}

// ensureRunCtx lazily initializes runCtx/runCancel. Safe to call
// multiple times; the first caller wins and later calls are no-ops.
func (s *Syncer) ensureRunCtx() context.Context {
	s.runCtxMu.Lock()
	defer s.runCtxMu.Unlock()
	if s.runCtx == nil {
		s.runCtx, s.runCancel = context.WithCancel(context.Background())
	}
	return s.runCtx
}

// mergeWithRunCtx returns a context that is canceled when either the
// caller's ctx or the syncer's lifetime ctx is canceled. The returned
// cancel function must be called to release resources. Used by
// TriggerRun so ad-hoc runs respect both the caller's deadline and
// Stop's global cancellation signal.
func (s *Syncer) mergeWithRunCtx(caller context.Context) (context.Context, context.CancelFunc) {
	runCtx := s.ensureRunCtx()
	merged, cancel := context.WithCancel(caller)
	go func() {
		select {
		case <-runCtx.Done():
			cancel()
		case <-merged.Done():
		}
	}()
	return merged, cancel
}

// publishStatus stores a status snapshot and invokes the
// onStatusChange callback if one is registered. Used in place of
// s.status.Store so SSE subscribers see every state transition.
func (s *Syncer) publishStatus(status *SyncStatus) {
	s.statusMu.Lock()
	defer s.statusMu.Unlock()
	s.publishStatusLocked(status)
}

func (s *Syncer) publishStatusLocked(status *SyncStatus) {
	if status.Running {
		status.DetailRefreshOverdue = s.Status().DetailRefreshOverdue
	}
	if status.Relay == nil {
		status.Relay = s.Status().Relay
	}
	status.GitAccess = s.gitAccessProblems()
	status.RepoErrors = s.repoSyncProblems()
	s.status.Store(status)
	if s.onStatusChange != nil {
		s.onStatusChange(status)
	}
}

// NewSyncer creates a Syncer that polls the given repos on the
// given interval. clients maps host -> Client; rateTrackers maps
// host -> RateTracker. Both may contain nil values. clones may
// be nil. budgets maps host -> SyncBudget; nil or empty disables
// detail drain and archive collection. Budgets are created by the caller
// (typically main.go) and wired into each Client's HTTP transport
// at construction time so every sync-context RoundTrip is
// automatically counted.
func NewSyncer(
	clients map[string]Client,
	database *db.DB,
	clones *gitclone.Manager,
	repos []RepoRef,
	interval time.Duration,
	rateTrackers map[string]*RateTracker,
	budgets map[string]*SyncBudget,
) *Syncer {
	return NewSyncerWithRegistry(
		registryFromGitHubClients(clients),
		database,
		clones,
		repos,
		interval,
		rateTrackers,
		budgets,
	)
}

func NewSyncerWithRegistry(
	registry *platform.Registry,
	database *db.DB,
	clones *gitclone.Manager,
	repos []RepoRef,
	interval time.Duration,
	rateTrackers map[string]*RateTracker,
	budgets map[string]*SyncBudget,
) *Syncer {
	if registry == nil {
		registry, _ = platform.NewRegistry()
	}
	if rateTrackers == nil {
		rateTrackers = make(map[string]*RateTracker)
	}
	if budgets == nil {
		budgets = make(map[string]*SyncBudget)
	}

	s := &Syncer{
		db:                       database,
		clones:                   clones,
		rateTrackers:             rateTrackers,
		budgets:                  budgets,
		repos:                    slices.Clone(repos),
		interval:                 interval,
		rateLimitSnapshotRefresh: make(map[string]time.Time),
		branchActivityRetention:  defaultBranchActivityRetention,
		branchActivityMaxCommits: defaultBranchActivityMaxCommits,
		nextSyncAfter:            make(map[string]time.Time),
		nextWatchSyncAfter:       make(map[string]time.Time),
		archiveProviderRequests:  make(map[string]archiveProviderRequest),
		stopCh:                   make(chan struct{}),
		archiveWake:              make(chan struct{}, 1),
		archivePollInterval:      time.Second,
		now:                      time.Now,
		displayNames: newDisplayNameCache(
			displayNameCacheSize,
			displayNameSuccessTTL,
			displayNameFailureTTL,
		),
	}
	s.directClients = registry
	s.clients = registry.WithProviderGate(s.syncDisabledError)
	s.parallelism.Store(defaultParallelism)
	s.status.Store(&SyncStatus{})

	// Wire budget reset to rate tracker window resets.
	for h, rt := range rateTrackers {
		if b, ok := budgets[h]; ok && rt != nil {
			rt.SetOnWindowReset(b.Reset)
		}
	}

	return s
}

// ErrRepoNotTracked marks sync requests for repositories outside the
// configured tracked set so handlers can map them to 403 without matching text.
var ErrRepoNotTracked = errors.New("not tracked")

const authenticatedViewerLoginTTL = time.Hour

// Start runs an immediate sync then launches a background ticker.
// It returns as soon as the goroutine is started; call Stop to shut it down.
// A second goroutine runs watched-MR fast-syncs on a shorter interval.
//
// The caller's ctx and the syncer's internal lifetime ctx (canceled
// by Stop) are both honored: either one unblocks any in-flight work.
func (s *Syncer) Start(ctx context.Context) {
	if !s.SyncEnabled() {
		return
	}
	s.lifecycleMu.Lock()
	if s.stopped {
		s.lifecycleMu.Unlock()
		return
	}

	startMerged, startCancel := s.mergeWithRunCtx(ctx)
	s.wg.Add(1)

	watchMerged, watchCancel := s.mergeWithRunCtx(ctx)
	s.wg.Add(1)
	var archiveReady chan struct{}
	if s.archiveRunner != nil {
		archiveReady = make(chan struct{})
		s.wg.Add(1)
	}
	s.lifecycleMu.Unlock()

	go func() {
		defer s.wg.Done()
		defer startCancel()
		oldest, retry := s.liveRepoSyncTimes(startMerged)
		s.runMu.Lock()
		s.startupRepoSyncTimes = oldest
		s.runMu.Unlock()
		if s.AutomaticSyncEnabled() {
			s.runOnceWithSlot(startMerged, false, nil, nil, retry, false)
		}
		if archiveReady != nil {
			close(archiveReady)
			s.WakeArchive()
		}
		ticker := time.NewTicker(s.interval)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				s.RunOnce(startMerged)
			case <-s.stopCh:
				return
			case <-startMerged.Done():
				return
			}
		}
	}()

	if archiveReady != nil {
		go func() {
			defer s.wg.Done()
			s.runArchiveLoop(startMerged, archiveReady)
		}()
	}

	go func() {
		defer s.wg.Done()
		defer watchCancel()
		for {
			watchInt, _ := s.watchSettings()
			timer := time.NewTimer(watchInt)
			select {
			case <-timer.C:
				s.syncWatchedMRs(watchMerged)
			case <-s.stopCh:
				timer.Stop()
				return
			case <-watchMerged.Done():
				timer.Stop()
				return
			}
		}
	}()
}

// liveRepoSyncTimes reads the oldest good completion per credential for tracked
// live repositories, and collects failed and never-synced repositories.
func (s *Syncer) liveRepoSyncTimes(ctx context.Context) (map[string]time.Time, []RepoRef) {
	if s.db == nil {
		return nil, nil
	}
	oldest := make(map[string]time.Time)
	var retry []RepoRef
	now := time.Now().UTC()
	for _, repo := range s.TrackedRepos() {
		if repo.Archived {
			continue
		}
		stored, err := s.db.GetRepoByIdentity(ctx, platformdb.DBRepoIdentity(platformRepoRef(repo)))
		if err != nil {
			slog.Warn("get repository sync time", "repo", repo, "err", err)
			continue
		}
		var last time.Time
		if stored != nil && stored.LastSyncCompletedAt != nil {
			last = *stored.LastSyncCompletedAt
		}
		if last.IsZero() || stored != nil && stored.LastSyncError != "" {
			retry = append(retry, repo)
			continue
		}
		if last.After(now) {
			continue
		}
		key, err := s.bucketKeyForRepo(repo, false)
		if err != nil || s.rateTrackers[key] == nil {
			continue
		}
		if previous, ok := oldest[key]; !ok || last.Before(previous) {
			oldest[key] = last
		}
	}
	return oldest, retry
}

// Stop signals the background goroutine to exit. Safe to call
// multiple times. Cancels the syncer's lifetime context first so
// blocked RunOnce and TriggerRun goroutines can observe the
// cancellation and unwind their GitHub calls, then waits through the
// shared syncer shutdown budget. A dependency that ignores ctx cannot
// deadlock process shutdown.
func (s *Syncer) Stop() {
	s.stopOnce.Do(func() {
		s.lifecycleMu.Lock()
		s.stopped = true
		s.lifecycleMu.Unlock()

		close(s.stopCh)
		s.runCtxMu.Lock()
		cancel := s.runCancel
		s.runCtxMu.Unlock()
		if cancel != nil {
			cancel()
		}
	})

	done := make(chan struct{})
	go func() {
		s.wg.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(shutdownbudget.Syncer):
		slog.Warn("syncer stop timed out; returning while work is still in flight",
			"grace", shutdownbudget.Syncer)
	}
}

// Status returns a snapshot of the current sync state.
func (s *Syncer) Status() *SyncStatus {
	return s.status.Load().(*SyncStatus)
}
