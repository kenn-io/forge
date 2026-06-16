package db

import (
	"context"
	"fmt"
	"time"
)

// HostRuntimeTmuxSession records a tmux session owned by runtime launched at
// host scope (not tied to a registered project worktree). SessionKey is the
// runtime session key; routing and cleanup key off it.
type HostRuntimeTmuxSession struct {
	SessionKey  string
	SessionName string
	Label       string
	CWD         string
	CreatedAt   time.Time
}

// UpsertHostRuntimeTmuxSession records a tmux-backed host-scoped runtime
// launch, keyed by the runtime session key.
func (d *DB) UpsertHostRuntimeTmuxSession(
	ctx context.Context,
	session *HostRuntimeTmuxSession,
) error {
	createdAt := canonicalUTCTime(session.CreatedAt)
	if createdAt.IsZero() {
		createdAt = time.Now().UTC()
	}
	_, err := d.rw.ExecContext(ctx, `
		INSERT INTO middleman_host_runtime_sessions
		    (session_key, runtime_backend, backend_session_key, label, cwd,
		     created_at)
		VALUES (?, 'tmux', ?, ?, ?, ?)
		ON CONFLICT(session_key) DO UPDATE SET
		    runtime_backend = excluded.runtime_backend,
		    backend_session_key = excluded.backend_session_key,
		    label = excluded.label,
		    cwd = excluded.cwd,
		    created_at = excluded.created_at`,
		session.SessionKey, session.SessionName, session.Label,
		session.CWD, createdAt,
	)
	if err != nil {
		return fmt.Errorf("upsert host runtime tmux session: %w", err)
	}
	return nil
}

// ListHostRuntimeTmuxSessions returns stored host-scoped runtime tmux
// sessions ordered by creation time.
func (d *DB) ListHostRuntimeTmuxSessions(
	ctx context.Context,
) ([]HostRuntimeTmuxSession, error) {
	rows, err := d.ro.QueryContext(ctx, `
		SELECT session_key, COALESCE(backend_session_key, ''), label, cwd,
		       created_at
		FROM middleman_host_runtime_sessions
		WHERE runtime_backend = 'tmux'
		ORDER BY created_at, session_key`,
	)
	if err != nil {
		return nil, fmt.Errorf("list host runtime tmux sessions: %w", err)
	}
	defer rows.Close()

	var out []HostRuntimeTmuxSession
	for rows.Next() {
		var session HostRuntimeTmuxSession
		if err := rows.Scan(
			&session.SessionKey, &session.SessionName, &session.Label,
			&session.CWD, &session.CreatedAt,
		); err != nil {
			return nil, fmt.Errorf("scan host runtime tmux session: %w", err)
		}
		session.CreatedAt = session.CreatedAt.UTC()
		out = append(out, session)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate host runtime tmux sessions: %w", err)
	}
	return out, nil
}

// DeleteHostRuntimeTmuxSession removes one stored host-scoped runtime tmux
// session by its runtime session key.
func (d *DB) DeleteHostRuntimeTmuxSession(
	ctx context.Context,
	sessionKey string,
) error {
	_, err := d.rw.ExecContext(ctx, `
		DELETE FROM middleman_host_runtime_sessions
		WHERE session_key = ? AND runtime_backend = 'tmux'`, sessionKey,
	)
	if err != nil {
		return fmt.Errorf("delete host runtime tmux session: %w", err)
	}
	return nil
}

// DeleteHostRuntimeTmuxSessionCreatedAt removes one stored host-scoped
// runtime tmux session only if it still belongs to the same session
// generation. Host command session keys are caller-owned and reusable, so an
// exited generation's async cleanup must not delete the row a newer live
// session with the same key has since written.
func (d *DB) DeleteHostRuntimeTmuxSessionCreatedAt(
	ctx context.Context,
	sessionKey string,
	createdAt time.Time,
) (bool, error) {
	result, err := d.rw.ExecContext(ctx, `
		DELETE FROM middleman_host_runtime_sessions
		WHERE session_key = ?
		  AND runtime_backend = 'tmux'
		  AND created_at = ?`,
		sessionKey, canonicalUTCTime(createdAt),
	)
	if err != nil {
		return false, fmt.Errorf("delete host runtime tmux session: %w", err)
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("delete host runtime tmux session rows: %w", err)
	}
	return rows > 0, nil
}
