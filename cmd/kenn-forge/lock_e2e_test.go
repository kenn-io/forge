package main

import (
	"bytes"
	"fmt"
	"io"
	"net"
	"net/http"
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
	"go.kenn.io/kit/daemon"

	"go.kenn.io/forge/internal/daemonruntime"
	"go.kenn.io/forge/internal/procutil"
	"go.kenn.io/forge/internal/runtimelock"
)

// buildMiddleman compiles the kenn-forge binary into a per-test temp dir
// and returns the absolute path. The build runs once per test via
// t.TempDir.
func buildForge(t *testing.T) string {
	t.Helper()
	binDir := t.TempDir()
	binPath := filepath.Join(binDir, "kenn-forge")
	cmd := procutil.Command("go", "build", "-o", binPath, ".")
	cmd.Stdout = os.Stderr
	cmd.Stderr = os.Stderr
	require.NoError(t, cmd.Run(), "go build ./cmd/kenn-forge")
	return binPath
}

// TestStartBackgroundSerializesAndReusesCompatibleRuntime protects the
// process-level lifecycle contract. If serialization or rediscovery breaks,
// simultaneous callers can launch competing children or a later call can
// replace the verified runtime instead of reusing it.
func TestStartBackgroundSerializesAndReusesCompatibleRuntime(t *testing.T) {
	require := require.New(t)
	bin := buildForge(t)
	root := t.TempDir()
	dataDir := filepath.Join(root, "data")
	require.NoError(os.MkdirAll(dataDir, 0o700))
	runtimeDir := filepath.Join(root, "config-home")
	require.NoError(os.MkdirAll(runtimeDir, 0o700))
	t.Setenv("KENN_FORGE_HOME", runtimeDir)
	configPath := filepath.Join(runtimeDir, "config.toml")
	writeMinimalConfig(t, configPath, "./data", reserveFreePort(t))

	type commandResult struct {
		stderr string
		err    error
	}
	runStart := func() commandResult {
		cmd := procutil.Command(bin, "start", "--background", "--config", configPath)
		cmd.Dir = root
		var stderr bytes.Buffer
		cmd.Stderr = &stderr
		cmd.Env = append(os.Environ(), "KENN_FORGE_LOG_LEVEL=warn")
		return commandResult{stderr: stderr.String(), err: cmd.Run()}
	}

	gate := make(chan struct{})
	results := make(chan commandResult, 2)
	for range 2 {
		go func() {
			<-gate
			results <- runStart()
		}()
	}
	close(gate)
	for range 2 {
		result := <-results
		require.NoError(result.err, result.stderr)
	}

	store := daemon.RuntimeStore{Dir: runtimeDir}
	records, err := store.List()
	require.NoError(err)
	require.Len(records, 1)
	record := records[0]
	t.Cleanup(func() {
		if process, findErr := os.FindProcess(record.PID); findErr == nil {
			_ = process.Kill()
		}
	})
	require.Equal("kenn-forge", record.Service)
	require.Equal(daemon.NetworkTCP, record.Network)
	canonicalDir, err := filepath.EvalSymlinks(dataDir)
	require.NoError(err)
	require.Equal(canonicalDir, record.Metadata["data_dir"])

	again := runStart()
	require.NoError(again.err, again.stderr)
	recordsAfter, err := store.List()
	require.NoError(err)
	require.Len(recordsAfter, 1)
	require.Equal(record.PID, recordsAfter[0].PID)
}

// TestForegroundAndBackgroundStartShareAuthToken protects a fresh data
// directory started through both lifecycle paths at once. If token creation
// has multiple winners, discovery cannot authenticate the runtime that won the
// authoritative data-directory lock.
func TestForegroundAndBackgroundStartShareAuthToken(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	bin := buildForge(t)
	root := t.TempDir()
	dataDir := filepath.Join(root, "data")
	runtimeDir := filepath.Join(root, "config-home")
	require.NoError(os.MkdirAll(runtimeDir, 0o700))
	t.Setenv("KENN_FORGE_HOME", runtimeDir)
	configPath := filepath.Join(runtimeDir, "config.toml")
	writeMinimalConfig(t, configPath, dataDir, reserveFreePort(t))
	store := daemon.RuntimeStore{Dir: runtimeDir}

	foreground := procutil.Command(bin, "serve", "--config", configPath)
	var foregroundStderr bytes.Buffer
	foreground.Stderr = &foregroundStderr
	foreground.Env = append(os.Environ(), "KENN_FORGE_LOG_LEVEL=warn")
	background := procutil.Command(
		bin, "start", "--background", "--config", configPath,
	)
	var backgroundStderr bytes.Buffer
	background.Stderr = &backgroundStderr
	background.Env = append(os.Environ(), "KENN_FORGE_LOG_LEVEL=warn")
	t.Cleanup(func() {
		records, _ := store.List()
		for _, record := range records {
			if process, findErr := os.FindProcess(record.PID); findErr == nil {
				_ = process.Kill()
			}
		}
		if foreground.Process != nil {
			_ = foreground.Process.Kill()
			_ = foreground.Wait()
		}
	})

	gate := make(chan struct{})
	foregroundStarted := make(chan error, 1)
	backgroundDone := make(chan error, 1)
	go func() {
		<-gate
		foregroundStarted <- foreground.Start()
	}()
	go func() {
		<-gate
		backgroundDone <- background.Run()
	}()
	close(gate)
	require.NoError(<-foregroundStarted)
	require.NoError(<-backgroundDone, backgroundStderr.String())

	records, err := store.List()
	require.NoError(err)
	require.Len(records, 1)
	record := records[0]
	token, err := runtimelock.ReadAuthToken(dataDir)
	require.NoError(err)
	proof, err := daemon.NewProof([]byte(token))
	require.NoError(err)
	ping, err := proof.Probe(t.Context(), record, daemon.ProbeOptions{
		Path: daemonruntime.ProofPingPath,
	})
	require.NoError(err)
	assert.Equal(record.PID, ping.PID)
}

// TestStartBackgroundReportsConfiguredBasePath protects the operator-facing
// URL. If discovery drops the startup-bound prefix, the reported location
// points at an unmounted route and returns 404.
func TestStartBackgroundReportsConfiguredBasePath(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	bin := buildForge(t)
	root := t.TempDir()
	dataDir := filepath.Join(root, "data")
	runtimeDir := filepath.Join(root, "config-home")
	require.NoError(os.MkdirAll(runtimeDir, 0o700))
	t.Setenv("KENN_FORGE_HOME", runtimeDir)
	configPath := filepath.Join(runtimeDir, "config.toml")
	port := reserveFreePort(t)
	writeMinimalConfigWithBasePath(
		t, configPath, dataDir, port, "/console/",
	)
	store := daemon.RuntimeStore{Dir: runtimeDir}
	t.Cleanup(func() {
		records, _ := store.List()
		for _, record := range records {
			if process, findErr := os.FindProcess(record.PID); findErr == nil {
				_ = process.Kill()
			}
		}
	})

	cmd := procutil.Command(
		bin, "start", "--background", "--config", configPath,
	)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	cmd.Env = append(os.Environ(), "KENN_FORGE_LOG_LEVEL=warn")
	require.NoError(cmd.Run(), stderr.String())

	records, err := store.List()
	require.NoError(err)
	require.Len(records, 1)
	wantURL := fmt.Sprintf("http://127.0.0.1:%d/console", port)
	require.Contains(strings.TrimSpace(stdout.String()), wantURL+" (pid ")
	client := &http.Client{Timeout: 5 * time.Second}
	request, err := http.NewRequestWithContext(
		t.Context(), http.MethodGet, wantURL, nil,
	)
	require.NoError(err)
	response, err := client.Do(request)
	require.NoError(err)
	t.Cleanup(func() { require.NoError(response.Body.Close()) })
	_, err = io.Copy(io.Discard, response.Body)
	require.NoError(err)
	assert.Equal(http.StatusOK, response.StatusCode)
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
// with the developer's real ~/.kenn/forge.
func writeMinimalConfig(t *testing.T, configPath, dataDir string, port int) {
	t.Helper()
	writeMinimalConfigWithBasePath(t, configPath, dataDir, port, "")
}

func writeMinimalConfigWithBasePath(
	t *testing.T, configPath, dataDir string, port int, basePath string,
) {
	t.Helper()
	binDir := t.TempDir()
	ghName := "gh"
	ghBody := "#!/bin/sh\nexit 1\n"
	if runtime.GOOS == "windows" {
		ghName = "gh.bat"
		ghBody = "@exit /b 1\r\n"
	}
	require.NoError(t, os.WriteFile(
		filepath.Join(binDir, ghName), []byte(ghBody), 0o700,
	))
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("KENN_FORGE_GITHUB_TOKEN_UNSET_FOR_LOCK_E2E", "")
	basePathConfig := ""
	if basePath != "" {
		basePathConfig = fmt.Sprintf("base_path = %q\n", basePath)
	}
	body := fmt.Sprintf(`host = "127.0.0.1"
port = %d
data_dir = %q
%ssync_interval = "5m"
github_token_env = "KENN_FORGE_GITHUB_TOKEN_UNSET_FOR_LOCK_E2E"

[activity]
view_mode = "threaded"
time_range = "7d"

[terminal]
`, port, dataDir, basePathConfig)
	require.NoError(t, os.WriteFile(configPath, []byte(body), 0o600))
}

func TestStartupLockCollisionAndStatus(t *testing.T) {
	require := require.New(t)

	bin := buildForge(t)
	root := t.TempDir()
	dataDir := filepath.Join(root, "data")
	require.NoError(os.MkdirAll(dataDir, 0o700))
	canonicalDataDir, err := filepath.EvalSymlinks(dataDir)
	require.NoError(err)
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

	// `kenn-forge status` while the lock is held before WriteMetadata:
	// reports running, but metadata is unavailable.
	startupStatusCmd := procutil.Command(bin, "status", "--config", cfgPath)
	var startupStatusOut bytes.Buffer
	startupStatusCmd.Stdout = &startupStatusOut
	startupStatusCmd.Stderr = os.Stderr
	require.NoError(startupStatusCmd.Run())
	require.Contains(startupStatusOut.String(), "running (metadata unavailable: missing")
	require.Contains(startupStatusOut.String(), dataDir)
	require.Contains(startupStatusOut.String(), runtimelock.LockPath(dataDir))

	// `kenn-forge status --json`: same lock-held/missing-metadata state.
	startupJSONCmd := procutil.Command(bin, "status", "--json", "--config", cfgPath)
	var startupJSONOut bytes.Buffer
	startupJSONCmd.Stdout = &startupJSONOut
	startupJSONCmd.Stderr = os.Stderr
	require.NoError(startupJSONCmd.Run())
	require.Contains(startupJSONOut.String(), "\"running\": true")
	require.Contains(startupJSONOut.String(), "\"data_dir\": \""+canonicalDataDir+"\"")
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
		"KENN_FORGE_LOG_LEVEL=warn",
		"KENN_FORGE_GITHUB_TOKEN_UNSET_FOR_LOCK_E2E=",
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
	require.Contains(stderr.String(), "another kenn-forge instance is already running")
	require.Contains(stderr.String(), dataDir)
	require.Contains(stderr.String(), "running pid:")
	require.Contains(stderr.String(), "listening on: 127.0.0.1:"+strconv.Itoa(port))

	// `kenn-forge status` against the same config: reports running with
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

	// `kenn-forge status --json`: same data, JSON shape.
	jsonCmd := procutil.Command(bin, "status", "--json", "--config", cfgPath)
	var jsonOut bytes.Buffer
	jsonCmd.Stdout = &jsonOut
	jsonCmd.Stderr = os.Stderr
	require.NoError(jsonCmd.Run())
	require.Contains(jsonOut.String(), "\"running\": true")
	require.Contains(jsonOut.String(), "\"data_dir\": \""+canonicalDataDir+"\"")
	require.Contains(jsonOut.String(), "\"port\": "+strconv.Itoa(port))

	// Shut down the first process gracefully so the deferred Release
	// path runs (which removes the metadata file). The kernel releases
	// the lock itself on exit.
	require.NoError(first.Process.Signal(syscall.SIGTERM))
	require.NoError(first.Wait())
	firstStopped = true

	// Wait for the metadata file to disappear (clean Release path).
	waitForNoFile(t, runtimelock.MetadataPath(dataDir), 10*time.Second)

	// `kenn-forge status` now reports not-running.
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
