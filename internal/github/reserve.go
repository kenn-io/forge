package github

import (
	"context"
	"time"

	"go.kenn.io/forge/platform"
)

// backgroundReserveCost is the request allowance every background reserve check
// asks for. One constant rather than a per-caller cost: against a 200-request
// buffer the difference between one request and a queue item's worst case is
// noise, and the local hourly ceiling still accounts for each caller's real
// spend atomically per wire attempt.
const backgroundReserveCost = 3

// reserveVerdict is one credential bucket's cached reserve decision for one
// resource and mode. known distinguishes "this credential has capacity" from
// "nothing has ever been observed", which callers with a host-wide fallback
// must tell apart.
type reserveVerdict struct {
	exhausted bool
	known     bool
	resetAt   *time.Time
	at        time.Time
}

// backgroundReserveExhausted reports whether background work on this
// repository's routed credential must stop.
//
// This is the only background reserve check in the syncer. It is evaluated at
// most once per credential per resource per snapshot cadence and cached, so
// repository admission, workers, the detail and comment drains, bulk GraphQL,
// and notification acknowledgements all answer from one decision rather than
// each re-deriving it per repository, per queue item, and per page. The
// provider quota it reads only changes when the three-minute snapshot refresh
// updates it, so evaluating it more often than that mostly re-reads the same
// numbers.
//
// The consequence is deliberate: a credential that crosses its reserve inside a
// cadence window keeps spending until the window turns. The reserve is a soft
// buffer held for foreground work, not a lease; the hard guard is the local
// hourly ceiling, enforced per wire attempt in budgetTransport.
func (s *Syncer) backgroundReserveExhausted(
	repo RepoRef, resource QuotaResource, writeIdentity bool,
) bool {
	return s.reserveVerdictFor(repo, resource, writeIdentity, true).exhausted
}

// reserveVerdictFor returns the cached decision for one credential, resource,
// and mode, recomputing it at most once per snapshot cadence. Background mode
// holds the foreground reserve; foreground mode stops only on a genuinely
// empty pool, because explicit user work is what the reserve is held for.
func (s *Syncer) reserveVerdictFor(
	repo RepoRef, resource QuotaResource, writeIdentity, background bool,
) reserveVerdict {
	return s.reserveVerdictForContext(
		context.Background(), repo, resource, writeIdentity, background,
	)
}

func (s *Syncer) reserveVerdictForContext(
	ctx context.Context,
	repo RepoRef,
	resource QuotaResource,
	writeIdentity, background bool,
) reserveVerdict {
	if repoPlatform(repo) != platform.KindGitHub || s.quotaRegistry == nil {
		return reserveVerdict{}
	}
	bucket, err := s.bucketKeyForRepoContext(ctx, repo, writeIdentity)
	if err != nil {
		return reserveVerdict{}
	}
	key := reserveCacheKey(bucket, resource, background)
	now := s.nowUTC()
	s.reserveMu.RLock()
	cached, ok := s.reserves[key]
	s.reserveMu.RUnlock()
	if ok && now.Sub(cached.at) < rateLimitSnapshotRefreshInterval {
		return cached
	}
	availability := s.evaluateBackgroundReserve(
		ctx, repo, resource, writeIdentity, background,
	)
	verdict := reserveVerdict{
		exhausted: availability.Exhausted,
		known:     availability.Known,
		resetAt:   availability.ResetAt,
		at:        now,
	}
	s.reserveMu.Lock()
	if s.reserves == nil {
		s.reserves = make(map[string]reserveVerdict)
	}
	s.reserves[key] = verdict
	s.reserveMu.Unlock()
	return verdict
}

func reserveCacheKey(
	bucket string, resource QuotaResource, background bool,
) string {
	mode := "foreground"
	if background {
		mode = "background"
	}
	return bucket + "\x00" + string(resource) + "\x00" + mode
}

// invalidateReserveVerdicts drops cached decisions for a credential bucket so
// the next check re-reads the snapshot that just landed.
func (s *Syncer) invalidateReserveVerdicts(bucket string) {
	s.reserveMu.Lock()
	defer s.reserveMu.Unlock()
	for _, resource := range []QuotaResource{
		QuotaResourceREST, QuotaResourceGraphQL,
	} {
		for _, background := range []bool{true, false} {
			delete(s.reserves, reserveCacheKey(bucket, resource, background))
		}
	}
}

func (s *Syncer) nowUTC() time.Time {
	if s.now != nil {
		return s.now().UTC()
	}
	return time.Now().UTC()
}

// evaluateBackgroundReserve is the single reserve computation the cache wraps.
func (s *Syncer) evaluateBackgroundReserve(
	ctx context.Context,
	repo RepoRef, resource QuotaResource, writeIdentity, background bool,
) QuotaAvailability {
	identity, err := s.identityForRepoContext(ctx, repo, writeIdentity)
	if err != nil {
		return QuotaAvailability{Allowed: true}
	}
	reserve := 0
	if background {
		reserve = RateReserveBuffer
	}
	availability := s.quotaRegistry.CheckReserve(
		identity, []QuotaResource{resource}, backgroundReserveCost, reserve,
	)
	if availability.Known {
		return availability
	}
	return s.persistedReserve(ctx, repo, resource, writeIdentity, reserve, availability)
}

// persistedReserve answers from the SQLite-backed rate tracker when the
// registry has never observed this credential. The registry lives only in
// memory, so a restart starts it empty even though the tracker rehydrates the
// credential's last observed pool -- and the /rate_limit refresh that would
// repopulate the registry is exactly what fails when a credential is in
// trouble.
func (s *Syncer) persistedReserve(
	ctx context.Context,
	repo RepoRef,
	resource QuotaResource,
	writeIdentity bool,
	reserve int,
	unobserved QuotaAvailability,
) QuotaAvailability {
	bucket, err := s.bucketKeyForRepoContext(ctx, repo, writeIdentity)
	if err != nil {
		return unobserved
	}
	tracker := s.reserveTracker(ctx, repo, bucket, resource, writeIdentity)
	if tracker == nil || !tracker.Known() {
		return unobserved
	}
	// An elapsed window describes a quota that has since reset, so it cannot
	// speak for the current one.
	resetAt := tracker.ResetAt()
	if resetAt == nil || !s.nowUTC().Before(*resetAt) {
		return unobserved
	}
	if tracker.Remaining()-backgroundReserveCost < reserve {
		return QuotaAvailability{Known: true, Exhausted: true, ResetAt: resetAt}
	}
	return QuotaAvailability{Allowed: true, Known: true, ResetAt: resetAt}
}

func (s *Syncer) reserveTracker(
	ctx context.Context,
	repo RepoRef, bucket string, resource QuotaResource, writeIdentity bool,
) *RateTracker {
	if resource == QuotaResourceGraphQL {
		if writeIdentity {
			return s.writeGQLRateTrackers[bucket]
		}
		// GraphQL trackers hang off the route's fetcher rather than a
		// syncer-level map.
		fetcher := s.fetcherForContext(ctx, repo)
		if fetcher == nil {
			return nil
		}
		return fetcher.RateTracker()
	}
	if writeIdentity {
		if tracker := s.writeRateTrackers[bucket]; tracker != nil {
			return tracker
		}
	}
	return s.rateTrackers[bucket]
}

// repoEligibility computes which credential buckets may sync now. Buckets are
// the routed identity buckets, so one credential's exhaustion cannot throttle
// a repository routed to a different credential.
func (s *Syncer) repoEligibility(
	repos []RepoRef,
	nextAfter map[string]time.Time,
) map[string]bool {
	now := time.Now().UTC()
	eligible := make(map[string]bool, len(repos))
	for _, repo := range repos {
		key, err := s.bucketKeyForRepo(repo, false)
		if err != nil {
			continue
		}
		if _, checked := eligible[key]; checked {
			continue
		}
		if after, ok := nextAfter[key]; ok && now.Before(after) {
			eligible[key] = false
			continue
		}
		if repoPlatform(repo) == platform.KindGitHub && s.quotaRegistry != nil {
			// Ordinary background sync may establish an unknown pool from
			// response headers, so unknown quota is not a stop. Archive
			// admission is stricter and waits for both pools to be known
			// before spending surplus.
			eligible[key] = !s.backgroundReserveExhausted(
				repo, QuotaResourceREST, false,
			)
			continue
		}
		tracker := s.rateTrackers[key]
		eligible[key] = tracker == nil || !tracker.IsPaused()
	}
	return eligible
}

// advanceNextSync updates the next-sync-after gate for buckets
// that were eligible, using each bucket's current throttle factor.
func (s *Syncer) advanceNextSync(
	eligible map[string]bool,
	nextAfter map[string]time.Time,
	interval time.Duration,
) {
	now := time.Now()
	for host, ok := range eligible {
		if !ok {
			continue
		}
		rt := s.rateTrackers[host]
		if rt == nil {
			continue
		}
		nextAfter[host] = now.Add(interval * time.Duration(rt.ThrottleFactor()))
	}
}

// graphQLReadAllowed reports whether optional GraphQL sync work may run for a
// repository. It reads the routed credential's pool so one exhausted identity
// cannot suppress a healthy identity sharing the same host tracker. Background
// work holds the GraphQL reserve; foreground work stops only on hard backoff.
func (s *Syncer) graphQLReadAllowed(
	ctx context.Context, repo RepoRef, fetcher *GraphQLFetcher,
) bool {
	verdict := s.reserveVerdictForContext(
		ctx,
		repo, QuotaResourceGraphQL, false, IsSyncBudgetContext(ctx),
	)
	// A known credential pool answers on its own. Falling through to the
	// fetcher's tracker would consult a host-wide signal that both credentials
	// on a split-auth host feed, letting one exhausted pool suppress bulk
	// fetches for a healthy one.
	if verdict.known {
		return !verdict.exhausted
	}
	backoff, _ := fetcher.ShouldBackoff()
	return !backoff
}
