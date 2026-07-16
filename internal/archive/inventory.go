package archive

import (
	"context"
	"errors"
	"fmt"
	"strconv"

	"go.kenn.io/middleman/internal/db"
	"go.kenn.io/middleman/internal/platform"
)

func (s *Service) inventoryPage(ctx context.Context, repo resolvedRepository, state db.ArchiveRepoState, itemType db.ArchiveItemType) error {
	commit := db.ArchiveInventoryCommit{
		RepoID: repo.ID, ItemType: itemType,
		RefreshReason: db.ArchiveRefreshReasonInitial, Now: s.now(),
	}
	if !archiveInventorySupported(repo, itemType) {
		commit.Exhausted = true
	} else {
		requestCtx, err := s.admit(ctx, repo, 1)
		if err != nil {
			if errors.Is(err, errAdmissionDeferred) {
				return err
			}
			return s.recordInventoryFailure(ctx, repo.ID, err)
		}
		switch itemType {
		case db.ArchiveItemTypeIssue:
			cursor := archiveCursorValue(state.IssueCursor)
			page, err := repo.Reader.ListHistoricalIssues(requestCtx, repo.Ref, cursor)
			if err != nil {
				return s.recordInventoryFailure(ctx, repo.ID, fmt.Errorf(
					"list historical issues for %s: %w", archiveRepoIdentityKey(repo.Ref), err,
				))
			}
			commit.NextCursor, commit.Exhausted = page.NextCursor, page.Exhausted
			commit.Issues = make([]db.ArchiveInventoryIssue, 0, len(page.Items))
			for _, item := range page.Items {
				commit.Issues = append(commit.Issues, archiveInventoryIssue(repo, item))
			}
		case db.ArchiveItemTypeMergeRequest:
			cursor := archiveCursorValue(state.MergeRequestCursor)
			page, err := repo.Reader.ListHistoricalMergeRequests(requestCtx, repo.Ref, cursor)
			if err != nil {
				return s.recordInventoryFailure(ctx, repo.ID, fmt.Errorf(
					"list historical merge requests for %s: %w", archiveRepoIdentityKey(repo.Ref), err,
				))
			}
			commit.NextCursor, commit.Exhausted = page.NextCursor, page.Exhausted
			commit.MergeRequests = make([]db.ArchiveInventoryMergeRequest, 0, len(page.Items))
			for _, item := range page.Items {
				commit.MergeRequests = append(commit.MergeRequests, archiveInventoryMergeRequest(repo, item))
			}
		default:
			return fmt.Errorf("archive inventory: invalid item type %q", itemType)
		}
	}
	return s.db.CommitArchiveInventoryPage(ctx, commit)
}

func archiveInventorySupported(repo resolvedRepository, itemType db.ArchiveItemType) bool {
	switch itemType {
	case db.ArchiveItemTypeIssue:
		return repo.Capabilities.HistoricalIssues
	case db.ArchiveItemTypeMergeRequest:
		return repo.Capabilities.HistoricalMergeRequests
	default:
		return false
	}
}

func (s *Service) recordInventoryFailure(ctx context.Context, repoID int64, cause error) error {
	decision := s.retries.Classify(cause, 0, s.now())
	if decision.Code == "" {
		decision.Code = db.ArchiveErrorCodeTransient
	}
	if err := s.db.RecordArchiveRepositoryFailure(
		ctx, repoID, decision.Code, cause.Error(), decision.RetryAt, s.now(),
	); err != nil {
		return errors.Join(cause, err)
	}
	return cause
}

func archiveInventoryIssue(repo resolvedRepository, item platform.Issue) db.ArchiveInventoryIssue {
	parent := platform.DBIssue(repo.ID, item)
	return db.ArchiveInventoryIssue{
		Snapshot:       db.IssueSnapshot{Issue: *parent, Labels: parent.Labels},
		ProviderItemID: providerItemID(item.PlatformExternalID, item.PlatformID),
		CommentsStatus: datasetStatus(repo.Capabilities.OrdinaryComments),
	}
}

func archiveInventoryMergeRequest(repo resolvedRepository, item platform.MergeRequest) db.ArchiveInventoryMergeRequest {
	parent := platform.DBMergeRequest(repo.ID, item)
	return db.ArchiveInventoryMergeRequest{
		Snapshot:             db.MergeRequestSnapshot{MergeRequest: *parent, Labels: parent.Labels},
		ProviderItemID:       providerItemID(item.PlatformExternalID, item.PlatformID),
		CommentsStatus:       datasetStatus(repo.Capabilities.OrdinaryComments),
		ReviewsStatus:        datasetStatus(repo.Capabilities.SubmittedReviews),
		InlineCommentsStatus: datasetStatus(repo.Capabilities.InlineReviewComments),
	}
}

func datasetStatus(supported bool) db.ArchiveDatasetStatus {
	if supported {
		return db.ArchiveDatasetStatusPending
	}
	return db.ArchiveDatasetStatusUnsupported
}

func providerItemID(external string, numeric int64) string {
	if external != "" {
		return external
	}
	return strconv.FormatInt(numeric, 10)
}

func archiveCursorValue(cursor *string) string {
	if cursor == nil {
		return ""
	}
	return *cursor
}
