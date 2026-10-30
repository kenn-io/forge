package github

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"time"

	"go.kenn.io/forge/internal/platformdb"

	gh "github.com/google/go-github/v93/github"
	"go.kenn.io/forge/internal/db"
	"go.kenn.io/forge/platform"
	platformgithub "go.kenn.io/forge/platform/github"
)

// fetchAndUpdateClosed retrieves the final state of a now-closed PR from GitHub.
func (s *Syncer) fetchAndUpdateClosed(ctx context.Context, repo RepoRef, repoID int64, number int, cloneFetchOK bool) error {
	removed, err := s.db.IsArchiveItemRemovedUpstream(
		ctx, repoID, db.ArchiveItemTypeMergeRequest, number,
	)
	if err != nil {
		return fmt.Errorf("check closed PR #%d visibility: %w", number, err)
	}
	if removed {
		return nil
	}
	client, err := s.clientFor(repo)
	if err != nil {
		return fmt.Errorf("resolve client for %s/%s: %w", repo.Owner, repo.Name, err)
	}
	requestedAt := s.nowUTC()
	ghPR, err := client.GetPullRequest(ctx, repo.Owner, repo.Name, number)
	// Route fetch failures and detected transfers through the canonical
	// lookup classification so removed, inaccessible, and moved items
	// surface typed outcomes instead of generic upstream failures.
	if outcomeErr := s.mergeRequestFetchOutcomeError(ctx, repo, number, ghPR, err); outcomeErr != nil {
		if errors.Is(outcomeErr, platform.ErrLookupNotPresent) &&
			errors.Is(outcomeErr, platform.ErrNotFound) && lookupDestination(outcomeErr) == nil {
			return s.db.RecordRemovedMergeRequest(ctx, repoID, number, outcomeErr.Error(), s.nowUTC())
		}
		return fmt.Errorf("get closed PR #%d: %w", number, outcomeErr)
	}
	if err != nil {
		return fmt.Errorf("get closed PR #%d: %w", number, err)
	}
	if ghPR == nil {
		return fmt.Errorf(
			"get closed PR #%d: client returned nil pull request",
			number,
		)
	}

	normalized, err := NormalizePR(repoID, ghPR)
	if err != nil {
		return fmt.Errorf("normalize closed PR #%d: %w", number, err)
	}
	normalized.MergeableStateObservedAt = &requestedAt
	existing, err := s.db.GetMergeRequestByRepoIDAndNumber(ctx, repoID, number)
	if err != nil {
		return fmt.Errorf("get closed MR #%d: %w", number, err)
	}
	CarryMergeRequestDerivedFields(normalized, existing)
	mrID, revision, accepted, err := s.CommitMergeRequestParentSnapshot(ctx, repo, normalized)
	if err != nil {
		return fmt.Errorf("commit closed MR #%d: %w", number, err)
	}
	if !accepted {
		return nil
	}
	if _, err := s.persistMergedTransitionEvent(ctx, mrID, revision, ghPR, normalized.MergedAt); err != nil {
		return fmt.Errorf("persist merged lifecycle event for MR #%d: %w", number, err)
	}
	if err := s.markClosedLinkedPRNotificationsDone(ctx, repoID, number); err != nil {
		return err
	}

	// Compute diff SHAs so the diff endpoint works.
	// For closed-but-not-merged PRs, use GitHub's head/base SHAs directly.
	// For merged PRs, use merge-base(merge_commit^1, refs/pull/<number>/head)
	// to find the fork point. This works for all merge strategies because ^1
	// is always a pre-merge commit on the base branch lineage, and the pull
	// ref always points to the original PR head. We only do this when no diff
	// SHAs exist yet; PRs synced while open already have valid diff SHAs.
	closedHost := repo.PlatformHost
	if closedHost == "" {
		closedHost = "github.com"
	}
	if s.clones != nil && cloneFetchOK {
		headSHA := ghPR.GetHead().GetSHA()
		baseSHA := ghPR.GetBase().GetSHA()

		if pullRequestWasMerged(ghPR) {
			if err := s.computeMergedMRDiffSHAs(
				ctx, repo, repoID, mrID, revision, number,
				normalized.PlatformHeadSHA, normalized.PlatformBaseSHA,
				ghPR.GetMergeCommitSHA(), false,
			); err != nil {
				if errors.Is(err, errParentSnapshotAdvanced) {
					return nil
				}
				slog.Warn("compute merged PR diff SHAs failed",
					"repo", repo.Owner+"/"+repo.Name,
					"number", number, "err", err,
				)
			}
		} else if headSHA != "" && baseSHA != "" {
			mb, err := s.clones.MergeBase(ctx, string(repoPlatform(repo)), closedHost, repo.Owner, repo.Name, baseSHA, headSHA)
			if err != nil {
				slog.Warn("merge-base for closed PR failed",
					"repo", repo.Owner+"/"+repo.Name,
					"number", number, "err", err,
				)
			} else {
				diffApplied, err := s.db.UpdateDiffSHAsSnapshot(
					ctx, mrID, revision, headSHA, baseSHA,
					headSHA, baseSHA, mb,
				)
				if err != nil {
					slog.Warn("update diff SHAs for closed PR failed",
						"repo", repo.Owner+"/"+repo.Name,
						"number", number, "err", err,
					)
				} else if !diffApplied {
					return nil
				}
			}
		}
	}
	return s.markClosedLinkedPRNotificationsDone(ctx, repoID, number)
}

func (s *Syncer) filterDuplicateMergedLifecycleEvents(
	ctx context.Context,
	mrID int64,
	events []db.MREvent,
) ([]db.MREvent, error) {
	if !slices.ContainsFunc(events, isAuthoredMergedLifecycleEvent) {
		return events, nil
	}
	existing, err := s.db.ListMREvents(ctx, mrID)
	if err != nil {
		return nil, err
	}
	out := events[:0]
	for _, event := range events {
		if isAuthoredMergedLifecycleEvent(event) &&
			authoredMergedLifecycleEventExists(existing) {
			continue
		}
		out = append(out, event)
	}
	return out, nil
}

func authoredMergedLifecycleEventExists(events []db.MREvent) bool {
	return slices.ContainsFunc(events, isAuthoredMergedLifecycleEvent)
}

func isAuthoredMergedLifecycleEvent(event db.MREvent) bool {
	return event.EventType == "merged" &&
		strings.TrimSpace(event.Author) != ""
}

func (s *Syncer) persistMergedTransitionEvent(
	ctx context.Context,
	mrID int64,
	expectedRevision int64,
	ghPR *gh.PullRequest,
	mergedAt *time.Time,
) (bool, error) {
	if ghPR == nil || mergedAt == nil {
		return false, nil
	}
	mergedBy := ghPR.GetMergedBy()
	if mergedBy == nil {
		return false, nil
	}
	return s.persistMergedActorEvent(ctx, mrID, expectedRevision, mergedBy.GetLogin(), mergedAt)
}

func (s *Syncer) persistMergedActorEvent(
	ctx context.Context,
	mrID int64,
	_ int64,
	actor string,
	mergedAt *time.Time,
) (bool, error) {
	if mergedAt == nil {
		return false, nil
	}
	actor = strings.TrimSpace(actor)
	if actor == "" {
		return false, nil
	}
	event := NormalizeTimelineEvent(mrID, platformgithub.PullRequestTimelineEvent{
		EventType: "merged",
		Actor:     actor,
		CreatedAt: *mergedAt,
	})
	if event == nil {
		return false, nil
	}
	return s.db.UpsertMergedActorEvent(ctx, *event)
}

func (s *Syncer) fetchAndUpdateClosedMergeRequest(
	ctx context.Context,
	reader platform.MergeRequestReader,
	repo RepoRef,
	repoID int64,
	number int,
	cloneFetchOK bool,
) error {
	removed, err := s.db.IsArchiveItemRemovedUpstream(
		ctx, repoID, db.ArchiveItemTypeMergeRequest, number,
	)
	if err != nil {
		return fmt.Errorf("check closed MR #%d visibility: %w", number, err)
	}
	if removed {
		return nil
	}
	if _, ok := reader.(interface {
		GetGitHubPullRequest(context.Context, platform.RepoRef, int) (*gh.PullRequest, platform.MergeRequest, error)
	}); ok {
		return s.fetchAndUpdateClosed(ctx, repo, repoID, number, cloneFetchOK)
	}

	mrReader, err := s.mergeRequestReaderFor(repo)
	if err != nil {
		return fmt.Errorf("resolve merge request reader for %s/%s: %w", repo.Owner, repo.Name, err)
	}
	requestedAt := s.nowUTC()
	mr, err := mrReader.GetMergeRequest(ctx, platformRepoRef(repo), number)
	if err != nil {
		return fmt.Errorf("get closed MR #%d: %w", number, err)
	}
	normalized := platformdb.DBMergeRequest(repoID, mr)
	normalized.MergeableStateObservedAt = &requestedAt
	existing, err := s.db.GetMergeRequestByRepoIDAndNumber(ctx, repoID, number)
	if err != nil {
		return fmt.Errorf("get stored closed MR #%d: %w", number, err)
	}
	CarryMergeRequestDerivedFields(normalized, existing)
	mrID, revision, accepted, err := s.CommitMergeRequestParentSnapshot(ctx, repo, normalized)
	if err != nil {
		return fmt.Errorf("upsert closed MR #%d: %w", number, err)
	}
	if !accepted {
		return nil
	}
	if _, err := s.persistMergedActorEvent(ctx, mrID, revision, mr.MergedBy, normalized.MergedAt); err != nil {
		return fmt.Errorf("persist merged lifecycle event for closed MR #%d: %w", number, err)
	}
	return nil
}

// computeMergedMRDiffSHAs computes diff SHAs for a merged PR.
// Uses merge-base(merge_commit^1, refs/pull/<number>/head) which works for all
// GitHub merge strategies:
//   - Merge commit: ^1 is the pre-merge base tip
//   - Squash: ^1 is the pre-squash base tip
//   - Rebase: ^1 is the previous rebased commit
//
// In all cases, merge-base with the original PR head (from the pull ref)
// correctly identifies the fork point.
//
// When force is false, skips PRs that already have diff SHAs (periodic sync).
// When force is true, always recomputes (on-demand SyncMR).
//
// Returns a *DiffSyncError (wrapped as an error) describing the failure when
// any git or DB operation fails. A nil return covers both success and the
// no-op skip cases (empty merge SHA, existing valid diff SHAs without force).
func (s *Syncer) computeMergedMRDiffSHAs(
	ctx context.Context, repo RepoRef, repoID, mrID, expectedRevision int64,
	number int, expectedHeadSHA, expectedBaseSHA, mergeCommitSHA string, force bool,
) error {
	if mergeCommitSHA == "" {
		return nil
	}

	if !force {
		existing, err := s.db.GetDiffSHAsByRepoID(ctx, repoID, number)
		if err != nil {
			return &DiffSyncError{
				Code: DiffSyncCodeInternal,
				Err:  fmt.Errorf("get diff SHAs for merged PR #%d: %w", number, err),
			}
		}
		if existing == nil || existing.DiffHeadSHA != "" {
			return nil // already has diff SHAs or PR not found
		}
	}

	// Resolve the PR head from the pull ref. GitHub keeps these refs
	// indefinitely, pointing to the original PR head commit regardless
	// of merge strategy.
	mergedHost := repo.PlatformHost
	if mergedHost == "" {
		mergedHost = "github.com"
	}
	pullRef := fmt.Sprintf("refs/pull/%d/head", number)
	prHead, err := s.clones.RevParse(ctx, string(repoPlatform(repo)), mergedHost, repo.Owner, repo.Name, pullRef)
	if err != nil {
		return &DiffSyncError{
			Code: DiffSyncCodeCommitUnreachable,
			Err:  fmt.Errorf("rev-parse %s for merged PR #%d: %w", pullRef, number, err),
		}
	}

	// Use the merge commit's first parent as the base for merge-base.
	// This avoids the post-merge ancestor problem where prHead is reachable
	// from the current base branch tip (making merge-base return prHead).
	preMergeBase, err := s.clones.RevParse(ctx, string(repoPlatform(repo)), mergedHost, repo.Owner, repo.Name, mergeCommitSHA+"^1")
	if err != nil {
		return &DiffSyncError{
			Code: DiffSyncCodeCommitUnreachable,
			Err:  fmt.Errorf("rev-parse %s^1 for merged PR #%d: %w", mergeCommitSHA, number, err),
		}
	}

	mb, err := s.clones.MergeBase(ctx, string(repoPlatform(repo)), mergedHost, repo.Owner, repo.Name, preMergeBase, prHead)
	if err != nil {
		return &DiffSyncError{
			Code: DiffSyncCodeMergeBaseFailed,
			Err:  fmt.Errorf("merge-base for merged PR #%d: %w", number, err),
		}
	}

	if prHead == "" || mb == "" {
		return nil
	}

	applied, err := s.db.UpdateDiffSHAsSnapshot(
		ctx, mrID, expectedRevision, expectedHeadSHA, expectedBaseSHA,
		prHead, mb, mb,
	)
	if err != nil {
		return &DiffSyncError{
			Code: DiffSyncCodeInternal,
			Err:  fmt.Errorf("update diff SHAs for merged PR #%d: %w", number, err),
		}
	}
	if !applied {
		return errParentSnapshotAdvanced
	}
	return nil
}
