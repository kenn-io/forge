package agentactivity

import (
	"encoding/json"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestStoreTracksHookLifecycleByRuntimeSession(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	root := t.TempDir()
	workspace := t.TempDir()
	store := NewStore(root)
	now := time.Date(2026, 7, 26, 12, 0, 0, 0, time.UTC)
	store.now = func() time.Time { return now }

	reportHook(t, store, "runtime-a", map[string]any{
		"session_id": "agent-a", "cwd": workspace,
		"hook_event_name": "UserPromptSubmit",
	})
	snapshot, ok := store.SnapshotForWorkspace(workspace, []string{"runtime-a"})
	require.True(ok)
	assert.Equal(StateWorking, snapshot.State)
	assert.Equal(now, snapshot.UpdatedAt)

	now = now.Add(time.Minute)
	reportHook(t, store, "runtime-a", map[string]any{
		"session_id": "agent-a", "cwd": workspace,
		"hook_event_name": "PreToolUse", "tool_name": "request_user_input",
	})
	snapshot, ok = store.SnapshotForWorkspace(workspace, []string{"runtime-a"})
	require.True(ok)
	assert.Equal(StateInput, snapshot.State)

	reportHook(t, store, "runtime-a", map[string]any{
		"session_id": "agent-a", "cwd": workspace,
		"hook_event_name": "Stop",
	})
	snapshot, ok = store.SnapshotForWorkspace(workspace, []string{"runtime-a"})
	require.True(ok)
	assert.Equal(StateDone, snapshot.State)

	reportHook(t, store, "runtime-a", map[string]any{
		"session_id": "agent-a", "cwd": workspace,
		"hook_event_name": "SessionEnd",
	})
	_, ok = store.SnapshotForWorkspace(workspace, []string{"runtime-a"})
	assert.False(ok)
}

func TestStoreAggregatesOnlyLiveWorkspaceSessions(t *testing.T) {
	store := NewStore(t.TempDir())
	workspace := t.TempDir()
	reportHook(t, store, "runtime-stale", map[string]any{
		"session_id": "agent-stale", "cwd": workspace,
		"hook_event_name": "PermissionRequest",
	})
	reportHook(t, store, "runtime-live", map[string]any{
		"session_id": "agent-live", "cwd": workspace,
		"hook_event_name": "UserPromptSubmit",
	})

	snapshot, ok := store.SnapshotForWorkspace(workspace, []string{"runtime-live"})
	require.True(t, ok)
	assert.Equal(t, StateWorking, snapshot.State)
}

func TestStoreMatchesWorkspaceReachedThroughSymlink(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink creation requires elevated privileges on Windows")
	}
	require := require.New(t)
	workspace := t.TempDir()
	workspaceLink := filepath.Join(t.TempDir(), "workspace-link")
	require.NoError(os.Symlink(workspace, workspaceLink))
	store := NewStore(t.TempDir())
	reportHook(t, store, "runtime-live", map[string]any{
		"session_id": "agent-live", "cwd": workspaceLink,
		"hook_event_name": "UserPromptSubmit",
	})

	snapshot, ok := store.SnapshotForWorkspace(workspace, []string{"runtime-live"})
	require.True(ok)
	assert.Equal(t, StateWorking, snapshot.State)
}

func TestStoreKeepsReportsUntilTheSessionIsTornDown(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	root := t.TempDir()
	workspace := t.TempDir()
	store := NewStore(root)
	now := time.Date(2026, 7, 27, 12, 0, 0, 0, time.UTC)
	store.now = func() time.Time { return now }

	reportHook(t, store, "runtime-quiet", map[string]any{
		"session_id": "agent-quiet", "cwd": workspace,
		"hook_event_name": "UserPromptSubmit",
	})
	// A launched agent may go hours without a lifecycle event while it works;
	// its hook state must not lapse to weaker signals on a timer.
	now = now.Add(6 * time.Hour)

	snapshot, ok := store.SnapshotForWorkspace(workspace, []string{"runtime-quiet"})
	require.True(ok)
	assert.Equal(StateWorking, snapshot.State)

	require.NoError(store.RemoveRuntimeSession("runtime-quiet"))
	_, ok = store.SnapshotForWorkspace(workspace, []string{"runtime-quiet"})
	assert.False(ok)
	entries, err := os.ReadDir(root)
	require.NoError(err)
	assert.Empty(entries)
}

func TestStoreCacheObservesReportsWrittenByAnotherProcess(t *testing.T) {
	require := require.New(t)
	root := t.TempDir()
	workspace := t.TempDir()
	reader := NewStore(root)
	writer := NewStore(root)

	reportHook(t, writer, "runtime-live", map[string]any{
		"session_id": "agent-live", "cwd": workspace,
		"hook_event_name": "UserPromptSubmit",
	})
	snapshot, ok := reader.SnapshotForWorkspace(workspace, []string{"runtime-live"})
	require.True(ok)
	require.Equal(StateWorking, snapshot.State)
	dirInfo, err := os.Stat(root)
	require.NoError(err)

	reportHook(t, writer, "runtime-live", map[string]any{
		"session_id": "agent-live", "cwd": workspace,
		"hook_event_name": "PermissionRequest",
	})
	require.NoError(os.Chtimes(root, dirInfo.ModTime(), dirInfo.ModTime()))
	snapshot, ok = reader.SnapshotForWorkspace(workspace, []string{"runtime-live"})
	require.True(ok)
	require.Equal(StateApproval, snapshot.State)
}

func TestHandleEventRecordsWorkingState(t *testing.T) {
	t.Parallel()
	store := NewStore(t.TempDir())
	worktree := t.TempDir()

	require.NoError(t, store.HandleEvent("codex", HookEvent{
		SessionID:     "agent-1",
		CWD:           worktree,
		HookEventName: "UserPromptSubmit",
	}, "runtime-1"))

	snapshot, ok := store.SnapshotForWorkspace(worktree, []string{"runtime-1"})
	require.True(t, ok)
	assert.Equal(t, StateWorking, snapshot.State)
}

func TestStoreKeysReportsByAgentAndCodingSession(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	workspace := t.TempDir()
	store := NewStore(t.TempDir())

	reportAgentHook(t, store, "codex", "runtime-codex", map[string]any{
		"session_id": "shared-session", "cwd": workspace,
		"hook_event_name": "UserPromptSubmit",
	})
	reportAgentHook(t, store, "claude", "runtime-claude", map[string]any{
		"session_id": "shared-session", "cwd": workspace,
		"hook_event_name": "Stop",
	})

	reports := store.LiveReportsForWorkspace(
		workspace, []string{"runtime-codex", "runtime-claude"},
	)
	require.Len(reports, 2)
	assert.Equal("claude", reports[0].Agent)
	assert.Equal(StateDone, reports[0].State)
	assert.Equal("codex", reports[1].Agent)
	assert.Equal(StateWorking, reports[1].State)

	reportAgentHook(t, store, "codex", "runtime-codex", map[string]any{
		"session_id": "shared-session", "cwd": workspace,
		"hook_event_name": "SessionEnd",
	})
	reports = store.LiveReportsForWorkspace(
		workspace, []string{"runtime-codex", "runtime-claude"},
	)
	require.Len(reports, 1)
	assert.Equal("claude", reports[0].Agent)
}

func TestStoreLiveReportsExcludeWrongWorkspaceDeadAndNestedSessions(t *testing.T) {
	assert := assert.New(t)
	workspace := t.TempDir()
	otherWorkspace := t.TempDir()
	store := NewStore(t.TempDir())

	reportAgentHook(t, store, "codex", "runtime-live", map[string]any{
		"session_id": "live", "cwd": workspace,
		"hook_event_name": "Interrupt",
	})
	reportAgentHook(t, store, "codex", "runtime-dead", map[string]any{
		"session_id": "dead", "cwd": workspace,
		"hook_event_name": "UserPromptSubmit",
	})
	reportAgentHook(t, store, "codex", "runtime-wrong-cwd", map[string]any{
		"session_id": "wrong-cwd", "cwd": otherWorkspace,
		"hook_event_name": "UserPromptSubmit",
	})
	reportAgentHook(t, store, "codex", "runtime-nested", map[string]any{
		"session_id": "nested", "agent_id": "child", "cwd": workspace,
		"hook_event_name": "UserPromptSubmit",
	})

	reports := store.LiveReportsForWorkspace(workspace, []string{
		"runtime-live", "runtime-wrong-cwd", "runtime-nested",
	})
	require.Len(t, reports, 1)
	assert.Equal("live", reports[0].SessionID)
	assert.Equal(StateDone, reports[0].State)
}

func TestStoreRemovesLegacyAgentlessReportsDuringScan(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	root := t.TempDir()
	workspace := t.TempDir()
	legacyPath := filepath.Join(root, "legacy.json")
	require.NoError(os.WriteFile(legacyPath, fmt.Appendf(nil,
		`{"session_id":"legacy","runtime_session_key":"runtime-legacy","cwd":%q,"state":"working","updated_at":"2026-08-07T12:00:00Z"}`,
		workspace,
	), 0o600))
	store := NewStore(root)
	store.now = func() time.Time {
		return time.Date(2026, 8, 7, 12, 1, 0, 0, time.UTC)
	}

	assert.Empty(store.LiveReportsForWorkspace(workspace, []string{"runtime-legacy"}))
	_, err := os.Stat(legacyPath)
	require.ErrorIs(err, os.ErrNotExist)
}

func reportHook(t *testing.T, store *Store, runtimeKey string, input map[string]any) {
	t.Helper()
	reportAgentHook(t, store, "codex", runtimeKey, input)
}

func reportAgentHook(
	t *testing.T,
	store *Store,
	agent string,
	runtimeKey string,
	input map[string]any,
) {
	t.Helper()
	data, err := json.Marshal(input)
	require.NoError(t, err)
	require.NoError(t, store.HandleHook(agent, strings.NewReader(string(data)), runtimeKey))
}

func TestStoreTreatsIdlePromptAsDone(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	store := NewStore(t.TempDir())
	workspace := t.TempDir()

	reportHook(t, store, "runtime-a", map[string]any{
		"session_id": "agent-a", "cwd": workspace,
		"hook_event_name": "Stop",
	})
	// Claude Code raises idle_prompt roughly a minute after Stop when the
	// turn ended with nothing pending; it must not turn done into input.
	reportHook(t, store, "runtime-a", map[string]any{
		"session_id": "agent-a", "cwd": workspace,
		"hook_event_name": "Notification", "notification_type": "idle_prompt",
	})
	snapshot, ok := store.SnapshotForWorkspace(workspace, []string{"runtime-a"})
	require.True(ok)
	assert.Equal(StateDone, snapshot.State)

	// A real question for the user still surfaces as input.
	reportHook(t, store, "runtime-a", map[string]any{
		"session_id": "agent-a", "cwd": workspace,
		"hook_event_name": "Notification", "notification_type": "elicitation_dialog",
	})
	snapshot, ok = store.SnapshotForWorkspace(workspace, []string{"runtime-a"})
	require.True(ok)
	assert.Equal(StateInput, snapshot.State)
}

func TestStoreKeepsDoneTimestampAcrossIdlePrompt(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	store := NewStore(t.TempDir())
	workspace := t.TempDir()
	now := time.Date(2026, 7, 27, 12, 0, 0, 0, time.UTC)
	store.now = func() time.Time { return now }

	reportHook(t, store, "runtime-a", map[string]any{
		"session_id": "agent-a", "cwd": workspace,
		"hook_event_name": "Stop",
	})
	stoppedAt := now
	now = now.Add(time.Minute)
	reportHook(t, store, "runtime-a", map[string]any{
		"session_id": "agent-a", "cwd": workspace,
		"hook_event_name": "Notification", "notification_type": "idle_prompt",
	})

	// The sidebar acknowledges a completion by its timestamp; a later
	// idle_prompt must not mint a new one.
	snapshot, ok := store.SnapshotForWorkspace(workspace, []string{"runtime-a"})
	require.True(ok)
	assert.Equal(StateDone, snapshot.State)
	assert.Equal(stoppedAt, snapshot.UpdatedAt)

	// A new turn resets the clock as before.
	now = now.Add(time.Minute)
	reportHook(t, store, "runtime-a", map[string]any{
		"session_id": "agent-a", "cwd": workspace,
		"hook_event_name": "UserPromptSubmit",
	})
	now = now.Add(time.Minute)
	reportHook(t, store, "runtime-a", map[string]any{
		"session_id": "agent-a", "cwd": workspace,
		"hook_event_name": "Stop",
	})
	snapshot, ok = store.SnapshotForWorkspace(workspace, []string{"runtime-a"})
	require.True(ok)
	assert.Equal(StateDone, snapshot.State)
	assert.Equal(now, snapshot.UpdatedAt)
}

func TestStoreIdlePromptKeepsPendingInputAndApproval(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	store := NewStore(t.TempDir())
	workspace := t.TempDir()

	for _, tc := range []struct {
		name  string
		event map[string]any
		want  State
	}{
		{"question", map[string]any{"hook_event_name": "PreToolUse", "tool_name": "AskUserQuestion"}, StateInput},
		{"permission", map[string]any{"hook_event_name": "PermissionRequest"}, StateApproval},
	} {
		event := map[string]any{"session_id": "agent-" + tc.name, "cwd": workspace}
		maps.Copy(event, tc.event)
		reportHook(t, store, "runtime-"+tc.name, event)
		// Claude Code raises idle_prompt after a minute of waiting for an
		// answer; the question is still pending.
		reportHook(t, store, "runtime-"+tc.name, map[string]any{
			"session_id": "agent-" + tc.name, "cwd": workspace,
			"hook_event_name": "Notification", "notification_type": "idle_prompt",
		})
		snapshot, ok := store.SnapshotForWorkspace(workspace, []string{"runtime-" + tc.name})
		require.True(ok, tc.name)
		assert.Equal(tc.want, snapshot.State, tc.name)
	}
}

func TestStoreRetainRuntimeSessionsRemovesOthers(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	root := t.TempDir()
	store := NewStore(root)
	workspace := t.TempDir()
	for _, key := range []string{"runtime-a", "runtime-b"} {
		reportHook(t, store, key, map[string]any{
			"session_id": "agent-" + key, "cwd": workspace,
			"hook_event_name": "UserPromptSubmit",
		})
	}

	require.NoError(store.RetainRuntimeSessions(map[string]struct{}{"runtime-a": {}}))

	entries, err := os.ReadDir(root)
	require.NoError(err)
	assert.Len(entries, 1)
	_, ok := store.SnapshotForWorkspace(workspace, []string{"runtime-a"})
	assert.True(ok)
	_, ok = store.SnapshotForWorkspace(workspace, []string{"runtime-b"})
	assert.False(ok, "the report of a runtime not in the keep set is removed")
}
