package agentactivity

import (
	"crypto/sha256"
	"encoding/hex"
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
	for _, tc := range []struct {
		name                    string
		firstAgent, secondAgent string
		firstKey, secondKey     string
	}{
		{"agent", "codex", "claude", "shared-runtime", "shared-runtime"},
		{"runtime", "opencode", "opencode", "runtime-a", "runtime-b"},
	} {
		for _, source := range []string{"hook", "record"} {
			t.Run(tc.name+"/"+source, func(t *testing.T) {
				assert := assert.New(t)
				require := require.New(t)
				workspace := t.TempDir()
				store := NewStore(t.TempDir())
				now := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
				store.now = func() time.Time { return now }
				recordDone := func(agent, key string) {
					if source == "record" {
						require.NoError(store.Record(agent, "shared-session", key, workspace, StateDone))
						return
					}
					require.NoError(store.HandleEvent(agent, HookEvent{
						SessionID: "shared-session", CWD: workspace, HookEventName: "Stop",
					}, key))
				}

				recordDone(tc.firstAgent, tc.firstKey)
				firstCompletion := now
				now = now.Add(time.Minute)
				recordDone(tc.secondAgent, tc.secondKey)
				secondCompletion := now
				now = now.Add(time.Minute)
				recordDone(tc.firstAgent, tc.firstKey)

				liveKeys := []string{tc.firstKey, tc.secondKey}
				reports := store.LiveReportsForWorkspace(workspace, liveKeys)
				require.Len(reports, 2)
				assert.Equal(tc.secondAgent, reports[0].Agent)
				assert.Equal(tc.secondKey, reports[0].RuntimeSessionKey)
				assert.Equal(StateDone, reports[0].State)
				assert.Equal(secondCompletion, reports[0].UpdatedAt)
				assert.Equal(tc.firstAgent, reports[1].Agent)
				assert.Equal(tc.firstKey, reports[1].RuntimeSessionKey)
				assert.Equal(StateDone, reports[1].State)
				assert.Equal(firstCompletion, reports[1].UpdatedAt)

				if source == "hook" {
					survivor := reports[0]
					reportAgentHook(t, store, tc.firstAgent, tc.firstKey, map[string]any{
						"session_id": "shared-session", "cwd": workspace,
						"hook_event_name": "SessionEnd",
					})
					reports = store.LiveReportsForWorkspace(workspace, liveKeys)
					require.Len(reports, 1)
					assert.Equal(survivor, reports[0])
				}
			})
		}
	}
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

func TestStoreReadsReportsSavedBeforePerTerminalNames(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	root := t.TempDir()
	workspace := t.TempDir()
	sum := sha256.Sum256([]byte("claude\x00shared"))
	legacy := filepath.Join(root, hex.EncodeToString(sum[:])+".json")
	writeLegacy := func(state, updated string) {
		require.NoError(os.WriteFile(legacy, fmt.Appendf(nil,
			`{"agent":"claude","session_id":"shared","runtime_session_key":"runtime-a","cwd":%q,"state":%q,"updated_at":%q}`,
			workspace, state, updated,
		), 0o600))
	}
	s := NewStore(root)
	s.now = func() time.Time { return time.Date(2026, 8, 7, 12, 5, 0, 0, time.UTC) }
	state := func() State {
		reports := s.LiveReportsForWorkspace(workspace, []string{"runtime-a"})
		require.Len(reports, 1)
		return reports[0].State
	}

	writeLegacy("approval", "2026-08-07T12:00:00Z")
	assert.Equal(StateApproval, state())

	require.NoError(s.Record("claude", "shared", "runtime-a", workspace, StateDone))
	assert.Equal(StateDone, state())

	// An agent started before the upgrade still writes the old name.
	writeLegacy("approval", "2026-08-07T12:10:00Z")
	assert.Equal(StateApproval, state())

	// Claude Code's idle_prompt leaves a pending approval saved under the old name alone.
	reportAgentHook(t, s, "claude", "runtime-a", map[string]any{
		"session_id": "shared", "cwd": workspace,
		"hook_event_name": "Notification", "notification_type": "idle_prompt",
	})
	assert.Equal(StateApproval, state())

	writeLegacy("done", "2026-08-07T12:20:00Z")
	s.now = func() time.Time { return time.Date(2026, 8, 7, 12, 30, 0, 0, time.UTC) }
	require.NoError(s.Record("claude", "shared", "runtime-a", workspace, StateDone))
	reports := s.LiveReportsForWorkspace(workspace, []string{"runtime-a"})
	require.Len(reports, 1)
	assert.Equal(time.Date(2026, 8, 7, 12, 20, 0, 0, time.UTC), reports[0].UpdatedAt)

	require.NoError(s.Remove("claude", "shared", "runtime-a"))
	entries, err := os.ReadDir(root)
	require.NoError(err)
	assert.Empty(entries)
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

func TestRecordKeepsFirstCompletionAndRemovesSession(t *testing.T) {
	store := NewStore(t.TempDir())
	cwd := t.TempDir()
	now := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
	store.now = func() time.Time { return now }
	require.NoError(t, store.Record("acp", "chat", "runtime", cwd, StateDone))
	now = now.Add(time.Minute)
	require.NoError(t, store.Record("acp", "chat", "runtime", cwd, StateDone))
	reports := store.LiveReportsForWorkspace(cwd, []string{"runtime"})
	require.Len(t, reports, 1)
	assert.Equal(t, time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC), reports[0].UpdatedAt)

	require.NoError(t, store.Record("acp", "chat", "runtime", cwd, StateWorking))
	require.NoError(t, store.Record("acp", "chat", "runtime", cwd, StateDone))
	reports = store.LiveReportsForWorkspace(cwd, []string{"runtime"})
	require.Len(t, reports, 1)
	assert.Equal(t, now, reports[0].UpdatedAt, "a new turn completes as new")
	require.Error(t, store.Record("acp", "chat", "runtime", cwd, "unknown"))

	require.NoError(t, store.Record("acp", "chat", "other-runtime", cwd, StateWorking))
	require.NoError(t, store.Remove("acp", "chat", "runtime"))
	assert.Empty(t, store.LiveReportsForWorkspace(cwd, []string{"runtime"}))
	reports = store.LiveReportsForWorkspace(cwd, []string{"other-runtime"})
	require.Len(t, reports, 1)
	assert.Equal(t, StateWorking, reports[0].State)
}

func TestStoreRemoveReturnsLegacyReadError(t *testing.T) {
	t.Parallel()
	store := NewStore(t.TempDir())
	legacy := store.legacyReportPath("claude", "chat")
	require.NoError(t, os.Mkdir(legacy, 0o700))
	require.NoError(t, store.Record("claude", "chat", "runtime", t.TempDir(), StateWorking))

	require.ErrorContains(t, store.Remove("claude", "chat", "runtime"), legacy)
	assert.DirExists(t, legacy)
	assert.NoFileExists(t, store.reportPath("claude", "chat", "runtime"))
}

func TestStoreRemoveReturnsCurrentRemovalError(t *testing.T) {
	t.Parallel()
	store := NewStore(t.TempDir())
	path := store.reportPath("claude", "chat", "runtime")
	require.NoError(t, os.Mkdir(path, 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(path, "child"), nil, 0o600))
	require.ErrorContains(t, store.Remove("claude", "chat", "runtime"), path)
}

func TestStoreRemoveIgnoresCorruptLegacyReport(t *testing.T) {
	t.Parallel()
	for _, content := range []string{"", "invalid", `{"state":`} {
		t.Run(content, func(t *testing.T) {
			store := NewStore(t.TempDir())
			legacy := store.legacyReportPath("claude", "chat")
			require.NoError(t, os.WriteFile(legacy, []byte(content), 0o600))
			require.NoError(t, store.Record("claude", "chat", "runtime", t.TempDir(), StateWorking))

			require.NoError(t, store.Remove("claude", "chat", "runtime"))
			assert.NoFileExists(t, store.reportPath("claude", "chat", "runtime"))
		})
	}
}
