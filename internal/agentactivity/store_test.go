package agentactivity

import (
	"encoding/json"
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

	require.NoError(t, store.HandleEvent(HookEvent{
		SessionID:     "agent-1",
		CWD:           worktree,
		HookEventName: "UserPromptSubmit",
	}, "runtime-1"))

	snapshot, ok := store.SnapshotForWorkspace(worktree, []string{"runtime-1"})
	require.True(t, ok)
	assert.Equal(t, StateWorking, snapshot.State)
}

func reportHook(t *testing.T, store *Store, runtimeKey string, input map[string]any) {
	t.Helper()
	data, err := json.Marshal(input)
	require.NoError(t, err)
	require.NoError(t, store.HandleHook(strings.NewReader(string(data)), runtimeKey))
}
