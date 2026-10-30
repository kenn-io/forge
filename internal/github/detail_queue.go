package github

import (
	"context"
	"fmt"
	"log/slog"
	"slices"
	"time"

	"go.kenn.io/forge/internal/db"
	"go.kenn.io/forge/platform"
)

// --- Detail Drain ---

// countOverdueDetails reports remaining daily work even when a credential's
// budget or provider throttle kept its repository out of this pass.
func (s *Syncer) countOverdueDetails(ctx context.Context) int {
	now := time.Now()
	overdue := 0
	for _, item := range s.buildDetailQueueItems(ctx, s.TrackedRepos()) {
		if item.DetailFetchedAt == nil || now.Sub(*item.DetailFetchedAt) >= dailyRefetchInterval {
			overdue++
		}
	}
	return overdue
}

// drainDetailQueue builds a priority queue of items needing detail
// fetches and processes them within the per-provider/host budget.
func (s *Syncer) drainDetailQueue(
	ctx context.Context,
	eligibleBuckets map[string]bool,
	repos []RepoRef,
) {
	if len(s.budgets) == 0 {
		return
	}

	items := s.buildDetailQueueItems(ctx, repos)
	if len(items) == 0 {
		return
	}

	queue := BuildQueue(items, time.Now())
	if len(queue) == 0 {
		return
	}

	// A failed detail attempt stays overdue, but must not spend more budget
	// retrying through the comment-only pass in this same cycle.
	type detailAttempt struct {
		repoID int64
		number int
		kind   QueueItemType
	}
	attempted := make(map[detailAttempt]bool)
	defer func() {
		s.commentRefreshMu.Lock()
		defer s.commentRefreshMu.Unlock()
		s.pendingPRCommentSyncs = slices.DeleteFunc(s.pendingPRCommentSyncs, func(item queuedPRCommentSync) bool {
			return attempted[detailAttempt{item.repoID, item.number, QueueItemPR}]
		})
		s.pendingIssueCommentSyncs = slices.DeleteFunc(s.pendingIssueCommentSyncs, func(item queuedIssueCommentSync) bool {
			return attempted[detailAttempt{item.repoID, item.number, QueueItemIssue}]
		})
	}()

	// Track which hosts are exhausted so we skip quickly.
	exhausted := make(map[string]bool)
	repoResolutions := make(repoResolutionCache)

	for i := range queue {
		if ctx.Err() != nil {
			return
		}
		qi := &queue[i]
		repo := RepoRef{
			Owner: qi.RepoOwner, Name: qi.RepoName,
			Platform: qi.Platform, PlatformHost: qi.PlatformHost,
		}
		host := repoHost(repo)
		if tracked, ok := s.trackedRepoByIdentity(qi.Platform, qi.RepoOwner, qi.RepoName, host); ok {
			repo = tracked
			repo.Owner = qi.RepoOwner
			repo.Name = qi.RepoName
			repo.PlatformHost = host
		}
		// The queue was built before the pass ran, so it can hold items
		// for a repository the pass just published as archived. Detail
		// work is live syncing; archived repos hydrate through the
		// archive budget path instead.
		if repo.Archived {
			continue
		}
		// Resolve the credential bucket from the tracked repository. Routing
		// keys off the owner and host that survive tracking, not the raw
		// queue row, so this must follow the lookup above.
		bucket, err := s.bucketKeyForRepo(repo, false)
		if err != nil || !eligibleBuckets[bucket] {
			continue
		}
		feature := platform.RepositoryFeatureIssues
		if qi.Type == QueueItemPR {
			feature = platform.RepositoryFeatureMergeRequests
		}
		probe, due := s.beginRepositoryFeatureProbe(ctx, repo, feature)
		if !due {
			continue
		}
		if exhausted[bucket] {
			probe.abandon()
			continue
		}

		budget := s.budgets[bucket]
		if budget == nil {
			probe.abandon()
			continue
		}

		// Soft admission gate: check if the budget has nominal
		// capacity for this item. The transport layer handles
		// actual per-RoundTrip accounting; this prevents starting
		// work we almost certainly can't afford.
		worstCase := qi.WorstCaseCost()
		if !budget.CanSpend(worstCase) {
			probe.abandon()
			exhausted[bucket] = true
			continue
		}
		// The provider reserve is re-read per item alongside the local
		// ceiling. A drain can be many items long, so a credential that had
		// headroom when the drain started can reach its reserve partway
		// through.
		if s.backgroundReserveExhausted(repo, QuotaResourceREST, false) {
			probe.abandon()
			exhausted[bucket] = true
			continue
		}
		resolution, fresh := repoResolutions.resolve(ctx, s, repo)
		if !fresh && (resolution.err != nil || resolution.repoID == 0) {
			probe.abandon()
			continue
		}
		if resolution.err != nil {
			probe.abandon()
			slog.Warn("detail drain: verify repo identity failed",
				"repo", qi.RepoOwner+"/"+qi.RepoName,
				"err", resolution.err,
			)
			continue
		}
		repo, repoID := resolution.repo, resolution.repoID
		if repo.Archived {
			// Identity verification just discovered the archived flip;
			// the publication already dropped the repo from live sync.
			probe.abandon()
			continue
		}
		if repoID == 0 {
			probe.abandon()
			slog.Warn("detail drain: verified repo has no database identity",
				"repo", qi.RepoOwner+"/"+qi.RepoName,
			)
			continue
		}
		itemCtx := withCloneRepositoryIdentity(ctx, repo)

		// Compute diff SHAs if clone available.
		cloneFetchOK := false
		if s.clones != nil {
			if cloneErr := s.ensureClone(itemCtx, repo); cloneErr != nil {
				if !isReportedGitAccessFailure(cloneErr) {
					slog.Warn("detail drain: bare clone failed",
						"repo", qi.RepoOwner+"/"+qi.RepoName,
						"err", cloneErr,
					)
				}
			} else {
				cloneFetchOK = true
			}
		}
		var providerCalls int
		if qi.Type == QueueItemPR {
			providerCalls, err = s.fetchMRDetail(
				itemCtx, repo, repoID, qi.Number, cloneFetchOK,
			)
		} else {
			providerCalls, err = s.fetchIssueDetail(
				itemCtx, repo, repoID, qi.Number,
			)
		}
		if providerCalls > 0 {
			attempted[detailAttempt{repoID, qi.Number, qi.Type}] = true
		}

		if err != nil {
			disabledErr := repositoryFeatureDisabledError(repo, feature, err)
			disabled := disabledErr != nil &&
				s.recordRepositoryFeatureDisabled(repo, feature, disabledErr)
			if providerCalls > 0 {
				probe.release()
			} else {
				probe.abandon()
			}
			if disabled {
				continue
			}
			logSyncFailure(ctx, slog.LevelWarn, "detail drain: fetch failed", err,
				"repo", qi.RepoOwner+"/"+qi.RepoName,
				"number", qi.Number,
				"type", qi.Type,
			)
			continue
		}
		if providerCalls > 0 {
			probe.release()
		} else {
			probe.abandon()
		}
	}
}

// buildDetailQueueItems queries the DB for open PRs and issues
// that may need a detail fetch, combining with starred/watched
// state to build queue items for scoring.
func (s *Syncer) buildDetailQueueItems(
	ctx context.Context,
	repos []RepoRef,
) []QueueItem {
	// Build the set of repos selected for this run. In addition to filtering
	// stale DB rows from removed repos, this keeps repository-scoped runs from
	// spending their detail budget on unrelated repositories on the same host.
	trackedRepos := make(map[string]bool, len(repos))
	for _, r := range repos {
		if r.Archived {
			continue
		}
		trackedRepos[detailRepoKey(repoPlatform(r), repoHost(r), r.Owner, r.Name)] = true
	}
	if len(trackedRepos) == 0 {
		return nil
	}
	storedRepos, err := s.db.ListRepos(ctx)
	if err != nil {
		slog.Warn("detail drain: list repositories failed", "err", err)
		return nil
	}
	reposByID := make(map[int64]db.Repo, len(storedRepos))
	for _, repo := range storedRepos {
		reposByID[repo.ID] = repo
	}

	// Gather watched MR numbers for matching.
	s.watchMu.Lock()
	watched := make(map[string]bool, len(s.watchedMRs))
	for _, w := range s.watchedMRs {
		watched[watchedMRKey(w)] = true
	}
	s.watchMu.Unlock()

	var items []QueueItem

	// Open PRs.
	prs, err := s.db.ListMergeRequests(
		ctx, db.ListMergeRequestsOpts{State: "open"},
	)
	if err != nil {
		slog.Warn("detail drain: list open PRs failed",
			"err", err,
		)
		return nil
	}
	prCountsByRepoID := make(map[int64]int, len(prs))
	for _, pr := range prs {
		prCountsByRepoID[pr.RepoID]++
	}
	for _, pr := range prs {
		repo, found := reposByID[pr.RepoID]
		if !found {
			continue
		}
		repoKey := detailRepoKey(platform.Kind(repo.Platform), repo.PlatformHost, repo.Owner, repo.Name)
		if !trackedRepos[repoKey] {
			continue
		}
		watchKey := detailRepoKey(
			platform.Kind(repo.Platform), repo.PlatformHost,
			repo.Owner, repo.Name,
		) + fmt.Sprintf("#%d", pr.Number)
		ciHadPending := pr.CIHadPending || ciHasPending(pr.CIChecksJSON)
		mergeabilityPending := repo.Platform == "github" &&
			(pr.MergeableState == "" || pr.MergeableState == "unknown")
		items = append(items, QueueItem{
			Type:                QueueItemPR,
			Platform:            platform.Kind(repo.Platform),
			RepoOwner:           repo.Owner,
			RepoName:            repo.Name,
			Number:              pr.Number,
			PlatformHost:        repo.PlatformHost,
			UpdatedAt:           pr.UpdatedAt,
			DetailFetchedAt:     pr.DetailFetchedAt,
			CIHadPending:        ciHadPending,
			MergeabilityPending: mergeabilityPending,
			Starred:             pr.Starred,
			Watched:             watched[watchKey],
			IsOpen:              true,
			LargeRepo:           prCountsByRepoID[pr.RepoID] >= largeRepoBulkGraphQLThreshold,
		})
	}

	// Open issues.
	issues, err := s.db.ListIssues(
		ctx, db.ListIssuesOpts{State: "open"},
	)
	if err != nil {
		slog.Warn("detail drain: list open issues failed",
			"err", err,
		)
		return items
	}
	issueCountsByRepoID := make(map[int64]int, len(issues))
	for _, issue := range issues {
		issueCountsByRepoID[issue.RepoID]++
	}
	for _, issue := range issues {
		repo, found := reposByID[issue.RepoID]
		if !found {
			continue
		}
		repoKey := detailRepoKey(platform.Kind(repo.Platform), repo.PlatformHost, repo.Owner, repo.Name)
		if !trackedRepos[repoKey] {
			continue
		}
		items = append(items, QueueItem{
			Type:            QueueItemIssue,
			Platform:        platform.Kind(repo.Platform),
			RepoOwner:       repo.Owner,
			RepoName:        repo.Name,
			Number:          issue.Number,
			PlatformHost:    repo.PlatformHost,
			UpdatedAt:       issue.UpdatedAt,
			DetailFetchedAt: issue.DetailFetchedAt,
			Starred:         issue.Starred,
			IsOpen:          true,
			LargeRepo:       issueCountsByRepoID[issue.RepoID] >= largeRepoBulkGraphQLThreshold,
		})
	}

	return items
}
