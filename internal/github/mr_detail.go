package github

import (
	"context"
	"encoding/json/v2"
	"errors"
	"fmt"
	"log/slog"

	"go.kenn.io/forge/internal/platformdb"
	"go.kenn.io/forge/internal/telemetry"

	gh "github.com/google/go-github/v93/github"
	"go.kenn.io/forge/internal/db"
	"go.kenn.io/forge/platform"
	platformgithub "go.kenn.io/forge/platform/github"
)

// fetchMRDetail performs a full detail fetch for a single MR:
// GetPullRequest, refreshTimeline, refreshCIStatus. Returns the
// number of API calls made.
func (s *Syncer) fetchMRDetail(
	ctx context.Context,
	repo RepoRef,
	repoID int64,
	number int,
	cloneFetchOK bool,
) (int, error) {
	removed, err := s.removedUpstreamForLiveSync(
		ctx, repoID, db.ArchiveItemTypeMergeRequest, number,
	)
	if err != nil {
		return 0, fmt.Errorf("check PR #%d visibility: %w", number, err)
	}
	if removed {
		return 0, nil
	}
	calls, err := s.fetchMRDetailWithRouteFence(
		ctx, repo, repoID, number, cloneFetchOK,
	)
	if errors.Is(err, db.ErrRepositoryIdentityChanged) {
		return calls, nil
	}
	s.refreshMergeQueueAfterDetailSync(ctx, repo, repoID, number, &err)
	s.refreshMergeBypassAfterDetailSync(ctx, repo, repoID, number, &err)
	return calls, err
}

func (s *Syncer) fetchMRDetailWithRouteFence(
	ctx context.Context,
	repo RepoRef,
	repoID int64,
	number int,
	cloneFetchOK bool,
) (int, error) {
	ctx = withCloneRepositoryIdentity(ctx, repo)
	calls := 0
	mrReader, err := s.mergeRequestReaderFor(repo)
	if err != nil {
		return calls, fmt.Errorf("resolve merge request reader for %s/%s: %w", repo.Owner, repo.Name, err)
	}
	if _, ok := mrReader.(interface {
		GetGitHubPullRequest(context.Context, platform.RepoRef, int) (*gh.PullRequest, platform.MergeRequest, error)
	}); !ok {
		return s.fetchProviderMRDetail(
			ctx, mrReader, repo, repoID, number,
		)
	}

	client, err := s.clientFor(repo)
	if err != nil {
		return calls, fmt.Errorf("resolve client for %s/%s: %w", repo.Owner, repo.Name, err)
	}
	existing, err := s.db.GetMergeRequestByRepoIDAndNumber(
		ctx, repoID, number,
	)
	if err != nil {
		return calls, fmt.Errorf(
			"get existing MR #%d: %w", number, err,
		)
	}

	requestedAt := s.nowUTC()
	fullPR, newETag, notModified, err := s.getPullRequestForDetail(
		ctx, client, repo, number,
	)
	calls++
	// Route fetch failures and detected transfers through the canonical
	// lookup classification so removed, inaccessible, and moved items
	// surface typed outcomes instead of generic upstream failures.
	if provider, ok := mrReader.(*platformgithub.Provider); ok {
		if outcomeErr := provider.MergeRequestLookupOutcomeError(
			ctx, platformRepoRef(repo), number, fullPR, err,
		); outcomeErr != nil {
			return calls, fmt.Errorf("get full PR #%d: %w", number, outcomeErr)
		}
	}
	if err == nil && fullPR == nil {
		if notModified && existing != nil {
			return s.markUnchangedMRDetailFetched(
				ctx, repo, repoID, number, existing, calls,
			)
		}
		err = errors.New("client returned nil pull request")
	}
	if err != nil {
		return calls, fmt.Errorf(
			"get full PR #%d: %w", number, err,
		)
	}
	normalized, err := NormalizePR(repoID, fullPR)
	if err != nil {
		return calls, fmt.Errorf("normalize full PR #%d: %w", number, err)
	}
	normalized.MergeableStateObservedAt = &requestedAt
	preserveMergeableStateIfOmitted(normalized, existing)
	// The REST pull request carries no review decision or CI state. Keep the
	// stored values until the timeline and CI refreshes below re-observe
	// them, so a failed refresh does not leave them cleared.
	CarryMergeRequestDerivedFields(normalized, existing)

	if normalized.Author != "" &&
		normalized.AuthorDisplayName == "" {
		if name, ok := s.resolveDisplayName(
			ctx, client, repo, normalized.Author,
		); ok {
			normalized.AuthorDisplayName = name
		}
		calls++ // GetUser
	}

	mrID, revision, accepted, err := s.CommitMergeRequestParentSnapshot(
		ctx, repo, normalized,
	)
	if err != nil {
		return calls, fmt.Errorf(
			"upsert MR #%d: %w", number, err,
		)
	}
	if !accepted {
		return calls, nil
	}

	if err := s.db.EnsureKanbanState(ctx, mrID); err != nil {
		if errors.Is(err, db.ErrRepositoryIdentityChanged) {
			return calls, nil
		}
		return calls, fmt.Errorf(
			"ensure kanban state for MR #%d: %w", number, err,
		)
	}

	// Diff SHAs if clone available.
	cloneRepoHost := repo.PlatformHost
	if cloneRepoHost == "" {
		cloneRepoHost = "github.com"
	}
	if s.clones != nil && cloneFetchOK {
		headSHA := normalized.PlatformHeadSHA
		baseSHA := normalized.PlatformBaseSHA
		if headSHA != "" && baseSHA != "" {
			mb, mbErr := s.clones.MergeBase(
				ctx, string(repoPlatform(repo)), cloneRepoHost, repo.Owner,
				repo.Name, baseSHA, headSHA,
			)
			if mbErr != nil {
				slog.Warn("merge-base computation failed",
					"repo", repo.Owner+"/"+repo.Name,
					"number", number, "err", mbErr,
				)
			} else {
				diffApplied, dbErr := s.db.UpdateDiffSHAsSnapshot(
					ctx, mrID, revision, headSHA, baseSHA,
					headSHA, baseSHA, mb,
				)
				if dbErr != nil {
					slog.Warn("update diff SHAs failed",
						"repo", repo.Owner+"/"+repo.Name,
						"number", number, "err", dbErr,
					)
				} else if !diffApplied {
					return calls, nil
				}
			}
		}
	}

	if err := s.refreshTimeline(
		ctx, repo, mrID, revision, number,
		livenessHeadForRound(normalized, existing),
	); err != nil {
		// Timeline = 4 base calls (comments + reviews + commits + force-push);
		// provider review-thread sync is handled inside refreshTimeline.
		calls += 4
		if errors.Is(err, errParentSnapshotAdvanced) {
			return calls, nil
		}
		return calls, err
	}
	calls += 4
	if _, err := s.persistMergedTransitionEvent(ctx, mrID, revision, fullPR, normalized.MergedAt); err != nil {
		return calls, fmt.Errorf("persist merged lifecycle event for MR #%d: %w", number, err)
	}

	ciHeadSHA := ""
	if fullPR.GetHead() != nil {
		ciHeadSHA = fullPR.GetHead().GetSHA()
	}
	ciApplied, err := s.refreshCIStatusSnapshot(
		ctx, repo, mrID, revision, number, ciHeadSHA,
	)
	if err != nil {
		// CI = 2 calls (combined status + check runs).
		calls += 2
		return calls, err
	}
	calls += 2
	if !ciApplied {
		return calls, nil
	}

	// Refresh workflow approval state so the DB-only detail GET
	// can render the Approve workflows button without a foreground
	// sync. Same path as syncMRForRepo, but the budgeted detail
	// drain needs to count this call too.
	approvalApplied, approvalCalls := s.refreshWorkflowApproval(
		ctx, repo, repoID, number, ciHeadSHA, fullPR, normalized,
	)
	calls += approvalCalls
	if !approvalApplied {
		return calls, nil
	}

	// Determine whether CI had pending checks for scoring by
	// reading the DB row that refreshCIStatus just wrote. Use
	// ciHasPending (checks individual statuses) rather than the
	// aggregate CIStatus, which becomes "failure" when any check
	// fails even if others are still running.
	pending := false
	freshMR, freshErr := s.db.GetMergeRequestByRepoIDAndNumber(
		ctx, repoID, number,
	)
	if freshErr == nil && freshMR != nil {
		pending = ciHasPending(freshMR.CIChecksJSON)
	}

	detailApplied, err := s.db.MarkMergeRequestDetailFetchedSnapshot(
		ctx, mrID, revision, pending, nil,
	)
	if err != nil {
		return calls, fmt.Errorf(
			"mark detail fetched for MR #%d: %w", number, err,
		)
	}
	if !detailApplied {
		return calls, nil
	}

	// Fire onMRSynced hook.
	if s.onMRSynced != nil {
		fresh, fErr := s.db.GetMergeRequestByRepoIDAndNumber(
			ctx, repoID, number,
		)
		if fErr != nil {
			slog.Warn("get MR for onMRSynced hook failed",
				"repo", repo.Owner+"/"+repo.Name,
				"number", number, "err", fErr,
			)
		} else {
			s.onMRSynced(repo.Owner, repo.Name, fresh)
		}
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

	return calls, nil
}

func (s *Syncer) getPullRequestForDetail(
	ctx context.Context,
	client Client,
	repo RepoRef,
	number int,
) (*gh.PullRequest, string, bool, error) {
	conditional, ok := client.(conditionalPullRequestGetter)
	if !ok {
		pr, err := client.GetPullRequest(ctx, repo.Owner, repo.Name, number)
		return pr, "", false, err
	}

	etag, err := s.db.GetHTTPEtag(
		ctx, string(repoPlatform(repo)), repoHost(repo),
		repo.Owner, repo.Name, "pull_request", number,
	)
	if err != nil {
		slog.Warn("load pull request ETag failed",
			"repo", repo.Owner+"/"+repo.Name,
			"number", number,
			"err", err,
		)
		pr, err := client.GetPullRequest(ctx, repo.Owner, repo.Name, number)
		return pr, "", false, err
	}
	return conditional.GetPullRequestIfChanged(
		ctx, repo.Owner, repo.Name, number, etag,
	)
}

func (s *Syncer) markUnchangedMRDetailFetched(
	ctx context.Context,
	repo RepoRef,
	repoID int64,
	number int,
	existing *db.MergeRequest,
	calls int,
) (int, error) {
	// A parent 304 does not cover edited/deleted comments. Once a detail
	// check is admitted, check comments too before advancing freshness.
	if existing.State == "open" {
		client, err := s.clientFor(repo)
		if err != nil {
			return calls, err
		}
		comments, err := s.listCommentsForRefresh(ctx, client, repo, number, existing.CommentCount)
		calls++
		if err != nil && !platformgithub.IsNotModified(err) {
			return calls, err
		}
		if err == nil {
			if err := s.persistPRComments(ctx, repo, existing, comments); err != nil {
				client.InvalidateListETagsForRepo(repo.Owner, repo.Name, "comments")
				return calls, err
			}
		}
	}
	if err := s.refreshStoredPRCommentVisibility(
		ctx, repo, existing.ID, existing.SnapshotRevision, number,
	); err != nil {
		if errors.Is(err, errParentSnapshotAdvanced) ||
			errors.Is(err, db.ErrRepositoryIdentityChanged) {
			return calls, nil
		}
		return calls, err
	}
	if fetcher := s.fetcherForContext(ctx, repo); fetcher != nil && s.graphQLReadAllowed(ctx, repo, fetcher) {
		threadCalls, err := s.syncProviderMRReviewThreads(
			ctx, repo, existing.ID, number, existing.SnapshotRevision, true,
		)
		calls += threadCalls
		if err != nil {
			if errors.Is(err, errParentSnapshotAdvanced) {
				return calls, nil
			}
			return calls, err
		}
	}
	pending := existing.CIHadPending
	if existing.CIHadPending && existing.PlatformHeadSHA != "" {
		ciApplied, err := s.refreshCIStatusSnapshot(
			ctx, repo, existing.ID, existing.SnapshotRevision,
			number, existing.PlatformHeadSHA,
		)
		if err != nil {
			calls += 2
			return calls, err
		}
		calls += 2
		if !ciApplied {
			return calls, nil
		}
		fresh, err := s.db.GetMergeRequestByRepoIDAndNumber(ctx, repoID, number)
		if err == nil && fresh != nil {
			pending = ciHasPending(fresh.CIChecksJSON)
		}
	}
	// Unchanged rounds have no dataset commit of their own, so commit
	// liveness travels with the detail-fetched marker under the same
	// revision guard instead. The round is unchanged, so no state
	// transition is possible here.
	metadataUpdates := s.computeCommitLiveness(
		ctx, repo, existing.ID, livenessHeadForRound(existing, existing), nil,
	)
	detailApplied, err := s.db.MarkMergeRequestDetailFetchedSnapshot(
		ctx, existing.ID, existing.SnapshotRevision, pending,
		metadataUpdates,
	)
	if err != nil {
		return calls, fmt.Errorf("mark unchanged detail fetched for MR #%d: %w", number, err)
	}
	if !detailApplied {
		return calls, nil
	}
	s.notifyMRDetailSynced(ctx, repo, repoID, number)
	return calls, nil
}

func (s *Syncer) fetchProviderMRDetail(
	ctx context.Context,
	reader platform.MergeRequestReader,
	repo RepoRef,
	repoID int64,
	number int,
) (int, error) {
	calls := 0
	mrReader, err := s.mergeRequestReaderFor(repo)
	if err != nil {
		return calls, fmt.Errorf("resolve merge request reader for %s/%s: %w", repo.Owner, repo.Name, err)
	}
	requestedAt := s.nowUTC()
	mr, err := mrReader.GetMergeRequest(ctx, platformRepoRef(repo), number)
	calls++
	if err != nil {
		return calls, fmt.Errorf(
			"get full MR #%d: %w", number,
			s.classifyProviderItemLookupError(ctx, repo, err),
		)
	}

	normalized := platformdb.DBMergeRequest(repoID, mr)
	normalized.MergeableStateObservedAt = &requestedAt
	existing, err := s.db.GetMergeRequestByRepoIDAndNumber(
		ctx, repoID, number,
	)
	if err != nil {
		return calls, fmt.Errorf(
			"get existing MR #%d: %w", number, err,
		)
	}
	preserveMergeableStateIfOmitted(normalized, existing)
	// The detail response carries at most a pipeline-only CI status without
	// checks. Keep the stored CI state until the CI-check fetch below
	// replaces it, so a failed or unsupported fetch does not overwrite it.
	carryCIStateOnSameHead(normalized, existing)

	mrID, revision, accepted, err := s.CommitMergeRequestParentSnapshot(
		ctx, repo, normalized,
	)
	if err != nil {
		return calls, fmt.Errorf(
			"upsert MR #%d: %w", number, err,
		)
	}
	if !accepted {
		return calls, nil
	}
	if err := s.db.EnsureKanbanState(ctx, mrID); err != nil {
		if errors.Is(err, db.ErrRepositoryIdentityChanged) {
			return calls, nil
		}
		return calls, fmt.Errorf(
			"ensure kanban state for MR #%d: %w", number, err,
		)
	}

	detailCalls, pending, err := s.syncProviderMRDetailExtras(
		ctx, reader, repo, mrID, number, revision, normalized.PlatformHeadSHA,
		livenessHeadForRound(normalized, existing),
	)
	calls += detailCalls
	if err != nil {
		if errors.Is(err, errParentSnapshotAdvanced) {
			return calls, nil
		}
		return calls, err
	}
	if _, err := s.persistMergedActorEvent(ctx, mrID, revision, mr.MergedBy, normalized.MergedAt); err != nil {
		return calls, fmt.Errorf("persist merged lifecycle event for MR #%d: %w", number, err)
	}

	detailApplied, err := s.db.MarkMergeRequestDetailFetchedSnapshot(
		ctx, mrID, revision, pending, nil,
	)
	if err != nil {
		return calls, fmt.Errorf("mark detail fetched for MR #%d: %w", number, err)
	}
	if !detailApplied {
		return calls, nil
	}

	if s.onMRSynced != nil {
		fresh, fErr := s.db.GetMergeRequestByRepoIDAndNumber(ctx, repoID, number)
		if fErr != nil {
			slog.Warn("get MR for onMRSynced hook failed",
				"repo", repo.Owner+"/"+repo.Name,
				"number", number, "err", fErr,
			)
		} else {
			s.onMRSynced(repo.Owner, repo.Name, fresh)
		}
	}

	return calls, nil
}

func (s *Syncer) syncProviderMRDetailExtras(
	ctx context.Context,
	reader platform.MergeRequestReader,
	repo RepoRef,
	mrID int64,
	number int,
	expectedRevision int64,
	headSHA string,
	livenessHeadSHA string,
) (int, bool, error) {
	calls := 0
	events, err := reader.ListMergeRequestEvents(ctx, platformRepoRef(repo), number)
	calls++
	if err != nil && !errors.Is(err, platform.ErrUnsupportedCapability) {
		return calls, false, fmt.Errorf("list MR events for #%d: %w", number, err)
	}
	if err == nil {
		dbEvents := make([]db.MREvent, 0, len(events))
		comments := make([]db.MREvent, 0, len(events))
		reviews := make([]db.MREvent, 0, len(events))
		commitOrderer, orderErr := s.commitOrderAssigner(ctx, mrID)
		if orderErr != nil {
			return calls, false, fmt.Errorf("load commit order for MR #%d: %w", number, orderErr)
		}
		commitListOrder := 0
		for _, event := range events {
			dbEvent := platformdb.DBMREvent(mrID, event)
			if dbEvent.EventType == "commit" {
				commitListOrder++
				commitOrderer.apply(&dbEvent, commitListOrder)
			}
			switch dbEvent.EventType {
			case "issue_comment":
				comments = append(comments, dbEvent)
			case "review":
				reviews = append(reviews, dbEvent)
			default:
				dbEvents = append(dbEvents, dbEvent)
			}
		}
		dbEvents, err = s.filterDuplicateMergedLifecycleEvents(ctx, mrID, dbEvents)
		if err != nil {
			return calls, false, fmt.Errorf("dedupe merged lifecycle events for MR #%d: %w", number, err)
		}
		applied, commitErr := s.commitMergeRequestDatasets(
			ctx, repo, mrID, number, expectedRevision,
			comments, true,
			reviews,
			nil, nil, false, dbEvents, nil,
			livenessHeadSHA,
		)
		if commitErr != nil {
			return calls, false, fmt.Errorf("replace provider MR events for #%d: %w", number, commitErr)
		}
		if !applied {
			return calls, false, errParentSnapshotAdvanced
		}
	}

	reviewThreadCalls, err := s.syncProviderMRReviewThreads(ctx, repo, mrID, number, expectedRevision, false)
	calls += reviewThreadCalls
	if err != nil {
		return calls, false, fmt.Errorf("sync review threads for MR #%d: %w", number, err)
	}

	pending := false
	if headSHA == "" {
		return calls, pending, nil
	}
	ciReader, err := s.ciReaderFor(repo)
	if err != nil {
		if errors.Is(err, platform.ErrUnsupportedCapability) {
			return calls, pending, nil
		}
		return calls, false, fmt.Errorf("resolve CI reader for %s/%s: %w", repo.Owner, repo.Name, err)
	}
	ciRequestedAt := s.nowUTC()
	checks, err := ciReader.ListCIChecks(ctx, platformRepoRef(repo), headSHA)
	calls++
	if errors.Is(err, platform.ErrUnsupportedCapability) {
		return calls, pending, nil
	}
	s.observeSync(ctx, repo, telemetry.SyncSourceChecks, err)
	if err != nil {
		return calls, false, fmt.Errorf("list CI checks for MR #%d: %w", number, err)
	}
	dbChecks := platformdb.DBCIChecks(checks)
	if dbChecks == nil {
		dbChecks = []db.CICheck{}
	}
	ciJSON, _ := json.Marshal(dbChecks)
	ciStatus := deriveCIStatusFromChecks(dbChecks)
	ciApplied, err := s.db.UpdateMergeRequestCISnapshot(
		ctx, mrID, expectedRevision, ciStatus, string(ciJSON),
		&ciRequestedAt,
	)
	if err != nil {
		return calls, false, fmt.Errorf("update CI status for MR #%d: %w", number, err)
	}
	if !ciApplied {
		return calls, false, errParentSnapshotAdvanced
	}
	pending = ciHasPending(string(ciJSON))
	return calls, pending, nil
}

func (s *Syncer) syncProviderMRReviewThreads(
	ctx context.Context,
	repo RepoRef,
	mrID int64,
	number int,
	expectedRevision int64,
	preserveOnReadFailure bool,
) (int, error) {
	caps, err := s.clients.Capabilities(repoPlatform(repo), repoHost(repo))
	if err != nil {
		if errors.Is(err, platform.ErrUnsupportedCapability) {
			return 0, nil
		}
		return 0, err
	}
	if !caps.ReadReviewThreads {
		return 0, nil
	}
	reader, err := s.clients.MergeRequestReviewThreadReader(repoPlatform(repo), repoHost(repo))
	if err != nil {
		return 0, err
	}
	threads, err := reader.ListMergeRequestReviewThreads(ctx, platformRepoRef(repo), number)
	calls := 1
	if err != nil {
		if preserveOnReadFailure {
			logSyncFailure(ctx, slog.LevelWarn, "current PR review-thread visibility fetch failed; preserving stored state", err,
				"repo", repo.Owner+"/"+repo.Name,
				"number", number,
			)
			return calls, nil
		}
		return calls, err
	}

	events, dbThreads := platformdb.DBReviewThreads(threads)
	applied, err := s.commitMergeRequestDatasets(
		ctx, repo, mrID, number, expectedRevision,
		nil, false, nil, events, dbThreads, true, nil, nil, "",
	)
	if err != nil {
		return calls, err
	}
	if !applied {
		return calls, errParentSnapshotAdvanced
	}
	return calls, nil
}

// refreshTimeline fetches comments, reviews, and commits for a PR and
// updates its derived fields (ReviewDecision, CommentCount, LastActivityAt, CIStatus).
func (s *Syncer) refreshTimeline(
	ctx context.Context,
	repo RepoRef,
	mrID int64,
	expectedRevision int64,
	number int,
	livenessHeadSHA string,
) error {
	client, err := s.clientFor(repo)
	if err != nil {
		return fmt.Errorf("resolve client for %s/%s: %w", repo.Owner, repo.Name, err)
	}

	comments, err := client.ListIssueComments(ctx, repo.Owner, repo.Name, number)
	if err != nil {
		return fmt.Errorf("list comments for MR #%d: %w", number, err)
	}

	reviewsRequestedAt := s.nowUTC()
	reviews, err := client.ListReviews(ctx, repo.Owner, repo.Name, number)
	if err != nil {
		return fmt.Errorf("list reviews for MR #%d: %w", number, err)
	}

	commits, err := client.ListCommits(ctx, repo.Owner, repo.Name, number)
	if err != nil {
		return fmt.Errorf("list commits for MR #%d: %w", number, err)
	}

	timelineEvents, err := client.ListPullRequestTimelineEvents(ctx, repo.Owner, repo.Name, number)
	if err != nil {
		if disabledErr := repositoryFeatureDisabledError(
			repo, platform.RepositoryFeatureMergeRequests, err,
		); disabledErr != nil {
			return disabledErr
		}
		logSyncFailure(ctx, slog.LevelWarn, "timeline event fetch failed during timeline refresh", err,
			"repo", repo.Owner+"/"+repo.Name,
			"number", number,
		)
		timelineEvents = nil
	}

	commentEvents := make([]db.MREvent, 0, len(comments))
	reviewEvents := make([]db.MREvent, 0, len(reviews))
	var events []db.MREvent
	commitOrderer, err := s.commitOrderAssigner(ctx, mrID)
	if err != nil {
		return fmt.Errorf("load commit order for MR #%d: %w", number, err)
	}
	commentVisibility, err := s.storedPRCommentVisibility(ctx, mrID)
	if err != nil {
		return fmt.Errorf("load stored comment visibility for MR #%d: %w", number, err)
	}
	decisionRequestedAt := s.nowUTC()
	observedVisibility, providerDecision, providerObserved := s.currentPRCommentVisibility(ctx, repo, number)
	if providerObserved {
		commentVisibility = observedVisibility
	}
	for _, c := range comments {
		commentEvents = append(commentEvents, NormalizeCommentEventWithVisibility(
			mrID, c, commentVisibility[c.GetID()],
		))
	}
	for _, r := range reviews {
		reviewEvents = append(reviewEvents, NormalizeReviewEvent(mrID, r))
	}
	for i, c := range commits {
		event := NormalizeCommitEvent(mrID, c)
		commitOrderer.apply(&event, i+1)
		events = append(events, event)
	}
	for _, timelineEvent := range timelineEvents {
		event := NormalizeTimelineEvent(mrID, timelineEvent)
		if event == nil {
			continue
		}
		events = append(events, *event)
	}
	events, err = s.filterDuplicateMergedLifecycleEvents(ctx, mrID, events)
	if err != nil {
		return fmt.Errorf("dedupe merged lifecycle events for MR #%d: %w", number, err)
	}

	// The review list does not show how many approvals the rules require, so
	// GitHub's own decision wins when the query returned one. A null decision
	// means GitHub enforces none, and the complete review list decides.
	reviewDecision, reviewDecisionObservedAt := DeriveReviewDecision(reviews), &reviewsRequestedAt
	if decision, authoritative := mapGraphQLReviewDecision(providerDecision); providerObserved && authoritative {
		reviewDecision, reviewDecisionObservedAt = decision, &decisionRequestedAt
	} else if !providerObserved {
		// Without GitHub's decision, only a requested change clears a
		// stored required review.
		storedDecision, storedDecisionObservedAt, err := s.db.GetMRReviewDecision(ctx, mrID)
		if err != nil {
			return fmt.Errorf("load review decision for MR #%d: %w", number, err)
		}
		if storedDecision == reviewDecisionRequired && reviewDecision != "changes_requested" {
			reviewDecision, reviewDecisionObservedAt = storedDecision, storedDecisionObservedAt
		}
	}
	derived := db.MRDerivedFields{
		ReviewDecision:           reviewDecision,
		ReviewDecisionObservedAt: reviewDecisionObservedAt,
		CommentCount:             len(comments),
	}
	applied, err := s.commitMergeRequestDatasets(
		ctx, repo, mrID, number, expectedRevision,
		commentEvents, true,
		reviewEvents,
		nil, nil, false, events, &derived,
		livenessHeadSHA,
	)
	if err != nil {
		return fmt.Errorf("commit comments and reviews for MR #%d: %w", number, err)
	}
	if !applied {
		return errParentSnapshotAdvanced
	}
	if _, err := s.syncProviderMRReviewThreads(ctx, repo, mrID, number, expectedRevision, false); err != nil {
		return fmt.Errorf("sync review threads for MR #%d: %w", number, err)
	}
	return nil
}
