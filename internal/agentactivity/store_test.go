package agentactivity

import (
	"encoding/json"
	"os"
	"path/filepath"
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
	assert.Equal(StateIdle, snapshot.State)

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

func TestInstallPreservesOtherHooksAndReplacesMiddlemanHooks(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	configDir := t.TempDir()
	t.Setenv("CODEX_HOME", configDir)
	configPath := filepath.Join(configDir, "hooks.json")
	require.NoError(os.WriteFile(configPath, []byte(`{
  "hooks": {
    "Stop": [{"hooks": [{"type": "command", "command": "keep-me"}]}],
    "SessionStart": [{"hooks": [{"type": "command", "command": "old agent-hook --source middleman-agent-activity"}]}]
  }
}`), 0o600))

	result, err := Install(IntegrationCodex, "/opt/middleman", "/tmp/activity")
	require.NoError(err)
	assert.Equal(configPath, result.ConfigPath)

	data, err := os.ReadFile(configPath)
	require.NoError(err)
	var root map[string]any
	require.NoError(json.Unmarshal(data, &root))
	hooks := root["hooks"].(map[string]any)
	stopJSON, err := json.Marshal(hooks["Stop"])
	require.NoError(err)
	assert.Contains(string(stopJSON), "keep-me")
	assert.Contains(string(stopJSON), hookCommandMarker)
	startJSON, err := json.Marshal(hooks["SessionStart"])
	require.NoError(err)
	assert.NotContains(string(startJSON), "old agent-hook")
	assert.Contains(string(startJSON), "/opt/middleman")
	assert.Contains(string(startJSON), "agent-hook run")
}

func reportHook(t *testing.T, store *Store, runtimeKey string, input map[string]any) {
	t.Helper()
	data, err := json.Marshal(input)
	require.NoError(t, err)
	require.NoError(t, store.HandleHook(strings.NewReader(string(data)), runtimeKey))
}
