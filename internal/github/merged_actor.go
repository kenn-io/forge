package github

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"go.kenn.io/forge/internal/archive"
	"go.kenn.io/forge/internal/db"
	"go.kenn.io/forge/platform"
)

// mergedActorBackfillPerSync caps how many merged MRs are re-fetched per
// sync cycle to backfill a missing merged actor; mergedActorBackfillWindow
// bounds how far back the backfill looks so a provider that never reports
// the actor cannot cause an unbounded refetch loop.
const (
	mergedActorBackfillPerSync       = 10
	mergedActorBackfillWindow        = 90 * 24 * time.Hour
	mergedActorBackfillSweepInterval = time.Hour
)

type mergedActorSweepState struct {
	Cursor    db.MergedMRMissingActorCursor
	RestartAt time.Time
}

// reconcileMergedActorEvents backfills authored merged events for MRs that
// were marked merged without one. Merges performed through forge itself
// eagerly write state=merged, which suppresses the open->closed transition
// in syncMergeRequestsFromList — the only sync path that fetches the MR
// with its merged_by actor. Failures are logged, not propagated: this is a
// repair pass and must not fail an otherwise healthy sync cycle.
//
// Each cycle takes the next batch below the per-repo sweep cursor instead
// of a fixed newest-first batch: candidates whose provider permanently
// reports no actor (e.g. the merging account was deleted) would otherwise
// occupy every batch and starve older candidates. An exhausted sweep retains
// a bounded cooldown, so persistent candidates are retried once per interval,
// not once per sync cycle.
func (s *Syncer) reconcileMergedActorEvents(
	ctx context.Context,
	repo RepoRef,
	repoID int64,
) {
	now := s.nowUTC()
	since := now.Add(-mergedActorBackfillWindow)
	cursor, ready := s.mergedActorSweepCursor(repoID, now)
	if !ready {
		return
	}
	missing, err := s.db.GetMergedMRNumbersMissingMergedActor(
		ctx, repoID, since, cursor,
		mergedActorBackfillPerSync,
	)
	if err != nil {
		slog.Error("list merged MRs missing merged actor",
			"repo", repo.Owner+"/"+repo.Name, "err", err)
		return
	}
	if len(missing) < mergedActorBackfillPerSync {
		s.setMergedActorSweepState(repoID, mergedActorSweepState{
			RestartAt: now.Add(mergedActorBackfillSweepInterval),
		})
	} else {
		last := missing[len(missing)-1]
		s.setMergedActorSweepState(repoID, mergedActorSweepState{
			Cursor: db.MergedMRMissingActorCursor{
				MergedAt:       last.MergedAt,
				MergeRequestID: last.MergeRequestID,
			},
		})
	}
	for _, candidate := range missing {
		changed, err := s.backfillMergedActorEvent(ctx, repo, repoID, candidate.Number)
		if err != nil {
			logSyncFailure(ctx, slog.LevelWarn, "backfill merged actor failed", err,
				"repo", repo.Owner+"/"+repo.Name,
				"number", candidate.Number)
			continue
		}
		if changed && s.onMergedActorRepaired != nil {
			s.onMergedActorRepaired(ctx, repoID, candidate.Number)
		}
	}
}

// mergedActorSweepCursor returns the exclusive composite upper bound for the
// repo's next backfill batch. Exhausted sweeps retain a cooldown before a new
// sweep starts from the top of the window; the initial timestamp sits ahead of
// now so a merge stamped by a marginally faster clock is still swept.
func (s *Syncer) mergedActorSweepCursor(
	repoID int64,
	now time.Time,
) (db.MergedMRMissingActorCursor, bool) {
	s.mergedActorCursorMu.Lock()
	defer s.mergedActorCursorMu.Unlock()
	state, ok := s.mergedActorCursors[repoID]
	if ok && !state.RestartAt.IsZero() {
		if now.Before(state.RestartAt) {
			return db.MergedMRMissingActorCursor{}, false
		}
		delete(s.mergedActorCursors, repoID)
		ok = false
	}
	if !ok {
		return db.MergedMRMissingActorCursor{
			MergedAt:       now.Add(time.Hour),
			MergeRequestID: 1<<63 - 1,
		}, true
	}
	return state.Cursor, true
}

func (s *Syncer) setMergedActorSweepState(
	repoID int64,
	state mergedActorSweepState,
) {
	s.mergedActorCursorMu.Lock()
	defer s.mergedActorCursorMu.Unlock()
	if s.mergedActorCursors == nil {
		s.mergedActorCursors = make(map[int64]mergedActorSweepState)
	}
	s.mergedActorCursors[repoID] = state
}

// BackfillMergedActorEventOnProvider records the authored merged lifecycle
// event for one MR by re-fetching it from the provider, reporting whether an
// event was inserted. The merge mutation calls this in the background after
// a successful merge: its eager state=merged write both suppresses the
// sync-side closed transition and advances updated_at past the provider's,
// so a full snapshot resync would be rejected as stale and never persist the
// actor. The provider target is resolved from the stable repository row for
// repoID so a configuration rename between the mutation and this background
// pass cannot fetch from a different repository than the row being updated.
func (s *Syncer) BackfillMergedActorEventOnProvider(
	ctx context.Context,
	repoID int64,
	number int,
) (bool, error) {
	stored, err := s.db.GetRepoByID(ctx, repoID)
	if err != nil {
		return false, fmt.Errorf("get repo %d for merged-actor backfill: %w", repoID, err)
	}
	if stored == nil {
		return false, fmt.Errorf("repo %d is not known for merged-actor backfill", repoID)
	}
	kind := platform.Kind(stored.Platform)
	providerID := stored.Key
	if providerID.IsZero() {
		return false, fmt.Errorf(
			"repo %d has no stable provider ID for merged-actor backfill", repoID,
		)
	}
	repo, ok := s.trackedRepoByProviderKey(kind, stored.PlatformHost, providerID)
	if !ok {
		routed, routeOK := s.trackedRepoByIdentity(
			kind, stored.Owner, stored.Name, stored.PlatformHost,
		)
		if !routeOK {
			return false, fmt.Errorf(
				"repo %s/%s on %s/%s with provider ID %s is %w",
				stored.Owner, stored.Name, stored.Platform, stored.PlatformHost, providerID,
				ErrRepoNotTracked,
			)
		}
		if routedID := routed.Key; !routedID.IsZero() {
			return false, fmt.Errorf(
				"tracked repo %s/%s provider ID %s does not match stored provider ID %s",
				stored.Owner, stored.Name, routedID, providerID,
			)
		}
		repo = routed
	}
	repo = repoRefFromStoredIdentity(repo, *stored)
	bucket, err := s.bucketKeyForRepo(repo, false)
	if err != nil {
		return false, fmt.Errorf(
			"resolve merged-actor credential route for %s/%s: %w",
			repo.Owner, repo.Name, err,
		)
	}
	releaseProviderWork := s.beginProviderWork(ctx, bucket, archive.PriorityActiveDetail)
	defer releaseProviderWork()
	return s.backfillMergedActorEvent(ctx, repo, repoID, number)
}

// repoRefFromStoredIdentity preserves the current provider-verified route
// while attaching stable identity and non-routing metadata from persistence.
func repoRefFromStoredIdentity(tracked RepoRef, stored db.Repo) RepoRef {
	repo := tracked
	repo.Platform = platform.Kind(stored.Platform)
	repo.PlatformHost = stored.PlatformHost
	repo.RepoID = stored.ID
	repo.Key = stored.Key
	if repo.WebURL == "" {
		repo.WebURL = stored.WebURL
	}
	if repo.CloneURL == "" {
		repo.CloneURL = stored.CloneURL
	}
	if repo.DefaultBranch == "" {
		repo.DefaultBranch = stored.DefaultBranch
	}
	return repo
}

// backfillMergedActorEvent persists the authored merged event from a fresh
// provider fetch, bypassing the parent-snapshot staleness gate — the event
// is keyed to the MR row and does not depend on snapshot acceptance. It
// reports whether an event was inserted.
func (s *Syncer) backfillMergedActorEvent(
	ctx context.Context,
	repo RepoRef,
	repoID int64,
	number int,
) (bool, error) {
	removed, err := s.db.IsArchiveItemRemovedUpstream(
		ctx, repoID, db.ArchiveItemTypeMergeRequest, number,
	)
	if err != nil {
		return false, fmt.Errorf(
			"check MR #%d for merged-actor backfill: %w", number, err,
		)
	}
	if removed {
		return false, nil
	}
	stored, err := s.db.GetMergeRequestByRepoIDAndNumber(ctx, repoID, number)
	if err != nil {
		return false, fmt.Errorf("get MR #%d for merged-actor backfill: %w", number, err)
	}
	if stored == nil {
		return false, nil
	}
	mrReader, err := s.mergeRequestReaderFor(repo)
	if err != nil {
		return false, fmt.Errorf(
			"resolve merge request reader for %s/%s: %w", repo.Owner, repo.Name, err,
		)
	}
	mr, err := mrReader.GetMergeRequest(ctx, platformRepoRef(repo), number)
	if err != nil {
		return false, fmt.Errorf("get MR #%d for merged-actor backfill: %w", number, err)
	}
	storedRepo, err := s.db.GetRepoByID(ctx, repoID)
	if err != nil {
		return false, fmt.Errorf("get repo %d for merged-actor identity check: %w", repoID, err)
	}
	if storedRepo == nil {
		return false, fmt.Errorf("repo %d disappeared during merged-actor backfill", repoID)
	}
	if err := s.verifyMergedActorBackfillIdentity(
		ctx, repo, storedRepo.Key,
	); err != nil {
		return false, err
	}
	inserted, err := s.persistMergedActorEvent(
		ctx, stored.ID, 0, mr.MergedBy, stored.MergedAt,
	)
	if err != nil {
		return false, fmt.Errorf(
			"persist merged lifecycle event for MR #%d: %w", number, err,
		)
	}
	return inserted, nil
}

func (s *Syncer) verifyMergedActorBackfillIdentity(
	ctx context.Context,
	repo RepoRef,
	expectedProviderID platform.RepositoryKey,
) error {
	if expectedProviderID.IsZero() {
		return errors.New("merged-actor backfill requires a stable provider ID")
	}
	reader, err := s.clients.RepositoryReader(repoPlatform(repo), repoHost(repo))
	if err != nil {
		return fmt.Errorf("resolve repository reader for merged-actor identity check: %w", err)
	}
	// The merge request was fetched by route, so the check resolves that
	// route: a lookup by ID would always report the expected repository.
	routeRef := platformRepoRef(repo)
	routeRef.Key = platform.RepositoryKey{}
	observed, err := reader.GetRepository(ctx, routeRef)
	if err != nil {
		return fmt.Errorf(
			"verify repository identity before merged-actor persistence: %w", err,
		)
	}
	observedProviderID := observed.Ref.Key
	if observedProviderID.IsZero() {
		return errors.New("provider returned no repository ID during merged-actor identity check")
	}
	if observedProviderID != expectedProviderID {
		return fmt.Errorf(
			"repository route %s/%s changed provider ID from %s to %s during merged-actor backfill",
			repo.Owner, repo.Name, expectedProviderID, observedProviderID,
		)
	}
	return nil
}
