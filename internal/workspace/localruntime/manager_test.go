package localruntime

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/creack/pty/v2"
	Assert "github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/wesm/middleman/internal/procutil"
	ptyownerruntime "github.com/wesm/middleman/internal/ptyowner/runtime"
)

func requirePTYAvailable(t *testing.T) {
	t.Helper()
	ptmx, tty, err := pty.Open()
	if err != nil {
		t.Skipf("pty unavailable in this test environment: %v", err)
	}
	_ = ptmx.Close()
	_ = tty.Close()
}

func TestManagerLaunchesSingletonPerWorkspaceTarget(t *testing.T) {
	requirePTYAvailable(t)
	t.Setenv("MIDDLEMAN_LOCALRUNTIME_HELPER", "1")

	ctx := context.Background()
	mgr := NewManager(Options{Targets: []LaunchTarget{
		helperTarget("helper", "sleep"),
	}})
	t.Cleanup(mgr.Shutdown)

	session1, err := mgr.Launch(ctx, "ws-1", t.TempDir(), "helper")
	require.NoError(t, err)
	session2, err := mgr.Launch(ctx, "ws-1", t.TempDir(), "helper")
	require.NoError(t, err)

	sessions := mgr.ListSessions("ws-1")
	assert := Assert.New(t)
	assert.Equal(session1.Key, session2.Key)
	assert.Equal(SessionStatusRunning, session1.Status)
	assert.Len(sessions, 1)
}

func TestManagerLaunchConcurrentStartsOneProcess(t *testing.T) {
	requirePTYAvailable(t)
	t.Setenv("MIDDLEMAN_LOCALRUNTIME_HELPER", "1")
	require := require.New(t)
	assert := Assert.New(t)

	ctx := context.Background()
	record := filepath.Join(t.TempDir(), "starts")
	mgr := NewManager(Options{Targets: []LaunchTarget{
		{
			Key: "helper", Label: "helper", Kind: LaunchTargetAgent,
			Source: "config", Command: helperRecordCommand(record),
			Available: true,
		},
	}})
	t.Cleanup(mgr.Shutdown)

	const launches = 12
	var wg sync.WaitGroup
	errs := make(chan error, launches)
	infos := make(chan SessionInfo, launches)
	cwd := t.TempDir()
	for range launches {
		wg.Go(func() {
			info, err := mgr.Launch(ctx, "ws-1", cwd, "helper")
			errs <- err
			infos <- info
		})
	}
	wg.Wait()
	close(errs)
	close(infos)

	for err := range errs {
		require.NoError(err)
	}
	var firstKey string
	for info := range infos {
		if firstKey == "" {
			firstKey = info.Key
		}
		assert.Equal(firstKey, info.Key)
	}
	require.Eventually(func() bool {
		data, err := os.ReadFile(record)
		if err != nil {
			return false
		}
		return strings.Count(string(data), "\n") == 1
	}, 2*time.Second, 20*time.Millisecond)
	assert.Len(mgr.ListSessions("ws-1"), 1)
}

func TestSessionKeyIsFilesystemSafe(t *testing.T) {
	key := sessionKey("ws:alpha", "foo:bar/baz")

	assert := Assert.New(t)
	assert.NotContains(key, ":")
	assert.NotContains(key, "/")
	assert.NotContains(key, `\\`)
	assert.Equal(key, sessionKey("ws:alpha", "foo:bar/baz"))
	assert.NotEqual(key, sessionKey("ws:alpha", "foo:bar/qux"))
}

func TestManagerLaunchUnavailableTarget(t *testing.T) {
	ctx := context.Background()
	mgr := NewManager(Options{Targets: []LaunchTarget{{
		Key: "missing", Label: "Missing", Kind: LaunchTargetAgent,
		Available: false, DisabledReason: "not found",
	}}})
	t.Cleanup(mgr.Shutdown)

	_, err := mgr.Launch(ctx, "ws-1", t.TempDir(), "missing")
	require.Error(t, err)
	require.Contains(t, err.Error(), "not available")
}

func TestManagerLaunchMissingTarget(t *testing.T) {
	ctx := context.Background()
	mgr := NewManager(Options{})
	t.Cleanup(mgr.Shutdown)

	_, err := mgr.Launch(ctx, "ws-1", t.TempDir(), "missing")
	require.Error(t, err)
	require.Contains(t, err.Error(), "target not found")
}

func TestManagerUpdateTargetsAffectsFutureLaunches(t *testing.T) {
	requirePTYAvailable(t)
	t.Setenv("MIDDLEMAN_LOCALRUNTIME_HELPER", "1")
	assert := Assert.New(t)

	ctx := context.Background()
	mgr := NewManager(Options{Targets: []LaunchTarget{
		helperTarget("helper", "exit"),
	}})
	t.Cleanup(mgr.Shutdown)

	mgr.UpdateTargets([]LaunchTarget{{
		Key: "custom", Label: "Custom", Kind: LaunchTargetAgent,
		Source: "config", Command: helperCommand("exit"),
		Available: true,
	}})

	_, err := mgr.Launch(ctx, "ws-1", t.TempDir(), "helper")
	require.Error(t, err)
	assert.Contains(err.Error(), "target not found")

	session, err := mgr.Launch(ctx, "ws-1", t.TempDir(), "custom")
	require.NoError(t, err)
	assert.Equal("custom", session.TargetKey)
	assert.Equal("Custom", session.Label)
}

func TestManagerTmuxSessionsReturnsWrappedAgentSessions(t *testing.T) {
	assert := Assert.New(t)
	mgr := NewManager(Options{})
	mgr.sessions["ws-1:codex"] = &session{
		info: SessionInfo{
			Key:         "ws-1:codex",
			WorkspaceID: "ws-1",
			TargetKey:   "codex",
			Kind:        LaunchTargetAgent,
		},
		tmuxSession: "middleman-ws-1-codex",
	}
	mgr.sessions["ws-1:direct"] = &session{
		info: SessionInfo{
			Key:         "ws-1:direct",
			WorkspaceID: "ws-1",
			TargetKey:   "direct",
			Kind:        LaunchTargetAgent,
		},
	}
	mgr.sessions["ws-2:codex"] = &session{
		info: SessionInfo{
			Key:         "ws-2:codex",
			WorkspaceID: "ws-2",
			TargetKey:   "codex",
			Kind:        LaunchTargetAgent,
		},
		tmuxSession: "middleman-ws-2-codex",
	}

	assert.Equal(
		[]string{"middleman-ws-1-codex"},
		mgr.TmuxSessions("ws-1"),
	)
}

func TestManagerLaunchCommandWrapsAgentsInTmuxWhenEnabled(t *testing.T) {
	assert := Assert.New(t)
	agent := helperTarget("codex", "sleep")
	agent.Label = "Codex"
	mgr := NewManager(Options{
		Targets: []LaunchTarget{
			agent,
			{
				Key: "tmux", Label: "tmux", Kind: LaunchTargetTmux,
				Source: "system", Command: []string{"/usr/bin/tmux"},
				Available: true,
			},
		},
		TmuxCommand:             []string{"/usr/bin/tmux"},
		WrapAgentSessionsInTmux: true,
	})
	t.Cleanup(mgr.Shutdown)

	launch, err := mgr.launchCommand(
		agent, "ws:alpha", "/tmp/work tree",
	)
	require.NoError(t, err)
	sessionName := tmuxSessionName("ws:alpha", "codex")

	assert.Equal("/usr/bin/tmux", launch.Command[0])
	assert.Equal(
		[]string{
			"new-session",
			"-A",
			"-s",
			sessionName,
			"-c",
			"/tmp/work tree",
		},
		launch.Command[1:7],
	)
	assert.Contains(launch.Command[7], "exec ")
	assert.Contains(launch.Command[7], shellQuote(agent.Command[0]))
	assert.Equal(sessionName, launch.TmuxSession)
}

func TestManagerLaunchCommandMarksWrappedAgentTmuxSession(t *testing.T) {
	assert := Assert.New(t)
	require := require.New(t)
	agent := helperTarget("codex", "sleep")
	mgr := NewManager(Options{
		Targets: []LaunchTarget{
			agent,
			{
				Key: "tmux", Label: "tmux", Kind: LaunchTargetTmux,
				Source: "system", Command: []string{"/usr/bin/tmux"},
				Available: true,
			},
		},
		TmuxCommand:             []string{"/usr/bin/tmux"},
		TmuxOwnerMarker:         "middleman:test-owner",
		WrapAgentSessionsInTmux: true,
	})
	t.Cleanup(mgr.Shutdown)

	launch, err := mgr.launchCommand(agent, "ws-1", "/tmp/work tree")
	require.NoError(err)
	sessionName := tmuxSessionName("ws-1", "codex")

	require.Len(launch.Command, 3)
	assert.Equal([]string{"/bin/sh", "-lc"}, launch.Command[:2])
	script := launch.Command[2]
	assert.Contains(script, "has-session")
	assert.Contains(script, "new-session")
	assert.Contains(script, "set-option")
	assert.Contains(script, "kill-session")
	assert.Contains(script, "exit 1")
	assert.Contains(script, "@middleman_owner")
	assert.Contains(script, "middleman:test-owner")
	assert.Contains(script, "attach-session")
	assert.Contains(script, sessionName)
	assert.Contains(script, shellQuote(agent.Command[0]))
	assert.Equal(sessionName, launch.TmuxSession)
}

func TestTmuxSessionNameUsesOpaqueTargetHash(t *testing.T) {
	assert := Assert.New(t)

	fooSlash := tmuxSessionName("ws:alpha", "foo/bar")
	fooColon := tmuxSessionName("ws:alpha", "foo:bar")

	assert.NotEqual(fooSlash, fooColon)
	assert.NotContains(fooSlash, "foo")
	assert.NotContains(fooSlash, "/")
	assert.NotContains(fooColon, ":")
	assert.Contains(fooSlash, "middleman-ws-alpha-")
}

func TestManagerLaunchCommandCleansUpWhenOwnerMarkingFails(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("tmux owner shell wrapper uses Unix shell semantics")
	}

	assert := Assert.New(t)
	require := require.New(t)
	dir := t.TempDir()
	record := filepath.Join(dir, "record")
	tmuxPath := filepath.Join(dir, "tmux-fails-set-option")
	require.NoError(os.WriteFile(tmuxPath, []byte(`#!/bin/sh
printf '%s\0' "$@" >> "$TMUX_RECORD"
case "$1" in
  has-session)
    exit 1
    ;;
  new-session)
    for a in "$@"; do
      if [ "$a" = "@middleman_owner" ]; then
        exit 42
      fi
    done
    exit 0
    ;;
  kill-session)
    exit 0
    ;;
esac
exit 0
`), 0o755))
	t.Setenv("TMUX_RECORD", record)

	agent := helperTarget("codex", "sleep")
	mgr := NewManager(Options{
		Targets: []LaunchTarget{
			agent,
			{
				Key: "tmux", Label: "tmux", Kind: LaunchTargetTmux,
				Source: "system", Command: []string{tmuxPath},
				Available: true,
			},
		},
		TmuxCommand:             []string{tmuxPath},
		TmuxOwnerMarker:         "middleman:test-owner",
		WrapAgentSessionsInTmux: true,
	})
	t.Cleanup(mgr.Shutdown)

	launch, err := mgr.launchCommand(agent, "ws-1", t.TempDir())
	require.NoError(err)
	cmd := procutil.Command(launch.Command[0], launch.Command[1:]...)
	cmd.Env = append(os.Environ(), "TMUX_RECORD="+record)

	err = cmd.Run()
	require.Error(err)
	data, err := os.ReadFile(record)
	require.NoError(err)
	recorded := string(data)
	assert.Contains(recorded, "new-session")
	assert.Contains(recorded, "@middleman_owner")
	assert.Contains(recorded, "kill-session")
}

func TestManagerLaunchCommandRejectsRelativeAgentCommandWhenWrapped(t *testing.T) {
	agent := helperTarget("codex", "sleep")
	agent.Command = []string{"./codex"}
	mgr := NewManager(Options{
		Targets: []LaunchTarget{
			agent,
			{
				Key: "tmux", Label: "tmux", Kind: LaunchTargetTmux,
				Source: "system", Command: []string{"/usr/bin/tmux"},
				Available: true,
			},
		},
		TmuxCommand:             []string{"/usr/bin/tmux"},
		WrapAgentSessionsInTmux: true,
	})
	t.Cleanup(mgr.Shutdown)

	_, err := mgr.launchCommand(agent, "ws-1", t.TempDir())

	require.Error(t, err)
	require.Contains(t, err.Error(), "absolute path")
}

func TestManagerLaunchCommandUsesSanitizedEnvForWrappedAgent(t *testing.T) {
	t.Setenv("MIDDLEMAN_GITHUB_TOKEN", "secret-token")
	t.Setenv("CONTEXT7_API_KEY", "context7-secret")
	t.Setenv("MIDDLEMAN_SAFE_FOR_TEST", "not-carried")
	assert := Assert.New(t)
	agent := helperTarget("codex", "sleep")
	agent.Command = []string{"sh", "-c", "echo ok"}
	mgr := NewManager(Options{
		Targets: []LaunchTarget{
			agent,
			{
				Key: "tmux", Label: "tmux", Kind: LaunchTargetTmux,
				Source: "system", Command: []string{"/usr/bin/tmux"},
				Available: true,
			},
		},
		TmuxCommand:             []string{"/usr/bin/tmux"},
		WrapAgentSessionsInTmux: true,
	})
	t.Cleanup(mgr.Shutdown)

	launch, err := mgr.launchCommand(agent, "ws-1", t.TempDir())
	require.NoError(t, err)

	tmuxCommand := strings.Join(launch.Command, "\n")
	assert.Contains(tmuxCommand, "env -i")
	assert.Contains(tmuxCommand, "TERM=xterm-256color")
	assert.Contains(tmuxCommand, "HOME=")
	assert.NotContains(tmuxCommand, "secret-token")
	assert.NotContains(tmuxCommand, "context7-secret")
	assert.NotContains(tmuxCommand, "not-carried")
	assert.NotContains(tmuxCommand, "'sh'")
}

func TestManagerLaunchCommandFallsBackWhenTmuxUnavailable(t *testing.T) {
	assert := Assert.New(t)
	agent := helperTarget("codex", "sleep")
	mgr := NewManager(Options{
		Targets: []LaunchTarget{
			agent,
			{
				Key: "tmux", Label: "tmux", Kind: LaunchTargetTmux,
				Source: "system", Command: []string{"tmux"},
				Available: false, DisabledReason: "tmux not found",
			},
		},
		TmuxCommand:             []string{"tmux"},
		WrapAgentSessionsInTmux: true,
	})
	t.Cleanup(mgr.Shutdown)

	launch, err := mgr.launchCommand(agent, "ws-1", t.TempDir())
	require.NoError(t, err)

	assert.Equal(agent.Command, launch.Command)
	assert.Empty(launch.TmuxSession)
}

func TestManagerLaunchUsesPtyOwnerWhenConfigured(t *testing.T) {
	require := require.New(t)
	assert := Assert.New(t)
	ctx := context.Background()
	backend := newFakeRuntimePtyOwner()
	agent := helperTarget("codex", "exit")
	mgr := NewManager(Options{
		Targets:         []LaunchTarget{agent},
		PtyOwnerRuntime: backend,
	})
	t.Cleanup(mgr.Shutdown)

	info, err := mgr.Launch(ctx, "ws-1", t.TempDir(), "codex")
	require.NoError(err)

	assert.Equal(SessionStatusRunning, info.Status)
	assert.Equal(info.Key, backend.startedSession)
	assert.NotContains(backend.startedSession, ":")
	assert.Equal(agent.Command, backend.startedCommand)
	assert.Len(mgr.ListSessions("ws-1"), 1)
}

func TestManagerLaunchPassesStripEnvVarsToPtyOwner(t *testing.T) {
	require := require.New(t)
	assert := Assert.New(t)
	ctx := context.Background()
	backend := newFakeRuntimePtyOwner()
	agent := helperTarget("codex", "exit")
	mgr := NewManager(Options{
		Targets:         []LaunchTarget{agent},
		PtyOwnerRuntime: backend,
		StripEnvVars:    []string{"WORKSPACE_TOKEN", "WORKSPACE_TOKEN"},
	})
	t.Cleanup(mgr.Shutdown)

	_, err := mgr.Launch(ctx, "ws-1", t.TempDir(), "codex")
	require.NoError(err)

	assert.Equal([]string{"WORKSPACE_TOKEN"}, backend.startedStripEnvVars)
}

func TestManagerLaunchCommandDoesNotWrapWhenConfigDisabled(t *testing.T) {
	assert := Assert.New(t)
	agent := helperTarget("codex", "sleep")
	mgr := NewManager(Options{
		Targets: []LaunchTarget{
			agent,
			{
				Key: "tmux", Label: "tmux", Kind: LaunchTargetTmux,
				Source: "system", Command: []string{"/usr/bin/tmux"},
				Available: true,
			},
		},
		TmuxCommand:             []string{"/usr/bin/tmux"},
		WrapAgentSessionsInTmux: false,
	})
	t.Cleanup(mgr.Shutdown)

	launch, err := mgr.launchCommand(agent, "ws-1", t.TempDir())
	require.NoError(t, err)

	assert.Equal(agent.Command, launch.Command)
	assert.Empty(launch.TmuxSession)
}

func TestManagerStopReportsTmuxCleanupFailure(t *testing.T) {
	require := require.New(t)
	tmuxPath := filepath.Join(t.TempDir(), "tmux-fails")
	require.NoError(os.WriteFile(
		tmuxPath,
		[]byte("#!/bin/sh\nexit 42\n"),
		0o755,
	))
	done := make(chan struct{})
	close(done)
	mgr := NewManager(Options{TmuxCommand: []string{tmuxPath}})
	mgr.sessions["ws-1:codex"] = &session{
		info: SessionInfo{
			Key:         "ws-1:codex",
			WorkspaceID: "ws-1",
			TargetKey:   "codex",
			Kind:        LaunchTargetAgent,
		},
		cmd:         &exec.Cmd{},
		tmuxSession: "middleman-ws-1-codex",
		done:        done,
	}

	err := mgr.Stop(context.Background(), "ws-1", "ws-1:codex")

	require.Error(err)
	require.Contains(err.Error(), "kill tmux session")
	require.Len(mgr.ListSessions("ws-1"), 1)
}

func TestManagerStopFailedTmuxCleanupDoesNotSuppressExitCleanup(t *testing.T) {
	requirePTYAvailable(t)
	t.Setenv("MIDDLEMAN_LOCALRUNTIME_HELPER", "1")
	require := require.New(t)
	assert := Assert.New(t)

	tmuxPath := filepath.Join(t.TempDir(), "tmux-fails")
	require.NoError(os.WriteFile(
		tmuxPath,
		[]byte("#!/bin/sh\nexit 42\n"),
		0o755,
	))
	ctx := context.Background()
	mgr := NewManager(Options{
		Targets: []LaunchTarget{
			helperTarget("helper", "sleep"),
		},
		TmuxCommand: []string{tmuxPath},
	})
	t.Cleanup(mgr.Shutdown)

	info, err := mgr.Launch(ctx, "ws-1", t.TempDir(), "helper")
	require.NoError(err)

	mgr.mu.Lock()
	mgr.sessions[info.Key].tmuxSession = "middleman-ws-1-helper"
	mgr.mu.Unlock()

	err = mgr.Stop(ctx, "ws-1", info.Key)

	require.Error(err)
	require.Contains(err.Error(), "kill tmux session")
	assert.Eventually(func() bool {
		return len(mgr.ListSessions("ws-1")) == 0
	}, 2*time.Second, 20*time.Millisecond)
}

func TestManagerStopIgnoresAbsentTmuxSession(t *testing.T) {
	tmuxPath := filepath.Join(t.TempDir(), "tmux-absent")
	require.NoError(t, os.WriteFile(
		tmuxPath,
		[]byte("#!/bin/sh\necho \"can't find session: nope\" >&2\nexit 1\n"),
		0o755,
	))
	done := make(chan struct{})
	close(done)
	mgr := NewManager(Options{TmuxCommand: []string{tmuxPath}})
	mgr.sessions["ws-1:codex"] = &session{
		info: SessionInfo{
			Key:         "ws-1:codex",
			WorkspaceID: "ws-1",
			TargetKey:   "codex",
			Kind:        LaunchTargetAgent,
		},
		cmd:         &exec.Cmd{},
		tmuxSession: "middleman-ws-1-codex",
		done:        done,
	}

	err := mgr.Stop(context.Background(), "ws-1", "ws-1:codex")

	require.NoError(t, err)
}

func TestManagerShutdownLeavesTmuxSessionsRunning(t *testing.T) {
	assert := Assert.New(t)
	dir := t.TempDir()
	record := filepath.Join(dir, "record")
	tmuxPath := filepath.Join(dir, "tmux-records")
	require.NoError(t, os.WriteFile(
		tmuxPath,
		[]byte("#!/bin/sh\nprintf '%s\\0' \"$@\" >> \"$TMUX_RECORD\"\n"),
		0o755,
	))
	t.Setenv("TMUX_RECORD", record)
	done := make(chan struct{})
	close(done)
	mgr := NewManager(Options{TmuxCommand: []string{tmuxPath}})
	mgr.sessions["ws-1:codex"] = &session{
		info: SessionInfo{
			Key:         "ws-1:codex",
			WorkspaceID: "ws-1",
			TargetKey:   "codex",
			Kind:        LaunchTargetAgent,
		},
		cmd:         &exec.Cmd{},
		tmuxSession: "middleman-ws-1-codex",
		done:        done,
	}

	mgr.Shutdown()

	_, err := os.Stat(record)
	assert.True(os.IsNotExist(err), "shutdown should not invoke tmux cleanup")
	assert.Empty(mgr.ListSessions("ws-1"))
}

func TestManagerShutdownTerminatesPTYManagedSessions(t *testing.T) {
	requirePTYAvailable(t)
	t.Setenv("MIDDLEMAN_LOCALRUNTIME_HELPER", "1")
	require := require.New(t)
	assert := Assert.New(t)

	ctx := context.Background()
	mgr := NewManager(Options{Targets: []LaunchTarget{
		helperTarget("helper", "sleep"),
	}})

	info, err := mgr.Launch(ctx, "ws-1", t.TempDir(), "helper")
	require.NoError(err)

	var pid int
	require.Eventually(func() bool {
		mgr.mu.Lock()
		defer mgr.mu.Unlock()
		s := mgr.sessions[info.Key]
		if s == nil || s.cmd == nil || s.cmd.Process == nil {
			return false
		}
		pid = s.cmd.Process.Pid
		return pid > 0
	}, 2*time.Second, 20*time.Millisecond)
	require.True(processAlive(pid), "helper should be alive")

	mgr.Shutdown()

	assert.Eventually(func() bool {
		return !processAlive(pid)
	}, 5*time.Second, 25*time.Millisecond)
	assert.Empty(mgr.ListSessions("ws-1"))
}

func TestManagerStopRemovesSession(t *testing.T) {
	requirePTYAvailable(t)
	t.Setenv("MIDDLEMAN_LOCALRUNTIME_HELPER", "1")

	ctx := context.Background()
	mgr := NewManager(Options{Targets: []LaunchTarget{
		helperTarget("helper", "sleep"),
	}})
	t.Cleanup(mgr.Shutdown)

	session, err := mgr.Launch(ctx, "ws-1", t.TempDir(), "helper")
	require.NoError(t, err)
	require.NoError(t, mgr.Stop(ctx, "ws-1", session.Key))

	assert := Assert.New(t)
	assert.Empty(mgr.ListSessions("ws-1"))
	assert.Error(mgr.Stop(ctx, "ws-1", session.Key))
}

func TestManagerLaunchRejectsWhileWorkspaceStopping(t *testing.T) {
	requirePTYAvailable(t)
	t.Setenv("MIDDLEMAN_LOCALRUNTIME_HELPER", "1")
	require := require.New(t)
	assert := Assert.New(t)

	record := filepath.Join(t.TempDir(), "pids")
	mgr := NewManager(Options{Targets: []LaunchTarget{{
		Key: "helper", Label: "helper", Kind: LaunchTargetAgent,
		Source: "config", Available: true,
		Command: helperRecordCommand(record),
	}}})
	t.Cleanup(mgr.Shutdown)

	mgr.mu.Lock()
	mgr.stoppingWS["ws-1"] = 1
	mgr.mu.Unlock()

	_, err := mgr.Launch(context.Background(), "ws-1", t.TempDir(), "helper")
	require.ErrorIs(err, errWorkspaceStopping)
	assert.Empty(mgr.ListSessions("ws-1"))

	// Whatever PID the helper recorded before being killed must be
	// gone — no orphan from the rejected launch.
	assert.Eventually(func() bool {
		data, err := os.ReadFile(record)
		if err != nil || len(data) == 0 {
			return true // helper died before recording
		}
		for line := range strings.SplitSeq(strings.TrimSpace(string(data)), "\n") {
			pid, _ := strconv.Atoi(line)
			if processAlive(pid) {
				return false
			}
		}
		return true
	}, 5*time.Second, 25*time.Millisecond,
		"rejected launch's helper process must be reaped")

	// Launches succeed again once the marker clears.
	mgr.mu.Lock()
	delete(mgr.stoppingWS, "ws-1")
	mgr.mu.Unlock()
	_, err = mgr.Launch(context.Background(), "ws-1", t.TempDir(), "helper")
	require.NoError(err)
}

func TestBeginStoppingRejectsLaunchUntilEnd(t *testing.T) {
	requirePTYAvailable(t)
	t.Setenv("MIDDLEMAN_LOCALRUNTIME_HELPER", "1")
	require := require.New(t)

	mgr := NewManager(Options{Targets: []LaunchTarget{
		helperTarget("helper", "sleep"),
	}})
	t.Cleanup(mgr.Shutdown)

	mgr.BeginStopping("ws-1")
	_, err := mgr.Launch(context.Background(), "ws-1", t.TempDir(), "helper")
	require.ErrorIs(err, errWorkspaceStopping)

	// Other workspaces are unaffected.
	_, err = mgr.Launch(context.Background(), "ws-2", t.TempDir(), "helper")
	require.NoError(err)

	mgr.EndStopping("ws-1")
	_, err = mgr.Launch(context.Background(), "ws-1", t.TempDir(), "helper")
	require.NoError(err)
}

func TestStopWorkspaceWaitsForInflightLaunches(t *testing.T) {
	require := require.New(t)
	assert := Assert.New(t)

	mgr := NewManager(Options{})
	t.Cleanup(mgr.Shutdown)

	// Simulate a Launch that already passed claimInflight but has
	// not yet returned (i.e. still inside startSession). Without
	// the drain, StopWorkspace would snapshot empty sessions and
	// finish; the in-flight launch would then insert a session
	// after the workspace was supposedly stopped.
	mgr.mu.Lock()
	mgr.inflightWS["ws-1"] = 1
	mgr.mu.Unlock()

	stopReturned := make(chan struct{})
	go func() {
		mgr.StopWorkspace(context.Background(), "ws-1")
		close(stopReturned)
	}()

	select {
	case <-stopReturned:
		require.FailNow(
			"StopWorkspace returned before inflight launch drained",
		)
	case <-time.After(75 * time.Millisecond):
	}

	mgr.releaseInflight("ws-1")

	select {
	case <-stopReturned:
	case <-time.After(2 * time.Second):
		require.FailNow(
			"StopWorkspace did not return after inflight drained",
		)
	}

	// And the marker is cleared, so subsequent launches are not
	// permanently rejected.
	mgr.mu.Lock()
	stopping := mgr.stoppingWS["ws-1"]
	mgr.mu.Unlock()
	assert.Equal(0, stopping)
}

func TestManagerStopKillsDescendantProcesses(t *testing.T) {
	requirePTYAvailable(t)
	t.Setenv("MIDDLEMAN_LOCALRUNTIME_HELPER", "1")
	require := require.New(t)
	assert := Assert.New(t)

	record := filepath.Join(t.TempDir(), "pids")
	ctx := context.Background()
	mgr := NewManager(Options{Targets: []LaunchTarget{{
		Key: "helper", Label: "helper", Kind: LaunchTargetAgent,
		Source: "config", Available: true,
		Command: []string{
			os.Args[0],
			"-test.run=TestHelperProcess",
			"--",
			"spawn-child",
			record,
		},
	}}})
	t.Cleanup(mgr.Shutdown)

	session, err := mgr.Launch(ctx, "ws-1", t.TempDir(), "helper")
	require.NoError(err)

	var parentPID, childPID int
	require.Eventually(func() bool {
		data, err := os.ReadFile(record)
		if err != nil {
			return false
		}
		lines := strings.Split(strings.TrimSpace(string(data)), "\n")
		if len(lines) < 2 {
			return false
		}
		parentPID, _ = strconv.Atoi(lines[0])
		childPID, _ = strconv.Atoi(lines[1])
		return parentPID > 0 && childPID > 0
	}, 5*time.Second, 25*time.Millisecond, "helper should record both pids")

	require.True(processAlive(parentPID), "parent should be alive")
	require.True(processAlive(childPID), "child should be alive")

	require.NoError(mgr.Stop(ctx, "ws-1", session.Key))

	assert.Eventually(func() bool {
		return !processAlive(parentPID) && !processAlive(childPID)
	}, 5*time.Second, 25*time.Millisecond,
		"descendant child should die with the session leader")
}

func TestSessionWatchLeavesOutputOpenForDrain(t *testing.T) {
	require := require.New(t)

	readEnd, writeEnd, err := os.Pipe()
	require.NoError(err)
	defer writeEnd.Close()
	defer readEnd.Close()

	_, err = writeEnd.WriteString("final output")
	require.NoError(err)

	cmd := procutil.Command("sh", "-c", "exit 0")
	require.NoError(cmd.Start())
	outputDone := make(chan struct{})
	s := &session{
		cmd:        cmd,
		ptmx:       readEnd,
		done:       make(chan struct{}),
		outputDone: outputDone,
	}

	s.watch()

	buf := make([]byte, len("final output"))
	_, err = readEnd.Read(buf)
	require.NoError(err)
	close(outputDone)
	require.Equal("final output", string(buf))
}

func TestSessionWatchClosesPTYAfterPostExitDrainTimeout(t *testing.T) {
	require := require.New(t)

	readEnd, writeEnd, err := os.Pipe()
	require.NoError(err)
	defer readEnd.Close()
	defer writeEnd.Close()

	cmd := procutil.Command("sh", "-c", "exit 0")
	require.NoError(cmd.Start())
	outputDone := make(chan struct{})
	s := &session{
		cmd:        cmd,
		ptmx:       readEnd,
		done:       make(chan struct{}),
		outputDone: outputDone,
	}

	s.watch()
	defer close(outputDone)

	require.Eventually(func() bool {
		_, err := readEnd.Stat()
		return err != nil
	}, time.Second, 10*time.Millisecond)
}

func TestManagerRemovesNaturallyExitedSession(t *testing.T) {
	requirePTYAvailable(t)
	t.Setenv("MIDDLEMAN_LOCALRUNTIME_HELPER", "1")

	ctx := context.Background()
	exited := make(chan SessionInfo, 1)
	mgr := NewManager(Options{Targets: []LaunchTarget{
		helperTarget("helper", "exit"),
	}, OnSessionExit: func(info SessionInfo) {
		exited <- info
	}})
	t.Cleanup(mgr.Shutdown)

	session, err := mgr.Launch(ctx, "ws-1", t.TempDir(), "helper")
	require.NoError(t, err)

	var got SessionInfo
	require.Eventually(t, func() bool {
		select {
		case got = <-exited:
			return true
		default:
			return false
		}
	}, 2*time.Second, 20*time.Millisecond)

	assert := Assert.New(t)
	assert.Equal(session.Key, got.Key)
	assert.Equal(SessionStatusExited, got.Status)
	assert.NotNil(got.ExitedAt)
	assert.NotNil(got.ExitCode)
	assert.Equal(3, *got.ExitCode)
	assert.Empty(mgr.ListSessions("ws-1"))
}

func TestManagerRemovesNaturallyExitedShell(t *testing.T) {
	requirePTYAvailable(t)
	t.Setenv("MIDDLEMAN_LOCALRUNTIME_HELPER", "1")

	ctx := context.Background()
	exited := make(chan SessionInfo, 1)
	mgr := NewManager(Options{
		ShellCommand: helperCommand("exit"),
		OnSessionExit: func(info SessionInfo) {
			exited <- info
		},
	})
	t.Cleanup(mgr.Shutdown)

	shell, err := mgr.EnsureShell(ctx, "ws-1", t.TempDir())
	require.NoError(t, err)

	var got SessionInfo
	require.Eventually(t, func() bool {
		select {
		case got = <-exited:
			return true
		default:
			return false
		}
	}, 2*time.Second, 20*time.Millisecond)

	assert := Assert.New(t)
	assert.Equal(shell.Key, got.Key)
	assert.Equal(SessionStatusExited, got.Status)
	assert.NotNil(got.ExitedAt)
	assert.NotNil(got.ExitCode)
	assert.Equal(3, *got.ExitCode)
	assert.Nil(mgr.ShellSession("ws-1"))
}

func TestManagerShellSingletonPerWorkspace(t *testing.T) {
	requirePTYAvailable(t)
	t.Setenv("MIDDLEMAN_LOCALRUNTIME_HELPER", "1")

	ctx := context.Background()
	mgr := NewManager(Options{
		ShellCommand: helperCommand("sleep"),
	})
	t.Cleanup(mgr.Shutdown)

	shell1, err := mgr.EnsureShell(ctx, "ws-1", t.TempDir())
	require.NoError(t, err)
	shell2, err := mgr.EnsureShell(ctx, "ws-1", t.TempDir())
	require.NoError(t, err)

	assert := Assert.New(t)
	assert.Equal(shell1.Key, shell2.Key)
	assert.Equal(SessionStatusRunning, shell1.Status)
	assert.Empty(mgr.ListSessions("ws-1"))
}

// TestManagerEnsureShellSkipsZombieSessions pins the runningSession
// outputClosed check that prevents EnsureShell from returning a
// session whose drainOutput already saw PTY EOF but whose
// watchSession's cmd.Wait hasn't fired yet.
//
// Wrapped shells (systemd-run --wait, etc.) routinely sit in this
// "output dead, status still Running" window after the inner zsh
// exits while the wrapper does its cleanup. Without this guard,
// EnsureShell would hand the next caller a snapshot of the zombie:
// status=Running, but a closed Output channel that yields nothing.
// The frontend then mounts a TerminalPane, attaches, gets the exit
// frame instantly, auto-closes — and on the user's next click does
// it all over again until the zombie is finally collected. From the
// user's seat that looks like a hang.
func TestManagerEnsureShellSkipsZombieSessions(t *testing.T) {
	requirePTYAvailable(t)
	t.Setenv("MIDDLEMAN_LOCALRUNTIME_HELPER", "1")

	require := require.New(t)
	assert := Assert.New(t)
	ctx := context.Background()
	mgr := NewManager(Options{
		ShellCommand: helperCommand("sleep"),
	})
	t.Cleanup(mgr.Shutdown)

	first, err := mgr.EnsureShell(ctx, "ws-1", t.TempDir())
	require.NoError(err)
	require.Equal(SessionStatusRunning, first.Status)

	// Reach into the manager state and synthesize the zombie window:
	// outputClosed flipped while the watchSession goroutine is still
	// blocked on cmd.Wait (the helperCommand("sleep") process never
	// exits, so cmd.Wait won't return until Shutdown kills it).
	mgr.mu.Lock()
	s := mgr.shells[first.Key]
	mgr.mu.Unlock()
	require.NotNil(s, "shell session should be in m.shells")
	s.mu.Lock()
	s.outputClosed = true
	s.mu.Unlock()

	second, err := mgr.EnsureShell(ctx, "ws-1", t.TempDir())
	require.NoError(err)
	assert.Equal(SessionStatusRunning, second.Status)
	assert.NotEqual(
		first.CreatedAt, second.CreatedAt,
		"EnsureShell after a zombie must return a fresh session",
	)
	// The new session replaces the zombie in the map.
	mgr.mu.Lock()
	current := mgr.shells[first.Key]
	mgr.mu.Unlock()
	assert.NotSame(s, current,
		"the zombie should have been replaced, not reused")

	// Once the zombie is no longer in the map, Manager.Shutdown
	// can't reach it. EnsureShell must therefore reap it inline:
	// SIGKILL the process group so cmd.Wait returns and the
	// per-session watchSession goroutine completes (closing s.done).
	// helperCommand("sleep") would otherwise block forever.
	require.Eventually(func() bool {
		select {
		case <-s.done:
			return true
		default:
			return false
		}
	}, 2*time.Second, 20*time.Millisecond,
		"zombie's process must be killed and reaped, not orphaned")
}

// TestAttachmentSessionOutputClosedDistinguishesSubscriberDrop covers
// the contract bridges rely on to tell a real session exit from a
// dropped subscriber: a closed Output channel can mean either, and
// auto-closing the drawer on the latter would hang the user out on a
// healthy shell.
//
// broadcast drops a subscriber when its 64-slot buffer can't accept
// another chunk (slow client / congested writer). drainOutput's PTY
// EOF, in contrast, runs closeSubscribers which flips s.outputClosed
// before closing every subscriber channel. SessionOutputClosed
// exposes that distinction to bridge code.
func TestAttachmentSessionOutputClosedDistinguishesSubscriberDrop(t *testing.T) {
	requirePTYAvailable(t)
	t.Setenv("MIDDLEMAN_LOCALRUNTIME_HELPER", "1")

	require := require.New(t)
	assert := Assert.New(t)
	ctx := context.Background()
	mgr := NewManager(Options{ShellCommand: helperCommand("sleep")})
	t.Cleanup(mgr.Shutdown)

	shell, err := mgr.EnsureShell(ctx, "ws-1", t.TempDir())
	require.NoError(err)

	attach, err := mgr.AttachShell("ws-1")
	require.NoError(err)
	t.Cleanup(attach.Close)
	require.Equal(shell.Key, attach.Info().Key)

	// Healthy session: SessionOutputClosed must report false.
	assert.False(attach.SessionOutputClosed(),
		"freshly-attached session should not look output-closed")

	mgr.mu.Lock()
	s := mgr.shells[shell.Key]
	mgr.mu.Unlock()
	require.NotNil(s)

	// Force the broadcast-drops-subscriber path: the channel buffer
	// is 64, so the 65th broadcast that can't enqueue takes the
	// `default` branch and closes the channel. Run the broadcasts
	// synchronously WITHOUT a concurrent consumer — a parallel
	// reader could drain the buffer faster than we fill it and the
	// drop would never trigger. Drain afterward to confirm closure.
	for range 200 {
		s.broadcast([]byte("x"))
	}
	drained := 0
drain:
	for {
		// Bound the receive: if broadcast regresses and never
		// closes the channel, the buffered messages drain and the
		// next receive would block forever, hanging the test
		// process instead of failing it.
		select {
		case _, ok := <-attach.Output:
			if !ok {
				break drain
			}
			drained++
			require.Less(drained, 200,
				"channel never closed; broadcast did not "+
					"drop the slow subscriber")
		case <-time.After(2 * time.Second):
			require.Fail(
				"timed out waiting for channel close; " +
					"broadcast did not drop the slow subscriber",
			)
		}
	}
	assert.LessOrEqual(drained, 64,
		"buffer is 64; drop should fire by the 65th broadcast")

	// Subscriber dropped, but the session itself is still healthy
	// (helperCommand("sleep") is still running and drainOutput has
	// not seen PTY EOF). SessionOutputClosed must NOT be true here —
	// otherwise the bridge would emit "exited" on a live shell.
	assert.False(attach.SessionOutputClosed(),
		"subscriber drop must not be misreported as session exit")

	// Now simulate the real session-exit path. closeSubscribers is
	// what drainOutput calls on PTY EOF; it flips outputClosed.
	s.closeSubscribers()
	assert.True(attach.SessionOutputClosed(),
		"after drainOutput's closeSubscribers, the bridge must see "+
			"the session as output-closed and emit the exit frame")
}

type fakeRuntimePtyOwner struct {
	startedSession      string
	startedCwd          string
	startedCommand      []string
	startedStripEnvVars []string
	startedPTY          *fakeRuntimePTY
	stoppedSession      string
}

func newFakeRuntimePtyOwner() *fakeRuntimePtyOwner {
	return &fakeRuntimePtyOwner{}
}

func (f *fakeRuntimePtyOwner) Start(
	_ context.Context,
	session string,
	cwd string,
	command []string,
	stripEnvVars []string,
) (ptyownerruntime.PTY, error) {
	f.startedSession = session
	f.startedCwd = cwd
	f.startedCommand = slices.Clone(command)
	f.startedStripEnvVars = slices.Clone(stripEnvVars)
	f.startedPTY = &fakeRuntimePTY{
		output: make(chan []byte, 64),
		done:   make(chan struct{}),
	}
	return f.startedPTY, nil
}

func (f *fakeRuntimePtyOwner) Stop(_ context.Context, session string) error {
	f.stoppedSession = session
	if f.startedPTY != nil {
		f.startedPTY.Close()
	}
	return nil
}

type fakeRuntimePTY struct {
	output chan []byte
	done   chan struct{}
}

func (f *fakeRuntimePTY) Output() <-chan []byte { return f.output }

func (f *fakeRuntimePTY) Done() <-chan struct{} { return f.done }

func (f *fakeRuntimePTY) Write([]byte) error { return nil }

func (f *fakeRuntimePTY) Resize(int, int) error { return nil }

func (f *fakeRuntimePTY) ExitCode() int { return 0 }

func (f *fakeRuntimePTY) Close() {
	select {
	case <-f.done:
	default:
		close(f.output)
		close(f.done)
	}
}

func TestManagerShellConcurrentStartsOneProcess(t *testing.T) {
	requirePTYAvailable(t)
	t.Setenv("MIDDLEMAN_LOCALRUNTIME_HELPER", "1")
	require := require.New(t)
	assert := Assert.New(t)

	ctx := context.Background()
	record := filepath.Join(t.TempDir(), "shell-starts")
	mgr := NewManager(Options{
		ShellCommand: helperRecordCommand(record),
	})
	t.Cleanup(mgr.Shutdown)

	const launches = 12
	var wg sync.WaitGroup
	errs := make(chan error, launches)
	infos := make(chan SessionInfo, launches)
	cwd := t.TempDir()
	for range launches {
		wg.Go(func() {
			info, err := mgr.EnsureShell(ctx, "ws-1", cwd)
			errs <- err
			infos <- info
		})
	}
	wg.Wait()
	close(errs)
	close(infos)

	for err := range errs {
		require.NoError(err)
	}
	var firstKey string
	for info := range infos {
		if firstKey == "" {
			firstKey = info.Key
		}
		assert.Equal(firstKey, info.Key)
	}
	require.Eventually(func() bool {
		data, err := os.ReadFile(record)
		if err != nil {
			return false
		}
		return strings.Count(string(data), "\n") == 1
	}, 2*time.Second, 20*time.Millisecond)
	assert.NotNil(mgr.ShellSession("ws-1"))
}

func TestManagerShutdownRejectsNewLaunches(t *testing.T) {
	t.Setenv("MIDDLEMAN_LOCALRUNTIME_HELPER", "1")

	mgr := NewManager(Options{Targets: []LaunchTarget{
		helperTarget("helper", "sleep"),
	}})
	t.Cleanup(mgr.Shutdown)

	mgr.Shutdown()

	_, err := mgr.Launch(
		context.Background(), "ws-1", t.TempDir(), "helper",
	)
	require.Error(t, err)
	require.Contains(t, err.Error(), "runtime manager is shut down")
}

func TestSessionBroadcastClosesSlowSubscriber(t *testing.T) {
	s := &session{
		subscribers: make(map[chan []byte]struct{}),
	}
	ch := make(chan []byte, 1)
	ch <- []byte("queued")
	s.subscribers[ch] = struct{}{}

	s.broadcast([]byte("new"))

	got := <-ch
	assert := Assert.New(t)
	assert.Equal([]byte("queued"), got)
	select {
	case _, ok := <-ch:
		assert.False(ok)
	case <-time.After(100 * time.Millisecond):
		assert.Fail("slow subscriber was not closed")
	}
	s.mu.Lock()
	_, subscribed := s.subscribers[ch]
	s.mu.Unlock()
	assert.False(subscribed)
}

func TestSessionSubscribeReplaysBufferedOutput(t *testing.T) {
	s := &session{
		subscribers: make(map[chan []byte]struct{}),
	}
	s.broadcast([]byte("startup-banner\r\n"))
	s.broadcast([]byte("$ "))

	ch, cancel := s.subscribe()
	t.Cleanup(cancel)

	assert := Assert.New(t)
	select {
	case data := <-ch:
		assert.Equal("startup-banner\r\n$ ", string(data))
	case <-time.After(100 * time.Millisecond):
		assert.Fail("subscriber did not receive replay")
	}

	s.broadcast([]byte("ls\r\n"))
	select {
	case data := <-ch:
		assert.Equal("ls\r\n", string(data))
	case <-time.After(100 * time.Millisecond):
		assert.Fail("subscriber did not receive new output after replay")
	}
}

func TestSessionSubscribeSkipsReplayWhileAlternateScreenActive(t *testing.T) {
	s := &session{
		subscribers: make(map[chan []byte]struct{}),
	}
	s.broadcast([]byte("startup-banner\r\n$ "))
	s.broadcast([]byte("\x1b[?1049h\x1b[Hcodex screen"))

	ch, cancel := s.subscribe()
	t.Cleanup(cancel)

	assert := Assert.New(t)
	select {
	case data := <-ch:
		assert.Failf(
			"subscriber received alternate screen replay",
			"unexpected replay: %q",
			string(data),
		)
	case <-time.After(25 * time.Millisecond):
	}

	s.broadcast([]byte("\x1b[Hupdated screen"))
	select {
	case data := <-ch:
		assert.Equal("\x1b[Hupdated screen", string(data))
	case <-time.After(100 * time.Millisecond):
		assert.Fail("subscriber did not receive live output")
	}
}

func TestSessionSubscribeReplaysNormalOutputAfterAlternateScreenExit(t *testing.T) {
	s := &session{
		subscribers: make(map[chan []byte]struct{}),
	}
	s.broadcast([]byte("startup-banner\r\n$ "))
	s.broadcast([]byte("\x1b[?1049h\x1b[Hcodex screen"))
	s.broadcast([]byte("\x1b[?1049l\r\n$ "))

	ch, cancel := s.subscribe()
	t.Cleanup(cancel)

	assert := Assert.New(t)
	select {
	case data := <-ch:
		assert.Equal("\r\n$ ", string(data))
	case <-time.After(100 * time.Millisecond):
		assert.Fail("subscriber did not receive normal replay after exit")
	}
}

func TestSessionAlternateScreenTrackingHandlesSplitEscapeSequences(t *testing.T) {
	s := &session{
		subscribers: make(map[chan []byte]struct{}),
	}
	s.broadcast([]byte("startup-banner\r\n$ \x1b[?104"))
	s.broadcast([]byte("9h\x1b[Hcodex screen"))

	ch, cancel := s.subscribe()
	t.Cleanup(cancel)

	assert := Assert.New(t)
	select {
	case data := <-ch:
		assert.Failf(
			"subscriber received split alternate screen replay",
			"unexpected replay: %q",
			string(data),
		)
	case <-time.After(25 * time.Millisecond):
	}

	s.broadcast([]byte("\x1b[?104"))
	s.broadcast([]byte("9l\r\n$ "))
	var live strings.Builder
	select {
	case data := <-ch:
		live.Write(data)
	case <-time.After(100 * time.Millisecond):
		assert.Fail("subscriber did not receive live split exit prefix")
	}
	select {
	case data := <-ch:
		live.Write(data)
	case <-time.After(100 * time.Millisecond):
		assert.Fail("subscriber did not receive live split exit suffix")
	}
	assert.Equal("\x1b[?1049l\r\n$ ", live.String())

	ch2, cancel2 := s.subscribe()
	t.Cleanup(cancel2)
	select {
	case data := <-ch2:
		assert.Equal("\r\n$ ", string(data))
	case <-time.After(100 * time.Millisecond):
		assert.Fail("subscriber did not receive replay after split exit")
	}
}

func TestSessionSubscribeAfterCloseStillReplays(t *testing.T) {
	s := &session{
		subscribers: make(map[chan []byte]struct{}),
	}
	s.broadcast([]byte("hello\r\nbye\r\n"))
	s.closeSubscribers()

	ch, cancel := s.subscribe()
	t.Cleanup(cancel)

	assert := Assert.New(t)
	select {
	case data, ok := <-ch:
		assert.True(ok)
		assert.Equal("hello\r\nbye\r\n", string(data))
	case <-time.After(100 * time.Millisecond):
		assert.Fail("expected replay before channel close")
	}
	select {
	case _, ok := <-ch:
		assert.False(ok)
	case <-time.After(100 * time.Millisecond):
		assert.Fail("expected channel to close after replay")
	}
}

func TestSessionOutputBufferIsBounded(t *testing.T) {
	s := &session{
		subscribers: make(map[chan []byte]struct{}),
	}
	chunk := make([]byte, 8*1024)
	for i := range chunk {
		chunk[i] = 'x'
	}
	for range 12 {
		s.broadcast(chunk)
	}

	s.mu.Lock()
	bufLen := len(s.outputBuffer)
	s.mu.Unlock()
	Assert.New(t).LessOrEqual(bufLen, maxSessionOutputReplay)
}

func TestManagerStopWorkspaceStopsAllSessions(t *testing.T) {
	requirePTYAvailable(t)
	t.Setenv("MIDDLEMAN_LOCALRUNTIME_HELPER", "1")

	require := require.New(t)
	assert := Assert.New(t)

	ctx := context.Background()
	mgr := NewManager(Options{
		Targets: []LaunchTarget{
			helperTarget("agent-a", "sleep"),
			helperTarget("agent-b", "sleep"),
		},
		ShellCommand: helperCommand("sleep"),
	})
	t.Cleanup(mgr.Shutdown)

	_, err := mgr.Launch(ctx, "ws-1", t.TempDir(), "agent-a")
	require.NoError(err)
	_, err = mgr.Launch(ctx, "ws-1", t.TempDir(), "agent-b")
	require.NoError(err)
	_, err = mgr.EnsureShell(ctx, "ws-1", t.TempDir())
	require.NoError(err)

	// A second workspace's sessions must survive.
	_, err = mgr.Launch(ctx, "ws-2", t.TempDir(), "agent-a")
	require.NoError(err)

	mgr.StopWorkspace(ctx, "ws-1")

	assert.Empty(mgr.ListSessions("ws-1"))
	assert.Nil(mgr.ShellSession("ws-1"))
	assert.Len(mgr.ListSessions("ws-2"), 1)
}

func helperTarget(key, mode string) LaunchTarget {
	return LaunchTarget{
		Key: key, Label: key, Kind: LaunchTargetAgent,
		Source: "config", Command: helperCommand(mode),
		Available: true,
	}
}

func helperRecordCommand(record string) []string {
	return []string{
		os.Args[0],
		"-test.run=TestHelperProcess",
		"--",
		"sleep-record",
		record,
	}
}

func helperCommand(mode string) []string {
	return []string{
		os.Args[0],
		"-test.run=TestHelperProcess",
		"--",
		mode,
	}
}

// TestResolveExecutableRejectsRelativePaths ensures startSession
// refuses commands that would resolve inside the workspace worktree
// (PR-controlled content). Absolute paths and PATH-resolvable
// names are accepted; relative names with separators are rejected.
func TestResolveExecutableRejectsRelativePaths(t *testing.T) {
	require := require.New(t)
	assert := Assert.New(t)

	// Absolute path: pass through unchanged.
	absCommand := "/usr/local/bin/codex"
	if runtime.GOOS == "windows" {
		absCommand = `C:\tools\codex.exe`
	}
	got, err := resolveExecutable(absCommand)
	require.NoError(err)
	assert.Equal(absCommand, got)

	// PATH-resolvable: returns the full path.
	exeName, exePath := writeFakeRuntimeTool(t, t.TempDir(), "fake-runtime-tool")
	t.Setenv("PATH", filepath.Dir(exePath))
	got, err = resolveExecutable(exeName)
	require.NoError(err)
	assert.True(filepath.IsAbs(got), "expected absolute path, got %q", got)
	if runtime.GOOS == "windows" {
		assert.True(
			strings.EqualFold(exePath, got),
			"expected %q, got %q",
			exePath,
			got,
		)
	} else {
		assert.Equal(exePath, got)
	}

	// Relative paths must be rejected.
	for _, rel := range []string{
		"./agent",
		"../scripts/codex",
		"scripts/codex",
		"a/b",
	} {
		_, err := resolveExecutable(rel)
		require.Error(err, "expected error for %q", rel)
		assert.Contains(err.Error(), "absolute path")
	}

	// Empty name.
	_, err = resolveExecutable("")
	require.Error(err)

	// Bare name not on PATH should surface a LookPath error.
	_, err = resolveExecutable(
		"middleman-localruntime-bogus-name-zzz",
	)
	require.Error(err)
}

func TestResolveExecutableForcesAbsoluteFromRelativePATH(t *testing.T) {
	require := require.New(t)
	assert := Assert.New(t)

	dir := t.TempDir()
	binDir := filepath.Join(dir, "bin")
	require.NoError(os.MkdirAll(binDir, 0o755))
	exeName, exe := writeFakeRuntimeTool(t, binDir, "fake-runtime-tool")

	t.Chdir(dir)
	t.Setenv("PATH", "bin")
	// Recent Go versions wrap LookPath results from relative PATH
	// entries with ErrDot. With execerrdot=0 they're returned with
	// no error — that's exactly the case where the worktree-cwd
	// rebinding is dangerous, so verify the absolute fallback runs.
	t.Setenv("GODEBUG", "execerrdot=0")

	got, err := resolveExecutable(exeName)
	require.NoError(err)
	assert.True(
		filepath.IsAbs(got),
		"expected absolute path, got %q (relative would resolve "+
			"inside cmd.Dir = the workspace worktree)",
		got,
	)
	if runtime.GOOS == "windows" {
		assert.True(
			strings.EqualFold(exe, got),
			"expected %q, got %q",
			exe,
			got,
		)
	} else {
		assert.Equal(exe, got)
	}
}

func writeFakeRuntimeTool(
	t *testing.T,
	dir string,
	name string,
) (string, string) {
	t.Helper()
	require.NoError(t, os.MkdirAll(dir, 0o755))
	if runtime.GOOS == "windows" {
		t.Setenv("PATHEXT", ".COM;.EXE;.BAT;.CMD")
		path := filepath.Join(dir, name+".cmd")
		require.NoError(t, os.WriteFile(
			path,
			[]byte("@echo off\r\nexit /b 0\r\n"),
			0o755,
		))
		return name, path
	}
	path := filepath.Join(dir, name)
	require.NoError(t, os.WriteFile(
		path,
		[]byte("#!/bin/sh\nexit 0\n"),
		0o755,
	))
	return name, path
}

// TestSessionEnvironmentStripsCredentials verifies that the
// environment passed to runtime sessions has GitHub-token-shaped
// variables removed so that launched agents cannot exfiltrate
// the maintainer's credentials.
func TestSessionEnvironmentStripsCredentials(t *testing.T) {
	require := require.New(t)
	assert := Assert.New(t)

	in := []string{
		"PATH=/usr/bin",
		"HOME=/home/me",
		"MIDDLEMAN_GITHUB_TOKEN=secret-1",
		"GITHUB_TOKEN=secret-2",
		"GH_TOKEN=secret-3",
		"GITHUB_PAT=secret-4",
		"GH_PAT=secret-5",
		"GITHUB_ENTERPRISE_TOKEN=secret-6",
		"GH_ENTERPRISE_TOKEN=secret-7",
		"GITHUB_TOKEN_GHE=secret-8",
		"MAINTAINER_PERSONAL_GH_PAT=secret-9",
		"NOTSECRET=ok",
	}
	out := sessionEnvironment(in, []string{
		"MAINTAINER_PERSONAL_GH_PAT",
	})

	require.Contains(out, "PATH=/usr/bin")
	require.Contains(out, "HOME=/home/me")
	require.Contains(out, "NOTSECRET=ok")

	for _, kv := range out {
		assert.NotContains(
			kv, "secret-",
			"credential leaked through sessionEnvironment: %q", kv,
		)
	}
}

func TestSessionEnvironmentStripsConfiguredTokenEnv(t *testing.T) {
	require := require.New(t)
	in := []string{
		"PATH=/usr/bin",
		"WORK_GH_BOT_TOKEN=top-secret",
	}
	out := sessionEnvironment(in, []string{"WORK_GH_BOT_TOKEN"})
	require.Contains(out, "PATH=/usr/bin")
	for _, kv := range out {
		require.NotContains(
			kv, "top-secret",
			"configured token env leaked: %q", kv,
		)
	}
}

func TestHelperProcess(t *testing.T) {
	if os.Getenv("MIDDLEMAN_LOCALRUNTIME_HELPER") != "1" {
		return
	}
	args := os.Args
	helperArgs := args[len(args)-1:]
	for i, arg := range args {
		if arg == "--" && i+1 < len(args) {
			helperArgs = args[i+1:]
			break
		}
	}
	mode := helperArgs[0]
	switch mode {
	case "sleep":
		time.Sleep(time.Hour)
	case "sleep-record":
		if len(helperArgs) < 2 {
			os.Exit(2)
		}
		f, err := os.OpenFile(
			helperArgs[1],
			os.O_APPEND|os.O_CREATE|os.O_WRONLY,
			0o644,
		)
		if err != nil {
			os.Exit(2)
		}
		_, _ = fmt.Fprintf(f, "%d\n", os.Getpid())
		_ = f.Close()
		time.Sleep(time.Hour)
	case "spawn-child":
		if len(helperArgs) < 2 {
			os.Exit(2)
		}
		child := procutil.Command(
			os.Args[0], "-test.run=TestHelperProcess", "--", "sleep",
		)
		if err := child.Start(); err != nil {
			os.Exit(2)
		}
		f, err := os.OpenFile(
			helperArgs[1],
			os.O_APPEND|os.O_CREATE|os.O_WRONLY,
			0o644,
		)
		if err != nil {
			os.Exit(2)
		}
		_, _ = fmt.Fprintf(
			f, "%d\n%d\n", os.Getpid(), child.Process.Pid,
		)
		_ = f.Close()
		time.Sleep(time.Hour)
	case "exit":
		os.Exit(3)
	default:
		os.Exit(2)
	}
}
