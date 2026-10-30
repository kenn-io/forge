package github

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"go.kenn.io/forge/internal/platformdb"

	gh "github.com/google/go-github/v93/github"
	"go.kenn.io/forge/internal/archive"
	"go.kenn.io/forge/internal/db"
	"go.kenn.io/forge/platform"
	platformgithub "go.kenn.io/forge/platform/github"
)

// SyncMR fetches fresh data for a single MR from GitHub and updates the DB.
// Unlike the periodic sync, this always does a full fetch (details, timeline, CI).
// Returns an error if the repo is not in the configured repo list.
func (s *Syncer) SyncMR(ctx context.Context, owner, name string, number int) error {
	return s.syncMRWithHost(ctx, owner, name, number, "")
}

// SyncMROnProvider fetches fresh data for a single MR from a specific
// configured provider host.
// SyncClosedMROnProvider records a merge request the provider now reports as
// closed or merged, through the same close-detection flow the periodic sync
// uses. UI-driven terminal mutations call this instead of writing local state
// eagerly, so every terminal transition — user-initiated or detected — flows
// through the one parent-snapshot choke point, which finalizes commit
// liveness inside the transition's own transaction, and local rows always
// carry provider timestamps instead of eager local ones that would leave
// later resyncs rejected as stale. The provider target is resolved from the
// stable repository row for repoID — the same discipline the merged-actor
// backfill used — so a rename or route reuse between the mutation and this
// resync cannot fetch from a different repository than the row being updated.
func (s *Syncer) SyncClosedMROnProvider(
	ctx context.Context,
	repoID int64,
	number int,
) error {
	stored, err := s.db.GetRepoByID(ctx, repoID)
	if err != nil {
		return fmt.Errorf("get repo %d for closed-MR resync: %w", repoID, err)
	}
	if stored == nil {
		return fmt.Errorf("repo %d is not known for closed-MR resync", repoID)
	}
	kind := platform.Kind(stored.Platform)
	repo, ok := s.trackedRepoByProviderKey(
		kind, stored.PlatformHost, stored.Key,
	)
	if !ok {
		routed, routeOK := s.trackedRepoByIdentity(
			kind, stored.Owner, stored.Name, stored.PlatformHost,
		)
		if !routeOK {
			return fmt.Errorf(
				"repo %s/%s on %s/%s is %w",
				stored.Owner, stored.Name, stored.Platform, stored.PlatformHost, ErrRepoNotTracked,
			)
		}
		if routedID := routed.Key; !routedID.IsZero() &&
			routedID != stored.Key {
			return fmt.Errorf(
				"tracked repo %s/%s provider ID %s does not match stored provider ID %s",
				stored.Owner, stored.Name, routedID, stored.Key,
			)
		}
		repo = routed
	}
	repo = repoRefFromStoredIdentity(repo, *stored)
	ctx = withCloneRepositoryIdentity(ctx, repo)
	reader, err := s.mergeRequestReaderFor(repo)
	if err != nil {
		return fmt.Errorf(
			"resolve merge request reader for %s/%s: %w",
			stored.Owner, stored.Name, err,
		)
	}
	return s.fetchAndUpdateClosedMergeRequest(ctx, reader, repo, repoID, number, true)
}

func (s *Syncer) SyncMROnProvider(
	ctx context.Context,
	kind platform.Kind,
	host, owner, name string,
	number int,
) error {
	// A manual or post-mutation refresh reads everything unconditionally,
	// including CI and comment lists a background poll would revalidate,
	// so a user can always force a fresh answer past a weak validator.
	return s.syncMROnProvider(
		platformgithub.WithUnconditionalRead(ctx), kind, host, owner, name, number, mrDetailFull,
	)
}

// SyncMROnProviderIfChanged refreshes a PR page, retaining child freshness on a parent 304.
func (s *Syncer) SyncMROnProviderIfChanged(
	ctx context.Context,
	kind platform.Kind,
	host, owner, name string,
	number int,
) error {
	return s.syncMROnProvider(ctx, kind, host, owner, name, number, mrDetailPage)
}

type mrDetailMode uint8

const (
	mrDetailFull mrDetailMode = iota
	mrDetailBackground
	mrDetailPage
)

func (s *Syncer) syncMROnProvider(
	ctx context.Context,
	kind platform.Kind,
	host, owner, name string,
	number int,
	mode mrDetailMode,
) error {
	repo, ok := s.trackedRepoByIdentity(kind, owner, name, host)
	if !ok {
		host = repoHost(RepoRef{Platform: kind, PlatformHost: host})
		return fmt.Errorf(
			"repo %s/%s on %s/%s is %w",
			owner, name, kind, host, ErrRepoNotTracked,
		)
	}
	repo.Owner = owner
	repo.Name = name
	repo.PlatformHost = repoHost(repo)
	return s.syncMRForRepo(ctx, repo, number, mode, nil)
}

// SyncMRForRepository refreshes an MR only while repoID still owns repo's
// route. It is intended for delayed work that was queued for a stable
// repository identity rather than for an owner/name route.
func (s *Syncer) SyncMRForRepository(
	ctx context.Context,
	repo RepoRef,
	repoID int64,
	number int,
) error {
	return s.syncMRForRepoResolved(
		ctx, repo, number, mrDetailFull, nil, nil, nil, nil, &repoID, nil,
	)
}

// syncMRWithHost is the internal implementation of SyncMR.
// When hostHint is non-empty it is used instead of resolving via
// s.hostFor, avoiding ambiguity when the same owner/name exists on
// multiple hosts.
func (s *Syncer) syncMRWithHost(
	ctx context.Context,
	owner, name string,
	number int,
	hostHint string,
) error {
	var (
		repo RepoRef
		ok   bool
		err  error
	)
	if hostHint == "" {
		repo, ok, err = s.trackedRepo(owner, name)
	} else {
		repo, ok, err = s.trackedRepoOnHostUnique(owner, name, hostHint)
	}
	if err != nil {
		return err
	}
	if !ok {
		host := hostHint
		if host == "" {
			host = s.hostFor(owner, name)
		}
		return fmt.Errorf(
			"repo %s/%s on %s is %w", owner, name, host, ErrRepoNotTracked,
		)
	}
	repo.Owner = owner
	repo.Name = name
	repo.PlatformHost = repoHost(repo)
	return s.syncMRForRepo(ctx, repo, number, mrDetailFull, nil)
}

func (s *Syncer) syncMRWithWatchedRef(
	ctx context.Context,
	mr WatchedMR,
) error {
	return s.syncMRWithWatchedRefTracking(ctx, mr, nil, nil)
}

func (s *Syncer) syncMRWithWatchedRefTracking(
	ctx context.Context,
	mr WatchedMR,
	providerAttempted *bool,
	resolutions repoResolutionCache,
) error {
	kind := watchedMRPlatform(mr)
	repo, ok := s.trackedRepoByIdentity(
		kind, mr.Owner, mr.Name, mr.PlatformHost,
	)
	if !ok {
		host := repoHost(RepoRef{Platform: kind, PlatformHost: mr.PlatformHost})
		return fmt.Errorf(
			"repo %s/%s on %s/%s is %w",
			mr.Owner, mr.Name, kind, host, ErrRepoNotTracked,
		)
	}
	if repo.Archived {
		return nil
	}
	return s.syncMRForRepoResolved(
		ctx, repo, mr.Number, mrDetailBackground, providerAttempted, nil, nil, nil, nil, resolutions,
	)
}

func (s *Syncer) syncMRForRepo(
	ctx context.Context,
	repo RepoRef,
	number int,
	mode mrDetailMode,
	providerAttempted *bool,
) error {
	return s.syncMRForRepoResolved(
		ctx, repo, number, mode, providerAttempted, nil, nil, nil, nil, nil,
	)
}

// removedUpstreamForLiveSync fences delayed live work against an archive
// tombstone created after the work was queued. Archive hydration deliberately
// bypasses this check because it must fetch terminal items to repair them when
// they reappear upstream.
func (s *Syncer) removedUpstreamForLiveSync(
	ctx context.Context,
	repoID int64,
	itemType db.ArchiveItemType,
	number int,
) (bool, error) {
	if IsArchiveSyncBudgetContext(ctx) {
		return false, nil
	}
	return s.db.IsArchiveItemRemovedUpstream(ctx, repoID, itemType, number)
}

type mergeRequestFetchEvidence struct {
	merged         bool
	headSHA        string
	mergeCommitSHA string
	filesChanged   *int
}

// refreshMRDetailSnapshot shares child refreshes between full responses and page 304s.
func (s *Syncer) refreshMRDetailSnapshot(
	ctx context.Context,
	repo RepoRef,
	repoID, mrID, revision int64,
	number int,
	normalized, existing *db.MergeRequest,
	ghPR *gh.PullRequest,
	lifecyclePersisted *bool,
) (bool, error) {
	if err := s.refreshTimeline(
		ctx, repo, mrID, revision, number,
		livenessHeadForRound(normalized, existing),
	); err != nil {
		return false, fmt.Errorf("refresh timeline for MR #%d: %w", number, err)
	}
	if ghPR != nil {
		if _, err := s.persistMergedTransitionEvent(ctx, mrID, revision, ghPR, normalized.MergedAt); err != nil {
			return false, fmt.Errorf("persist merged lifecycle event for MR #%d: %w", number, err)
		}
		if lifecyclePersisted != nil {
			*lifecyclePersisted = true
		}
	}

	syncMRHeadSHA := normalized.PlatformHeadSHA
	ciApplied, err := s.refreshCIStatusSnapshot(
		ctx, repo, mrID, revision, number, syncMRHeadSHA,
	)
	if err != nil {
		return false, err
	}
	if !ciApplied {
		return false, nil
	}

	// Refresh workflow approval state for the current head SHA.
	// Persisting it here (instead of computing live on every GET)
	// means the DB-only detail path the frontend uses by default
	// can show the Approve Workflows button without a foreground
	// sync round-trip. The result is tied to syncMRHeadSHA so a
	// later read can detect a stale snapshot after force-push.
	approvalApplied, _ := s.refreshWorkflowApproval(
		ctx, repo, repoID, number, syncMRHeadSHA, ghPR, normalized,
	)
	if !approvalApplied {
		return false, nil
	}

	// Update ci_had_pending after refreshing CI status.
	fresh, freshErr := s.db.GetMergeRequestByRepoIDAndNumber(ctx, repoID, number)
	if freshErr == nil && fresh != nil {
		pending := ciHasPending(fresh.CIChecksJSON)
		detailApplied, detailErr := s.db.MarkMergeRequestDetailFetchedSnapshot(
			ctx, mrID, revision, pending, nil,
		)
		if detailErr != nil {
			return false, fmt.Errorf("mark detail fetched for MR #%d: %w", number, detailErr)
		}
		if !detailApplied {
			return false, nil
		}
	}
	s.notifyMRDetailSynced(ctx, repo, repoID, number)
	return true, nil
}

func (s *Syncer) notifyMRDetailSynced(ctx context.Context, repo RepoRef, repoID int64, number int) {
	if s.onMRSynced != nil {
		fresh, err := s.db.GetMergeRequestByRepoIDAndNumber(ctx, repoID, number)
		if err != nil {
			slog.Warn("get MR for onMRSynced hook failed",
				"repo", repo.Owner+"/"+repo.Name,
				"number", number,
				"err", err,
			)
		} else {
			s.onMRSynced(repo.Owner, repo.Name, fresh)
		}
	}
}

func (s *Syncer) canUseConditionalPRDetail(ctx context.Context, repo RepoRef, existing *db.MergeRequest, mode mrDetailMode) bool {
	switch mode {
	case mrDetailBackground:
		return true
	case mrDetailPage:
	default:
		return false
	}
	if existing == nil || existing.State != db.MergeRequestStateOpen {
		return false
	}
	if s.clones == nil {
		return true
	}
	diffSHAs := db.DiffSHAs{
		PlatformHeadSHA: existing.PlatformHeadSHA,
		PlatformBaseSHA: existing.PlatformBaseSHA,
		DiffHeadSHA:     existing.DiffHeadSHA,
		DiffBaseSHA:     existing.DiffBaseSHA,
		MergeBaseSHA:    existing.MergeBaseSHA,
		State:           string(existing.State),
	}
	if diffSHAs.Stale() {
		return false
	}
	for _, sha := range []string{diffSHAs.DiffHeadSHA, diffSHAs.MergeBaseSHA} {
		if _, err := s.clones.ResolveCommit(ctx, string(repoPlatform(repo)), repoHost(repo), repo.Owner, repo.Name, sha); err != nil {
			return false
		}
	}
	return true
}

type directSyncRepoResolution struct {
	repo   RepoRef
	repoID int64
	err    error
	// identity is the provider identity this lookup answered for: the
	// resolved key, or the tracked key when the lookup failed.
	identity platform.RepositoryKey
}

type repoResolutionCache map[string]directSyncRepoResolution

func (c repoResolutionCache) resolve(ctx context.Context, s *Syncer, repo RepoRef) (directSyncRepoResolution, bool) {
	key := detailRepoKey(repoPlatform(repo), repoHost(repo), repo.Owner, repo.Name)
	resolution, ok := c[key]
	// A route the tracked list now assigns to a different provider identity
	// is a replacement repository; neither a cached row ID nor a cached
	// failure describes it.
	if ok && !repo.Key.IsZero() && !resolution.identity.IsZero() &&
		resolution.identity != repo.Key {
		ok = false
	}
	if !ok {
		resolution = directSyncRepoResolution{identity: repo.Key}
		resolution.repo, resolution.repoID, resolution.err = s.reconcileRepoForDirectSync(ctx, repo)
		if !resolution.repo.Key.IsZero() {
			resolution.identity = resolution.repo.Key
		}
		if c != nil {
			c[key] = resolution
		}
	}
	return resolution, !ok
}

func (s *Syncer) syncMRForRepoResolved(
	ctx context.Context,
	repo RepoRef,
	number int,
	mode mrDetailMode,
	providerAttempted *bool,
	resolvedRepoID *int64,
	fetchedEvidence *mergeRequestFetchEvidence,
	lifecyclePersisted *bool,
	expectedRepoID *int64,
	resolutions repoResolutionCache,
) (retErr error) {
	if resolvedRepoID != nil {
		*resolvedRepoID = 0
	}
	if fetchedEvidence != nil {
		*fetchedEvidence = mergeRequestFetchEvidence{}
	}
	if lifecyclePersisted != nil {
		*lifecyclePersisted = false
	}
	if !IsArchiveSyncBudgetContext(ctx) {
		bucket, err := s.bucketKeyForRepo(repo, false)
		if err != nil {
			return fmt.Errorf("resolve detail credential route for %s/%s: %w", repo.Owner, repo.Name, err)
		}
		releaseProviderWork := s.beginProviderWork(ctx, bucket, archive.PriorityActiveDetail)
		defer releaseProviderWork()
	}

	owner := repo.Owner
	name := repo.Name
	mrReader, err := s.mergeRequestReaderFor(repo)
	if err != nil {
		return fmt.Errorf("resolve merge request reader for %s/%s: %w", owner, name, err)
	}

	resolution, _ := resolutions.resolve(ctx, s, repo)
	resolvedRef, repoID, err := resolution.repo, resolution.repoID, resolution.err
	if err != nil {
		return err
	}
	if expectedRepoID != nil && repoID != *expectedRepoID {
		return nil
	}
	if resolvedRepoID != nil {
		*resolvedRepoID = repoID
	}
	repo = resolvedRef
	if repo.Archived && !IsArchiveSyncBudgetContext(ctx) {
		// Live detail syncing stops on an archived repo; only archive
		// hydration, which runs under the archive budget, proceeds.
		slog.Debug("skipping MR detail sync for archived repo",
			"repo", owner+"/"+name, "number", number,
		)
		return nil
	}
	removed, err := s.removedUpstreamForLiveSync(
		ctx, repoID, db.ArchiveItemTypeMergeRequest, number,
	)
	if err != nil {
		return fmt.Errorf("check MR #%d visibility: %w", number, err)
	}
	if removed {
		return nil
	}
	ctx = withCloneRepositoryIdentity(ctx, repo)
	defer s.refreshMergeBypassAfterDetailSync(ctx, repo, repoID, number, &retErr)
	defer s.refreshMergeQueueAfterDetailSync(ctx, repo, repoID, number, &retErr)

	// Preserve derived fields that provider detail doesn't populate. CI is
	// refreshed later in this sync path; keeping the previous values here
	// prevents detail reads from briefly seeing "no CI" during refresh.
	existing, err := s.db.GetMergeRequestByRepoIDAndNumber(
		ctx, repoID, number,
	)
	if err != nil {
		return fmt.Errorf("get existing MR #%d: %w", number, err)
	}

	var ghPR *gh.PullRequest
	var platformMR platform.MergeRequest
	var normalized *db.MergeRequest
	var newETag string
	requestedAt := s.nowUTC()
	if rawReader, ok := mrReader.(interface {
		GetGitHubPullRequest(context.Context, platform.RepoRef, int) (*gh.PullRequest, platform.MergeRequest, error)
	}); ok {
		useConditionalPRDetail := s.canUseConditionalPRDetail(ctx, repo, existing, mode)
		if client, ok := s.optionalGitHubClientFor(repo); ok && (useConditionalPRDetail || mode == mrDetailPage) {
			var notModified bool
			if providerAttempted != nil {
				*providerAttempted = true
			}
			if useConditionalPRDetail {
				ghPR, newETag, notModified, err = s.getPullRequestForDetail(
					ctx, client, repo, number,
				)
			} else if conditional, ok := client.(conditionalPullRequestGetter); ok {
				// Full page reads still save the validator for the next poll.
				ghPR, newETag, notModified, err = conditional.GetPullRequestIfChanged(
					ctx, repo.Owner, repo.Name, number, "",
				)
			} else {
				ghPR, err = client.GetPullRequest(ctx, repo.Owner, repo.Name, number)
			}
			// Same canonical lookup classification as the raw
			// GetGitHubPullRequest branch below.
			if provider, ok := mrReader.(*platformgithub.Provider); ok {
				if outcomeErr := provider.MergeRequestLookupOutcomeError(
					ctx, platformRepoRef(repo), number, ghPR, err,
				); outcomeErr != nil {
					err = outcomeErr
				}
			}
			if err == nil && ghPR == nil {
				if notModified && existing != nil {
					if mode == mrDetailPage {
						_, err := s.refreshMRDetailSnapshot(
							ctx, repo, repoID, existing.ID, existing.SnapshotRevision, number,
							existing, existing, nil, nil,
						)
						if errors.Is(err, errParentSnapshotAdvanced) {
							return nil
						}
						return err
					}
					_, err := s.markUnchangedMRDetailFetched(
						ctx, repo, repoID, number, existing, 1,
					)
					return err
				}
				err = errors.New("client returned nil pull request")
			}
			if err == nil {
				normalized, err = NormalizePR(repoID, ghPR)
			}
		} else {
			if providerAttempted != nil {
				*providerAttempted = true
			}
			ghPR, platformMR, err = rawReader.GetGitHubPullRequest(ctx, platformRepoRef(repo), number)
			if err == nil {
				normalized = platformdb.DBMergeRequest(repoID, platformMR)
			}
		}
	} else {
		if providerAttempted != nil {
			*providerAttempted = true
		}
		platformMR, err = mrReader.GetMergeRequest(ctx, platformRepoRef(repo), number)
		if err == nil {
			normalized = platformdb.DBMergeRequest(repoID, platformMR)
		}
	}
	if err != nil {
		if errors.Is(err, ErrNilPullRequest) {
			return fmt.Errorf(
				"get MR %s/%s#%d: client returned nil pull request",
				owner, name, number,
			)
		}
		return fmt.Errorf("get MR %s/%s#%d: %w", owner, name, number, err)
	}
	if normalized == nil {
		return fmt.Errorf("get MR %s/%s#%d: provider returned no merge request", owner, name, number)
	}
	normalized.MergeableStateObservedAt = &requestedAt
	if fetchedEvidence != nil {
		*fetchedEvidence = mergeRequestFetchEvidence{
			merged:         normalized.State == db.MergeRequestStateMerged || normalized.MergedAt != nil,
			headSHA:        normalized.PlatformHeadSHA,
			mergeCommitSHA: normalized.MergeCommitSHA,
			filesChanged:   normalized.FilesChanged,
		}
	}
	headChanged := existing != nil &&
		existing.PlatformHeadSHA != normalized.PlatformHeadSHA
	if existing != nil {
		normalized.CommentCount = existing.CommentCount
		normalized.ReviewDecision = existing.ReviewDecision
		normalized.ReviewDecisionObservedAt = existing.ReviewDecisionObservedAt
		preserveMergeableStateIfOmitted(normalized, existing)
		preserveMergedAtIfOmitted(normalized, existing)
		// CI is tied to the head SHA. If the head moved we must clear the
		// previous values; otherwise a failed CI refresh would leave stale
		// checks attached to the new commit.
		if !headChanged {
			normalized.CIStatus = existing.CIStatus
			normalized.CIChecksJSON = existing.CIChecksJSON
			normalized.CIObservedAt = existing.CIObservedAt
			normalized.CIHadPending = existing.CIHadPending
		}
		normalized.DetailFetchedAt = existing.DetailFetchedAt
		if normalized.AuthorDisplayName == "" {
			normalized.AuthorDisplayName = existing.AuthorDisplayName
		}
	}

	if normalized.Author != "" && normalized.AuthorDisplayName == "" {
		// Resolve directly instead of using s.resolveDisplayName to
		// preserve existing display names on failure.
		if client, ok := s.optionalGitHubClientFor(repo); ok {
			if displayName, found := s.resolveDisplayName(ctx, client, repo, normalized.Author); found {
				normalized.AuthorDisplayName = displayName
			}
		}
		if normalized.AuthorDisplayName == "" && existing != nil {
			normalized.AuthorDisplayName = existing.AuthorDisplayName
		}
	}

	mrID, revision, accepted, err := s.CommitMergeRequestParentSnapshot(
		ctx, repo, normalized,
	)
	if err != nil {
		return fmt.Errorf("upsert MR #%d: %w", number, err)
	}
	if !accepted {
		if ghPR != nil && pullRequestWasMerged(ghPR) {
			abandonRepair := func() {
				if resolvedRepoID != nil {
					*resolvedRepoID = 0
				}
			}
			_, repairErr := s.db.FillMissingMergedMRMetrics(
				ctx,
				db.MergeRequestMergeMetrics{
					RepoID: repoID, Number: number,
					HeadSHA:        normalized.PlatformHeadSHA,
					MergeCommitSHA: &normalized.MergeCommitSHA,
					FilesChanged:   normalized.FilesChanged,
					MergedAt:       normalized.MergedAt,
				},
			)
			if errors.Is(repairErr, db.ErrRepositoryIdentityChanged) {
				abandonRepair()
				return nil
			}
			if repairErr != nil {
				return fmt.Errorf("repair merged MR #%d metrics: %w", number, repairErr)
			}
			if s.afterMergedMRMetricsRepair != nil {
				s.afterMergedMRMetricsRepair()
			}
			current, currentErr := s.db.GetMergeRequestByRepoIDAndNumber(
				ctx, repoID, number,
			)
			if currentErr != nil {
				return fmt.Errorf("read merged MR #%d after repair: %w", number, currentErr)
			}
			var currentMergedAt *time.Time
			if current != nil {
				currentMergedAt = current.MergedAt
			}
			if _, actorErr := s.persistMergedTransitionEvent(
				ctx, mrID, revision, ghPR, currentMergedAt,
			); errors.Is(actorErr, db.ErrRepositoryIdentityChanged) {
				abandonRepair()
				return nil
			} else if actorErr != nil {
				return fmt.Errorf("repair merged MR #%d actor: %w", number, actorErr)
			}
			if lifecyclePersisted != nil {
				*lifecyclePersisted = true
			}
		}
		return nil
	}
	if err := s.markClosedLinkedPRNotificationsDone(ctx, repoID, number); err != nil {
		if errors.Is(err, db.ErrRepositoryIdentityChanged) {
			return nil
		}
		return err
	}
	// UpsertMergeRequest preserves ci_had_pending across upserts. Clear
	// it here when the head SHA changed so a stale pending flag from
	// the previous head doesn't survive across the refresh.
	if headChanged {
		ciCleared, err := s.db.ClearMRCISnapshot(
			ctx, mrID, revision, normalized.PlatformHeadSHA,
		)
		if err != nil {
			return fmt.Errorf("clear stale CI for MR #%d: %w", number, err)
		}
		if !ciCleared {
			return nil
		}
	}
	if err := s.db.EnsureKanbanState(ctx, mrID); err != nil {
		return fmt.Errorf("ensure kanban state for MR #%d: %w", number, err)
	}

	var diffErr error
	if ghPR != nil {
		// Run the diff sync, but don't let its failure abort the rest of SyncMR:
		// timeline and CI status are independent and the user still wants them
		// fresh. Capture the error and surface it via DiffSyncError at the end.
		diffErr = s.syncMRDiff(
			ctx, repo, repoID, mrID, revision, number,
			ghPR, normalized,
		)
		if errors.Is(diffErr, errParentSnapshotAdvanced) {
			return nil
		}

		applied, err := s.refreshMRDetailSnapshot(
			ctx, repo, repoID, mrID, revision, number, normalized, existing, ghPR, lifecyclePersisted,
		)
		if errors.Is(err, errParentSnapshotAdvanced) {
			return nil
		}
		if err != nil || !applied {
			return err
		}
	} else {
		// Record the reviewed diff snapshot for non-GitHub providers
		// too: head-binding providers refuse merge/approve until
		// DiffHeadSHA matches the platform head, so a sync that never
		// writes it would leave head-bound actions permanently
		// disabled with 409 head_unknown.
		diffErr = s.syncProviderMRDiff(
			ctx, repo, repoID, mrID, revision, number,
			normalized, true,
		)
		if errors.Is(diffErr, errParentSnapshotAdvanced) {
			return nil
		}

		var pending bool
		_, pending, err = s.syncProviderMRDetailExtras(
			ctx, mrReader, repo, mrID, number, revision, normalized.PlatformHeadSHA,
			livenessHeadForRound(normalized, existing),
		)
		if err != nil {
			if errors.Is(err, errParentSnapshotAdvanced) {
				return nil
			}
			return err
		}
		if _, err := s.persistMergedActorEvent(ctx, mrID, revision, platformMR.MergedBy, normalized.MergedAt); err != nil {
			return fmt.Errorf("persist merged lifecycle event for MR #%d: %w", number, err)
		}
		if lifecyclePersisted != nil {
			*lifecyclePersisted = true
		}
		detailApplied, err := s.db.MarkMergeRequestDetailFetchedSnapshot(
			ctx, mrID, revision, pending, nil,
		)
		if err != nil {
			return fmt.Errorf("mark detail fetched for MR #%d: %w", number, err)
		}
		if !detailApplied {
			return nil
		}
	}

	if ghPR == nil {
		s.notifyMRDetailSynced(ctx, repo, repoID, number)
	}

	if err := s.markClosedLinkedPRNotificationsDone(ctx, repoID, number); err != nil {
		if diffErr != nil {
			return errors.Join(diffErr, err)
		}
		return err
	}
	if diffErr != nil {
		return diffErr
	}
	if newETag != "" {
		if err := s.db.UpsertHTTPEtag(
			ctx, string(repoPlatform(repo)), repoHost(repo), repo.Owner, repo.Name,
			"pull_request", number, newETag,
		); err != nil {
			slog.Warn("persist pull request ETag failed",
				"repo", repo.Owner+"/"+repo.Name,
				"number", number,
				"err", err,
			)
		}
	}
	return nil
}

// preservePlatformBaseSHAIfOmitted keeps the stored base SHA when the
// list endpoint omits it but the head is unchanged. GitLab list
// payloads carry no diff_refs, so a bare list-driven upsert would
// blank the base SHA that a prior detail sync recorded — invalidating
// the reviewed diff snapshot (diff_base_sha != platform_base_sha) and
// dropping reviewed_head_sha back to 409 head_unknown.
func preservePlatformBaseSHAIfOmitted(
	normalized *db.MergeRequest,
	existing *db.MergeRequest,
) {
	if normalized == nil || existing == nil {
		return
	}
	if normalized.PlatformBaseSHA != "" || existing.PlatformBaseSHA == "" {
		return
	}
	if normalized.PlatformHeadSHA == "" ||
		existing.PlatformHeadSHA == "" ||
		normalized.PlatformHeadSHA != existing.PlatformHeadSHA {
		return
	}
	normalized.PlatformBaseSHA = existing.PlatformBaseSHA
}

func preserveMergeableStateIfOmitted(
	normalized *db.MergeRequest,
	existing *db.MergeRequest,
) {
	if normalized == nil || existing == nil {
		return
	}
	if normalized.PlatformHeadSHA == "" ||
		existing.PlatformHeadSHA == "" ||
		normalized.PlatformHeadSHA != existing.PlatformHeadSHA {
		return
	}
	if normalized.PlatformBaseSHA == "" ||
		existing.PlatformBaseSHA == "" ||
		normalized.PlatformBaseSHA != existing.PlatformBaseSHA {
		return
	}
	if normalized.MergeableState == "" ||
		(normalized.MergeableState == "unknown" && existing.MergeableState != "") {
		normalized.MergeableState = existing.MergeableState
		normalized.MergeableStateObservedAt = existing.MergeableStateObservedAt
	}
}

func preserveMergedAtIfOmitted(
	normalized *db.MergeRequest,
	existing *db.MergeRequest,
) {
	if normalized.State == db.MergeRequestStateMerged &&
		normalized.MergedAt == nil && existing.MergedAt != nil {
		mergedAt := *existing.MergedAt
		normalized.MergedAt = &mergedAt
	}
}

func preserveReviewDecisionIfOmitted(
	normalized *db.MergeRequest,
	existing *db.MergeRequest,
) {
	if normalized == nil || existing == nil {
		return
	}
	if normalized.ReviewDecision != "" {
		return
	}
	if normalized.PlatformHeadSHA != "" &&
		existing.PlatformHeadSHA != "" &&
		normalized.PlatformHeadSHA != existing.PlatformHeadSHA {
		return
	}
	normalized.ReviewDecision = existing.ReviewDecision
	normalized.ReviewDecisionObservedAt = existing.ReviewDecisionObservedAt
}

func preserveCIStateIfOmitted(
	normalized *db.MergeRequest,
	existing *db.MergeRequest,
) bool {
	if normalized == nil || existing == nil {
		return false
	}
	if normalized.PlatformHeadSHA == "" ||
		existing.PlatformHeadSHA == "" ||
		normalized.PlatformHeadSHA != existing.PlatformHeadSHA {
		return false
	}
	ciStatusOmitted := normalized.CIStatus == ""
	ciStatusChanged := !ciStatusOmitted &&
		normalized.CIStatus != existing.CIStatus
	if normalized.CIStatus == "" {
		normalized.CIStatus = existing.CIStatus
		normalized.CIObservedAt = existing.CIObservedAt
	}
	if normalized.CIChecksJSON == "" && !ciStatusChanged {
		normalized.CIChecksJSON = existing.CIChecksJSON
	}
	return ciStatusChanged && normalized.CIChecksJSON == ""
}

// clearUnknownMergeableStateTime drops the observation time of an empty or
// unknown mergeable state: the provider either omitted the field or had not
// computed it. Empty review decisions and CI keep their times, because the
// paths that stamp them do so only from authoritative, complete responses.
func clearUnknownMergeableStateTime(mr *db.MergeRequest) {
	if mr.MergeableState == "" || mr.MergeableState == "unknown" {
		mr.MergeableStateObservedAt = nil
	}
}

// CarryMergeRequestDerivedFields copies sync-derived columns a provider
// snapshot cannot represent — comment count, review decision, and CI state —
// from the stored row onto normalized, so committing a fetched or
// mutation-returned snapshot does not erase them. CI status and checks are
// head-derived and carried only while the head is unchanged (see the
// SHA-sensitive sync rules). The ci_had_pending flag and the detail-fetched
// marker are owned by the snapshot upsert itself (the stored flag always
// wins, and a set marker is never cleared by a snapshot), so this helper
// deliberately leaves them alone.
func CarryMergeRequestDerivedFields(normalized, existing *db.MergeRequest) {
	if normalized == nil || existing == nil {
		return
	}
	normalized.CommentCount = existing.CommentCount
	normalized.ReviewDecision = existing.ReviewDecision
	normalized.ReviewDecisionObservedAt = existing.ReviewDecisionObservedAt
	carryCIStateOnSameHead(normalized, existing)
}

// carryCIStateOnSameHead copies the stored CI status, checks, and CI
// observation time onto normalized while the head is unchanged. CI state is
// head-derived, so a new head keeps the snapshot's own value.
func carryCIStateOnSameHead(normalized, existing *db.MergeRequest) {
	if normalized == nil || existing == nil ||
		!strings.EqualFold(normalized.PlatformHeadSHA, existing.PlatformHeadSHA) {
		return
	}
	normalized.CIStatus = existing.CIStatus
	normalized.CIChecksJSON = existing.CIChecksJSON
	normalized.CIObservedAt = existing.CIObservedAt
}
