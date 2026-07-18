package archive

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"go.kenn.io/middleman/internal/db"
	"go.kenn.io/middleman/internal/platform"
)

// hydrateItem drains one item's due datasets. The lookup dataset runs first:
// a present lookup refreshes the parent and rebinds child dataset progress, a
// terminal outcome ends the item's work. Child datasets then scan page by
// page through the canonical provider readers, re-acquiring admission and
// committing each normalized page with its durable cursor advance.
func (s *Service) hydrateItem(ctx context.Context, repo resolvedRepository, work db.ArchiveItemWork) error {
	if len(work.Datasets) > 0 && work.Datasets[0].Dataset == db.ArchiveDatasetLookup {
		terminal, err := s.hydrateLookup(ctx, repo, work, work.Datasets[0])
		if err != nil || terminal {
			return err
		}
		// A present lookup rebinds and reopens child datasets, so the claimed
		// dataset list is stale by design. Re-derive due work for the item.
		datasets, err := s.db.GetDueDatasetWork(ctx, work.RepoID, work.ItemType, work.ItemNumber, s.now())
		if err != nil {
			return err
		}
		work.Datasets = datasets
	}
	for _, dataset := range work.Datasets {
		if dataset.Dataset == db.ArchiveDatasetLookup {
			continue
		}
		if err := s.scanDataset(ctx, repo, work, dataset); err != nil {
			return err
		}
	}
	return nil
}

// archiveLookupCost declares the worst-case logical request count of one
// canonical item lookup. Every provider spends the item fetch plus at most
// one repository probe on lookup classification. Only a GitLab merge-request
// lookup adds a third request: the best-effort fork head clone-URL
// enrichment inside the canonical lookup. The declared cost must cover the
// provider's worst case so an admitted lookup can never overspend the
// protected live floor — and no other provider reserves requests it never
// issues.
func archiveLookupCost(kind platform.Kind, itemType db.ArchiveItemType) int {
	if itemType == db.ArchiveItemTypeMergeRequest && kind == platform.KindGitLab {
		return archiveAttemptCost(3)
	}
	return archiveAttemptCost(2)
}

// hydrateLookup runs the lookup dataset: one canonical provider lookup whose
// outcome commits through the shared parent-observation core. A terminal
// outcome (removed, moved, inaccessible) marks the item terminal — archived
// domain content is retained — and a moved destination queues a prompt scan.
func (s *Service) hydrateLookup(
	ctx context.Context,
	repo resolvedRepository,
	work db.ArchiveItemWork,
	dataset db.ArchiveDatasetWork,
) (bool, error) {
	// The lookup commit is revision-fenced on the domain parent as observed
	// before the provider read: a live observation that advances the parent
	// mid-lookup supersedes this lookup's conclusion.
	_, expectedRevision, err := s.db.GetDomainParent(ctx, work.RepoID, work.ItemType, work.ItemNumber)
	if err != nil {
		if !errors.Is(err, sql.ErrNoRows) {
			return false, err
		}
		expectedRevision = 0
	}
	requestCtx, release, err := s.admit(ctx, repo, archiveLookupCost(repo.Ref.Platform, work.ItemType))
	if err != nil {
		return false, err
	}
	commit := db.ParentLookupCommit{
		RepoID: work.RepoID, ItemType: work.ItemType, ItemNumber: work.ItemNumber,
		ScanGeneration: dataset.ScanGeneration, ExpectedRevision: expectedRevision,
		Now: s.now(),
	}
	var lookupErr error
	switch work.ItemType {
	case db.ArchiveItemTypeIssue:
		var lookup platform.ItemLookup[platform.Issue]
		lookup, lookupErr = repo.Issues.LookupIssue(requestCtx, repo.Ref, work.ItemNumber)
		if lookupErr == nil {
			commit.Outcome = db.ArchiveLookupOutcome(lookup.Outcome)
			if lookup.Outcome == platform.LookupPresent {
				commit.Issue = platform.DBIssue(work.RepoID, lookup.Item)
			}
			if lookup.Destination != nil {
				destination := platform.DBRepoIdentity(*lookup.Destination)
				commit.Destination = &destination
			}
		}
	case db.ArchiveItemTypeMergeRequest:
		var lookup platform.ItemLookup[platform.MergeRequest]
		lookup, lookupErr = repo.MergeRequests.LookupMergeRequest(requestCtx, repo.Ref, work.ItemNumber)
		if lookupErr == nil {
			commit.Outcome = db.ArchiveLookupOutcome(lookup.Outcome)
			if lookup.Outcome == platform.LookupPresent {
				commit.MergeRequest = platform.DBMergeRequest(work.RepoID, lookup.Item)
			}
			if lookup.Destination != nil {
				destination := platform.DBRepoIdentity(*lookup.Destination)
				commit.Destination = &destination
			}
		}
	default:
		release()
		return false, fmt.Errorf("hydrate archive item: invalid item type %q", work.ItemType)
	}
	preempted := archivePreempted(ctx, requestCtx)
	release()
	if lookupErr != nil {
		if preempted {
			return false, errAdmissionDeferred
		}
		if pageScopedProviderFailure(lookupErr) {
			// A malformed lookup outcome indicts only this lookup, exactly
			// like a malformed page result in a child dataset scan: block the
			// lookup dataset durably and let the item's other datasets and
			// the repository proceed. Auth, identity, and capability failures
			// keep repository scope below.
			key := db.ArchiveDatasetProgressKey{
				RepoID: work.RepoID, ItemType: work.ItemType,
				ItemNumber: work.ItemNumber, Dataset: db.ArchiveDatasetLookup,
			}
			return true, s.blockDataset(ctx, key, dataset, lookupErr)
		}
		return false, s.recordDatasetFailure(ctx, repo, work, dataset, lookupErr)
	}
	err = s.db.CommitParentLookup(ctx, commit)
	var stale *db.StaleDatasetProgressError
	if errors.As(err, &stale) {
		// A forced reset or a newer parent observation superseded this lookup
		// between claim and commit. Nothing was written; the next claim
		// rescans from durable state.
		return true, nil
	}
	var blocked *db.ScanBlockedError
	if errors.As(err, &blocked) {
		// Durably blocked: no further automatic spend for this item.
		return true, nil
	}
	if err != nil {
		return false, err
	}
	return commit.Outcome != db.ArchiveLookupPresent, nil
}

// scanDataset drains one child dataset page by page from its durable cursor.
// Every page re-acquires admission, holds it only for the provider read, and
// commits rows and cursor atomically.
func (s *Service) scanDataset(
	ctx context.Context,
	repo resolvedRepository,
	work db.ArchiveItemWork,
	dataset db.ArchiveDatasetWork,
) error {
	parentID, currentRevision, err := s.db.GetDomainParent(ctx, work.RepoID, work.ItemType, work.ItemNumber)
	if err != nil {
		return err
	}
	key := db.ArchiveDatasetProgressKey{
		RepoID: work.RepoID, ItemType: work.ItemType,
		ItemNumber: work.ItemNumber, Dataset: dataset.Dataset,
	}
	if currentRevision != dataset.ParentRevision {
		// The parent advanced since this dataset was bound. Rebinding before
		// the first read avoids spending a provider request on a page commit
		// that would be rejected as stale.
		if err := s.db.ReopenDatasetForRevision(ctx, key, currentRevision); err != nil {
			return err
		}
		progress, err := s.db.GetDatasetProgress(ctx, key.RepoID, key.ItemType, key.ItemNumber, key.Dataset)
		if err != nil {
			return err
		}
		if progress.Status != db.ArchiveDatasetProgressPending &&
			progress.Status != db.ArchiveDatasetProgressRunning &&
			progress.Status != db.ArchiveDatasetProgressFailed {
			return nil
		}
		dataset.ParentRevision = progress.ParentRevision
		dataset.ScanGeneration = progress.ScanGeneration
		dataset.NextCursor = progress.NextCursor
	}
	cursor := dataset.Cursor()
	for {
		requestCtx, release, err := s.admit(ctx, repo, archiveAttemptCost(1))
		if err != nil {
			return err
		}
		rows, next, exhausted, readErr := s.readDatasetPage(requestCtx, repo, work, dataset.Dataset, parentID, cursor)
		preempted := archivePreempted(ctx, requestCtx)
		release()
		if readErr != nil {
			if preempted {
				return errAdmissionDeferred
			}
			if pageScopedProviderFailure(readErr) {
				return s.blockDataset(ctx, key, dataset, readErr)
			}
			return s.recordDatasetFailure(ctx, repo, work, dataset, readErr)
		}
		err = s.db.CommitDatasetPage(ctx, db.DatasetPageCommit{
			Parent:           db.DomainParentRef{ItemType: work.ItemType, ID: parentID},
			ExpectedRevision: dataset.ParentRevision,
			Dataset:          dataset.Dataset,
			ScanGeneration:   dataset.ScanGeneration,
			Rows:             rows,
			Final:            exhausted,
			Progress: &db.DatasetProgressAdvance{
				RepoID: work.RepoID, ItemNumber: work.ItemNumber,
				InputCursor: cursor, NextCursor: next,
			},
		})
		var stale *db.StaleDatasetProgressError
		if errors.As(err, &stale) {
			// The parent advanced mid-scan; the commit reopened the dataset
			// for the new revision. The next claim rescans from page one of
			// the fresh generation.
			return nil
		}
		var blocked *db.ScanBlockedError
		if errors.As(err, &blocked) {
			// Echoed cursor or page bound: durably recorded, no retry here.
			return nil
		}
		if err != nil {
			return err
		}
		if exhausted {
			return nil
		}
		cursor = next
	}
}

// readDatasetPage executes one canonical provider page read and converts the
// normalized values into DB rows scoped to the parent.
func (s *Service) readDatasetPage(
	ctx context.Context,
	repo resolvedRepository,
	work db.ArchiveItemWork,
	dataset db.ArchiveDataset,
	parentID int64,
	cursor string,
) (db.DatasetRows, string, bool, error) {
	switch dataset {
	case db.ArchiveDatasetComments:
		if work.ItemType == db.ArchiveItemTypeIssue {
			page, err := repo.Issues.ListIssueCommentsPage(ctx, repo.Ref, work.ItemNumber, cursor)
			if err != nil {
				return db.DatasetRows{}, "", false, err
			}
			events := make([]db.IssueEvent, 0, len(page.Items))
			for _, event := range page.Items {
				events = append(events, platform.DBIssueEvent(parentID, event))
			}
			return db.DatasetRows{IssueComments: events}, page.NextCursor, page.Exhausted, nil
		}
		page, err := repo.MergeRequests.ListMergeRequestCommentsPage(ctx, repo.Ref, work.ItemNumber, cursor)
		if err != nil {
			return db.DatasetRows{}, "", false, err
		}
		return db.DatasetRows{MRComments: dbMergeRequestEvents(parentID, page.Items)}, page.NextCursor, page.Exhausted, nil
	case db.ArchiveDatasetReviews:
		page, err := repo.MergeRequests.ListSubmittedReviewsPage(ctx, repo.Ref, work.ItemNumber, cursor)
		if err != nil {
			return db.DatasetRows{}, "", false, err
		}
		return db.DatasetRows{Reviews: dbMergeRequestEvents(parentID, page.Items)}, page.NextCursor, page.Exhausted, nil
	case db.ArchiveDatasetInlineComments:
		page, err := repo.MergeRequests.ListReviewThreadsPage(ctx, repo.Ref, work.ItemNumber, cursor)
		if err != nil {
			return db.DatasetRows{}, "", false, err
		}
		events, threads := platform.DBReviewThreads(page.Items)
		for i := range events {
			events[i].MergeRequestID = parentID
		}
		return db.DatasetRows{ReviewThreads: threads, ThreadEvents: events}, page.NextCursor, page.Exhausted, nil
	default:
		return db.DatasetRows{}, "", false, fmt.Errorf("scan archive dataset: invalid dataset %q", dataset)
	}
}

func dbMergeRequestEvents(mrID int64, events []platform.MergeRequestEvent) []db.MREvent {
	out := make([]db.MREvent, 0, len(events))
	for _, event := range events {
		out = append(out, platform.DBMREvent(mrID, event))
	}
	return out
}

// blockDataset durably blocks one dataset scan after a page-scoped provider
// contract violation. Other datasets and repository work proceed. The block
// carries the claimed generation and revision, so a delayed violation from
// superseded work is a stale no-op.
func (s *Service) blockDataset(
	ctx context.Context,
	key db.ArchiveDatasetProgressKey,
	dataset db.ArchiveDatasetWork,
	cause error,
) error {
	return s.db.BlockDatasetProgress(
		ctx, key, dataset.ScanGeneration, dataset.ParentRevision,
		scanBlockCode(cause), cause.Error(),
	)
}

// recordDatasetFailure classifies a provider failure: repository-wide causes
// (authentication, identity, capability) block the repository, and the
// dataset row records retry bookkeeping either way. The failure carries the
// claimed generation and revision, so a delayed provider failure from
// superseded work never marks reset, completed, or newer-generation progress
// failed.
func (s *Service) recordDatasetFailure(
	ctx context.Context,
	repo resolvedRepository,
	work db.ArchiveItemWork,
	dataset db.ArchiveDatasetWork,
	cause error,
) error {
	decision := s.retries.Classify(cause, dataset.AttemptCount, s.now())
	if decision.Code == "" {
		decision.Code = db.ArchiveErrorCodeTransient
	}
	key := db.ArchiveDatasetProgressKey{
		RepoID: work.RepoID, ItemType: work.ItemType,
		ItemNumber: work.ItemNumber, Dataset: dataset.Dataset,
	}
	if decision.Code == db.ArchiveErrorCodeAuthentication || decision.Code == db.ArchiveErrorCodeRepoBlocked {
		// Repository-wide causes commit atomically with the dataset CAS: a
		// delayed repository-scoped response from superseded work must not
		// re-block a repository whose claim was since reset or completed.
		if _, err := s.db.FailDatasetProgressRecordingRepositoryFailure(
			ctx, key, dataset.ScanGeneration, dataset.ParentRevision,
			decision.Code, cause.Error(), decision.RetryAt, s.now(),
		); err != nil {
			return errors.Join(cause, err)
		}
		return cause
	}
	if err := s.db.FailDatasetProgress(
		ctx, key, dataset.ScanGeneration, dataset.ParentRevision,
		decision.Code, cause.Error(), decision.RetryAt,
	); err != nil {
		return errors.Join(cause, err)
	}
	return cause
}
