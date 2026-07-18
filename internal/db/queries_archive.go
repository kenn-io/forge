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
					initial_started_at, initial_completed_at,
					maintenance_watermark, maintenance_succeeded_at,
					comments_coverage, reviews_coverage, inline_comments_coverage,
					last_error_code, last_error_detail, next_retry_at,
					created_at, updated_at
				) VALUES (
					?, 'discovery', 'active',
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
				SELECT repo_id FROM middleman_archive_repos WHERE collection_mode = 'discovery'
			  )
			  AND EXISTS (
				SELECT 1 FROM middleman_archive_items ai
				WHERE ai.repo_id = middleman_archive_dataset_progress.repo_id
				  AND ai.item_type = middleman_archive_dataset_progress.item_type
				  AND ai.item_number = middleman_archive_dataset_progress.item_number
				  AND ai.lifecycle_state = 'active'
			  )`, nextEvenScanGenerationSQL, sqlPlaceholders(len(repoIDs))), promoteArgs...); err != nil {
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
			SELECT scan_generation, status FROM middleman_archive_repo_scans
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
		UPDATE middleman_archive_items
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
		if err := advanceArchiveScanTx(ctx, tx, commit, kind, outcome.newPageCount, commit.Now); err != nil {
			return err
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
		ctx, tx, issue, item.Snapshot.Labels, reopenDatasetsAtomic,
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
	if err := upsertArchiveItemWorkTx(
		ctx, tx, repoID, ArchiveItemTypeIssue, issue.Number,
		archiveProviderItemID(item.ProviderItemID, issue.PlatformExternalID, issue.PlatformID),
		issue.CreatedAt, issue.UpdatedAt, refreshReason,
	); err != nil {
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
		ctx, tx, mr, item.Snapshot.Labels, detail, reopenDatasetsAtomic,
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
	if err := upsertArchiveItemWorkTx(
		ctx, tx, repoID, ArchiveItemTypeMergeRequest, mr.Number,
		archiveProviderItemID(item.ProviderItemID, mr.PlatformExternalID, mr.PlatformID),
		mr.CreatedAt, mr.UpdatedAt, refreshReason,
	); err != nil {
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
// parent revisions is a separate reopen step. Rows seed at generation 2: the
// archive namespace is even (nextEvenScanGenerationSQL) so a seeded
// generation can never equal an odd live ingest stamp already present on the
// parent's rows.
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
				repo_id, item_type, item_number, dataset, scan_generation, status, updated_at
			) VALUES (?, ?, ?, ?, 2, ?, ?)
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
	if err := reopenDatasetsForParentTx(ctx, tx, parent, repoID, itemNumber, revision, revision); err != nil {
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
		SET scan_generation = `+nextEvenScanGenerationSQL+`,
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
		SELECT platform_head_sha, platform_base_sha, head_repo_clone_url,
			additions, deletions, comment_count, review_decision, ci_status,
			ci_checks_json, detail_fetched_at, ci_had_pending, mergeable_state
		FROM middleman_merge_requests
		WHERE repo_id = ? AND number = ?`, mr.RepoID, mr.Number,
	).Scan(&existing.PlatformHeadSHA, &existing.PlatformBaseSHA, &existing.HeadRepoCloneURL,
		&existing.Additions, &existing.Deletions, &existing.CommentCount,
		&existing.ReviewDecision, &existing.CIStatus, &existing.CIChecksJSON,
		&existing.DetailFetchedAt, &existing.CIHadPending, &existing.MergeableState)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("preserve merge request detail before archive upsert: %w", err)
	}
	if mr.HeadRepoCloneURLUnknown || (preserveProviderDetail && mr.HeadRepoCloneURL == "") {
		mr.HeadRepoCloneURL = existing.HeadRepoCloneURL
		mr.HeadRepoCloneURLUnknown = false
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
		var err error
		id, revision, accepted, err = commitIssueParentSnapshotTx(ctx, tx, snapshot, snapshot.Labels, reopenDatasetsAtomic)
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
			ctx, tx, snapshot.RepoID, ArchiveItemTypeIssue, snapshot.Number, winningUpdatedAt,
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
		var err error
		id, revision, accepted, err = commitMergeRequestParentSnapshotTx(
			ctx, tx, snapshot, snapshot.Labels, mergeRequestDetailPreserveDerived, reopenDatasetsAtomic,
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
			ctx, tx, snapshot.RepoID, ArchiveItemTypeMergeRequest, snapshot.Number, winningUpdatedAt,
		)
	})
	return id, revision, accepted, err
}

// reconcileArchiveItemSnapshotTx keeps the archive work row bound to the
// winning provider timestamp after a parent observation. Dataset-level reopen
// and retry state live entirely in middleman_archive_dataset_progress.
func reconcileArchiveItemSnapshotTx(
	ctx context.Context,
	tx *sql.Tx,
	repoID int64,
	itemType ArchiveItemType,
	itemNumber int,
	winningUpdatedAt time.Time,
) error {
	if _, err := tx.ExecContext(ctx, `
		UPDATE middleman_archive_items
		SET provider_updated_at = MAX(provider_updated_at, ?)
		WHERE repo_id = ? AND item_type = ? AND item_number = ?`,
		winningUpdatedAt.UTC(), repoID, itemType, itemNumber,
	); err != nil {
		return fmt.Errorf("reconcile archive item snapshot: %w", err)
	}
	return nil
}

// completeArchiveInitialIfReadyTx marks the initial archive complete once both
// inventory scans finished and no active item carries outstanding dataset
// work. The only statuses that count as completed exceptions are 'complete',
// 'unsupported' (a declared capability gap), and 'terminal' (the item's
// lookup settled the outcome). 'blocked' is outstanding: it awaits an
// explicit operator reset, and completing around it would start maintenance
// over an incomplete initial archive.
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
		UPDATE middleman_archive_items
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
		UPDATE middleman_archive_repos
		SET last_error_code = NULL, last_error_detail = NULL, next_retry_at = NULL,
			updated_at = MAX(updated_at, ?)
		WHERE repo_id = ?`, now.UTC(), repoID)
	if err != nil {
		return fmt.Errorf("clear archive repository failure: %w", err)
	}
	return nil
}

// upsertArchiveItemWorkTx records the item-level work row for one parent
// observation. Dataset state lives in middleman_archive_dataset_progress; the
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
		INSERT INTO middleman_archive_items (
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
