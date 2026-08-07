package db

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestReserveAgentInitialMessagePersistsOnlyReceiptMetadata(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	database := openTestDB(t)
	ctx := t.Context()
	insertAgentReceiptTestRuntime(t, database, "ws-receipt", "runtime-1")

	receipt, reserved, err := database.ReserveAgentInitialMessage(ctx, AgentInitialMessageReceipt{
		WorkspaceID:       "ws-receipt",
		RuntimeSessionKey: "runtime-1",
		Agent:             "codex",
		CodingSessionID:   "coding-session-1",
		MessageBytes:      23,
	})
	require.NoError(err)
	assert.True(reserved)
	assert.Equal("ws-receipt", receipt.WorkspaceID)
	assert.Equal("runtime-1", receipt.RuntimeSessionKey)
	assert.Equal("codex", receipt.Agent)
	assert.Equal("coding-session-1", receipt.CodingSessionID)
	assert.Equal(23, receipt.MessageBytes)
	assert.Equal(AgentInitialMessagePending, receipt.State)
	assert.False(receipt.ReservedAt.IsZero())
	assert.Equal(receipt.ReservedAt, receipt.ReservedAt.UTC())
	assert.Nil(receipt.DeliveredAt)

	rows, err := database.ReadDB().Query(`PRAGMA table_info(forge_agent_initial_message_receipts)`)
	require.NoError(err)
	defer rows.Close()
	var columns []string
	for rows.Next() {
		var cid int
		var name, columnType string
		var notNull, primaryKey int
		var defaultValue any
		require.NoError(rows.Scan(&cid, &name, &columnType, &notNull, &defaultValue, &primaryKey))
		columns = append(columns, name)
	}
	require.NoError(rows.Err())
	assert.Equal([]string{
		"workspace_id",
		"runtime_session_key",
		"agent",
		"coding_session_id",
		"message_bytes",
		"state",
		"reserved_at",
		"delivered_at",
	}, columns)
}

func TestReserveAgentInitialMessageReturnsExistingReceiptBeforeRuntimeValidation(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	database := openTestDB(t)
	ctx := t.Context()
	insertAgentReceiptTestRuntime(t, database, "ws-existing-receipt", "runtime-existing")

	first, reserved, err := database.ReserveAgentInitialMessage(ctx, AgentInitialMessageReceipt{
		WorkspaceID:       "ws-existing-receipt",
		RuntimeSessionKey: "runtime-existing",
		Agent:             "codex",
		CodingSessionID:   "coding-original",
		MessageBytes:      12,
	})
	require.NoError(err)
	require.True(reserved)
	require.NoError(database.DeleteWorkspaceRuntimeSession(
		ctx, "ws-existing-receipt", "runtime-existing",
	))

	existing, reserved, err := database.ReserveAgentInitialMessage(ctx, AgentInitialMessageReceipt{
		WorkspaceID:       "ws-existing-receipt",
		RuntimeSessionKey: "runtime-existing",
		Agent:             "codex",
		CodingSessionID:   "coding-original",
		MessageBytes:      12,
	})
	require.NoError(err)
	assert.False(reserved)
	assert.Equal(first, existing)
}

func TestReserveAgentInitialMessageRejectsMismatchedExistingReceipt(t *testing.T) {
	require := require.New(t)
	database := openTestDB(t)
	ctx := t.Context()
	insertAgentReceiptTestRuntime(t, database, "ws-receipt-conflict", "runtime-conflict")
	original := AgentInitialMessageReceipt{
		WorkspaceID:       "ws-receipt-conflict",
		RuntimeSessionKey: "runtime-conflict",
		Agent:             "codex",
		CodingSessionID:   "coding-original",
		MessageBytes:      12,
	}
	_, reserved, err := database.ReserveAgentInitialMessage(ctx, original)
	require.NoError(err)
	require.True(reserved)
	require.NoError(database.DeleteWorkspaceRuntimeSession(
		ctx, original.WorkspaceID, original.RuntimeSessionKey,
	))

	for _, tc := range []struct {
		name   string
		change func(*AgentInitialMessageReceipt)
	}{
		{name: "agent", change: func(receipt *AgentInitialMessageReceipt) {
			receipt.Agent = "claude"
		}},
		{name: "coding session", change: func(receipt *AgentInitialMessageReceipt) {
			receipt.CodingSessionID = "coding-other"
		}},
		{name: "message bytes", change: func(receipt *AgentInitialMessageReceipt) {
			receipt.MessageBytes++
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			retry := original
			tc.change(&retry)
			_, reserved, reserveErr := database.ReserveAgentInitialMessage(ctx, retry)
			require.ErrorIs(reserveErr, ErrAgentInitialMessageReceiptConflict)
			require.False(reserved)
			var conflict *AgentInitialMessageReceiptConflictError
			require.ErrorAs(reserveErr, &conflict)
			require.Equal(original.Agent, conflict.Existing.Agent)
			require.Equal(original.CodingSessionID, conflict.Existing.CodingSessionID)
			require.Equal(original.MessageBytes, conflict.Existing.MessageBytes)
		})
	}
}

func TestReserveAgentInitialMessageRequiresStoredRuntime(t *testing.T) {
	require := require.New(t)
	database := openTestDB(t)
	ctx := t.Context()
	insertAgentReceiptTestWorkspace(t, database, "ws-missing-runtime")

	_, _, err := database.ReserveAgentInitialMessage(ctx, AgentInitialMessageReceipt{
		WorkspaceID:       "ws-missing-runtime",
		RuntimeSessionKey: "runtime-missing",
		Agent:             "codex",
		CodingSessionID:   "coding-missing",
		MessageBytes:      1,
	})
	require.ErrorIs(err, ErrAgentInitialMessageRuntimeNotFound)
}

func TestAgentInitialMessageReceiptTransitions(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	database := openTestDB(t)
	ctx := t.Context()
	insertAgentReceiptTestRuntime(t, database, "ws-transitions", "runtime-delivered")
	insertAgentReceiptTestRuntimeRow(t, database, "ws-transitions", "runtime-uncertain")

	_, reserved, err := database.ReserveAgentInitialMessage(ctx, AgentInitialMessageReceipt{
		WorkspaceID:       "ws-transitions",
		RuntimeSessionKey: "runtime-delivered",
		Agent:             "codex",
		CodingSessionID:   "coding-delivered",
		MessageBytes:      20,
	})
	require.NoError(err)
	require.True(reserved)
	_, reserved, err = database.ReserveAgentInitialMessage(ctx, AgentInitialMessageReceipt{
		WorkspaceID:       "ws-transitions",
		RuntimeSessionKey: "runtime-uncertain",
		Agent:             "claude",
		CodingSessionID:   "coding-uncertain",
		MessageBytes:      30,
	})
	require.NoError(err)
	require.True(reserved)

	delivered, err := database.MarkAgentInitialMessageDelivered(
		ctx, "ws-transitions", "runtime-delivered",
	)
	require.NoError(err)
	assert.Equal(AgentInitialMessageDelivered, delivered.State)
	require.NotNil(delivered.DeliveredAt)
	assert.Equal(delivered.DeliveredAt.UTC(), *delivered.DeliveredAt)

	uncertain, err := database.MarkAgentInitialMessageUncertain(
		ctx, "ws-transitions", "runtime-uncertain",
	)
	require.NoError(err)
	assert.Equal(AgentInitialMessageUncertain, uncertain.State)
	assert.Nil(uncertain.DeliveredAt)

	_, err = database.MarkAgentInitialMessageDelivered(
		ctx, "ws-transitions", "runtime-uncertain",
	)
	require.ErrorIs(err, ErrAgentInitialMessageReceiptNotPending)
}

func TestPendingAgentInitialMessageRecoveryRequiresExplicitDaemonStartupStep(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	databasePath := filepath.Join(t.TempDir(), "agent-message-recovery.db")
	database, err := Open(databasePath)
	require.NoError(err)
	ctx := t.Context()
	insertAgentReceiptTestRuntime(t, database, "ws-recovery", "runtime-pending")
	insertAgentReceiptTestRuntimeRow(t, database, "ws-recovery", "runtime-delivered")

	for _, session := range []string{"runtime-pending", "runtime-delivered"} {
		_, reserved, reserveErr := database.ReserveAgentInitialMessage(ctx, AgentInitialMessageReceipt{
			WorkspaceID:       "ws-recovery",
			RuntimeSessionKey: session,
			Agent:             "codex",
			CodingSessionID:   "coding-" + session,
			MessageBytes:      8,
		})
		require.NoError(reserveErr)
		require.True(reserved)
	}
	_, err = database.MarkAgentInitialMessageDelivered(
		ctx, "ws-recovery", "runtime-delivered",
	)
	require.NoError(err)
	require.NoError(database.Close())

	database, err = Open(databasePath)
	require.NoError(err)
	t.Cleanup(func() { require.NoError(database.Close()) })

	pending, err := database.GetAgentInitialMessageReceipt(
		ctx, "ws-recovery", "runtime-pending",
	)
	require.NoError(err)
	require.NotNil(pending)
	assert.Equal(AgentInitialMessagePending, pending.State)

	recovered, err := database.RecoverPendingAgentInitialMessages(ctx)
	require.NoError(err)
	assert.Equal(int64(1), recovered)
	pending, err = database.GetAgentInitialMessageReceipt(
		ctx, "ws-recovery", "runtime-pending",
	)
	require.NoError(err)
	require.NotNil(pending)
	assert.Equal(AgentInitialMessageUncertain, pending.State)
	delivered, err := database.GetAgentInitialMessageReceipt(
		ctx, "ws-recovery", "runtime-delivered",
	)
	require.NoError(err)
	require.NotNil(delivered)
	assert.Equal(AgentInitialMessageDelivered, delivered.State)
}

func TestAgentInitialMessageReceiptConstraintsAndCleanup(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	database := openTestDB(t)
	ctx := t.Context()
	insertAgentReceiptTestRuntime(t, database, "ws-constraints", "runtime-constraints")

	for _, tc := range []struct {
		name           string
		messageBytes   int
		state          string
		deliveredValue any
	}{
		{name: "negative bytes", messageBytes: -1, state: AgentInitialMessagePending},
		{name: "oversized bytes", messageBytes: 65537, state: AgentInitialMessagePending},
		{name: "invalid state", messageBytes: 1, state: "unknown"},
		{name: "delivered without timestamp", messageBytes: 1, state: AgentInitialMessageDelivered},
		{name: "pending with timestamp", messageBytes: 1, state: AgentInitialMessagePending, deliveredValue: "2026-08-07T12:00:00Z"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := database.WriteDB().ExecContext(ctx, `
				INSERT INTO forge_agent_initial_message_receipts (
					workspace_id, runtime_session_key, agent,
					coding_session_id, message_bytes, state, delivered_at
				) VALUES (?, ?, 'codex', 'coding-constraints', ?, ?, ?)`,
				"ws-constraints", "runtime-"+tc.name,
				tc.messageBytes, tc.state, tc.deliveredValue,
			)
			require.Error(err)
		})
	}

	_, reserved, err := database.ReserveAgentInitialMessage(ctx, AgentInitialMessageReceipt{
		WorkspaceID:       "ws-constraints",
		RuntimeSessionKey: "runtime-constraints",
		Agent:             "codex",
		CodingSessionID:   "coding-constraints",
		MessageBytes:      1,
	})
	require.NoError(err)
	require.True(reserved)
	require.NoError(database.DeleteWorkspaceRuntimeSession(
		ctx, "ws-constraints", "runtime-constraints",
	))
	receipt, err := database.GetAgentInitialMessageReceipt(
		ctx, "ws-constraints", "runtime-constraints",
	)
	require.NoError(err)
	require.NotNil(receipt)

	require.NoError(database.DeleteWorkspace(ctx, "ws-constraints"))
	receipt, err = database.GetAgentInitialMessageReceipt(
		ctx, "ws-constraints", "runtime-constraints",
	)
	require.NoError(err)
	assert.Nil(receipt)
}

func TestMarkAgentInitialMessageMissingReceipt(t *testing.T) {
	require := require.New(t)
	database := openTestDB(t)

	_, err := database.MarkAgentInitialMessageUncertain(
		t.Context(), "missing-workspace", "missing-runtime",
	)
	require.ErrorIs(err, ErrAgentInitialMessageReceiptNotFound)
}

func insertAgentReceiptTestRuntime(
	t *testing.T,
	database *DB,
	workspaceID string,
	runtimeSessionKey string,
) {
	t.Helper()
	insertAgentReceiptTestWorkspace(t, database, workspaceID)
	insertAgentReceiptTestRuntimeRow(t, database, workspaceID, runtimeSessionKey)
}

func insertAgentReceiptTestWorkspace(t *testing.T, database *DB, workspaceID string) {
	t.Helper()
	require.NoError(t, database.InsertWorkspace(t.Context(), &Workspace{
		ID:              workspaceID,
		Platform:        "github",
		PlatformHost:    "github.com",
		RepoOwner:       "acme",
		RepoName:        "widgets",
		ItemType:        WorkspaceItemTypePullRequest,
		ItemNumber:      42,
		GitHeadRef:      "feature/receipt",
		WorkspaceBranch: "feature/receipt",
		WorktreePath:    "/tmp/" + workspaceID,
		TmuxSession:     "kenn-forge-" + workspaceID,
		Status:          "ready",
	}))
}

func insertAgentReceiptTestRuntimeRow(
	t *testing.T,
	database *DB,
	workspaceID string,
	runtimeSessionKey string,
) {
	t.Helper()
	require.NoError(t, database.UpsertWorkspaceRuntimeSession(t.Context(), &WorkspaceRuntimeSession{
		WorkspaceID: workspaceID,
		SessionKey:  runtimeSessionKey,
		TargetKey:   "codex",
		Label:       "Codex",
		Kind:        "agent",
		Scope:       "session",
	}))
}
