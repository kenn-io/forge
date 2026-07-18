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
			if err := requireArchiveRepoIDs(ctx, tx, "middleman_repos", repoIDs); err != nil {
				return err
			}
		}
		for _, repoID := range repoIDs {
			_, err := tx.ExecContext(ctx, `
				INSERT INTO middleman_archive_repos (
					repo_id, collection_mode, operator_state,
					issue_cursor, issue_inventory_complete,
					merge_request_cursor, merge_request_inventory_complete,
					initial_started_at, initial_completed_at,
					maintenance_watermark, maintenance_succeeded_at,
					comments_coverage, reviews_coverage, inline_comments_coverage,
					last_error_code, last_error_detail, next_retry_at,
					created_at, updated_at
				) VALUES (
					?, 'discovery', 'active',
					NULL, 0, NULL, 0,
					NULL, NULL, NULL, NULL,
					'unknown', 'unknown', 'unknown',
					NULL, NULL, NULL, ?, ?
				)
				ON CONFLICT(repo_id) DO UPDATE SET
					operator_state = CASE
						WHEN middleman_archive_repos.last_error_code = 'configuration_removed' THEN 'active'
						ELSE middleman_archive_repos.operator_state END,
					last_error_code = CASE
						WHEN middleman_archive_repos.last_error_code = 'configuration_removed' THEN NULL
						ELSE middleman_archive_repos.last_error_code END,
					last_error_detail = CASE
						WHEN middleman_archive_repos.last_error_code = 'configuration_removed' THEN NULL
						ELSE middleman_archive_repos.last_error_detail END,
					next_retry_at = CASE
						WHEN middleman_archive_repos.last_error_code = 'configuration_removed' THEN NULL
						ELSE middleman_archive_repos.next_retry_at END,
					updated_at = CASE
						WHEN middleman_archive_repos.last_error_code = 'configuration_removed' THEN excluded.updated_at
						ELSE middleman_archive_repos.updated_at END`, repoID, now, now)
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
		query := `UPDATE middleman_archive_repos
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
		if err := requireArchiveRepoIDs(ctx, tx, "middleman_archive_repos", repoIDs); err != nil {
			return err
		}
		args := archiveRepoIDArgs(repoIDs)
		if _, err := tx.ExecContext(ctx, fmt.Sprintf(`
			UPDATE middleman_archive_items
			SET refresh_reason = 'initial'
			WHERE lifecycle_state = 'active'
			  AND repo_id IN (%s)
			  AND repo_id IN (
				SELECT repo_id FROM middleman_archive_repos WHERE collection_mode = 'discovery'
			  )`, sqlPlaceholders(len(repoIDs))), args...); err != nil {
			return fmt.Errorf("queue promoted archive items: %w", err)
		}
		promoteArgs := append([]any{formatDatasetProgressTime(now)}, args...)
		if _, err := tx.ExecContext(ctx, fmt.Sprintf(`
			UPDATE middleman_archive_dataset_progress
			SET scan_generation = scan_generation + (CASE WHEN status = 'complete' THEN 1 ELSE 0 END),
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
				SELECT repo_id FROM middleman_archive_repos WHERE collection_mode = 'discovery'
			  )
			  AND EXISTS (
				SELECT 1 FROM middleman_archive_items ai
				WHERE ai.repo_id = middleman_archive_dataset_progress.repo_id
				  AND ai.item_type = middleman_archive_dataset_progress.item_type
				  AND ai.item_number = middleman_archive_dataset_progress.item_number
				  AND ai.lifecycle_state = 'active'
			  )`, sqlPlaceholders(len(repoIDs))), promoteArgs...); err != nil {
			return fmt.Errorf("queue promoted archive dataset progress: %w", err)
		}
		updateArgs := append([]any{now, now}, args...)
		if _, err := tx.ExecContext(ctx, fmt.Sprintf(`
			UPDATE middleman_archive_repos
			SET collection_mode = 'full', operator_state = 'active',
				initial_started_at = COALESCE(initial_started_at, ?),
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
		if err := requireArchiveRepoIDs(ctx, tx, "middleman_archive_repos", repoIDs); err != nil {
			return err
		}
		args := append([]any{now}, archiveRepoIDArgs(repoIDs)...)
		_, err := tx.ExecContext(ctx, fmt.Sprintf(`
			UPDATE middleman_archive_repos
			SET operator_state = 'paused', updated_at = ?
			WHERE repo_id IN (%s) AND operator_state <> 'paused'`,
			sqlPlaceholders(len(repoIDs))), args...)
		if err != nil {
			return fmt.Errorf("pause archives: %w", err)
		}
		return nil
	})
}

// SetArchiveCoverage records provider capability coverage independently of
// work rows so unsupported datasets remain an honest repository-level partial.
func (d *DB) SetArchiveCoverage(ctx context.Context, repoID int64, coverage ArchiveCoverageSet, now time.Time) error {
	if repoID <= 0 {
		return fmt.Errorf("set archive coverage: repository ID is required")
	}
	for name, value := range map[string]ArchiveCoverage{
		"comments": coverage.Comments, "reviews": coverage.Reviews,
		"inline_comments": coverage.InlineComments,
	} {
		if value != ArchiveCoverageSupported && value != ArchiveCoverageUnsupported {
			return fmt.Errorf("set archive coverage: invalid %s coverage %q", name, value)
		}
	}
	result, err := d.rw.ExecContext(ctx, `
		UPDATE middleman_archive_repos
		SET comments_coverage = ?, reviews_coverage = ?, inline_comments_coverage = ?,
			updated_at = ?
		WHERE repo_id = ?`, coverage.Comments, coverage.Reviews,
		coverage.InlineComments, now.UTC(), repoID)
	if err != nil {
		return fmt.Errorf("set archive coverage: %w", err)
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("set archive coverage rows affected: %w", err)
	}
	if rows != 1 {
		return &ArchiveRepoStateNotFoundError{RepoIDs: []int64{repoID}}
	}
	return nil
}

// DeferArchiveRepository records provider-host admission waits without
// incrementing item attempts. A later successful request clears this state.
func (d *DB) DeferArchiveRepository(ctx context.Context, repoID int64, retryAt time.Time, detail string, now time.Time) error {
	result, err := d.rw.ExecContext(ctx, `
		UPDATE middleman_archive_repos
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
	_, err := d.rw.ExecContext(ctx, `
		UPDATE middleman_archive_repos
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
	_, err := d.rw.ExecContext(ctx, fmt.Sprintf(`
		UPDATE middleman_archive_repos
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
	if repoID <= 0 || code == "" {
		return errors.New("record archive repository failure: repository and error code are required")
	}
	var retry any
	if retryAt != nil {
		retry = retryAt.UTC()
	}
	result, err := d.rw.ExecContext(ctx, `
		UPDATE middleman_archive_repos
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
			SELECT prompt_scan_started_at FROM middleman_archive_repos
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
			UPDATE middleman_archive_repos
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
			UPDATE middleman_archive_repos
			SET maintenance_watermark = ?, maintenance_succeeded_at = ?,
				prompt_scan_started_at = NULL, prompt_since = NULL,
				last_error_code = NULL, last_error_detail = NULL, next_retry_at = NULL,
				updated_at = MAX(updated_at, ?)
			WHERE repo_id = ? AND collection_mode = 'full' AND operator_state = 'active'
			  AND (
				SELECT status FROM middleman_archive_repo_scans
				WHERE repo_id = ? AND scan = 'maintenance_issues'
			  ) = 'complete'
			  AND (
				SELECT status FROM middleman_archive_repo_scans
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
		UPDATE middleman_archive_repos
		SET maintenance_watermark = COALESCE(maintenance_watermark, initial_completed_at, created_at),
			maintenance_succeeded_at = NULL, updated_at = MAX(updated_at, ?)
		WHERE collection_mode = 'full' AND operator_state = 'active'
		  AND repo_id = (
			SELECT id FROM middleman_repos
			WHERE platform = ? AND platform_host = ? AND owner = ? AND name = ?
		  )`, now.UTC(), identity.Platform, identity.PlatformHost, identity.Owner, identity.Name)
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
			comments_coverage, reviews_coverage, inline_comments_coverage,
			last_error_code, last_error_detail, next_retry_at,
			created_at, updated_at
		FROM middleman_archive_repos`
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
		&state.CommentsCoverage, &state.ReviewsCoverage, &state.InlineCommentsCoverage,
		&state.LastErrorCode, &state.LastErrorDetail, &state.NextRetryAt,
		&state.CreatedAt, &state.UpdatedAt,
	)
}

// ClaimArchiveItem returns the oldest item with due dataset progress from the
// explicitly eligible repositories, together with its due datasets in scan
// order (lookup first). Claims are observational because the schema has no
// lease; the compare-and-swap on every progress commit makes duplicate
// scheduling safe.
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
		candidate, err := claimArchiveItemForRepo(ctx, tx, repoID, opts.Now)
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

// dueArchiveDatasetsQuery selects claimable dataset progress rows in stable
// item order: lookup before child datasets, children in scan order.
const dueArchiveDatasetsQuery = `
	SELECT ai.repo_id, ai.item_type, ai.item_number, ai.provider_created_at,
		p.dataset, p.parent_revision, p.scan_generation, p.next_cursor,
		p.status, p.attempt_count
	FROM middleman_archive_dataset_progress p
	JOIN middleman_archive_items ai
	  ON ai.repo_id = p.repo_id AND ai.item_type = p.item_type AND ai.item_number = p.item_number
	JOIN middleman_archive_repos ar ON ar.repo_id = p.repo_id
	WHERE p.repo_id = ?
	  AND ar.collection_mode = 'full'
	  AND ar.operator_state = 'active'
	  AND (ar.next_retry_at IS NULL OR ar.next_retry_at <= ?)
	  AND ai.lifecycle_state = 'active'
	  AND p.status IN ('pending', 'running', 'failed')
	  AND (p.next_retry_at IS NULL OR p.next_retry_at <= ?)
	ORDER BY ai.provider_created_at, ai.item_type, ai.item_number,
	  CASE p.dataset
		WHEN 'lookup' THEN 0
		WHEN 'comments' THEN 1
		WHEN 'reviews' THEN 2
		ELSE 3
	  END`

func claimArchiveItemForRepo(
	ctx context.Context,
	queryer archiveQueryer,
	repoID int64,
	now time.Time,
) (*ArchiveItemWork, error) {
	rows, err := queryer.QueryContext(
		ctx, dueArchiveDatasetsQuery, repoID, now, formatDatasetProgressTime(now),
	)
	if err != nil {
		return nil, fmt.Errorf("claim archive item: %w", err)
	}
	defer rows.Close()
	var work *ArchiveItemWork
	for rows.Next() {
		var repo int64
		var itemType ArchiveItemType
		var itemNumber int
		var createdAt time.Time
		var dataset ArchiveDatasetWork
		var nextCursor sql.NullString
		if err := rows.Scan(
			&repo, &itemType, &itemNumber, &createdAt,
			&dataset.Dataset, &dataset.ParentRevision, &dataset.ScanGeneration,
			&nextCursor, &dataset.Status, &dataset.AttemptCount,
		); err != nil {
			return nil, fmt.Errorf("scan claimable archive dataset: %w", err)
		}
		if nextCursor.Valid {
			dataset.NextCursor = &nextCursor.String
		}
		if work == nil {
			work = &ArchiveItemWork{
				RepoID: repo, ItemType: itemType, ItemNumber: itemNumber,
				ProviderCreatedAt: createdAt.UTC(),
			}
		} else if work.ItemType != itemType || work.ItemNumber != itemNumber {
			break
		}
		work.Datasets = append(work.Datasets, dataset)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("claim archive item: %w", err)
	}
	return work, nil
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

// GetDueDatasetWork returns the currently due datasets of one item in scan
// order. Hydration re-derives due work after its lookup commits, because a
// present lookup rebinds and reopens child datasets.
func (d *DB) GetDueDatasetWork(
	ctx context.Context,
	repoID int64,
	itemType ArchiveItemType,
	itemNumber int,
	now time.Time,
) ([]ArchiveDatasetWork, error) {
	return d.dueDatasetsForItem(ctx, repoID, itemType, itemNumber, now.UTC())
}

func (d *DB) dueDatasetsForItem(
	ctx context.Context,
	repoID int64,
	itemType ArchiveItemType,
	itemNumber int,
	now time.Time,
) ([]ArchiveDatasetWork, error) {
	rows, err := d.ro.QueryContext(ctx, `
		SELECT p.dataset, p.parent_revision, p.scan_generation, p.next_cursor,
			p.status, p.attempt_count
		FROM middleman_archive_dataset_progress p
		WHERE p.repo_id = ? AND p.item_type = ? AND p.item_number = ?
		  AND p.status IN ('pending', 'running', 'failed')
		  AND (p.next_retry_at IS NULL OR p.next_retry_at <= ?)
		ORDER BY CASE p.dataset
			WHEN 'lookup' THEN 0
			WHEN 'comments' THEN 1
			WHEN 'reviews' THEN 2
			ELSE 3
		  END`,
		repoID, itemType, itemNumber, formatDatasetProgressTime(now))
	if err != nil {
		return nil, fmt.Errorf("list due archive datasets: %w", err)
	}
	defer rows.Close()
	var datasets []ArchiveDatasetWork
	for rows.Next() {
		var dataset ArchiveDatasetWork
		var nextCursor sql.NullString
		if err := rows.Scan(
			&dataset.Dataset, &dataset.ParentRevision, &dataset.ScanGeneration,
			&nextCursor, &dataset.Status, &dataset.AttemptCount,
		); err != nil {
			return nil, fmt.Errorf("scan due archive dataset: %w", err)
		}
		if nextCursor.Valid {
			dataset.NextCursor = &nextCursor.String
		}
		datasets = append(datasets, dataset)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list due archive datasets: %w", err)
	}
	return datasets, nil
}

// ArchiveItemHydration marks every applicable dataset of one archive item
// after its provider data has been completely persisted.
type ArchiveItemHydration struct {
	RepoID            int64
	ItemType          ArchiveItemType
	ItemNumber        int
	DomainRevision    int64
	CommentsStatus    ArchiveDatasetStatus
	ReviewsStatus     ArchiveDatasetStatus
	InlineStatus      ArchiveDatasetStatus
	MirroredUpdatedAt time.Time
	HydratedAt        time.Time
}

// MarkArchiveItemHydrated records a completed hydration, clears retry state,
// and completes the repository's initial archive when it was the last pending
// item. It never touches domain content rows.
func (d *DB) MarkArchiveItemHydrated(ctx context.Context, h ArchiveItemHydration) error {
	if h.RepoID <= 0 || h.ItemNumber <= 0 || h.DomainRevision <= 0 {
		return fmt.Errorf("mark archive item hydrated: repo ID, item number, and domain revision are required")
	}
	parentTable := ""
	switch h.ItemType {
	case ArchiveItemTypeIssue:
		parentTable = "middleman_issues"
	case ArchiveItemTypeMergeRequest:
		parentTable = "middleman_merge_requests"
	default:
		return fmt.Errorf("mark archive item hydrated: invalid item type %q", h.ItemType)
	}
	return d.Tx(ctx, func(tx *sql.Tx) error {
		result, err := tx.ExecContext(ctx, fmt.Sprintf(`
			UPDATE middleman_archive_items
			SET comments_status = ?, reviews_status = ?, inline_comments_status = ?,
				mirrored_provider_updated_at = ?, hydrated_at = ?,
				attempt_count = 0, next_retry_at = NULL,
				last_error_code = NULL, last_error_detail = NULL
			WHERE repo_id = ? AND item_type = ? AND item_number = ?
			  AND lifecycle_state = 'active' AND provider_updated_at = ?
			  AND EXISTS (
				SELECT 1 FROM %s
				WHERE repo_id = ? AND number = ? AND updated_at = ?
				  AND snapshot_revision = ?
			  )`, parentTable),
			h.CommentsStatus, h.ReviewsStatus, h.InlineStatus,
			h.MirroredUpdatedAt.UTC(), h.HydratedAt.UTC(),
			h.RepoID, h.ItemType, h.ItemNumber, h.MirroredUpdatedAt.UTC(),
			h.RepoID, h.ItemNumber, h.MirroredUpdatedAt.UTC(), h.DomainRevision,
		)
		if err != nil {
			return fmt.Errorf("mark archive item hydrated: %w", err)
		}
		changed, err := result.RowsAffected()
		if err != nil {
			return fmt.Errorf("mark archive item hydrated rows affected: %w", err)
		}
		if changed == 0 {
			var workUpdatedAt, parentUpdatedAt time.Time
			var parentRevision int64
			var lifecycle ArchiveLifecycleState
			if err := tx.QueryRowContext(ctx, fmt.Sprintf(`
				SELECT a.provider_updated_at, a.lifecycle_state, p.updated_at, p.snapshot_revision
				FROM middleman_archive_items a
				JOIN %s p ON p.repo_id = a.repo_id AND p.number = a.item_number
				WHERE a.repo_id = ? AND a.item_type = ? AND a.item_number = ?`, parentTable),
				h.RepoID, h.ItemType, h.ItemNumber,
			).Scan(&workUpdatedAt, &lifecycle, &parentUpdatedAt, &parentRevision); err != nil {
				return fmt.Errorf("read archive item after rejected hydration finalization: %w", err)
			}
			if lifecycle != ArchiveLifecycleStateActive {
				return nil
			}
			if parentRevision != h.DomainRevision {
				if err := requeueArchiveItemDatasetsTx(
					ctx, tx, h.RepoID, h.ItemType, h.ItemNumber,
				); err != nil {
					return err
				}
			}
			winningUpdatedAt := workUpdatedAt
			if parentUpdatedAt.After(winningUpdatedAt) {
				winningUpdatedAt = parentUpdatedAt
			}
			return reconcileArchiveItemSnapshotTx(
				ctx, tx, h.RepoID, h.ItemType, h.ItemNumber, winningUpdatedAt, parentRevision,
			)
		}
		if err := clearArchiveRepositoryFailureTx(ctx, tx, h.RepoID, h.HydratedAt); err != nil {
			return err
		}
		return completeArchiveInitialIfReadyTx(ctx, tx, h.RepoID, h.HydratedAt)
	})
}

func requeueArchiveItemDatasetsTx(
	ctx context.Context,
	tx *sql.Tx,
	repoID int64,
	itemType ArchiveItemType,
	itemNumber int,
) error {
	_, err := tx.ExecContext(ctx, `
		UPDATE middleman_archive_items
		SET comments_status = CASE
				WHEN comments_status IN ('unsupported', 'not_applicable') THEN comments_status
				ELSE 'pending' END,
			reviews_status = CASE
				WHEN reviews_status IN ('unsupported', 'not_applicable') THEN reviews_status
				ELSE 'pending' END,
			inline_comments_status = CASE
				WHEN inline_comments_status IN ('unsupported', 'not_applicable') THEN inline_comments_status
				ELSE 'pending' END,
			hydrated_at = NULL, attempt_count = 0, next_retry_at = NULL,
			last_error_code = NULL, last_error_detail = NULL
		WHERE repo_id = ? AND item_type = ? AND item_number = ?
		  AND lifecycle_state = 'active'`, repoID, itemType, itemNumber)
	if err != nil {
		return fmt.Errorf("requeue archive datasets after parent revision changed: %w", err)
	}
	return nil
}

// ArchiveItemTerminal marks an archive item's lifecycle as removed upstream or
// inaccessible. Domain issue/merge-request content is deliberately retained.
type ArchiveItemTerminal struct {
	RepoID      int64
	ItemType    ArchiveItemType
	ItemNumber  int
	Lifecycle   ArchiveLifecycleState
	ErrorCode   string
	ErrorDetail string
	At          time.Time
}

// MarkArchiveItemTerminal records a terminal provider lookup outcome on the
// archive work row only; archived content stays in place.
func (d *DB) MarkArchiveItemTerminal(ctx context.Context, t ArchiveItemTerminal) error {
	if t.RepoID <= 0 || t.ItemNumber <= 0 {
		return fmt.Errorf("mark archive item terminal: repo ID and item number are required")
	}
	switch t.Lifecycle {
	case ArchiveLifecycleStateRemovedUpstream, ArchiveLifecycleStateInaccessible:
	case ArchiveLifecycleStateActive:
		return fmt.Errorf("mark archive item terminal: %q is not a terminal lifecycle state", t.Lifecycle)
	default:
		return fmt.Errorf("mark archive item terminal: unknown lifecycle state %q", t.Lifecycle)
	}
	return d.Tx(ctx, func(tx *sql.Tx) error {
		return markArchiveItemTerminalTx(ctx, tx, t)
	})
}

func markArchiveItemTerminalTx(ctx context.Context, tx *sql.Tx, t ArchiveItemTerminal) error {
	result, err := tx.ExecContext(ctx, `
		UPDATE middleman_archive_items
		SET lifecycle_state = ?, next_retry_at = NULL,
			last_error_code = ?, last_error_detail = ?, hydrated_at = ?
		WHERE repo_id = ? AND item_type = ? AND item_number = ?`,
		t.Lifecycle, nullableArchiveError(t.ErrorCode),
		nullableArchiveError(sanitizeArchiveErrorDetail(t.ErrorDetail)), t.At.UTC(),
		t.RepoID, t.ItemType, t.ItemNumber,
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
	if _, err := tx.ExecContext(ctx, `
		DELETE FROM middleman_archive_dataset_pages
		WHERE repo_id = ? AND item_type = ? AND item_number = ?`,
		t.RepoID, t.ItemType, t.ItemNumber); err != nil {
		return fmt.Errorf("clear terminal archive dataset stages: %w", err)
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

// FailArchiveItem records retry state without replacing provider-owned content
// or changing already terminal dataset statuses.
func (d *DB) FailArchiveItem(ctx context.Context, failure ArchiveItemFailure) error {
	if failure.RepoID <= 0 || failure.ItemNumber <= 0 || failure.Code == "" {
		return fmt.Errorf("fail archive item: repo ID, item number, and error code are required")
	}
	datasets, err := normalizedArchiveDatasets(failure.Datasets)
	if err != nil {
		return err
	}
	comments := datasets[ArchiveDatasetComments]
	reviews := datasets[ArchiveDatasetReviews]
	inlineComments := datasets[ArchiveDatasetInlineComments]
	nextRetryAt := failure.NextRetryAt
	if nextRetryAt != nil {
		normalized := nextRetryAt.UTC()
		nextRetryAt = &normalized
	}
	result, err := d.rw.ExecContext(ctx, `
		UPDATE middleman_archive_items
		SET comments_status = CASE
				WHEN ? AND comments_status IN ('pending', 'failed') THEN 'failed'
				ELSE comments_status END,
			reviews_status = CASE
				WHEN ? AND reviews_status IN ('pending', 'failed') THEN 'failed'
				ELSE reviews_status END,
			inline_comments_status = CASE
				WHEN ? AND inline_comments_status IN ('pending', 'failed') THEN 'failed'
				ELSE inline_comments_status END,
			attempt_count = attempt_count + 1,
			next_retry_at = ?, last_error_code = ?, last_error_detail = ?
		WHERE repo_id = ? AND item_type = ? AND item_number = ?
		  AND lifecycle_state = 'active'
		  AND (
			(? AND comments_status IN ('pending', 'failed'))
			OR (? AND reviews_status IN ('pending', 'failed'))
			OR (? AND inline_comments_status IN ('pending', 'failed'))
		  )`,
		comments, reviews, inlineComments,
		nextRetryAt, failure.Code, sanitizeArchiveErrorDetail(failure.Detail),
		failure.RepoID, failure.ItemType, failure.ItemNumber,
		comments, reviews, inlineComments,
	)
	if err != nil {
		return fmt.Errorf("fail archive item: %w", err)
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("fail archive item rows affected: %w", err)
	}
	if changed == 0 {
		return fmt.Errorf(
			"fail archive item: no mutable dataset for repo %d %s %d",
			failure.RepoID, failure.ItemType, failure.ItemNumber,
		)
	}
	return nil
}

func normalizedArchiveDatasets(datasets []ArchiveDataset) (map[ArchiveDataset]bool, error) {
	if len(datasets) == 0 {
		return nil, fmt.Errorf("fail archive item: at least one dataset is required")
	}
	result := make(map[ArchiveDataset]bool, len(datasets))
	for _, dataset := range datasets {
		switch dataset {
		case ArchiveDatasetComments, ArchiveDatasetReviews, ArchiveDatasetInlineComments:
			result[dataset] = true
		default:
			return nil, fmt.Errorf("fail archive item: unknown dataset %q", dataset)
		}
	}
	return result, nil
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
		FROM middleman_archive_items ai
		LEFT JOIN (
			SELECT repo_id, item_type, item_number,
				SUM(CASE WHEN status IN ('pending', 'running') THEN 1 ELSE 0 END) AS pending_count,
				SUM(CASE WHEN status = 'failed' THEN 1 ELSE 0 END) AS failed_count,
				SUM(CASE WHEN status = 'unsupported' THEN 1 ELSE 0 END) AS unsupported_count,
				SUM(CASE WHEN status = 'blocked' THEN 1 ELSE 0 END) AS blocked_count,
				SUM(CASE WHEN status IN ('pending', 'running', 'failed')
					AND (next_retry_at IS NULL OR next_retry_at <= ?) THEN 1 ELSE 0 END) AS due_count,
				SUM(CASE WHEN status IN ('pending', 'running', 'failed') THEN 1 ELSE 0 END) AS open_count
			FROM middleman_archive_dataset_progress
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
	return state.CommentsCoverage == ArchiveCoverageUnsupported ||
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
	if table == "middleman_repos" {
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

// CommitArchiveInventoryPage durably applies one inventory or prompt page in a
// single transaction: parent snapshots with labels, archive work rows, and the
// scan-row cursor advance, so a crash never splits a page. The scan row is
// compare-and-swapped on generation and input cursor; typed stale, replay, and
// blocked outcomes never write parent rows.
func (d *DB) CommitArchiveInventoryPage(ctx context.Context, commit ArchiveInventoryCommit) error {
	if commit.RefreshReason == "" {
		commit.RefreshReason = ArchiveRefreshReasonInitial
	}
	if commit.ScanGeneration == 0 {
		commit.ScanGeneration = 1
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
		for i := range commit.Issues {
			if _, _, _, err := commitArchiveIssueParentTx(
				ctx, tx, commit.RepoID, &commit.Issues[i], commit.RefreshReason,
			); err != nil {
				return err
			}
		}
		for i := range commit.MergeRequests {
			if _, _, _, err := commitArchiveMergeRequestParentTx(
				ctx, tx, commit.RepoID, &commit.MergeRequests[i], commit.RefreshReason,
				mergeRequestDetailPreserveProviderDetail,
			); err != nil {
				return err
			}
		}
		if !outcome.refreshOnly {
			if err := advanceArchiveScanTx(ctx, tx, commit, kind, outcome.newPageCount, commit.Now); err != nil {
				return err
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

// commitArchiveIssueParentTx is the issue mode of the single archive
// parent-observation core: parent snapshot upsert with labels, terminal-work
// reactivation, and per-item archive work reconciliation. Inventory pages and
// single-item lookups both flow through it.
func commitArchiveIssueParentTx(
	ctx context.Context,
	tx *sql.Tx,
	repoID int64,
	item *ArchiveInventoryIssue,
	refreshReason ArchiveRefreshReason,
) (int64, int64, bool, error) {
	issue := &item.Snapshot.Issue
	issue.RepoID = repoID
	issueID, revision, accepted, err := commitIssueParentSnapshotTx(
		ctx, tx, issue, item.Snapshot.Labels,
	)
	if err != nil {
		return 0, 0, false, err
	}
	if !accepted {
		// The domain row holds a newer snapshot than this observation. The
		// archive work row must track the winning provider timestamp so later
		// maintenance passes do not treat already-superseded activity as new.
		if err := tx.QueryRowContext(ctx, `
			SELECT updated_at FROM middleman_issues WHERE id = ?`, issueID,
		).Scan(&issue.UpdatedAt); err != nil {
			return 0, 0, false, fmt.Errorf("read winning issue snapshot: %w", err)
		}
	}
	if err := reactivateTerminalArchiveWorkTx(
		ctx, tx, repoID, ArchiveItemTypeIssue, issue.Number,
	); err != nil {
		return 0, 0, false, err
	}
	if err := upsertArchiveIssueWorkTx(ctx, tx, repoID, *item, refreshReason); err != nil {
		return 0, 0, false, err
	}
	if err := seedArchiveDatasetProgressTx(ctx, tx, repoID, ArchiveItemTypeIssue, issue.Number,
		map[ArchiveDataset]ArchiveDatasetProgressStatus{
			ArchiveDatasetLookup:   ArchiveDatasetProgressPending,
			ArchiveDatasetComments: progressStatusForSeed(item.CommentsStatus),
		}); err != nil {
		return 0, 0, false, err
	}
	if err := rebindArchiveDatasetProgressTx(
		ctx, tx, DomainParentRef{ItemType: ArchiveItemTypeIssue, ID: issueID},
		repoID, issue.Number, revision, refreshReason,
	); err != nil {
		return 0, 0, false, err
	}
	return issueID, revision, accepted, nil
}

// commitArchiveMergeRequestParentTx is the merge-request mode of the archive
// parent-observation core shared by inventory pages and single-item lookups.
// Inventory pages carry list-grade snapshots and preserve live-owned provider
// detail; lookups carry detailed snapshots and apply provider diff detail
// while keeping derived review/CI state.
func commitArchiveMergeRequestParentTx(
	ctx context.Context,
	tx *sql.Tx,
	repoID int64,
	item *ArchiveInventoryMergeRequest,
	refreshReason ArchiveRefreshReason,
	detail mergeRequestDetailMode,
) (int64, int64, bool, error) {
	mr := &item.Snapshot.MergeRequest
	mr.RepoID = repoID
	mrID, revision, accepted, err := commitMergeRequestParentSnapshotTx(
		ctx, tx, mr, item.Snapshot.Labels, detail,
	)
	if err != nil {
		return 0, 0, false, err
	}
	if !accepted {
		// See commitArchiveIssueParentTx: rejected observations still bind the
		// archive work row to the winning provider timestamp.
		if err := tx.QueryRowContext(ctx, `
			SELECT updated_at FROM middleman_merge_requests WHERE id = ?`, mrID,
		).Scan(&mr.UpdatedAt); err != nil {
			return 0, 0, false, fmt.Errorf("read winning merge-request snapshot: %w", err)
		}
	}
	if err := reactivateTerminalArchiveWorkTx(
		ctx, tx, repoID, ArchiveItemTypeMergeRequest, mr.Number,
	); err != nil {
		return 0, 0, false, err
	}
	if err := upsertArchiveMergeRequestWorkTx(ctx, tx, repoID, *item, refreshReason); err != nil {
		return 0, 0, false, err
	}
	if err := seedArchiveDatasetProgressTx(ctx, tx, repoID, ArchiveItemTypeMergeRequest, mr.Number,
		map[ArchiveDataset]ArchiveDatasetProgressStatus{
			ArchiveDatasetLookup:         ArchiveDatasetProgressPending,
			ArchiveDatasetComments:       progressStatusForSeed(item.CommentsStatus),
			ArchiveDatasetReviews:        progressStatusForSeed(item.ReviewsStatus),
			ArchiveDatasetInlineComments: progressStatusForSeed(item.InlineCommentsStatus),
		}); err != nil {
		return 0, 0, false, err
	}
	if err := rebindArchiveDatasetProgressTx(
		ctx, tx, DomainParentRef{ItemType: ArchiveItemTypeMergeRequest, ID: mrID},
		repoID, mr.Number, revision, refreshReason,
	); err != nil {
		return 0, 0, false, err
	}
	return mrID, revision, accepted, nil
}

// progressStatusForSeed maps a capability-derived inventory dataset status to
// the seeded progress-row status.
func progressStatusForSeed(status ArchiveDatasetStatus) ArchiveDatasetProgressStatus {
	if status == ArchiveDatasetStatusUnsupported {
		return ArchiveDatasetProgressUnsupported
	}
	return ArchiveDatasetProgressPending
}

// seedArchiveDatasetProgressTx creates missing dataset progress rows for one
// archive item. Existing rows are never modified here; rebinding to newer
// parent revisions is a separate reopen step.
func seedArchiveDatasetProgressTx(
	ctx context.Context,
	tx *sql.Tx,
	repoID int64,
	itemType ArchiveItemType,
	itemNumber int,
	datasets map[ArchiveDataset]ArchiveDatasetProgressStatus,
) error {
	nowText := formatDatasetProgressTime(time.Now())
	for _, dataset := range []ArchiveDataset{
		ArchiveDatasetLookup, ArchiveDatasetComments,
		ArchiveDatasetReviews, ArchiveDatasetInlineComments,
	} {
		status, ok := datasets[dataset]
		if !ok {
			continue
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO middleman_archive_dataset_progress (
				repo_id, item_type, item_number, dataset, status, updated_at
			) VALUES (?, ?, ?, ?, ?, ?)
			ON CONFLICT(repo_id, item_type, item_number, dataset) DO NOTHING`,
			repoID, itemType, itemNumber, dataset, status, nowText); err != nil {
			return fmt.Errorf("seed archive dataset progress %s: %w", dataset, err)
		}
	}
	return nil
}

// rebindArchiveDatasetProgressTx binds an item's dataset progress to the
// current parent revision after a parent observation. Initial observations
// reopen datasets superseded by a newer revision; prompt observations also
// refresh equal-revision datasets once, because coarse provider timestamps
// can hide activity behind an unchanged updated_at.
func rebindArchiveDatasetProgressTx(
	ctx context.Context,
	tx *sql.Tx,
	parent DomainParentRef,
	repoID int64,
	itemNumber int,
	revision int64,
	refreshReason ArchiveRefreshReason,
) error {
	if err := reopenDatasetsForParentTx(ctx, tx, parent, repoID, itemNumber, revision); err != nil {
		return err
	}
	if err := reopenLookupForRevisionTx(ctx, tx, repoID, parent.ItemType, itemNumber, revision, false); err != nil {
		return err
	}
	if refreshReason != ArchiveRefreshReasonPrompt {
		return nil
	}
	if _, err := tx.ExecContext(ctx, `
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
		  AND parent_revision = ? AND status = 'complete'`,
		revision, formatDatasetProgressTime(time.Now()),
		repoID, parent.ItemType, itemNumber, revision); err != nil {
		return fmt.Errorf("refresh equal-revision datasets for prompt observation: %w", err)
	}
	if err := reopenLookupForRevisionTx(ctx, tx, repoID, parent.ItemType, itemNumber, revision, true); err != nil {
		return err
	}
	return nil
}

func preserveMergeRequestDetailTx(
	ctx context.Context,
	tx *sql.Tx,
	mr *MergeRequest,
	preserveProviderDetail bool,
) (bool, error) {
	var existing MergeRequest
	err := tx.QueryRowContext(ctx, `
		SELECT platform_head_sha, platform_base_sha,
			additions, deletions, comment_count, review_decision, ci_status,
			ci_checks_json, detail_fetched_at, ci_had_pending, mergeable_state
		FROM middleman_merge_requests
		WHERE repo_id = ? AND number = ?`, mr.RepoID, mr.Number,
	).Scan(&existing.PlatformHeadSHA, &existing.PlatformBaseSHA,
		&existing.Additions, &existing.Deletions, &existing.CommentCount,
		&existing.ReviewDecision, &existing.CIStatus, &existing.CIChecksJSON,
		&existing.DetailFetchedAt, &existing.CIHadPending, &existing.MergeableState)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("preserve merge request detail before archive upsert: %w", err)
	}
	headChanged := mr.PlatformHeadSHA != "" && existing.PlatformHeadSHA != "" && mr.PlatformHeadSHA != existing.PlatformHeadSHA
	baseChanged := mr.PlatformBaseSHA != "" && existing.PlatformBaseSHA != "" && mr.PlatformBaseSHA != existing.PlatformBaseSHA
	if preserveProviderDetail {
		mr.CommentCount = existing.CommentCount
	}
	if headChanged || baseChanged {
		return true, nil
	}
	if mr.PlatformHeadSHA == "" {
		mr.PlatformHeadSHA = existing.PlatformHeadSHA
	}
	if mr.PlatformBaseSHA == "" {
		mr.PlatformBaseSHA = existing.PlatformBaseSHA
	}
	if preserveProviderDetail {
		mr.Additions = existing.Additions
		mr.Deletions = existing.Deletions
		mr.MergeableState = existing.MergeableState
	} else if mr.MergeableState == "" || (mr.MergeableState == "unknown" && existing.MergeableState != "") {
		mr.MergeableState = existing.MergeableState
	}
	mr.ReviewDecision = existing.ReviewDecision
	mr.CIStatus = existing.CIStatus
	mr.CIChecksJSON = existing.CIChecksJSON
	mr.DetailFetchedAt = existing.DetailFetchedAt
	mr.CIHadPending = existing.CIHadPending
	return false, nil
}

func clearArchiveMergeRequestDiffDetailTx(ctx context.Context, tx *sql.Tx, mrID int64) error {
	if _, err := tx.ExecContext(ctx, `
		UPDATE middleman_merge_requests
		SET detail_fetched_at = NULL, ci_had_pending = 0
		WHERE id = ?`, mrID); err != nil {
		return fmt.Errorf("clear diff-bound merge request detail after archive upsert: %w", err)
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
		if len(commit.MergeRequests) != 0 {
			return errors.New("commit archive inventory: issue page contains merge requests")
		}
		for i := range commit.Issues {
			if commit.Issues[i].Snapshot.Issue.Number <= 0 {
				return fmt.Errorf("commit archive inventory: issue number must be positive")
			}
		}
	case ArchiveItemTypeMergeRequest:
		if len(commit.Issues) != 0 {
			return errors.New("commit archive inventory: merge request page contains issues")
		}
		for i := range commit.MergeRequests {
			if commit.MergeRequests[i].Snapshot.MergeRequest.Number <= 0 {
				return fmt.Errorf("commit archive inventory: merge request number must be positive")
			}
		}
	default:
		return fmt.Errorf("commit archive inventory: invalid item type %q", commit.ItemType)
	}
	switch commit.RefreshReason {
	case ArchiveRefreshReasonInitial, ArchiveRefreshReasonPrompt:
	default:
		return fmt.Errorf("commit archive inventory: invalid refresh reason %q", commit.RefreshReason)
	}
	return nil
}

// UpsertArchiveIssueSnapshot applies a provider parent and its labels in one
// transaction. Labels advance only when the monotonic parent snapshot wins.
// The acceptance result must also gate every dependent archive dataset.
func (d *DB) UpsertArchiveIssueSnapshot(ctx context.Context, snapshot *Issue) (int64, int64, bool, error) {
	canonicalizeIssueTimestamps(snapshot)
	var id int64
	var revision int64
	var accepted bool
	err := d.Tx(ctx, func(tx *sql.Tx) error {
		var resume bool
		var err error
		id, revision, resume, err = resumableArchiveParentTx(
			ctx, tx, snapshot.RepoID, ArchiveItemTypeIssue,
			snapshot.Number, snapshot.UpdatedAt,
		)
		if err != nil {
			return err
		}
		if resume {
			accepted = true
			return nil
		}
		id, revision, accepted, err = commitIssueParentSnapshotTx(ctx, tx, snapshot, snapshot.Labels)
		if err != nil {
			return err
		}
		winningUpdatedAt := snapshot.UpdatedAt
		if !accepted {
			if err := tx.QueryRowContext(ctx, `
				SELECT updated_at FROM middleman_issues WHERE id = ?`, id,
			).Scan(&winningUpdatedAt); err != nil {
				return fmt.Errorf("read winning issue snapshot: %w", err)
			}
		}
		return reconcileArchiveItemSnapshotTx(
			ctx, tx, snapshot.RepoID, ArchiveItemTypeIssue, snapshot.Number, winningUpdatedAt, revision,
		)
	})
	return id, revision, accepted, err
}

// UpsertArchiveMergeRequestSnapshot is the merge-request counterpart to
// UpsertArchiveIssueSnapshot.
func (d *DB) UpsertArchiveMergeRequestSnapshot(ctx context.Context, snapshot *MergeRequest) (int64, int64, bool, error) {
	canonicalizeMergeRequestTimestamps(snapshot)
	var id int64
	var revision int64
	var accepted bool
	err := d.Tx(ctx, func(tx *sql.Tx) error {
		var resume bool
		var err error
		id, revision, resume, err = resumableArchiveParentTx(
			ctx, tx, snapshot.RepoID, ArchiveItemTypeMergeRequest,
			snapshot.Number, snapshot.UpdatedAt,
		)
		if err != nil {
			return err
		}
		if resume {
			accepted = true
			return nil
		}
		id, revision, accepted, err = commitMergeRequestParentSnapshotTx(
			ctx, tx, snapshot, snapshot.Labels, mergeRequestDetailPreserveDerived,
		)
		if err != nil {
			return err
		}
		winningUpdatedAt := snapshot.UpdatedAt
		if !accepted {
			if err := tx.QueryRowContext(ctx, `
				SELECT updated_at FROM middleman_merge_requests WHERE id = ?`, id,
			).Scan(&winningUpdatedAt); err != nil {
				return fmt.Errorf("read winning merge-request snapshot: %w", err)
			}
		}
		return reconcileArchiveItemSnapshotTx(
			ctx, tx, snapshot.RepoID, ArchiveItemTypeMergeRequest, snapshot.Number, winningUpdatedAt, revision,
		)
	})
	return id, revision, accepted, err
}

// resumableArchiveParentTx returns the parent revision that already owns a
// staged page for the same provider timestamp. Archive retries must keep that
// revision so they resume its cursor; a normal-sync parent advance makes the
// stage ineligible and the next archive snapshot starts a fresh revision.
func resumableArchiveParentTx(
	ctx context.Context,
	tx *sql.Tx,
	repoID int64,
	itemType ArchiveItemType,
	itemNumber int,
	updatedAt time.Time,
) (int64, int64, bool, error) {
	table := "middleman_issues"
	if itemType == ArchiveItemTypeMergeRequest {
		table = "middleman_merge_requests"
	}
	var id, revision int64
	err := tx.QueryRowContext(ctx, fmt.Sprintf(`
		SELECT p.id, p.snapshot_revision
		FROM %s p
		WHERE p.repo_id = ? AND p.number = ? AND p.updated_at = ?
		  AND EXISTS (
			SELECT 1 FROM middleman_archive_dataset_pages pages
			WHERE pages.repo_id = p.repo_id
			  AND pages.item_type = ?
			  AND pages.item_number = p.number
			  AND pages.snapshot_updated_at = p.updated_at
			  AND pages.domain_revision = p.snapshot_revision
		  )`, table), repoID, itemNumber, updatedAt.UTC(), itemType,
	).Scan(&id, &revision)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, 0, false, nil
	}
	if err != nil {
		return 0, 0, false, fmt.Errorf("find resumable archive parent: %w", err)
	}
	return id, revision, true, nil
}

func reconcileArchiveItemSnapshotTx(
	ctx context.Context,
	tx *sql.Tx,
	repoID int64,
	itemType ArchiveItemType,
	itemNumber int,
	winningUpdatedAt time.Time,
	winningRevision int64,
) error {
	winningUpdatedAt = winningUpdatedAt.UTC()
	if _, err := tx.ExecContext(ctx, `
		UPDATE middleman_archive_items
		SET provider_updated_at = MAX(provider_updated_at, ?),
			hydration_snapshot_updated_at = CASE
				WHEN ? > hydration_snapshot_updated_at THEN ?
				ELSE hydration_snapshot_updated_at END,
			comments_status = CASE
				WHEN ? > hydration_snapshot_updated_at
					AND comments_status NOT IN ('unsupported', 'not_applicable') THEN 'pending'
				ELSE comments_status END,
			reviews_status = CASE
				WHEN ? > hydration_snapshot_updated_at
					AND reviews_status NOT IN ('unsupported', 'not_applicable') THEN 'pending'
				ELSE reviews_status END,
			inline_comments_status = CASE
				WHEN ? > hydration_snapshot_updated_at
					AND inline_comments_status NOT IN ('unsupported', 'not_applicable') THEN 'pending'
				ELSE inline_comments_status END,
			next_retry_at = CASE
				WHEN ? > hydration_snapshot_updated_at THEN NULL
				ELSE next_retry_at END,
			last_error_code = CASE
				WHEN ? > hydration_snapshot_updated_at THEN NULL
				ELSE last_error_code END,
			last_error_detail = CASE
				WHEN ? > hydration_snapshot_updated_at THEN NULL
				ELSE last_error_detail END
		WHERE repo_id = ? AND item_type = ? AND item_number = ?`,
		winningUpdatedAt, winningUpdatedAt, winningUpdatedAt,
		winningUpdatedAt, winningUpdatedAt, winningUpdatedAt,
		winningUpdatedAt, winningUpdatedAt, winningUpdatedAt,
		repoID, itemType, itemNumber,
	); err != nil {
		return fmt.Errorf("reconcile archive item snapshot: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE middleman_archive_dataset_pages
		SET domain_revision = ?
		WHERE repo_id = ? AND item_type = ? AND item_number = ?
		  AND snapshot_updated_at = ? AND domain_revision <> ?`,
		winningRevision, repoID, itemType, itemNumber, winningUpdatedAt, winningRevision,
	); err != nil {
		return fmt.Errorf("rebind resumable archive dataset stages: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `
		DELETE FROM middleman_archive_dataset_pages
		WHERE repo_id = ? AND item_type = ? AND item_number = ?
		  AND snapshot_updated_at <> ?`,
		repoID, itemType, itemNumber, winningUpdatedAt,
	); err != nil {
		return fmt.Errorf("discard rejected archive dataset stages: %w", err)
	}
	return nil
}

func completeArchiveInitialIfReadyTx(ctx context.Context, tx *sql.Tx, repoID int64, now time.Time) error {
	_, err := tx.ExecContext(ctx, `
		UPDATE middleman_archive_repos
		SET initial_completed_at = COALESCE(initial_completed_at, ?), updated_at = MAX(updated_at, ?)
		WHERE repo_id = ?
		  AND collection_mode = 'full'
		  AND (
			SELECT status FROM middleman_archive_repo_scans
			WHERE repo_id = ? AND scan = 'issue_inventory'
		  ) = 'complete'
		  AND (
			SELECT status FROM middleman_archive_repo_scans
			WHERE repo_id = ? AND scan = 'merge_request_inventory'
		  ) = 'complete'
		  AND NOT EXISTS (
			SELECT 1 FROM middleman_archive_dataset_progress p
			JOIN middleman_archive_items ai
			  ON ai.repo_id = p.repo_id AND ai.item_type = p.item_type AND ai.item_number = p.item_number
			WHERE p.repo_id = ? AND ai.lifecycle_state = 'active'
			  AND p.status IN ('pending', 'running', 'failed')
		  )`, now.UTC(), now.UTC(), repoID, repoID, repoID, repoID)
	if err != nil {
		return fmt.Errorf("complete initial archive: %w", err)
	}
	return nil
}

func (d *DB) GetArchiveDatasetStage(ctx context.Context, key ArchiveDatasetKey) (ArchiveDatasetStage, error) {
	var stage ArchiveDatasetStage
	var exhausted sql.NullBool
	var snapshotUpdatedAt sql.NullTime
	err := d.ro.QueryRowContext(ctx, `
		WITH pages AS (
			SELECT * FROM middleman_archive_dataset_pages
			WHERE repo_id = ? AND item_type = ? AND item_number = ?
			  AND dataset = ? AND domain_revision = ?
		)
		SELECT (SELECT snapshot_updated_at FROM pages ORDER BY page_number DESC LIMIT 1),
			COALESCE((SELECT next_cursor FROM pages ORDER BY page_number DESC LIMIT 1), ''),
			(SELECT exhausted FROM pages ORDER BY page_number DESC LIMIT 1),
			COUNT(*), COALESCE(SUM(record_count), 0),
			COALESCE(SUM(length(payload) + length(input_cursor) + length(next_cursor)), 0)
		FROM pages`,
		key.RepoID, key.ItemType, key.ItemNumber, key.Dataset, key.DomainRevision,
	).Scan(&snapshotUpdatedAt, &stage.NextCursor, &exhausted, &stage.PageCount, &stage.Records, &stage.Bytes)
	if err != nil {
		return ArchiveDatasetStage{}, fmt.Errorf("get archive dataset stage: %w", err)
	}
	stage.Exhausted = exhausted.Valid && exhausted.Bool
	if snapshotUpdatedAt.Valid {
		snapshot := snapshotUpdatedAt.Time.UTC()
		stage.SnapshotUpdatedAt = &snapshot
	}
	return stage, nil
}

func (d *DB) CommitArchiveDatasetPage(ctx context.Context, page ArchiveDatasetPage) (ArchiveDatasetStage, error) {
	if page.RepoID <= 0 || page.ItemNumber <= 0 || page.Dataset == "" || page.SnapshotUpdatedAt.IsZero() || page.MaxPages <= 0 || page.MaxRecords <= 0 || page.MaxBytes <= 0 {
		return ArchiveDatasetStage{}, errors.New("commit archive dataset page: invalid page or limits")
	}
	var result ArchiveDatasetStage
	err := d.Tx(ctx, func(tx *sql.Tx) error {
		if err := requireArchiveDatasetSnapshotTx(ctx, tx, page.ArchiveDatasetKey); err != nil {
			return err
		}
		if page.DomainRevision <= 0 {
			return errors.New("commit archive dataset page: domain revision is required")
		}
		if err := requireArchiveDomainSnapshotTx(ctx, tx, page.ArchiveDatasetKey); err != nil {
			return err
		}
		stage, err := getArchiveDatasetStageTx(ctx, tx, page.ArchiveDatasetKey)
		if err != nil {
			return err
		}
		if stage.Exhausted {
			return errors.New("commit archive dataset page: dataset is already exhausted")
		}
		if stage.SnapshotUpdatedAt != nil && !stage.SnapshotUpdatedAt.Equal(page.SnapshotUpdatedAt) {
			return errors.New("commit archive dataset page: staged snapshot changed")
		}
		if stage.NextCursor != page.InputCursor {
			return fmt.Errorf("commit archive dataset page: cursor mismatch: expected %q, got %q", stage.NextCursor, page.InputCursor)
		}
		if !page.Exhausted && page.NextCursor == "" {
			return errors.New("commit archive dataset page: next cursor or explicit exhaustion is required")
		}
		pageBytes := int64(len(page.Payload) + len(page.InputCursor) + len(page.NextCursor))
		limits := []struct {
			name       string
			value, max int64
		}{
			{"page", int64(stage.PageCount + 1), int64(page.MaxPages)},
			{"record", int64(stage.Records + page.RecordCount), int64(page.MaxRecords)},
			{"byte", stage.Bytes + pageBytes, page.MaxBytes},
		}
		for _, limit := range limits {
			if limit.value > limit.max {
				return &ArchiveDatasetLimitError{Dataset: page.Dataset, Limit: limit.name, Value: limit.value, Max: limit.max}
			}
		}
		_, err = tx.ExecContext(ctx, `
			INSERT INTO middleman_archive_dataset_pages (
				repo_id, item_type, item_number, dataset, snapshot_updated_at, domain_revision, page_number,
				input_cursor, next_cursor, exhausted, record_count, payload, created_at
			) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			page.RepoID, page.ItemType, page.ItemNumber, page.Dataset, page.SnapshotUpdatedAt.UTC(), page.DomainRevision, stage.PageCount,
			page.InputCursor, page.NextCursor, page.Exhausted, page.RecordCount, page.Payload, page.Now.UTC())
		if err != nil {
			return fmt.Errorf("commit archive dataset page: %w", err)
		}
		snapshot := page.SnapshotUpdatedAt.UTC()
		result = ArchiveDatasetStage{SnapshotUpdatedAt: &snapshot, NextCursor: page.NextCursor, Exhausted: page.Exhausted,
			PageCount: stage.PageCount + 1, Records: stage.Records + page.RecordCount,
			Bytes: stage.Bytes + pageBytes}
		return nil
	})
	return result, err
}

func getArchiveDatasetStageTx(ctx context.Context, tx *sql.Tx, key ArchiveDatasetKey) (ArchiveDatasetStage, error) {
	var stage ArchiveDatasetStage
	var exhausted sql.NullBool
	var snapshotUpdatedAt sql.NullTime
	err := tx.QueryRowContext(ctx, `
		WITH pages AS (
			SELECT * FROM middleman_archive_dataset_pages
			WHERE repo_id = ? AND item_type = ? AND item_number = ?
			  AND dataset = ? AND domain_revision = ?
		)
		SELECT (SELECT snapshot_updated_at FROM pages ORDER BY page_number DESC LIMIT 1),
			COALESCE((SELECT next_cursor FROM pages ORDER BY page_number DESC LIMIT 1), ''),
			(SELECT exhausted FROM pages ORDER BY page_number DESC LIMIT 1),
			COUNT(*), COALESCE(SUM(record_count), 0),
			COALESCE(SUM(length(payload) + length(input_cursor) + length(next_cursor)), 0)
		FROM pages`,
		key.RepoID, key.ItemType, key.ItemNumber, key.Dataset, key.DomainRevision,
	).Scan(&snapshotUpdatedAt, &stage.NextCursor, &exhausted, &stage.PageCount, &stage.Records, &stage.Bytes)
	if err != nil {
		return stage, err
	}
	stage.Exhausted = exhausted.Valid && exhausted.Bool
	if snapshotUpdatedAt.Valid {
		snapshot := snapshotUpdatedAt.Time.UTC()
		stage.SnapshotUpdatedAt = &snapshot
	}
	return stage, nil
}

func requireArchiveDatasetSnapshotTx(ctx context.Context, tx *sql.Tx, key ArchiveDatasetKey) error {
	column := map[ArchiveDataset]string{
		ArchiveDatasetComments:       "comments_status",
		ArchiveDatasetReviews:        "reviews_status",
		ArchiveDatasetInlineComments: "inline_comments_status",
	}[key.Dataset]
	if column == "" {
		return fmt.Errorf("read archive dataset snapshot: invalid dataset %q", key.Dataset)
	}
	var current time.Time
	var status ArchiveDatasetStatus
	if err := tx.QueryRowContext(ctx, fmt.Sprintf(`
		SELECT provider_updated_at, %s FROM middleman_archive_items
		WHERE repo_id = ? AND item_type = ? AND item_number = ?`, column),
		key.RepoID, key.ItemType, key.ItemNumber).Scan(&current, &status); err != nil {
		return fmt.Errorf("read archive dataset snapshot: %w", err)
	}
	if !current.UTC().Equal(key.SnapshotUpdatedAt.UTC()) {
		return fmt.Errorf("archive dataset snapshot changed from %s to %s",
			key.SnapshotUpdatedAt.UTC().Format(time.RFC3339Nano), current.UTC().Format(time.RFC3339Nano))
	}
	if status == ArchiveDatasetStatusComplete {
		return fmt.Errorf("archive dataset %s is already complete", key.Dataset)
	}
	return nil
}

func (d *DB) LoadArchiveDatasetPages(ctx context.Context, key ArchiveDatasetKey) ([][]byte, error) {
	stage, err := d.GetArchiveDatasetStage(ctx, key)
	if err != nil {
		return nil, err
	}
	if !stage.Exhausted {
		return nil, errors.New("load archive dataset pages: dataset is not exhausted")
	}
	rows, err := d.ro.QueryContext(ctx, `
		SELECT payload FROM middleman_archive_dataset_pages
		WHERE repo_id = ? AND item_type = ? AND item_number = ?
		  AND dataset = ? AND domain_revision = ?
		ORDER BY page_number`,
		key.RepoID, key.ItemType, key.ItemNumber, key.Dataset, key.DomainRevision)
	if err != nil {
		return nil, fmt.Errorf("load archive dataset pages: %w", err)
	}
	defer rows.Close()
	var payloads [][]byte
	for rows.Next() {
		var payload []byte
		if err := rows.Scan(&payload); err != nil {
			return nil, err
		}
		payloads = append(payloads, payload)
	}
	return payloads, rows.Err()
}

func (d *DB) PublishArchiveIssueComments(ctx context.Context, key ArchiveDatasetKey, issueID int64, events []IssueEvent) error {
	return d.Tx(ctx, func(tx *sql.Tx) error {
		if err := requireArchiveDatasetSnapshotTx(ctx, tx, key); err != nil {
			return err
		}
		if err := requireArchiveDomainSnapshotTx(ctx, tx, key); err != nil {
			return err
		}
		stage, err := getArchiveDatasetStageTx(ctx, tx, key)
		if err != nil {
			return err
		}
		if err := requireArchiveDatasetStageReady(stage, key, "publish archive issue comments"); err != nil {
			return err
		}
		if err := commitLiveDatasetTx(ctx, tx, DatasetPageCommit{
			Parent:           DomainParentRef{ItemType: ArchiveItemTypeIssue, ID: issueID},
			ExpectedRevision: key.DomainRevision,
			Dataset:          ArchiveDatasetComments,
			Rows:             DatasetRows{IssueComments: events},
			Final:            true,
		}); err != nil {
			return err
		}
		return completeArchiveDatasetTx(ctx, tx, key)
	})
}

func (d *DB) PublishArchiveMREvents(ctx context.Context, key ArchiveDatasetKey, mrID int64, eventType string, events []MREvent) error {
	return d.Tx(ctx, func(tx *sql.Tx) error {
		if err := requireArchiveDatasetSnapshotTx(ctx, tx, key); err != nil {
			return err
		}
		if err := requireArchiveDomainSnapshotTx(ctx, tx, key); err != nil {
			return err
		}
		stage, err := getArchiveDatasetStageTx(ctx, tx, key)
		if err != nil {
			return err
		}
		if err := requireArchiveDatasetStageReady(stage, key, "publish archive merge-request events"); err != nil {
			return err
		}
		commit := DatasetPageCommit{
			Parent:           DomainParentRef{ItemType: ArchiveItemTypeMergeRequest, ID: mrID},
			ExpectedRevision: key.DomainRevision,
			Final:            true,
		}
		switch eventType {
		case "issue_comment":
			commit.Dataset = ArchiveDatasetComments
			commit.Rows = DatasetRows{MRComments: events}
		case "review":
			commit.Dataset = ArchiveDatasetReviews
			commit.Rows = DatasetRows{Reviews: events}
		default:
			return fmt.Errorf("publish archive merge-request events: invalid event type %q", eventType)
		}
		if err := commitLiveDatasetTx(ctx, tx, commit); err != nil {
			return err
		}
		return completeArchiveDatasetTx(ctx, tx, key)
	})
}

func (d *DB) PublishArchiveReviewThreads(ctx context.Context, key ArchiveDatasetKey, mrID int64, threads []MRReviewThread, events []MREvent) error {
	return d.Tx(ctx, func(tx *sql.Tx) error {
		if err := requireArchiveDatasetSnapshotTx(ctx, tx, key); err != nil {
			return err
		}
		if err := requireArchiveDomainSnapshotTx(ctx, tx, key); err != nil {
			return err
		}
		stage, err := getArchiveDatasetStageTx(ctx, tx, key)
		if err != nil {
			return err
		}
		if err := requireArchiveDatasetStageReady(stage, key, "publish archive review threads"); err != nil {
			return err
		}
		if err := commitLiveDatasetTx(ctx, tx, DatasetPageCommit{
			Parent:           DomainParentRef{ItemType: ArchiveItemTypeMergeRequest, ID: mrID},
			ExpectedRevision: key.DomainRevision,
			Dataset:          ArchiveDatasetInlineComments,
			Rows:             DatasetRows{ReviewThreads: threads, ThreadEvents: events},
			Final:            true,
		}); err != nil {
			return err
		}
		return completeArchiveDatasetTx(ctx, tx, key)
	})
}

func requireArchiveDomainSnapshotTx(ctx context.Context, tx *sql.Tx, key ArchiveDatasetKey) error {
	if key.DomainRevision <= 0 {
		return errors.New("archive domain revision is required")
	}
	var current time.Time
	var currentRevision int64
	var err error
	switch key.ItemType {
	case ArchiveItemTypeIssue:
		err = tx.QueryRowContext(ctx, `
			SELECT updated_at, snapshot_revision FROM middleman_issues
			WHERE repo_id = ? AND number = ?`, key.RepoID, key.ItemNumber,
		).Scan(&current, &currentRevision)
	case ArchiveItemTypeMergeRequest:
		err = tx.QueryRowContext(ctx, `
			SELECT updated_at, snapshot_revision FROM middleman_merge_requests
			WHERE repo_id = ? AND number = ?`, key.RepoID, key.ItemNumber,
		).Scan(&current, &currentRevision)
	default:
		return fmt.Errorf("read archive domain snapshot: invalid item type %q", key.ItemType)
	}
	if err != nil {
		return fmt.Errorf("read archive domain snapshot: %w", err)
	}
	if !current.UTC().Equal(key.SnapshotUpdatedAt.UTC()) {
		return fmt.Errorf("archive domain parent snapshot changed from %s to %s",
			key.SnapshotUpdatedAt.UTC().Format(time.RFC3339Nano), current.UTC().Format(time.RFC3339Nano))
	}
	if currentRevision != key.DomainRevision {
		return fmt.Errorf("archive domain parent revision changed from %d to %d",
			key.DomainRevision, currentRevision)
	}
	return nil
}

func requireArchiveDatasetStageReady(stage ArchiveDatasetStage, key ArchiveDatasetKey, operation string) error {
	if !stage.Exhausted {
		return fmt.Errorf("%s: dataset is not exhausted", operation)
	}
	if stage.SnapshotUpdatedAt == nil || !stage.SnapshotUpdatedAt.Equal(key.SnapshotUpdatedAt) {
		return fmt.Errorf("%s: staged snapshot changed", operation)
	}
	return nil
}

func completeArchiveDatasetTx(ctx context.Context, tx *sql.Tx, key ArchiveDatasetKey) error {
	column := map[ArchiveDataset]string{ArchiveDatasetComments: "comments_status", ArchiveDatasetReviews: "reviews_status", ArchiveDatasetInlineComments: "inline_comments_status"}[key.Dataset]
	if column == "" {
		return fmt.Errorf("complete archive dataset: invalid dataset %q", key.Dataset)
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM middleman_archive_dataset_pages WHERE repo_id = ? AND item_type = ? AND item_number = ? AND dataset = ?`, key.RepoID, key.ItemType, key.ItemNumber, key.Dataset); err != nil {
		return err
	}
	_, err := tx.ExecContext(ctx, fmt.Sprintf(`UPDATE middleman_archive_items SET %s = 'complete' WHERE repo_id = ? AND item_type = ? AND item_number = ?`, column), key.RepoID, key.ItemType, key.ItemNumber)
	return err
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
		UPDATE middleman_archive_items
		SET lifecycle_state = 'active',
			comments_status = CASE
				WHEN comments_status IN ('unsupported', 'not_applicable') THEN comments_status
				ELSE 'pending'
			END,
			reviews_status = CASE
				WHEN reviews_status IN ('unsupported', 'not_applicable') THEN reviews_status
				ELSE 'pending'
			END,
			inline_comments_status = CASE
				WHEN inline_comments_status IN ('unsupported', 'not_applicable') THEN inline_comments_status
				ELSE 'pending'
			END,
			attempt_count = 0, next_retry_at = NULL,
			last_error_code = NULL, last_error_detail = NULL, hydrated_at = NULL
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
		UPDATE middleman_archive_repos
		SET last_error_code = NULL, last_error_detail = NULL, next_retry_at = NULL,
			updated_at = MAX(updated_at, ?)
		WHERE repo_id = ?`, now.UTC(), repoID)
	if err != nil {
		return fmt.Errorf("clear archive repository failure: %w", err)
	}
	return nil
}

func upsertArchiveIssueWorkTx(
	ctx context.Context,
	tx *sql.Tx,
	repoID int64,
	item ArchiveInventoryIssue,
	refreshReason ArchiveRefreshReason,
) error {
	issue := item.Snapshot.Issue
	if err := clearStagedArchiveDatasetsForNewSnapshotTx(ctx, tx, repoID, ArchiveItemTypeIssue, issue.Number, issue.UpdatedAt, refreshReason); err != nil {
		return err
	}
	providerID := archiveProviderItemID(item.ProviderItemID, issue.PlatformExternalID, issue.PlatformID)
	_, err := tx.ExecContext(ctx, `
		INSERT INTO middleman_archive_items (
			repo_id, item_type, item_number, provider_item_id,
			provider_created_at, provider_updated_at, hydration_snapshot_updated_at,
			lifecycle_state, refresh_reason,
			comments_status, reviews_status, inline_comments_status
		) VALUES (?, 'issue', ?, ?, ?, ?, ?, 'active', ?, ?, 'not_applicable', 'not_applicable')
		ON CONFLICT(repo_id, item_type, item_number) DO UPDATE SET
			provider_item_id = CASE WHEN excluded.provider_updated_at >= provider_updated_at THEN excluded.provider_item_id ELSE provider_item_id END,
			provider_created_at = CASE WHEN excluded.provider_updated_at >= provider_updated_at THEN excluded.provider_created_at ELSE provider_created_at END,
			provider_updated_at = MAX(provider_updated_at, excluded.provider_updated_at),
			hydration_snapshot_updated_at = CASE WHEN excluded.provider_updated_at > provider_updated_at OR (excluded.provider_updated_at = provider_updated_at AND excluded.refresh_reason = 'prompt') THEN excluded.provider_updated_at ELSE hydration_snapshot_updated_at END,
			lifecycle_state = CASE WHEN excluded.provider_updated_at > provider_updated_at THEN 'active' ELSE lifecycle_state END,
			refresh_reason = CASE WHEN excluded.provider_updated_at > provider_updated_at OR (excluded.provider_updated_at = provider_updated_at AND excluded.refresh_reason = 'prompt') THEN excluded.refresh_reason ELSE refresh_reason END,
			comments_status = CASE WHEN excluded.provider_updated_at > provider_updated_at OR (excluded.provider_updated_at = provider_updated_at AND excluded.refresh_reason = 'prompt') THEN excluded.comments_status ELSE comments_status END,
			next_retry_at = CASE WHEN excluded.provider_updated_at > provider_updated_at OR (excluded.provider_updated_at = provider_updated_at AND excluded.refresh_reason = 'prompt') THEN NULL ELSE next_retry_at END,
			last_error_code = CASE WHEN excluded.provider_updated_at > provider_updated_at OR (excluded.provider_updated_at = provider_updated_at AND excluded.refresh_reason = 'prompt') THEN NULL ELSE last_error_code END,
			last_error_detail = CASE WHEN excluded.provider_updated_at > provider_updated_at OR (excluded.provider_updated_at = provider_updated_at AND excluded.refresh_reason = 'prompt') THEN NULL ELSE last_error_detail END`,
		repoID, issue.Number, providerID, issue.CreatedAt, issue.UpdatedAt, issue.UpdatedAt,
		refreshReason, item.CommentsStatus,
	)
	if err != nil {
		return fmt.Errorf("upsert archive issue inventory work: %w", err)
	}
	return nil
}

func upsertArchiveMergeRequestWorkTx(
	ctx context.Context,
	tx *sql.Tx,
	repoID int64,
	item ArchiveInventoryMergeRequest,
	refreshReason ArchiveRefreshReason,
) error {
	mr := item.Snapshot.MergeRequest
	if err := clearStagedArchiveDatasetsForNewSnapshotTx(ctx, tx, repoID, ArchiveItemTypeMergeRequest, mr.Number, mr.UpdatedAt, refreshReason); err != nil {
		return err
	}
	providerID := archiveProviderItemID(item.ProviderItemID, mr.PlatformExternalID, mr.PlatformID)
	_, err := tx.ExecContext(ctx, `
		INSERT INTO middleman_archive_items (
			repo_id, item_type, item_number, provider_item_id,
			provider_created_at, provider_updated_at, hydration_snapshot_updated_at,
			lifecycle_state, refresh_reason,
			comments_status, reviews_status, inline_comments_status
		) VALUES (?, 'merge_request', ?, ?, ?, ?, ?, 'active', ?, ?, ?, ?)
		ON CONFLICT(repo_id, item_type, item_number) DO UPDATE SET
			provider_item_id = CASE WHEN excluded.provider_updated_at >= provider_updated_at THEN excluded.provider_item_id ELSE provider_item_id END,
			provider_created_at = CASE WHEN excluded.provider_updated_at >= provider_updated_at THEN excluded.provider_created_at ELSE provider_created_at END,
			provider_updated_at = MAX(provider_updated_at, excluded.provider_updated_at),
			hydration_snapshot_updated_at = CASE WHEN excluded.provider_updated_at > provider_updated_at OR (excluded.provider_updated_at = provider_updated_at AND excluded.refresh_reason = 'prompt') THEN excluded.provider_updated_at ELSE hydration_snapshot_updated_at END,
			lifecycle_state = CASE WHEN excluded.provider_updated_at > provider_updated_at THEN 'active' ELSE lifecycle_state END,
			refresh_reason = CASE WHEN excluded.provider_updated_at > provider_updated_at OR (excluded.provider_updated_at = provider_updated_at AND excluded.refresh_reason = 'prompt') THEN excluded.refresh_reason ELSE refresh_reason END,
			comments_status = CASE WHEN excluded.provider_updated_at > provider_updated_at OR (excluded.provider_updated_at = provider_updated_at AND excluded.refresh_reason = 'prompt') THEN excluded.comments_status ELSE comments_status END,
			reviews_status = CASE WHEN excluded.provider_updated_at > provider_updated_at OR (excluded.provider_updated_at = provider_updated_at AND excluded.refresh_reason = 'prompt') THEN excluded.reviews_status ELSE reviews_status END,
			inline_comments_status = CASE WHEN excluded.provider_updated_at > provider_updated_at OR (excluded.provider_updated_at = provider_updated_at AND excluded.refresh_reason = 'prompt') THEN excluded.inline_comments_status ELSE inline_comments_status END,
			next_retry_at = CASE WHEN excluded.provider_updated_at > provider_updated_at OR (excluded.provider_updated_at = provider_updated_at AND excluded.refresh_reason = 'prompt') THEN NULL ELSE next_retry_at END,
			last_error_code = CASE WHEN excluded.provider_updated_at > provider_updated_at OR (excluded.provider_updated_at = provider_updated_at AND excluded.refresh_reason = 'prompt') THEN NULL ELSE last_error_code END,
			last_error_detail = CASE WHEN excluded.provider_updated_at > provider_updated_at OR (excluded.provider_updated_at = provider_updated_at AND excluded.refresh_reason = 'prompt') THEN NULL ELSE last_error_detail END`,
		repoID, mr.Number, providerID, mr.CreatedAt, mr.UpdatedAt, mr.UpdatedAt, refreshReason,
		item.CommentsStatus, item.ReviewsStatus, item.InlineCommentsStatus,
	)
	if err != nil {
		return fmt.Errorf("upsert archive merge request inventory work: %w", err)
	}
	return nil
}

func clearStagedArchiveDatasetsForNewSnapshotTx(
	ctx context.Context,
	tx *sql.Tx,
	repoID int64,
	itemType ArchiveItemType,
	itemNumber int,
	updatedAt time.Time,
	refreshReason ArchiveRefreshReason,
) error {
	_, err := tx.ExecContext(ctx, `
		DELETE FROM middleman_archive_dataset_pages
		WHERE repo_id = ? AND item_type = ? AND item_number = ?
		  AND EXISTS (
			SELECT 1 FROM middleman_archive_items
			WHERE repo_id = ? AND item_type = ? AND item_number = ?
			  AND (provider_updated_at < ? OR (provider_updated_at = ? AND ? = 'prompt'))
		  )`, repoID, itemType, itemNumber, repoID, itemType, itemNumber,
		updatedAt.UTC(), updatedAt.UTC(), refreshReason)
	if err != nil {
		return fmt.Errorf("clear staged archive datasets for newer snapshot: %w", err)
	}
	return nil
}

func archiveRepositoryActiveTx(ctx context.Context, tx *sql.Tx, repoID int64) (bool, error) {
	var operator ArchiveOperatorState
	err := tx.QueryRowContext(ctx,
		`SELECT operator_state FROM middleman_archive_repos WHERE repo_id = ?`, repoID,
	).Scan(&operator)
	if errors.Is(err, sql.ErrNoRows) {
		return false, &ArchiveRepoStateNotFoundError{RepoIDs: []int64{repoID}}
	}
	if err != nil {
		return false, fmt.Errorf("read archive repository operator state: %w", err)
	}
	return operator == ArchiveOperatorStateActive, nil
}

func archiveProviderItemID(explicit, external string, numeric int64) string {
	if explicit != "" {
		return explicit
	}
	if external != "" {
		return external
	}
	return fmt.Sprintf("%d", numeric)
}
