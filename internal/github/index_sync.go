package github

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"go.kenn.io/forge/internal/platformdb"
	"go.kenn.io/forge/internal/telemetry"

	gh "github.com/google/go-github/v93/github"
	"go.kenn.io/forge/internal/db"
	"go.kenn.io/forge/internal/workspace"
	"go.kenn.io/forge/platform"
	platformgithub "go.kenn.io/forge/platform/github"
)

// indexSyncRepo performs the cheap index scan: list endpoints only,
// upserting basic data without detail fetches. This runs every cycle.
func (s *Syncer) indexSyncRepo(
	ctx context.Context,
	repo RepoRef,
	repoID int64,
	cloneFetchOK bool,
) error {
	// List fetches count as discovery; item work after them counts as detail.
	detailCtx := WithSyncSpendKind(ctx, SyncSpendDetail)
	caps, err := s.clients.Capabilities(repoPlatform(repo), repoHost(repo))
	if err != nil {
		return fmt.Errorf("resolve provider capabilities for %s/%s: %w", repo.Owner, repo.Name, err)
	}
	gitHubClient, hasGitHubClient := s.optionalGitHubClientFor(repo)
	platformRef := platformRepoRef(repo)

	// If the previous sync of this repo partially failed after the
	// ETag cache was populated by a 200 list response, a naive next
	// cycle would see 304 and skip the per-item upserts that failed
	// last time, leaving the DB stale until the TTL expired. Evict
	// the repo's list ETags so the following calls are
	// unconditional, forcing a fresh 200 that we can re-apply.
	priorFail := s.consumeRepoFailed(repo)
	forceMR := priorFail&failMR != 0
	forceIssues := priorFail&failIssues != 0
	if priorFail != 0 {
		var endpoints []string
		if forceMR {
			endpoints = append(endpoints, "pulls")
		}
		if forceIssues {
			endpoints = append(endpoints, "issues")
		}
		if hasGitHubClient {
			gitHubClient.InvalidateListETagsForRepo(repo.Owner, repo.Name, endpoints...)
		}
	}

	// Track partial-failure and disabled-feature signals per path so
	// disabled repository features never enter transient recovery.
	var attemptedScope failScope
	var failedScope failScope
	var disabledScope failScope
	// Failures caused by the local budget refusing a list fetch before
	// any wire attempt. They fail the cycle but must not evict list
	// ETags: nothing upstream was touched, so the cached validators are
	// still correct and eviction would only add unconditional-refetch
	// spend to an already-exhausted window.
	var budgetRefusedScope failScope
	var partialCause error

	preferNativeStacks := s.preferGitHubNativeStacks.Load() &&
		repoPlatform(repo) == platform.KindGitHub
	var nativeStackHints map[int]*platformgithub.NativeStackHint

	prListUnchanged := false
	var mrProbe repositoryFeatureProbe
	mrProbeDue := false
	if caps.ReadMergeRequests {
		mrProbe, mrProbeDue = s.beginRepositoryFeatureProbe(
			ctx, repo, platform.RepositoryFeatureMergeRequests,
		)
	}
	if caps.ReadMergeRequests && mrProbeDue {
		mrProviderAttempted := false
		defer func() {
			if mrProviderAttempted {
				mrProbe.release()
			} else {
				mrProbe.abandon()
			}
		}()
		attemptedScope |= failMR
		mrReader, err := s.mergeRequestReaderFor(repo)
		if err != nil {
			s.observeSync(ctx, repo, telemetry.SyncSourceMergeRequests, err)
			return fmt.Errorf("resolve merge request reader for %s/%s: %w", repo.Owner, repo.Name, err)
		}
		mrProviderAttempted = true
		var openMRs []platform.MergeRequest
		// Discovery of new and closed PRs rides the essential budget
		// reserve so optional background spend cannot starve it.
		listCtx := WithEssentialSyncBudget(ctx)
		listRequestedAt := s.nowUTC()
		if nativeReader, ok := mrReader.(interface {
			ListOpenMergeRequestsWithNativeStackHints(
				context.Context, platform.RepoRef,
			) ([]platform.MergeRequest, map[int]*platformgithub.NativeStackHint, error)
		}); preferNativeStacks && ok {
			openMRs, nativeStackHints, err = nativeReader.ListOpenMergeRequestsWithNativeStackHints(listCtx, platformRef)
		} else {
			openMRs, err = mrReader.ListOpenMergeRequests(listCtx, platformRef)
		}
		s.observeSync(ctx, repo, telemetry.SyncSourceMergeRequests, err)
		mrListBlocked := false
		if err != nil {
			// 304 Not Modified means the open-PR list is byte-identical
			// to the previous fetch. No PR opened, no PR closed, no
			// metadata on any open PR changed. Skip per-PR upserts and
			// closure detection — both ran on the previous sync that
			// produced the cached etag.
			if platformgithub.IsNotModified(err) {
				prListUnchanged = true
			} else if errors.Is(err, platform.ErrSyncBudgetExhausted) {
				// The local budget refused the request before any wire
				// attempt, so the cached list validators are still
				// correct. Marking the repo failed would evict them and
				// force an unconditional refetch next cycle — extra
				// spend exactly when the budget is already exhausted.
				return fmt.Errorf("list open PRs: %w", err)
			} else if s.recordRepositoryFeatureDisabled(
				repo, platform.RepositoryFeatureMergeRequests, err,
			) {
				disabledScope |= failMR
				mrListBlocked = true
			} else {
				s.markRepoFailed(repo, failMR)
				// The queue read does not depend on the pull list, so a
				// failing list must not freeze queue state.
				s.refreshQueuedPullRequests(detailCtx, repo, repoID)
				return fmt.Errorf("list open PRs: %w", err)
			}
		}

		if prListUnchanged {
			// The open list is unchanged, but repair work is independent
			// of that ETag and must continue advancing on every healthy MR
			// sync cycle. The detail drain handles CI updates for PRs with
			// pending checks via priority scoring.
			s.reconcileMergedActorEvents(detailCtx, repo, repoID)
		} else if !mrListBlocked {
			// GraphQL path: if fetcher available and not rate-limited,
			// do a bulk fetch that replaces both index upsert and
			// detail drain for complete PRs. For large repos that
			// already have indexed rows, keep the refresh incremental:
			// the list phase updates timestamps and the detail drain
			// conditionally fetches individual stale PRs.
			graphQLDone := false
			if fetcher := s.fetcherForContext(ctx, repo); fetcher != nil &&
				s.shouldUseBulkGraphQLForMRs(ctx, repo, repoID, len(openMRs)) {
				if s.graphQLReadAllowed(ctx, repo, fetcher) {
					bulkRequestedAt := s.nowUTC()
					priorRevisions, snapshotErr := s.db.SnapshotRevisions(ctx, repoID, db.ArchiveItemTypeMergeRequest)
					result, gqlErr := func() (*RepoBulkResult, error) {
						if snapshotErr != nil {
							return nil, snapshotErr
						}
						return fetcher.FetchRepoPRs(
							detailCtx, repo.Owner, repo.Name, preferNativeStacks,
						)
					}()
					if gqlErr != nil {
						if s.recordRepositoryFeatureDisabled(
							repo, platform.RepositoryFeatureMergeRequests, gqlErr,
						) {
							disabledScope |= failMR
							graphQLDone = true
						} else if syncBudgetWait(gqlErr) {
							partialCause = retainSyncFailureCause(partialCause, gqlErr) // REST indexing below still covers the scope
						} else {
							slog.Warn("GraphQL fetch failed, falling back to REST index",
								"repo", repo.Owner+"/"+repo.Name,
								"err", gqlErr,
							)
						}
					} else {
						// Only a query that asked for the preview fields can
						// speak for stack membership. A GraphQL fallback that
						// dropped them must leave REST-derived hints intact.
						if preferNativeStacks && result.NativeStacksQueried {
							nativeStackHints = nativeStackHintsFromBulk(result)
						}
						if err := s.doSyncRepoGraphQL(
							detailCtx, repo, repoID, priorRevisions, result, bulkRequestedAt, cloneFetchOK,
						); err != nil {
							if s.recordRepositoryFeatureDisabled(
								repo, platform.RepositoryFeatureMergeRequests, err,
							) {
								disabledScope |= failMR
								failedScope |= partialSyncFailureScope(err) & failMR
							} else {
								failedScope |= failMR
							}
							if failedScope&failMR != 0 {
								partialCause = retainSyncFailureCause(partialCause, err)
							}
						}
						graphQLDone = true
					}
				}
			}

			if !graphQLDone {
				if err := s.syncMergeRequestsFromList(
					detailCtx, mrReader, repo, repoID, openMRs, listRequestedAt, cloneFetchOK,
				); err != nil {
					if s.recordRepositoryFeatureDisabled(
						repo, platform.RepositoryFeatureMergeRequests, err,
					) {
						disabledScope |= failMR
						failedScope |= partialSyncFailureScope(err) & failMR
					} else {
						logSyncFailure(ctx, slog.LevelError, "merge request sync failed", err,
							"repo", repo.Owner+"/"+repo.Name,
						)
						failedScope |= failMR
					}
					if failedScope&failMR != 0 {
						partialCause = retainSyncFailureCause(partialCause, err)
					}
				}
			}
		}
	}
	if preferNativeStacks && failedScope&failMR == 0 {
		nativeResult := s.refreshGitHubNativeStackCache(
			ctx, repo, repoID, nativeStackHints, prListUnchanged,
		)
		s.nativeStackResults.Store(repoFailKey(repo), nativeResult)
	}
	// The queue and merge rules reads do not depend on the pull list
	// succeeding, so a partial pull failure still observes them. Both wait
	// out the pull request feature probe and its cooldown.
	if repoPlatform(repo) == platform.KindGitHub && attemptedScope&failMR != 0 &&
		disabledScope&failMR == 0 {
		s.refreshQueuedPullRequests(detailCtx, repo, repoID)
		s.refreshRepoMergeRules(detailCtx, repo, repoID)
	}

	// Index issues — ETag-gated, with GraphQL when available.
	// Same structure as PR sync: REST list first (ETag gate),
	// then GraphQL if available, REST fallback if not.
	issueListUnchanged := false
	var issueProbe repositoryFeatureProbe
	issueProbeDue := false
	if caps.ReadIssues {
		issueProbe, issueProbeDue = s.beginRepositoryFeatureProbe(
			ctx, repo, platform.RepositoryFeatureIssues,
		)
	}
	if caps.ReadIssues && issueProbeDue {
		issueProviderAttempted := false
		defer func() {
			if issueProviderAttempted {
				issueProbe.release()
			} else {
				issueProbe.abandon()
			}
		}()
		attemptedScope |= failIssues
		issueReader, err := s.issueReaderFor(repo)
		if err != nil {
			slog.Error("resolve issue reader failed",
				"repo", repo.Owner+"/"+repo.Name,
				"err", err,
			)
			failedScope |= failIssues
			if failedScope != 0 {
				s.markRepoFailed(repo, failedScope)
			}
			s.observeSync(ctx, repo, telemetry.SyncSourceIssues, err)
			return joinPartialFailureCause(
				partialCause,
				fmt.Errorf("resolve issue reader for %s/%s: %w", repo.Owner, repo.Name, err),
			)
		}
		issueProviderAttempted = true

		var openIssues []platform.Issue
		var ghIssues []*gh.Issue
		_, useGitHubIssuePath := issueReader.(interface {
			ListOpenGitHubIssues(context.Context, platform.RepoRef) ([]*gh.Issue, error)
		})
		var issueListErr error
		// Same essential-reserve treatment as the open-PR list.
		issueListCtx := WithEssentialSyncBudget(ctx)
		if rawIssueReader, ok := issueReader.(interface {
			ListOpenGitHubIssues(context.Context, platform.RepoRef) ([]*gh.Issue, error)
		}); ok && hasGitHubClient {
			// Keep GitHub's ETag-gated bulk path so index sync retains the
			// provider-only fields used by per-item detail refreshes. The raw
			// reader performs the same contract checks before persistence.
			ghIssues, issueListErr = rawIssueReader.ListOpenGitHubIssues(issueListCtx, platformRef)
		} else {
			openIssues, issueListErr = issueReader.ListOpenIssues(issueListCtx, platformRef)
		}
		s.observeSync(ctx, repo, telemetry.SyncSourceIssues, issueListErr)
		if issueListErr != nil {
			if platformgithub.IsNotModified(issueListErr) {
				// 304: open issue list unchanged, skip.
				issueListUnchanged = true
			} else if errors.Is(issueListErr, platform.ErrSyncBudgetExhausted) {
				failedScope |= failIssues
				budgetRefusedScope |= failIssues
				partialCause = retainSyncFailureCause(partialCause, issueListErr)
			} else if s.recordRepositoryFeatureDisabled(
				repo, platform.RepositoryFeatureIssues, issueListErr,
			) {
				disabledScope |= failIssues
			} else {
				slog.Error("list open issues failed",
					"repo", repo.Owner+"/"+repo.Name,
					"err", issueListErr,
				)
				failedScope |= failIssues
				partialCause = retainSyncFailureCause(partialCause, issueListErr)
			}
		} else {
			graphQLIssuesDone := false
			if fetcher := s.fetcherForContext(ctx, repo); fetcher != nil &&
				s.shouldUseBulkGraphQLForIssues(ctx, repo, repoID, len(openIssues)+len(ghIssues)) {
				if s.graphQLReadAllowed(ctx, repo, fetcher) {
					priorRevisions, snapshotErr := s.db.SnapshotRevisions(ctx, repoID, db.ArchiveItemTypeIssue)
					issueResult, gqlErr := func() (*RepoBulkResult, error) {
						if snapshotErr != nil {
							return nil, snapshotErr
						}
						return fetcher.FetchRepoIssues(
							detailCtx, repo.Owner, repo.Name,
						)
					}()
					if gqlErr != nil {
						if s.recordRepositoryFeatureDisabled(
							repo, platform.RepositoryFeatureIssues, gqlErr,
						) {
							disabledScope |= failIssues
							graphQLIssuesDone = true
						} else if syncBudgetWait(gqlErr) {
							partialCause = retainSyncFailureCause(partialCause, gqlErr) // REST indexing below still covers the scope
						} else {
							slog.Warn("GraphQL issue fetch failed, falling back to REST",
								"repo", repo.Owner+"/"+repo.Name,
								"err", gqlErr,
							)
						}
					} else {
						if err := s.doSyncRepoGraphQLIssues(
							detailCtx, repo, repoID, priorRevisions, issueResult,
						); err != nil {
							if s.recordRepositoryFeatureDisabled(
								repo, platform.RepositoryFeatureIssues, err,
							) {
								disabledScope |= failIssues
								failedScope |= partialSyncFailureScope(err) & failIssues
							} else {
								failedScope |= failIssues
							}
							if failedScope&failIssues != 0 {
								partialCause = retainSyncFailureCause(partialCause, err)
							}
						}
						graphQLIssuesDone = true
					}
				}
			}

			if !graphQLIssuesDone {
				if useGitHubIssuePath && hasGitHubClient {
					if err := s.syncIssuesFromList(
						detailCtx, gitHubClient, repo, repoID, ghIssues, forceIssues,
					); err != nil {
						if s.recordRepositoryFeatureDisabled(
							repo, platform.RepositoryFeatureIssues, err,
						) {
							disabledScope |= failIssues
							failedScope |= partialSyncFailureScope(err) & failIssues
						} else {
							logSyncFailure(ctx, slog.LevelError, "REST issue sync failed", err,
								"repo", repo.Owner+"/"+repo.Name,
							)
							failedScope |= failIssues
						}
						if failedScope&failIssues != 0 {
							partialCause = retainSyncFailureCause(partialCause, err)
						}
					}
				} else {
					if err := s.syncPlatformIssuesFromList(
						detailCtx, issueReader, repo, repoID, openIssues, forceIssues,
					); err != nil {
						if s.recordRepositoryFeatureDisabled(
							repo, platform.RepositoryFeatureIssues, err,
						) {
							disabledScope |= failIssues
							failedScope |= partialSyncFailureScope(err) & failIssues
						} else {
							logSyncFailure(ctx, slog.LevelError, "issue sync failed", err,
								"repo", repo.Owner+"/"+repo.Name,
							)
							failedScope |= failIssues
						}
						if failedScope&failIssues != 0 {
							partialCause = retainSyncFailureCause(partialCause, err)
						}
					}
				}
			}
		}
	}
	if attemptedScope&failIssues != 0 &&
		failedScope&failIssues == 0 && disabledScope&failIssues == 0 {
		issueProbe.clear()
	}
	if attemptedScope&failMR != 0 &&
		failedScope&failMR == 0 && disabledScope&failMR == 0 {
		mrProbe.clear()
	}

	if evictScope := failedScope &^ budgetRefusedScope; evictScope != 0 {
		// One or more per-item steps failed. Record which paths
		// failed so the next cycle forces an unconditional refetch
		// only for the affected list endpoints. Budget-refused list
		// fetches are excluded: their cached validators are intact.
		s.markRepoFailed(repo, evictScope)
	}
	succeededScope := attemptedScope &^ failedScope &^ disabledScope
	if succeededScope != 0 {
		s.clearRepoFailedScope(repo, succeededScope)
	}

	if caps.ReadMergeRequests && prListUnchanged &&
		failedScope&failMR == 0 && disabledScope&failMR == 0 {
		s.queueRepoPRComments(ctx, repo)
	}
	if caps.ReadIssues && issueListUnchanged &&
		failedScope&failIssues == 0 && disabledScope&failIssues == 0 {
		s.queueRepoIssueComments(ctx, repo)
	}

	if failedScope != 0 || partialCause != nil {
		// Surface the partially-failed cycle to the caller so repo
		// sync health (last_sync_error, sync status) records it;
		// without this, per-item failures such as a transferred or
		// inaccessible item look like a clean sync from the API. The
		// typed partial error lets consumers that depend only on an
		// unaffected scope proceed.
		return &PartialSyncError{
			MergeRequests: failedScope&failMR != 0,
			Issues:        failedScope&failIssues != 0,
			Cause:         partialCause,
		}
	}

	return nil
}

func (s *Syncer) syncMergeRequestsFromList(
	ctx context.Context,
	reader platform.MergeRequestReader,
	repo RepoRef,
	repoID int64,
	mrs []platform.MergeRequest,
	requestedAt time.Time,
	cloneFetchOK bool,
) error {
	stillOpen := make(map[int]bool, len(mrs))
	for _, mr := range mrs {
		stillOpen[mr.Number] = true
	}

	var hadItemFailure bool
	var failureCause error
	progress := newMergeRequestSyncProgressLogger(repo, "provider", len(mrs))
	for i, mr := range mrs {
		if err := s.indexUpsertMergeRequest(ctx, repo, repoID, mr, requestedAt, cloneFetchOK); err != nil {
			if errors.Is(err, platform.ErrRepositoryFeatureDisabled) {
				return preservePartialSyncFailure(
					failMR, err, failureCause,
				)
			}
			failureCause = retainSyncFailureCause(failureCause, err)
			logSyncFailure(ctx, slog.LevelError, "index upsert MR failed", err,
				"repo", repo.Owner+"/"+repo.Name,
				"number", mr.Number,
			)
			hadItemFailure = true
		}
		progress.record(i + 1)
	}

	closedNumbers, err := s.db.GetPreviouslyOpenMRNumbers(
		ctx, repoID, stillOpen,
	)
	if err != nil {
		s.markRepoFailed(repo, failMR)
		return joinPartialFailureCause(
			failureCause, fmt.Errorf("get previously open MRs: %w", err),
		)
	}
	for _, number := range closedNumbers {
		if err := s.fetchAndUpdateClosedMergeRequest(
			ctx, reader, repo, repoID, number, cloneFetchOK,
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
			hadItemFailure = true
		}
	}

	s.reconcileMergedActorEvents(ctx, repo, repoID)

	if hadItemFailure {
		return partialItemFailureError(
			"one or more merge request sync items failed", failureCause,
		)
	}
	progress.done()
	return nil
}

func (s *Syncer) shouldUseBulkGraphQLForMRs(
	ctx context.Context,
	repo RepoRef,
	repoID int64,
	listCount int,
) bool {
	localOpenCount, err := s.db.CountOpenMergeRequestsForRepo(ctx, repoID)
	if err != nil {
		slog.Warn("count existing merge requests before GraphQL bulk fetch failed",
			"repo", repo.Owner+"/"+repo.Name,
			"err", err,
		)
		return true
	}
	if localOpenCount < largeRepoBulkGraphQLThreshold {
		return true
	}
	slog.Info("skipping GraphQL merge request bulk fetch for large existing repo",
		"repo", repo.Owner+"/"+repo.Name,
		"platform", repoPlatform(repo),
		"host", repoHost(repo),
		"local_open_total", localOpenCount,
		"fetched_total", listCount,
	)
	return false
}

func (s *Syncer) shouldUseBulkGraphQLForIssues(
	ctx context.Context,
	repo RepoRef,
	repoID int64,
	listCount int,
) bool {
	localOpenCount, err := s.db.CountOpenIssuesForRepo(ctx, repoID)
	if err != nil {
		slog.Warn("count existing issues before GraphQL bulk fetch failed",
			"repo", repo.Owner+"/"+repo.Name,
			"err", err,
		)
		return true
	}
	if localOpenCount < largeRepoBulkGraphQLThreshold {
		return true
	}
	slog.Info("skipping GraphQL issue bulk fetch for large existing repo",
		"repo", repo.Owner+"/"+repo.Name,
		"platform", repoPlatform(repo),
		"host", repoHost(repo),
		"local_open_total", localOpenCount,
		"fetched_total", listCount,
	)
	return false
}

func (s *Syncer) indexUpsertMergeRequest(
	ctx context.Context,
	repo RepoRef,
	repoID int64,
	mr platform.MergeRequest,
	requestedAt time.Time,
	cloneFetchOK bool,
) error {
	normalized := platformdb.DBMergeRequest(repoID, mr)
	normalized.MergeableStateObservedAt = &requestedAt

	if err := s.db.RecordLiveMergeRequestPresence(ctx, normalized); err != nil {
		return err
	}

	existing, err := s.db.GetMergeRequestByRepoIDAndNumber(
		ctx, repoID, mr.Number,
	)
	if err != nil {
		return fmt.Errorf(
			"get existing MR #%d: %w", mr.Number, err,
		)
	}

	// Preserve fields list endpoints commonly omit. The comment count is
	// owned by the comment sync: list counts are absent (GitHub) or count
	// different notes (GitLab), and writing them would make it flap.
	needsCIDetailRefresh := false
	if existing != nil {
		normalized.CommentCount = existing.CommentCount
		if !mr.AdditionsKnown {
			normalized.Additions = existing.Additions
		}
		if !mr.DeletionsKnown {
			normalized.Deletions = existing.Deletions
		}
		preservePlatformBaseSHAIfOmitted(normalized, existing)
		preserveReviewDecisionIfOmitted(normalized, existing)
		preserveMergeableStateIfOmitted(normalized, existing)
		needsCIDetailRefresh = preserveCIStateIfOmitted(normalized, existing)
	}

	if normalized.Author != "" &&
		normalized.AuthorDisplayName == "" {
		if client, ok := s.optionalGitHubClientFor(repo); ok {
			if name, found := s.resolveDisplayName(
				ctx, client, repo, normalized.Author,
			); found {
				normalized.AuthorDisplayName = name
			}
		}
		if normalized.AuthorDisplayName == "" && existing != nil {
			normalized.AuthorDisplayName = existing.AuthorDisplayName
		}
	}

	mrID, revision, accepted, err := s.CommitMergeRequestParentSnapshot(ctx, repo, normalized)
	if err != nil {
		return fmt.Errorf(
			"upsert MR #%d: %w", mr.Number, err,
		)
	}
	if !accepted {
		return nil
	}

	if needsCIDetailRefresh {
		detailCleared, err := s.db.ClearMRDetailFetchedSnapshot(ctx, mrID, revision)
		if err != nil {
			return fmt.Errorf(
				"clear detail fetch marker for MR #%d: %w",
				mr.Number, err,
			)
		}
		if !detailCleared {
			return nil
		}
	}
	if _, err := s.persistMergedActorEvent(ctx, mrID, revision, mr.MergedBy, normalized.MergedAt); err != nil {
		return fmt.Errorf("persist merged lifecycle event for MR #%d: %w", mr.Number, err)
	}

	if err := s.db.EnsureKanbanState(ctx, mrID); err != nil {
		return fmt.Errorf(
			"ensure kanban state for MR #%d: %w",
			mr.Number, err,
		)
	}

	// Record the reviewed diff snapshot so head-binding providers can
	// pin mutations after a plain list sync — without it the head
	// stays unreviewable and head-bound actions 409 with head_unknown.
	// Skipped when the stored snapshot already covers this head/base
	// pair: recomputing the merge-base for every open MR on every
	// cycle would add unbounded git work to large repos. Non-fatal
	// like the bulk GitHub path: a missed snapshot only delays
	// head-bound actions until the next successful sync.
	snapshotCurrent := existing != nil &&
		existing.DiffHeadSHA == normalized.PlatformHeadSHA &&
		existing.DiffBaseSHA == normalized.PlatformBaseSHA &&
		existing.MergeBaseSHA != ""
	if s.clones != nil && cloneFetchOK && !snapshotCurrent {
		if err := s.syncProviderMRDiff(
			ctx, repo, repoID, mrID, revision, mr.Number,
			normalized, false,
		); err != nil {
			if errors.Is(err, errParentSnapshotAdvanced) {
				return nil
			}
			logSyncFailure(ctx, slog.LevelWarn, "provider diff snapshot failed", err,
				"repo", repo.Owner+"/"+repo.Name,
				"number", mr.Number,
			)
		}
	}

	if existing != nil &&
		existing.DetailFetchedAt != nil &&
		existing.UpdatedAt.Equal(normalized.UpdatedAt) {
		s.queuePRCommentSync(repo, existing.RepoID, existing.Number, existing.DetailFetchedAt)
	}

	return nil
}

// reclassifyWorkspaceHeadRepoTrust recomputes and persists the head-repo
// trust classification for a workspace tracking this merge request, after a
// sync upsert may have changed head_repo_clone_url. It is called only from
// CommitMergeRequestParentSnapshot, so it runs for every MR-upsert path in
// this file (REST list sync, GraphQL bulk fetch, detail refresh, and
// closed-MR refresh) rather than just one of them. Without this fan-out, a
// workspace created (or last refreshed) while a PR was same-repo never
// learns that the PR's head was later retargeted to a fork until another
// workspace setup, retry, or agent-context refresh. Reads the just-upserted
// MR row rather than the incoming provider snapshot so a list sync that
// reports an unknown head (platform.MergeRequest.HeadRepoCloneURLUnknown)
// cannot downgrade a classification the stored row already resolved. The
// workspace lookup runs first and is a cheap no-op for the common case of no
// tracking workspace, since this now runs on every accepted MR upsert in
// every sync cycle. Failures are logged, not returned: like other non-fatal
// per-item persistence steps in this sync path (for example
// syncProviderMRDiff), a missed reclassification only delays the workspace
// catching up, so it must not fail the sync.
func (s *Syncer) reclassifyWorkspaceHeadRepoTrust(
	ctx context.Context, repo RepoRef, repoID int64, mrNumber int,
) {
	ws, err := s.db.GetWorkspaceByMRForProvider(
		ctx, string(repoPlatform(repo)), repoHost(repo), repo.Owner, repo.Name, mrNumber,
	)
	if err != nil {
		slog.Error("look up workspace for head-repo trust reclassification failed",
			"repo", repo.Owner+"/"+repo.Name,
			"number", mrNumber, "err", err,
		)
		return
	}
	if ws == nil {
		return
	}

	for {
		stored, err := s.db.GetMergeRequestByRepoIDAndNumber(
			ctx, repoID, mrNumber,
		)
		if err != nil {
			slog.Error("look up stored merge request for head-repo trust reclassification failed",
				"repo", repo.Owner+"/"+repo.Name,
				"number", mrNumber, "err", err,
			)
			return
		}
		cloneURL := ""
		var snapshotRevision int64
		removed, visibilityErr := s.db.IsArchiveItemRemovedUpstream(
			ctx, repoID, db.ArchiveItemTypeMergeRequest, mrNumber,
		)
		if visibilityErr != nil {
			slog.Error("check workspace merge request visibility for head-repo trust reclassification failed",
				"repo", repo.Owner+"/"+repo.Name,
				"number", mrNumber, "err", visibilityErr,
			)
			return
		}
		if stored != nil {
			snapshotRevision = stored.SnapshotRevision
			if !removed {
				cloneURL = stored.HeadRepoCloneURL
			}
			if !removed && stored.HeadRepoIdentityStale {
				return
			}
		}

		refreshed := workspace.WorkspaceHeadRepo(
			ws.Platform, ws.PlatformHost, ws.RepoOwner, ws.RepoName, cloneURL,
		)
		if s.afterHeadRepoSnapshotRead != nil {
			s.afterHeadRepoSnapshotRead()
		}
		applied, updateErr := s.db.UpdateWorkspaceMRHeadRepoForSnapshot(
			ctx,
			ws.ID,
			repoID,
			mrNumber,
			snapshotRevision,
			removed,
			refreshed,
		)
		if updateErr != nil {
			slog.Error("persist reclassified workspace head-repo trust failed",
				"workspace_id", ws.ID,
				"repo", repo.Owner+"/"+repo.Name,
				"number", mrNumber, "err", updateErr,
			)
			return
		}
		if applied {
			return
		}
		if ctx.Err() != nil {
			return
		}
	}
}

// indexUpsertMR upserts a PR from list endpoint data only. No
// GetPullRequest, no timeline, no CI. Preserves fields that the
// list endpoint does not return (additions, deletions,
// mergeable_state, cached CI) from the existing DB row.
func (s *Syncer) indexUpsertMR(
	ctx context.Context,
	client Client,
	repo RepoRef,
	repoID int64,
	ghPR *gh.PullRequest,
	requestedAt time.Time,
) error {
	normalized, err := NormalizePR(repoID, ghPR)
	if err != nil {
		return fmt.Errorf("normalize MR #%d: %w", ghPR.GetNumber(), err)
	}
	normalized.MergeableStateObservedAt = &requestedAt

	existing, err := s.db.GetMergeRequestByRepoIDAndNumber(
		ctx, repoID, ghPR.GetNumber(),
	)
	if err != nil {
		return fmt.Errorf(
			"get existing MR #%d: %w", ghPR.GetNumber(), err,
		)
	}

	// Preserve fields the list endpoint doesn't return.
	needsCIDetailRefresh := false
	if existing != nil {
		normalized.CommentCount = existing.CommentCount
		normalized.Additions = existing.Additions
		normalized.Deletions = existing.Deletions
		preservePlatformBaseSHAIfOmitted(normalized, existing)
		preserveReviewDecisionIfOmitted(normalized, existing)
		preserveMergeableStateIfOmitted(normalized, existing)
		needsCIDetailRefresh = preserveCIStateIfOmitted(normalized, existing)
	}

	if normalized.Author != "" &&
		normalized.AuthorDisplayName == "" {
		if name, ok := s.resolveDisplayName(
			ctx, client, repo, normalized.Author,
		); ok {
			normalized.AuthorDisplayName = name
		} else if existing != nil {
			normalized.AuthorDisplayName = existing.AuthorDisplayName
		}
	}

	mrID, revision, accepted, err := s.CommitMergeRequestParentSnapshot(ctx, repo, normalized)
	if err != nil {
		return fmt.Errorf(
			"upsert MR #%d: %w", ghPR.GetNumber(), err,
		)
	}
	if !accepted {
		return nil
	}
	if needsCIDetailRefresh {
		detailCleared, err := s.db.ClearMRDetailFetchedSnapshot(ctx, mrID, revision)
		if err != nil {
			return fmt.Errorf(
				"clear detail fetch marker for MR #%d: %w",
				ghPR.GetNumber(), err,
			)
		}
		if !detailCleared {
			return nil
		}
	}
	if _, err := s.persistMergedTransitionEvent(ctx, mrID, revision, ghPR, normalized.MergedAt); err != nil {
		return fmt.Errorf("persist merged lifecycle event for MR #%d: %w", ghPR.GetNumber(), err)
	}

	if err := s.db.EnsureKanbanState(ctx, mrID); err != nil {
		return fmt.Errorf(
			"ensure kanban state for MR #%d: %w",
			ghPR.GetNumber(), err,
		)
	}

	if existing != nil &&
		existing.DetailFetchedAt != nil &&
		existing.UpdatedAt.Equal(normalized.UpdatedAt) {
		s.queuePRCommentSync(repo, existing.RepoID, existing.Number, existing.DetailFetchedAt)
	}

	return nil
}
