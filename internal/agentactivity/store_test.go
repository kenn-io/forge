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

func TestInstallLifecyclePreservesOtherHooks(t *testing.T) {
	tests := []struct {
		name        string
		integration Integration
		env         string
		configName  string
		agentArg    string
	}{
		{name: "Claude", integration: IntegrationClaude, env: "CLAUDE_CONFIG_DIR", configName: "settings.json", agentArg: "--agent claude"},
		{name: "Codex", integration: IntegrationCodex, env: "CODEX_HOME", configName: "hooks.json", agentArg: "--agent codex"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert := assert.New(t)
			require := require.New(t)
			configDir := t.TempDir()
			t.Setenv(tt.env, configDir)
			configPath := filepath.Join(configDir, tt.configName)
			require.NoError(os.WriteFile(configPath, []byte(`{
  "mode": "keep",
  "sequence": 9007199254740993,
  "hooks": {
    "Stop": [{"hooks": [{"type": "command", "command": "keep-me"}]}],
    "SessionStart": [{"hooks": [{"type": "command", "command": "old agent-hook --source middleman-agent-activity"}]}]
  }
}`), 0o600))

			for range 2 {
				result, err := Install(tt.integration, "/opt/middleman", "/tmp/middleman.toml")
				require.NoError(err)
				assert.Equal(configPath, result.ConfigPath)
			}

			data, err := os.ReadFile(configPath)
			require.NoError(err)
			assert.Contains(string(data), `"sequence": 9007199254740993`)
			var root map[string]any
			require.NoError(json.Unmarshal(data, &root))
			assert.Equal("keep", root["mode"])
			hooks := root["hooks"].(map[string]any)
			stopJSON, err := json.Marshal(hooks["Stop"])
			require.NoError(err)
			assert.Contains(string(stopJSON), "keep-me")
			markerHandlers := 0
			for _, rawEntry := range hooks["Stop"].([]any) {
				entry := rawEntry.(map[string]any)
				for _, rawHandler := range entry["hooks"].([]any) {
					handler := rawHandler.(map[string]any)
					command, _ := handler["command"].(string)
					if strings.Contains(command, hookCommandMarker) {
						markerHandlers++
					}
				}
			}
			assert.Equal(1, markerHandlers)
			startJSON, err := json.Marshal(hooks["SessionStart"])
			require.NoError(err)
			assert.NotContains(string(startJSON), "old agent-hook")
			assert.Contains(string(startJSON), "/opt/middleman")
			assert.Contains(string(startJSON), tt.agentArg)
			assert.Contains(string(startJSON), "--config /tmp/middleman.toml")
			assert.NotContains(string(startJSON), "--state-dir")

			_, err = Uninstall(tt.integration)
			require.NoError(err)
			data, err = os.ReadFile(configPath)
			require.NoError(err)
			assert.Contains(string(data), `"sequence": 9007199254740993`)
			require.NoError(json.Unmarshal(data, &root))
			assert.Equal("keep", root["mode"])
			hooks = root["hooks"].(map[string]any)
			stopJSON, err = json.Marshal(hooks["Stop"])
			require.NoError(err)
			assert.Contains(string(stopJSON), "keep-me")
			assert.NotContains(string(stopJSON), hookCommandMarker)
			startJSON, err = json.Marshal(hooks["SessionStart"])
			require.NoError(err)
			assert.NotContains(string(startJSON), hookCommandMarker)
		})
	}
}

func TestInstallLifecyclePreservesSymlinkedConfig(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink creation requires elevated privileges on Windows")
	}
	assert := assert.New(t)
	require := require.New(t)
	configDir := t.TempDir()
	t.Setenv("CODEX_HOME", configDir)
	targetPath := filepath.Join(t.TempDir(), "hooks.json")
	require.NoError(os.WriteFile(targetPath, []byte(`{
  "hooks": {"Stop": [{"hooks": [{"type": "command", "command": "keep-me"}]}]}
}`), 0o600))
	configPath := filepath.Join(configDir, "hooks.json")
	require.NoError(os.Symlink(targetPath, configPath))

	_, err := Install(IntegrationCodex, "/opt/middleman", "/tmp/middleman.toml")
	require.NoError(err)
	info, err := os.Lstat(configPath)
	require.NoError(err)
	assert.NotZero(info.Mode() & os.ModeSymlink)
	data, err := os.ReadFile(targetPath)
	require.NoError(err)
	assert.Contains(string(data), hookCommandMarker)

	_, err = Uninstall(IntegrationCodex)
	require.NoError(err)
	info, err = os.Lstat(configPath)
	require.NoError(err)
	assert.NotZero(info.Mode() & os.ModeSymlink)
	data, err = os.ReadFile(targetPath)
	require.NoError(err)
	assert.Contains(string(data), "keep-me")
	assert.NotContains(string(data), hookCommandMarker)
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
