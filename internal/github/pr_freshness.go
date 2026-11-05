package github

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"go.kenn.io/forge/internal/archive"
	"go.kenn.io/forge/internal/db"
	"go.kenn.io/forge/platform"
)

// prFreshnessFullReadInterval bounds how long a matching probe can stand in
// for a full read. The probe cannot see everything: an edit inside a review
// thread, or moderation of a comment older than the hundred most recently
// updated, changes no probed field.
const prFreshnessFullReadInterval = 30 * time.Minute

type prFreshnessBaseline struct {
	fingerprint string
	fullReadAt  time.Time
}

// prFreshnessBaselines remembers the probe fingerprint taken just before
// each pull request's last successful full read. A later probe with the same
// fingerprint proves the stored detail is still current. The probe runs
// before the read, so a change that lands between them makes the next probe
// differ and forces another read; it can never hide a change.
type prFreshnessBaselines struct {
	window  time.Duration
	mu      sync.Mutex
	entries map[string]prFreshnessBaseline
}

func (b *prFreshnessBaselines) matches(key, fingerprint string, now time.Time) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	baseline, ok := b.entries[key]
	return ok && baseline.fingerprint != "" && baseline.fingerprint == fingerprint &&
		now.Sub(baseline.fullReadAt) < b.window
}

// known reports whether key has a baseline that can still vouch.
func (b *prFreshnessBaselines) known(key string, now time.Time) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	baseline, ok := b.entries[key]
	return ok && now.Sub(baseline.fullReadAt) < b.window
}

func (b *prFreshnessBaselines) record(key, fingerprint string, now time.Time) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.entries == nil {
		b.entries = make(map[string]prFreshnessBaseline)
	}
	// An expired baseline can never match, so drop it.
	for k, baseline := range b.entries {
		if now.Sub(baseline.fullReadAt) >= b.window {
			delete(b.entries, k)
		}
	}
	b.entries[key] = prFreshnessBaseline{fingerprint: fingerprint, fullReadAt: now}
}

// recordIncomplete leaves a marker that never matches, so a pull request
// whose last read kept stored data reads as changed until a read completes.
func (b *prFreshnessBaselines) recordIncomplete(key string, now time.Time) {
	b.record(key, "", now)
}

func (b *prFreshnessBaselines) forget(key string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	delete(b.entries, key)
}

// probeMRFreshness probes the GitHub pull requests in mrs in batched
// GraphQL reads, keyed by watchedMRKey. A pull request is missing from the
// result when it is not on GitHub, its credential's GraphQL pool is held
// back, or the probe failed.
func (s *Syncer) probeMRFreshness(ctx context.Context, mrs []WatchedMR) map[string]PullRequestFreshness {
	targets := make([]freshnessTarget, 0, len(mrs))
	for _, mr := range mrs {
		targets = append(targets, mrFreshnessTarget(mr))
	}
	return s.probeFreshness(ctx, targets, prFreshnessFields, false)
}

type freshnessTarget struct {
	repo RepoRef
	ref  PullRequestFreshnessRef
	key  string
}

func mrFreshnessTarget(mr WatchedMR) freshnessTarget {
	return freshnessTarget{
		repo: RepoRef{Platform: watchedMRPlatform(mr), PlatformHost: watchedMRHost(mr), Owner: mr.Owner, Name: mr.Name},
		ref:  PullRequestFreshnessRef{Owner: mr.Owner, Name: mr.Name, Number: mr.Number},
		key:  watchedMRKey(mr),
	}
}

func (s *Syncer) probeFreshness(ctx context.Context, targets []freshnessTarget, fields string, complete bool) map[string]PullRequestFreshness {
	type group struct {
		fetcher *GraphQLFetcher
		refs    []PullRequestFreshnessRef
		keys    map[PullRequestFreshnessRef]string
		buckets map[string]struct{}
	}
	var groups []*group
	byFetcher := make(map[*GraphQLFetcher]*group)
	for _, target := range targets {
		repo := target.repo
		if repoPlatform(repo) != platform.KindGitHub {
			continue
		}
		fetcher := s.fetcherForContext(ctx, repo)
		if fetcher == nil || !s.graphQLReadAllowed(ctx, repo, fetcher) {
			continue
		}
		g, ok := byFetcher[fetcher]
		if !ok {
			g = &group{
				fetcher: fetcher, keys: make(map[PullRequestFreshnessRef]string),
				buckets: make(map[string]struct{}),
			}
			byFetcher[fetcher] = g
			groups = append(groups, g)
		}
		ref := target.ref
		if _, seen := g.keys[ref]; seen {
			continue
		}
		g.keys[ref] = target.key
		g.refs = append(g.refs, ref)
		if bucket, err := s.bucketKeyForRepo(repo, false); err == nil {
			g.buckets[bucket] = struct{}{}
		}
	}
	out := make(map[string]PullRequestFreshness)
	for _, g := range groups {
		// Probes are active detail work, so archive collection on the same
		// credential yields to them.
		releases := make([]func(), 0, len(g.buckets))
		for bucket := range g.buckets {
			releases = append(releases, s.beginProviderWork(ctx, bucket, archive.PriorityActiveDetail))
		}
		fingerprints, err := g.fetcher.fetchFreshness(ctx, g.refs, fields, complete)
		for _, release := range releases {
			release()
		}
		if err != nil {
			logSyncFailure(ctx, slog.LevelDebug, "pull request freshness probe failed", err,
				"host", g.fetcher.host, "pull_requests", len(g.refs),
			)
		}
		for ref, freshness := range fingerprints {
			out[g.keys[ref]] = freshness
		}
	}
	return out
}

// markMRFreshFromProbe records a detail read for a pull request whose probe
// matched its last full read, without reading it again. It still
// revalidates head CI, and runs the follow-up reads every live detail sync owes:
// merge queue state when due, and merge rules for a blocked pull request. It reports false when the
// pull request needs a full read instead.
func (s *Syncer) markMRFreshFromProbe(
	ctx context.Context, mr WatchedMR, resolutions repoResolutionCache,
) (bool, error) {
	repo, ok := s.trackedRepoByIdentity(
		watchedMRPlatform(mr), mr.Owner, mr.Name, mr.PlatformHost,
	)
	if !ok || repo.Archived {
		return false, nil
	}
	bucket, err := s.bucketKeyForRepo(repo, false)
	if err != nil {
		return false, err
	}
	defer s.beginProviderWork(ctx, bucket, archive.PriorityActiveDetail)()
	resolution, _ := resolutions.resolve(ctx, s, repo)
	if resolution.err != nil {
		return false, resolution.err
	}
	repo, repoID := resolution.repo, resolution.repoID
	ctx = withCloneRepositoryIdentity(ctx, repo)
	existing, err := s.db.GetMergeRequestByRepoIDAndNumber(ctx, repoID, mr.Number)
	if err != nil || existing == nil || existing.State != db.MergeRequestStateOpen {
		return false, err
	}
	// Check counts by state cannot tell which check holds which result: two
	// checks can swap them without changing the counts. Head CI is always
	// revalidated, which costs no quota while it is unchanged.
	pending := existing.CIHadPending
	if existing.PlatformHeadSHA != "" {
		ciCtx, degraded := withDegradedSyncTracking(ctx)
		applied, err := s.refreshCIStatusSnapshot(
			ciCtx, repo, existing.ID, existing.SnapshotRevision, mr.Number,
			ciRulesBranch(existing), existing.PlatformHeadSHA,
		)
		if err != nil || !applied || degraded.Load() {
			return false, err
		}
		fresh, err := s.db.GetMergeRequestByRepoIDAndNumber(ctx, repoID, mr.Number)
		if err != nil || fresh == nil {
			return false, err
		}
		existing, pending = fresh, ciHasPending(fresh.CIChecksJSON)
	}
	metadataUpdates := s.computeCommitLiveness(
		ctx, repo, existing.ID, livenessHeadForRound(existing, existing), nil,
	)
	applied, err := s.db.MarkMergeRequestDetailFetchedSnapshot(
		ctx, existing.ID, existing.SnapshotRevision, pending, metadataUpdates,
	)
	if err != nil || !applied {
		return false, err
	}
	var followUpErr error
	s.refreshMergeQueueAfterDetailSync(ctx, repo, repoID, mr.Number, &followUpErr)
	s.refreshMergeBypassAfterDetailSync(ctx, repo, repoID, mr.Number, &followUpErr)
	s.notifyMRDetailSynced(ctx, repo, repoID, mr.Number)
	return true, nil
}

// detailReadSince reports whether the pull request's detail-fetched marker
// was set at or after since. Only a read whose datasets were all applied
// sets it; a read a concurrent sync superseded leaves it alone, so such a
// read cannot vouch for a probe. The marker uses the database clock.
func (s *Syncer) detailReadSince(ctx context.Context, target freshnessTarget, since time.Time) bool {
	repo := target.repo
	var fetched *time.Time
	if target.ref.IsIssue {
		row, err := s.db.GetIssue(ctx, string(repoPlatform(repo)), repoHost(repo), repo.Owner, repo.Name, target.ref.Number)
		if err != nil || row == nil {
			return false
		}
		fetched = row.DetailFetchedAt
	} else {
		row, err := s.db.GetMergeRequest(ctx, string(repoPlatform(repo)), repoHost(repo), repo.Owner, repo.Name, target.ref.Number)
		if err != nil || row == nil {
			return false
		}
		fetched = row.DetailFetchedAt
	}
	return fetched != nil && !fetched.Before(since.Truncate(time.Second))
}

func (s *Syncer) recordFreshnessAfterRead(
	ctx context.Context, baselines *prFreshnessBaselines, target freshnessTarget,
	fingerprint string, probedAt, readStarted time.Time, degraded bool, err error,
) {
	switch {
	case err != nil:
		baselines.forget(target.key)
	case degraded || !s.detailReadSince(ctx, target, readStarted):
		baselines.recordIncomplete(target.key, probedAt)
	case fingerprint != "":
		baselines.record(target.key, fingerprint, probedAt)
	default:
		baselines.forget(target.key)
	}
}

func (s *Syncer) markIssueFreshFromProbe(ctx context.Context, repoID int64, number int) (bool, error) {
	issue, err := s.db.GetIssueByRepoIDAndNumber(ctx, repoID, number)
	if err != nil || issue == nil || issue.State != "open" {
		return false, err
	}
	return s.db.MarkIssueDetailFetchedSnapshot(ctx, issue.ID, issue.SnapshotRevision)
}
