package github

import (
	"context"
	"log/slog"
	"maps"
	"slices"
	"time"

	"go.kenn.io/forge/internal/tokenauth"
	"go.kenn.io/forge/platform"
	platformgithub "go.kenn.io/forge/platform/github"
)

// SetRatePrincipalLabels registers safe display labels keyed by rate bucket.
func (s *Syncer) SetRatePrincipalLabels(labels map[string]string) {
	s.ratePrincipalLabels = labels
}

// RatePrincipalLabels returns safe display labels keyed by rate bucket.
func (s *Syncer) RatePrincipalLabels() map[string]string {
	return s.ratePrincipalLabels
}

// RateTrackers returns the per-host rate trackers map.
func (s *Syncer) RateTrackers() map[string]*RateTracker {
	return s.rateTrackers
}

// SetWriteRateTrackers attaches the per-host trackers fed by
// mutation-path responses. Kept apart from rateTrackers: the write
// credential (the user's PAT when a GitHub App handles sync reads)
// has its own budget, and the sync tracker iteration paths
// (snapshot refresh, pause handling) must not treat it as a sync
// bucket. Mutation operation availability consults this map.
func (s *Syncer) SetWriteRateTrackers(trackers map[string]*RateTracker) {
	s.writeRateTrackers = trackers
}

// WriteRateTrackers returns the per-host mutation-credential rate
// trackers; empty when no host splits read and write credentials.
func (s *Syncer) WriteRateTrackers() map[string]*RateTracker {
	if s.writeRateTrackers == nil {
		return map[string]*RateTracker{}
	}
	return s.writeRateTrackers
}

// SetWriteGQLRateTrackers attaches the per-host trackers fed by the
// write credential's GraphQL mutation responses (ready-for-review).
func (s *Syncer) SetWriteGQLRateTrackers(trackers map[string]*RateTracker) {
	s.writeGQLRateTrackers = trackers
}

// WriteGQLRateTrackers returns the per-host write-credential GraphQL
// trackers; empty when no host splits read and write credentials.
func (s *Syncer) WriteGQLRateTrackers() map[string]*RateTracker {
	if s.writeGQLRateTrackers == nil {
		return map[string]*RateTracker{}
	}
	return s.writeGQLRateTrackers
}

// Budgets returns the per-host sync budgets map.
func (s *Syncer) Budgets() map[string]*SyncBudget {
	return s.budgets
}

// SetBudgetLimit applies a new hourly ceiling to every sync budget. Each
// budget keeps the spend already recorded in its current window.
func (s *Syncer) SetBudgetLimit(limit int) {
	for _, budget := range s.budgets {
		if budget != nil {
			budget.SetLimit(limit)
		}
	}
}

func (s *Syncer) SetQuotaRegistry(registry *QuotaRegistry) {
	s.quotaRegistry = registry
}

func (s *Syncer) QuotaRegistry() *QuotaRegistry {
	return s.quotaRegistry
}

// GQLRateTrackers returns per-provider/host GraphQL rate trackers
// extracted from the registered GraphQL fetchers. Hosts with
// nil fetchers or trackers are skipped.
func (s *Syncer) GQLRateTrackers() map[string]*RateTracker {
	result := make(map[string]*RateTracker, len(s.fetchers))
	add := func(f *GraphQLFetcher) {
		if f == nil {
			return
		}
		if rt := f.RateTracker(); rt != nil {
			result[rt.BucketKey()] = rt
		}
	}
	for _, f := range s.fetchers {
		add(f)
	}
	for _, router := range s.routers {
		for _, route := range router.Routes() {
			add(route.Fetcher)
			add(route.ArchiveFetcher)
		}
	}
	return result
}

type rateLimitSnapshotter interface {
	GetRateLimitSnapshot(ctx context.Context) (*platformgithub.RateLimitSnapshot, error)
}

// RefreshRateLimitSnapshots refreshes GitHub REST and GraphQL quota facts from
// GitHub's /rate_limit endpoint. The snapshot call is intentionally not
// recorded as a kenn-forge request because GitHub does not charge it against the
// primary REST budget.
func (s *Syncer) RefreshRateLimitSnapshots(ctx context.Context) {
	s.refreshRateLimitSnapshots(ctx)
}

func (s *Syncer) refreshRateLimitSnapshots(ctx context.Context) map[string]struct{} {
	refreshed := make(map[string]struct{})
	if !s.SyncEnabled() || s.clients == nil {
		return refreshed
	}
	type snapshotCandidate struct {
		client  Client
		tracker *RateTracker
		fetcher *GraphQLFetcher
		owner   string
		// credential distinguishes routes backed by different token sources.
		// Startup builds a separate client per route, so client identity
		// cannot tell many routes on one credential apart from genuinely
		// independent credentials resolving to the same account.
		credential string
	}
	candidates := make(map[string][]snapshotCandidate)
	for _, router := range s.routers {
		for _, route := range router.Routes() {
			readBucket := RateBucketKey(
				string(platform.KindGitHub), route.ReadIdentity.Host,
				route.ReadIdentity.Principal,
			)
			candidates[readBucket] = append(candidates[readBucket], snapshotCandidate{
				client: route.Client, tracker: s.rateTrackers[readBucket],
				fetcher: route.Fetcher, owner: route.Key.Owner,
				credential: route.CredentialKey,
			})
			if route.ArchiveReadIdentity.Principal != "" {
				archiveBucket := RateBucketKey(
					string(platform.KindGitHub), route.ArchiveReadIdentity.Host,
					route.ArchiveReadIdentity.Principal,
				)
				candidates[archiveBucket] = append(candidates[archiveBucket], snapshotCandidate{
					client:     route.ArchiveClient,
					tracker:    s.rateTrackers[archiveBucket],
					fetcher:    route.ArchiveFetcher,
					owner:      route.ArchiveKey.Owner,
					credential: route.ArchiveCredentialKey,
				})
			}
			if route.WriteIdentity.Principal != "" && route.WriteIdentity != route.ReadIdentity {
				writeBucket := RateBucketKey(
					string(platform.KindGitHub), route.WriteIdentity.Host,
					route.WriteIdentity.Principal,
				)
				writeGQL := s.writeGQLRateTrackers[writeBucket]
				var writeFetcher *GraphQLFetcher
				if writeGQL != nil {
					writeFetcher = &GraphQLFetcher{rateTracker: writeGQL}
				}
				candidates[writeBucket] = append(candidates[writeBucket], snapshotCandidate{
					client:  route.WriteSnapshotClient,
					tracker: s.writeRateTrackers[writeBucket],
					fetcher: writeFetcher, owner: route.Key.Owner,
					credential: route.WriteCredentialKey,
				})
			}
		}
	}
	for _, bucket := range slices.Sorted(maps.Keys(candidates)) {
		// Claim the refresh window before the attempt, not after it succeeds.
		// Recording it only on success let a credential whose /rate_limit call
		// keeps failing re-attempt on every pass, so a broken App installation
		// produced a steady stream of failing requests instead of one per
		// window. A failure now leaves the previous snapshot in place and
		// waits for the next window.
		if !s.claimRateLimitSnapshotRefresh(bucket, time.Now().UTC()) {
			continue
		}
		// Routes sharing a credential may still hold distinct token sources, so
		// a route whose token is broken must not stop the credential from being
		// refreshed through a healthy one. Only distinct sources are worth
		// trying: asking the same credential once per repository owner under a
		// shared App installation cannot produce a different answer, and turns
		// one failure into one request per owner. A route with no known
		// credential key counts as its own source.
		attemptedCredentials := make(map[string]struct{}, len(candidates[bucket]))
		attemptedClients := make(map[Client]struct{}, len(candidates[bucket]))
		for _, candidate := range candidates[bucket] {
			if candidate.tracker == nil || candidate.client == nil {
				continue
			}
			if candidate.credential != "" {
				if _, seen := attemptedCredentials[candidate.credential]; seen {
					continue
				}
				attemptedCredentials[candidate.credential] = struct{}{}
			} else if _, seen := attemptedClients[candidate.client]; seen {
				continue
			}
			attemptedClients[candidate.client] = struct{}{}
			snapshotRoute := &Route{
				Client: candidate.client, Fetcher: candidate.fetcher,
			}
			snapshotCtx := tokenauth.WithGitHubOwner(ctx, candidate.owner)
			if s.refreshRateLimitSnapshotForRoute(
				snapshotCtx, snapshotRoute, candidate.tracker,
			) {
				refreshed[bucket] = struct{}{}
				break
			}
		}
	}
	for _, rt := range s.rateTrackers {
		if rt == nil || rt.Provider() != string(platform.KindGitHub) || rt.APIType() != "rest" {
			continue
		}
		if rt.Principal() != "host" {
			continue
		}
		key := rt.BucketKey()
		if !s.claimRateLimitSnapshotRefresh(key, time.Now().UTC()) {
			continue
		}
		client, err := s.clientFor(RepoRef{
			Platform: platform.KindGitHub, PlatformHost: rt.PlatformHost(),
		})
		if err != nil {
			continue
		}
		route := &Route{
			Client:  client,
			Fetcher: s.fetchers[canonicalRepoHost(rt.PlatformHost())],
		}
		if s.refreshRateLimitSnapshotForRoute(ctx, route, rt) {
			refreshed[key] = struct{}{}
		}
	}
	return refreshed
}

func (s *Syncer) refreshRateLimitSnapshotForRoute(
	ctx context.Context, route *Route, rt *RateTracker,
) bool {
	if route == nil || route.Client == nil {
		return false
	}
	snapshotter, ok := route.Client.(rateLimitSnapshotter)
	if !ok {
		return false
	}
	snapshot, err := snapshotter.GetRateLimitSnapshot(ctx)
	if err != nil {
		slog.Warn("refresh GitHub rate limit snapshot failed",
			"host", rt.PlatformHost(), "principal", rt.Principal(), "err", err)
		return false
	}
	if snapshot == nil {
		return false
	}
	// The tracker is already bound to one principal's bucket, so it names the
	// identity whose pools this snapshot describes. Reconciling the registry
	// here keeps provider quota and the local trackers reading the same facts.
	identity := IdentityKey{Host: rt.PlatformHost(), Principal: rt.Principal()}
	// The cached reserve verdicts for this credential were computed from the
	// numbers this snapshot replaces, so drop them and let the next check
	// re-read. This is what puts the reserve on the snapshot's cadence rather
	// than merely on the same interval.
	defer s.invalidateReserveVerdicts(rt.BucketKey())
	updated := false
	if snapshot.Core != nil {
		rt.UpdateFromSnapshot(*snapshot.Core)
		s.quotaRegistry.UpdateSnapshot(identity, QuotaResourceREST, *snapshot.Core)
		updated = true
	}
	if snapshot.GraphQL != nil {
		if route.Fetcher != nil && route.Fetcher.RateTracker() != nil {
			route.Fetcher.RateTracker().UpdateFromSnapshot(*snapshot.GraphQL)
		}
		s.quotaRegistry.UpdateSnapshot(identity, QuotaResourceGraphQL, *snapshot.GraphQL)
	}
	return updated
}

func (s *Syncer) canRefreshRateLimitSnapshot(key string, now time.Time) bool {
	s.rateLimitSnapshotMu.Lock()
	defer s.rateLimitSnapshotMu.Unlock()
	last := s.rateLimitSnapshotRefresh[key]
	return last.IsZero() || now.Sub(last) >= rateLimitSnapshotRefreshInterval
}

func (s *Syncer) markRateLimitSnapshotRefreshed(key string, now time.Time) {
	s.rateLimitSnapshotMu.Lock()
	if s.rateLimitSnapshotRefresh == nil {
		s.rateLimitSnapshotRefresh = make(map[string]time.Time)
	}
	s.rateLimitSnapshotRefresh[key] = now
	s.rateLimitSnapshotMu.Unlock()
}

func (s *Syncer) claimRateLimitSnapshotRefresh(key string, now time.Time) bool {
	if !s.canRefreshRateLimitSnapshot(key, now) {
		return false
	}
	s.markRateLimitSnapshotRefreshed(key, now)
	return true
}

func (s *Syncer) clearRecoveredRateLimitGates(
	refreshed map[string]struct{},
	nextAfter map[string]time.Time,
	interval time.Duration,
) {
	if len(refreshed) == 0 || nextAfter == nil || interval <= 0 {
		return
	}
	now := time.Now().UTC()
	for bucket := range refreshed {
		after, ok := nextAfter[bucket]
		if !ok || !now.Before(after) {
			continue
		}
		rt := s.rateTrackers[bucket]
		if rt == nil {
			continue
		}
		refreshedDelay := interval * time.Duration(rt.ThrottleFactor())
		if after.Sub(now) > refreshedDelay {
			delete(nextAfter, bucket)
		}
	}
}
