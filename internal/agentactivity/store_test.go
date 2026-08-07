package agentactivity

import (
	"encoding/json"
	"fmt"
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

func TestStoreExpiresAndRemovesStaleReports(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	root := t.TempDir()
	workspace := t.TempDir()
	store := NewStore(root)
	now := time.Date(2026, 7, 27, 12, 0, 0, 0, time.UTC)
	store.now = func() time.Time { return now }

	reportHook(t, store, "runtime-stale", map[string]any{
		"session_id": "agent-stale", "cwd": workspace,
		"hook_event_name": "PermissionRequest",
	})
	now = now.Add(31 * time.Minute)

	_, ok := store.SnapshotForWorkspace(workspace, []string{"runtime-stale"})
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
