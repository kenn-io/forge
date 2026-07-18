package archive

import (
	"context"
	"errors"
	"fmt"
	"strconv"

	"go.kenn.io/middleman/internal/db"
	"go.kenn.io/middleman/internal/platform"
)

// inventoryPage advances one historical inventory scan by one canonical page:
// StateAll ordered by creation for a stable traversal, cursor and generation
// bound to the durable scan row.
func (s *Service) inventoryPage(ctx context.Context, repo resolvedRepository, state db.ArchiveRepoState, itemType db.ArchiveItemType) error {
	kind := db.ArchiveScanIssueInventory
	if itemType == db.ArchiveItemTypeMergeRequest {
		kind = db.ArchiveScanMergeRequestInventory
	}
	scan := state.Scan(kind)
	commit := db.ArchiveInventoryCommit{
		RepoID: repo.ID, ItemType: itemType,
		RefreshReason:  db.ArchiveRefreshReasonInitial,
		ScanGeneration: scan.Generation, InputCursor: scan.Cursor(),
		Now: s.now(),
	}
	if !archiveInventorySupported(repo, itemType) {
		commit.Exhausted = true
	} else {
		requestCtx, release, err := s.admit(ctx, repo, archiveAttemptCost(1))
		if err != nil {
			if errors.Is(err, errAdmissionDeferred) {
				return err
			}
			return s.recordInventoryFailure(ctx, repo.ID, err)
		}
		query := platform.ItemPageQuery{
			State: platform.ItemStateAll, Order: platform.ItemOrderCreated,
			Cursor: scan.Cursor(),
		}
		switch itemType {
		case db.ArchiveItemTypeIssue:
			page, err := repo.Issues.ListIssuesPage(requestCtx, repo.Ref, query)
			preempted := archivePreempted(ctx, requestCtx)
			release()
			if err != nil {
				if preempted {
					return errAdmissionDeferred
				}
				return s.recordScanFailure(ctx, repo, kind, fmt.Errorf(
					"list historical issues for %s: %w", archiveRepoIdentityKey(repo.Ref), err,
				))
			}
			commit.NextCursor, commit.Exhausted = page.NextCursor, page.Exhausted
			commit.Issues = make([]db.ArchiveInventoryIssue, 0, len(page.Items))
			for _, item := range page.Items {
				commit.Issues = append(commit.Issues, archiveInventoryIssue(repo, item))
			}
		case db.ArchiveItemTypeMergeRequest:
			page, err := repo.MergeRequests.ListMergeRequestsPage(requestCtx, repo.Ref, query)
			preempted := archivePreempted(ctx, requestCtx)
			release()
			if err != nil {
				if preempted {
					return errAdmissionDeferred
				}
				return s.recordScanFailure(ctx, repo, kind, fmt.Errorf(
					"list historical merge requests for %s: %w", archiveRepoIdentityKey(repo.Ref), err,
				))
			}
			commit.NextCursor, commit.Exhausted = page.NextCursor, page.Exhausted
			commit.MergeRequests = make([]db.ArchiveInventoryMergeRequest, 0, len(page.Items))
			for _, item := range page.Items {
				commit.MergeRequests = append(commit.MergeRequests, archiveInventoryMergeRequest(repo, item))
			}
		default:
			release()
			return fmt.Errorf("archive inventory: invalid item type %q", itemType)
		}
	}
	_, err := s.commitInventoryPage(ctx, commit)
	return err
}

// commitInventoryPage absorbs the typed non-failure outcomes of an inventory
// commit: stale generations mean another worker or a reset advanced the scan,
// and blocked scans are already durably recorded. Both halt the current
// traversal without failing it.
func (s *Service) commitInventoryPage(ctx context.Context, commit db.ArchiveInventoryCommit) (bool, error) {
	err := s.db.CommitArchiveInventoryPage(ctx, commit)
	var stale *db.StaleArchiveScanError
	if errors.As(err, &stale) {
		return true, nil
	}
	var blocked *db.ScanBlockedError
	if errors.As(err, &blocked) {
		return true, nil
	}
	return err != nil, err
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

// recordScanFailure routes one scan read failure: page-scoped provider
// contract violations durably block only the affected scan, everything else
// records a repository-level failure with retry classification.
func (s *Service) recordScanFailure(
	ctx context.Context,
	repo resolvedRepository,
	kind db.ArchiveScanKind,
	cause error,
) error {
	if pageScopedProviderFailure(cause) {
		if err := s.db.BlockArchiveRepoScan(
			ctx, repo.ID, kind, scanBlockCode(cause), cause.Error(),
		); err != nil {
			return errors.Join(cause, err)
		}
		return nil
	}
	return s.recordInventoryFailure(ctx, repo.ID, cause)
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
