package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// ExternalAgentSession is an agent session that reports through hooks without
// a Forge runtime: one started outside Forge.
type ExternalAgentSession struct {
	Agent          string
	SessionID      string
	CWD            string
	TranscriptPath string
	// ProcessID and ProcessStart identify the agent's process, zero when its
	// hook could not tell. Only a process known gone proves the session ended
	// without a SessionEnd hook.
	ProcessID    int
	ProcessStart string
	// HerdrPaneID and HerdrSocketPath name the Herdr pane the agent runs in,
	// empty when it runs in any other terminal.
	HerdrPaneID     string
	HerdrSocketPath string
	FirstSeenAt     time.Time
	EndedAt         *time.Time
	// WorkspaceID and RuntimeSessionKey name the workspace and chat that adopted
	// the session. They stay set after the workspace is deleted, so a session
	// someone removed from Forge is never adopted again.
	WorkspaceID       string
	RuntimeSessionKey string
	// HandedOffAt is when the session moved into a Forge-run agent in its chat.
	HandedOffAt *time.Time
}

const externalAgentSessionColumns = `agent, session_id, cwd, transcript_path, process_id, process_start,
	herdr_pane_id, herdr_socket_path, first_seen_at, ended_at, workspace_id,
	runtime_session_key, handed_off_at`

func scanExternalAgentSession(scanner interface{ Scan(...any) error }) (ExternalAgentSession, error) {
	var session ExternalAgentSession
	var ended, handedOff sql.NullTime
	err := scanner.Scan(
		&session.Agent, &session.SessionID, &session.CWD, &session.TranscriptPath,
		&session.ProcessID, &session.ProcessStart, &session.HerdrPaneID, &session.HerdrSocketPath,
		&session.FirstSeenAt, &ended, &session.WorkspaceID, &session.RuntimeSessionKey, &handedOff,
	)
	if err != nil {
		return ExternalAgentSession{}, err
	}
	session.FirstSeenAt = session.FirstSeenAt.UTC()
	if ended.Valid {
		at := ended.Time.UTC()
		session.EndedAt = &at
	}
	if handedOff.Valid {
		at := handedOff.Time.UTC()
		session.HandedOffAt = &at
	}
	return session, nil
}

// RecordExternalAgentSession saves what a hook reported about a session. A
// report for a session that ended means it was resumed outside Forge and runs
// again, unless Forge now runs it. An empty report field keeps the saved value,
// since agents do not send every field on every event.
func (d *DB) RecordExternalAgentSession(ctx context.Context, session ExternalAgentSession) error {
	_, err := d.rwExecContext(ctx, `
		INSERT INTO forge_external_agent_sessions
		    (agent, session_id, cwd, transcript_path, process_id, process_start,
		     herdr_pane_id, herdr_socket_path)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT (agent, session_id) DO UPDATE SET
		    cwd = excluded.cwd,
		    transcript_path = COALESCE(NULLIF(excluded.transcript_path, ''), transcript_path),
		    process_id = CASE WHEN excluded.process_id != 0 THEN excluded.process_id ELSE process_id END,
		    process_start = COALESCE(NULLIF(excluded.process_start, ''), process_start),
		    herdr_pane_id = excluded.herdr_pane_id,
		    herdr_socket_path = excluded.herdr_socket_path,
		    ended_at = NULL
		WHERE handed_off_at IS NULL`,
		session.Agent, session.SessionID, session.CWD, session.TranscriptPath,
		session.ProcessID, session.ProcessStart, session.HerdrPaneID, session.HerdrSocketPath,
	)
	if err != nil {
		return fmt.Errorf("record external agent session: %w", err)
	}
	return nil
}

// MarkExternalAgentSessionEnded records that the session's agent process ended.
func (d *DB) MarkExternalAgentSessionEnded(ctx context.Context, agent, sessionID string, at time.Time) error {
	_, err := d.rwExecContext(ctx, `
		UPDATE forge_external_agent_sessions SET ended_at = ?
		WHERE agent = ? AND session_id = ? AND ended_at IS NULL`,
		at.UTC(), agent, sessionID,
	)
	if err != nil {
		return fmt.Errorf("mark external agent session ended: %w", err)
	}
	return nil
}

// ListRunningExternalAgentSessions returns sessions whose agent may still run,
// oldest first, so a worktree's first agent is considered before any agent it
// started.
func (d *DB) ListRunningExternalAgentSessions(ctx context.Context) ([]ExternalAgentSession, error) {
	return d.listExternalAgentSessions(ctx, `ended_at IS NULL AND handed_off_at IS NULL`)
}

// ListEndedUnadoptedExternalAgentSessions returns sessions that ended at or
// after since without a workspace, oldest first. Their pull request may sync
// after the agent exits.
func (d *DB) ListEndedUnadoptedExternalAgentSessions(ctx context.Context, since time.Time) ([]ExternalAgentSession, error) {
	return d.listExternalAgentSessions(ctx, `ended_at >= ? AND workspace_id = '' AND handed_off_at IS NULL`, since.UTC())
}

func (d *DB) listExternalAgentSessions(ctx context.Context, where string, args ...any) ([]ExternalAgentSession, error) {
	rows, err := d.roQueryContext(ctx, `
		SELECT `+externalAgentSessionColumns+`
		FROM forge_external_agent_sessions
		WHERE `+where+`
		ORDER BY first_seen_at, agent, session_id`, args...)
	if err != nil {
		return nil, fmt.Errorf("list external agent sessions: %w", err)
	}
	defer rows.Close()
	var sessions []ExternalAgentSession
	for rows.Next() {
		session, err := scanExternalAgentSession(rows)
		if err != nil {
			return nil, fmt.Errorf("scan external agent session: %w", err)
		}
		sessions = append(sessions, session)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list external agent sessions: %w", err)
	}
	return sessions, nil
}

// GetExternalAgentSession returns one session, or nil when none is saved.
func (d *DB) GetExternalAgentSession(ctx context.Context, agent, sessionID string) (*ExternalAgentSession, error) {
	return d.oneExternalAgentSession(ctx, `agent = ? AND session_id = ?`, agent, sessionID)
}

// GetExternalAgentSessionByRuntimeKey returns the session a chat mirrors, or
// nil when the chat mirrors none.
func (d *DB) GetExternalAgentSessionByRuntimeKey(ctx context.Context, key string) (*ExternalAgentSession, error) {
	return d.oneExternalAgentSession(ctx, `runtime_session_key = ?`, key)
}

func (d *DB) oneExternalAgentSession(ctx context.Context, where string, args ...any) (*ExternalAgentSession, error) {
	session, err := scanExternalAgentSession(d.roQueryRowContext(ctx,
		`SELECT `+externalAgentSessionColumns+` FROM forge_external_agent_sessions WHERE `+where, args...))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get external agent session: %w", err)
	}
	return &session, nil
}

// AdoptExternalAgentSession records the workspace and chat that adopted a session.
func (d *DB) AdoptExternalAgentSession(ctx context.Context, agent, sessionID, workspaceID, key string) error {
	_, err := d.rwExecContext(ctx, `
		UPDATE forge_external_agent_sessions SET workspace_id = ?, runtime_session_key = ?
		WHERE agent = ? AND session_id = ?`,
		workspaceID, key, agent, sessionID,
	)
	if err != nil {
		return fmt.Errorf("adopt external agent session: %w", err)
	}
	return nil
}

// MarkExternalAgentSessionHandedOff records that a Forge-run agent now holds
// the session, so later reports under its ID no longer come from outside Forge.
func (d *DB) MarkExternalAgentSessionHandedOff(ctx context.Context, agent, sessionID string, at time.Time) error {
	_, err := d.rwExecContext(ctx, `
		UPDATE forge_external_agent_sessions SET handed_off_at = ?
		WHERE agent = ? AND session_id = ?`,
		at.UTC(), agent, sessionID,
	)
	if err != nil {
		return fmt.Errorf("mark external agent session handed off: %w", err)
	}
	return nil
}

// DeleteEndedUnadoptedExternalAgentSessions forgets sessions that ended before
// endedBefore without a workspace.
func (d *DB) DeleteEndedUnadoptedExternalAgentSessions(ctx context.Context, endedBefore time.Time) error {
	_, err := d.rwExecContext(ctx, `
		DELETE FROM forge_external_agent_sessions
		WHERE ended_at < ? AND workspace_id = ''`, endedBefore.UTC())
	if err != nil {
		return fmt.Errorf("delete ended external agent sessions: %w", err)
	}
	return nil
}
