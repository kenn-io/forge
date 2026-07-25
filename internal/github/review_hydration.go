package github

import (
	"context"
	"fmt"
	"time"

	"go.kenn.io/middleman/internal/db"
	"go.kenn.io/middleman/internal/platform"
	"go.kenn.io/middleman/internal/platform/gitealike"
)

func (s *Syncer) syncStagedMRReviewThreads(
	ctx context.Context,
	hydrator platform.MergeRequestReviewHydrator,
	repo RepoRef,
	mrID int64,
	number int,
	expectedRevision int64,
) (int, bool, error) {
	parent, err := s.db.GetMergeRequest(
		ctx, string(repoPlatform(repo)), repoHost(repo), repo.Owner, repo.Name, number,
	)
	if err != nil {
		return 0, false, fmt.Errorf("load merge request review hydration parent: %w", err)
	}
	if parent == nil || parent.ID != mrID || parent.SnapshotRevision != expectedRevision {
		return 0, false, errParentSnapshotAdvanced
	}
	key := db.MRReviewHydrationStageKey{
		MergeRequestID: mrID, ProviderUpdatedAt: parent.UpdatedAt,
		HeadSHA: parent.PlatformHeadSHA,
	}
	stage, err := s.db.GetMRReviewHydrationStage(ctx, mrID)
	if err != nil {
		return 0, false, fmt.Errorf("load merge request review hydration stage: %w", err)
	}
	if stage == nil || !reviewHydrationStageMatches(*stage, key) {
		reviewIDs, err := hydrator.ListMergeRequestReviewIDs(
			ctx, platformRepoRef(repo), number,
		)
		if err != nil {
			return 1, false, err
		}
		if _, err := s.db.ReplaceMRReviewHydrationStage(ctx, key, reviewIDs); err != nil {
			return 1, false, fmt.Errorf("replace merge request review hydration stage: %w", err)
		}
		cleared, err := s.db.ClearMRDetailFetchedSnapshot(ctx, mrID, expectedRevision)
		if err != nil {
			return 1, false, fmt.Errorf("clear staged merge request detail marker: %w", err)
		}
		if !cleared {
			return 1, false, errParentSnapshotAdvanced
		}
		return 1, false, nil
	}

	end := min(
		stage.NextReviewIndex+gitealike.MaxReviewHydrationReviewsPerPass,
		len(stage.ReviewIDs),
	)
	calls := 0
	var batch []db.MRReviewHydrationThread
	for _, reviewID := range stage.ReviewIDs[stage.NextReviewIndex:end] {
		threads, err := hydrator.ListMergeRequestReviewThreadsForReview(
			ctx, platformRepoRef(repo), number, reviewID,
		)
		calls++
		if err != nil {
			return calls, false, err
		}
		for i := range threads {
			if threads[i].CreatedAt.IsZero() {
				threads[i].CreatedAt = time.Now().UTC()
			}
		}
		batch = append(batch, platform.DBReviewHydrationThreads(threads)...)
	}
	if len(stage.Threads)+len(batch) > gitealike.MaxReviewHydrationComments {
		return calls, false, gitealike.ReviewHydrationLimit(
			"review_hydration_comments", len(stage.Threads)+len(batch),
			gitealike.MaxReviewHydrationComments,
		)
	}
	applied, err := s.db.AppendMRReviewHydrationStage(ctx, *stage, batch, end)
	if err != nil {
		return calls, false, fmt.Errorf("append merge request review hydration stage: %w", err)
	}
	if !applied {
		return calls, false, errParentSnapshotAdvanced
	}
	if end < len(stage.ReviewIDs) {
		return calls, false, nil
	}
	stage.NextReviewIndex = end
	committed, err := s.db.CommitMRReviewHydrationStage(ctx, *stage, expectedRevision)
	if err != nil {
		return calls, false, fmt.Errorf("commit merge request review hydration stage: %w", err)
	}
	if !committed {
		return calls, false, errParentSnapshotAdvanced
	}
	return calls, true, nil
}

func reviewHydrationStageMatches(
	stage db.MRReviewHydrationStage,
	key db.MRReviewHydrationStageKey,
) bool {
	return stage.MergeRequestID == key.MergeRequestID &&
		stage.ProviderUpdatedAt.Equal(key.ProviderUpdatedAt) &&
		stage.HeadSHA == key.HeadSHA
}
