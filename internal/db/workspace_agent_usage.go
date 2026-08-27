package db

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"
)

type workspaceRuntimeSessionExecer interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
}

func upsertWorkspaceRuntimeSession(
	ctx context.Context,
	execer workspaceRuntimeSessionExecer,
	session *WorkspaceRuntimeSession,
) error {
	createdAt := canonicalUTCTime(session.CreatedAt)
	if createdAt.IsZero() {
		createdAt = time.Now().UTC()
	}
	_, err := execer.ExecContext(ctx, `
		INSERT INTO forge_workspace_runtime_sessions
		    (workspace_id, session_key, target_key, label, kind, display_region, scope,
		     tmux_session, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(workspace_id, session_key) DO UPDATE SET
		    target_key = excluded.target_key,
		    label = excluded.label,
		    kind = excluded.kind,
		    display_region = excluded.display_region,
		    scope = excluded.scope,
		    tmux_session = excluded.tmux_session,
		    created_at = excluded.created_at`,
		session.WorkspaceID, session.SessionKey, session.TargetKey,
		session.Label, session.Kind, session.DisplayRegion, session.Scope,
		session.TmuxSession, createdAt,
	)
	if err != nil {
		return fmt.Errorf("upsert workspace runtime session: %w", err)
	}
	return nil
}

// RecordWorkspaceRuntimeSession stores a runtime session and records each
// agent launch once, independently of the live session's later cleanup.
func (d *DB) RecordWorkspaceRuntimeSession(
	ctx context.Context,
	session *WorkspaceRuntimeSession,
) error {
	return d.Tx(ctx, func(tx *sql.Tx) error {
		if err := upsertWorkspaceRuntimeSession(ctx, tx, session); err != nil {
			return err
		}
		if session.Kind != "agent" {
			return nil
		}
		createdAt := canonicalUTCTime(session.CreatedAt)
		if createdAt.IsZero() {
			createdAt = time.Now().UTC()
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO forge_workspace_agent_launches
			    (session_key, target_key, created_at)
			VALUES (?, ?, ?)
			ON CONFLICT(session_key) DO NOTHING`,
			session.SessionKey, session.TargetKey, createdAt,
		); err != nil {
			return fmt.Errorf("record workspace agent launch: %w", err)
		}
		return nil
	})
}

// PreferredWorkspaceAgentTarget returns the most frequently launched allowed
// agent target since the supplied time. Recency and target key break ties.
func (d *DB) PreferredWorkspaceAgentTarget(
	ctx context.Context,
	since time.Time,
	targetKeys []string,
) (string, bool, error) {
	allowed := make(map[string]struct{}, len(targetKeys))
	for _, key := range targetKeys {
		if key = strings.TrimSpace(key); key != "" {
			allowed[key] = struct{}{}
		}
	}
	if len(allowed) == 0 {
		return "", false, nil
	}

	rows, err := d.ro.QueryContext(ctx, `
		SELECT target_key
		FROM forge_workspace_agent_launches
		WHERE created_at >= ?
		GROUP BY target_key
		ORDER BY COUNT(*) DESC, MAX(created_at) DESC, target_key ASC`, since.UTC())
	if err != nil {
		return "", false, fmt.Errorf("rank workspace agent launches: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var key string
		if err := rows.Scan(&key); err != nil {
			return "", false, fmt.Errorf("scan workspace agent launch rank: %w", err)
		}
		if _, ok := allowed[key]; ok {
			return key, true, nil
		}
	}
	if err := rows.Err(); err != nil {
		return "", false, fmt.Errorf("rank workspace agent launches: %w", err)
	}
	return "", false, nil
}
