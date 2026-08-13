package testtmux

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/forge/internal/procutil"
	"go.kenn.io/forge/internal/testutil/testsignal"
)

func requireTmux(t *testing.T) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("real tmux tests require Unix")
	}
	path, err := exec.LookPath("tmux")
	if err != nil {
		t.Skipf("tmux is unavailable: %v", err)
	}
	return path
}

func shortTempDir(t *testing.T) string {
	t.Helper()
	directory, err := os.MkdirTemp("/tmp", "kf-tmux-test-")
	require.NoError(t, err)
	t.Cleanup(func() { _ = os.RemoveAll(directory) })
	return directory
}

func startTmuxServer(
	t *testing.T,
	tmuxPath string,
	command []string,
) int {
	t.Helper()
	args := append(append([]string(nil), command[1:]...),
		"new-session", "-d", "-s", "fixture", "sleep 30",
	)
	output, err := procutil.Command(command[0], args...).CombinedOutput()
	require.NoError(t, err, string(output))
	pidArgs := append(append([]string(nil), command[1:]...),
		"display-message", "-p", "#{pid}",
	)
	output, err = procutil.Command(tmuxPath, pidArgs...).CombinedOutput()
	require.NoError(t, err, string(output))
	pid, err := strconv.Atoi(strings.TrimSpace(string(output)))
	require.NoError(t, err)
	return pid
}

func processGone(pid int) bool {
	command := procutil.Command("/bin/kill", "-0", strconv.Itoa(pid))
	return command.Run() != nil
}

func requireProcessGone(t *testing.T, pid int) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if processGone(pid) {
			return
		}
		time.Sleep(25 * time.Millisecond)
	}
	require.Fail(t, "process survived cleanup", "pid=%d", pid)
}

func TestParseRunName(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		want      processIdentity
		wantValid bool
	}{
		{
			name:      "run.31415.0123456789ab.abcdef",
			want:      processIdentity{pid: 31415, startToken: "0123456789ab"},
			wantValid: true,
		},
		{name: "run.0.0123456789ab.abcdef"},
		{name: "run.31415.short.abcdef"},
		{name: "run.31415.0123456789ab.bad/slash"},
		{name: "not-a-run"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, ok := parseRunName(tt.name)
			assert.Equal(t, tt.wantValid, ok)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestRunIsLiveRequiresMatchingProcessStart(t *testing.T) {
	t.Parallel()

	lookup := func(pid int) (string, error) {
		switch pid {
		case 101:
			return "same-start", nil
		case 202:
			return "reused-start", nil
		default:
			return "", errors.New("process not found")
		}
	}

	assert.True(t, runIsLive(
		processIdentity{pid: 101, startToken: tokenForStart("same-start")},
		lookup,
	))
	assert.False(t, runIsLive(
		processIdentity{pid: 202, startToken: tokenForStart("original-start")},
		lookup,
	), "a reused PID must not pin a dead run")
	assert.False(t, runIsLive(
		processIdentity{pid: 303, startToken: tokenForStart("gone")},
		lookup,
	))
}

func TestProcessStartIsStableForLiveProcess(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	if runtime.GOOS == "windows" {
		t.Skip("the Windows identity implementation is cross-compiled here")
	}
	first, err := processStart(os.Getpid())
	require.NoError(err)
	time.Sleep(10 * time.Millisecond)
	second, err := processStart(os.Getpid())
	require.NoError(err)
	assert.NotEmpty(first)
	assert.Equal(first, second)
}

func TestProcessStillMatchesRequiresOriginalStart(t *testing.T) {
	t.Parallel()
	assert := assert.New(t)

	lookup := func(pid int) (string, error) {
		if pid == 101 {
			return "current-start", nil
		}
		return "", errors.New("process not found")
	}
	assert.True(processStillMatches(101, "current-start", lookup))
	assert.False(processStillMatches(101, "prior-start", lookup))
	assert.False(processStillMatches(202, "gone", lookup))
}

func TestIdentityForSocketRequiresContainedRunPath(t *testing.T) {
	t.Parallel()
	assert := assert.New(t)

	root := t.TempDir()
	run := filepath.Join(root, "run.31415.0123456789ab.abcdef")
	inside := filepath.Join(run, "server-1", "tmux.sock")
	identity, ok := identityForSocket(root, inside)
	require.True(t, ok)
	assert.Equal(processIdentity{
		pid:        31415,
		startToken: "0123456789ab",
	}, identity)

	_, ok = identityForSocket(root, filepath.Join(root, "..", "escape", "tmux.sock"))
	assert.False(ok)
	_, ok = identityForSocket(root, filepath.Join(root, "unexpected", "tmux.sock"))
	assert.False(ok)
	_, ok = identityForSocket(root, "relative/tmux.sock")
	assert.False(ok)
}

func TestOwnerCleanupStopsRegisteredServerAndPreservesControl(t *testing.T) {
	tmuxPath := requireTmux(t)
	root := filepath.Join(shortTempDir(t), "owned")
	owner, err := newAt(root)
	require.NoError(t, err)
	t.Cleanup(func() { _ = owner.Cleanup() })

	ownedCommand := owner.Command(t, tmuxPath)
	ownedPID := startTmuxServer(t, tmuxPath, ownedCommand)

	controlDir := shortTempDir(t)
	controlSocket := filepath.Join(controlDir, "control.sock")
	controlCommand := []string{
		tmuxPath, "-f", "/dev/null", "-S", controlSocket,
	}
	controlPID := startTmuxServer(t, tmuxPath, controlCommand)
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = procutil.CommandContext(
			ctx, tmuxPath, "-S", controlSocket, "kill-server",
		).Run()
	})

	require.NoError(t, owner.Cleanup())
	requireProcessGone(t, ownedPID)
	assert.False(t, processGone(controlPID), "cleanup touched an unrelated server")
}

func TestOwnerCleanupStopsRegisteredServerAfterDirectoryDisappears(t *testing.T) {
	tmuxPath := requireTmux(t)
	owner, err := newAt(filepath.Join(shortTempDir(t), "owned"))
	require.NoError(t, err)
	command := owner.Command(t, tmuxPath)
	serverPID := startTmuxServer(t, tmuxPath, command)
	t.Cleanup(func() {
		if !processGone(serverPID) {
			_ = procutil.Command("/bin/kill", "-KILL", strconv.Itoa(serverPID)).Run()
		}
	})
	require.NoError(t, os.RemoveAll(owner.runDir))

	require.NoError(t, owner.Cleanup())
	requireProcessGone(t, serverPID)
}

func TestNewAtReapsServerAfterRunDirectoryDisappears(t *testing.T) {
	require := require.New(t)
	tmuxPath := requireTmux(t)
	root := filepath.Join(shortTempDir(t), "owned")
	require.NoError(os.MkdirAll(root, 0o700))
	staleIdentity := processIdentity{
		pid:        999999,
		startToken: tokenForStart("dead-owner"),
	}
	runName := fmt.Sprintf(
		"run.%d.%s.abcdef",
		staleIdentity.pid,
		staleIdentity.startToken,
	)
	runDir := filepath.Join(root, runName)
	serverDir := filepath.Join(runDir, "server-abcdef")
	require.NoError(os.MkdirAll(serverDir, 0o700))
	socket := filepath.Join(serverDir, "tmux.sock")
	command := []string{tmuxPath, "-f", "/dev/null", "-S", socket}
	serverPID := startTmuxServer(t, tmuxPath, command)
	t.Cleanup(func() {
		if !processGone(serverPID) {
			_ = procutil.Command("/bin/kill", "-KILL", strconv.Itoa(serverPID)).Run()
		}
	})

	require.NoError(os.RemoveAll(runDir))
	owner, err := newAt(root)
	require.NoError(err)
	t.Cleanup(func() { _ = owner.Cleanup() })
	requireProcessGone(t, serverPID)
}

func TestNewAtRefusesUnmarkedStaleRun(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	tmuxPath := requireTmux(t)
	root := filepath.Join(shortTempDir(t), "owned")
	runDir := filepath.Join(
		root,
		"run.999999."+tokenForStart("dead-owner")+".abcdef",
	)
	serverDir := filepath.Join(runDir, "server-abcdef")
	require.NoError(os.MkdirAll(serverDir, 0o700))
	socket := filepath.Join(serverDir, "tmux.sock")
	command := []string{tmuxPath, "-f", "/dev/null", "-S", socket}
	serverPID := startTmuxServer(t, tmuxPath, command)
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = procutil.CommandContext(ctx, tmuxPath, "-S", socket, "kill-server").Run()
	})

	owner, err := newAt(root)
	if owner != nil {
		t.Cleanup(func() { _ = owner.Cleanup() })
	}
	require.Error(err)
	assert.False(processGone(serverPID), "ambiguous stale ownership must fail closed")
	_, statErr := os.Stat(runDir)
	assert.NoError(statErr)
}

func TestSignalCleanupStopsRegisteredServer(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	if os.Getenv("KENN_FORGE_TEST_TMUX_SIGNAL_HELPER") == "1" {
		owner, err := newAt(os.Getenv("KENN_FORGE_TEST_TMUX_SIGNAL_ROOT"))
		require.NoError(err)
		command := owner.Command(t, os.Getenv("KENN_FORGE_TEST_TMUX_BINARY"))
		pid := startTmuxServer(t, command[0], command)
		require.NoError(os.WriteFile(
			os.Getenv("KENN_FORGE_TEST_TMUX_SIGNAL_PID"),
			[]byte(strconv.Itoa(pid)),
			0o600,
		))
		testsignal.Install(owner.Cleanup, nil)
		require.NoError(os.WriteFile(
			os.Getenv("KENN_FORGE_TEST_TMUX_SIGNAL_READY"),
			[]byte("ready\n"),
			0o600,
		))
		select {}
	}
	if runtime.GOOS == "windows" {
		t.Skip("SIGTERM cleanup requires Unix")
	}

	tmuxPath := requireTmux(t)
	directory := shortTempDir(t)
	ready := filepath.Join(directory, "ready")
	pidFile := filepath.Join(directory, "server-pid")
	command := procutil.Command(
		os.Args[0], "-test.run=^TestSignalCleanupStopsRegisteredServer$",
	)
	command.Env = append(os.Environ(),
		"KENN_FORGE_TEST_TMUX_SIGNAL_HELPER=1",
		"KENN_FORGE_TEST_TMUX_SIGNAL_ROOT="+filepath.Join(directory, "root"),
		"KENN_FORGE_TEST_TMUX_SIGNAL_READY="+ready,
		"KENN_FORGE_TEST_TMUX_SIGNAL_PID="+pidFile,
		"KENN_FORGE_TEST_TMUX_BINARY="+tmuxPath,
	)
	require.NoError(command.Start())
	t.Cleanup(func() {
		if command.ProcessState == nil {
			_ = command.Process.Kill()
			_, _ = command.Process.Wait()
		}
	})
	require.Eventually(func() bool {
		_, err := os.Stat(ready)
		return err == nil
	}, 5*time.Second, 25*time.Millisecond)
	content, err := os.ReadFile(pidFile)
	require.NoError(err)
	serverPID, err := strconv.Atoi(string(content))
	require.NoError(err)

	require.NoError(command.Process.Signal(syscall.SIGTERM))
	err = command.Wait()
	var exitErr *exec.ExitError
	require.ErrorAs(err, &exitErr)
	assert.Equal(143, exitErr.ExitCode())
	requireProcessGone(t, serverPID)
}
