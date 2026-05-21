package main

import (
	"bytes"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/wesm/middleman/internal/procutil"
	"github.com/wesm/middleman/internal/runtimelock"
)

// buildMiddleman compiles the middleman binary into a per-test temp dir
// and returns the absolute path. The build runs once per test via
// t.TempDir.
func buildMiddleman(t *testing.T) string {
	t.Helper()
	binDir := t.TempDir()
	binPath := filepath.Join(binDir, "middleman")
	cmd := procutil.Command("go", "build", "-o", binPath, ".")
	cmd.Stdout = os.Stderr
	cmd.Stderr = os.Stderr
	require.NoError(t, cmd.Run(), "go build ./cmd/middleman")
	return binPath
}

// reserveFreePort opens a listener on 127.0.0.1:0, closes it, and
// returns the port the kernel assigned. The window between Close and
// the subprocess's own Listen is wide in theory but is the same idiom
// used elsewhere in the repo for "pick me a free port".
func reserveFreePort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	port := ln.Addr().(*net.TCPAddr).Port
	require.NoError(t, ln.Close())
	return port
}

// writeMinimalConfig writes a config that binds to the chosen port
// with no provider repos. The dataDir is set so it does not collide
// with the developer's real ~/.config/middleman.
func writeMinimalConfig(t *testing.T, configPath, dataDir string, port int) {
	t.Helper()
	body := fmt.Sprintf(`host = "127.0.0.1"
port = %d
data_dir = %q
sync_interval = "5m"
github_token_env = "MIDDLEMAN_GITHUB_TOKEN_UNSET_FOR_LOCK_E2E"

[activity]
view_mode = "threaded"
time_range = "7d"

[terminal]
renderer = "xterm"
`, port, dataDir)
	require.NoError(t, os.WriteFile(configPath, []byte(body), 0o600))
}

func TestStartupLockCollisionAndStatus(t *testing.T) {
	require := require.New(t)

	bin := buildMiddleman(t)
	root := t.TempDir()
	dataDir := filepath.Join(root, "data")
	require.NoError(os.MkdirAll(dataDir, 0o700))
	cfgPath := filepath.Join(root, "config.toml")

	port := reserveFreePort(t)
	writeMinimalConfig(t, cfgPath, dataDir, port)

	startupLock, err := runtimelock.Acquire(dataDir)
	require.NoError(err)
	startupLockReleased := false
	t.Cleanup(func() {
		if !startupLockReleased {
			require.NoError(startupLock.Release())
		}
	})

	// `middleman status` while the lock is held before WriteMetadata:
	// reports running, but metadata is unavailable.
	startupStatusCmd := procutil.Command(bin, "status", "--config", cfgPath)
	var startupStatusOut bytes.Buffer
	startupStatusCmd.Stdout = &startupStatusOut
	startupStatusCmd.Stderr = os.Stderr
	require.NoError(startupStatusCmd.Run())
	require.Contains(startupStatusOut.String(), "running (metadata unavailable: missing")
	require.Contains(startupStatusOut.String(), dataDir)
	require.Contains(startupStatusOut.String(), runtimelock.LockPath(dataDir))

	// `middleman status --json`: same lock-held/missing-metadata state.
	startupJSONCmd := procutil.Command(bin, "status", "--json", "--config", cfgPath)
	var startupJSONOut bytes.Buffer
	startupJSONCmd.Stdout = &startupJSONOut
	startupJSONCmd.Stderr = os.Stderr
	require.NoError(startupJSONCmd.Run())
	require.Contains(startupJSONOut.String(), "\"running\": true")
	require.Contains(startupJSONOut.String(), "\"data_dir\": \""+dataDir+"\"")
	require.Contains(startupJSONOut.String(), "\"metadata\": null")
	require.Contains(startupJSONOut.String(), "\"metadata_error\": \"missing\"")

	require.NoError(startupLock.Release())
	startupLockReleased = true

	// First subprocess: should start and hold the lock. Don't use
	// CommandContext here because its default Cancel sends SIGKILL,
	// which bypasses signal.NotifyContext + defer chains in main.go and
	// leaves the metadata file behind. Send SIGTERM explicitly when we
	// want a graceful shutdown.
	first := procutil.Command(bin, "--config", cfgPath)
	first.Stdout = os.Stderr
	first.Stderr = os.Stderr
	first.Env = append(os.Environ(),
		"MIDDLEMAN_LOG_LEVEL=warn",
		"MIDDLEMAN_GITHUB_TOKEN_UNSET_FOR_LOCK_E2E=",
	)
	require.NoError(first.Start())
	firstStopped := false
	t.Cleanup(func() {
		if !firstStopped && first.Process != nil {
			_ = first.Process.Signal(syscall.SIGKILL)
			_ = first.Wait()
		}
	})

	// Wait until the metadata file appears (means Acquire +
	// WriteMetadata both completed).
	waitForFile(t, runtimelock.MetadataPath(dataDir), 10*time.Second)

	// Second subprocess against the same data_dir + port. Should exit 1
	// with the banner on stderr.
	second := procutil.Command(bin, "--config", cfgPath)
	var stderr bytes.Buffer
	second.Stderr = &stderr
	err = second.Run()
	require.Error(err)
	var exitErr *exec.ExitError
	require.ErrorAs(err, &exitErr)
	require.Equal(1, exitErr.ExitCode())
	require.Contains(stderr.String(), "another middleman instance is already running")
	require.Contains(stderr.String(), dataDir)
	require.Contains(stderr.String(), "running pid:")
	require.Contains(stderr.String(), "listening on: 127.0.0.1:"+strconv.Itoa(port))

	// `middleman status` against the same config: reports running with
	// metadata.
	statusCmd := procutil.Command(bin, "status", "--config", cfgPath)
	var statusOut bytes.Buffer
	statusCmd.Stdout = &statusOut
	statusCmd.Stderr = os.Stderr
	require.NoError(statusCmd.Run())
	require.Contains(statusOut.String(), "running")
	require.Contains(statusOut.String(), dataDir)
	require.Contains(statusOut.String(), "pid:")
	require.Contains(statusOut.String(), "port:         "+strconv.Itoa(port))

	// `middleman status --json`: same data, JSON shape.
	jsonCmd := procutil.Command(bin, "status", "--json", "--config", cfgPath)
	var jsonOut bytes.Buffer
	jsonCmd.Stdout = &jsonOut
	jsonCmd.Stderr = os.Stderr
	require.NoError(jsonCmd.Run())
	require.Contains(jsonOut.String(), "\"running\": true")
	require.Contains(jsonOut.String(), "\"data_dir\": \""+dataDir+"\"")
	require.Contains(jsonOut.String(), "\"port\": "+strconv.Itoa(port))

	// Shut down the first process gracefully so the deferred Release
	// path runs (which removes the metadata file). The kernel releases
	// the lock itself on exit.
	require.NoError(first.Process.Signal(syscall.SIGTERM))
	require.NoError(first.Wait())
	firstStopped = true

	// Wait for the metadata file to disappear (clean Release path).
	waitForNoFile(t, runtimelock.MetadataPath(dataDir), 10*time.Second)

	// `middleman status` now reports not-running.
	statusCmd2 := procutil.Command(bin, "status", "--config", cfgPath)
	var statusOut2 bytes.Buffer
	statusCmd2.Stdout = &statusOut2
	statusCmd2.Stderr = os.Stderr
	require.NoError(statusCmd2.Run())
	require.Contains(statusOut2.String(), "no running daemon")
}

func waitForFile(t *testing.T, path string, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(path); err == nil {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	require.FailNowf(t, "file did not appear", "path=%s timeout=%s", path, timeout)
}

func waitForNoFile(t *testing.T, path string, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(path); err != nil && os.IsNotExist(err) {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	require.FailNowf(t, "file did not disappear", "path=%s timeout=%s", path, timeout)
}
