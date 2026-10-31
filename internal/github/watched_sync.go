package github

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"time"

	"go.kenn.io/forge/internal/db"
	"go.kenn.io/forge/platform"
)

// SyncWatchedMRs runs one synchronous pass of the fast detail-refresh lane.
// Embedders normally call Start to run this lane on its configured cadence;
// this method is available when an immediate pass is required.
func (s *Syncer) SyncWatchedMRs(ctx context.Context) {
	s.syncWatchedMRs(ctx)
}

func (s *Syncer) syncWatchedMRs(ctx context.Context) {
	if !s.AutomaticSyncEnabled() {
		return
	}
	s.watchSyncMu.Lock()
	defer s.watchSyncMu.Unlock()

	ctx = WithSyncSpendKind(WithSyncBudget(ctx), SyncSpendWatchedPRRefresh)

	mrs := s.watchedMRsForFastSync(ctx, time.Now().UTC())
	if len(mrs) == 0 {
		return
	}
	slices.SortStableFunc(mrs, func(a, b WatchedMR) int {
		return s.watchedAttemptedAt[watchedMRKey(a)].Compare(s.watchedAttemptedAt[watchedMRKey(b)])
	})

	watchInt, _ := s.watchSettings()
	watchRepos := make([]RepoRef, len(mrs))
	for i, mr := range mrs {
		watchRepos[i] = RepoRef{
			Platform: watchedMRPlatform(mr), PlatformHost: watchedMRHost(mr),
			Owner: mr.Owner, Name: mr.Name,
		}
	}
	eligibleBuckets := s.repoEligibility(watchRepos, s.nextWatchSyncAfter)

	// Check backoff once per provider/host bucket to avoid redundant checks.
	blockedBuckets := make(map[string]bool)
	for i := range mrs {
		bucket, err := s.bucketKeyForRepo(watchRepos[i], false)
		if err != nil {
			continue
		}
		if _, checked := blockedBuckets[bucket]; checked {
			continue
		}
		if rt := s.rateTrackers[bucket]; rt != nil {
			if backoff, _ := rt.ShouldBackoff(); backoff {
				blockedBuckets[bucket] = true
				continue
			}
		}
		budget := s.budgets[bucket]
		// Leave room for the detail drain as well as one watched read and probe.
		blockedBuckets[bucket] = budget != nil && !budget.CanSpend(wireAttemptsPerRequest+2*detailWorstCaseAttemptCost(watchedMRPlatform(mrs[i]), QueueItemPR))
	}

	syncedAny := false
	repoResolutions := make(repoResolutionCache)
	fingerprints := s.probeMRFreshness(ctx, s.eligibleWatchedMRs(mrs, eligibleBuckets, blockedBuckets))
	probedAt := s.nowUTC()
	for _, mr := range mrs {
		host := watchedMRHost(mr)
		repo := RepoRef{
			Platform:     watchedMRPlatform(mr),
			PlatformHost: host,
			Owner:        mr.Owner,
			Name:         mr.Name,
		}
		bucket, bucketErr := s.bucketKeyForRepo(repo, false)
		if bucketErr != nil {
			slog.Warn("resolve fast-sync credential route",
				"host", host,
				"owner", mr.Owner,
				"name", mr.Name,
				"err", bucketErr,
			)
			continue
		}
		if !eligibleBuckets[bucket] {
			slog.Debug("skipping fast-sync for throttled host",
				"host", host,
				"owner", mr.Owner,
				"name", mr.Name,
				"number", mr.Number,
			)
			continue
		}
		if blockedBuckets[bucket] {
			slog.Debug("skipping fast-sync for rate-limited or paced host",
				"host", host,
				"owner", mr.Owner,
				"name", mr.Name,
				"number", mr.Number,
			)
			continue
		}
		if budget := s.budgets[bucket]; budget != nil && !budget.CanSpend(2*detailWorstCaseAttemptCost(repo.Platform, QueueItemPR)) {
			blockedBuckets[bucket] = true
			slog.Debug("skipping fast-sync for paced host", "host", host, "number", mr.Number)
			continue
		}
		key := watchedMRKey(mr)
		if s.watchedAttemptedAt == nil {
			s.watchedAttemptedAt = make(map[string]time.Time)
		}
		s.watchedAttemptedAt[key] = s.nowUTC()
		probe, due := s.beginRepositoryFeatureProbe(
			ctx, repo, platform.RepositoryFeatureMergeRequests,
		)
		if !due {
			slog.Debug("skipping fast-sync for disabled repository feature",
				"platform", repoPlatform(repo),
				"host", repoHost(repo),
				"repo", platformRepoRef(repo).RepoPath,
				"feature", platform.RepositoryFeatureMergeRequests,
			)
			continue
		}
		fingerprint := fingerprints[key].Fingerprint
		probed := fingerprint != ""
		if probed && s.prFreshness.matches(key, fingerprint, probedAt) {
			fresh, err := s.markMRFreshFromProbe(ctx, mr, repoResolutions)
			if err == nil && fresh {
				probe.abandon()
				syncedAny = true
				continue
			}
		}
		providerAttempted := false
		cachedResolution := repoResolutions[detailRepoKey(watchedMRPlatform(mr), host, mr.Owner, mr.Name)]
		syncCtx, degraded := withDegradedSyncTracking(ctx)
		readStarted := time.Now()
		err := s.syncMRWithWatchedRefTracking(syncCtx, mr, &providerAttempted, repoResolutions)
		// A read that kept stored data for a dropped child refresh, or that
		// a concurrent sync superseded, cannot vouch for the probe before it.
		switch {
		case err != nil:
			s.prFreshness.forget(key)
		case degraded.Load() || !s.detailReadSince(ctx, mr, readStarted):
			s.prFreshness.recordIncomplete(key, probedAt)
		case probed:
			s.prFreshness.record(key, fingerprint, probedAt)
		default:
			s.prFreshness.forget(key)
		}
		if err != nil {
			disabledErr := repositoryFeatureDisabledError(
				repo, platform.RepositoryFeatureMergeRequests, err,
			)
			disabled := disabledErr != nil && s.recordRepositoryFeatureDisabled(
				repo, platform.RepositoryFeatureMergeRequests, disabledErr,
			)
			if providerAttempted {
				probe.release()
			} else {
				probe.abandon()
			}
			if disabled {
				continue
			}
			if cachedResolution.err != nil && errors.Is(err, cachedResolution.err) {
				slog.Debug("skipping watched MR after repository lookup failure",
					"owner", mr.Owner,
					"name", mr.Name,
					"number", mr.Number,
				)
			} else {
				slog.Warn("fast-sync watched MR failed",
					"owner", mr.Owner,
					"name", mr.Name,
					"number", mr.Number,
					"err", err,
				)
			}
			if _, ok := errors.AsType[*DiffSyncError](err); ok {
				syncedAny = true
			}
			continue
		}
		if providerAttempted {
			probe.release()
		} else {
			probe.abandon()
		}
		syncedAny = true
	}

	s.advanceNextSync(
		eligibleBuckets, s.nextWatchSyncAfter, watchInt,
	)
	if syncedAny && s.onWatchedMRSyncCompleted != nil {
		s.onWatchedMRSyncCompleted()
	}
}

func (s *Syncer) watchedMRsForFastSync(ctx context.Context, now time.Time) []WatchedMR {
	s.watchMu.Lock()
	mrs := slices.Clone(s.watchedMRs)
	watchInt, activeWindow := s.watchSettingsLocked()
	s.watchMu.Unlock()

	watched := newWatchedMRSet(mrs)
	for _, mr := range s.hotAndWarmOpenMRs(ctx, now, activeWindow, watchInt) {
		watched.add(mr)
	}
	items := watched.slice()
	archived := s.archivedRepoKeys()
	if len(archived) == 0 {
		return items
	}
	live := make([]WatchedMR, 0, len(items))
	for _, mr := range items {
		key := detailRepoKey(
			watchedMRPlatform(mr), watchedMRHost(mr), mr.Owner, mr.Name,
		)
		if _, ok := archived[key]; ok {
			continue
		}
		live = append(live, mr)
	}
	return live
}

// archivedRepoKeys returns detailRepoKey identities for tracked archived
// repositories so live lanes (fast sync, notifications) can skip them.
func (s *Syncer) archivedRepoKeys() map[string]struct{} {
	s.reposMu.Lock()
	defer s.reposMu.Unlock()
	keys := make(map[string]struct{})
	for _, repo := range s.repos {
		if repo.Archived {
			keys[detailRepoKey(
				repoPlatform(repo), repo.PlatformHost, repo.Owner, repo.Name,
			)] = struct{}{}
		}
	}
	return keys
}

func (s *Syncer) watchSettingsLocked() (time.Duration, time.Duration) {
	watchInt := s.watchInterval
	if watchInt <= 0 {
		watchInt = 30 * time.Second
	}
	return watchInt, s.activeMRWindow
}

func (s *Syncer) hotAndWarmOpenMRs(
	ctx context.Context,
	now time.Time,
	window time.Duration,
	hotInterval time.Duration,
) []WatchedMR {
	s.watchMu.Lock()
	hotWindow, warmInterval := s.activeMRHotWindow, s.activeMRWarmInterval
	s.watchMu.Unlock()
	if warmInterval <= 0 {
		warmInterval = activeMRWarmRefreshInterval
	}
	prs, err := s.db.ListMergeRequests(ctx, db.ListMergeRequestsOpts{State: "open"})
	if err != nil {
		slog.Warn("fast-sync active MR selection failed", "err", err)
		return nil
	}
	hotIDs, err := s.db.ListHotMergeRequestIDs(ctx, db.HotMergeRequestLimit)
	if err != nil {
		slog.Warn("fast-sync hot MR selection failed", "err", err)
	}
	hot := make(map[int64]struct{}, len(hotIDs))
	for _, id := range hotIDs {
		hot[id] = struct{}{}
	}

	notificationActivity := make(map[int64]time.Time)
	cutoff := now.Add(-window)
	if window > 0 {
		hints, hintErr := s.db.LatestOpenPRNotificationActivity(ctx, cutoff)
		if hintErr != nil {
			slog.Warn("fast-sync active MR notification activity failed", "err", hintErr)
		} else {
			for _, hint := range hints {
				notificationActivity[hint.MergeRequestID] = hint.SourceUpdatedAt
			}
		}
	}
	repoByID := make(map[int64]*db.Repo)
	var watched []WatchedMR
	for _, pr := range prs {
		_, isHot := hot[pr.ID]
		refreshInterval := hotInterval
		if !isHot {
			if window <= 0 {
				continue
			}
			effectiveActivityAt := pr.LastActivityAt
			if hint := notificationActivity[pr.ID]; hint.After(effectiveActivityAt) {
				effectiveActivityAt = hint
			}
			if effectiveActivityAt.Before(cutoff) {
				continue
			}
			refreshInterval = warmInterval
			if hotWindow > 0 && !effectiveActivityAt.Before(now.Add(-hotWindow)) {
				refreshInterval = hotInterval
			}
		}
		if !mergeRequestDetailDue(pr, now, refreshInterval) {
			continue
		}
		repo, ok := repoByID[pr.RepoID]
		if !ok {
			var repoErr error
			repo, repoErr = s.db.GetRepoByID(ctx, pr.RepoID)
			if repoErr != nil || repo == nil {
				if repoErr != nil {
					slog.Warn("fast-sync active MR repo lookup failed",
						"repo_id", pr.RepoID,
						"err", repoErr,
					)
				}
				continue
			}
			repoByID[pr.RepoID] = repo
		}
		if _, ok := s.trackedRepoByIdentity(
			platform.Kind(repo.Platform), repo.Owner, repo.Name, repo.PlatformHost,
		); !ok {
			continue
		}
		watched = append(watched, WatchedMR{
			Platform:     platform.Kind(repo.Platform),
			PlatformHost: repo.PlatformHost,
			Owner:        repo.Owner,
			Name:         repo.Name,
			Number:       pr.Number,
		})
	}
	return watched
}

func mergeRequestDetailDue(pr db.MergeRequest, now time.Time, interval time.Duration) bool {
	if pr.DetailFetchedAt == nil {
		return true
	}
	if interval <= 0 {
		return true
	}
	return !pr.DetailFetchedAt.Add(interval).After(now)
}

func watchedMRPlatform(mr WatchedMR) platform.Kind {
	if mr.Platform != "" {
		return mr.Platform
	}
	return platform.KindGitHub
}

func watchedMRHost(mr WatchedMR) string {
	return repoHost(RepoRef{
		Platform:     watchedMRPlatform(mr),
		PlatformHost: mr.PlatformHost,
	})
}

func watchedMRKey(mr WatchedMR) string {
	return detailRepoKey(
		watchedMRPlatform(mr), watchedMRHost(mr), mr.Owner, mr.Name,
	) + fmt.Sprintf("#%d", mr.Number)
}

type watchedMRSet struct {
	seen  map[string]struct{}
	items []WatchedMR
}

func newWatchedMRSet(initial []WatchedMR) *watchedMRSet {
	w := &watchedMRSet{seen: make(map[string]struct{}, len(initial))}
	for _, mr := range initial {
		w.add(mr)
	}
	return w
}

func (w *watchedMRSet) add(mr WatchedMR) {
	key := watchedMRKey(mr)
	if _, ok := w.seen[key]; ok {
		return
	}
	w.seen[key] = struct{}{}
	w.items = append(w.items, mr)
}

func (w *watchedMRSet) slice() []WatchedMR {
	return slices.Clone(w.items)
}

// eligibleWatchedMRs returns the watched pull requests whose credential
// bucket this pass may spend and whose repository is not in a disabled
// pull request cooldown.
func (s *Syncer) eligibleWatchedMRs(mrs []WatchedMR, eligible, blocked map[string]bool) []WatchedMR {
	out := make([]WatchedMR, 0, len(mrs))
	now := s.nowUTC()
	for _, mr := range mrs {
		repo := RepoRef{
			Platform: watchedMRPlatform(mr), PlatformHost: watchedMRHost(mr),
			Owner: mr.Owner, Name: mr.Name,
		}
		bucket, err := s.bucketKeyForRepo(repo, false)
		if err != nil || !eligible[bucket] || blocked[bucket] ||
			s.featureCooldowns.inCooldown(repo, platform.RepositoryFeatureMergeRequests, now) {
			continue
		}
		out = append(out, mr)
	}
	return out
}
