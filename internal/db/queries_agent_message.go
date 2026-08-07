package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

var (
	ErrAgentInitialMessageRuntimeNotFound   = errors.New("agent initial message runtime not found")
	ErrAgentInitialMessageReceiptNotFound   = errors.New("agent initial message receipt not found")
	ErrAgentInitialMessageReceiptNotPending = errors.New("agent initial message receipt is not pending")
	ErrAgentInitialMessageReceiptConflict   = errors.New("agent initial message receipt identity conflict")
)

// AgentInitialMessageReceiptConflictError reports an existing attempt whose
// correlation metadata does not match the proposed retry.
type AgentInitialMessageReceiptConflictError struct {
	Existing AgentInitialMessageReceipt
}

func (e *AgentInitialMessageReceiptConflictError) Error() string {
	return ErrAgentInitialMessageReceiptConflict.Error()
}

func (e *AgentInitialMessageReceiptConflictError) Unwrap() error {
	return ErrAgentInitialMessageReceiptConflict
}

// ReserveAgentInitialMessage records the one permitted delivery attempt. An
// existing receipt wins even after its runtime row has been cleaned up.
func (d *DB) ReserveAgentInitialMessage(
	ctx context.Context,
	receipt AgentInitialMessageReceipt,
) (AgentInitialMessageReceipt, bool, error) {
	var stored AgentInitialMessageReceipt
	reserved := false
	err := d.Tx(ctx, func(tx *sql.Tx) error {
		existing, err := getAgentInitialMessageReceipt(ctx, tx, receipt.WorkspaceID, receipt.RuntimeSessionKey)
		if err != nil {
			return err
		}
		if existing != nil {
			if existing.Agent != receipt.Agent ||
				existing.CodingSessionID != receipt.CodingSessionID ||
				existing.MessageBytes != receipt.MessageBytes {
				return &AgentInitialMessageReceiptConflictError{Existing: *existing}
			}
			stored = *existing
			return nil
		}

		var runtimeExists bool
		if err := tx.QueryRowContext(ctx, `
			SELECT EXISTS (
				SELECT 1
				FROM forge_workspace_runtime_sessions
				WHERE workspace_id = ? AND session_key = ?
			)`, receipt.WorkspaceID, receipt.RuntimeSessionKey,
		).Scan(&runtimeExists); err != nil {
			return fmt.Errorf("reserve agent initial message: verify runtime: %w", err)
		}
		if !runtimeExists {
			return ErrAgentInitialMessageRuntimeNotFound
		}

		if err := scanAgentInitialMessageReceipt(tx.QueryRowContext(ctx, `
			INSERT INTO forge_agent_initial_message_receipts (
				workspace_id, runtime_session_key, agent,
				coding_session_id, message_bytes, state
			) VALUES (?, ?, ?, ?, ?, 'pending')
			RETURNING workspace_id, runtime_session_key, agent,
			          coding_session_id, message_bytes, state,
			          reserved_at, delivered_at`,
			receipt.WorkspaceID,
			receipt.RuntimeSessionKey,
			receipt.Agent,
			receipt.CodingSessionID,
			receipt.MessageBytes,
		), &stored); err != nil {
			return fmt.Errorf("reserve agent initial message: insert: %w", err)
		}
		reserved = true
		return nil
	})
	if err != nil {
		return AgentInitialMessageReceipt{}, false, err
	}
	return stored, reserved, nil
}

// GetAgentInitialMessageReceipt returns the durable receipt for one runtime.
func (d *DB) GetAgentInitialMessageReceipt(
	ctx context.Context,
	workspaceID string,
	runtimeSessionKey string,
) (*AgentInitialMessageReceipt, error) {
	return getAgentInitialMessageReceipt(ctx, d.ro, workspaceID, runtimeSessionKey)
}

// MarkAgentInitialMessageDelivered completes a pending attempt after all input
// bytes were written successfully.
func (d *DB) MarkAgentInitialMessageDelivered(
	ctx context.Context,
	workspaceID string,
	runtimeSessionKey string,
) (AgentInitialMessageReceipt, error) {
	return d.transitionAgentInitialMessage(
		ctx, workspaceID, runtimeSessionKey, AgentInitialMessageDelivered,
	)
}

// MarkAgentInitialMessageUncertain closes a pending attempt whose delivery
// cannot be proven. An uncertain attempt must never be retried.
func (d *DB) MarkAgentInitialMessageUncertain(
	ctx context.Context,
	workspaceID string,
	runtimeSessionKey string,
) (AgentInitialMessageReceipt, error) {
	return d.transitionAgentInitialMessage(
		ctx, workspaceID, runtimeSessionKey, AgentInitialMessageUncertain,
	)
}

// RecoverPendingAgentInitialMessages marks attempts interrupted by daemon
// restart as uncertain before any new delivery orchestration begins.
func (d *DB) RecoverPendingAgentInitialMessages(ctx context.Context) (int64, error) {
	result, err := d.rw.ExecContext(ctx, `
		UPDATE forge_agent_initial_message_receipts
		SET state = 'uncertain'
		WHERE state = 'pending'`)
	if err != nil {
		return 0, fmt.Errorf("recover pending agent initial messages: %w", err)
	}
	count, err := result.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("recover pending agent initial messages rows: %w", err)
	}
	return count, nil
}

func (d *DB) transitionAgentInitialMessage(
	ctx context.Context,
	workspaceID string,
	runtimeSessionKey string,
	state string,
) (AgentInitialMessageReceipt, error) {
	var stored AgentInitialMessageReceipt
	err := d.Tx(ctx, func(tx *sql.Tx) error {
		err := scanAgentInitialMessageReceipt(tx.QueryRowContext(ctx, `
			UPDATE forge_agent_initial_message_receipts
			SET state = ?,
			    delivered_at = CASE
			        WHEN ? = 'delivered' THEN datetime('now')
			        ELSE NULL
			    END
			WHERE workspace_id = ? AND runtime_session_key = ? AND state = 'pending'
			RETURNING workspace_id, runtime_session_key, agent,
			          coding_session_id, message_bytes, state,
			          reserved_at, delivered_at`,
			state, state, workspaceID, runtimeSessionKey,
		),
			&stored,
		)
		if err == nil {
			return nil
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("mark agent initial message %s: %w", state, err)
		}
		existing, err := getAgentInitialMessageReceipt(ctx, tx, workspaceID, runtimeSessionKey)
		if err != nil {
			return err
		}
		if existing == nil {
			return ErrAgentInitialMessageReceiptNotFound
		}
		return ErrAgentInitialMessageReceiptNotPending
	})
	if err != nil {
		return AgentInitialMessageReceipt{}, err
	}
	return stored, nil
}

type agentInitialMessageQuerier interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func getAgentInitialMessageReceipt(
	ctx context.Context,
	querier agentInitialMessageQuerier,
	workspaceID string,
	runtimeSessionKey string,
) (*AgentInitialMessageReceipt, error) {
	var receipt AgentInitialMessageReceipt
	err := scanAgentInitialMessageReceipt(querier.QueryRowContext(ctx, `
		SELECT workspace_id, runtime_session_key, agent,
		       coding_session_id, message_bytes, state,
		       reserved_at, delivered_at
		FROM forge_agent_initial_message_receipts
		WHERE workspace_id = ? AND runtime_session_key = ?`,
		workspaceID, runtimeSessionKey,
	), &receipt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get agent initial message receipt: %w", err)
	}
	return &receipt, nil
}

func scanAgentInitialMessageReceipt(
	row *sql.Row,
	receipt *AgentInitialMessageReceipt,
) error {
	var deliveredAt sql.NullTime
	if err := row.Scan(
		&receipt.WorkspaceID,
		&receipt.RuntimeSessionKey,
		&receipt.Agent,
		&receipt.CodingSessionID,
		&receipt.MessageBytes,
		&receipt.State,
		&receipt.ReservedAt,
		&deliveredAt,
	); err != nil {
		return err
	}
	receipt.ReservedAt = receipt.ReservedAt.UTC()
	if deliveredAt.Valid {
		delivered := deliveredAt.Time.UTC()
		receipt.DeliveredAt = &delivered
	}
	return nil
}
