package localruntime

import (
	"encoding/json/v2"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"go.kenn.io/forge/internal/agentactivity"
	"go.kenn.io/forge/internal/config"
	"go.kenn.io/forge/internal/ptyowner"
)

const testACPIdleTimeout = 300 * time.Millisecond

// idleACPChat launches a chat whose owner parks it after testACPIdleTimeout
// and returns the manager, the chat, its fixture directory, and its exits.
func idleACPChat(t *testing.T, noLoad bool) (*Manager, SessionInfo, string, chan SessionInfo) {
	t.Helper()
	t.Setenv("KENN_FORGE_LOCALRUNTIME_HELPER", "1")
	t.Setenv("KENN_FORGE_ACP_FIXTURE", "1")
	if noLoad {
		t.Setenv("KENN_FORGE_ACP_NO_LOAD", "1")
	}
	dir := t.TempDir()
	t.Setenv("KENN_FORGE_ACP_CONTINUE_DIR", dir)
	executable, err := os.Executable()
	require.NoError(t, err)
	exits := make(chan SessionInfo, 4)
	manager := newACPTestManager(t, Options{
		ACPSessionsDir:   filepath.Join(dir, "acp"),
		AgentActivityDir: filepath.Join(dir, "activity"),
		ACPIdleTimeout:   testACPIdleTimeout,
		OnSessionExit:    func(info SessionInfo) { exits <- info },
		Targets:          ResolveLaunchTargets([]config.Agent{{Key: "chat", Protocol: "acp", Command: []string{executable, "-test.run=^TestACPStdioHelper$"}}}, nil, nil),
	})
	info, err := manager.Launch(t.Context(), "workspace", dir, "chat")
	require.NoError(t, err)
	return manager, info, dir, exits
}

func agentPID(t *testing.T, dir string) int {
	t.Helper()
	pidText, err := os.ReadFile(filepath.Join(dir, "pid"))
	require.NoError(t, err)
	pid, err := strconv.Atoi(string(pidText))
	require.NoError(t, err)
	return pid
}

func acpChatState(t *testing.T, chat ACPChat) ACPState {
	t.Helper()
	data, err := chat.Snapshot()
	require.NoError(t, err)
	var state ACPState
	require.NoError(t, json.Unmarshal(data, &state))
	return state
}

// An idle chat nobody views parks itself: the agent stops, the chat is not an
// exit, and restoring it loads the saved session with its transcript.
func TestACPOwnerParksIdleUnviewedChat(t *testing.T) {
	manager, info, dir, exits := idleACPChat(t, false)
	chat, err := manager.ACP("workspace", info.Key)
	require.NoError(t, err)
	// A browser viewing the chat keeps it running however long it idles.
	_, unsubscribe := chat.Subscribe()
	require.NoError(t, chat.Command(ACPCommand{Type: "prompt", Text: "remember", ID: "kept"}))
	require.Eventually(t, func() bool { state := acpChatState(t, chat); return !state.Busy && len(state.Messages) == 2 }, 5*time.Second, 10*time.Millisecond)
	pid := agentPID(t, dir)
	time.Sleep(4 * testACPIdleTimeout) //nolint:kennlint // the owner must stay up for several idle timeouts while viewed
	_, err = manager.ACP("workspace", info.Key)
	require.NoError(t, err, "a viewed chat must keep running")
	require.True(t, processAlive(pid))
	unsubscribe()

	var parked SessionInfo
	require.Eventually(t, func() bool {
		select {
		case parked = <-exits:
			return true
		default:
			return false
		}
	}, 10*time.Second, 10*time.Millisecond, "the idle chat was not parked")
	assert := assert.New(t)
	assert.Equal(SessionStatusParked, parked.Status)
	assert.False(manager.Exited(info.Key), "a parked chat must stay restorable")
	assert.Empty(manager.ListSessions("workspace"))
	require.Eventually(t, func() bool { return !processAlive(pid) }, 5*time.Second, 10*time.Millisecond, "parking must stop the agent")
	reports := agentactivity.NewStore(filepath.Join(dir, "activity")).LiveReportsForWorkspace(dir, []string{info.Key})
	require.Len(t, reports, 1, "a parked chat keeps its activity report")
	assert.Equal(agentactivity.StateDone, reports[0].State)

	// Restoring waits until the parked owner's backend is gone.
	require.Eventually(t, func() bool {
		return manager.RestoreRuntimeSessions(t.Context(), []RestoredRuntimeSession{{WorkspaceID: "workspace", SessionKey: info.Key, TargetKey: "chat", Kind: LaunchTargetACP, CWD: dir, CreatedAt: info.CreatedAt}}) == nil
	}, 10*time.Second, 50*time.Millisecond)
	resumed, err := manager.ACP("workspace", info.Key)
	require.NoError(t, err)
	state := acpChatState(t, resumed)
	require.Len(t, state.Messages, 2)
	assert.Equal("kept", state.Messages[0].SubmissionID)
	assert.True(state.Connected)
	sessions, err := os.ReadFile(filepath.Join(dir, "sessions"))
	require.NoError(t, err)
	assert.Equal("session/new\nsession/load\n", string(sessions))
}

// Chats that cannot be parked without losing something keep running: an
// agent without session loading, and a turn waiting for a permission.
func TestACPOwnerKeepsChatsThatCannotPark(t *testing.T) {
	for _, tc := range []struct {
		name   string
		noLoad bool
		prompt string
	}{
		{name: "no session load", noLoad: true, prompt: "remember"},
		{name: "pending permission", prompt: "permission"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			manager, info, dir, exits := idleACPChat(t, tc.noLoad)
			chat, err := manager.ACP("workspace", info.Key)
			require.NoError(t, err)
			require.NoError(t, chat.Command(ACPCommand{Type: "prompt", Text: tc.prompt}))
			time.Sleep(4 * testACPIdleTimeout) //nolint:kennlint // the owner must stay up for several idle timeouts
			assert.Empty(t, exits, "the chat must keep running")
			require.True(t, processAlive(agentPID(t, dir)))
		})
	}
}

// An owner whose state directory was removed can never be reached again, so
// it stops its agent even while a turn waits.
func TestACPOwnerStopsWhenItsStateIsRemoved(t *testing.T) {
	manager, info, dir, _ := idleACPChat(t, false)
	chat, err := manager.ACP("workspace", info.Key)
	require.NoError(t, err)
	_, unsubscribe := chat.Subscribe()
	defer unsubscribe()
	require.NoError(t, chat.Command(ACPCommand{Type: "prompt", Text: "permission"}))
	require.Eventually(t, func() bool { return len(acpChatState(t, chat).Permissions) == 1 }, 5*time.Second, 10*time.Millisecond)
	pid := agentPID(t, dir)
	paths, err := ptyowner.NewSessionPaths(filepath.Join(dir, "acp"), info.Key)
	require.NoError(t, err)
	// The owner can be saving into the directory while it is removed.
	require.Eventually(t, func() bool { return os.RemoveAll(paths.Dir) == nil }, 5*time.Second, 10*time.Millisecond)
	require.Eventually(t, func() bool { return !processAlive(pid) }, 10*time.Second, 10*time.Millisecond)
}

// Parking disconnects the chat under the turn lock, so a prompt that arrives
// afterwards is refused unsent rather than accepted and lost.
func TestACPParkRefusesLaterPrompts(t *testing.T) {
	record := filepath.Join(t.TempDir(), "session.json")
	agent := &ACP{state: ACPState{Connected: true}, loadSession: true, recordPath: record, subscribers: make(map[chan struct{}]struct{})}
	now := time.Now()
	agent.usedAt = now.Add(-time.Minute)
	assert.False(t, agent.parkIfIdle(now, 2*time.Minute), "a recently used chat must not park")
	require.True(t, agent.parkIfIdle(now, time.Minute))
	assert.FileExists(t, record, "parking saves the session first")
	assert.ErrorIs(t, agent.Command(ACPCommand{Type: "prompt", Text: "late", ID: "late"}), ErrACPAgentUnavailable)
}
