package archive

import (
	"context"
	"errors"
	"fmt"
	"time"

	"go.kenn.io/middleman/internal/db"
)

func (s *Service) promptMaintenance(
	ctx context.Context,
	repo resolvedRepository,
	state db.ArchiveRepoState,
) error {
	since := state.MaintenanceWatermark
	if since == nil {
		since = state.InitialStartedAt
	}
	if since == nil {
		return errors.New("run archive prompt maintenance: initial completion time is missing")
	}
	state, err := s.db.BeginArchivePromptMaintenance(ctx, repo.ID, since.UTC(), s.now())
	if err != nil {
		return err
	}
	if state.PromptSince == nil || state.PromptScanStartedAt == nil {
		return errors.New("run archive prompt maintenance: durable scan boundary is missing")
	}
	if !state.PromptIssueComplete {
		if err := s.promptIssuePages(ctx, repo, state.PromptSince.UTC(), archiveCursorValue(state.PromptIssueCursor)); err != nil {
			return err
		}
	}
	if !state.PromptMergeRequestComplete {
		if err := s.promptMergeRequestPages(ctx, repo, state.PromptSince.UTC(), archiveCursorValue(state.PromptMergeRequestCursor)); err != nil {
			return err
		}
	}
	return s.db.CompleteArchivePromptMaintenance(ctx, repo.ID, state.PromptScanStartedAt.UTC(), s.now())
}

func (s *Service) promptIssuePages(
	ctx context.Context,
	repo resolvedRepository,
	since time.Time,
	cursor string,
) error {
	for range maxArchiveDatasetPages {
		requestCtx, release, err := s.admit(ctx, repo, archiveAttemptCost(1))
		if err != nil {
			if errors.Is(err, errAdmissionDeferred) {
				return err
			}
			return s.recordInventoryFailure(ctx, repo.ID, err)
		}
		page, err := repo.Reader.ListUpdatedIssues(requestCtx, repo.Ref, since, cursor)
		preempted := archivePreempted(ctx, requestCtx)
		release()
		if err != nil {
			if preempted {
				return errAdmissionDeferred
			}
			return s.recordInventoryFailure(ctx, repo.ID, fmt.Errorf(
				"list updated issues for %s: %w", archiveRepoIdentityKey(repo.Ref), err,
			))
		}
		commit := db.ArchiveInventoryCommit{
			RepoID: repo.ID, ItemType: db.ArchiveItemTypeIssue,
			RefreshReason: db.ArchiveRefreshReasonPrompt,
			NextCursor:    page.NextCursor, Exhausted: page.Exhausted, Now: s.now(),
			Issues: make([]db.ArchiveInventoryIssue, 0, len(page.Items)),
		}
		for _, item := range page.Items {
			commit.Issues = append(commit.Issues, archiveInventoryIssue(repo, item))
		}
		if err := s.db.CommitArchiveInventoryPage(ctx, commit); err != nil {
			return err
		}
		if page.Exhausted {
			return nil
		}
		cursor = page.NextCursor
	}
	return errors.New("archive updated issue inventory exceeded page limit")
}

func (s *Service) promptMergeRequestPages(
	ctx context.Context,
	repo resolvedRepository,
	since time.Time,
	cursor string,
) error {
	for range maxArchiveDatasetPages {
		requestCtx, release, err := s.admit(ctx, repo, archiveAttemptCost(1))
		if err != nil {
			if errors.Is(err, errAdmissionDeferred) {
				return err
			}
			return s.recordInventoryFailure(ctx, repo.ID, err)
		}
		page, err := repo.Reader.ListUpdatedMergeRequests(requestCtx, repo.Ref, since, cursor)
		preempted := archivePreempted(ctx, requestCtx)
		release()
		if err != nil {
			if preempted {
				return errAdmissionDeferred
			}
			return s.recordInventoryFailure(ctx, repo.ID, fmt.Errorf(
				"list updated merge requests for %s: %w", archiveRepoIdentityKey(repo.Ref), err,
			))
		}
		commit := db.ArchiveInventoryCommit{
			RepoID: repo.ID, ItemType: db.ArchiveItemTypeMergeRequest,
			RefreshReason: db.ArchiveRefreshReasonPrompt,
			NextCursor:    page.NextCursor, Exhausted: page.Exhausted, Now: s.now(),
			MergeRequests: make([]db.ArchiveInventoryMergeRequest, 0, len(page.Items)),
		}
		for _, item := range page.Items {
			commit.MergeRequests = append(commit.MergeRequests, archiveInventoryMergeRequest(repo, item))
		}
		if err := s.db.CommitArchiveInventoryPage(ctx, commit); err != nil {
			return err
		}
		if page.Exhausted {
			return nil
		}
		cursor = page.NextCursor
	}
	return errors.New("archive updated merge-request inventory exceeded page limit")
}

func promptMaintenanceDue(state db.ArchiveRepoState, now time.Time, interval time.Duration) bool {
	if state.InitialCompletedAt == nil {
		return false
	}
	if state.MaintenanceWatermark == nil || state.MaintenanceSucceededAt == nil {
		return true
	}
	return !state.MaintenanceSucceededAt.Add(interval).After(now)
}
