package dbupgrade

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	migratedb "github.com/golang-migrate/migrate/v4/database"
)

const LegacyTimestampRepairSchemaVersion = 10

func NeedsLegacyTimestampRepair(startVersion int) bool {
	if startVersion == migratedb.NilVersion {
		return false
	}
	return startVersion < LegacyTimestampRepairSchemaVersion
}

func RepairLegacyTimestamps(
	ctx context.Context, tx *sql.Tx,
) error {
	repairs := []struct {
		table  string
		column string
	}{
		{table: "forge_repos", column: "created_at"},
		{table: "forge_repos", column: "last_sync_started_at"},
		{table: "forge_repos", column: "last_sync_completed_at"},
		{table: "forge_merge_requests", column: "created_at"},
		{table: "forge_merge_requests", column: "updated_at"},
		{table: "forge_merge_requests", column: "last_activity_at"},
		{table: "forge_merge_requests", column: "merged_at"},
		{table: "forge_merge_requests", column: "closed_at"},
		{table: "forge_merge_requests", column: "detail_fetched_at"},
		{table: "forge_issues", column: "created_at"},
		{table: "forge_issues", column: "updated_at"},
		{table: "forge_issues", column: "last_activity_at"},
		{table: "forge_issues", column: "closed_at"},
		{table: "forge_issues", column: "detail_fetched_at"},
		{table: "forge_mr_events", column: "created_at"},
		{table: "forge_issue_events", column: "created_at"},
	}

	for _, repair := range repairs {
		if err := repairTimestampColumn(
			ctx, tx, repair.table, repair.column,
		); err != nil {
			return err
		}
	}
	return nil
}

func repairTimestampColumn(
	ctx context.Context, tx *sql.Tx, table, column string,
) error {
	query := fmt.Sprintf(
		`SELECT rowid, %s FROM %s WHERE %s IS NOT NULL`,
		column, table, column,
	)
	rows, err := tx.QueryContext(ctx, query)
	if err != nil {
		return fmt.Errorf("query %s.%s timestamps: %w", table, column, err)
	}
	defer rows.Close()

	type repairRow struct {
		rowID int64
		value time.Time
	}

	var repairs []repairRow
	for rows.Next() {
		var rowID int64
		var raw string
		if err := rows.Scan(&rowID, &raw); err != nil {
			return fmt.Errorf("scan %s.%s timestamp: %w", table, column, err)
		}
		parsed, err := parseStoredTimestamp(raw)
		if err != nil {
			return fmt.Errorf("parse %s.%s row %d timestamp %q: %w", table, column, rowID, raw, err)
		}
		if raw == canonicalUTCDatabaseString(parsed) {
			continue
		}
		repairs = append(repairs, repairRow{rowID: rowID, value: parsed.UTC()})
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterate %s.%s timestamps: %w", table, column, err)
	}

	update := fmt.Sprintf(`UPDATE %s SET %s = ? WHERE rowid = ?`, table, column)
	for _, repair := range repairs {
		if _, err := tx.ExecContext(ctx, update, repair.value, repair.rowID); err != nil {
			return fmt.Errorf("update %s.%s row %d timestamp: %w", table, column, repair.rowID, err)
		}
	}
	return nil
}

func canonicalUTCDatabaseString(t time.Time) string {
	return t.UTC().Format(time.RFC3339)
}

var storedTimestampLayouts = []string{
	"2006-01-02 15:04:05 +0000 UTC",
	"2006-01-02 15:04:05 -0700 -0700",
	"2006-01-02 15:04:05 -0700 MST",
	"2006-01-02T15:04:05Z",
	time.RFC3339,
	time.RFC3339Nano,
	"2006-01-02 15:04:05",
}

func parseStoredTimestamp(raw string) (time.Time, error) {
	for _, layout := range storedTimestampLayouts {
		if parsed, err := time.Parse(layout, raw); err == nil {
			return parsed.UTC(), nil
		}
	}
	return time.Time{}, fmt.Errorf("unrecognized time format: %q", raw)
}
