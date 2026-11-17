package server

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"go.kenn.io/forge/internal/db"
	"go.kenn.io/forge/internal/server/authapi"
	"go.kenn.io/forge/internal/server/workspaceapi"
	"go.kenn.io/forge/internal/testutil/serverfake"
	"go.kenn.io/forge/internal/workspace/localruntime"
)

// holdRuntimeWriterPastOldDeadline keeps SQLite's single writer busy for three
// seconds. Runtime-session cleanup once gave its delete two seconds, and on a
// busy machine the delete gave up and no later pass removed the row.
func holdRuntimeWriterPastOldDeadline(t *testing.T, database *db.DB) {
	t.Helper()
	transaction := occupyRuntimeWriter(t, database)
	release := time.AfterFunc(3*time.Second, func() { _ = transaction.Rollback() })
	t.Cleanup(func() { release.Stop() })
}

func TestHostRuntimeExitForgetsStoredTmuxSessionAfterWaitingForBusyWriter(t *testing.T) {
	serverfake.RunParallelServerTest(t)
	require := require.New(t)
	srv, _, _ := setupProjectWorktreeRuntimeTest(t)
	ctx := t.Context()
	createdAt := time.Date(2026, 11, 12, 3, 37, 1, 0, time.UTC)
	require.NoError(srv.db.UpsertHostRuntimeTmuxSession(ctx, &db.HostRuntimeTmuxSession{
		SessionKey: "cmd-busy", SessionName: "host-runtime-busy",
		CWD: t.TempDir(), CreatedAt: createdAt,
	}))

	holdRuntimeWriterPastOldDeadline(t, srv.db)
	srv.streamapi.HandleRuntimeSessionExit(localruntime.SessionInfo{
		Key: "cmd-busy", WorkspaceID: authapi.HostRuntimeScope,
		TmuxSession: "host-runtime-busy", CreatedAt: createdAt,
	})

	require.Eventually(func() bool {
		rows, err := srv.db.ListHostRuntimeTmuxSessions(ctx)
		return err == nil && len(rows) == 0
	}, 10*time.Second, 10*time.Millisecond)
}

func TestProjectWorktreeRuntimeExitForgetsStoredTmuxSessionAfterWaitingForBusyWriter(
	t *testing.T,
) {
	serverfake.RunParallelServerTest(t)
	require := require.New(t)
	srv, _, worktreeID := setupProjectWorktreeRuntimeTest(t)
	ctx := t.Context()
	sessionKey := worktreeID + "_helper"
	createdAt := time.Date(2026, 11, 12, 3, 37, 1, 0, time.UTC)
	require.NoError(srv.db.UpsertProjectWorktreeTmuxSession(ctx, &db.ProjectWorktreeTmuxSession{
		WorktreeID: worktreeID, SessionKey: sessionKey,
		SessionName: "project-runtime-busy", TargetKey: "helper", CreatedAt: createdAt,
	}))

	holdRuntimeWriterPastOldDeadline(t, srv.db)
	srv.streamapi.HandleRuntimeSessionExit(localruntime.SessionInfo{
		Key: sessionKey, WorkspaceID: workspaceapi.ProjectWorktreeRuntimeScope(worktreeID),
		TargetKey: "helper", TmuxSession: "project-runtime-busy", CreatedAt: createdAt,
	})

	require.Eventually(func() bool {
		rows, err := srv.db.ListProjectWorktreeTmuxSessions(ctx, worktreeID)
		return err == nil && len(rows) == 0
	}, 10*time.Second, 10*time.Millisecond)
}

func TestForgetHostRuntimeCommandSessionIfExitedWaitsForBusyWriter(t *testing.T) {
	serverfake.RunParallelServerTest(t)
	require := require.New(t)
	srv, _, _ := setupProjectWorktreeRuntimeTest(t)
	ctx := t.Context()
	session := localruntime.SessionInfo{
		Key: "cmd-exited", WorkspaceID: authapi.HostRuntimeScope,
		TmuxSession: "host-runtime-exited",
		CreatedAt:   time.Date(2026, 11, 12, 3, 37, 1, 0, time.UTC),
	}
	require.NoError(srv.db.UpsertHostRuntimeTmuxSession(ctx, &db.HostRuntimeTmuxSession{
		SessionKey: session.Key, SessionName: session.TmuxSession,
		CWD: t.TempDir(), CreatedAt: session.CreatedAt,
	}))

	holdRuntimeWriterPastOldDeadline(t, srv.db)
	srv.hostapi.ForgetHostRuntimeCommandSessionIfExited(ctx, session)

	rows, err := srv.db.ListHostRuntimeTmuxSessions(ctx)
	require.NoError(err)
	require.Empty(rows)
}
