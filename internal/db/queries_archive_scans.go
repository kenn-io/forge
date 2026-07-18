package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// archiveScanKinds enumerates every repository-level scan row a repository
// carries. All four are seeded together so scan reads never race row creation.
var archiveScanKinds = []ArchiveScanKind{
	ArchiveScanIssueInventory,
	ArchiveScanMergeRequestInventory,
	ArchiveScanMaintenanceIssues,
	ArchiveScanMaintenanceMergeRequests,
}

// archiveScanKindFor maps an inventory commit to the scan row it advances.
func archiveScanKindFor(itemType ArchiveItemType, reason ArchiveRefreshReason) (ArchiveScanKind, error) {
	switch {
	case itemType == ArchiveItemTypeIssue && reason == ArchiveRefreshReasonInitial:
		return ArchiveScanIssueInventory, nil
	case itemType == ArchiveItemTypeMergeRequest && reason == ArchiveRefreshReasonInitial:
		return ArchiveScanMergeRequestInventory, nil
	case itemType == ArchiveItemTypeIssue && reason == ArchiveRefreshReasonPrompt:
		return ArchiveScanMaintenanceIssues, nil
	case itemType == ArchiveItemTypeMergeRequest && reason == ArchiveRefreshReasonPrompt:
		return ArchiveScanMaintenanceMergeRequests, nil
	default:
		return "", fmt.Errorf("archive scan: no scan for item type %q reason %q", itemType, reason)
	}
}

// ensureArchiveRepoScansTx seeds the four durable scan rows of one repository.
// Existing rows are never modified.
func ensureArchiveRepoScansTx(ctx context.Context, tx *sql.Tx, repoID int64, now time.Time) error {
	nowText := formatDatasetProgressTime(now)
	for _, kind := range archiveScanKinds {
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO middleman_archive_repo_scans (repo_id, scan, updated_at)
			VALUES (?, ?, ?)
			ON CONFLICT(repo_id, scan) DO NOTHING`,
			repoID, kind, nowText); err != nil {
			return fmt.Errorf("seed archive scan %s for repo %d: %w", kind, repoID, err)
		}
	}
	return nil
}

func archiveScanScope(repoID int64, kind ArchiveScanKind) string {
	return fmt.Sprintf("repo %d scan %s", repoID, kind)
}

type archiveScanAdvanceOutcome struct {
	typedErr error
	replay   bool
	// refreshOnly marks an observation against an already-complete scan:
	// parent snapshots still apply (prompt refreshes and boundary
	// re-observations), but the finished traversal's cursor never moves.
	refreshOnly  bool
	newPageCount int
}

// checkArchiveScanAdvanceTx applies the repository-scan cursor and generation
// compare-and-swap, mirroring the dataset-progress CAS: replayed pages are
// idempotent no-ops, stale generations or cursors are typed rejections, and
// echoed cursors or page-bound exhaustion durably block the scan.
func checkArchiveScanAdvanceTx(
	ctx context.Context,
	tx *sql.Tx,
	commit ArchiveInventoryCommit,
	kind ArchiveScanKind,
	now time.Time,
) (archiveScanAdvanceOutcome, error) {
	var generation int64
	var nextCursor, lastInputCursor, lastErrorCode sql.NullString
	var pageCount int
	var status ArchiveScanStatus
	err := tx.QueryRowContext(ctx, `
		SELECT scan_generation, next_cursor, last_input_cursor, page_count, status, last_error_code
		FROM middleman_archive_repo_scans
		WHERE repo_id = ? AND scan = ?`, commit.RepoID, kind,
	).Scan(&generation, &nextCursor, &lastInputCursor, &pageCount, &status, &lastErrorCode)
	if errors.Is(err, sql.ErrNoRows) {
		return archiveScanAdvanceOutcome{typedErr: &StaleArchiveScanError{
			RepoID: commit.RepoID, Scan: kind, ExpectedGeneration: commit.ScanGeneration,
		}}, nil
	}
	if err != nil {
		return archiveScanAdvanceOutcome{}, fmt.Errorf("read archive scan: %w", err)
	}
	if status == ArchiveScanBlocked {
		reason := "blocked"
		if lastErrorCode.Valid && lastErrorCode.String != "" {
			reason = lastErrorCode.String
		}
		return archiveScanAdvanceOutcome{typedErr: &ScanBlockedError{
			Scope: archiveScanScope(commit.RepoID, kind), Reason: reason,
		}}, nil
	}
	if status == ArchiveScanComplete {
		return archiveScanAdvanceOutcome{refreshOnly: true}, nil
	}
	if generation != commit.ScanGeneration {
		return archiveScanAdvanceOutcome{typedErr: &StaleArchiveScanError{
			RepoID: commit.RepoID, Scan: kind,
			ExpectedGeneration: commit.ScanGeneration, GotGeneration: generation,
		}}, nil
	}
	if lastInputCursor.Valid && commit.InputCursor == lastInputCursor.String {
		// The already-committed page: idempotent replay writes nothing.
		return archiveScanAdvanceOutcome{replay: true}, nil
	}
	if commit.InputCursor != nextCursor.String {
		return archiveScanAdvanceOutcome{typedErr: &StaleArchiveScanError{
			RepoID: commit.RepoID, Scan: kind,
			ExpectedGeneration: commit.ScanGeneration, GotGeneration: generation,
			ExpectedCursor: nextCursor.String, GotCursor: commit.InputCursor,
		}}, nil
	}
	newPageCount := pageCount + 1
	if newPageCount > maxScanPages {
		if err := blockArchiveScanTx(
			ctx, tx, commit.RepoID, kind, datasetErrorCodePageBound,
			fmt.Sprintf("scan exceeded %d pages in one generation", maxScanPages), now,
		); err != nil {
			return archiveScanAdvanceOutcome{}, err
		}
		return archiveScanAdvanceOutcome{typedErr: &ScanBlockedError{
			Scope: archiveScanScope(commit.RepoID, kind), Reason: datasetErrorCodePageBound,
		}}, nil
	}
	if !commit.Exhausted && commit.NextCursor == commit.InputCursor {
		// The provider echoed the input cursor without exhaustion: a directly
		// detected in-window cycle.
		if err := blockArchiveScanTx(
			ctx, tx, commit.RepoID, kind, datasetErrorCodeInvalidCursor,
			"provider echoed the input cursor without exhaustion", now,
		); err != nil {
			return archiveScanAdvanceOutcome{}, err
		}
		return archiveScanAdvanceOutcome{typedErr: &ScanBlockedError{
			Scope: archiveScanScope(commit.RepoID, kind), Reason: datasetErrorCodeInvalidCursor,
		}}, nil
	}
	return archiveScanAdvanceOutcome{newPageCount: newPageCount}, nil
}

// advanceArchiveScanTx commits one page's cursor movement on the scan row.
// Maintenance scans advance only while a durable prompt boundary is active.
func advanceArchiveScanTx(
	ctx context.Context,
	tx *sql.Tx,
	commit ArchiveInventoryCommit,
	kind ArchiveScanKind,
	newPageCount int,
	now time.Time,
) error {
	status := ArchiveScanRunning
	var cursor, lastInput any
	if commit.Exhausted {
		status = ArchiveScanComplete
	} else {
		cursor = commit.NextCursor
		lastInput = commit.InputCursor
	}
	query := `
		UPDATE middleman_archive_repo_scans
		SET next_cursor = ?, last_input_cursor = ?, page_count = ?, status = ?,
			last_error_code = NULL, last_error_detail = NULL, updated_at = ?
		WHERE repo_id = ? AND scan = ?`
	args := []any{
		cursor, lastInput, newPageCount, status,
		formatDatasetProgressTime(now), commit.RepoID, kind,
	}
	if kind == ArchiveScanMaintenanceIssues || kind == ArchiveScanMaintenanceMergeRequests {
		query += `
		  AND EXISTS (
			SELECT 1 FROM middleman_archive_repos
			WHERE repo_id = ? AND collection_mode = 'full' AND operator_state = 'active'
			  AND prompt_scan_started_at IS NOT NULL
		  )`
		args = append(args, commit.RepoID)
	}
	result, err := tx.ExecContext(ctx, query, args...)
	if err != nil {
		return fmt.Errorf("advance archive scan: %w", err)
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("advance archive scan rows affected: %w", err)
	}
	if changed != 1 {
		return fmt.Errorf("advance archive scan: no advanceable scan row for %s", archiveScanScope(commit.RepoID, kind))
	}
	return nil
}

func blockArchiveScanTx(
	ctx context.Context,
	tx *sql.Tx,
	repoID int64,
	kind ArchiveScanKind,
	code string,
	detail string,
	now time.Time,
) error {
	result, err := tx.ExecContext(ctx, `
		UPDATE middleman_archive_repo_scans
		SET status = 'blocked', last_error_code = ?, last_error_detail = ?, updated_at = ?
		WHERE repo_id = ? AND scan = ?`,
		code, sanitizeArchiveErrorDetail(detail), formatDatasetProgressTime(now), repoID, kind)
	if err != nil {
		return fmt.Errorf("block archive scan: %w", err)
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("block archive scan rows affected: %w", err)
	}
	if changed == 0 {
		return fmt.Errorf("block archive scan: no scan row for %s", archiveScanScope(repoID, kind))
	}
	return nil
}

// BlockArchiveRepoScan durably stops one repository-level scan: progress is
// retained for diagnostics, no further provider requests are spent
// automatically, and recovery requires an explicit reset.
func (d *DB) BlockArchiveRepoScan(
	ctx context.Context,
	repoID int64,
	kind ArchiveScanKind,
	code string,
	detail string,
) error {
	return d.Tx(ctx, func(tx *sql.Tx) error {
		return blockArchiveScanTx(ctx, tx, repoID, kind, code, detail, time.Now())
	})
}

// resetArchiveScanTx starts a fresh generation for one scan: cursor cleared,
// page count zeroed, status pending.
func resetArchiveScanTx(
	ctx context.Context,
	tx *sql.Tx,
	repoID int64,
	kind ArchiveScanKind,
	now time.Time,
) error {
	result, err := tx.ExecContext(ctx, `
		UPDATE middleman_archive_repo_scans
		SET scan_generation = scan_generation + 1,
			next_cursor = NULL, last_input_cursor = NULL, page_count = 0,
			status = 'pending', last_error_code = NULL, last_error_detail = NULL,
			updated_at = ?
		WHERE repo_id = ? AND scan = ?`,
		formatDatasetProgressTime(now), repoID, kind)
	if err != nil {
		return fmt.Errorf("reset archive scan: %w", err)
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("reset archive scan rows affected: %w", err)
	}
	if changed == 0 {
		return fmt.Errorf("reset archive scan: no scan row for %s", archiveScanScope(repoID, kind))
	}
	return nil
}

// ResetArchiveRepoScan starts a fresh scan generation for one repository-level
// scan. It is the restart mode of blocked-scan recovery: only the affected
// scan restarts, domain content and unrelated cursors are untouched.
func (d *DB) ResetArchiveRepoScan(ctx context.Context, repoID int64, kind ArchiveScanKind) error {
	return d.Tx(ctx, func(tx *sql.Tx) error {
		return resetArchiveScanTx(ctx, tx, repoID, kind, time.Now())
	})
}

// loadArchiveScanStates merges scan rows into the supplied states, keyed by
// repository ID. Missing rows read as an untouched pending generation one.
func loadArchiveScanStates(
	ctx context.Context,
	queryer archiveQueryer,
	states []ArchiveRepoState,
) error {
	if len(states) == 0 {
		return nil
	}
	byID := make(map[int64]*ArchiveRepoState, len(states))
	repoIDs := make([]int64, 0, len(states))
	for i := range states {
		defaultScan := ArchiveScanState{Generation: 1, Status: ArchiveScanPending}
		states[i].IssueInventory = defaultScan
		states[i].MergeRequestInventory = defaultScan
		states[i].MaintenanceIssues = defaultScan
		states[i].MaintenanceMergeRequests = defaultScan
		byID[states[i].RepoID] = &states[i]
		repoIDs = append(repoIDs, states[i].RepoID)
	}
	rows, err := queryer.QueryContext(ctx, fmt.Sprintf(`
		SELECT repo_id, scan, scan_generation, next_cursor, last_input_cursor,
			page_count, status, last_error_code, last_error_detail
		FROM middleman_archive_repo_scans
		WHERE repo_id IN (%s)`, sqlPlaceholders(len(repoIDs))),
		archiveRepoIDArgs(repoIDs)...)
	if err != nil {
		return fmt.Errorf("list archive scan states: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var repoID int64
		var kind ArchiveScanKind
		var scan ArchiveScanState
		var nextCursor, lastInput, errorCode, errorDetail sql.NullString
		if err := rows.Scan(
			&repoID, &kind, &scan.Generation, &nextCursor, &lastInput,
			&scan.PageCount, &scan.Status, &errorCode, &errorDetail,
		); err != nil {
			return fmt.Errorf("scan archive scan state: %w", err)
		}
		if nextCursor.Valid {
			scan.NextCursor = &nextCursor.String
		}
		if lastInput.Valid {
			scan.LastInputCursor = &lastInput.String
		}
		if errorCode.Valid {
			scan.LastErrorCode = &errorCode.String
		}
		if errorDetail.Valid {
			scan.LastErrorDetail = &errorDetail.String
		}
		state, ok := byID[repoID]
		if !ok {
			continue
		}
		switch kind {
		case ArchiveScanIssueInventory:
			state.IssueInventory = scan
		case ArchiveScanMergeRequestInventory:
			state.MergeRequestInventory = scan
		case ArchiveScanMaintenanceIssues:
			state.MaintenanceIssues = scan
		case ArchiveScanMaintenanceMergeRequests:
			state.MaintenanceMergeRequests = scan
		}
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("list archive scan states: %w", err)
	}
	return nil
}
