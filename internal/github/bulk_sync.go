package github

import (
	"context"
	"encoding/json/v2"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"go.kenn.io/forge/internal/platformdb"
	"go.kenn.io/forge/internal/telemetry"

	"go.kenn.io/forge/internal/db"
	"go.kenn.io/forge/platform"
)

// doSyncRepoGraphQL processes bulk GraphQL results for a repo.
func (s *Syncer) doSyncRepoGraphQL(
	ctx context.Context,
	repo RepoRef,
	repoID int64,
	priorRevisions map[int]int64,
	result *RepoBulkResult,
	requestedAt time.Time,
	cloneFetchOK bool,
) error {
	var failedScope failScope
	var failureCause error
	stillOpen := make(map[int]bool, len(result.PullRequests))
	progress := newMergeRequestSyncProgressLogger(repo, "graphql", len(result.PullRequests))

	for i := range result.PullRequests {
		bulk := &result.PullRequests[i]
		number := bulk.PR.GetNumber()
		stillOpen[number] = true

		if err := s.syncOpenMRFromBulk(
			ctx, repo, repoID, priorRevisions, requestedAt, bulk, cloneFetchOK,
		); err != nil {
			if errors.Is(err, platform.ErrRepositoryFeatureDisabled) {
				return preservePartialSyncFailure(
					failMR, err, failureCause,
				)
			}
			failureCause = retainSyncFailureCause(failureCause, err)
			slog.Error("GraphQL sync MR failed",
				"repo", repo.Owner+"/"+repo.Name,
				"number", number,
				"err", err,
			)
			failedScope |= failMR
		}
		progress.record(i + 1)
	}

	// Detect closed PRs — same as REST path.
	closedNumbers, err := s.db.GetPreviouslyOpenMRNumbers(
		ctx, repoID, stillOpen,
	)
	if err != nil {
		return joinPartialFailureCause(
			failureCause, fmt.Errorf("get previously open MRs: %w", err),
		)
	}
	for _, number := range closedNumbers {
		if err := s.fetchAndUpdateClosed(
			ctx, repo, repoID, number, cloneFetchOK,
		); err != nil {
			if errors.Is(err, platform.ErrRepositoryFeatureDisabled) {
				return preservePartialSyncFailure(
					failMR, err, failureCause,
				)
			}
			failureCause = retainSyncFailureCause(failureCause, err)
			logSyncFailure(ctx, slog.LevelError, "update closed MR failed", err,
				"repo", repo.Owner+"/"+repo.Name,
				"number", number,
			)
			failedScope |= failMR
		}
	}

	s.reconcileMergedActorEvents(ctx, repo, repoID)

	if failedScope != 0 {
		return partialItemFailureError("GraphQL sync had partial failures", failureCause)
	}
	progress.done()
	return nil
}

// doSyncRepoGraphQLIssues processes bulk GraphQL results for issues.
func (s *Syncer) doSyncRepoGraphQLIssues(
	ctx context.Context,
	repo RepoRef,
	repoID int64,
	priorRevisions map[int]int64,
	result *RepoBulkResult,
) error {
	var failedScope failScope
	var failureCause error
	stillOpen := make(map[int]bool, len(result.Issues))
	progress := newIssueSyncProgressLogger(repo, "graphql", len(result.Issues))

	for i := range result.Issues {
		bulk := &result.Issues[i]
		number := bulk.Issue.GetNumber()
		stillOpen[number] = true

		if err := s.syncOpenIssueFromBulk(
			ctx, repo, repoID, priorRevisions, bulk,
		); err != nil {
			if disabledErr := repositoryFeatureDisabledError(
				repo, platform.RepositoryFeatureIssues, err,
			); disabledErr != nil {
				return preservePartialSyncFailure(
					failIssues, disabledErr, failureCause,
				)
			}
			failureCause = retainSyncFailureCause(failureCause, err)
			logSyncFailure(ctx, slog.LevelError, "GraphQL sync issue failed", err,
				"repo", repo.Owner+"/"+repo.Name,
				"number", number,
			)
			failedScope |= failIssues
		}
		progress.record(i + 1)
	}

	// Detect closed issues — same as REST path.
	closedNumbers, err := s.db.GetPreviouslyOpenIssueNumbers(
		ctx, repoID, stillOpen,
	)
	if err != nil {
		return joinPartialFailureCause(
			failureCause, fmt.Errorf("get previously open issues: %w", err),
		)
	}
	for _, number := range closedNumbers {
		if err := s.fetchAndUpdateClosedIssue(
			ctx, repo, repoID, number,
		); err != nil {
			if disabledErr := repositoryFeatureDisabledError(
				repo, platform.RepositoryFeatureIssues, err,
			); disabledErr != nil {
				return preservePartialSyncFailure(
					failIssues, disabledErr, failureCause,
				)
			}
			failureCause = retainSyncFailureCause(failureCause, err)
			logSyncFailure(ctx, slog.LevelError, "update closed issue failed", err,
				"repo", repo.Owner+"/"+repo.Name,
				"number", number,
			)
			failedScope |= failIssues
		}
	}

	if failedScope != 0 {
		return partialItemFailureError("GraphQL issue sync had partial failures", failureCause)
	}
	progress.done()
	return nil
}

// syncOpenIssueFromBulk processes a single issue from GraphQL bulk
// results. Uses pre-fetched data instead of per-issue REST calls.
func (s *Syncer) syncOpenIssueFromBulk(
	ctx context.Context,
	repo RepoRef,
	repoID int64,
	priorRevisions map[int]int64,
	bulk *BulkIssue,
) error {
	number := bulk.Issue.GetNumber()
	normalized, err := NormalizeIssue(repoID, bulk.Issue)
	if err != nil {
		return fmt.Errorf("normalize issue #%d: %w", number, err)
	}

	// Preserve derived fields that NormalizeIssue doesn't populate
	// from bulk data. Without this, upsert overwrites them with
	// zero values.
	existing, err := s.db.GetIssueByRepoIDAndNumber(
		ctx, repoID, number,
	)
	if err != nil {
		return fmt.Errorf(
			"get existing issue #%d: %w", number, err,
		)
	}
	if existing != nil {
		// Only preserve DetailFetchedAt when timeline data is complete.
		// When incomplete, clear it so the detail drain re-queues
		// this issue if the REST fallback fails.
		if bulk.CommentsComplete && bulk.TimelineComplete {
			normalized.DetailFetchedAt = existing.DetailFetchedAt
		}
		// CommentCount comes from GraphQL Comments.TotalCount via
		// adaptIssue, so trust the fresh GraphQL value.
	}

	issueID, revision, accepted, err := s.commitIssueParentSnapshot(ctx, normalized)
	if err != nil {
		return fmt.Errorf("upsert issue #%d: %w", number, err)
	}
	if !accepted {
		return nil
	}
	commentsCurrent := revision == priorRevisions[number]+1

	// UpsertIssue uses COALESCE to preserve existing detail_fetched_at,
	// so passing nil doesn't clear it. When comments are incomplete,
	// explicitly clear it so the detail drain re-queues this issue
	// if the REST fallback fails.
	if !commentsCurrent || !bulk.CommentsComplete || !bulk.TimelineComplete {
		var detailCleared bool
		detailCleared, err = s.db.ClearIssueDetailFetchedSnapshot(ctx, issueID, revision)
		if err != nil {
			return fmt.Errorf(
				"clear detail_fetched_at for issue #%d: %w", number, err,
			)
		}
		if !detailCleared {
			return nil
		}
	}

	if !commentsCurrent {
		return nil
	}

	if bulk.CommentsComplete && bulk.TimelineComplete {
		derived := db.IssueDerivedFields{
			CommentCount:   normalized.CommentCount,
			LastActivityAt: computeIssueCommentLastActivity(bulk.Issue, bulk.Comments),
		}
		applied, err := s.replaceIssueCommentEvents(
			ctx, repo, number, issueID, revision, bulk.Comments,
			normalizeIssueTimelineEvents(issueID, bulk.TimelineEvents), &derived,
			bulk.CommentVisibility,
		)
		if err != nil {
			return fmt.Errorf(
				"replace issue comment events for #%d: %w", number, err,
			)
		}
		if !applied {
			return nil
		}
		// Mark detail as fetched so the detail drain doesn't
		// re-queue this issue for REST detail fetches.
		detailApplied, err := s.db.MarkIssueDetailFetchedSnapshot(ctx, issueID, revision)
		if err != nil {
			slog.Warn("mark GraphQL issue detail fetched failed",
				"repo", repo.Owner+"/"+repo.Name,
				"number", number, "err", err,
			)
		}
		if !detailApplied {
			return nil
		}
	} else {
		// Timeline data truncated — fall back to detail fetch.
		if err := s.refreshIssueTimeline(
			ctx, repo, issueID, revision, bulk.Issue, bulk.CommentVisibility,
		); err != nil {
			if errors.Is(err, errParentSnapshotAdvanced) {
				return nil
			}
			return fmt.Errorf(
				"refresh timeline for issue #%d: %w", number, err,
			)
		}
		// REST fallback succeeded — mark detail as fetched.
		detailApplied, err := s.db.MarkIssueDetailFetchedSnapshot(ctx, issueID, revision)
		if err != nil {
			slog.Warn("mark issue detail fetched after REST fallback failed",
				"repo", repo.Owner+"/"+repo.Name,
				"number", number, "err", err,
			)
		}
		if !detailApplied {
			return nil
		}
	}

	return nil
}

// syncOpenMRFromBulk processes a single PR from GraphQL bulk
// results. It performs the same operations as fetchMRDetail but
// using pre-fetched data instead of per-PR REST calls.
func (s *Syncer) syncOpenMRFromBulk(
	ctx context.Context,
	repo RepoRef,
	repoID int64,
	priorRevisions map[int]int64,
	requestedAt time.Time,
	bulk *BulkPR,
	cloneFetchOK bool,
) error {
	number := bulk.PR.GetNumber()
	normalized, err := NormalizePR(repoID, bulk.PR)
	if err != nil {
		return fmt.Errorf("normalize MR #%d: %w", number, err)
	}
	normalized.MergeableStateObservedAt = &requestedAt

	if err := s.db.RecordLiveMergeRequestPresence(ctx, normalized); err != nil {
		return err
	}

	// Preserve derived fields that NormalizePR doesn't populate.
	// Without this, upsert overwrites them with zero values; if
	// nested connections are truncated the later allComplete guard
	// skips restoring them and correct data is lost.
	existing, err := s.db.GetMergeRequestByRepoIDAndNumber(
		ctx, repoID, number,
	)
	if err != nil {
		return fmt.Errorf(
			"get existing MR #%d: %w", number, err,
		)
	}
	headChanged := existing != nil &&
		existing.PlatformHeadSHA != normalized.PlatformHeadSHA
	if existing != nil {
		normalized.CommentCount = existing.CommentCount
		normalized.ReviewDecision = existing.ReviewDecision
		normalized.ReviewDecisionObservedAt = existing.ReviewDecisionObservedAt
		// CI is tied to the head SHA. If the head moved we must clear
		// the previous values; otherwise an incomplete bulk CI fetch
		// (CIComplete=false skips the UpdateMRCIStatus write below)
		// would leave stale checks attached to the new commit.
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

	// Resolve the review decision independent of nested-connection
	// pagination. GitHub's reviewDecision scalar is provider-authoritative:
	// it is computed over the PR's entire review history, so a truncated
	// reviews/comments/commits/CI page on this fetch must not gate it. Apply
	// it to the parent snapshot whenever the provider supplies it. Only when
	// the provider reports no decision (null/empty enum) do we fall back to
	// deriving from this fetch's reviews, and only when that reviews
	// connection is complete; otherwise the previously persisted decision is
	// retained (review history is additive).
	if decision, authoritative := mapGraphQLReviewDecision(bulk.ReviewDecision); authoritative {
		normalized.ReviewDecision = decision
		normalized.ReviewDecisionObservedAt = &requestedAt
	} else if bulk.ReviewsComplete && len(bulk.Reviews) > 0 {
		normalized.ReviewDecision = DeriveReviewDecision(bulk.Reviews)
		normalized.ReviewDecisionObservedAt = &requestedAt
	} else if bulk.ReviewsComplete && normalized.ReviewDecision == "" {
		// A complete, empty review history with no provider decision
		// confirms that no decision exists (repositories that do not
		// require reviews). A stored decision is left alone because
		// review history is additive.
		normalized.ReviewDecisionObservedAt = &requestedAt
	}
	// The child snapshot below writes the review decision again; it takes
	// this resolved pair, not whatever the parent commit leaves in normalized.
	reviewDecision := normalized.ReviewDecision
	reviewDecisionObservedAt := normalized.ReviewDecisionObservedAt

	// Resolve display name if missing.
	if normalized.Author != "" &&
		normalized.AuthorDisplayName == "" {
		client, clientErr := s.clientFor(repo)
		if clientErr == nil {
			if name, ok := s.resolveDisplayName(
				ctx, client, repo, normalized.Author,
			); ok {
				normalized.AuthorDisplayName = name
			}
		}
	}

	mrID, revision, accepted, err := s.CommitMergeRequestParentSnapshot(ctx, repo, normalized)
	if err != nil {
		return fmt.Errorf("upsert MR #%d: %w", number, err)
	}
	if !accepted {
		return nil
	}
	commentsCurrent := revision == priorRevisions[number]+1
	if !commentsCurrent {
		currentBulk := *bulk
		currentBulk.CommentsComplete = false
		currentBulk.Comments = nil
		currentBulk.ReviewThreadsComplete = false
		currentBulk.ReviewThreads = nil
		bulk = &currentBulk
	}
	if !bulk.CommentsComplete || !bulk.ReviewsComplete ||
		!bulk.ReviewThreadsComplete || !bulk.CommitsComplete ||
		!bulk.TimelineComplete || !bulk.CIComplete {
		detailCleared, err := s.db.ClearMRDetailFetchedSnapshot(ctx, mrID, revision)
		if err != nil {
			return fmt.Errorf("clear detail fetch marker for MR #%d: %w", number, err)
		}
		if !detailCleared {
			return nil
		}
	}

	// UpsertMergeRequest preserves ci_had_pending across upserts, so
	// the head-changed reset above doesn't actually persist that field
	// without an explicit clear. Drop the stale CI state here so it
	// doesn't outlive the old commit.
	if headChanged {
		ciCleared, err := s.db.ClearMRCISnapshot(
			ctx, mrID, revision, normalized.PlatformHeadSHA,
		)
		if err != nil {
			return fmt.Errorf(
				"clear stale CI for MR #%d: %w", number, err,
			)
		}
		if !ciCleared {
			return nil
		}
	}

	if err := s.db.EnsureKanbanState(ctx, mrID); err != nil {
		return fmt.Errorf(
			"ensure kanban state for MR #%d: %w", number, err,
		)
	}

	// Diff SHAs.
	repoHost := repo.PlatformHost
	if repoHost == "" {
		repoHost = "github.com"
	}
	if s.clones != nil && cloneFetchOK {
		headSHA := normalized.PlatformHeadSHA
		baseSHA := normalized.PlatformBaseSHA
		if headSHA != "" && baseSHA != "" {
			mb, mbErr := s.clones.MergeBase(
				ctx, string(repoPlatform(repo)), repoHost, repo.Owner,
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
					return nil
				}
			}
		}
	}

	// Commit complete archival families atomically under the page's epoch.
	comments := make([]db.MREvent, 0, len(bulk.Comments))
	reviews := make([]db.MREvent, 0, len(bulk.Reviews))
	var events []db.MREvent
	commitOrderer, err := s.commitOrderAssigner(ctx, mrID)
	if err != nil {
		return fmt.Errorf("load commit order for MR #%d: %w", number, err)
	}
	for _, c := range bulk.Comments {
		comments = append(comments, NormalizeCommentEventWithVisibility(
			mrID, c, bulk.CommentVisibility[c.GetID()],
		))
	}
	for _, r := range bulk.Reviews {
		reviews = append(reviews, NormalizeReviewEvent(mrID, r))
	}
	for i, c := range bulk.Commits {
		event := NormalizeCommitEvent(mrID, c)
		commitOrderer.apply(&event, i+1)
		events = append(events, event)
	}
	for _, timelineEvent := range bulk.TimelineEvents {
		if event := NormalizeTimelineEvent(mrID, timelineEvent); event != nil {
			events = append(events, *event)
		}
	}
	events, err = s.filterDuplicateMergedLifecycleEvents(ctx, mrID, events)
	if err != nil {
		return fmt.Errorf("dedupe merged lifecycle events for MR #%d: %w", number, err)
	}
	bulkAllComplete := bulk.CommentsComplete &&
		bulk.ReviewsComplete &&
		bulk.ReviewThreadsComplete &&
		bulk.CommitsComplete &&
		bulk.TimelineComplete &&
		bulk.CIComplete
	var derived *db.MRDerivedFields
	if bulk.CommentsComplete {
		fields := db.MRDerivedFields{
			ReviewDecision:           reviewDecision,
			ReviewDecisionObservedAt: reviewDecisionObservedAt,
			CommentCount:             len(bulk.Comments),
		}
		derived = &fields
	}
	var inline []db.MREvent
	var reviewThreads []db.MRReviewThread
	if bulk.ReviewThreadsComplete {
		inline, reviewThreads = platformdb.DBReviewThreads(bulk.ReviewThreads)
	}
	if bulk.CommentsComplete || bulk.ReviewsComplete || bulk.ReviewThreadsComplete || len(events) > 0 {
		applied, err := s.commitMergeRequestDatasets(
			ctx, repo, mrID, number, revision,
			comments, bulk.CommentsComplete,
			reviews,
			inline, reviewThreads, bulk.ReviewThreadsComplete, events, derived,
			livenessHeadForRound(normalized, nil),
		)
		if err != nil {
			return fmt.Errorf("commit archival events for MR #%d: %w", number, err)
		}
		if !applied {
			return nil
		}
	}
	allComplete := bulkAllComplete
	if _, err := s.persistMergedTransitionEvent(ctx, mrID, revision, bulk.PR, normalized.MergedAt); err != nil {
		return fmt.Errorf("persist merged lifecycle event for MR #%d: %w", number, err)
	}

	// CI status — only write if complete (don't write
	// truncated CI data that could hide failures).
	var ciChecks []db.CICheck
	var ciJSON []byte
	if bulk.CIComplete {
		s.observeSync(ctx, repo, telemetry.SyncSourceChecks, nil)
		ciChecks = normalizeBulkCI(bulk)
		if existing != nil && !headChanged {
			ciChecks, err = carryBlockingChecks(existing.CIChecksJSON, ciChecks)
			if err != nil {
				return fmt.Errorf("carry CI checks for MR #%d: %w", number, err)
			}
		}
		if ciChecks == nil {
			ciChecks = []db.CICheck{}
		}
		ciJSON, _ = json.Marshal(ciChecks)
		ciStatus := deriveCIStatusFromChecks(ciChecks)
		ciApplied, err := s.db.UpdateMergeRequestCISnapshot(
			ctx, mrID, revision, ciStatus, string(ciJSON),
			&requestedAt,
		)
		if err != nil {
			slog.Warn("update CI status failed",
				"repo", repo.Owner+"/"+repo.Name,
				"number", number, "err", err,
			)
		}
		if !ciApplied {
			return nil
		}
	}

	// Mark detail as fetched only when
	// ALL connections are complete. Incomplete PRs leave
	// DetailFetchedAt stale so the detail drain picks it up for a
	// full REST fetch. Derived fields are committed atomically with
	// their complete mirrored datasets above.
	if allComplete {
		pending := ciHasPending(string(ciJSON))
		detailApplied, err := s.db.MarkMergeRequestDetailFetchedSnapshot(ctx, mrID, revision, pending, nil)
		if err != nil {
			slog.Warn("mark GraphQL detail fetched failed",
				"repo", repo.Owner+"/"+repo.Name,
				"number", number, "err", err,
			)
		}
		if !detailApplied {
			return nil
		}
		// Bulk hydration marks the detail fresh, which postpones the
		// scheduled detail refresh that would otherwise check queue state.
		s.refreshMergeQueueAfterBulkDetail(ctx, repo, repoID, number)
		// Refresh workflow approval state so the DB-only detail GET
		// can render the Approve workflows button without a foreground
		// sync. GraphQL doesn't return action_required runs, so this
		// stays a one-extra REST call per fully-synced PR, gated by
		// the same per-host budget as the REST detail drain. The
		// sync-budget transport spends the actual REST call; this is
		// only the admission check.
		if s.canSpendWorkflowApprovalRefresh(repo) {
			approvalApplied, _ := s.refreshWorkflowApproval(
				ctx, repo, repoID, number,
				normalized.PlatformHeadSHA, bulk.PR, normalized,
			)
			if !approvalApplied {
				return nil
			}
		}
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

	return nil
}

// deriveCIStatusFromChecks computes the overall CI status from
// a []db.CICheck. Mirrors DeriveOverallCIStatus but works on the
// normalized CICheck format produced by normalizeBulkCI.
func deriveCIStatusFromChecks(checks []db.CICheck) string {
	if len(checks) == 0 {
		return ""
	}
	hasPending := false
	hasFailed := false
	for _, c := range checks {
		if c.Status != "completed" {
			hasPending = true
			continue
		}
		switch c.Conclusion {
		case "success", "neutral", "skipped":
			// OK
		default:
			if c.Conclusion != "" {
				hasFailed = true
			}
		}
	}
	if hasFailed {
		return "failure"
	}
	if hasPending {
		return "pending"
	}
	return "success"
}

// normalizeBulkCI converts GraphQL check runs and statuses to
// the db.CICheck slice format used by the rest of the codebase.
func normalizeBulkCI(bulk *BulkPR) []db.CICheck {
	return normalizeCIChecks(bulk.CheckRuns, bulk.Statuses)
}
