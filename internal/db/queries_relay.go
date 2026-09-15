package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"go.kenn.io/forge/internal/activityrelay"
)

func (d *DB) RelayCursor(ctx context.Context, relayURL string) (string, error) {
	var cursor string
	err := d.roQueryRowContext(ctx, `SELECT cursor FROM forge_relay_cursors WHERE relay_url = ?`, relayURL).Scan(&cursor)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("read relay cursor: %w", err)
	}
	return cursor, nil
}

// SaveRelayPage checkpoints only after every selected target is durable. A single
// poll/drain loop owns these rows; provider work never races another page writer.
func (d *DB) SaveRelayPage(ctx context.Context, relayURL, cursor string, hints []activityrelay.Hint) error {
	if relayURL == "" || cursor == "" {
		return errors.New("relay checkpoint requires URL and cursor")
	}
	tx, err := d.rw.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin relay checkpoint: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	_, err = tx.ExecContext(ctx, `INSERT INTO forge_relay_cursors (relay_url, cursor) VALUES (?, ?)
		ON CONFLICT(relay_url) DO UPDATE SET cursor = excluded.cursor`, relayURL, cursor)
	if err != nil {
		return fmt.Errorf("save relay cursor: %w", err)
	}
	for _, hint := range hints {
		if err := hint.Validate(); err != nil {
			return err
		}
		if hint.Target == activityrelay.PullRequestChecks {
			var pending bool
			err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM forge_relay_pending
				WHERE relay_url = ? AND repository_id = ? AND target = 'pull_request' AND number = ?)`,
				relayURL, hint.RepositoryID, hint.Number).Scan(&pending)
			if err != nil {
				return fmt.Errorf("read pending PR refresh: %w", err)
			}
			if pending {
				continue
			}
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO forge_relay_pending (relay_url, repository_id, target, number)
			VALUES (?, ?, ?, ?) ON CONFLICT DO NOTHING`, relayURL, hint.RepositoryID, hint.Target, hint.Number)
		if err != nil {
			return fmt.Errorf("save relay target: %w", err)
		}
		if hint.Target == activityrelay.PullRequest {
			_, err = tx.ExecContext(ctx, `DELETE FROM forge_relay_pending
				WHERE relay_url = ? AND repository_id = ? AND target = 'pull_request_checks' AND number = ?`,
				relayURL, hint.RepositoryID, hint.Number)
			if err != nil {
				return fmt.Errorf("coalesce relay CI refresh: %w", err)
			}
		}
	}
	return tx.Commit()
}

func (d *DB) PendingRelayHints(ctx context.Context, relayURL string) ([]activityrelay.Hint, error) {
	rows, err := d.roQueryContext(ctx, `SELECT repository_id, target, number FROM forge_relay_pending
		WHERE relay_url = ? ORDER BY repository_id, target, number`, relayURL)
	if err != nil {
		return nil, fmt.Errorf("read pending relay hints: %w", err)
	}
	defer rows.Close()
	hints := []activityrelay.Hint{}
	for rows.Next() {
		hint := activityrelay.Hint{Provider: "github", Host: "github.com"}
		if err := rows.Scan(&hint.RepositoryID, &hint.Target, &hint.Number); err != nil {
			return nil, fmt.Errorf("scan pending relay hint: %w", err)
		}
		hints = append(hints, hint)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate pending relay hints: %w", err)
	}
	return hints, nil
}

func (d *DB) CompleteRelayHint(ctx context.Context, relayURL string, hint activityrelay.Hint) error {
	_, err := d.rwExecContext(ctx, `DELETE FROM forge_relay_pending
		WHERE relay_url = ? AND repository_id = ? AND target = ? AND number = ?`,
		relayURL, hint.RepositoryID, hint.Target, hint.Number)
	if err != nil {
		return fmt.Errorf("complete relay hint: %w", err)
	}
	return nil
}
