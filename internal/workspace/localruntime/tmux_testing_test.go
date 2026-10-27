package localruntime

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"go.kenn.io/forge/internal/procutil"
	ptyownerruntime "go.kenn.io/forge/internal/ptyowner/runtime"
)

func TestTmuxRuntimeFixtureRejectsKillWithoutPrivateServer(t *testing.T) {
	fixture := NewTmuxRuntimeFixture(t, nil)

	err := fixture.killServerAndWait(t.Context(), nil)

	require.EqualError(t, err, "fixture has no private tmux server command")
}

func TestTmuxRuntimeFixtureStopsDetachedAgentBeforeRemovingState(t *testing.T) {
	if privateTmuxOwner == nil {
		t.Skip("private tmux unavailable")
	}
	tmuxPath, err := exec.LookPath("tmux")
	require.NoError(t, err)
	command := privateTmuxOwner.Command(t, tmuxPath)
	cwd := t.TempDir()
	t.Setenv("KENN_FORGE_HOME", t.TempDir())
	t.Setenv("KENN_FORGE_TMUX_ENV_DIR", "")
	t.Setenv("KENN_FORGE_LOCALRUNTIME_HELPER", "1")
	// Record whether the exit-record directory exists when kill-server runs.
	probeDir := t.TempDir()
	exitDirFile := filepath.Join(probeDir, "exit-dir")
	marker := filepath.Join(probeDir, "kill-server")
	wrapper := filepath.Join(probeDir, "tmux")
	require.NoError(t, os.WriteFile(wrapper, []byte(`#!/bin/sh
for arg in "$@"; do
	if [ "$arg" = kill-server ]; then
		if [ -d "$(cat '`+exitDirFile+`')" ]; then echo present; else echo absent; fi >>'`+marker+`'
	fi
done
exec "$TMUX_FIXTURE_REAL" "$@"
`), 0o700))
	t.Setenv("TMUX_FIXTURE_REAL", command[0])
	serverCommand := append([]string{wrapper}, command[1:]...)
	var exitDir, session string
	t.Run("restart", func(t *testing.T) {
		fixture := NewTmuxRuntimeFixture(t, serverCommand)
		manager := fixture.NewAgentManager(command, Options{Targets: []LaunchTarget{
			helperTarget("agent", "sleep"),
			{Key: "shell", Kind: LaunchTargetShell, Available: true, Command: command},
		}})
		exitDir = manager.agentExitDir
		require.NoError(t, os.WriteFile(exitDirFile, []byte(exitDir), 0o600))
		info, err := manager.Launch(t.Context(), "workspace", cwd, "agent")
		require.NoError(t, err)
		session = info.TmuxSession
		require.NotEmpty(t, session)
		require.DirExists(t, exitDir)
		manager.Shutdown()
		require.NoError(t, procutil.Command(command[0], append(command[1:], "has-session", "-t", session)...).Run())
	})
	got, err := os.ReadFile(marker)
	require.NoError(t, err)
	require.Equal(t, "present\n", string(got), "fixture must kill the server once, before removing exit records")
	require.NoDirExists(t, exitDir)
	require.Error(t, procutil.Command(command[0], append(command[1:], "has-session", "-t", session)...).Run())
}

func TestTmuxRuntimeFixtureStopsDetachedPtyOwnerAgent(t *testing.T) {
	requirePTYAvailable(t)
	t.Setenv("KENN_FORGE_LOCALRUNTIME_HELPER", "1")
	// Register TempDir cleanup first so the owner remains observable after fixture cleanup.
	cwd := t.TempDir()
	var owner ptyownerruntime.Owner
	var sessionKey string
	fixture := NewTmuxRuntimeFixture(t, nil)
	// Observe owner cleanup while fixture state still exists.
	t.Cleanup(func() {
		if owner == nil || sessionKey == "" {
			return
		}
		ctx, cancel := context.WithTimeout(context.WithoutCancel(t.Context()), 10*time.Second)
		defer cancel()
		// Reap the real backend only after checking what fixture cleanup did.
		defer func() { require.NoError(t, owner.Stop(ctx, sessionKey)) }()
		require.Eventually(t, func() bool { return owner.Gone(ctx, sessionKey) }, 5*time.Second, 10*time.Millisecond,
			"fixture cleanup left the detached PTY-owner agent running")
	})
	manager := fixture.NewAgentManager([]string{"/missing/fixture-tmux"}, Options{
		Targets: []LaunchTarget{helperTarget("agent", "sleep")},
	})
	owner = manager.ptyOwnerRuntime
	info, err := manager.Launch(t.Context(), "ws:alpha", cwd, "agent")
	require.NoError(t, err)
	sessionKey = info.Key
	require.Empty(t, info.TmuxSession, "the agent must use the PTY-owner fallback")
	require.False(t, owner.Gone(t.Context(), sessionKey))

	manager.Shutdown()

	require.Empty(t, manager.ListSessions("ws:alpha"))
	require.False(t, owner.Gone(t.Context(), sessionKey), "shutdown must detach the durable owner")
}
