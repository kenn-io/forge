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
	mu      sync.Mutex
	entries map[string]prFreshnessBaseline
}

func (b *prFreshnessBaselines) matches(key, fingerprint string, now time.Time) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	baseline, ok := b.entries[key]
	return ok && baseline.fingerprint == fingerprint &&
		now.Sub(baseline.fullReadAt) < prFreshnessFullReadInterval
}

func (b *prFreshnessBaselines) record(key, fingerprint string, now time.Time) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.entries == nil {
		b.entries = make(map[string]prFreshnessBaseline)
	}
	// An expired baseline can never match, so drop it.
	for k, baseline := range b.entries {
		if now.Sub(baseline.fullReadAt) >= prFreshnessFullReadInterval {
			delete(b.entries, k)
		}
	}
	b.entries[key] = prFreshnessBaseline{fingerprint: fingerprint, fullReadAt: now}
}

func (b *prFreshnessBaselines) forget(key string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	delete(b.entries, key)
}

// probeMRFreshness fingerprints the open GitHub pull requests in mrs in
// batched GraphQL reads, keyed by watchedMRKey. A pull request is missing
// from the result when it is not on GitHub, its credential's GraphQL pool
// is held back, or the probe failed; it then takes the full read.
func (s *Syncer) probeMRFreshness(ctx context.Context, mrs []WatchedMR) map[string]string {
	type group struct {
		fetcher *GraphQLFetcher
		refs    []PullRequestFreshnessRef
		keys    map[PullRequestFreshnessRef]string
	}
	groups := make(map[*GraphQLFetcher]*group)
	var order []*GraphQLFetcher
	for _, mr := range mrs {
		if watchedMRPlatform(mr) != platform.KindGitHub {
			continue
		}
		repo := RepoRef{
			Platform: platform.KindGitHub, PlatformHost: watchedMRHost(mr),
			Owner: mr.Owner, Name: mr.Name,
		}
		fetcher := s.fetcherForContext(ctx, repo)
		if fetcher == nil || !s.graphQLReadAllowed(ctx, repo, fetcher) {
			continue
		}
		g := groups[fetcher]
		if g == nil {
			g = &group{fetcher: fetcher, keys: make(map[PullRequestFreshnessRef]string)}
			groups[fetcher] = g
			order = append(order, fetcher)
		}
		ref := PullRequestFreshnessRef{Owner: mr.Owner, Name: mr.Name, Number: mr.Number}
		if _, seen := g.keys[ref]; seen {
			continue
		}
		g.keys[ref] = watchedMRKey(mr)
		g.refs = append(g.refs, ref)
	}
	out := make(map[string]string)
	for _, fetcher := range order {
		g := groups[fetcher]
		fingerprints, err := fetcher.FetchPullRequestFreshness(ctx, g.refs)
		if err != nil {
			logSyncFailure(ctx, slog.LevelDebug, "pull request freshness probe failed", err,
				"host", fetcher.host, "pull_requests", len(g.refs),
			)
		}
		for ref, fingerprint := range fingerprints {
			out[g.keys[ref]] = fingerprint
		}
	}
	return out
}

// markMRFreshFromProbe records a detail read for a pull request whose probe
// matched its last full read, without reading it again. It still runs the
// follow-up reads every live detail sync owes: merge queue state when due,
// and merge rules for a blocked pull request. It reports false when the
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
	existing, err := s.db.GetMergeRequestByRepoIDAndNumber(ctx, repoID, mr.Number)
	if err != nil || existing == nil || existing.State != db.MergeRequestStateOpen {
		return false, err
	}
	metadataUpdates := s.computeCommitLiveness(
		ctx, repo, existing.ID, livenessHeadForRound(existing, existing), nil,
	)
	applied, err := s.db.MarkMergeRequestDetailFetchedSnapshot(
		ctx, existing.ID, existing.SnapshotRevision, existing.CIHadPending, metadataUpdates,
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
