package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

const nextEvenScanGenerationSQL = "scan_generation + 2 - (scan_generation % 2)"
const archiveLifecycleDetailsGeneration int64 = 1 << 34
const maxScanPages = 10_000

const (
	datasetErrorCodePageBound     = "page_bound"
	datasetErrorCodeInvalidCursor = "invalid_cursor"
)

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

func datasetProgressScope(key ArchiveDatasetProgressKey) string {
	return fmt.Sprintf("repo %d %s %d %s", key.RepoID, key.ItemType, key.ItemNumber, key.Dataset)
}

func reopenArchiveItemProgressTx(
	ctx context.Context,
	tx *sql.Tx,
	repoID int64,
	itemType ArchiveItemType,
	itemNumber int,
) error {
	_, err := tx.ExecContext(ctx, fmt.Sprintf(`
		UPDATE forge_archive_dataset_progress
		SET scan_generation = %s,
			next_cursor = NULL, last_input_cursor = NULL,
			page_count = 0, observed_count = 0,
			status = 'pending', attempt_count = 0, next_retry_at = NULL,
			last_error_code = NULL, last_error_detail = NULL,
			started_at = NULL, completed_at = NULL, updated_at = ?
		WHERE repo_id = ? AND item_type = ? AND item_number = ? AND dataset = 'lookup'
		  AND status NOT IN ('unsupported', 'blocked')`, nextEvenScanGenerationSQL),
		formatDatasetProgressTime(time.Now()), repoID, itemType, itemNumber)
	if err != nil {
		return fmt.Errorf("reopen archive item progress: %w", err)
	}
	return nil
}

// CommitArchiveItemSync records archive bookkeeping after the existing live
// item sync has performed provider reads, normalization, and persistence.
func (d *DB) CommitArchiveItemSync(ctx context.Context, commit ArchiveItemSyncCommit) error {
	if commit.RepoID == 0 || commit.ItemNumber <= 0 {
		return errors.New("commit archive item sync: repository and item number are required")
	}
	if commit.ItemType != ArchiveItemTypeIssue && commit.ItemType != ArchiveItemTypeMergeRequest {
		return fmt.Errorf("commit archive item sync: invalid item type %q", commit.ItemType)
	}
	switch commit.Outcome {
	case ArchiveLookupPresent, ArchiveLookupRemoved, ArchiveLookupInaccessible:
	case ArchiveLookupMoved:
		if commit.Destination == nil {
			return errors.New("commit archive item sync: moved outcome requires a destination")
		}
	default:
		return fmt.Errorf("commit archive item sync: invalid outcome %q", commit.Outcome)
	}
	commit.Now = canonicalUTCTime(commit.Now)
	var typedErr error
	err := d.Tx(ctx, func(tx *sql.Tx) error {
		var generation int64
		var status ArchiveDatasetProgressStatus
		err := tx.QueryRowContext(ctx, `
			SELECT scan_generation, status
			FROM forge_archive_dataset_progress
			WHERE repo_id = ? AND item_type = ? AND item_number = ? AND dataset = 'lookup'`,
			commit.RepoID, commit.ItemType, commit.ItemNumber,
		).Scan(&generation, &status)
		if err != nil {
			return fmt.Errorf("read archive item sync progress: %w", err)
		}
		if generation != commit.ScanGeneration {
			typedErr = &StaleDatasetProgressError{
				RepoID: commit.RepoID, ItemType: commit.ItemType,
				ItemNumber: commit.ItemNumber, Dataset: ArchiveDatasetLookup,
				ExpectedGeneration: commit.ScanGeneration, GotGeneration: generation,
			}
			return nil
		}
		if status == ArchiveDatasetProgressBlocked {
			typedErr = &ScanBlockedError{Scope: datasetProgressScope(ArchiveDatasetProgressKey{
				RepoID: commit.RepoID, ItemType: commit.ItemType,
				ItemNumber: commit.ItemNumber, Dataset: ArchiveDatasetLookup,
			}), Reason: "blocked"}
			return nil
		}
		if commit.Outcome == ArchiveLookupPresent {
			if status == ArchiveDatasetProgressComplete {
				return nil
			}
			revision, err := lookupDomainRevisionTx(
				ctx, tx, commit.RepoID, commit.ItemType, commit.ItemNumber,
			)
			if err != nil {
				return err
			}
			if err := reactivateTerminalArchiveWorkTx(
				ctx, tx, commit.RepoID, commit.ItemType, commit.ItemNumber,
			); err != nil {
				return err
			}
			nowText := formatDatasetProgressTime(commit.Now)
			if _, err := tx.ExecContext(ctx, `
				UPDATE forge_archive_dataset_progress
				SET parent_revision = ?,
					scan_generation = MAX(scan_generation, ?),
					status = 'complete',
					next_cursor = NULL, last_input_cursor = NULL,
					attempt_count = 0, next_retry_at = NULL,
					last_error_code = NULL, last_error_detail = NULL,
					completed_at = ?, updated_at = ?
				WHERE repo_id = ? AND item_type = ? AND item_number = ? AND dataset = 'lookup'`,
				revision, archiveLifecycleDetailsGeneration, nowText, nowText,
				commit.RepoID, commit.ItemType, commit.ItemNumber,
			); err != nil {
				return fmt.Errorf("complete archive item sync: %w", err)
			}
			if err := clearArchiveRepositoryFailureTx(ctx, tx, commit.RepoID, commit.Now); err != nil {
				return err
			}
			return completeArchiveInitialIfReadyTx(ctx, tx, commit.RepoID, commit.Now)
		}

		lifecycle := ArchiveLifecycleStateRemovedUpstream
		if commit.Outcome == ArchiveLookupInaccessible {
			lifecycle = ArchiveLifecycleStateInaccessible
		}
		if err := markArchiveItemTerminalTx(ctx, tx, ArchiveItemTerminal{
			RepoID: commit.RepoID, ItemType: commit.ItemType,
			ItemNumber: commit.ItemNumber, Lifecycle: lifecycle, At: commit.Now,
		}); err != nil {
			return err
		}
		nowText := formatDatasetProgressTime(commit.Now)
		if _, err := tx.ExecContext(ctx, `
			UPDATE forge_archive_dataset_progress
			SET status = 'terminal', next_cursor = NULL, last_input_cursor = NULL,
				next_retry_at = NULL, last_error_code = ?, last_error_detail = ?,
				completed_at = ?, updated_at = ?
			WHERE repo_id = ? AND item_type = ? AND item_number = ? AND dataset = 'lookup'`,
			nullableArchiveError(commit.ErrorCode),
			nullableArchiveError(sanitizeArchiveErrorDetail(commit.ErrorDetail)),
			nowText, nowText, commit.RepoID, commit.ItemType, commit.ItemNumber,
		); err != nil {
			return fmt.Errorf("complete terminal archive item sync: %w", err)
		}
		if commit.Outcome == ArchiveLookupMoved {
			return queueArchivePromptByIdentity(ctx, tx, *commit.Destination, commit.Now)
		}
		return nil
	})
	if err != nil {
		return err
	}
	return typedErr
}

// FailArchiveItemSync applies one retry boundary to the item-level sync.
func (d *DB) FailArchiveItemSync(
	ctx context.Context,
	commit ArchiveItemSyncCommit,
	code ArchiveErrorCode,
	retryAt *time.Time,
	repositoryFailure bool,
) error {
	commit.Now = canonicalUTCTime(commit.Now)
	return d.Tx(ctx, func(tx *sql.Tx) error {
		var generation int64
		if err := tx.QueryRowContext(ctx, `
			SELECT scan_generation FROM forge_archive_dataset_progress
			WHERE repo_id = ? AND item_type = ? AND item_number = ? AND dataset = 'lookup'`,
			commit.RepoID, commit.ItemType, commit.ItemNumber,
		).Scan(&generation); err != nil {
			return fmt.Errorf("read failed archive item sync progress: %w", err)
		}
		if generation != commit.ScanGeneration {
			return nil
		}
		var retry any
		if retryAt != nil {
			retry = formatDatasetProgressTime(*retryAt)
		}
		result, err := tx.ExecContext(ctx, `
			UPDATE forge_archive_dataset_progress
			SET status = 'failed', attempt_count = attempt_count + 1,
				next_retry_at = ?, last_error_code = ?, last_error_detail = ?, updated_at = ?
			WHERE repo_id = ? AND item_type = ? AND item_number = ?
			  AND status IN ('pending', 'running', 'failed')`,
			retry, code, sanitizeArchiveErrorDetail(commit.ErrorDetail),
			formatDatasetProgressTime(commit.Now),
			commit.RepoID, commit.ItemType, commit.ItemNumber,
		)
		if err != nil {
			return fmt.Errorf("fail archive item sync: %w", err)
		}
		changed, err := result.RowsAffected()
		if err != nil {
			return fmt.Errorf("fail archive item sync rows affected: %w", err)
		}
		if changed == 0 || !repositoryFailure {
			return nil
		}
		return recordArchiveRepositoryFailureTx(
			ctx, tx, commit.RepoID, code, commit.ErrorDetail, retryAt, commit.Now,
		)
	})
}

func lookupDomainRevisionTx(
	ctx context.Context,
	tx *sql.Tx,
	repoID int64,
	itemType ArchiveItemType,
	itemNumber int,
) (int64, error) {
	table := "forge_issues"
	if itemType == ArchiveItemTypeMergeRequest {
		table = "forge_merge_requests"
	} else if itemType != ArchiveItemTypeIssue {
		return 0, fmt.Errorf("read archive item revision: invalid item type %q", itemType)
	}
	var revision int64
	err := tx.QueryRowContext(ctx, fmt.Sprintf(
		`SELECT snapshot_revision FROM %s WHERE repo_id = ? AND number = ?`, table),
		repoID, itemNumber,
	).Scan(&revision)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("read archive item revision: %w", err)
	}
	return revision, nil
}

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
		FROM forge_archive_dataset_progress
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
