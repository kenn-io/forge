package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// maxScanPages bounds every dataset scan generation. Cursor compare-and-swap
// alone cannot stop longer cursor cycles, so exceeding the bound blocks the
// scan with the deliberately ambiguous "page_bound" reason: it may be a
// legitimately oversized scan or an undetected cycle, and continuation is an
// explicit operator classification.
const maxScanPages = 10_000

const (
	datasetErrorCodePageBound     = "page_bound"
	datasetErrorCodeInvalidCursor = "invalid_cursor"
)

// formatDatasetProgressTime writes UTC RFC3339 text for the TEXT timestamp
// columns of the dataset progress schema.
func formatDatasetProgressTime(t time.Time) string {
	return t.UTC().Format(time.RFC3339Nano)
}

func parseDatasetProgressTimePtr(value sql.NullString) (*time.Time, error) {
	if !value.Valid || value.String == "" {
		return nil, nil
	}
	parsed, err := parseDBTime(value.String)
	if err != nil {
		return nil, err
	}
	return &parsed, nil
}

// domainParentRowTx resolves the repo, item number, and current snapshot
// revision of one domain parent row.
func domainParentRowTx(
	ctx context.Context,
	tx *sql.Tx,
	parent DomainParentRef,
) (repoID int64, itemNumber int, revision int64, err error) {
	table := ""
	switch parent.ItemType {
	case ArchiveItemTypeIssue:
		table = "middleman_issues"
	case ArchiveItemTypeMergeRequest:
		table = "middleman_merge_requests"
	default:
		return 0, 0, 0, fmt.Errorf("resolve dataset parent: invalid item type %q", parent.ItemType)
	}
	err = tx.QueryRowContext(ctx, fmt.Sprintf(
		`SELECT repo_id, number, snapshot_revision FROM %s WHERE id = ?`, table,
	), parent.ID).Scan(&repoID, &itemNumber, &revision)
	if err != nil {
		return 0, 0, 0, fmt.Errorf("resolve dataset parent %s %d: %w", parent.ItemType, parent.ID, err)
	}
	return repoID, itemNumber, revision, nil
}

func validateDatasetPageCommit(commit DatasetPageCommit) error {
	if commit.Parent.ID <= 0 {
		return errors.New("commit dataset page: parent id is required")
	}
	isMR := false
	switch commit.Parent.ItemType {
	case ArchiveItemTypeIssue:
	case ArchiveItemTypeMergeRequest:
		isMR = true
	default:
		return fmt.Errorf("commit dataset page: invalid item type %q", commit.Parent.ItemType)
	}
	rows := commit.Rows
	switch commit.Dataset {
	case ArchiveDatasetComments:
		if len(rows.Reviews) != 0 || len(rows.ReviewThreads) != 0 || len(rows.ThreadEvents) != 0 {
			return errors.New("commit dataset page: comments page carries non-comment rows")
		}
		if isMR {
			if len(rows.IssueComments) != 0 {
				return errors.New("commit dataset page: merge-request comments page carries issue comments")
			}
			for i := range rows.MRComments {
				if rows.MRComments[i].EventType != "issue_comment" {
					return fmt.Errorf("commit dataset page: ordinary comment has event type %q", rows.MRComments[i].EventType)
				}
			}
		} else {
			if len(rows.MRComments) != 0 {
				return errors.New("commit dataset page: issue comments page carries merge-request comments")
			}
			for i := range rows.IssueComments {
				if rows.IssueComments[i].EventType != "issue_comment" {
					return fmt.Errorf("commit dataset page: ordinary comment has event type %q", rows.IssueComments[i].EventType)
				}
			}
		}
	case ArchiveDatasetReviews:
		if !isMR {
			return errors.New("commit dataset page: reviews apply to merge requests only")
		}
		if len(rows.IssueComments) != 0 || len(rows.MRComments) != 0 ||
			len(rows.ReviewThreads) != 0 || len(rows.ThreadEvents) != 0 {
			return errors.New("commit dataset page: reviews page carries non-review rows")
		}
	case ArchiveDatasetInlineComments:
		if !isMR {
			return errors.New("commit dataset page: inline comments apply to merge requests only")
		}
		if len(rows.IssueComments) != 0 || len(rows.MRComments) != 0 || len(rows.Reviews) != 0 {
			return errors.New("commit dataset page: inline comments page carries non-thread rows")
		}
	default:
		return fmt.Errorf("commit dataset page: invalid dataset %q", commit.Dataset)
	}
	if commit.Progress != nil && !commit.Final && commit.Progress.NextCursor == "" {
		return errors.New("commit dataset page: next cursor or explicit final page is required")
	}
	return nil
}

func datasetRowCount(rows DatasetRows) int {
	return len(rows.IssueComments) + len(rows.MRComments) + len(rows.Reviews) +
		len(rows.ReviewThreads) + len(rows.ThreadEvents)
}

// CommitDatasetPage is the single child-dataset page commit shared by live
// sync and archive scans. Domain persistence is identical in both modes;
// archive work additionally compare-and-swaps durable dataset progress in the
// same transaction, and live work conditionally satisfies matching archive
// progress without ever depending on it.
func (d *DB) CommitDatasetPage(ctx context.Context, commit DatasetPageCommit) error {
	if err := validateDatasetPageCommit(commit); err != nil {
		return err
	}
	now := time.Now().UTC()
	var typedErr error
	err := d.Tx(ctx, func(tx *sql.Tx) error {
		var err error
		typedErr, err = commitDatasetPageTx(ctx, tx, commit, now)
		return err
	})
	if err != nil {
		return err
	}
	return typedErr
}

// commitDatasetPageTx is the transaction core of CommitDatasetPage. It is the
// single implementation of child-dataset row writes and ordinary-comment
// reconciliation; live child-snapshot writers compose it per complete dataset
// inside their own transaction. A non-nil typedErr describes an outcome whose
// progress side effects (dataset reopen, block) must still commit, so the
// caller must not roll back on it.
func commitDatasetPageTx(
	ctx context.Context,
	tx *sql.Tx,
	commit DatasetPageCommit,
	now time.Time,
) (typedErr error, err error) {
	repoID, itemNumber, currentRevision, err := domainParentRowTx(ctx, tx, commit.Parent)
	if err != nil {
		return nil, err
	}
	key := ArchiveDatasetProgressKey{
		RepoID:     repoID,
		ItemType:   commit.Parent.ItemType,
		ItemNumber: itemNumber,
		Dataset:    commit.Dataset,
	}
	if currentRevision != commit.ExpectedRevision {
		if err := reopenDatasetForRevisionTx(ctx, tx, key, currentRevision, now); err != nil {
			return nil, err
		}
		return &StaleDatasetProgressError{
			RepoID: repoID, ItemType: key.ItemType, ItemNumber: itemNumber, Dataset: commit.Dataset,
			ExpectedRevision: commit.ExpectedRevision, GotRevision: currentRevision,
		}, nil
	}
	pageCount := 0
	if commit.Progress != nil {
		if commit.Progress.RepoID != repoID || commit.Progress.ItemNumber != itemNumber {
			return nil, fmt.Errorf(
				"commit dataset page: progress repo %d item %d does not match parent repo %d item %d",
				commit.Progress.RepoID, commit.Progress.ItemNumber, repoID, itemNumber,
			)
		}
		outcome, err := checkDatasetProgressAdvanceTx(ctx, tx, key, commit, currentRevision, now)
		if err != nil {
			return nil, err
		}
		if outcome.typedErr != nil || outcome.replay {
			return outcome.typedErr, nil
		}
		pageCount = outcome.newPageCount
	}
	if err := upsertDatasetRowsTx(ctx, tx, commit); err != nil {
		return nil, err
	}
	if commit.Final && commit.Dataset == ArchiveDatasetComments {
		if err := reconcileOrdinaryCommentsTx(ctx, tx, commit.Parent, commit.ScanGeneration); err != nil {
			return nil, err
		}
	}
	if commit.Progress != nil {
		return nil, advanceDatasetProgressTx(ctx, tx, key, commit, pageCount, now)
	}
	if commit.Final {
		return nil, satisfyDatasetProgressFromLiveTx(ctx, tx, key, currentRevision, now)
	}
	return nil, nil
}

type datasetAdvanceOutcome struct {
	typedErr     error
	replay       bool
	newPageCount int
}

// checkDatasetProgressAdvanceTx applies the cursor and generation
// compare-and-swap. Progress rows persist both the next cursor and the last
// committed input cursor, which makes duplicate delivery distinguishable from
// unrelated stale delivery.
func checkDatasetProgressAdvanceTx(
	ctx context.Context,
	tx *sql.Tx,
	key ArchiveDatasetProgressKey,
	commit DatasetPageCommit,
	currentRevision int64,
	now time.Time,
) (datasetAdvanceOutcome, error) {
	var boundRevision, generation int64
	var nextCursor, lastInputCursor, lastErrorCode sql.NullString
	var pageCount int
	var status ArchiveDatasetProgressStatus
	err := tx.QueryRowContext(ctx, `
		SELECT parent_revision, scan_generation, next_cursor, last_input_cursor,
			page_count, status, last_error_code
		FROM middleman_archive_dataset_progress
		WHERE repo_id = ? AND item_type = ? AND item_number = ? AND dataset = ?`,
		key.RepoID, key.ItemType, key.ItemNumber, key.Dataset,
	).Scan(&boundRevision, &generation, &nextCursor, &lastInputCursor, &pageCount, &status, &lastErrorCode)
	if errors.Is(err, sql.ErrNoRows) {
		return datasetAdvanceOutcome{typedErr: &StaleDatasetProgressError{
			RepoID: key.RepoID, ItemType: key.ItemType, ItemNumber: key.ItemNumber, Dataset: key.Dataset,
			ExpectedGeneration: commit.ScanGeneration,
		}}, nil
	}
	if err != nil {
		return datasetAdvanceOutcome{}, fmt.Errorf("read dataset progress: %w", err)
	}
	if status == ArchiveDatasetProgressBlocked {
		reason := "blocked"
		if lastErrorCode.Valid && lastErrorCode.String != "" {
			reason = lastErrorCode.String
		}
		return datasetAdvanceOutcome{typedErr: &ScanBlockedError{
			Scope:  datasetProgressScope(key),
			Reason: reason,
		}}, nil
	}
	if generation != commit.ScanGeneration || boundRevision != currentRevision {
		return datasetAdvanceOutcome{typedErr: &StaleDatasetProgressError{
			RepoID: key.RepoID, ItemType: key.ItemType, ItemNumber: key.ItemNumber, Dataset: key.Dataset,
			ExpectedRevision: currentRevision, GotRevision: boundRevision,
			ExpectedGeneration: commit.ScanGeneration, GotGeneration: generation,
		}}, nil
	}
	if lastInputCursor.Valid && commit.Progress.InputCursor == lastInputCursor.String {
		// The already-committed page: idempotent replay writes nothing.
		return datasetAdvanceOutcome{replay: true}, nil
	}
	if commit.Progress.InputCursor != nextCursor.String {
		return datasetAdvanceOutcome{typedErr: &StaleDatasetProgressError{
			RepoID: key.RepoID, ItemType: key.ItemType, ItemNumber: key.ItemNumber, Dataset: key.Dataset,
			ExpectedGeneration: commit.ScanGeneration, GotGeneration: generation,
			ExpectedCursor: nextCursor.String, GotCursor: commit.Progress.InputCursor,
		}}, nil
	}
	newPageCount := pageCount + 1
	if newPageCount > maxScanPages {
		if err := blockDatasetProgressTx(
			ctx, tx, key, datasetErrorCodePageBound,
			fmt.Sprintf("scan exceeded %d pages in one generation", maxScanPages), now,
		); err != nil {
			return datasetAdvanceOutcome{}, err
		}
		return datasetAdvanceOutcome{typedErr: &ScanBlockedError{
			Scope:  datasetProgressScope(key),
			Reason: datasetErrorCodePageBound,
		}}, nil
	}
	if !commit.Final && commit.Progress.NextCursor == commit.Progress.InputCursor {
		// The provider echoed the input cursor without exhaustion: a directly
		// detected in-window cycle.
		if err := blockDatasetProgressTx(
			ctx, tx, key, datasetErrorCodeInvalidCursor,
			"provider echoed the input cursor without exhaustion", now,
		); err != nil {
			return datasetAdvanceOutcome{}, err
		}
		return datasetAdvanceOutcome{typedErr: &ScanBlockedError{
			Scope:  datasetProgressScope(key),
			Reason: datasetErrorCodeInvalidCursor,
		}}, nil
	}
	return datasetAdvanceOutcome{newPageCount: newPageCount}, nil
}

func datasetProgressScope(key ArchiveDatasetProgressKey) string {
	return fmt.Sprintf("repo %d %s %d %s", key.RepoID, key.ItemType, key.ItemNumber, key.Dataset)
}

// upsertDatasetRowsTx writes one page of normalized child rows scoped to the
// commit's parent. Ordinary comments are stamped with the commit's scan
// generation; reviews and threads are stable-identity additive history.
func upsertDatasetRowsTx(ctx context.Context, tx *sql.Tx, commit DatasetPageCommit) error {
	switch commit.Dataset {
	case ArchiveDatasetComments:
		if commit.Parent.ItemType == ArchiveItemTypeIssue {
			events := commit.Rows.IssueComments
			for i := range events {
				events[i].IssueID = commit.Parent.ID
			}
			if err := upsertIssueEventsTx(ctx, tx, events); err != nil {
				return err
			}
			return stampIssueEventGenerationTx(ctx, tx, commit.Parent.ID, events, commit.ScanGeneration)
		}
		events := commit.Rows.MRComments
		for i := range events {
			events[i].MergeRequestID = commit.Parent.ID
		}
		if err := upsertMREventsTx(ctx, tx, events); err != nil {
			return err
		}
		return stampMREventGenerationTx(ctx, tx, commit.Parent.ID, events, commit.ScanGeneration)
	case ArchiveDatasetReviews:
		events := commit.Rows.Reviews
		for i := range events {
			events[i].MergeRequestID = commit.Parent.ID
		}
		return upsertMREventsTx(ctx, tx, events)
	case ArchiveDatasetInlineComments:
		if err := upsertMRReviewThreadsTx(ctx, tx, commit.Parent.ID, commit.Rows.ReviewThreads); err != nil {
			return err
		}
		events := commit.Rows.ThreadEvents
		for i := range events {
			events[i].MergeRequestID = commit.Parent.ID
		}
		return upsertMREventsTx(ctx, tx, events)
	default:
		return fmt.Errorf("commit dataset page: invalid dataset %q", commit.Dataset)
	}
}

func stampIssueEventGenerationTx(
	ctx context.Context,
	tx *sql.Tx,
	issueID int64,
	events []IssueEvent,
	generation int64,
) error {
	if len(events) == 0 {
		return nil
	}
	keys := make([]string, len(events))
	for i := range events {
		keys[i] = events[i].DedupeKey
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE middleman_issue_events SET ingest_generation = ?
		WHERE issue_id = ? AND dedupe_key IN (SELECT value FROM json_each(?))`,
		generation, issueID, jsonStringList(keys)); err != nil {
		return fmt.Errorf("stamp issue comment ingest generation: %w", err)
	}
	return nil
}

func stampMREventGenerationTx(
	ctx context.Context,
	tx *sql.Tx,
	mrID int64,
	events []MREvent,
	generation int64,
) error {
	if len(events) == 0 {
		return nil
	}
	keys := make([]string, len(events))
	for i := range events {
		keys[i] = events[i].DedupeKey
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE middleman_mr_events SET ingest_generation = ?
		WHERE merge_request_id = ? AND dedupe_key IN (SELECT value FROM json_each(?))`,
		generation, mrID, jsonStringList(keys)); err != nil {
		return fmt.Errorf("stamp merge-request comment ingest generation: %w", err)
	}
	return nil
}

// reconcileOrdinaryCommentsTx deletes ordinary comments of one parent that
// were not observed in the completed scan generation. It runs only on the
// exhausted page, so a partial scan never deletes anything.
func reconcileOrdinaryCommentsTx(
	ctx context.Context,
	tx *sql.Tx,
	parent DomainParentRef,
	generation int64,
) error {
	query := `
		DELETE FROM middleman_issue_events
		WHERE issue_id = ? AND event_type = 'issue_comment'
		  AND (ingest_generation IS NULL OR ingest_generation <> ?)`
	if parent.ItemType == ArchiveItemTypeMergeRequest {
		query = `
			DELETE FROM middleman_mr_events
			WHERE merge_request_id = ? AND event_type = 'issue_comment'
			  AND (ingest_generation IS NULL OR ingest_generation <> ?)`
	}
	if _, err := tx.ExecContext(ctx, query, parent.ID, generation); err != nil {
		return fmt.Errorf("reconcile ordinary comments at exhaustion: %w", err)
	}
	return nil
}

func advanceDatasetProgressTx(
	ctx context.Context,
	tx *sql.Tx,
	key ArchiveDatasetProgressKey,
	commit DatasetPageCommit,
	newPageCount int,
	now time.Time,
) error {
	observed := datasetRowCount(commit.Rows)
	nowText := formatDatasetProgressTime(now)
	if commit.Final {
		_, err := tx.ExecContext(ctx, `
			UPDATE middleman_archive_dataset_progress
			SET next_cursor = NULL, last_input_cursor = NULL, page_count = ?,
				status = 'complete', observed_count = observed_count + ?,
				next_retry_at = NULL, last_error_code = NULL, last_error_detail = NULL,
				started_at = COALESCE(started_at, ?), completed_at = ?, updated_at = ?
			WHERE repo_id = ? AND item_type = ? AND item_number = ? AND dataset = ?`,
			newPageCount, observed, nowText, nowText, nowText,
			key.RepoID, key.ItemType, key.ItemNumber, key.Dataset)
		if err != nil {
			return fmt.Errorf("complete dataset progress: %w", err)
		}
		return nil
	}
	_, err := tx.ExecContext(ctx, `
		UPDATE middleman_archive_dataset_progress
		SET next_cursor = ?, last_input_cursor = ?, page_count = ?,
			status = 'running', observed_count = observed_count + ?,
			next_retry_at = NULL, last_error_code = NULL, last_error_detail = NULL,
			started_at = COALESCE(started_at, ?), completed_at = NULL, updated_at = ?
		WHERE repo_id = ? AND item_type = ? AND item_number = ? AND dataset = ?`,
		commit.Progress.NextCursor, commit.Progress.InputCursor, newPageCount, observed,
		nowText, nowText,
		key.RepoID, key.ItemType, key.ItemNumber, key.Dataset)
	if err != nil {
		return fmt.Errorf("advance dataset progress: %w", err)
	}
	return nil
}

// satisfyDatasetProgressFromLiveTx conditionally completes matching archive
// progress after a complete live observation. Missing, paused, stale, blocked,
// or terminal progress leaves the row untouched and never rejects the live
// domain write.
func satisfyDatasetProgressFromLiveTx(
	ctx context.Context,
	tx *sql.Tx,
	key ArchiveDatasetProgressKey,
	currentRevision int64,
	now time.Time,
) error {
	nowText := formatDatasetProgressTime(now)
	_, err := tx.ExecContext(ctx, `
		UPDATE middleman_archive_dataset_progress
		SET status = 'complete', next_cursor = NULL, last_input_cursor = NULL,
			next_retry_at = NULL, last_error_code = NULL, last_error_detail = NULL,
			completed_at = ?, updated_at = ?
		WHERE repo_id = ? AND item_type = ? AND item_number = ? AND dataset = ?
		  AND parent_revision = ?
		  AND status IN ('pending', 'running', 'failed')
		  AND EXISTS (
			SELECT 1 FROM middleman_archive_repos
			WHERE repo_id = ? AND operator_state = 'active'
		  )`,
		nowText, nowText,
		key.RepoID, key.ItemType, key.ItemNumber, key.Dataset,
		currentRevision, key.RepoID)
	if err != nil {
		return fmt.Errorf("satisfy dataset progress from live sync: %w", err)
	}
	return nil
}

// reopenDatasetForRevisionTx rebinds one dataset's progress to a newer parent
// revision after a stale page commit: new generation, cleared cursor, pending
// status. Already-upserted additive history and unreconciled comments are
// retained.
func reopenDatasetForRevisionTx(
	ctx context.Context,
	tx *sql.Tx,
	key ArchiveDatasetProgressKey,
	newRevision int64,
	now time.Time,
) error {
	_, err := tx.ExecContext(ctx, `
		UPDATE middleman_archive_dataset_progress
		SET scan_generation = scan_generation + 1,
			parent_revision = ?,
			next_cursor = NULL, last_input_cursor = NULL,
			page_count = 0, observed_count = 0,
			status = 'pending', attempt_count = 0, next_retry_at = NULL,
			last_error_code = NULL, last_error_detail = NULL,
			started_at = NULL, completed_at = NULL, updated_at = ?
		WHERE repo_id = ? AND item_type = ? AND item_number = ? AND dataset = ?
		  AND parent_revision <> ?
		  AND status NOT IN ('unsupported', 'terminal', 'blocked')`,
		newRevision, formatDatasetProgressTime(now),
		key.RepoID, key.ItemType, key.ItemNumber, key.Dataset, newRevision)
	if err != nil {
		return fmt.Errorf("reopen dataset progress for new parent revision: %w", err)
	}
	return nil
}

// ReopenDatasetsForParent returns every superseded child dataset of one parent
// to pending for a newer parent revision: generation incremented, cursor
// cleared, existing rows retained until the new generation completes and
// reconciles.
func (d *DB) ReopenDatasetsForParent(
	ctx context.Context,
	tx *sql.Tx,
	parent DomainParentRef,
	repoID int64,
	itemNumber int,
	newRevision int64,
) error {
	return reopenDatasetsForParentTx(ctx, tx, parent, repoID, itemNumber, newRevision)
}

func reopenDatasetsForParentTx(
	ctx context.Context,
	tx *sql.Tx,
	parent DomainParentRef,
	repoID int64,
	itemNumber int,
	newRevision int64,
) error {
	_, err := tx.ExecContext(ctx, `
		UPDATE middleman_archive_dataset_progress
		SET scan_generation = scan_generation + 1,
			parent_revision = ?,
			next_cursor = NULL, last_input_cursor = NULL,
			page_count = 0, observed_count = 0,
			status = 'pending', attempt_count = 0, next_retry_at = NULL,
			last_error_code = NULL, last_error_detail = NULL,
			started_at = NULL, completed_at = NULL, updated_at = ?
		WHERE repo_id = ? AND item_type = ? AND item_number = ?
		  AND dataset IN ('comments', 'reviews', 'inline_comments')
		  AND parent_revision < ?
		  AND status NOT IN ('unsupported', 'terminal', 'blocked')`,
		newRevision, formatDatasetProgressTime(time.Now()),
		repoID, parent.ItemType, itemNumber, newRevision)
	if err != nil {
		return fmt.Errorf("reopen child datasets for parent revision %d: %w", newRevision, err)
	}
	return nil
}

// CommitParentLookup is the single-item mode of the canonical
// parent-observation operation: one parent from a lookup outcome, advancing
// that item's lookup dataset progress. It shares the parent upsert and
// work-reconciliation core with CommitArchiveInventoryPage.
func (d *DB) CommitParentLookup(ctx context.Context, commit ParentLookupCommit) error {
	if err := validateParentLookupCommit(&commit); err != nil {
		return err
	}
	now := commit.Now
	if now.IsZero() {
		now = time.Now()
	}
	now = now.UTC()
	var typedErr error
	err := d.Tx(ctx, func(tx *sql.Tx) error {
		typedErr = nil
		currentGeneration := int64(1)
		err := tx.QueryRowContext(ctx, `
			SELECT scan_generation FROM middleman_archive_dataset_progress
			WHERE repo_id = ? AND item_type = ? AND item_number = ? AND dataset = 'lookup'`,
			commit.RepoID, commit.ItemType, commit.ItemNumber,
		).Scan(&currentGeneration)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("read lookup progress generation: %w", err)
		}
		if commit.ScanGeneration != currentGeneration {
			// A lookup completed after a forced reset must not mark the reset
			// progress terminal or upsert a stale parent.
			typedErr = &StaleDatasetProgressError{
				RepoID: commit.RepoID, ItemType: commit.ItemType,
				ItemNumber: commit.ItemNumber, Dataset: ArchiveDatasetLookup,
				ExpectedGeneration: commit.ScanGeneration, GotGeneration: currentGeneration,
			}
			return nil
		}
		if commit.Outcome == ArchiveLookupPresent {
			return commitPresentParentLookupTx(ctx, tx, commit, now)
		}
		return commitTerminalParentLookupTx(ctx, tx, commit, now)
	})
	if err != nil {
		return err
	}
	return typedErr
}

func validateParentLookupCommit(commit *ParentLookupCommit) error {
	if commit.RepoID <= 0 || commit.ItemNumber <= 0 {
		return errors.New("commit parent lookup: repo ID and item number are required")
	}
	switch commit.ItemType {
	case ArchiveItemTypeIssue, ArchiveItemTypeMergeRequest:
	default:
		return fmt.Errorf("commit parent lookup: invalid item type %q", commit.ItemType)
	}
	switch commit.Outcome {
	case ArchiveLookupPresent:
		if commit.ItemType == ArchiveItemTypeIssue {
			if commit.Issue == nil || commit.MergeRequest != nil {
				return errors.New("commit parent lookup: present issue lookup requires an issue snapshot")
			}
			if commit.Issue.Number != commit.ItemNumber {
				return fmt.Errorf(
					"commit parent lookup: issue snapshot number %d does not match item %d",
					commit.Issue.Number, commit.ItemNumber,
				)
			}
		} else {
			if commit.MergeRequest == nil || commit.Issue != nil {
				return errors.New("commit parent lookup: present merge-request lookup requires a merge-request snapshot")
			}
			if commit.MergeRequest.Number != commit.ItemNumber {
				return fmt.Errorf(
					"commit parent lookup: merge-request snapshot number %d does not match item %d",
					commit.MergeRequest.Number, commit.ItemNumber,
				)
			}
		}
	case ArchiveLookupMoved:
		if commit.Destination == nil {
			return errors.New("commit parent lookup: moved lookup requires a destination repository")
		}
	case ArchiveLookupRemoved, ArchiveLookupInaccessible:
	default:
		return fmt.Errorf("commit parent lookup: invalid outcome %q", commit.Outcome)
	}
	return nil
}

func commitPresentParentLookupTx(
	ctx context.Context,
	tx *sql.Tx,
	commit ParentLookupCommit,
	now time.Time,
) error {
	var parentID, revision int64
	var err error
	if commit.ItemType == ArchiveItemTypeIssue {
		item := ArchiveInventoryIssue{
			Snapshot: IssueSnapshot{
				Issue:  *commit.Issue,
				Labels: commit.Issue.Labels,
			},
			CommentsStatus: ArchiveDatasetStatusPending,
		}
		parentID, revision, _, err = commitArchiveIssueParentTx(
			ctx, tx, commit.RepoID, &item, ArchiveRefreshReasonInitial,
		)
	} else {
		item := ArchiveInventoryMergeRequest{
			Snapshot: MergeRequestSnapshot{
				MergeRequest: *commit.MergeRequest,
				Labels:       commit.MergeRequest.Labels,
			},
			CommentsStatus:       ArchiveDatasetStatusPending,
			ReviewsStatus:        ArchiveDatasetStatusPending,
			InlineCommentsStatus: ArchiveDatasetStatusPending,
		}
		parentID, revision, _, err = commitArchiveMergeRequestParentTx(
			ctx, tx, commit.RepoID, &item, ArchiveRefreshReasonInitial,
		)
	}
	if err != nil {
		return err
	}
	nowText := formatDatasetProgressTime(now)
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO middleman_archive_dataset_progress (
			repo_id, item_type, item_number, dataset,
			parent_revision, scan_generation, status, completed_at, updated_at
		) VALUES (?, ?, ?, 'lookup', ?, ?, 'complete', ?, ?)
		ON CONFLICT(repo_id, item_type, item_number, dataset) DO UPDATE SET
			parent_revision = excluded.parent_revision,
			status = 'complete',
			next_cursor = NULL, last_input_cursor = NULL,
			next_retry_at = NULL, last_error_code = NULL, last_error_detail = NULL,
			completed_at = excluded.completed_at,
			updated_at = excluded.updated_at`,
		commit.RepoID, commit.ItemType, commit.ItemNumber,
		revision, commit.ScanGeneration, nowText, nowText,
	); err != nil {
		return fmt.Errorf("bind lookup progress to parent revision: %w", err)
	}
	return reopenDatasetsForParentTx(
		ctx, tx,
		DomainParentRef{ItemType: commit.ItemType, ID: parentID},
		commit.RepoID, commit.ItemNumber, revision,
	)
}

func commitTerminalParentLookupTx(
	ctx context.Context,
	tx *sql.Tx,
	commit ParentLookupCommit,
	now time.Time,
) error {
	lifecycle := ArchiveLifecycleStateRemovedUpstream
	if commit.Outcome == ArchiveLookupInaccessible {
		lifecycle = ArchiveLifecycleStateInaccessible
	}
	if err := markArchiveItemTerminalTx(ctx, tx, ArchiveItemTerminal{
		RepoID:      commit.RepoID,
		ItemType:    commit.ItemType,
		ItemNumber:  commit.ItemNumber,
		Lifecycle:   lifecycle,
		ErrorCode:   commit.ErrorCode,
		ErrorDetail: commit.ErrorDetail,
		At:          now,
	}); err != nil {
		return err
	}
	nowText := formatDatasetProgressTime(now)
	errorCode := nullableArchiveError(commit.ErrorCode)
	errorDetail := nullableArchiveError(sanitizeArchiveErrorDetail(commit.ErrorDetail))
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO middleman_archive_dataset_progress (
			repo_id, item_type, item_number, dataset,
			scan_generation, status, last_error_code, last_error_detail,
			completed_at, updated_at
		) VALUES (?, ?, ?, 'lookup', ?, 'terminal', ?, ?, ?, ?)
		ON CONFLICT(repo_id, item_type, item_number, dataset) DO UPDATE SET
			status = 'terminal',
			next_cursor = NULL, last_input_cursor = NULL,
			next_retry_at = NULL,
			last_error_code = excluded.last_error_code,
			last_error_detail = excluded.last_error_detail,
			completed_at = excluded.completed_at,
			updated_at = excluded.updated_at`,
		commit.RepoID, commit.ItemType, commit.ItemNumber,
		commit.ScanGeneration, errorCode, errorDetail, nowText, nowText,
	); err != nil {
		return fmt.Errorf("mark lookup progress terminal: %w", err)
	}
	if commit.Outcome == ArchiveLookupMoved {
		return queueArchivePromptByIdentity(ctx, tx, *commit.Destination, now)
	}
	return nil
}

// GetDatasetProgress returns one durable dataset progress row.
func (d *DB) GetDatasetProgress(
	ctx context.Context,
	repoID int64,
	itemType ArchiveItemType,
	itemNumber int,
	dataset ArchiveDataset,
) (ArchiveDatasetProgress, error) {
	var progress ArchiveDatasetProgress
	var nextCursor, lastInputCursor sql.NullString
	var nextRetryAt, lastErrorCode, lastErrorDetail sql.NullString
	var startedAt, completedAt sql.NullString
	var updatedAt string
	err := d.ro.QueryRowContext(ctx, `
		SELECT repo_id, item_type, item_number, dataset,
			parent_revision, scan_generation, next_cursor, last_input_cursor,
			page_count, status, observed_count, attempt_count,
			next_retry_at, last_error_code, last_error_detail,
			started_at, completed_at, updated_at
		FROM middleman_archive_dataset_progress
		WHERE repo_id = ? AND item_type = ? AND item_number = ? AND dataset = ?`,
		repoID, itemType, itemNumber, dataset,
	).Scan(
		&progress.RepoID, &progress.ItemType, &progress.ItemNumber, &progress.Dataset,
		&progress.ParentRevision, &progress.ScanGeneration, &nextCursor, &lastInputCursor,
		&progress.PageCount, &progress.Status, &progress.ObservedCount, &progress.AttemptCount,
		&nextRetryAt, &lastErrorCode, &lastErrorDetail,
		&startedAt, &completedAt, &updatedAt,
	)
	if err != nil {
		return ArchiveDatasetProgress{}, fmt.Errorf(
			"get dataset progress for repo %d %s %d %s: %w",
			repoID, itemType, itemNumber, dataset, err,
		)
	}
	if nextCursor.Valid {
		progress.NextCursor = &nextCursor.String
	}
	if lastInputCursor.Valid {
		progress.LastInputCursor = &lastInputCursor.String
	}
	if lastErrorCode.Valid {
		progress.LastErrorCode = &lastErrorCode.String
	}
	if lastErrorDetail.Valid {
		progress.LastErrorDetail = &lastErrorDetail.String
	}
	if progress.NextRetryAt, err = parseDatasetProgressTimePtr(nextRetryAt); err != nil {
		return ArchiveDatasetProgress{}, fmt.Errorf("parse dataset progress next retry: %w", err)
	}
	if progress.StartedAt, err = parseDatasetProgressTimePtr(startedAt); err != nil {
		return ArchiveDatasetProgress{}, fmt.Errorf("parse dataset progress started at: %w", err)
	}
	if progress.CompletedAt, err = parseDatasetProgressTimePtr(completedAt); err != nil {
		return ArchiveDatasetProgress{}, fmt.Errorf("parse dataset progress completed at: %w", err)
	}
	if progress.UpdatedAt, err = parseDBTime(updatedAt); err != nil {
		return ArchiveDatasetProgress{}, fmt.Errorf("parse dataset progress updated at: %w", err)
	}
	return progress, nil
}

// BlockDatasetProgress durably stops one dataset scan: progress is retained
// for diagnostics, no further provider requests are spent automatically, and
// recovery requires an explicit reset.
func (d *DB) BlockDatasetProgress(
	ctx context.Context,
	key ArchiveDatasetProgressKey,
	code string,
	detail string,
) error {
	return d.Tx(ctx, func(tx *sql.Tx) error {
		return blockDatasetProgressTx(ctx, tx, key, code, detail, time.Now())
	})
}

func blockDatasetProgressTx(
	ctx context.Context,
	tx *sql.Tx,
	key ArchiveDatasetProgressKey,
	code string,
	detail string,
	now time.Time,
) error {
	result, err := tx.ExecContext(ctx, `
		UPDATE middleman_archive_dataset_progress
		SET status = 'blocked', last_error_code = ?, last_error_detail = ?, updated_at = ?
		WHERE repo_id = ? AND item_type = ? AND item_number = ? AND dataset = ?`,
		code, sanitizeArchiveErrorDetail(detail), formatDatasetProgressTime(now),
		key.RepoID, key.ItemType, key.ItemNumber, key.Dataset)
	if err != nil {
		return fmt.Errorf("block dataset progress: %w", err)
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("block dataset progress rows affected: %w", err)
	}
	if changed == 0 {
		return fmt.Errorf("block dataset progress: no progress row for %s", datasetProgressScope(key))
	}
	return nil
}

// ResetDatasetProgress starts a fresh scan generation for one dataset:
// cursor cleared, page count zeroed, status pending. Rows already ingested
// are retained until the new generation completes and reconciles.
func (d *DB) ResetDatasetProgress(ctx context.Context, key ArchiveDatasetProgressKey) error {
	result, err := d.rw.ExecContext(ctx, `
		UPDATE middleman_archive_dataset_progress
		SET scan_generation = scan_generation + 1,
			next_cursor = NULL, last_input_cursor = NULL,
			page_count = 0, observed_count = 0,
			status = 'pending', attempt_count = 0, next_retry_at = NULL,
			last_error_code = NULL, last_error_detail = NULL,
			started_at = NULL, completed_at = NULL, updated_at = ?
		WHERE repo_id = ? AND item_type = ? AND item_number = ? AND dataset = ?`,
		formatDatasetProgressTime(time.Now()),
		key.RepoID, key.ItemType, key.ItemNumber, key.Dataset)
	if err != nil {
		return fmt.Errorf("reset dataset progress: %w", err)
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("reset dataset progress rows affected: %w", err)
	}
	if changed == 0 {
		return fmt.Errorf("reset dataset progress: no progress row for %s", datasetProgressScope(key))
	}
	return nil
}
