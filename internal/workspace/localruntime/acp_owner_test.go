package localruntime

import (
	"bytes"
	"context"
	"encoding/json/v2"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/forge/internal/config"
	"go.kenn.io/forge/internal/ptyowner"
	ptyownerruntime "go.kenn.io/forge/internal/ptyowner/runtime"
)

func TestStopDormantACPStopsChatWhoseOwnerDied(t *testing.T) {
	require := require.New(t)
	manager := newACPTestManager(t, Options{ACPSessionsDir: t.TempDir()})
	paths, err := ptyowner.NewSessionPaths(manager.acpSessionsDir, "chat")
	require.NoError(err)
	require.NoError(os.MkdirAll(paths.Dir, 0o700))
	data, err := json.Marshal(acpOwnerConfig{Info: SessionInfo{WorkspaceID: "workspace", Key: "chat"}})
	require.NoError(err)
	require.NoError(os.WriteFile(filepath.Join(paths.Dir, "config.json"), data, 0o600))
	require.NoError(os.WriteFile(manager.acpSessionPath("chat"), []byte("{}"), 0o600))
	require.NoError(os.MkdirAll(filepath.Dir(paths.Socket), 0o700))
	if paths.SocketDir != "" {
		t.Cleanup(func() { _ = os.RemoveAll(paths.SocketDir) })
	}
	listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: paths.Socket, Net: "unix"})
	require.NoError(err)
	listener.SetUnlinkOnClose(false)
	require.NoError(listener.Close())

	require.NoError(manager.StopDormantACP(t.Context(), "workspace", "chat"))
	assert.NoFileExists(t, manager.acpSessionPath("chat"))
}

func TestACPOwnerSurvivesDaemonShutdown(t *testing.T) {
	for _, backend := range []string{"ptyowner", "tmux"} {
		t.Run(backend, func(t *testing.T) {
			require := require.New(t)
			assert := assert.New(t)
			t.Setenv("KENN_FORGE_LOCALRUNTIME_HELPER", "1")
			t.Setenv("KENN_FORGE_ACP_FIXTURE", "1")
			dir := t.TempDir()
			t.Setenv("KENN_FORGE_ACP_CONTINUE_DIR", dir)
			executable, err := os.Executable()
			require.NoError(err)
			options := Options{ACPSessionsDir: filepath.Join(dir, "acp"), ACPPreferencesPath: filepath.Join(dir, "preferences.json")}
			if backend == "tmux" {
				if privateTmuxOwner == nil {
					t.Skip("private tmux unavailable")
				}
				tmux, err := exec.LookPath("tmux")
				require.NoError(err)
				options.TmuxCommand = privateTmuxOwner.Command(t, tmux)
			}
			options.Targets = ResolveLaunchTargets([]config.Agent{{Key: "chat", Protocol: "acp", Command: []string{executable, "-test.run=^TestACPStdioHelper$"}}}, options.TmuxCommand, nil)
			options = withTestPtyOwnerRuntime(t, options)
			first := newACPTestManager(t, options)
			info, err := first.Launch(t.Context(), "workspace", dir, "chat")
			require.NoError(err)
			if backend == "tmux" {
				// Keep the private server alive after its only ACP owner exits.
				// This test checks agent lifetime, not tmux's exit-empty race
				// with Stop's subsequent kill-session cleanup.
				command := options.TmuxCommand
				cmd := exec.CommandContext(t.Context(), command[0], append(slices.Clone(command[1:]), "set-option", "-s", "exit-empty", "off")...)
				output, err := cmd.CombinedOutput()
				require.NoError(err, string(output))
			}
			agent, err := first.ACP("workspace", info.Key)
			require.NoError(err)
			require.NoError(agent.Command(ACPCommand{Type: "config", ID: "model", Value: "deep"}))
			require.NoError(agent.Command(ACPCommand{Type: "prompt", Text: "permission", ID: "accepted"}))
			var state ACPState
			require.Eventually(func() bool {
				data, err := agent.Snapshot()
				return err == nil && json.Unmarshal(data, &state) == nil && len(state.Permissions) == 1
			}, 5*time.Second, 10*time.Millisecond)
			permission := state.Permissions[0]
			pidText, err := os.ReadFile(filepath.Join(dir, "pid"))
			require.NoError(err)
			pid, err := strconv.Atoi(string(pidText))
			require.NoError(err)
			process, err := os.FindProcess(pid)
			require.NoError(err)

			first.Shutdown()
			require.NoError(process.Signal(syscall.Signal(0)), "daemon shutdown must leave the agent alive")
			// The peer produces new output while no daemon is attached.
			require.NoError(os.WriteFile(filepath.Join(dir, "continue"), nil, 0o600))
			require.Eventually(func() bool { _, err := os.Stat(filepath.Join(dir, "continued")); return err == nil }, 5*time.Second, 10*time.Millisecond)
			second := newACPTestManager(t, options)
			assert.Empty(second.ListSessions("workspace"), "constructing the daemon must not attach ACP")
			require.NoError(second.RestoreRuntimeSessions(t.Context(), []RestoredRuntimeSession{{WorkspaceID: "workspace", SessionKey: info.Key, TargetKey: "chat", Kind: LaunchTargetACP, TmuxSession: info.TmuxSession, CWD: dir, CreatedAt: info.CreatedAt}}))
			reattached, err := second.ACP("workspace", info.Key)
			require.NoError(err)
			var data []byte
			require.Eventually(func() bool {
				var err error
				data, err = reattached.Snapshot()
				return err == nil && json.Unmarshal(data, &state) == nil && bytes.Contains(data, []byte("output while detached"))
			}, 5*time.Second, 10*time.Millisecond)
			assert.True(state.Busy)
			assert.Equal([]ACPPermission{permission}, state.Permissions)
			assert.Equal("deep", acpOptionValue(t, reattached, "model"))
			assert.Contains(string(data), "output while detached")
			assert.Equal("accepted", state.Messages[0].SubmissionID)
			require.NoError(reattached.Command(ACPCommand{Type: "prompt", Text: "permission", ID: "accepted"}), "retry must acknowledge the existing turn")
			require.NoError(reattached.Command(ACPCommand{Type: "permission", ID: permission.ID, OptionID: "allow"}))
			require.Eventually(func() bool {
				data, err := reattached.Snapshot()
				return err == nil && json.Unmarshal(data, &state) == nil && !state.Busy && len(state.Permissions) == 0
			}, 5*time.Second, 10*time.Millisecond)
			require.NoError(process.Signal(syscall.Signal(0)), "permission must complete in the original process")
			require.NoError(second.Stop(t.Context(), "workspace", info.Key))
			if backend == "tmux" {
				require.ErrorIs(second.requireTmuxSession(t.Context(), info.TmuxSession), ErrSessionNotFound)
			}
			require.Eventually(func() bool { return process.Signal(syscall.Signal(0)) != nil }, 5*time.Second, 10*time.Millisecond, "explicit stop must stop the agent")
		})
	}
}

// The saved transcript is the conversation of record after a reload. The
// agent's history replay is dropped, and an agent that cannot load sessions
// continues the conversation in a new session instead of failing to start.
func TestACPReloadsSavedSessionOnlyAfterOwnerExit(t *testing.T) {
	for _, tc := range []struct {
		name       string
		noLoad     string
		sessions   string
		noticeText string
	}{
		{name: "load", sessions: "session/new\nsession/load\n"},
		{name: "no load capability", noLoad: "1", sessions: "session/new\nsession/new\n", noticeText: "cannot reload its previous session"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("KENN_FORGE_ACP_NO_LOAD", tc.noLoad)
			testACPReloadsSavedSession(t, tc.sessions, tc.noticeText)
		})
	}
}

func testACPReloadsSavedSession(t *testing.T, sessions, noticeText string) {
	t.Setenv("KENN_FORGE_LOCALRUNTIME_HELPER", "1")
	t.Setenv("KENN_FORGE_ACP_FIXTURE", "1")
	dir := t.TempDir()
	t.Setenv("KENN_FORGE_ACP_CONTINUE_DIR", dir)
	executable, err := os.Executable()
	require.NoError(t, err)
	owner := &ptyowner.Client{Root: filepath.Join(t.TempDir(), "pty-owner"), InProcess: true}
	options := Options{ACPSessionsDir: filepath.Join(dir, "acp"), PtyOwnerRuntime: ptyownerruntime.New(owner, nil), Targets: ResolveLaunchTargets([]config.Agent{{Key: "chat", Protocol: "acp", Command: []string{executable, "-test.run=^TestACPStdioHelper$"}}}, nil, nil)}
	first := newACPTestManager(t, options)
	info, err := first.Launch(t.Context(), "workspace", dir, "chat")
	require.NoError(t, err)
	agent, err := first.ACP("workspace", info.Key)
	require.NoError(t, err)
	require.NoError(t, agent.Command(ACPCommand{Type: "prompt", Text: "remember", ID: "saved-submission"}))
	// The owner saves the transcript when the turn ends.
	require.Eventually(t, func() bool {
		data, err := agent.Snapshot()
		var state ACPState
		return err == nil && json.Unmarshal(data, &state) == nil && !state.Busy && len(state.Messages) == 2
	}, 5*time.Second, 10*time.Millisecond)
	first.Shutdown()
	pidText, err := os.ReadFile(filepath.Join(dir, "pid"))
	require.NoError(t, err)
	pid, err := strconv.Atoi(string(pidText))
	require.NoError(t, err)
	process, err := os.FindProcess(pid)
	require.NoError(t, err)
	require.NoError(t, process.Kill())
	require.Eventually(t, func() bool { return !owner.HasState(info.Key) }, 10*time.Second, 10*time.Millisecond)
	second := newACPTestManager(t, options)
	require.NoError(t, second.RestoreRuntimeSessions(t.Context(), []RestoredRuntimeSession{{WorkspaceID: "workspace", SessionKey: info.Key, TargetKey: "chat", Kind: LaunchTargetACP, CWD: dir, CreatedAt: info.CreatedAt}}))
	resumed, err := second.ACP("workspace", info.Key)
	require.NoError(t, err)
	data, err := resumed.Snapshot()
	require.NoError(t, err)
	assert := assert.New(t)
	var state ACPState
	require.NoError(t, json.Unmarshal(data, &state))
	require.Len(t, state.Messages, 2, "the replay must not duplicate the saved transcript")
	assert.Equal("saved-submission", state.Messages[0].SubmissionID)
	assert.Equal("remember", state.Messages[0].Text)
	assert.Equal("Hello workspace", state.Messages[1].Text)
	assert.NotContains(string(data), "restored answer")
	// A new session in place of the saved one is a notice, not an error.
	assert.Empty(state.Error)
	if noticeText == "" {
		assert.Empty(state.Notices)
		// Commands the agent advertises during the reload are current state,
		// not replayed history.
		assert.Equal([]ACPCommandInfo{{Name: "review", Description: "Review changes"}}, state.Commands)
	} else {
		require.Len(t, state.Notices, 1)
		assert.Contains(state.Notices[0], noticeText)
	}
	log, err := os.ReadFile(filepath.Join(dir, "sessions"))
	require.NoError(t, err)
	assert.Equal(sessions, string(log))
	require.NoError(t, resumed.Command(ACPCommand{Type: "prompt", Text: "remember", ID: "saved-submission"}))
	require.NoError(t, second.Detach("workspace", info.Key))
	require.NoError(t, second.StopDormantACP(t.Context(), "workspace", info.Key))
	require.Eventually(t, func() bool { _, err := second.ACP("workspace", info.Key); return err != nil }, time.Second, 10*time.Millisecond)
}

// This helper is used by the subprocess restart test. It exits the daemon
// process itself, leaving its tmux-owned ACP connection running.
func TestACPDaemonHelper(t *testing.T) {
	if os.Getenv("KENN_FORGE_ACP_DAEMON_FIXTURE") != "1" {
		return
	}
	var cfg struct {
		Root string
		Tmux []string
	}
	if err := json.Unmarshal([]byte(os.Getenv("KENN_FORGE_ACP_DAEMON_CONFIG")), &cfg); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	executable, _ := os.Executable()
	manager := NewManager(Options{ACPSessionsDir: filepath.Join(cfg.Root, "acp"), ACPOwnerCommand: []string{executable, "-test.run=^TestACPOwnerHelper$", "--"}, TmuxCommand: cfg.Tmux, Targets: ResolveLaunchTargets([]config.Agent{{Key: "chat", Protocol: "acp", Command: []string{executable, "-test.run=^TestACPStdioHelper$"}}}, cfg.Tmux, nil)})
	info, err := manager.Launch(context.Background(), "workspace", cfg.Root, "chat")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	agent, _ := manager.ACP("workspace", info.Key)
	if err := agent.Command(ACPCommand{Type: "prompt", Text: "permission", ID: "before-exit"}); err != nil {
		os.Exit(2)
	}
	require.Eventually(t, func() bool {
		data, err := agent.Snapshot()
		var state ACPState
		return err == nil && json.Unmarshal(data, &state) == nil && len(state.Permissions) == 1
	}, 5*time.Second, 10*time.Millisecond)
	manager.Shutdown()
	data, _ := json.Marshal(info)
	if err := os.WriteFile(filepath.Join(cfg.Root, "runtime.json"), data, 0o600); err != nil {
		os.Exit(3)
	}
	os.Exit(0)
}

func TestACPReattachesAfterDaemonProcessExits(t *testing.T) {
	if privateTmuxOwner == nil {
		t.Skip("private tmux unavailable")
	}
	t.Setenv("KENN_FORGE_LOCALRUNTIME_HELPER", "1")
	t.Setenv("KENN_FORGE_ACP_FIXTURE", "1")
	t.Setenv("KENN_FORGE_ACP_OWNER_FIXTURE", "1")
	t.Setenv("KENN_FORGE_ACP_DAEMON_FIXTURE", "1")
	executable, err := os.Executable()
	require.NoError(t, err)
	tmux, err := exec.LookPath("tmux")
	require.NoError(t, err)
	command := privateTmuxOwner.Command(t, tmux)
	dir := t.TempDir()
	t.Setenv("KENN_FORGE_ACP_CONTINUE_DIR", dir)
	cfg, err := json.Marshal(struct {
		Root string
		Tmux []string
	}{dir, command})
	require.NoError(t, err)
	t.Setenv("KENN_FORGE_ACP_DAEMON_CONFIG", string(cfg))
	options := Options{ACPSessionsDir: filepath.Join(dir, "acp"), TmuxCommand: command}
	second := newACPTestManager(t, options)
	helper := exec.CommandContext(t.Context(), executable, "-test.run=^TestACPDaemonHelper$")
	output, err := helper.CombinedOutput()
	require.NoError(t, err, string(output))
	data, err := os.ReadFile(filepath.Join(dir, "runtime.json"))
	require.NoError(t, err)
	var info SessionInfo
	require.NoError(t, json.Unmarshal(data, &info))
	require.NoError(t, second.RestoreRuntimeSessions(t.Context(), []RestoredRuntimeSession{{WorkspaceID: info.WorkspaceID, SessionKey: info.Key, TargetKey: info.TargetKey, Kind: info.Kind, TmuxSession: info.TmuxSession, CWD: dir, CreatedAt: info.CreatedAt}}))
	agent, err := second.ACP("workspace", info.Key)
	require.NoError(t, err)
	data, err = agent.Snapshot()
	require.NoError(t, err)
	assert := assert.New(t)
	assert.Contains(string(data), "before-exit")
	assert.Contains(string(data), "Allow once")
	sessions, err := os.ReadFile(filepath.Join(dir, "sessions"))
	require.NoError(t, err)
	assert.Equal("session/new\n", string(sessions))
	assert.NotContains(string(data), "restored answer")
}

// An agent this daemon saw exit on its own is finished: a workspace open that
// lands before the stored record is forgotten must not relaunch it. (An agent
// that dies while no daemon is attached is still restored; see
// TestACPReloadsSavedSessionOnlyAfterOwnerExit.)
func TestACPDoesNotRestoreAnAgentThatExited(t *testing.T) {
	t.Setenv("KENN_FORGE_LOCALRUNTIME_HELPER", "1")
	t.Setenv("KENN_FORGE_ACP_FIXTURE", "1")
	t.Setenv("KENN_FORGE_ACP_EXIT_ON_PROMPT", "7")
	dir := t.TempDir()
	executable, err := os.Executable()
	require.NoError(t, err)
	exits := make(chan SessionInfo, 1)
	owner := &ptyowner.Client{Root: filepath.Join(t.TempDir(), "pty-owner"), InProcess: true}
	options := Options{
		ACPSessionsDir:  filepath.Join(dir, "acp"),
		PtyOwnerRuntime: ptyownerruntime.New(owner, nil),
		Targets:         ResolveLaunchTargets([]config.Agent{{Key: "chat", Protocol: "acp", Command: []string{executable, "-test.run=^TestACPStdioHelper$"}}}, nil, nil),
		OnSessionExit:   func(info SessionInfo) { exits <- info },
	}
	manager := newACPTestManager(t, options)
	info, err := manager.Launch(t.Context(), "workspace", dir, "chat")
	require.NoError(t, err)
	agent, err := manager.ACP("workspace", info.Key)
	require.NoError(t, err)
	require.NoError(t, agent.Command(ACPCommand{Type: "prompt", Text: "exit"}))
	select {
	case <-exits:
	case <-time.After(5 * time.Second):
		require.FailNow(t, "ACP exit was not reported")
	}
	assert.True(t, manager.Exited(info.Key))
	// The owner is fully gone, as when the reopen comes a moment later.
	require.Eventually(t, func() bool { return !owner.HasState(info.Key) }, 10*time.Second, 10*time.Millisecond)

	err = manager.RestoreRuntimeSessions(t.Context(), []RestoredRuntimeSession{{WorkspaceID: "workspace", SessionKey: info.Key, TargetKey: "chat", Kind: LaunchTargetACP, TmuxSession: info.TmuxSession, CWD: dir, CreatedAt: info.CreatedAt}})
	require.ErrorIs(t, err, ErrSessionUnavailable)
	_, err = manager.ACP("workspace", info.Key)
	assert.Error(t, err, "the exited chat was relaunched")
}

// exitedACPChat launches a chat whose agent exits on its first prompt while
// this manager watches, optionally under supervision. The owner is fully gone
// when it returns.
func exitedACPChat(t *testing.T, supervised bool) (*Manager, SessionInfo, ACPState, string) {
	t.Helper()
	t.Setenv("KENN_FORGE_LOCALRUNTIME_HELPER", "1")
	t.Setenv("KENN_FORGE_ACP_FIXTURE", "1")
	t.Setenv("KENN_FORGE_ACP_EXIT_ON_PROMPT", "7")
	dir := t.TempDir()
	t.Setenv("KENN_FORGE_ACP_CONTINUE_DIR", dir)
	executable, err := os.Executable()
	require.NoError(t, err)
	exits := make(chan SessionInfo, 1)
	owner := &ptyowner.Client{Root: filepath.Join(t.TempDir(), "pty-owner"), InProcess: true}
	manager := newACPTestManager(t, Options{
		ACPSessionsDir:  filepath.Join(dir, "acp"),
		PtyOwnerRuntime: ptyownerruntime.New(owner, nil),
		Targets:         ResolveLaunchTargets([]config.Agent{{Key: "chat", Protocol: "acp", Command: []string{executable, "-test.run=^TestACPStdioHelper$"}}}, nil, nil),
		OnSessionExit:   func(info SessionInfo) { exits <- info },
	})
	info, err := manager.Launch(t.Context(), "workspace", dir, "chat")
	require.NoError(t, err)
	agent, err := manager.ACP("workspace", info.Key)
	require.NoError(t, err)
	prompt := ACPCommand{Type: "prompt", Text: "exit", ID: "last"}
	if supervised {
		require.NoError(t, agent.Command(ACPCommand{Type: "supervise", Supervisor: "coordinator"}))
		prompt.Generation = 1
	}
	data, err := agent.Snapshot()
	require.NoError(t, err)
	var state ACPState
	require.NoError(t, json.Unmarshal(data, &state))
	require.NoError(t, agent.Command(prompt))
	select {
	case <-exits:
	case <-time.After(5 * time.Second):
		require.FailNow(t, "ACP exit was not reported")
	}
	require.Eventually(t, func() bool { return !owner.HasState(info.Key) }, 10*time.Second, 10*time.Millisecond)
	return manager, info, state, dir
}

// storedACPChat is the daemon's stored record of a launched chat.
func storedACPChat(info SessionInfo, dir string) RestoredRuntimeSession {
	return RestoredRuntimeSession{WorkspaceID: "workspace", SessionKey: info.Key, TargetKey: "chat", Label: "Coordinator task", Kind: LaunchTargetACP, TmuxSession: info.TmuxSession, CWD: dir, CreatedAt: info.CreatedAt}
}

func restoreACPChat(manager *Manager, info SessionInfo, dir string) error {
	return manager.RestoreRuntimeSessions(context.Background(), []RestoredRuntimeSession{storedACPChat(info, dir)})
}

func savedACPSession(t *testing.T, manager *Manager, key string) acpSavedSession {
	t.Helper()
	data, err := os.ReadFile(manager.acpSessionPath(key))
	require.NoError(t, err)
	var saved acpSavedSession
	require.NoError(t, json.Unmarshal(data, &saved))
	return saved
}

// A supervised chat is the exception to the exited-session rule: its
// coordinator may restore it after its agent exits on its own, and it resumes
// the same native session in a new agent process.
func TestACPRestoresSupervisedChatAfterItsAgentExits(t *testing.T) {
	manager, info, before, dir := exitedACPChat(t, true)
	require.ErrorIs(t, restoreACPChat(manager, info, dir), ErrSessionUnavailable, "ordinary restoration still skips exited chats")

	require.NoError(t, manager.RestoreSupervisedACP(t.Context(), storedACPChat(info, dir)))
	agent, err := manager.ACP("workspace", info.Key)
	require.NoError(t, err)
	assert.False(t, manager.Exited(info.Key))
	data, err := agent.Snapshot()
	require.NoError(t, err)
	var state ACPState
	require.NoError(t, json.Unmarshal(data, &state))
	assert := assert.New(t)
	assert.NotEmpty(state.RuntimeGeneration)
	assert.NotEqual(before.RuntimeGeneration, state.RuntimeGeneration)
	require.NotNil(t, state.Supervision)
	assert.Equal("coordinator", state.Supervision.Supervisor)
	assert.Equal(uint64(1), state.Supervision.Generation)
	assert.Equal("fixture-session", savedACPSession(t, manager, info.Key).SessionID)
	// The restored chat keeps the identity in the daemon's stored record.
	sessions := manager.ListSessions("workspace")
	require.Len(t, sessions, 1)
	assert.Equal("Coordinator task", sessions[0].Label)
	assert.True(info.CreatedAt.Equal(sessions[0].CreatedAt))

	// A running owner is the chat; restoring again starts no second agent.
	require.NoError(t, manager.RestoreSupervisedACP(t.Context(), storedACPChat(info, dir)))
	again, err := manager.ACP("workspace", info.Key)
	require.NoError(t, err)
	assert.Same(agent, again)
	log, err := os.ReadFile(filepath.Join(dir, "sessions"))
	require.NoError(t, err)
	assert.Equal("session/new\nsession/load\n", string(log))
}

// A supervised chat never continues in a new native session: an agent that
// cannot reload it refuses to start, by any restore path, and the saved
// session stays for a later attempt.
func TestACPSupervisedChatRefusesAgentThatCannotReload(t *testing.T) {
	t.Setenv("KENN_FORGE_ACP_NO_LOAD", "1")
	manager, info, _, dir := exitedACPChat(t, true)

	err := manager.RestoreSupervisedACP(t.Context(), storedACPChat(info, dir))
	require.ErrorIs(t, err, ErrACPCannotReload)
	_, err = manager.ACP("workspace", info.Key)
	require.ErrorIs(t, err, ErrSessionNotFound)
	assert.True(t, manager.Exited(info.Key))
	saved := savedACPSession(t, manager, info.Key)
	assert.Equal(t, "fixture-session", saved.SessionID)
	require.NotNil(t, saved.State.Supervision)

	// A daemon that never saw the exit restores it the ordinary way.
	manager.Shutdown()
	second := newACPTestManager(t, Options{ACPSessionsDir: manager.acpSessionsDir, Targets: manager.LaunchTargets()})
	require.ErrorIs(t, restoreACPChat(second, info, dir), ErrACPCannotReload)
	assert.FileExists(t, second.acpSessionPath(info.Key))
	log, err := os.ReadFile(filepath.Join(dir, "sessions"))
	require.NoError(t, err)
	assert.Equal(t, "session/new\n", string(log), "no restore may start a new native session")
}

func TestACPRestoreSupervisedRefusesUnsupervisedChat(t *testing.T) {
	manager, info, _, dir := exitedACPChat(t, false)
	require.ErrorIs(t, manager.RestoreSupervisedACP(t.Context(), storedACPChat(info, dir)), ErrSessionUnavailable)
	other := storedACPChat(info, dir)
	other.WorkspaceID = "other"
	require.ErrorIs(t, manager.RestoreSupervisedACP(t.Context(), other), ErrSessionNotFound)
	_, err := manager.ACP("workspace", info.Key)
	require.ErrorIs(t, err, ErrSessionNotFound)
	assert.True(t, manager.Exited(info.Key))
	log, err := os.ReadFile(filepath.Join(dir, "sessions"))
	require.NoError(t, err)
	assert.Equal(t, "session/new\n", string(log))
}

// A start failure recorded by an earlier owner belongs to that attempt; it
// must not fail a later restore of an agent that can reload the chat.
func TestACPRestoreSupervisedIgnoresEarlierStartFailure(t *testing.T) {
	manager, info, _, dir := exitedACPChat(t, true)
	paths, err := ptyowner.NewSessionPaths(manager.acpSessionsDir, info.Key)
	require.NoError(t, err)
	startError := filepath.Join(paths.Dir, acpStartErrorFile)
	require.NoError(t, os.WriteFile(startError, []byte("cannot_reload"), 0o600))
	// The daemon reads the record while it waits for the owner's socket.
	t.Setenv("KENN_FORGE_ACP_INITIALIZE_DELAY", "300ms")

	require.NoError(t, manager.RestoreSupervisedACP(t.Context(), storedACPChat(info, dir)))
	_, err = manager.ACP("workspace", info.Key)
	require.NoError(t, err)
	assert.NoFileExists(t, startError)
	log, err := os.ReadFile(filepath.Join(dir, "sessions"))
	require.NoError(t, err)
	assert.Equal(t, "session/new\nsession/load\n", string(log))
}

// The daemon keeps the stored record of a supervised chat whose agent exits,
// so it asks the saved session whether a coordinator supervises it.
func TestACPReportsWhetherASavedChatIsSupervised(t *testing.T) {
	for _, supervised := range []bool{true, false} {
		t.Run(fmt.Sprintf("supervised=%t", supervised), func(t *testing.T) {
			manager, info, _, _ := exitedACPChat(t, supervised)
			got, err := manager.SupervisedACP("workspace", info.Key)
			require.NoError(t, err)
			assert.Equal(t, supervised, got)

			_, err = manager.SupervisedACP("other-workspace", info.Key)
			require.ErrorIs(t, err, ErrSessionNotFound)
			_, err = manager.SupervisedACP("workspace", "unknown-chat")
			require.ErrorIs(t, err, ErrSessionNotFound)
		})
	}
}
