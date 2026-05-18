//go:build !windows

package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	Assert "github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/wesm/middleman/internal/config"
	_ "modernc.org/sqlite"
)

func TestPrepareEphemeralConfigOverridesPortAndDataDir(t *testing.T) {
	assert := Assert.New(t)
	require := require.New(t)
	dir := t.TempDir()
	sourcePath := filepath.Join(dir, "source.toml")
	sourceDataDir := filepath.Join(dir, "source-data")
	workDir := filepath.Join(dir, "run")
	require.NoError(os.MkdirAll(workDir, 0o700))

	source := config.Config{
		SyncInterval:        "5m",
		GitHubTokenEnv:      "MIDDLEMAN_GITHUB_TOKEN",
		DefaultPlatformHost: "github.com",
		Host:                "127.0.0.1",
		Port:                8091,
		DataDir:             sourceDataDir,
		Activity:            config.Activity{ViewMode: "threaded", TimeRange: "7d"},
	}
	require.NoError(source.Save(sourcePath))

	prepared, err := prepareEphemeralConfig(ephemeralOptions{
		sourceConfigPath: sourcePath,
		workDir:          workDir,
		backendPort:      39101,
		frontendPort:     39102,
	})
	require.NoError(err)

	reloaded, err := config.Load(prepared.configPath)
	require.NoError(err)
	assert.Equal(39101, reloaded.Port)
	assert.Equal(filepath.Join(workDir, "data"), reloaded.DataDir)
	assert.Equal(filepath.Join(workDir, "dev-ephemeral.json"), prepared.statusPath)
	assert.Equal("http://127.0.0.1:39101", prepared.backendURL)
	assert.Equal("http://127.0.0.1:39102", prepared.frontendURL)
	assert.Equal(sourceDataDir, source.DataDir)
}

func TestPrepareEphemeralConfigForcesBackendToLoopback(t *testing.T) {
	assert := Assert.New(t)
	require := require.New(t)
	dir := t.TempDir()
	sourcePath := filepath.Join(dir, "source.toml")

	source := config.Config{
		SyncInterval:        "5m",
		GitHubTokenEnv:      "MIDDLEMAN_GITHUB_TOKEN",
		DefaultPlatformHost: "github.com",
		Host:                "::1",
		Port:                8091,
		DataDir:             filepath.Join(dir, "source-data"),
		Activity:            config.Activity{ViewMode: "threaded", TimeRange: "7d"},
	}
	require.NoError(source.Save(sourcePath))

	prepared, err := prepareEphemeralConfig(ephemeralOptions{
		sourceConfigPath: sourcePath,
		workDir:          filepath.Join(dir, "run"),
		backendPort:      39131,
		frontendPort:     39132,
	})
	require.NoError(err)

	reloaded, err := config.Load(prepared.configPath)
	require.NoError(err)
	assert.Equal("127.0.0.1", reloaded.Host)
	assert.Equal("http://127.0.0.1:39131", prepared.backendURL)
	assert.Equal("::1", source.Host)
}

func TestPrepareEphemeralConfigCopiesSourceDatabaseByDefault(t *testing.T) {
	require := require.New(t)
	dir := t.TempDir()
	sourcePath := filepath.Join(dir, "source.toml")
	sourceDataDir := filepath.Join(dir, "source-data")
	workDir := filepath.Join(dir, "run")

	source := config.Config{
		SyncInterval:        "5m",
		GitHubTokenEnv:      "MIDDLEMAN_GITHUB_TOKEN",
		DefaultPlatformHost: "github.com",
		Host:                "127.0.0.1",
		Port:                8091,
		DataDir:             sourceDataDir,
		Activity:            config.Activity{ViewMode: "threaded", TimeRange: "7d"},
	}
	require.NoError(os.MkdirAll(sourceDataDir, 0o700))
	require.NoError(source.Save(sourcePath))
	writeSQLiteMarker(t, source.DBPath(), "copied state")

	prepared, err := prepareEphemeralConfig(ephemeralOptions{
		sourceConfigPath: sourcePath,
		workDir:          workDir,
		backendPort:      39111,
		frontendPort:     39112,
	})
	require.NoError(err)

	Assert.Equal(t, "copied state", readSQLiteMarker(t, filepath.Join(prepared.dataDir, "middleman.db")))
}

func TestPrepareEphemeralDatabaseRejectsSourceDestinationMatch(t *testing.T) {
	require := require.New(t)
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "middleman.db")
	writeSQLiteMarker(t, dbPath, "preserve me")

	err := prepareEphemeralDatabase(dbPath, dbPath, true)
	require.Error(err)

	Assert.Contains(t, err.Error(), "source and destination database are the same")
	Assert.Equal(t, "preserve me", readSQLiteMarker(t, dbPath))
}

func TestPrepareEphemeralDatabaseRejectsSymlinkedSourceDestinationMatch(t *testing.T) {
	require := require.New(t)
	dir := t.TempDir()
	realDir := filepath.Join(dir, "real")
	linkDir := filepath.Join(dir, "link")
	require.NoError(os.MkdirAll(realDir, 0o700))
	require.NoError(os.Symlink(realDir, linkDir))

	sourcePath := filepath.Join(realDir, "middleman.db")
	destPath := filepath.Join(linkDir, "middleman.db")
	writeSQLiteMarker(t, sourcePath, "preserve me")

	err := prepareEphemeralDatabase(sourcePath, destPath, true)
	require.Error(err)

	Assert.Contains(t, err.Error(), "source and destination database are the same")
	Assert.Equal(t, "preserve me", readSQLiteMarker(t, sourcePath))
}

func TestPrepareEphemeralConfigCanStartWithFreshDatabase(t *testing.T) {
	require := require.New(t)
	dir := t.TempDir()
	sourcePath := filepath.Join(dir, "source.toml")
	sourceDataDir := filepath.Join(dir, "source-data")
	workDir := filepath.Join(dir, "run")

	source := config.Config{
		SyncInterval:        "5m",
		GitHubTokenEnv:      "MIDDLEMAN_GITHUB_TOKEN",
		DefaultPlatformHost: "github.com",
		Host:                "127.0.0.1",
		Port:                8091,
		DataDir:             sourceDataDir,
		Activity:            config.Activity{ViewMode: "threaded", TimeRange: "7d"},
	}
	require.NoError(os.MkdirAll(sourceDataDir, 0o700))
	require.NoError(source.Save(sourcePath))
	writeSQLiteMarker(t, source.DBPath(), "do not copy")

	prepared, err := prepareEphemeralConfig(ephemeralOptions{
		sourceConfigPath: sourcePath,
		workDir:          workDir,
		backendPort:      39121,
		frontendPort:     39122,
		freshDB:          true,
	})
	require.NoError(err)

	_, err = os.Stat(filepath.Join(prepared.dataDir, "middleman.db"))
	require.ErrorIs(err, os.ErrNotExist)
}

func TestPrepareEphemeralConfigKeepsBasePathInBackendURL(t *testing.T) {
	require := require.New(t)
	dir := t.TempDir()
	sourcePath := filepath.Join(dir, "source.toml")

	source := config.Config{
		SyncInterval:        "5m",
		GitHubTokenEnv:      "MIDDLEMAN_GITHUB_TOKEN",
		DefaultPlatformHost: "github.com",
		Host:                "127.0.0.1",
		Port:                8091,
		BasePath:            "/middleman/",
		DataDir:             filepath.Join(dir, "source-data"),
		Activity:            config.Activity{ViewMode: "threaded", TimeRange: "7d"},
	}
	require.NoError(source.Save(sourcePath))

	prepared, err := prepareEphemeralConfig(ephemeralOptions{
		sourceConfigPath: sourcePath,
		workDir:          filepath.Join(dir, "run"),
		backendPort:      39201,
		frontendPort:     39202,
	})
	require.NoError(err)

	Assert.Equal(t, "http://127.0.0.1:39201/middleman", prepared.backendURL)
}

func TestBuildCommandSpecsWiresEphemeralEnvironment(t *testing.T) {
	assert := Assert.New(t)
	t.Setenv("PATH", "/bin")
	t.Setenv("HOME", "/tmp/home")
	t.Setenv("TMPDIR", "/tmp")
	t.Setenv("MIDDLEMAN_VITE_HMR_HOST", "dev.example.test")
	t.Setenv("MIDDLEMAN_GITHUB_TOKEN", "secret-token")
	t.Setenv("GH_TOKEN", "secret-gh")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "secret-aws")
	t.Setenv("OPENAI_API_KEY", "secret-openai")
	t.Setenv("AWS_ACCESS_KEY_ID", "secret-access-key")
	t.Setenv("GITHUB_PAT", "secret-pat")
	t.Setenv("SESSION_COOKIE", "secret-cookie")
	t.Setenv("PLAIN_FRONTEND_SETTING", "kept")

	specs := buildCommandSpecs(ephemeralRun{
		configPath:   "/tmp/middleman-dev/config.toml",
		backendURL:   "http://127.0.0.1:39301",
		frontendPort: 39302,
		logDir:       "/tmp/middleman-dev/logs",
	}, []string{"--host", "127.0.0.1"})

	assert.Equal("./scripts/dev-stack-backend.sh", specs.backend.name)
	assert.Contains(specs.backend.env, "MIDDLEMAN_CONFIG=/tmp/middleman-dev/config.toml")
	assert.Contains(specs.backend.env, "MIDDLEMAN_LOG_FILE=/tmp/middleman-dev/logs/backend-dev.log")
	assert.Equal("./scripts/frontend-dev.sh", specs.frontend.name)
	assert.Equal([]string{"--port", "39302", "--host", "127.0.0.1"}, specs.frontend.args)
	assert.Contains(specs.frontend.env, "MIDDLEMAN_CONFIG=/tmp/middleman-dev/config.toml")
	assert.Contains(specs.frontend.env, "MIDDLEMAN_API_URL=http://127.0.0.1:39301")
	assert.Contains(specs.frontend.env, "PATH=/bin")
	assert.Contains(specs.frontend.env, "HOME=/tmp/home")
	assert.Contains(specs.frontend.env, "TMPDIR=/tmp")
	assert.Contains(specs.frontend.env, "MIDDLEMAN_VITE_HMR_HOST=dev.example.test")
	assert.NotContains(specs.frontend.env, "PLAIN_FRONTEND_SETTING=kept")
	assert.NotContains(specs.frontend.env, "MIDDLEMAN_GITHUB_TOKEN=secret-token")
	assert.NotContains(specs.frontend.env, "GH_TOKEN=secret-gh")
	assert.NotContains(specs.frontend.env, "AWS_SECRET_ACCESS_KEY=secret-aws")
	assert.NotContains(specs.frontend.env, "OPENAI_API_KEY=secret-openai")
	assert.NotContains(specs.frontend.env, "AWS_ACCESS_KEY_ID=secret-access-key")
	assert.NotContains(specs.frontend.env, "GITHUB_PAT=secret-pat")
	assert.NotContains(specs.frontend.env, "SESSION_COOKIE=secret-cookie")
	assert.Contains(specs.backend.env, "MIDDLEMAN_GITHUB_TOKEN=secret-token")
	assert.Contains(specs.backend.env, "OPENAI_API_KEY=secret-openai")
}

func TestBuildCommandSpecsReferenceExecutableScripts(t *testing.T) {
	repoRoot := repoRoot(t)
	assertExecutable(t, filepath.Join(repoRoot, "scripts", "dev-stack-backend.sh"))
	assertExecutable(t, filepath.Join(repoRoot, "scripts", "frontend-dev.sh"))
}

func TestWriteStatusFileRecordsPIDsAndPortsNextToConfig(t *testing.T) {
	assert := Assert.New(t)
	require := require.New(t)
	dir := t.TempDir()
	statusPath := filepath.Join(dir, "dev-ephemeral.json")

	err := writeStatusFile(statusPath, ephemeralStatus{
		PID:          1001,
		BackendPID:   1002,
		FrontendPID:  1003,
		BackendPort:  39401,
		FrontendPort: 39402,
		ConfigPath:   filepath.Join(dir, "config.toml"),
		DataDir:      filepath.Join(dir, "data"),
		BackendURL:   "http://127.0.0.1:39401",
		FrontendURL:  "http://127.0.0.1:39402",
	})
	require.NoError(err)

	content, err := os.ReadFile(statusPath)
	require.NoError(err)

	var got ephemeralStatus
	require.NoError(json.Unmarshal(content, &got))
	assert.Equal(1001, got.PID)
	assert.Equal(1002, got.BackendPID)
	assert.Equal(1003, got.FrontendPID)
	assert.Equal(39401, got.BackendPort)
	assert.Equal(39402, got.FrontendPort)
	assert.Equal(filepath.Join(dir, "config.toml"), got.ConfigPath)
}

func TestResolveRunWorkDirDefaultsToStableDirectory(t *testing.T) {
	workDir, err := resolveRunWorkDir("")
	require.NoError(t, err)

	Assert.Equal(t, filepath.Join("tmp", "dev-ephemeral"), workDir)
}

func TestResolveStopStatusPathDefaultsToStableStatusPath(t *testing.T) {
	statusPath, err := resolveStopStatusPath("", "")
	require.NoError(t, err)

	Assert.Equal(t, filepath.Join("tmp", "dev-ephemeral", "dev-ephemeral.json"), statusPath)
}

func TestLockEphemeralWorkDirRejectsConcurrentLock(t *testing.T) {
	require := require.New(t)
	dir := t.TempDir()

	release, err := lockEphemeralWorkDir(dir)
	require.NoError(err)

	_, err = lockEphemeralWorkDir(dir)
	require.Error(err)
	Assert.Contains(t, err.Error(), "ephemeral work directory is locked")

	require.NoError(release())
	release, err = lockEphemeralWorkDir(dir)
	require.NoError(err)
	require.NoError(release())
}

func TestReadRunningEphemeralStatusReturnsLiveStatus(t *testing.T) {
	require := require.New(t)
	dir := t.TempDir()
	statusPath := filepath.Join(dir, "dev-ephemeral.json")
	scriptPath := filepath.Join(dir, "blocking.sh")
	writeBlockingScript(t, scriptPath)
	backend, _ := startTestCommand(t, commandSpec{name: scriptPath})
	frontend, _ := startTestCommand(t, commandSpec{name: scriptPath})
	launcherStartedAt, err := processStartTime(os.Getpid())
	require.NoError(err)
	backendStartedAt, err := processStartTime(backend.Process.Pid)
	require.NoError(err)
	frontendStartedAt, err := processStartTime(frontend.Process.Pid)
	require.NoError(err)
	err = writeStatusFile(statusPath, ephemeralStatus{
		PID:               os.Getpid(),
		PIDStartedAt:      launcherStartedAt,
		BackendPID:        backend.Process.Pid,
		BackendStartedAt:  backendStartedAt,
		FrontendPID:       frontend.Process.Pid,
		FrontendStartedAt: frontendStartedAt,
		BackendURL:        "http://127.0.0.1:39411",
		FrontendURL:       "http://127.0.0.1:39412",
	})
	require.NoError(err)

	status, running, err := readRunningEphemeralStatus(statusPath)
	require.NoError(err)

	assert := Assert.New(t)
	assert.True(running)
	assert.Equal(os.Getpid(), status.PID)
	assert.Equal("http://127.0.0.1:39411", status.BackendURL)
}

func TestReadRunningEphemeralStatusRemovesStaleStatus(t *testing.T) {
	require := require.New(t)
	dir := t.TempDir()
	statusPath := filepath.Join(dir, "dev-ephemeral.json")
	err := writeStatusFile(statusPath, ephemeralStatus{
		PID:         0,
		BackendPID:  0,
		FrontendPID: 0,
	})
	require.NoError(err)

	_, running, err := readRunningEphemeralStatus(statusPath)
	require.NoError(err)

	assert := Assert.New(t)
	assert.False(running)
	_, err = os.Stat(statusPath)
	assert.ErrorIs(err, os.ErrNotExist)
}

func TestReadRunningEphemeralStatusStopsPartialStackAndRemovesStatus(t *testing.T) {
	assert := Assert.New(t)
	require := require.New(t)
	dir := t.TempDir()
	statusPath := filepath.Join(dir, "dev-ephemeral.json")
	scriptPath := filepath.Join(dir, "blocking.sh")
	writeBlockingScript(t, scriptPath)
	cmd, waitCh := startTestCommand(t, commandSpec{name: scriptPath})
	startedAt, err := processStartTime(cmd.Process.Pid)
	require.NoError(err)

	err = writeStatusFile(statusPath, ephemeralStatus{
		BackendPID:       cmd.Process.Pid,
		BackendStartedAt: startedAt,
		BackendURL:       "http://127.0.0.1:39411",
		FrontendURL:      "http://127.0.0.1:39412",
	})
	require.NoError(err)

	_, running, err := readRunningEphemeralStatus(statusPath)
	require.NoError(err)

	assert.False(running)
	waitForCommandExit(t, cmd, waitCh)
	_, err = os.Stat(statusPath)
	assert.ErrorIs(err, os.ErrNotExist)
}

func TestStopEphemeralStackTreatsMissingStatusAsStopped(t *testing.T) {
	err := stopEphemeralStack(filepath.Join(t.TempDir(), "dev-ephemeral.json"))

	require.NoError(t, err)
}

func TestStopEphemeralStackWaitsForProcessesBeforeRemovingStatus(t *testing.T) {
	assert := Assert.New(t)
	require := require.New(t)
	dir := t.TempDir()
	statusPath := filepath.Join(dir, "dev-ephemeral.json")
	scriptPath := filepath.Join(dir, "ignore-int.sh")
	writeInterruptIgnoringScript(t, scriptPath)
	cmd, waitCh := startTestCommand(t, commandSpec{name: scriptPath})
	startedAt, err := processStartTime(cmd.Process.Pid)
	require.NoError(err)

	err = writeStatusFile(statusPath, ephemeralStatus{
		BackendPID:       cmd.Process.Pid,
		BackendStartedAt: startedAt,
	})
	require.NoError(err)

	err = stopEphemeralStack(statusPath)
	require.NoError(err)

	waitForCommandExit(t, cmd, waitCh)
	_, err = os.Stat(statusPath)
	assert.ErrorIs(err, os.ErrNotExist)
}

func TestStopEphemeralStackDoesNotSignalMismatchedProcessIdentity(t *testing.T) {
	assert := Assert.New(t)
	require := require.New(t)
	dir := t.TempDir()
	statusPath := filepath.Join(dir, "dev-ephemeral.json")
	scriptPath := filepath.Join(dir, "blocking.sh")
	writeBlockingScript(t, scriptPath)
	cmd, waitCh := startTestCommand(t, commandSpec{name: scriptPath})

	content := fmt.Appendf(nil,
		`{"backend_pid":%d,"backend_started_at":"definitely-not-%s"}`+"\n",
		cmd.Process.Pid,
		filepath.Base(scriptPath),
	)
	require.NoError(os.WriteFile(statusPath, content, 0o644))

	err := stopEphemeralStack(statusPath)
	require.NoError(err)

	running, err := processRunning(cmd.Process.Pid)
	require.NoError(err)
	assert.True(running)
	_, err = os.Stat(statusPath)
	require.ErrorIs(err, os.ErrNotExist)

	stopProcess(cmd.Process)
	waitForCommandExit(t, cmd, waitCh)
}

func TestWaitForCommandsEscalatesIgnoredInterrupt(t *testing.T) {
	require := require.New(t)
	dir := t.TempDir()
	backendScript := filepath.Join(dir, "backend.sh")
	frontendScript := filepath.Join(dir, "frontend.sh")
	writeInterruptIgnoringScript(t, backendScript)
	writeBlockingScript(t, frontendScript)
	backend, err := startCommand(context.Background(), commandSpec{name: backendScript})
	require.NoError(err)
	frontend, err := startCommand(context.Background(), commandSpec{name: frontendScript})
	require.NoError(err)
	t.Cleanup(func() {
		stopProcess(backend.Process)
		stopProcess(frontend.Process)
	})

	cancelCtx, cancel := context.WithCancel(context.Background())
	cancel()
	err = waitForCommands(cancelCtx, backend, frontend)
	require.NoError(err)

	backendRunning, err := processRunning(backend.Process.Pid)
	require.NoError(err)
	frontendRunning, err := processRunning(frontend.Process.Pid)
	require.NoError(err)
	Assert.False(t, backendRunning)
	Assert.False(t, frontendRunning)
}

func TestStopStartedCommandsEscalatesAndWaits(t *testing.T) {
	require := require.New(t)
	dir := t.TempDir()
	scriptPath := filepath.Join(dir, "ignore-int.sh")
	writeInterruptIgnoringScript(t, scriptPath)
	cmd, err := startCommand(context.Background(), commandSpec{name: scriptPath})
	require.NoError(err)
	t.Cleanup(func() { stopProcess(cmd.Process) })

	errs := stopStartedCommands(cmd)
	require.Empty(errs)

	running, err := processRunning(cmd.Process.Pid)
	require.NoError(err)
	Assert.False(t, running)
}

func TestRunWritesStatusAndReusesLiveDefaultStack(t *testing.T) {
	assert := Assert.New(t)
	require := require.New(t)
	dir := t.TempDir()
	sourcePath := filepath.Join(dir, "source.toml")
	sourceDataDir := filepath.Join(dir, "source-data")
	workDir := filepath.Join(dir, "run")

	source := config.Config{
		SyncInterval:        "5m",
		GitHubTokenEnv:      "MIDDLEMAN_GITHUB_TOKEN",
		DefaultPlatformHost: "github.com",
		Host:                "127.0.0.1",
		Port:                8091,
		DataDir:             sourceDataDir,
		Activity:            config.Activity{ViewMode: "threaded", TimeRange: "7d"},
	}
	require.NoError(os.MkdirAll(sourceDataDir, 0o700))
	require.NoError(source.Save(sourcePath))
	writeSQLiteMarker(t, source.DBPath(), "workflow state")

	commandDir := filepath.Join(dir, "commands")
	writeBlockingScript(t, filepath.Join(commandDir, "scripts", "dev-stack-backend.sh"))
	writeBlockingScript(t, filepath.Join(commandDir, "scripts", "frontend-dev.sh"))

	oldDir, err := os.Getwd()
	require.NoError(err)
	require.NoError(os.Chdir(commandDir))
	t.Cleanup(func() {
		require.NoError(os.Chdir(oldDir))
	})

	ctx := t.Context()
	errCh := make(chan error, 1)
	go func() {
		errCh <- run(ctx, []string{
			"-config", sourcePath,
			"-work-dir", workDir,
			"-backend-port", "39501",
			"-frontend-port", "39502",
			"--",
			"--host", "127.0.0.1",
		})
	}()

	statusPath := filepath.Join(workDir, "dev-ephemeral.json")
	status := waitForStatusFile(t, statusPath)
	assert.NotZero(status.PID)
	assert.NotZero(status.BackendPID)
	assert.NotZero(status.FrontendPID)
	assert.Equal("http://127.0.0.1:39501", status.BackendURL)
	assert.Equal("http://127.0.0.1:39502", status.FrontendURL)
	assert.Equal("workflow state", readSQLiteMarker(t, filepath.Join(workDir, "data", "middleman.db")))

	require.NoError(run(context.Background(), []string{
		"-config", sourcePath,
		"-work-dir", workDir,
		"-backend-port", "39503",
		"-frontend-port", "39504",
	}))
	reused := readStatusFile(t, statusPath)
	assert.Equal(status.BackendPID, reused.BackendPID)
	assert.Equal(status.FrontendPID, reused.FrontendPID)

	status.PID = 0
	require.NoError(writeStatusFile(statusPath, status))
	require.NoError(stopEphemeralStack(statusPath))
	select {
	case err := <-errCh:
		require.NoError(err)
	case <-time.After(5 * time.Second):
		require.Fail("timed out waiting for dev-ephemeral run to stop")
	}
}

func assertExecutable(t *testing.T, path string) {
	t.Helper()
	info, err := os.Stat(path)
	require.NoError(t, err)
	Assert.NotZero(t, info.Mode().Perm()&0o111, "%s must be executable", path)
}

func repoRoot(t *testing.T) string {
	t.Helper()
	wd, err := os.Getwd()
	require.NoError(t, err)
	return filepath.Clean(filepath.Join(wd, "..", ".."))
}

func writeBlockingScript(t *testing.T, path string) {
	t.Helper()
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o700))
	content := []byte("#!/usr/bin/env sh\ntrap 'exit 0' INT TERM\nwhile :; do sleep 1; done\n")
	require.NoError(t, os.WriteFile(path, content, 0o700))
}

func writeInterruptIgnoringScript(t *testing.T, path string) {
	t.Helper()
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o700))
	content := []byte("#!/usr/bin/env sh\ntrap '' INT\ntrap 'exit 0' TERM\nwhile :; do sleep 1; done\n")
	require.NoError(t, os.WriteFile(path, content, 0o700))
}

func startTestCommand(t *testing.T, spec commandSpec) (*exec.Cmd, <-chan error) {
	t.Helper()
	cmd, err := startCommand(context.Background(), spec)
	require.NoError(t, err)
	waitCh := make(chan error, 1)
	go func() {
		waitCh <- cmd.Wait()
	}()
	t.Cleanup(func() {
		running, err := processRunning(cmd.Process.Pid)
		if err == nil && !running {
			return
		}
		stopProcess(cmd.Process)
		select {
		case <-waitCh:
		case <-time.After(5 * time.Second):
			require.Fail(t, "timed out waiting for test command cleanup")
		}
	})
	return cmd, waitCh
}

func waitForCommandExit(t *testing.T, cmd *exec.Cmd, waitCh <-chan error) {
	t.Helper()
	select {
	case err := <-waitCh:
		require.NoError(t, commandWaitError("test command", err))
	case <-time.After(5 * time.Second):
		stopProcess(cmd.Process)
		require.Fail(t, "timed out waiting for command to exit")
	}
}

func waitForStatusFile(t *testing.T, path string) ephemeralStatus {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if status, ok := tryReadStatusFile(t, path); ok {
			return status
		}
		time.Sleep(25 * time.Millisecond)
	}
	require.Failf(t, "timed out waiting for status file", "path: %s", path)
	return ephemeralStatus{}
}

func readStatusFile(t *testing.T, path string) ephemeralStatus {
	t.Helper()
	status, ok := tryReadStatusFile(t, path)
	require.True(t, ok, "status file should be readable: %s", path)
	return status
}

func tryReadStatusFile(t *testing.T, path string) (ephemeralStatus, bool) {
	t.Helper()
	content, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return ephemeralStatus{}, false
	}
	require.NoError(t, err)
	var status ephemeralStatus
	if err := json.Unmarshal(content, &status); err != nil {
		return ephemeralStatus{}, false
	}
	return status, true
}

func writeSQLiteMarker(t *testing.T, path, value string) {
	t.Helper()
	require := require.New(t)
	db, err := sql.Open("sqlite", path)
	require.NoError(err)
	defer db.Close()
	_, err = db.Exec("CREATE TABLE marker (value TEXT NOT NULL)")
	require.NoError(err)
	_, err = db.Exec("INSERT INTO marker (value) VALUES (?)", value)
	require.NoError(err)
}

func readSQLiteMarker(t *testing.T, path string) string {
	t.Helper()
	require := require.New(t)
	db, err := sql.Open("sqlite", path)
	require.NoError(err)
	defer db.Close()
	var value string
	require.NoError(db.QueryRow("SELECT value FROM marker").Scan(&value))
	return value
}
