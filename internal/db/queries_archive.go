package db

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"
	"unicode"
)

func jsonStringList(values []string) string {
	payload, err := json.Marshal(values)
	if err != nil {
		panic(fmt.Sprintf("marshal string list: %v", err))
	}
	return string(payload)
}

// EnsureDiscoveryArchives creates discovery state for configured repositories
// while preserving any durable state that already exists.
func (d *DB) EnsureDiscoveryArchives(ctx context.Context, repoIDs []int64, now time.Time) error {
	repoIDs = normalizedArchiveRepoIDs(repoIDs)
	now = now.UTC()
	return d.Tx(ctx, func(tx *sql.Tx) error {
		if len(repoIDs) > 0 {
			if err := requireArchiveRepoIDs(ctx, tx, "forge_repos", repoIDs); err != nil {
				return err
			}
		}
		for _, repoID := range repoIDs {
			_, err := tx.ExecContext(ctx, `
				INSERT INTO forge_archive_repos (
					repo_id, collection_mode, operator_state,
					initial_started_at, initial_completed_at,
					maintenance_watermark, maintenance_succeeded_at,
					issues_coverage, merge_requests_coverage,
					comments_coverage, reviews_coverage, inline_comments_coverage,
					last_error_code, last_error_detail, next_retry_at,
					created_at, updated_at
				) VALUES (
					?, 'discovery', 'active',
					NULL, NULL, NULL, NULL,
					'unknown', 'unknown',
					'unknown', 'unknown', 'unknown',
					NULL, NULL, NULL, ?, ?
				)
				ON CONFLICT(repo_id) DO UPDATE SET
					operator_state = CASE
						WHEN forge_archive_repos.last_error_code = 'configuration_removed' THEN 'active'
						ELSE forge_archive_repos.operator_state END,
					last_error_code = CASE
						WHEN forge_archive_repos.last_error_code = 'configuration_removed' THEN NULL
						ELSE forge_archive_repos.last_error_code END,
					last_error_detail = CASE
						WHEN forge_archive_repos.last_error_code = 'configuration_removed' THEN NULL
						ELSE forge_archive_repos.last_error_detail END,
					next_retry_at = CASE
						WHEN forge_archive_repos.last_error_code = 'configuration_removed' THEN NULL
						ELSE forge_archive_repos.next_retry_at END,
					updated_at = CASE
						WHEN forge_archive_repos.last_error_code = 'configuration_removed' THEN excluded.updated_at
						ELSE forge_archive_repos.updated_at END`, repoID, now, now)
			if err != nil {
				return fmt.Errorf("ensure discovery archive for repo %d: %w", repoID, err)
			}
			if err := ensureArchiveRepoScansTx(ctx, tx, repoID, now); err != nil {
				return err
			}
		}
		return nil
	})
}

// ReconcileDiscoveryArchives ensures current repositories and automatically
// pauses durable archive state for repositories removed from configuration.
func (d *DB) ReconcileDiscoveryArchives(ctx context.Context, repoIDs []int64, now time.Time) error {
	repoIDs = normalizedArchiveRepoIDs(repoIDs)
	now = now.UTC()
	if err := d.Tx(ctx, func(tx *sql.Tx) error {
		query := `UPDATE forge_archive_repos
			SET operator_state = 'paused', last_error_code = ?,
				last_error_detail = 'repository is no longer configured',
				next_retry_at = NULL, updated_at = ?
			WHERE operator_state = 'active'
			  AND (last_error_code <> ? OR last_error_code IS NULL)`
		args := []any{ArchiveErrorCodeConfigurationRemoved, now, ArchiveErrorCodeConfigurationRemoved}
		if len(repoIDs) > 0 {
			query += " AND repo_id NOT IN (" + sqlPlaceholders(len(repoIDs)) + ")"
			args = append(args, archiveRepoIDArgs(repoIDs)...)
		}
		_, err := tx.ExecContext(ctx, query, args...)
		return err
	}); err != nil {
		return fmt.Errorf("reconcile removed archive repositories: %w", err)
	}
	return d.EnsureDiscoveryArchives(ctx, repoIDs, now)
}

// StartFullArchives atomically promotes or resumes selected archive states.
func (d *DB) StartFullArchives(ctx context.Context, repoIDs []int64, now time.Time) error {
	repoIDs = normalizedArchiveRepoIDs(repoIDs)
	now = now.UTC()
	if len(repoIDs) == 0 {
		return nil
	}
	return d.Tx(ctx, func(tx *sql.Tx) error {
		if err := requireArchiveRepoIDs(ctx, tx, "forge_archive_repos", repoIDs); err != nil {
			return err
		}
		args := archiveRepoIDArgs(repoIDs)
		if _, err := tx.ExecContext(ctx, fmt.Sprintf(`
			UPDATE forge_archive_items
			SET refresh_reason = 'initial'
			WHERE lifecycle_state = 'active'
			  AND repo_id IN (%s)
			  AND repo_id IN (
				SELECT repo_id FROM forge_archive_repos WHERE collection_mode = 'discovery'
			  )`, sqlPlaceholders(len(repoIDs))), args...); err != nil {
			return fmt.Errorf("queue promoted archive items: %w", err)
		}
		promoteArgs := append([]any{formatDatasetProgressTime(now)}, args...)
		if _, err := tx.ExecContext(ctx, fmt.Sprintf(`
			UPDATE forge_archive_dataset_progress
			SET scan_generation = CASE WHEN status = 'complete' THEN %s ELSE scan_generation END,
				next_cursor = CASE WHEN status = 'complete' THEN NULL ELSE next_cursor END,
				last_input_cursor = CASE WHEN status = 'complete' THEN NULL ELSE last_input_cursor END,
				page_count = CASE WHEN status = 'complete' THEN 0 ELSE page_count END,
				observed_count = CASE WHEN status = 'complete' THEN 0 ELSE observed_count END,
				status = 'pending', attempt_count = 0, next_retry_at = NULL,
				last_error_code = NULL, last_error_detail = NULL,
				started_at = CASE WHEN status = 'complete' THEN NULL ELSE started_at END,
				completed_at = NULL, updated_at = ?
			WHERE status IN ('pending', 'complete', 'failed')
			  AND repo_id IN (%s)
			  AND repo_id IN (
				SELECT repo_id FROM forge_archive_repos WHERE collection_mode = 'discovery'
			  )
			  AND EXISTS (
				SELECT 1 FROM forge_archive_items ai
				WHERE ai.repo_id = forge_archive_dataset_progress.repo_id
				  AND ai.item_type = forge_archive_dataset_progress.item_type
				  AND ai.item_number = forge_archive_dataset_progress.item_number
				  AND ai.lifecycle_state = 'active'
			  )`, nextEvenScanGenerationSQL, sqlPlaceholders(len(repoIDs))), promoteArgs...); err != nil {
			return fmt.Errorf("queue promoted archive dataset progress: %w", err)
		}
		updateArgs := append([]any{now}, args...)
		if _, err := tx.ExecContext(ctx, fmt.Sprintf(`
			UPDATE forge_archive_repos
			SET collection_mode = 'full', operator_state = 'active',
				initial_started_at = COALESCE(initial_started_at, created_at),
				last_error_code = CASE WHEN last_error_code IN ('budget_exhausted', 'authentication_failed', 'repository_blocked') THEN last_error_code ELSE NULL END,
				last_error_detail = CASE WHEN last_error_code IN ('budget_exhausted', 'authentication_failed', 'repository_blocked') THEN last_error_detail ELSE NULL END,
				next_retry_at = CASE WHEN last_error_code IN ('budget_exhausted', 'authentication_failed', 'repository_blocked') THEN next_retry_at ELSE NULL END,
				updated_at = ?
			WHERE repo_id IN (%s)
			  AND (
				collection_mode = 'discovery'
				OR (collection_mode = 'full' AND operator_state = 'paused')
				OR (collection_mode = 'full' AND last_error_code = 'transient')
			  )`, sqlPlaceholders(len(repoIDs))), updateArgs...); err != nil {
			return fmt.Errorf("start full archives: %w", err)
		}
		for _, repoID := range repoIDs {
			if err := completeArchiveInitialIfReadyTx(ctx, tx, repoID, now); err != nil {
				return err
			}
		}
		return nil
	})
}

// PauseArchives atomically pauses selected archive states without discarding progress.
func (d *DB) PauseArchives(ctx context.Context, repoIDs []int64, now time.Time) error {
	repoIDs = normalizedArchiveRepoIDs(repoIDs)
	now = now.UTC()
	if len(repoIDs) == 0 {
		return nil
	}
	return d.Tx(ctx, func(tx *sql.Tx) error {
		if err := requireArchiveRepoIDs(ctx, tx, "forge_archive_repos", repoIDs); err != nil {
			return err
		}
		args := append([]any{now}, archiveRepoIDArgs(repoIDs)...)
		_, err := tx.ExecContext(ctx, fmt.Sprintf(`
			UPDATE forge_archive_repos
			SET operator_state = 'paused', updated_at = ?
			WHERE repo_id IN (%s) AND operator_state <> 'paused'`,
			sqlPlaceholders(len(repoIDs))), args...)
		if err != nil {
			return fmt.Errorf("pause archives: %w", err)
		}
		return nil
	})
}

// ReconcileArchiveCoverage records current provider capabilities and reopens
// inventory that can now replace a previously unsupported result.
func (d *DB) ReconcileArchiveCoverage(ctx context.Context, repoID int64, coverage ArchiveCoverageSet, now time.Time) error {
	if repoID <= 0 {
		return fmt.Errorf("reconcile archive coverage: repository ID is required")
	}
	for name, value := range map[string]ArchiveCoverage{
		"issues": coverage.Issues, "merge_requests": coverage.MergeRequests,
		"comments": coverage.Comments, "reviews": coverage.Reviews,
		"inline_comments": coverage.InlineComments,
	} {
		if value != ArchiveCoverageSupported && value != ArchiveCoverageUnsupported {
			return fmt.Errorf("reconcile archive coverage: invalid %s coverage %q", name, value)
		}
	}
	now = now.UTC()
	return d.Tx(ctx, func(tx *sql.Tx) error {
		if err := requireArchiveRepoIDs(ctx, tx, "forge_archive_repos", []int64{repoID}); err != nil {
			return err
		}
		for _, inventory := range []struct {
			desired ArchiveCoverage
			scan    ArchiveScanKind
			column  string
		}{
			{desired: coverage.Issues, scan: ArchiveScanIssueInventory, column: "issues_coverage"},
			{desired: coverage.MergeRequests, scan: ArchiveScanMergeRequestInventory, column: "merge_requests_coverage"},
		} {
			if inventory.desired != ArchiveCoverageSupported {
				continue
			}
			result, err := tx.ExecContext(ctx, `
				UPDATE forge_archive_repo_scans
				SET scan_generation = `+nextEvenScanGenerationSQL+`,
					next_cursor = NULL, last_input_cursor = NULL,
					page_count = 0, status = 'pending',
					last_error_code = NULL, last_error_detail = NULL,
					updated_at = ?
				WHERE repo_id = ? AND scan = ? AND status = 'complete'
				  AND EXISTS (
					SELECT 1 FROM forge_archive_repos
					WHERE repo_id = ? AND `+inventory.column+` = 'unsupported'
				  )`, now, repoID, inventory.scan, repoID)
			if err != nil {
				return fmt.Errorf("reopen %s archive inventory: %w", inventory.scan, err)
			}
			reopened, err := result.RowsAffected()
			if err != nil {
				return fmt.Errorf("reopen %s archive inventory rows affected: %w", inventory.scan, err)
			}
			if reopened == 0 {
				continue
			}
			itemType := ArchiveItemTypeIssue
			if inventory.scan == ArchiveScanMergeRequestInventory {
				itemType = ArchiveItemTypeMergeRequest
			}
			if err := requeueArchiveKnownItemLookupsTx(
				ctx, tx, repoID, itemType, now,
			); err != nil {
				return err
			}
			if _, err := tx.ExecContext(ctx, `
				UPDATE forge_archive_repos
				SET `+inventory.column+` = 'unknown',
					initial_completed_at = NULL,
					updated_at = MAX(updated_at, ?)
				WHERE repo_id = ?`, now, repoID); err != nil {
				return fmt.Errorf("reset %s archive coverage: %w", inventory.scan, err)
			}
		}
		_, err := tx.ExecContext(ctx, `
			UPDATE forge_archive_repos
			SET issues_coverage = CASE WHEN EXISTS (
				SELECT 1 FROM forge_archive_repo_scans
					WHERE repo_id = ? AND scan = 'issue_inventory' AND status = 'complete'
				) THEN CASE WHEN issues_coverage = 'unknown'
					THEN ?
					ELSE issues_coverage END
				ELSE issues_coverage END,
				merge_requests_coverage = CASE WHEN EXISTS (
					SELECT 1 FROM forge_archive_repo_scans
					WHERE repo_id = ? AND scan = 'merge_request_inventory' AND status = 'complete'
				) THEN CASE WHEN merge_requests_coverage = 'unknown'
					THEN ?
					ELSE merge_requests_coverage END
				ELSE merge_requests_coverage END,
				comments_coverage = ?, reviews_coverage = ?, inline_comments_coverage = ?,
				updated_at = ?
			WHERE repo_id = ?`, repoID, coverage.Issues, repoID, coverage.MergeRequests,
			coverage.Comments, coverage.Reviews,
			coverage.InlineComments, now, repoID)
		if err != nil {
			return fmt.Errorf("reconcile archive coverage: %w", err)
		}
		return nil
	})
}

func requeueArchiveKnownItemLookupsTx(
	ctx context.Context,
	tx *sql.Tx,
	repoID int64,
	itemType ArchiveItemType,
	now time.Time,
) error {
	_, err := tx.ExecContext(ctx, `
		UPDATE forge_archive_dataset_progress
		SET scan_generation = `+nextEvenScanGenerationSQL+`,
			next_cursor = NULL, last_input_cursor = NULL,
			page_count = 0, observed_count = 0,
			status = 'pending', attempt_count = 0, next_retry_at = NULL,
			last_error_code = NULL, last_error_detail = NULL,
			started_at = NULL, completed_at = NULL, updated_at = ?
		WHERE repo_id = ? AND item_type = ? AND dataset = 'lookup'
		  AND status = 'complete'
		  AND EXISTS (
			SELECT 1 FROM forge_archive_items ai
			WHERE ai.repo_id = forge_archive_dataset_progress.repo_id
			  AND ai.item_type = forge_archive_dataset_progress.item_type
			  AND ai.item_number = forge_archive_dataset_progress.item_number
			  AND ai.lifecycle_state = 'active'
		  )`, now, repoID, itemType)
	if err != nil {
		return fmt.Errorf(
			"requeue known %s archive lookups: %w", itemType, err,
		)
	}
	return nil
}

// RequeueArchiveLifecycleDetails reopens completed provider lookups whose
// persisted rows predate the current lifecycle verification contract. The
// normal archive hydration path owns the provider read and persistence.
func (d *DB) RequeueArchiveLifecycleDetails(
	ctx context.Context,
	repoIDs []int64,
	now time.Time,
) error {
	repoIDs = normalizedArchiveRepoIDs(repoIDs)
	if len(repoIDs) == 0 {
		return nil
	}
	args := []any{archiveLifecycleDetailsGeneration, formatDatasetProgressTime(now)}
	args = append(args, archiveRepoIDArgs(repoIDs)...)
	args = append(args, archiveLifecycleDetailsGeneration)
	_, err := d.execContext(ctx, fmt.Sprintf(`
		UPDATE forge_archive_dataset_progress
		SET scan_generation = ?,
			next_cursor = NULL, last_input_cursor = NULL,
			page_count = 0, observed_count = 0,
			status = 'pending', attempt_count = 0, next_retry_at = NULL,
			last_error_code = NULL, last_error_detail = NULL,
			started_at = NULL, completed_at = NULL, updated_at = ?
		WHERE repo_id IN (%s)
		  AND item_type IN ('issue', 'merge_request') AND dataset = 'lookup'
		  AND status = 'complete'
		  AND scan_generation < ?
		  AND EXISTS (
			SELECT 1
			FROM forge_archive_items ai
			JOIN forge_archive_repos ar ON ar.repo_id = ai.repo_id
			JOIN forge_repos r ON r.id = ai.repo_id
			WHERE ai.repo_id = forge_archive_dataset_progress.repo_id
			  AND ai.item_type = forge_archive_dataset_progress.item_type
			  AND ai.item_number = forge_archive_dataset_progress.item_number
			  AND ai.lifecycle_state = 'active'
			  AND ar.collection_mode = 'full'
			  AND (
				(ai.item_type = 'issue' AND EXISTS (
					SELECT 1 FROM forge_issues i
					WHERE i.repo_id = ai.repo_id AND i.number = ai.item_number
					  AND i.closed_at IS NOT NULL
					  AND (
						SELECT e.created_at
						FROM forge_issue_events e
						WHERE e.issue_id = i.id AND e.event_type = 'closed' AND e.author <> ''
						ORDER BY e.created_at DESC, e.id DESC
						LIMIT 1
					  ) IS DISTINCT FROM i.closed_at
				))
				OR
				(ai.item_type = 'merge_request' AND EXISTS (
					SELECT 1 FROM forge_merge_requests mr
					WHERE mr.repo_id = ai.repo_id AND mr.number = ai.item_number
					  AND (mr.state = 'merged' OR mr.merged_at IS NOT NULL)
					  AND (r.platform = 'github' OR (
						mr.merged_at IS NULL OR mr.files_changed IS NULL OR mr.merge_commit_sha = ''
						OR NOT EXISTS (
							SELECT 1 FROM forge_mr_events e
							WHERE e.merge_request_id = mr.id
							  AND e.event_type = 'merged' AND e.author <> ''
						)
					  ))
				))
			  )
		  )`, sqlPlaceholders(len(repoIDs))), args...)
	if err != nil {
		return fmt.Errorf("requeue archive lifecycle details: %w", err)
	}
	return nil
}

// DeferArchiveRepository records provider-host admission waits without
// incrementing item attempts. A later successful request clears this state.
func (d *DB) DeferArchiveRepository(ctx context.Context, repoID int64, retryAt time.Time, detail string, now time.Time) error {
	result, err := d.execContext(ctx, `
		UPDATE forge_archive_repos
		SET last_error_code = ?, last_error_detail = ?, next_retry_at = ?, updated_at = ?
		WHERE repo_id = ?`, ArchiveErrorCodeBudgetExhausted,
		sanitizeArchiveErrorDetail(detail), retryAt.UTC(), now.UTC(), repoID)
	if err != nil {
		return fmt.Errorf("defer archive repository: %w", err)
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("defer archive repository rows affected: %w", err)
	}
	if rows != 1 {
		return &ArchiveRepoStateNotFoundError{RepoIDs: []int64{repoID}}
	}
	return nil
}

func (d *DB) ClearArchiveRepositoryError(ctx context.Context, repoID int64, now time.Time) error {
	_, err := d.execContext(ctx, `
		UPDATE forge_archive_repos
		SET last_error_code = CASE WHEN last_error_code = ? THEN NULL ELSE last_error_code END,
			last_error_detail = CASE WHEN last_error_code = ? THEN NULL ELSE last_error_detail END,
			next_retry_at = CASE WHEN last_error_code = ? THEN NULL ELSE next_retry_at END,
			updated_at = MAX(updated_at, ?)
		WHERE repo_id = ?`, ArchiveErrorCodeBudgetExhausted, ArchiveErrorCodeBudgetExhausted,
		ArchiveErrorCodeBudgetExhausted, now.UTC(), repoID)
	if err != nil {
		return fmt.Errorf("clear archive repository error: %w", err)
	}
	return nil
}

// RetryArchiveAuthentication clears credential failures for repositories whose
// token sources changed. Inventory cursors and maintenance progress remain
// untouched so the next worker pass resumes from its durable boundary.
func (d *DB) RetryArchiveAuthentication(ctx context.Context, repoIDs []int64, now time.Time) error {
	repoIDs = normalizedArchiveRepoIDs(repoIDs)
	if len(repoIDs) == 0 {
		return nil
	}
	args := append([]any{now.UTC()}, archiveRepoIDArgs(repoIDs)...)
	_, err := d.execContext(ctx, fmt.Sprintf(`
		UPDATE forge_archive_repos
		SET last_error_code = NULL, last_error_detail = NULL,
			next_retry_at = NULL, updated_at = MAX(updated_at, ?)
		WHERE repo_id IN (%s) AND last_error_code = 'authentication_failed'`,
		sqlPlaceholders(len(repoIDs))), args...)
	if err != nil {
		return fmt.Errorf("retry archive authentication: %w", err)
	}
	return nil
}

// RecordArchiveRepositoryFailure persists a provider or contract failure for
// inventory work so scheduler restarts retain both the status and retry gate.
func (d *DB) RecordArchiveRepositoryFailure(
	ctx context.Context,
	repoID int64,
	code ArchiveErrorCode,
	detail string,
	retryAt *time.Time,
	now time.Time,
) error {
	return d.Tx(ctx, func(tx *sql.Tx) error {
		return recordArchiveRepositoryFailureTx(ctx, tx, repoID, code, detail, retryAt, now)
	})
}

func recordArchiveRepositoryFailureTx(
	ctx context.Context,
	tx *sql.Tx,
	repoID int64,
	code ArchiveErrorCode,
	detail string,
	retryAt *time.Time,
	now time.Time,
) error {
	if repoID <= 0 || code == "" {
		return errors.New("record archive repository failure: repository and error code are required")
	}
	var retry any
	if retryAt != nil {
		retry = retryAt.UTC()
	}
	result, err := tx.ExecContext(ctx, `
		UPDATE forge_archive_repos
		SET last_error_code = ?, last_error_detail = ?, next_retry_at = ?, updated_at = MAX(updated_at, ?)
		WHERE repo_id = ?`, code, sanitizeArchiveErrorDetail(detail), retry, now.UTC(), repoID)
	if err != nil {
		return fmt.Errorf("record archive repository failure: %w", err)
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("record archive repository failure rows affected: %w", err)
	}
	if rows != 1 {
		return &ArchiveRepoStateNotFoundError{RepoIDs: []int64{repoID}}
	}
	return nil
}

// RecordArchiveRepositoryFailureForScan records a repository-level failure
// only while the claimed scan is still the current, running traversal for
// its kind. A delayed repository-scoped response from a superseded, reset,
// completed, or blocked scan is a stale no-op, so it can never re-defer or
// re-block repository work that has since moved on.
func (d *DB) RecordArchiveRepositoryFailureForScan(
	ctx context.Context,
	repoID int64,
	kind ArchiveScanKind,
	claimedGeneration int64,
	code ArchiveErrorCode,
	detail string,
	retryAt *time.Time,
	now time.Time,
) (bool, error) {
	applied := false
	err := d.Tx(ctx, func(tx *sql.Tx) error {
		var generation int64
		var status ArchiveScanStatus
		err := tx.QueryRowContext(ctx, `
			SELECT scan_generation, status FROM forge_archive_repo_scans
			WHERE repo_id = ? AND scan = ?`, repoID, kind,
		).Scan(&generation, &status)
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("read scan claim for repository failure: %w", err)
		}
		// Pending counts as claimable: the first page read of a fresh scan can
		// fail before any commit moves the row to running. Completed, blocked,
		// or generation-superseded scans make the delivery stale.
		if generation != claimedGeneration ||
			(status != ArchiveScanRunning && status != ArchiveScanPending) {
			return nil
		}
		applied = true
		return recordArchiveRepositoryFailureTx(ctx, tx, repoID, code, detail, retryAt, now)
	})
	return applied, err
}

// BeginArchivePromptMaintenance establishes a fixed scan boundary once and
// preserves it with both maintenance scan cursors across budget deferrals and
// restarts. Starting a fresh boundary resets both maintenance scan rows to a
// new generation.
func (d *DB) BeginArchivePromptMaintenance(
	ctx context.Context,
	repoID int64,
	since time.Time,
	scanStart time.Time,
) (ArchiveRepoState, error) {
	err := d.Tx(ctx, func(tx *sql.Tx) error {
		var started sql.NullTime
		err := tx.QueryRowContext(ctx, `
			SELECT prompt_scan_started_at FROM forge_archive_repos
			WHERE repo_id = ? AND collection_mode = 'full' AND operator_state = 'active'`,
			repoID).Scan(&started)
		if errors.Is(err, sql.ErrNoRows) {
			return &ArchiveRepoStateNotFoundError{RepoIDs: []int64{repoID}}
		}
		if err != nil {
			return fmt.Errorf("begin archive prompt maintenance: %w", err)
		}
		if started.Valid {
			return nil
		}
		if _, err := tx.ExecContext(ctx, `
			UPDATE forge_archive_repos
			SET prompt_scan_started_at = ?, prompt_since = ?, updated_at = MAX(updated_at, ?)
			WHERE repo_id = ?`,
			scanStart.UTC(), since.UTC(), scanStart.UTC(), repoID); err != nil {
			return fmt.Errorf("begin archive prompt maintenance: %w", err)
		}
		for _, kind := range []ArchiveScanKind{ArchiveScanMaintenanceIssues, ArchiveScanMaintenanceMergeRequests} {
			if err := resetArchiveScanTx(ctx, tx, repoID, kind, scanStart); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return ArchiveRepoState{}, err
	}
	states, err := d.ListArchiveRepoStates(ctx, []int64{repoID})
	if err != nil {
		return ArchiveRepoState{}, err
	}
	if len(states) != 1 {
		return ArchiveRepoState{}, &ArchiveRepoStateNotFoundError{RepoIDs: []int64{repoID}}
	}
	return states[0], nil
}

// CompleteArchivePromptMaintenance advances the prompt watermark only after
// both maintenance scans have reached explicit end-of-pagination, then resets
// them for the next boundary.
func (d *DB) CompleteArchivePromptMaintenance(
	ctx context.Context,
	repoID int64,
	scanStart time.Time,
	completedAt time.Time,
) error {
	return d.Tx(ctx, func(tx *sql.Tx) error {
		result, err := tx.ExecContext(ctx, `
			UPDATE forge_archive_repos
			SET maintenance_watermark = ?, maintenance_succeeded_at = ?,
				prompt_scan_started_at = NULL, prompt_since = NULL,
				last_error_code = NULL, last_error_detail = NULL, next_retry_at = NULL,
				updated_at = MAX(updated_at, ?)
			WHERE repo_id = ? AND collection_mode = 'full' AND operator_state = 'active'
			  AND (
				SELECT status FROM forge_archive_repo_scans
				WHERE repo_id = ? AND scan = 'maintenance_issues'
			  ) = 'complete'
			  AND (
				SELECT status FROM forge_archive_repo_scans
				WHERE repo_id = ? AND scan = 'maintenance_merge_requests'
			  ) = 'complete'`,
			scanStart.UTC(), completedAt.UTC(), completedAt.UTC(), repoID, repoID, repoID)
		if err != nil {
			return fmt.Errorf("complete archive prompt maintenance: %w", err)
		}
		rows, err := result.RowsAffected()
		if err != nil {
			return fmt.Errorf("complete archive prompt maintenance rows affected: %w", err)
		}
		if rows != 1 {
			return &ArchiveRepoStateNotFoundError{RepoIDs: []int64{repoID}}
		}
		for _, kind := range []ArchiveScanKind{ArchiveScanMaintenanceIssues, ArchiveScanMaintenanceMergeRequests} {
			if err := resetArchiveScanTx(ctx, tx, repoID, kind, completedAt); err != nil {
				return err
			}
		}
		return nil
	})
}

// QueueArchivePromptByIdentity wakes prompt enumeration for a configured full
// destination repository after a provider reports that an item moved there.
// The next scan starts from the destination's prior watermark and therefore
// does not assume that provider item numbers survive the move.
func (d *DB) QueueArchivePromptByIdentity(
	ctx context.Context,
	identity RepoIdentity,
	now time.Time,
) error {
	return queueArchivePromptByIdentity(ctx, d.rw, identity, now)
}

type archiveExecer interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
}

func queueArchivePromptByIdentity(
	ctx context.Context,
	execer archiveExecer,
	identity RepoIdentity,
	now time.Time,
) error {
	identity = canonicalRepoIdentity(identity)
	_, err := execer.ExecContext(ctx, `
		UPDATE forge_archive_repos
		SET maintenance_watermark = COALESCE(maintenance_watermark, initial_completed_at, created_at),
			maintenance_succeeded_at = NULL, updated_at = MAX(updated_at, ?)
		WHERE collection_mode = 'full' AND operator_state = 'active'
		  AND repo_id = (
			SELECT r.id
			FROM forge_repos r
			JOIN forge_repo_routes rr
			  ON rr.repo_id = r.id AND rr.is_current = 1
			WHERE r.lifecycle_state = 'active'
			  AND rr.platform = ? AND rr.platform_host = ?
			  AND rr.repo_path_key = ?
		  )`, now.UTC(), identity.Platform, identity.PlatformHost, identity.RepoPathKey)
	if err != nil {
		return fmt.Errorf("queue archive prompt destination: %w", err)
	}
	return nil
}

// ListArchiveRepoStates returns stored archive state in repository ID order.
func (d *DB) ListArchiveRepoStates(ctx context.Context, repoIDs []int64) ([]ArchiveRepoState, error) {
	repoIDs = normalizedArchiveRepoIDs(repoIDs)
	return listArchiveRepoStates(ctx, d.ro, repoIDs)
}

func listArchiveRepoStates(
	ctx context.Context,
	queryer archiveQueryer,
	repoIDs []int64,
) ([]ArchiveRepoState, error) {
	query := `
		SELECT repo_id, collection_mode, operator_state,
			initial_started_at, initial_completed_at,
			maintenance_watermark, maintenance_succeeded_at,
			prompt_scan_started_at, prompt_since,
			issues_coverage, merge_requests_coverage,
			comments_coverage, reviews_coverage, inline_comments_coverage,
			last_error_code, last_error_detail, next_retry_at,
			created_at, updated_at
		FROM forge_archive_repos`
	var args []any
	if len(repoIDs) > 0 {
		query += " WHERE repo_id IN (" + sqlPlaceholders(len(repoIDs)) + ")"
		args = archiveRepoIDArgs(repoIDs)
	}
	query += " ORDER BY repo_id"
	rows, err := queryer.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("list archive repo states: %w", err)
	}
	defer rows.Close()
	states := make([]ArchiveRepoState, 0)
	for rows.Next() {
		var state ArchiveRepoState
		if err := scanArchiveRepoState(rows, &state); err != nil {
			return nil, fmt.Errorf("scan archive repo state: %w", err)
		}
		states = append(states, state)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list archive repo states: %w", err)
	}
	if err := loadArchiveScanStates(ctx, queryer, states); err != nil {
		return nil, err
	}
	return states, nil
}

type archiveRowScanner interface {
	Scan(dest ...any) error
}

type archiveQueryer interface {
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

func scanArchiveRepoState(row archiveRowScanner, state *ArchiveRepoState) error {
	return row.Scan(
		&state.RepoID, &state.CollectionMode, &state.OperatorState,
		&state.InitialStartedAt, &state.InitialCompletedAt,
		&state.MaintenanceWatermark, &state.MaintenanceSucceededAt,
		&state.PromptScanStartedAt, &state.PromptSince,
		&state.IssuesCoverage, &state.MergeRequestsCoverage,
		&state.CommentsCoverage, &state.ReviewsCoverage, &state.InlineCommentsCoverage,
		&state.LastErrorCode, &state.LastErrorDetail, &state.NextRetryAt,
		&state.CreatedAt, &state.UpdatedAt,
	)
}

// ClaimArchiveItem returns the oldest due item from the explicitly eligible
// repositories. Claims are observational; the generation check on progress
// commits makes duplicate scheduling safe.
func (d *DB) ClaimArchiveItem(ctx context.Context, opts ClaimArchiveItemOpts) (*ArchiveItemWork, error) {
	return d.claimArchiveItem(ctx, opts, nil)
}

func (d *DB) claimArchiveItem(
	ctx context.Context,
	opts ClaimArchiveItemOpts,
	inspectCandidates func([]ArchiveItemWork),
) (*ArchiveItemWork, error) {
	repoIDs := normalizedArchiveRepoIDs(opts.RepoIDs)
	opts.Now = opts.Now.UTC()
	if len(repoIDs) == 0 {
		return nil, nil
	}
	tx, err := d.ro.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, fmt.Errorf("begin archive item claim snapshot: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	candidates := make([]ArchiveItemWork, 0, len(repoIDs))
	for _, repoID := range repoIDs {
		candidate, err := claimArchiveItemForRepo(
			ctx, tx, repoID, opts.Now, excludedArchiveItemTypes(opts.ExcludedScopes, repoID),
		)
		if err != nil {
			return nil, err
		}
		if candidate != nil {
			candidates = append(candidates, *candidate)
		}
	}
	if inspectCandidates != nil {
		inspectCandidates(candidates)
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit archive item claim snapshot: %w", err)
	}
	if len(candidates) == 0 {
		return nil, nil
	}
	oldest := candidates[0]
	for i := 1; i < len(candidates); i++ {
		if archiveItemStableLess(candidates[i], oldest) {
			oldest = candidates[i]
		}
	}
	return &oldest, nil
}

const dueArchiveItemsQuery = `
	SELECT ai.repo_id, ai.item_type, ai.item_number, ai.provider_created_at,
		p.scan_generation, p.attempt_count
	FROM forge_archive_dataset_progress p
	JOIN forge_archive_items ai
	  ON ai.repo_id = p.repo_id AND ai.item_type = p.item_type AND ai.item_number = p.item_number
	JOIN forge_archive_repos ar ON ar.repo_id = p.repo_id
	WHERE p.repo_id = ?
	  AND ar.collection_mode = 'full'
	  AND ar.operator_state = 'active'
	  AND (ar.next_retry_at IS NULL OR ar.next_retry_at <= ?)
	  AND ai.lifecycle_state = 'active'
	  AND p.dataset = 'lookup'
	  AND p.status IN ('pending', 'running', 'failed')
	  AND (p.next_retry_at IS NULL OR p.next_retry_at <= ?)`

func claimArchiveItemForRepo(
	ctx context.Context,
	queryer archiveQueryer,
	repoID int64,
	now time.Time,
	excludedItemTypes []ArchiveItemType,
) (*ArchiveItemWork, error) {
	query := dueArchiveItemsQuery
	args := []any{repoID, now, formatDatasetProgressTime(now)}
	if len(excludedItemTypes) > 0 {
		query += fmt.Sprintf(" AND ai.item_type NOT IN (%s)", sqlPlaceholders(len(excludedItemTypes)))
		for _, itemType := range excludedItemTypes {
			args = append(args, itemType)
		}
	}
	query += ` ORDER BY ai.provider_created_at, ai.item_type, ai.item_number LIMIT 1`
	var work ArchiveItemWork
	err := queryer.QueryRowContext(
		ctx, query, args...,
	).Scan(
		&work.RepoID, &work.ItemType, &work.ItemNumber,
		&work.ProviderCreatedAt, &work.ScanGeneration, &work.AttemptCount,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("claim archive item: %w", err)
	}
	work.ProviderCreatedAt = work.ProviderCreatedAt.UTC()
	return &work, nil
}

func excludedArchiveItemTypes(
	scopes []ArchiveItemScope,
	repoID int64,
) []ArchiveItemType {
	seen := make(map[ArchiveItemType]struct{})
	for _, scope := range scopes {
		if scope.RepoID == repoID {
			seen[scope.ItemType] = struct{}{}
		}
	}
	itemTypes := make([]ArchiveItemType, 0, len(seen))
	for itemType := range seen {
		itemTypes = append(itemTypes, itemType)
	}
	slices.Sort(itemTypes)
	return itemTypes
}

func archiveItemStableLess(left, right ArchiveItemWork) bool {
	if !left.ProviderCreatedAt.Equal(right.ProviderCreatedAt) {
		return left.ProviderCreatedAt.Before(right.ProviderCreatedAt)
	}
	if left.ItemType != right.ItemType {
		return left.ItemType < right.ItemType
	}
	if left.ItemNumber != right.ItemNumber {
		return left.ItemNumber < right.ItemNumber
	}
	return left.RepoID < right.RepoID
}

// ArchiveItemTerminal marks an archive item's lifecycle as removed upstream or
// inaccessible. Domain issue/merge-request content is deliberately retained;
// terminal error detail lives on the item's lookup dataset progress row.
type ArchiveItemTerminal struct {
	RepoID     int64
	ItemType   ArchiveItemType
	ItemNumber int
	Lifecycle  ArchiveLifecycleState
	At         time.Time
}

func markArchiveItemTerminalTx(ctx context.Context, tx *sql.Tx, t ArchiveItemTerminal) error {
	result, err := tx.ExecContext(ctx, `
		UPDATE forge_archive_items
		SET lifecycle_state = ?
		WHERE repo_id = ? AND item_type = ? AND item_number = ?`,
		t.Lifecycle, t.RepoID, t.ItemType, t.ItemNumber,
	)
	if err != nil {
		return fmt.Errorf("mark archive item terminal: %w", err)
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("mark archive item terminal rows affected: %w", err)
	}
	if changed == 0 {
		return fmt.Errorf(
			"mark archive item terminal: no archive item for repo %d %s %d",
			t.RepoID, t.ItemType, t.ItemNumber,
		)
	}
	if err := clearArchiveRepositoryFailureTx(ctx, tx, t.RepoID, t.At); err != nil {
		return err
	}
	return completeArchiveInitialIfReadyTx(ctx, tx, t.RepoID, t.At)
}

func nullableArchiveError(value string) any {
	if value == "" {
		return nil
	}
	return value
}

func sanitizeArchiveErrorDetail(detail string) string {
	detail = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return ' '
		}
		return r
	}, detail)
	detail = strings.Join(strings.Fields(detail), " ")
	const maxDetailRunes = 1024
	runes := []rune(detail)
	if len(runes) > maxDetailRunes {
		detail = string(runes[:maxDetailRunes])
	}
	return detail
}

// GetArchiveProgress derives status, active phases, and counts from durable rows.
func (d *DB) GetArchiveProgress(ctx context.Context, opts ArchiveProgressOpts) ([]ArchiveRepoProgress, error) {
	return d.getArchiveProgress(ctx, opts, nil)
}

func (d *DB) getArchiveProgress(
	ctx context.Context,
	opts ArchiveProgressOpts,
	afterStateRead func() error,
) ([]ArchiveRepoProgress, error) {
	opts.Now = opts.Now.UTC()
	tx, err := d.ro.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, fmt.Errorf("begin archive progress snapshot: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	states, err := listArchiveRepoStates(ctx, tx, normalizedArchiveRepoIDs(opts.RepoIDs))
	if err != nil {
		return nil, err
	}
	if afterStateRead != nil {
		if err := afterStateRead(); err != nil {
			return nil, fmt.Errorf("after archive state read: %w", err)
		}
	}
	if len(states) == 0 {
		if err := tx.Commit(); err != nil {
			return nil, fmt.Errorf("commit archive progress snapshot: %w", err)
		}
		return []ArchiveRepoProgress{}, nil
	}
	repoIDs := make([]int64, len(states))
	for i := range states {
		repoIDs[i] = states[i].RepoID
	}
	countsByRepo, err := loadArchiveProgressCounts(ctx, tx, repoIDs, opts.Now)
	if err != nil {
		return nil, err
	}
	progress := make([]ArchiveRepoProgress, len(states))
	for i, state := range states {
		progress[i] = deriveArchiveProgress(state, countsByRepo[state.RepoID], opts.Now)
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit archive progress snapshot: %w", err)
	}
	return progress, nil
}

func loadArchiveProgressCounts(
	ctx context.Context,
	queryer archiveQueryer,
	repoIDs []int64,
	now time.Time,
) (map[int64]ArchiveProgressCounts, error) {
	rows, err := queryer.QueryContext(ctx, fmt.Sprintf(`
		SELECT ai.repo_id,
			COUNT(*),
			SUM(CASE WHEN ai.lifecycle_state = 'removed_upstream'
				OR (
					ai.lifecycle_state = 'active'
					AND COALESCE(pp.open_count, 0) = 0
					AND COALESCE(pp.blocked_count, 0) = 0
				) THEN 1 ELSE 0 END),
			SUM(CASE WHEN ai.lifecycle_state = 'active'
				AND COALESCE(pp.pending_count, 0) > 0 THEN 1 ELSE 0 END),
			SUM(CASE WHEN ai.lifecycle_state = 'active'
				AND COALESCE(pp.failed_count, 0) > 0 THEN 1 ELSE 0 END),
			SUM(CASE WHEN ai.lifecycle_state = 'active'
				AND COALESCE(pp.unsupported_count, 0) > 0 THEN 1 ELSE 0 END),
			SUM(CASE WHEN ai.lifecycle_state = 'inaccessible' THEN 1 ELSE 0 END),
			SUM(CASE WHEN ai.lifecycle_state = 'active'
				AND COALESCE(pp.due_count, 0) > 0 THEN 1 ELSE 0 END),
			SUM(CASE WHEN ai.lifecycle_state = 'active'
				AND COALESCE(pp.blocked_count, 0) > 0 THEN 1 ELSE 0 END)
		FROM forge_archive_items ai
		LEFT JOIN (
			SELECT repo_id, item_type, item_number,
				SUM(CASE WHEN status IN ('pending', 'running') THEN 1 ELSE 0 END) AS pending_count,
				SUM(CASE WHEN status = 'failed' THEN 1 ELSE 0 END) AS failed_count,
				SUM(CASE WHEN status = 'unsupported' THEN 1 ELSE 0 END) AS unsupported_count,
				SUM(CASE WHEN status = 'blocked' THEN 1 ELSE 0 END) AS blocked_count,
				SUM(CASE WHEN status IN ('pending', 'running', 'failed')
					AND (next_retry_at IS NULL OR next_retry_at <= ?) THEN 1 ELSE 0 END) AS due_count,
				SUM(CASE WHEN status IN ('pending', 'running', 'failed') THEN 1 ELSE 0 END) AS open_count
			FROM forge_archive_dataset_progress
			GROUP BY repo_id, item_type, item_number
		) pp ON pp.repo_id = ai.repo_id AND pp.item_type = ai.item_type AND pp.item_number = ai.item_number
		WHERE ai.repo_id IN (%s)
		GROUP BY ai.repo_id`, sqlPlaceholders(len(repoIDs))),
		append([]any{formatDatasetProgressTime(now)}, archiveRepoIDArgs(repoIDs)...)...)
	if err != nil {
		return nil, fmt.Errorf("count archive progress: %w", err)
	}
	defer rows.Close()
	countsByRepo := make(map[int64]ArchiveProgressCounts, len(repoIDs))
	for rows.Next() {
		var repoID int64
		var counts ArchiveProgressCounts
		if err := rows.Scan(
			&repoID, &counts.ItemCount, &counts.CompleteItemCount,
			&counts.PendingItemCount, &counts.FailedItemCount,
			&counts.UnsupportedItemCount, &counts.InaccessibleItemCount,
			&counts.DueItemCount, &counts.BlockedItemCount,
		); err != nil {
			return nil, fmt.Errorf("scan archive progress counts: %w", err)
		}
		countsByRepo[repoID] = counts
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("count archive progress: %w", err)
	}
	return countsByRepo, nil
}

func deriveArchiveProgress(
	state ArchiveRepoState,
	counts ArchiveProgressCounts,
	now time.Time,
) ArchiveRepoProgress {
	progress := ArchiveRepoProgress{RepoID: state.RepoID, Counts: counts}
	if archiveRepoHasError(state, ArchiveErrorCodeBudgetExhausted) {
		progress.BudgetWaitUntil = state.NextRetryAt
	}
	if state.OperatorState == ArchiveOperatorStatePaused {
		progress.Status = ArchiveStatusPaused
		return progress
	}

	if !state.IssueInventory.Complete() && !state.IssueInventory.Blocked() {
		progress.ActivePhases = append(progress.ActivePhases, ArchivePhaseIssueInventory)
	}
	if !state.MergeRequestInventory.Complete() && !state.MergeRequestInventory.Blocked() {
		progress.ActivePhases = append(progress.ActivePhases, ArchivePhaseMergeRequestInventory)
	}
	initialComplete := state.IssueInventory.Complete() && state.MergeRequestInventory.Complete() &&
		state.InitialCompletedAt != nil
	promptActive := initialComplete && archiveMaintenanceOutstanding(state)
	hasHydration := state.CollectionMode == ArchiveCollectionModeFull &&
		(counts.PendingItemCount > 0 || counts.FailedItemCount > 0)
	if hasHydration {
		progress.ActivePhases = append(progress.ActivePhases, ArchivePhaseHydration)
	}
	if promptActive {
		progress.ActivePhases = append(progress.ActivePhases, ArchivePhasePromptMaintenance)
	}

	if archiveRepoBlocked(state) || archiveScansBlocked(state) || counts.BlockedItemCount > 0 {
		progress.Status = ArchiveStatusBlocked
		return progress
	}
	budgetExhausted := archiveBudgetDeferred(state, now)
	inventoryWork := !state.IssueInventory.Complete() || !state.MergeRequestInventory.Complete()
	initialWork := !initialComplete || hasHydration
	budgetBlockedWork := inventoryWork || promptActive ||
		(hasHydration && counts.DueItemCount > 0)
	if state.CollectionMode == ArchiveCollectionModeDiscovery {
		if budgetExhausted && budgetBlockedWork {
			progress.Status = ArchiveStatusWaitingForBudget
		} else {
			progress.Status = ArchiveStatusRunning
		}
		return progress
	}
	if initialWork || promptActive {
		if budgetExhausted && budgetBlockedWork {
			progress.Status = ArchiveStatusWaitingForBudget
		} else {
			progress.Status = ArchiveStatusRunning
		}
		return progress
	}
	if archiveHasPartialCoverage(state, counts) {
		progress.Status = ArchiveStatusPartial
		return progress
	}
	if state.LastErrorCode != nil && !budgetExhausted {
		progress.Status = ArchiveStatusRunning
		return progress
	}
	progress.Status = ArchiveStatusCurrent
	return progress
}

func archiveBudgetDeferred(state ArchiveRepoState, now time.Time) bool {
	return archiveRepoHasError(state, ArchiveErrorCodeBudgetExhausted) &&
		state.NextRetryAt != nil && state.NextRetryAt.After(now)
}

func archiveMaintenanceOutstanding(state ArchiveRepoState) bool {
	if state.MaintenanceWatermark == nil {
		return false
	}
	return state.MaintenanceSucceededAt == nil ||
		state.MaintenanceSucceededAt.Before(*state.MaintenanceWatermark)
}

func archiveHasPartialCoverage(state ArchiveRepoState, counts ArchiveProgressCounts) bool {
	return state.IssuesCoverage == ArchiveCoverageUnsupported ||
		state.MergeRequestsCoverage == ArchiveCoverageUnsupported ||
		state.CommentsCoverage == ArchiveCoverageUnsupported ||
		state.ReviewsCoverage == ArchiveCoverageUnsupported ||
		state.InlineCommentsCoverage == ArchiveCoverageUnsupported ||
		counts.UnsupportedItemCount > 0 || counts.InaccessibleItemCount > 0
}

func archiveRepoHasError(state ArchiveRepoState, code ArchiveErrorCode) bool {
	return state.LastErrorCode != nil && *state.LastErrorCode == string(code)
}

func archiveScansBlocked(state ArchiveRepoState) bool {
	return state.IssueInventory.Blocked() || state.MergeRequestInventory.Blocked() ||
		state.MaintenanceIssues.Blocked() || state.MaintenanceMergeRequests.Blocked()
}

func archiveRepoBlocked(state ArchiveRepoState) bool {
	if state.LastErrorCode == nil {
		return false
	}
	if archiveRepoHasError(state, ArchiveErrorCodeAuthentication) ||
		archiveRepoHasError(state, ArchiveErrorCodeRepoBlocked) {
		return true
	}
	return state.NextRetryAt == nil &&
		!archiveRepoHasError(state, ArchiveErrorCodeBudgetExhausted) &&
		!archiveRepoHasError(state, ArchiveErrorCodeTransient)
}

func normalizedArchiveRepoIDs(repoIDs []int64) []int64 {
	if len(repoIDs) == 0 {
		return nil
	}
	seen := make(map[int64]struct{}, len(repoIDs))
	result := make([]int64, 0, len(repoIDs))
	for _, repoID := range repoIDs {
		if _, ok := seen[repoID]; ok {
			continue
		}
		seen[repoID] = struct{}{}
		result = append(result, repoID)
	}
	slices.Sort(result)
	return result
}

func archiveRepoIDArgs(repoIDs []int64) []any {
	args := make([]any, len(repoIDs))
	for i, repoID := range repoIDs {
		args[i] = repoID
	}
	return args
}

func requireArchiveRepoIDs(ctx context.Context, tx *sql.Tx, table string, repoIDs []int64) error {
	idColumn := "repo_id"
	if table == "forge_repos" {
		idColumn = "id"
	}
	rows, err := tx.QueryContext(ctx, fmt.Sprintf(
		"SELECT %s FROM %s WHERE %s IN (%s) ORDER BY %s",
		idColumn, table, idColumn, sqlPlaceholders(len(repoIDs)), idColumn,
	), archiveRepoIDArgs(repoIDs)...)
	if err != nil {
		return fmt.Errorf("validate archive repository IDs: %w", err)
	}
	defer rows.Close()
	found := make(map[int64]struct{}, len(repoIDs))
	for rows.Next() {
		var repoID int64
		if err := rows.Scan(&repoID); err != nil {
			return fmt.Errorf("scan archive repository ID: %w", err)
		}
		found[repoID] = struct{}{}
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("validate archive repository IDs: %w", err)
	}
	missing := make([]int64, 0)
	for _, repoID := range repoIDs {
		if _, ok := found[repoID]; !ok {
			missing = append(missing, repoID)
		}
	}
	if len(missing) > 0 {
		return &ArchiveRepoStateNotFoundError{RepoIDs: missing}
	}
	return nil
}

// CommitArchiveInventoryPage records item identities and advances the scan in
// one transaction. Provider content is populated later by the normal syncer.
func (d *DB) CommitArchiveInventoryPage(ctx context.Context, commit ArchiveInventoryCommit) error {
	if commit.RefreshReason == "" {
		commit.RefreshReason = ArchiveRefreshReasonInitial
	}
	if commit.ScanGeneration == 0 {
		commit.ScanGeneration = 1
	}
	if commit.Coverage == "" {
		commit.Coverage = ArchiveCoverageUnknown
	}
	if err := validateInventoryCommit(commit); err != nil {
		return err
	}
	kind, err := archiveScanKindFor(commit.ItemType, commit.RefreshReason)
	if err != nil {
		return err
	}
	commit.Now = canonicalUTCTime(commit.Now)
	var typedErr error
	err = d.Tx(ctx, func(tx *sql.Tx) error {
		typedErr = nil
		active, err := archiveRepositoryActiveTx(ctx, tx, commit.RepoID)
		if err != nil || !active {
			return err
		}
		outcome, err := checkArchiveScanAdvanceTx(ctx, tx, commit, kind, commit.Now)
		if err != nil {
			return err
		}
		if outcome.typedErr != nil || outcome.replay {
			typedErr = outcome.typedErr
			return nil
		}
		for _, item := range commit.Items {
			item.ProviderCreatedAt = canonicalUTCTime(item.ProviderCreatedAt)
			item.ProviderUpdatedAt = canonicalUTCTime(item.ProviderUpdatedAt)
			if err := commitArchiveInventoryItemTx(
				ctx, tx, commit.RepoID, commit.ItemType, item,
				commit.RefreshReason,
			); err != nil {
				return err
			}
		}
		if err := advanceArchiveScanTx(ctx, tx, commit, kind, outcome.newPageCount, commit.Now); err != nil {
			return err
		}
		if commit.Exhausted && commit.Coverage != ArchiveCoverageUnknown {
			column := "issues_coverage"
			if commit.ItemType == ArchiveItemTypeMergeRequest {
				column = "merge_requests_coverage"
			}
			if _, err := tx.ExecContext(ctx, `
				UPDATE forge_archive_repos
				SET `+column+` = ?, updated_at = MAX(updated_at, ?)
				WHERE repo_id = ?`, commit.Coverage, commit.Now, commit.RepoID); err != nil {
				return fmt.Errorf("record archive inventory coverage: %w", err)
			}
		}
		if err := clearArchiveRepositoryFailureTx(ctx, tx, commit.RepoID, commit.Now); err != nil {
			return err
		}
		return completeArchiveInitialIfReadyTx(ctx, tx, commit.RepoID, commit.Now)
	})
	if err != nil {
		return err
	}
	return typedErr
}

func commitArchiveInventoryItemTx(
	ctx context.Context,
	tx *sql.Tx,
	repoID int64,
	itemType ArchiveItemType,
	item ArchiveInventoryItem,
	refreshReason ArchiveRefreshReason,
) error {
	shouldReopen, err := archiveInventoryItemNeedsRefreshTx(
		ctx, tx, repoID, itemType, item, refreshReason,
	)
	if err != nil {
		return err
	}
	if err := reactivateTerminalArchiveWorkTx(
		ctx, tx, repoID, itemType, item.Number,
	); err != nil {
		return err
	}
	if err := upsertArchiveItemWorkTx(
		ctx, tx, repoID, itemType, item.Number, item.ProviderItemID,
		item.ProviderCreatedAt, item.ProviderUpdatedAt, refreshReason,
	); err != nil {
		return err
	}
	if err := seedArchiveItemProgressTx(ctx, tx, repoID, itemType, item.Number); err != nil {
		return err
	}
	if shouldReopen {
		return reopenArchiveItemProgressTx(ctx, tx, repoID, itemType, item.Number)
	}
	return nil
}

// archiveInventoryItemNeedsRefreshTx compares a prompt observation with the
// transaction-current inventory evidence. New items are seeded pending by the
// caller. Existing items are reopened when a terminal item reappears or the
// provider reports equal or newer evidence. Equal timestamps cannot prove
// stasis because provider timestamps may have second-level granularity.
func archiveInventoryItemNeedsRefreshTx(
	ctx context.Context,
	tx *sql.Tx,
	repoID int64,
	itemType ArchiveItemType,
	item ArchiveInventoryItem,
	refreshReason ArchiveRefreshReason,
) (bool, error) {
	if refreshReason != ArchiveRefreshReasonPrompt {
		return false, nil
	}
	var storedUpdatedAt time.Time
	var lifecycle ArchiveLifecycleState
	err := tx.QueryRowContext(ctx, `
		SELECT provider_updated_at, lifecycle_state
		FROM forge_archive_items
		WHERE repo_id = ? AND item_type = ? AND item_number = ?`,
		repoID, itemType, item.Number,
	).Scan(&storedUpdatedAt, &lifecycle)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("read archive inventory refresh evidence: %w", err)
	}
	if lifecycle == ArchiveLifecycleStateRemovedUpstream ||
		lifecycle == ArchiveLifecycleStateInaccessible {
		return true, nil
	}
	return !item.ProviderUpdatedAt.Before(storedUpdatedAt), nil
}

// seedArchiveItemProgressTx creates the one progress row for an archive item.
// Existing rows are not modified; rebinding handles newer observations.
func seedArchiveItemProgressTx(
	ctx context.Context,
	tx *sql.Tx,
	repoID int64,
	itemType ArchiveItemType,
	itemNumber int,
) error {
	_, err := tx.ExecContext(ctx, `
		INSERT INTO forge_archive_dataset_progress (
			repo_id, item_type, item_number, dataset, scan_generation, status, updated_at
		) VALUES (?, ?, ?, 'lookup', 2, 'pending', ?)
		ON CONFLICT(repo_id, item_type, item_number, dataset) DO NOTHING`,
		repoID, itemType, itemNumber, formatDatasetProgressTime(time.Now()))
	if err != nil {
		return fmt.Errorf("seed archive item progress: %w", err)
	}
	return nil
}

func validateInventoryCommit(commit ArchiveInventoryCommit) error {
	if commit.RepoID == 0 {
		return errors.New("commit archive inventory: repository is required")
	}
	if !commit.Exhausted && commit.NextCursor == "" {
		return errors.New("commit archive inventory: next cursor or explicit end marker is required")
	}
	switch commit.ItemType {
	case ArchiveItemTypeIssue:
	case ArchiveItemTypeMergeRequest:
	default:
		return fmt.Errorf("commit archive inventory: invalid item type %q", commit.ItemType)
	}
	for _, item := range commit.Items {
		if item.Number <= 0 {
			return errors.New("commit archive inventory: item number must be positive")
		}
	}
	switch commit.RefreshReason {
	case ArchiveRefreshReasonInitial, ArchiveRefreshReasonPrompt:
	default:
		return fmt.Errorf("commit archive inventory: invalid refresh reason %q", commit.RefreshReason)
	}
	if commit.Coverage != ArchiveCoverageUnknown &&
		commit.Coverage != ArchiveCoverageSupported &&
		commit.Coverage != ArchiveCoverageUnsupported {
		return fmt.Errorf("commit archive inventory: invalid coverage %q", commit.Coverage)
	}
	if commit.Coverage != ArchiveCoverageUnknown &&
		(commit.RefreshReason != ArchiveRefreshReasonInitial || !commit.Exhausted) {
		return errors.New("commit archive inventory: coverage requires exhausted initial inventory")
	}
	return nil
}

// completeArchiveInitialIfReadyTx marks the initial archive complete once both
// inventory scans finished and no active item carries outstanding dataset
// work. The only statuses that count as completed exceptions are 'complete',
// 'unsupported' (a declared capability gap), and 'terminal' (the item's
// lookup settled the outcome). 'blocked' is outstanding; completing around it
// would start maintenance over an incomplete initial archive.
func completeArchiveInitialIfReadyTx(ctx context.Context, tx *sql.Tx, repoID int64, now time.Time) error {
	_, err := tx.ExecContext(ctx, `
		UPDATE forge_archive_repos
		SET initial_completed_at = COALESCE(initial_completed_at, ?), updated_at = MAX(updated_at, ?)
		WHERE repo_id = ?
		  AND collection_mode = 'full'
		  AND (
			SELECT status FROM forge_archive_repo_scans
			WHERE repo_id = ? AND scan = 'issue_inventory'
		  ) = 'complete'
		  AND (
			SELECT status FROM forge_archive_repo_scans
			WHERE repo_id = ? AND scan = 'merge_request_inventory'
		  ) = 'complete'
		  AND NOT EXISTS (
			SELECT 1 FROM forge_archive_dataset_progress p
			JOIN forge_archive_items ai
			  ON ai.repo_id = p.repo_id AND ai.item_type = p.item_type AND ai.item_number = p.item_number
			WHERE p.repo_id = ? AND ai.lifecycle_state = 'active'
			  AND p.status IN ('pending', 'running', 'failed', 'blocked')
		  )`, now.UTC(), now.UTC(), repoID, repoID, repoID, repoID)
	if err != nil {
		return fmt.Errorf("complete initial archive: %w", err)
	}
	return nil
}

// reactivateTerminalArchiveWorkTx returns a terminal archive item to active
// after a present parent observation. Both terminal lifecycles recover —
// removed upstream and inaccessible — and recovery is unconditional on the
// provider timestamp: the observation itself is the proof of existence and
// access, even when coarse provider timestamps did not advance.
func reactivateTerminalArchiveWorkTx(
	ctx context.Context,
	tx *sql.Tx,
	repoID int64,
	itemType ArchiveItemType,
	itemNumber int,
) error {
	_, err := tx.ExecContext(ctx, `
		UPDATE forge_archive_items
		SET lifecycle_state = 'active'
		WHERE repo_id = ? AND item_type = ? AND item_number = ?
		  AND lifecycle_state IN ('removed_upstream', 'inaccessible')`, repoID, itemType, itemNumber)
	if err != nil {
		return fmt.Errorf("reactivate terminal archive work: %w", err)
	}
	return nil
}

func clearArchiveRepositoryFailureTx(
	ctx context.Context,
	tx *sql.Tx,
	repoID int64,
	now time.Time,
) error {
	_, err := tx.ExecContext(ctx, `
		UPDATE forge_archive_repos
		SET last_error_code = NULL, last_error_detail = NULL, next_retry_at = NULL,
			updated_at = MAX(updated_at, ?)
		WHERE repo_id = ?`, now.UTC(), repoID)
	if err != nil {
		return fmt.Errorf("clear archive repository failure: %w", err)
	}
	return nil
}

// upsertArchiveItemWorkTx records the item-level work row for one parent
// observation. Dataset state lives in forge_archive_dataset_progress; the
// work row only tracks provider identity, timestamps, lifecycle, and the
// refresh reason of the winning observation.
func upsertArchiveItemWorkTx(
	ctx context.Context,
	tx *sql.Tx,
	repoID int64,
	itemType ArchiveItemType,
	itemNumber int,
	providerItemID string,
	createdAt time.Time,
	updatedAt time.Time,
	refreshReason ArchiveRefreshReason,
) error {
	_, err := tx.ExecContext(ctx, `
		INSERT INTO forge_archive_items (
			repo_id, item_type, item_number, provider_item_id,
			provider_created_at, provider_updated_at,
			lifecycle_state, refresh_reason
		) VALUES (?, ?, ?, ?, ?, ?, 'active', ?)
		ON CONFLICT(repo_id, item_type, item_number) DO UPDATE SET
			provider_item_id = CASE WHEN excluded.provider_updated_at >= provider_updated_at THEN excluded.provider_item_id ELSE provider_item_id END,
			provider_created_at = CASE WHEN excluded.provider_updated_at >= provider_updated_at THEN excluded.provider_created_at ELSE provider_created_at END,
			provider_updated_at = MAX(provider_updated_at, excluded.provider_updated_at),
			lifecycle_state = CASE WHEN excluded.provider_updated_at > provider_updated_at THEN 'active' ELSE lifecycle_state END,
			refresh_reason = CASE WHEN excluded.provider_updated_at > provider_updated_at OR (excluded.provider_updated_at = provider_updated_at AND excluded.refresh_reason = 'prompt') THEN excluded.refresh_reason ELSE refresh_reason END`,
		repoID, itemType, itemNumber, providerItemID, createdAt, updatedAt, refreshReason,
	)
	if err != nil {
		return fmt.Errorf("upsert archive %s inventory work: %w", itemType, err)
	}
	return nil
}

func archiveRepositoryActiveTx(ctx context.Context, tx *sql.Tx, repoID int64) (bool, error) {
	var operator ArchiveOperatorState
	err := tx.QueryRowContext(ctx,
		`SELECT operator_state FROM forge_archive_repos WHERE repo_id = ?`, repoID,
	).Scan(&operator)
	if errors.Is(err, sql.ErrNoRows) {
		return false, &ArchiveRepoStateNotFoundError{RepoIDs: []int64{repoID}}
	}
	if err != nil {
		return false, fmt.Errorf("read archive repository operator state: %w", err)
	}
	return operator == ArchiveOperatorStateActive, nil
}
