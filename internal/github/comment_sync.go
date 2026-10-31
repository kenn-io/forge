package github

import (
	"context"
	"encoding/json/v2"
	"fmt"
	"log/slog"
	"slices"
	"time"

	gh "github.com/google/go-github/v93/github"
	"go.kenn.io/forge/internal/db"
	"go.kenn.io/forge/platform"
	platformgithub "go.kenn.io/forge/platform/github"
)

type queuedPRCommentSync struct {
	repo            RepoRef
	repoID          int64
	number          int
	detailFetchedAt *time.Time
}

type queuedIssueCommentSync struct {
	repo            RepoRef
	repoID          int64
	number          int
	detailFetchedAt *time.Time
}

const largeCommentThreadThreshold = 100

func (s *Syncer) listCommentsForRefresh(
	ctx context.Context,
	client Client,
	repo RepoRef,
	number int,
	knownCount int,
) ([]*gh.IssueComment, error) {
	if knownCount >= largeCommentThreadThreshold {
		return client.ListIssueComments(
			ctx, repo.Owner, repo.Name, number,
		)
	}
	return client.ListIssueCommentsIfChanged(
		ctx, repo.Owner, repo.Name, number,
	)
}

func (s *Syncer) refreshPRCommentsForItem(
	ctx context.Context,
	client Client,
	repo RepoRef,
	pr *db.MergeRequest,
) (bool, bool) {
	if pr == nil || pr.DetailFetchedAt == nil {
		return false, false
	}
	if !commentRefreshDue(string(pr.State), pr.UpdatedAt, *pr.DetailFetchedAt, pr.Starred, time.Now()) {
		return false, false
	}
	if !s.canSpendCommentRefresh(repo) {
		return false, false
	}
	comments, err := s.listCommentsForRefresh(
		ctx, client, repo, pr.Number, pr.CommentCount,
	)
	if err != nil {
		if platformgithub.IsNotModified(err) {
			return true, false
		}
		if s.recordGitHubRepositoryFeatureDisabled(
			repo, platform.RepositoryFeatureMergeRequests, err,
		) {
			return true, true
		}
		logSyncFailure(ctx, slog.LevelWarn, "comment refresh: list PR comments failed", err,
			"repo", repo.Owner+"/"+repo.Name,
			"number", pr.Number,
		)
		return true, false
	}
	if err := s.persistPRComments(ctx, repo, pr, comments); err != nil {
		client.InvalidateListETagsForRepo(repo.Owner, repo.Name, "comments")
		slog.Warn("comment refresh: persist PR comments failed",
			"repo", repo.Owner+"/"+repo.Name,
			"number", pr.Number,
			"err", err,
		)
	}
	return true, false
}

func (s *Syncer) refreshIssueCommentsForItem(
	ctx context.Context,
	client Client,
	repo RepoRef,
	issue *db.Issue,
) (bool, bool) {
	if issue == nil || issue.DetailFetchedAt == nil {
		return false, false
	}
	if !commentRefreshDue(issue.State, issue.UpdatedAt, *issue.DetailFetchedAt, issue.Starred, time.Now()) {
		return false, false
	}
	if !s.canSpendCommentRefresh(repo) {
		return false, false
	}
	comments, err := s.listCommentsForRefresh(
		ctx, client, repo, issue.Number, issue.CommentCount,
	)
	if err != nil {
		if platformgithub.IsNotModified(err) {
			return true, false
		}
		if s.recordGitHubRepositoryFeatureDisabled(
			repo, platform.RepositoryFeatureIssues, err,
		) {
			return true, true
		}
		logSyncFailure(ctx, slog.LevelWarn, "comment refresh: list issue comments failed", err,
			"repo", repo.Owner+"/"+repo.Name,
			"number", issue.Number,
		)
		return true, false
	}
	if err := s.persistIssueComments(ctx, repo, issue, comments); err != nil {
		client.InvalidateListETagsForRepo(repo.Owner, repo.Name, "comments")
		slog.Warn("comment refresh: persist issue comments failed",
			"repo", repo.Owner+"/"+repo.Name,
			"number", issue.Number,
			"err", err,
		)
	}
	return true, false
}

func (s *Syncer) resetPendingCommentSyncs() {
	s.commentRefreshMu.Lock()
	defer s.commentRefreshMu.Unlock()
	s.pendingPRCommentSyncs = nil
	s.pendingIssueCommentSyncs = nil
}

func (s *Syncer) queuePRCommentSync(repo RepoRef, repoID int64, number int, detailFetchedAt *time.Time) {
	s.commentRefreshMu.Lock()
	defer s.commentRefreshMu.Unlock()
	s.pendingPRCommentSyncs = append(s.pendingPRCommentSyncs, queuedPRCommentSync{
		repo: repo, repoID: repoID,
		number: number, detailFetchedAt: detailFetchedAt,
	})
}

func (s *Syncer) queueIssueCommentSync(repo RepoRef, repoID int64, number int, detailFetchedAt *time.Time) {
	s.commentRefreshMu.Lock()
	defer s.commentRefreshMu.Unlock()
	s.pendingIssueCommentSyncs = append(s.pendingIssueCommentSyncs, queuedIssueCommentSync{
		repo: repo, repoID: repoID,
		number: number, detailFetchedAt: detailFetchedAt,
	})
}

func (s *Syncer) drainPendingCommentSyncs(
	ctx context.Context,
	eligibleHosts map[string]bool,
) {
	s.commentRefreshMu.Lock()
	prs := slices.Clone(s.pendingPRCommentSyncs)
	issues := slices.Clone(s.pendingIssueCommentSyncs)
	s.pendingPRCommentSyncs = nil
	s.pendingIssueCommentSyncs = nil
	s.commentRefreshMu.Unlock()

	for _, item := range prs {
		if ctx.Err() != nil {
			return
		}
		bucket, err := s.bucketKeyForRepo(item.repo, false)
		if err != nil || !eligibleHosts[bucket] {
			continue
		}
		// Re-read the reserve per item: this loop can be long, so a credential
		// with headroom when the drain started can reach its reserve partway
		// through. Revoking the bucket stops the rest of its items too.
		if s.backgroundReserveExhausted(item.repo, QuotaResourceREST, false) {
			eligibleHosts[bucket] = false
			continue
		}
		refreshCtx := ctx
		client, err := s.clientFor(item.repo)
		if err != nil {
			slog.Warn("comment refresh: resolve client failed",
				"repo", item.repo.Owner+"/"+item.repo.Name,
				"number", item.number,
				"err", err,
			)
			continue
		}
		pr, err := s.db.GetVisibleMergeRequestByRepoIDAndNumber(
			refreshCtx, item.repoID, item.number,
		)
		if err != nil {
			slog.Warn("comment refresh: get PR failed",
				"repo", item.repo.Owner+"/"+item.repo.Name,
				"number", item.number,
				"err", err,
			)
			continue
		}
		if pr == nil {
			continue
		}
		// Detail refreshes include comments. Do not repeat that work when
		// the detail pass completed after this comment check was queued.
		if pr.DetailFetchedAt != nil &&
			(item.detailFetchedAt == nil || pr.DetailFetchedAt.After(*item.detailFetchedAt)) {
			continue
		}
		probe, due := s.beginRepositoryFeatureProbe(
			refreshCtx, item.repo, platform.RepositoryFeatureMergeRequests,
		)
		if !due {
			continue
		}
		providerAttempted, _ := s.refreshPRCommentsForItem(
			refreshCtx, client, item.repo, pr,
		)
		if providerAttempted {
			probe.release()
		} else {
			probe.abandon()
		}
	}

	for _, item := range issues {
		if ctx.Err() != nil {
			return
		}
		bucket, err := s.bucketKeyForRepo(item.repo, false)
		if err != nil || !eligibleHosts[bucket] {
			continue
		}
		// Issue refreshes spend the same credential as the pull-request loop
		// above and need the same per-item reserve check: an issue-only queue
		// would otherwise keep going after the credential reaches its reserve.
		if s.backgroundReserveExhausted(item.repo, QuotaResourceREST, false) {
			eligibleHosts[bucket] = false
			continue
		}
		refreshCtx := ctx
		client, err := s.clientFor(item.repo)
		if err != nil {
			slog.Warn("comment refresh: resolve client failed",
				"repo", item.repo.Owner+"/"+item.repo.Name,
				"number", item.number,
				"err", err,
			)
			continue
		}
		issue, err := s.db.GetVisibleIssueByRepoIDAndNumber(
			refreshCtx, item.repoID, item.number,
		)
		if err != nil {
			slog.Warn("comment refresh: get issue failed",
				"repo", item.repo.Owner+"/"+item.repo.Name,
				"number", item.number,
				"err", err,
			)
			continue
		}
		if issue == nil {
			continue
		}
		if issue.DetailFetchedAt != nil &&
			(item.detailFetchedAt == nil || issue.DetailFetchedAt.After(*item.detailFetchedAt)) {
			continue
		}
		probe, due := s.beginRepositoryFeatureProbe(
			refreshCtx, item.repo, platform.RepositoryFeatureIssues,
		)
		if !due {
			continue
		}
		providerAttempted, _ := s.refreshIssueCommentsForItem(
			refreshCtx, client, item.repo, issue,
		)
		if providerAttempted {
			probe.release()
		} else {
			probe.abandon()
		}
	}
}

func computeIssueCommentLastActivity(
	ghIssue *gh.Issue,
	comments []*gh.IssueComment,
) time.Time {
	var latest time.Time
	if ghIssue != nil {
		if ghIssue.UpdatedAt != nil {
			latest = ghIssue.UpdatedAt.Time
		}
		if latest.IsZero() && ghIssue.CreatedAt != nil {
			latest = ghIssue.CreatedAt.Time
		}
	}
	for _, c := range comments {
		switch {
		case c.UpdatedAt != nil && c.UpdatedAt.After(latest):
			latest = c.UpdatedAt.Time
		case c.CreatedAt != nil && c.CreatedAt.After(latest):
			latest = c.CreatedAt.Time
		}
	}
	return latest
}

func computeIssueCommentRefreshLastActivity(
	issue *db.Issue,
	comments []*gh.IssueComment,
) time.Time {
	return computeIssueCommentLastActivity(&gh.Issue{
		CreatedAt: &gh.Timestamp{Time: issue.CreatedAt},
		UpdatedAt: &gh.Timestamp{Time: issue.UpdatedAt},
	}, comments)
}

func (s *Syncer) replacePRCommentEvents(
	ctx context.Context,
	repo RepoRef,
	number int,
	mrID int64,
	expectedRevision int64,
	comments []*gh.IssueComment,
	derived *db.MRDerivedFields,
	visibility map[int64]platformgithub.CommentVisibility,
) (bool, error) {
	if visibility == nil {
		var err error
		visibility, err = s.storedPRCommentVisibility(ctx, mrID)
		if err != nil {
			return false, err
		}
	}
	events := make([]db.MREvent, 0, len(comments))
	for _, c := range comments {
		event := NormalizeCommentEventWithVisibility(mrID, c, visibility[c.GetID()])
		events = append(events, event)
	}
	applied, err := s.commitMergeRequestDatasets(
		ctx, repo, mrID, number, expectedRevision, events, true, nil, nil, nil, false, nil, derived, "",
	)
	return applied, err
}

func (s *Syncer) replaceIssueCommentEvents(
	ctx context.Context,
	repo RepoRef,
	number int,
	issueID int64,
	expectedRevision int64,
	comments []*gh.IssueComment,
	otherEvents []db.IssueEvent,
	derived *db.IssueDerivedFields,
	visibility map[int64]platformgithub.CommentVisibility,
) (bool, error) {
	if visibility == nil {
		var err error
		visibility, err = s.storedIssueCommentVisibility(ctx, issueID)
		if err != nil {
			return false, err
		}
	}
	events := make([]db.IssueEvent, 0, len(comments))
	for _, c := range comments {
		event := NormalizeIssueCommentEventWithVisibility(issueID, c, visibility[c.GetID()])
		events = append(events, event)
	}
	return s.commitIssueCommentsSnapshot(ctx, repo, issueID, number, expectedRevision, events, otherEvents, derived)
}

// currentPRCommentVisibility also returns GitHub's raw review decision, which
// the same query reads.
func (s *Syncer) currentPRCommentVisibility(
	ctx context.Context,
	repo RepoRef,
	number int,
) (map[int64]platformgithub.CommentVisibility, string, bool) {
	fetcher := s.fetcherForContext(ctx, repo)
	if fetcher == nil || !s.graphQLReadAllowed(ctx, repo, fetcher) {
		return nil, "", false
	}
	visibility, reviewDecision, err := fetcher.FetchPullRequestCommentVisibility(
		ctx, repo.Owner, repo.Name, number,
	)
	if err != nil {
		logSyncFailure(ctx, slog.LevelWarn, "current PR comment visibility fetch failed; preserving stored state", err,
			"repo", repo.Owner+"/"+repo.Name,
			"number", number,
		)
		return nil, "", false
	}
	return visibility, reviewDecision, true
}

func (s *Syncer) currentIssueCommentVisibility(
	ctx context.Context,
	repo RepoRef,
	number int,
) (map[int64]platformgithub.CommentVisibility, bool) {
	fetcher := s.fetcherForContext(ctx, repo)
	if fetcher == nil || !s.graphQLReadAllowed(ctx, repo, fetcher) {
		return nil, false
	}
	visibility, err := fetcher.FetchIssueCommentVisibility(
		ctx, repo.Owner, repo.Name, number,
	)
	if err != nil {
		logSyncFailure(ctx, slog.LevelWarn, "current issue comment visibility fetch failed; preserving stored state", err,
			"repo", repo.Owner+"/"+repo.Name,
			"number", number,
		)
		return nil, false
	}
	return visibility, true
}

func commentMetadataUpdates(
	visibility map[int64]platformgithub.CommentVisibility,
) []db.CommentMetadataUpdate {
	updates := make([]db.CommentMetadataUpdate, 0, len(visibility))
	for platformID, state := range visibility {
		updates = append(updates, db.CommentMetadataUpdate{
			PlatformID: platformID, MetadataJSON: platformgithub.NormalizeCommentVisibilityMetadata(state),
		})
	}
	return updates
}

func (s *Syncer) refreshStoredPRCommentVisibility(
	ctx context.Context,
	repo RepoRef,
	mrID int64,
	expectedRevision int64,
	number int,
) error {
	visibility, _, ok := s.currentPRCommentVisibility(ctx, repo, number)
	if !ok {
		return nil
	}
	applied, err := s.db.UpdateMergeRequestCommentMetadataSnapshot(
		ctx, mrID, expectedRevision, commentMetadataUpdates(visibility),
	)
	if err != nil {
		return fmt.Errorf("update current PR comment visibility for #%d: %w", number, err)
	}
	if !applied {
		return errParentSnapshotAdvanced
	}
	return nil
}

func (s *Syncer) refreshStoredIssueCommentVisibility(
	ctx context.Context,
	repo RepoRef,
	issueID int64,
	expectedRevision int64,
	number int,
) error {
	visibility, ok := s.currentIssueCommentVisibility(ctx, repo, number)
	if !ok {
		return nil
	}
	applied, err := s.db.UpdateIssueCommentMetadataSnapshot(
		ctx, issueID, expectedRevision, commentMetadataUpdates(visibility),
	)
	if err != nil {
		return fmt.Errorf("update current issue comment visibility for #%d: %w", number, err)
	}
	if !applied {
		return errParentSnapshotAdvanced
	}
	return nil
}

func (s *Syncer) storedPRCommentVisibility(
	ctx context.Context,
	mrID int64,
) (map[int64]platformgithub.CommentVisibility, error) {
	visibility := make(map[int64]platformgithub.CommentVisibility)
	existing, err := s.db.ListMREvents(ctx, mrID)
	if err != nil {
		return nil, fmt.Errorf("list existing PR comments before visibility-preserving replacement: %w", err)
	}
	for _, event := range existing {
		if event.EventType != "issue_comment" || event.PlatformID == nil {
			continue
		}
		if state, ok := commentVisibilityFromMetadata(event.MetadataJSON); ok {
			visibility[*event.PlatformID] = state
		}
	}
	return visibility, nil
}

func (s *Syncer) storedIssueCommentVisibility(
	ctx context.Context,
	issueID int64,
) (map[int64]platformgithub.CommentVisibility, error) {
	visibility := make(map[int64]platformgithub.CommentVisibility)
	existing, err := s.db.ListIssueEvents(ctx, issueID)
	if err != nil {
		return nil, fmt.Errorf("list existing issue comments before visibility-preserving replacement: %w", err)
	}
	for _, event := range existing {
		if event.EventType != "issue_comment" || event.PlatformID == nil {
			continue
		}
		if state, ok := commentVisibilityFromMetadata(event.MetadataJSON); ok {
			visibility[*event.PlatformID] = state
		}
	}
	return visibility, nil
}

func commentVisibilityFromMetadata(metadataJSON string) (platformgithub.CommentVisibility, bool) {
	if metadataJSON == "" {
		return platformgithub.CommentVisibility{}, false
	}
	var metadata struct {
		Hidden bool   `json:"provider_hidden"`
		Reason string `json:"provider_hidden_reason"`
	}
	if err := json.Unmarshal([]byte(metadataJSON), &metadata); err != nil || !metadata.Hidden {
		return platformgithub.CommentVisibility{}, false
	}
	return platformgithub.CommentVisibility{Hidden: true, Reason: metadata.Reason}, true
}

func (s *Syncer) queueRepoPRComments(
	ctx context.Context,
	repo RepoRef,
) {
	prs, err := s.db.ListMergeRequests(ctx, db.ListMergeRequestsOpts{
		PlatformHost: repoHost(repo),
		RepoOwner:    repo.Owner,
		RepoName:     repo.Name,
		State:        "open",
	})
	if err != nil {
		slog.Warn("comment refresh: list open PRs failed",
			"repo", repo.Owner+"/"+repo.Name,
			"err", err,
		)
		return
	}

	for i := range prs {
		if ctx.Err() != nil {
			return
		}
		s.queuePRCommentSync(repo, prs[i].RepoID, prs[i].Number, prs[i].DetailFetchedAt)
	}
}

func (s *Syncer) queueRepoIssueComments(
	ctx context.Context,
	repo RepoRef,
) {
	issues, err := s.db.ListIssues(ctx, db.ListIssuesOpts{
		PlatformHost: repoHost(repo),
		RepoOwner:    repo.Owner,
		RepoName:     repo.Name,
		State:        "open",
	})
	if err != nil {
		slog.Warn("comment refresh: list open issues failed",
			"repo", repo.Owner+"/"+repo.Name,
			"err", err,
		)
		return
	}

	for i := range issues {
		if ctx.Err() != nil {
			return
		}
		s.queueIssueCommentSync(repo, issues[i].RepoID, issues[i].Number, issues[i].DetailFetchedAt)
	}
}

func (s *Syncer) canSpendCommentRefresh(repo RepoRef) bool {
	budget, ok := s.BudgetForRepo(repo)
	return !ok || budget == nil || budget.CanSpend(1)
}

func (s *Syncer) persistPRComments(
	ctx context.Context,
	repo RepoRef,
	pr *db.MergeRequest,
	comments []*gh.IssueComment,
) error {
	applied, err := s.replacePRCommentEvents(
		ctx, repo, pr.Number, pr.ID, pr.SnapshotRevision, comments, nil, nil,
	)
	if err != nil {
		return fmt.Errorf("replace PR comment events: %w", err)
	}
	if !applied {
		return nil
	}
	return nil
}

func (s *Syncer) persistIssueComments(
	ctx context.Context,
	repo RepoRef,
	issue *db.Issue,
	comments []*gh.IssueComment,
) error {
	_, err := s.persistIssueCommentsApplied(ctx, repo, issue, comments)
	return err
}

// persistIssueCommentsApplied reports whether the comments were committed.
// A parent write that landed after the caller's snapshot leaves them
// uncommitted without an error.
func (s *Syncer) persistIssueCommentsApplied(
	ctx context.Context,
	repo RepoRef,
	issue *db.Issue,
	comments []*gh.IssueComment,
) (bool, error) {
	derived := db.IssueDerivedFields{
		CommentCount:   len(comments),
		LastActivityAt: computeIssueCommentRefreshLastActivity(issue, comments),
	}
	applied, err := s.replaceIssueCommentEvents(
		ctx, repo, issue.Number, issue.ID, issue.SnapshotRevision, comments, nil, &derived, nil,
	)
	if err != nil {
		return false, fmt.Errorf("replace issue comment events: %w", err)
	}
	return applied, nil
}
