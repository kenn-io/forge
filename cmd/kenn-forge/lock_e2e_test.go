package main

import (
	"bytes"
	"context"
	"encoding/json"
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

	"go.kenn.io/forge/internal/config"
	"go.kenn.io/forge/internal/daemonruntime"
	"go.kenn.io/forge/internal/procutil"
	"go.kenn.io/forge/internal/runtimelock"
	"go.kenn.io/forge/internal/testutil/dbtest"
)

// buildForge compiles the kenn-forge binary into a per-test temp dir
// and returns the absolute path. The build runs once per test via
// t.TempDir.
func buildForge(t *testing.T) string {
	return buildForgeWithLDFlags(t, "")
}

func buildForgeVersion(t *testing.T, runtimeVersion string) string {
	ldflags := ""
	if runtimeVersion != "" {
		ldflags = "-X main.version=" + runtimeVersion
	}
	return buildForgeWithLDFlags(t, ldflags)
}

func buildForgeWithLDFlags(t *testing.T, ldflags string) string {
	t.Helper()
	binDir := t.TempDir()
	binPath := filepath.Join(binDir, "kenn-forge")
	args := []string{"build", "-o", binPath}
	if ldflags != "" {
		args = append(args, "-ldflags="+ldflags)
	}
	args = append(args, ".")
	cmd := procutil.Command("go", args...)
	cmd.Stdout = os.Stderr
	cmd.Stderr = os.Stderr
	require.NoError(t, cmd.Run(), "go build ./cmd/kenn-forge")
	return binPath
}

func runDaemonLifecycle(bin string, args ...string) (string, string, error) {
	cmd := procutil.Command(bin, append([]string{"daemon"}, args...)...)
	cmd.Env = append(os.Environ(), "KENN_FORGE_LOG_LEVEL=warn")
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	return stdout.String(), stderr.String(), err
}

func cleanupDaemonRecords(t *testing.T, store daemon.RuntimeStore) {
	t.Helper()
	t.Cleanup(func() { killDaemonRecords(store) })
}

func killDaemonRecords(store daemon.RuntimeStore) {
	records, _ := store.List()
	for _, record := range records {
		if record.PID == os.Getpid() {
			continue
		}
		if process, err := os.FindProcess(record.PID); err == nil {
			_ = process.Kill()
		}
	}
}

type daemonLifecycleFixture struct {
	t          *testing.T
	bin        string
	root       string
	configPath string
	store      daemon.RuntimeStore
}

func newDaemonLifecycleFixture(t *testing.T, bin string) *daemonLifecycleFixture {
	t.Helper()
	root := t.TempDir()
	runtimeDir := filepath.Join(root, "runtime")
	require.NoError(t, os.MkdirAll(runtimeDir, 0o700))
	t.Setenv("KENN_FORGE_HOME", runtimeDir)
	fixture := &daemonLifecycleFixture{
		t:          t,
		bin:        bin,
		root:       root,
		configPath: filepath.Join(runtimeDir, "config.toml"),
		store:      daemon.RuntimeStore{Dir: runtimeDir},
	}
	cleanupDaemonRecords(t, fixture.store)
	return fixture
}

func (f *daemonLifecycleFixture) dataDir(name string) string {
	f.t.Helper()
	dataDir := filepath.Join(f.root, name)
	require.NoError(f.t, os.MkdirAll(dataDir, 0o700))
	canonicalDataDir, err := filepath.EvalSymlinks(dataDir)
	require.NoError(f.t, err)
	return canonicalDataDir
}

func (f *daemonLifecycleFixture) writeConfig(dataDir string) {
	f.t.Helper()
	writeMinimalConfig(f.t, f.configPath, dataDir, reserveFreePort(f.t))
}

func (f *daemonLifecycleFixture) run(args ...string) (string, string, error) {
	f.t.Helper()
	return runDaemonLifecycle(
		f.bin, append(args, "--config", f.configPath)...,
	)
}

func (f *daemonLifecycleFixture) verifiedRuntime(dataDir string) daemon.RuntimeRecord {
	f.t.Helper()
	record, _, found, err := daemonruntime.FindVerified(
		f.t.Context(), f.store, dataDir,
	)
	require.NoError(f.t, err)
	require.True(f.t, found)
	return record
}

func makeRuntimeLegacy(
	t *testing.T,
	store daemon.RuntimeStore,
	dataDir string,
) daemon.RuntimeRecord {
	t.Helper()
	record, _, found, err := daemonruntime.FindVerified(t.Context(), store, dataDir)
	require.NoError(t, err)
	require.True(t, found)
	delete(record.Metadata, "config_path")
	_, err = store.Write(record)
	require.NoError(t, err)
	status, err := runtimelock.Read(dataDir)
	require.NoError(t, err)
	require.True(t, status.Running)
	require.NotNil(t, status.Metadata)
	status.Metadata.ConfigPath = ""
	metadata, err := json.MarshalIndent(status.Metadata, "", "  ")
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(
		runtimelock.MetadataPath(dataDir), metadata, 0o600,
	))
	return record
}

func (f *daemonLifecycleFixture) writeUnauthenticatedRuntime(
	dataDir string,
) daemon.RuntimeRecord {
	f.t.Helper()
	canonicalConfigPath, err := daemonruntime.CanonicalConfigPath(f.configPath)
	require.NoError(f.t, err)
	record := daemon.NewRuntimeRecord(
		daemonruntime.Service, "dev",
		daemon.Endpoint{Network: daemon.NetworkTCP, Address: "127.0.0.1:1"},
	)
	record.Metadata = map[string]string{
		"config_path": canonicalConfigPath,
		"data_dir":    dataDir,
		"base_path":   "/",
	}
	_, err = f.store.Write(record)
	require.NoError(f.t, err)
	return record
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
		cmd := procutil.Command(bin, "daemon", "start", "--config", configPath)
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

func TestDaemonStartSerializesConfigMoveBeforeRuntimePublicationE2E(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	root := t.TempDir()
	gatePath := filepath.Join(root, "runtime-publish-gate")
	require.NoError(os.WriteFile(gatePath, []byte("hold\n"), 0o600))
	bin := buildForgeWithLDFlags(
		t, "-X main.runtimePublishGatePath="+gatePath,
	)

	firstDataDir := filepath.Join(root, "first-data")
	secondDataDir := filepath.Join(root, "second-data")
	runtimeDir := filepath.Join(root, "runtime")
	require.NoError(os.MkdirAll(firstDataDir, 0o700))
	require.NoError(os.MkdirAll(secondDataDir, 0o700))
	require.NoError(os.MkdirAll(runtimeDir, 0o700))
	t.Setenv("KENN_FORGE_HOME", runtimeDir)
	configPath := filepath.Join(runtimeDir, "config.toml")
	writeMinimalConfig(t, configPath, firstDataDir, reserveFreePort(t))
	store := daemon.RuntimeStore{Dir: runtimeDir}
	t.Cleanup(func() {
		_ = os.Remove(gatePath)
		killDaemonRecords(store)
	})

	type commandResult struct {
		stderr string
		err    error
	}
	runStart := func() commandResult {
		_, stderr, err := runDaemonLifecycle(bin, "start", "--config", configPath)
		return commandResult{stderr: stderr, err: err}
	}
	firstDone := make(chan commandResult, 1)
	go func() { firstDone <- runStart() }()
	waitForFile(t, gatePath+".ready", 10*time.Second)

	writeMinimalConfig(t, configPath, secondDataDir, reserveFreePort(t))
	secondDone := make(chan commandResult, 1)
	go func() { secondDone <- runStart() }()
	select {
	case result := <-secondDone:
		require.FailNow("second start bypassed config lifecycle lock", result.stderr)
	case <-time.After(250 * time.Millisecond):
	}
	require.NoError(os.Remove(gatePath))

	first := <-firstDone
	require.NoError(first.err, first.stderr)
	second := <-secondDone
	require.Error(second.err)
	assert.Contains(second.stderr, "daemon restart")
	records, err := store.List()
	require.NoError(err)
	require.Len(records, 1)
	canonicalFirstDataDir, err := filepath.EvalSymlinks(firstDataDir)
	require.NoError(err)
	assert.Equal(canonicalFirstDataDir, records[0].Metadata["data_dir"])

	_, stderr, err := runDaemonLifecycle(bin, "stop", "--config", configPath)
	require.NoError(err, stderr)
}

func TestDaemonStartPreservesUnauthenticatedLiveRuntimeE2E(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	gatePath := filepath.Join(t.TempDir(), "runtime-serve-gate")
	require.NoError(os.WriteFile(gatePath, []byte("hold\n"), 0o600))
	bin := buildForgeWithLDFlags(t, "-X main.runtimeServeGatePath="+gatePath)
	fixture := newDaemonLifecycleFixture(t, bin)
	dataDir := fixture.dataDir("data")
	fixture.writeConfig(dataDir)

	foreground := procutil.Command(bin, "serve", "--config", fixture.configPath)
	var foregroundStderr bytes.Buffer
	foreground.Stderr = &foregroundStderr
	foreground.Env = append(os.Environ(), "KENN_FORGE_LOG_LEVEL=warn")
	require.NoError(foreground.Start())
	foregroundDone := make(chan struct{})
	var foregroundErr error
	go func() {
		foregroundErr = foreground.Wait()
		close(foregroundDone)
	}()
	t.Cleanup(func() {
		_ = os.Remove(gatePath)
		if foreground.Process != nil {
			_ = foreground.Process.Kill()
		}
		select {
		case <-foregroundDone:
		case <-time.After(5 * time.Second):
		}
	})

	waitForFile(t, gatePath+".ready", 10*time.Second)
	records, err := fixture.store.List()
	require.NoError(err)
	require.Len(records, 1)
	liveRecord := records[0]
	assert.True(daemon.ProcessAlive(liveRecord.PID))

	_, stderr, err := fixture.run("start")
	require.Error(err)
	assert.Contains(stderr, "no authoritative match")
	records, err = fixture.store.List()
	require.NoError(err)
	require.Len(records, 1)
	assert.Equal(liveRecord.PID, records[0].PID)

	require.NoError(os.Remove(gatePath))
	require.Eventually(func() bool {
		_, _, found, findErr := daemonruntime.FindVerified(
			t.Context(), fixture.store, dataDir,
		)
		return findErr == nil && found
	}, 10*time.Second, 20*time.Millisecond)
	_, stderr, err = fixture.run("stop")
	require.NoError(err, stderr)
	select {
	case <-foregroundDone:
		require.NoError(foregroundErr, foregroundStderr.String())
	case <-time.After(5 * time.Second):
		require.Fail("foreground daemon did not stop")
	}
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
		bin, "daemon", "start", "--config", configPath,
	)
	var backgroundStderr bytes.Buffer
	background.Stderr = &backgroundStderr
	background.Env = append(os.Environ(), "KENN_FORGE_LOG_LEVEL=warn")
	t.Cleanup(func() {
		killDaemonRecords(store)
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
	cleanupDaemonRecords(t, store)

	cmd := procutil.Command(
		bin, "daemon", "start", "--config", configPath,
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

func TestDaemonLifecycleStartStatusRestartStopE2E(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	oldBin := buildForgeVersion(t, "v-old")
	newBin := buildForgeVersion(t, "v-new")
	fixture := newDaemonLifecycleFixture(t, oldBin)
	dataDir := fixture.dataDir("data")
	fixture.writeConfig(dataDir)

	stopped := false
	t.Cleanup(func() {
		if !stopped {
			_, _, _ = runDaemonLifecycle(
				newBin, "stop", "--config", fixture.configPath,
			)
		}
	})

	stdout, stderr, err := fixture.run("start")
	require.NoError(err, stderr)
	assert.Contains(stdout, "kenn-forge running at")
	first := makeRuntimeLegacy(t, fixture.store, dataDir)
	assert.Equal("v-old", first.Version)

	stdout, stderr, err = runDaemonLifecycle(
		newBin, "status", "--json", "--config", fixture.configPath,
	)
	require.NoError(err, stderr)
	assert.Contains(stdout, `"running": true`)
	assert.Contains(stdout, fmt.Sprintf(`"pid": %d`, first.PID))

	stdout, stderr, err = runDaemonLifecycle(
		newBin, "stop", "--config", fixture.configPath,
	)
	require.NoError(err, stderr)
	assert.Contains(stdout, fmt.Sprintf("pid %d", first.PID))
	assert.False(daemon.ProcessAlive(first.PID))

	_, stderr, err = fixture.run("start")
	require.NoError(err, stderr)
	first = makeRuntimeLegacy(t, fixture.store, dataDir)
	stdout, stderr, err = runDaemonLifecycle(
		newBin, "restart", "--config", fixture.configPath,
	)
	require.NoError(err, stderr)
	assert.Contains(stdout, "kenn-forge running at")
	second := fixture.verifiedRuntime(dataDir)
	assert.Equal("v-new", second.Version)
	assert.NotEqual(first.PID, second.PID)
	assert.False(daemon.ProcessAlive(first.PID))

	stdout, stderr, err = runDaemonLifecycle(
		newBin, "stop", "--config", fixture.configPath,
	)
	require.NoError(err, stderr)
	assert.Contains(stdout, fmt.Sprintf("pid %d", second.PID))
	stopped = true
	status, err := runtimelock.Read(dataDir)
	require.NoError(err)
	assert.False(status.Running)
	_, err = os.Stat(runtimelock.LockPath(dataDir))
	require.NoError(err)

	stdout, stderr, err = runDaemonLifecycle(
		newBin, "status", "--json", "--config", fixture.configPath,
	)
	require.NoError(err, stderr)
	assert.Contains(stdout, `"running": false`)
}

func TestDaemonStatusAndStopSurviveInvalidConfigE2E(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	fixture := newDaemonLifecycleFixture(t, buildForge(t))
	dataDir := fixture.dataDir("data")
	fixture.writeConfig(dataDir)

	_, stderr, err := fixture.run("start")
	require.NoError(err, stderr)
	record := fixture.verifiedRuntime(dataDir)
	require.NoError(os.WriteFile(
		fixture.configPath, []byte("[invalid\n"), 0o600,
	))

	stdout, stderr, err := fixture.run("status", "--json")
	require.NoError(err, stderr)
	assert.Contains(stdout, `"running": true`)
	assert.Contains(stdout, fmt.Sprintf(`"pid": %d`, record.PID))

	stdout, stderr, err = fixture.run("stop")
	require.NoError(err, stderr)
	assert.Contains(stdout, fmt.Sprintf("pid %d", record.PID))
	assert.False(daemon.ProcessAlive(record.PID))
}

func TestDaemonStopWaitsForLongGracefulShutdownE2E(t *testing.T) {
	bin := buildForgeWithLDFlags(t, "-X main.runtimeShutdownDelay=16s")
	fixture := newDaemonLifecycleFixture(t, bin)
	dataDir := fixture.dataDir("data")
	fixture.writeConfig(dataDir)

	_, stderr, err := fixture.run("start")
	require.NoError(t, err, stderr)
	record := fixture.verifiedRuntime(dataDir)

	_, stderr, err = fixture.run("stop")
	require.NoError(t, err, stderr)
	assert.False(t, daemon.ProcessAlive(record.PID))
}

func TestDaemonRestartReplacesRuntimeAfterConfiguredDataDirectoryChangesE2E(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	fixture := newDaemonLifecycleFixture(t, buildForge(t))
	firstDataDir := fixture.dataDir("first-data")
	secondDataDir := fixture.dataDir("second-data")
	fixture.writeConfig(firstDataDir)

	_, stderr, err := fixture.run("start")
	require.NoError(err, stderr)
	first := fixture.verifiedRuntime(firstDataDir)

	fixture.writeConfig(secondDataDir)
	stdout, stderr, err := fixture.run("restart")
	require.NoError(err, stderr)
	assert.Contains(stdout, "kenn-forge running at")
	second := fixture.verifiedRuntime(secondDataDir)
	assert.NotEqual(first.PID, second.PID)
	assert.False(daemon.ProcessAlive(first.PID))
	records, err := fixture.store.List()
	require.NoError(err)
	assert.Len(records, 1)
}

func TestDaemonRestartTreatsConfigAliasesAsSameIdentityE2E(t *testing.T) {
	bin := buildForge(t)
	aliases := []struct {
		name  string
		build func(*testing.T, *daemonLifecycleFixture) string
	}{
		{name: "symlink", build: func(t *testing.T, fixture *daemonLifecycleFixture) string {
			configPath := fixture.configPath
			alias := filepath.Join(filepath.Dir(configPath), "config-alias.toml")
			if err := os.Symlink(configPath, alias); err != nil {
				t.Skipf("config symlink unavailable: %v", err)
			}
			return alias
		}},
		{name: "case", build: func(t *testing.T, fixture *daemonLifecycleFixture) string {
			configPath := fixture.configPath
			alias := filepath.Join(filepath.Dir(configPath), "CONFIG.TOML")
			if _, err := os.Stat(alias); os.IsNotExist(err) {
				t.Skip("filesystem is case-sensitive")
			} else {
				require.NoError(t, err)
			}
			return alias
		}},
		{name: "unicode-normalization", build: func(
			t *testing.T, fixture *daemonLifecycleFixture,
		) string {
			decomposed := filepath.Join(filepath.Dir(fixture.configPath), "Cafe\u0301.toml")
			require.NoError(t, os.Rename(fixture.configPath, decomposed))
			fixture.configPath = decomposed
			alias := filepath.Join(filepath.Dir(decomposed), "Caf\u00e9.toml")
			if _, err := os.Stat(alias); os.IsNotExist(err) {
				t.Skip("filesystem distinguishes Unicode normalization forms")
			} else {
				require.NoError(t, err)
			}
			return alias
		}},
	}
	for _, alias := range aliases {
		t.Run(alias.name, func(t *testing.T) {
			assert := assert.New(t)
			require := require.New(t)
			fixture := newDaemonLifecycleFixture(t, bin)
			firstDataDir := fixture.dataDir("first-data")
			secondDataDir := fixture.dataDir("second-data")
			fixture.writeConfig(firstDataDir)
			configAlias := alias.build(t, fixture)

			_, stderr, err := runDaemonLifecycle(
				bin, "start", "--config", configAlias,
			)
			require.NoError(err, stderr)
			first := fixture.verifiedRuntime(firstDataDir)

			fixture.writeConfig(secondDataDir)
			_, stderr, err = fixture.run("restart")
			require.NoError(err, stderr)
			second := fixture.verifiedRuntime(secondDataDir)
			assert.NotEqual(first.PID, second.PID)
			assert.False(daemon.ProcessAlive(first.PID))
			records, err := fixture.store.List()
			require.NoError(err)
			assert.Len(records, 1)
		})
	}
}

func TestDaemonLifecycleTreatsDataDirectoryCaseAliasesAsSameIdentityE2E(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	fixture := newDaemonLifecycleFixture(t, buildForge(t))
	dataDir := fixture.dataDir("DataDir")
	caseAlias := filepath.Join(fixture.root, "datadir")
	if _, err := os.Stat(caseAlias); os.IsNotExist(err) {
		t.Skip("filesystem is case-sensitive")
	} else {
		require.NoError(err)
	}
	fixture.writeConfig(dataDir)

	_, stderr, err := fixture.run("start")
	require.NoError(err, stderr)
	first := fixture.verifiedRuntime(dataDir)
	writeMinimalConfig(
		t, fixture.configPath, caseAlias, reserveFreePort(t),
	)

	_, stderr, err = fixture.run("start")
	require.NoError(err, stderr)
	reused := fixture.verifiedRuntime(dataDir)
	assert.Equal(first.PID, reused.PID)

	_, stderr, err = fixture.run("restart")
	require.NoError(err, stderr)
	replacement := fixture.verifiedRuntime(dataDir)
	assert.NotEqual(first.PID, replacement.PID)
	assert.False(daemon.ProcessAlive(first.PID))

	_, stderr, err = fixture.run("stop")
	require.NoError(err, stderr)
}

func TestDaemonStartRejectsRuntimeAfterConfiguredDataDirectoryChangesE2E(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	fixture := newDaemonLifecycleFixture(t, buildForge(t))
	firstDataDir := fixture.dataDir("first-data")
	secondDataDir := fixture.dataDir("second-data")
	fixture.writeConfig(firstDataDir)

	_, stderr, err := fixture.run("start")
	require.NoError(err, stderr)
	first := fixture.verifiedRuntime(firstDataDir)

	fixture.writeConfig(secondDataDir)
	_, stderr, err = fixture.run("start")
	require.Error(err)
	assert.Contains(stderr, "daemon restart")
	assert.True(daemon.ProcessAlive(first.PID))
	records, err := fixture.store.List()
	require.NoError(err)
	require.Len(records, 1)
	assert.Equal(first.PID, records[0].PID)
}

func TestDaemonStartDiscardsUnauthenticatedConfigRuntimeE2E(t *testing.T) {
	bin := buildForge(t)
	for _, sameDataDir := range []bool{true, false} {
		name := "moved-data-dir"
		if sameDataDir {
			name = "same-data-dir"
		}
		t.Run(name, func(t *testing.T) {
			assert := assert.New(t)
			require := require.New(t)
			fixture := newDaemonLifecycleFixture(t, bin)
			dataDir := fixture.dataDir("configured-data")
			staleDataDir := fixture.dataDir("stale-data")
			if sameDataDir {
				staleDataDir = dataDir
			}
			fixture.writeConfig(dataDir)
			fixture.writeUnauthenticatedRuntime(staleDataDir)

			_, stderr, err := fixture.run("start")
			require.NoError(err, stderr)
			records, err := fixture.store.List()
			require.NoError(err)
			require.Len(records, 1)
			assert.NotEqual(os.Getpid(), records[0].PID)

			_, stderr, err = fixture.run("stop")
			require.NoError(err, stderr)
		})
	}
}

func TestDaemonStatusDiscardsUnauthenticatedConfigRuntimeE2E(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	fixture := newDaemonLifecycleFixture(t, buildForge(t))
	configuredDataDir := fixture.dataDir("configured-data")
	staleDataDir := fixture.dataDir("stale-data")
	fixture.writeConfig(configuredDataDir)
	fixture.writeUnauthenticatedRuntime(staleDataDir)

	stdout, stderr, err := fixture.run("status", "--json")
	require.NoError(err, stderr)
	assert.Contains(stdout, `"running": false`)
	records, err := fixture.store.List()
	require.NoError(err)
	assert.Empty(records)
}

func TestDaemonRestartDiscardsShadowedUnauthenticatedRuntimeE2E(t *testing.T) {
	bin := buildForge(t)
	for _, deletedDataDir := range []bool{false, true} {
		name := "same-data-dir"
		if deletedDataDir {
			name = "deleted-data-dir"
		}
		t.Run(name, func(t *testing.T) {
			assert := assert.New(t)
			require := require.New(t)
			fixture := newDaemonLifecycleFixture(t, bin)
			dataDir := fixture.dataDir("data")
			fixture.writeConfig(dataDir)

			_, stderr, err := fixture.run("start")
			require.NoError(err, stderr)
			first := fixture.verifiedRuntime(dataDir)
			staleDataDir := dataDir
			if deletedDataDir {
				staleDataDir = fixture.dataDir("deleted-data")
			}
			stale := fixture.writeUnauthenticatedRuntime(staleDataDir)
			if deletedDataDir {
				require.NoError(os.RemoveAll(staleDataDir))
			}
			records, err := fixture.store.List()
			require.NoError(err)
			require.Len(records, 2)

			_, stderr, err = fixture.run("restart")
			require.NoError(err, stderr)
			running := fixture.verifiedRuntime(dataDir)
			assert.NotEqual(first.PID, running.PID)
			assert.NotEqual(stale.PID, running.PID)

			stdout, stderr, err := fixture.run("status", "--json")
			require.NoError(err, stderr)
			assert.Contains(stdout, `"running": true`)
			assert.Contains(stdout, fmt.Sprintf(`"pid": %d`, running.PID))

			_, stderr, err = fixture.run("stop")
			require.NoError(err, stderr)
			records, err = fixture.store.List()
			require.NoError(err)
			assert.Empty(records)
		})
	}
}

func TestDaemonLifecycleCreatesRuntimeStoreForExplicitConfigE2E(t *testing.T) {
	require := require.New(t)
	bin := buildForge(t)
	root := t.TempDir()
	homeDir := filepath.Join(root, "home")
	runtimeDir := filepath.Join(homeDir, ".kenn", "forge")
	configDir := filepath.Join(root, "explicit-config")
	dataDir := filepath.Join(root, "custom-data")
	require.NoError(os.MkdirAll(configDir, 0o700))
	t.Setenv("HOME", homeDir)
	t.Setenv("KENN_FORGE_HOME", "")
	configPath := filepath.Join(configDir, "config.toml")
	writeMinimalConfig(t, configPath, dataDir, reserveFreePort(t))
	store := daemon.RuntimeStore{Dir: runtimeDir}

	cleanupDaemonRecords(t, store)

	_, stderr, err := runDaemonLifecycle(bin, "start", "--config", configPath)
	require.NoError(err, stderr)
	require.DirExists(runtimeDir)
	runtimeInfo, err := os.Stat(runtimeDir)
	require.NoError(err)
	if runtime.GOOS != "windows" {
		require.Equal(os.FileMode(0o700), runtimeInfo.Mode().Perm())
	}
	require.DirExists(dataDir)
	records, err := store.List()
	require.NoError(err)
	require.Len(records, 1)

	_, stderr, err = runDaemonLifecycle(bin, "stop", "--config", configPath)
	require.NoError(err, stderr)
	require.Eventually(func() bool {
		return !daemon.ProcessAlive(records[0].PID)
	}, 5*time.Second, 20*time.Millisecond)
}

func TestDaemonStartMigratesLegacyDefaultThroughSymlinkedHomeE2E(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	bin := buildForge(t)
	root := t.TempDir()
	realHome := filepath.Join(root, "real-home")
	homeAlias := filepath.Join(root, "home")
	require.NoError(os.MkdirAll(realHome, 0o700))
	if err := os.Symlink(realHome, homeAlias); err != nil {
		t.Skipf("home symlink unavailable: %v", err)
	}
	t.Setenv("HOME", homeAlias)
	t.Setenv("KENN_FORGE_HOME", "")
	installFailingGitHubCLI(t)

	legacyHome := filepath.Join(homeAlias, ".config", "middleman")
	forgeHome := filepath.Join(homeAlias, ".kenn", "forge")
	require.NoError(os.MkdirAll(legacyHome, 0o700))
	legacyPort := reserveFreePort(t)
	legacyConfig := fmt.Sprintf(`host = "127.0.0.1"
port = %d
data_dir = %q
base_path = "/legacy/"
`, legacyPort, legacyHome)
	require.NoError(os.WriteFile(
		filepath.Join(legacyHome, "config.toml"), []byte(legacyConfig), 0o600,
	))
	legacyDatabasePath := filepath.Join(legacyHome, "middleman.db")
	require.NoError(dbtest.OpenAt(t, legacyDatabasePath).Close())
	store := daemon.RuntimeStore{Dir: forgeHome}
	cleanupDaemonRecords(t, store)

	_, stderr, err := runDaemonLifecycle(bin, "start")
	if err != nil {
		backgroundLog, _ := os.ReadFile(filepath.Join(forgeHome, "forge.background.log"))
		require.NoError(err, stderr+"\n"+string(backgroundLog))
	}
	migrated, err := config.Load(config.DefaultConfigPath())
	require.NoError(err)
	assert.Equal(legacyPort, migrated.Port)
	assert.Equal("/legacy/", migrated.BasePath)
	assert.FileExists(filepath.Join(forgeHome, ".legacy-config-migrated"))
	assert.FileExists(filepath.Join(migrated.DataDir, "forge.db"))
	assert.NoFileExists(legacyDatabasePath)
}

func TestDaemonStatusAndStopFindRuntimeAfterConfiguredDataDirectoryChangesE2E(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	fixture := newDaemonLifecycleFixture(t, buildForge(t))
	firstDataDir := fixture.dataDir("first-data")
	secondDataDir := fixture.dataDir("second-data")
	fixture.writeConfig(firstDataDir)

	_, stderr, err := fixture.run("start")
	require.NoError(err, stderr)
	first := fixture.verifiedRuntime(firstDataDir)

	fixture.writeConfig(secondDataDir)
	stdout, stderr, err := fixture.run("status", "--json")
	require.NoError(err, stderr)
	assert.Contains(stdout, `"running": true`)
	assert.Contains(stdout, fmt.Sprintf(`"pid": %d`, first.PID))
	assert.Contains(stdout, `"data_dir": "`+firstDataDir+`"`)

	stdout, stderr, err = fixture.run("stop")
	require.NoError(err, stderr)
	assert.Contains(stdout, fmt.Sprintf("pid %d", first.PID))
	assert.False(daemon.ProcessAlive(first.PID))
	records, err := fixture.store.List()
	require.NoError(err)
	assert.Empty(records)
}

func TestDaemonRestartRejectsForgedConfigRedirectToAnotherRuntimeE2E(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	bin := buildForge(t)
	root := t.TempDir()
	firstDataDir := filepath.Join(root, "first-data")
	secondDataDir := filepath.Join(root, "second-data")
	runtimeDir := filepath.Join(root, "config-home")
	require.NoError(os.MkdirAll(firstDataDir, 0o700))
	require.NoError(os.MkdirAll(secondDataDir, 0o700))
	require.NoError(os.MkdirAll(runtimeDir, 0o700))
	var err error
	firstDataDir, err = filepath.EvalSymlinks(firstDataDir)
	require.NoError(err)
	secondDataDir, err = filepath.EvalSymlinks(secondDataDir)
	require.NoError(err)
	t.Setenv("KENN_FORGE_HOME", runtimeDir)
	firstConfigPath := filepath.Join(root, "first.toml")
	secondConfigPath := filepath.Join(root, "second.toml")
	writeMinimalConfig(t, firstConfigPath, firstDataDir, reserveFreePort(t))
	writeMinimalConfig(t, secondConfigPath, secondDataDir, reserveFreePort(t))
	store := daemon.RuntimeStore{Dir: runtimeDir}

	cleanupDaemonRecords(t, store)

	_, stderr, err := runDaemonLifecycle(bin, "start", "--config", secondConfigPath)
	require.NoError(err, stderr)
	second, _, found, err := daemonruntime.FindVerified(t.Context(), store, secondDataDir)
	require.NoError(err)
	require.True(found)
	second.Metadata["config_path"] = firstConfigPath
	_, err = store.Write(second)
	require.NoError(err)

	_, stderr, err = runDaemonLifecycle(bin, "restart", "--config", firstConfigPath)
	require.Error(err)
	assert.Contains(stderr, "no authoritative match")
	assert.True(daemon.ProcessAlive(second.PID))
	records, err := store.List()
	require.NoError(err)
	require.Len(records, 1)
	assert.Equal(second.PID, records[0].PID)
}

func TestDaemonRestartIgnoresUnrelatedLegacyRuntimeE2E(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	bin := buildForge(t)
	root := t.TempDir()
	firstDataDir := filepath.Join(root, "first-data")
	secondDataDir := filepath.Join(root, "second-data")
	runtimeDir := filepath.Join(root, "config-home")
	require.NoError(os.MkdirAll(firstDataDir, 0o700))
	require.NoError(os.MkdirAll(secondDataDir, 0o700))
	require.NoError(os.MkdirAll(runtimeDir, 0o700))
	var err error
	firstDataDir, err = filepath.EvalSymlinks(firstDataDir)
	require.NoError(err)
	secondDataDir, err = filepath.EvalSymlinks(secondDataDir)
	require.NoError(err)
	t.Setenv("KENN_FORGE_HOME", runtimeDir)
	firstConfigPath := filepath.Join(root, "first.toml")
	secondConfigPath := filepath.Join(root, "second.toml")
	writeMinimalConfig(t, firstConfigPath, firstDataDir, reserveFreePort(t))
	writeMinimalConfig(t, secondConfigPath, secondDataDir, reserveFreePort(t))
	store := daemon.RuntimeStore{Dir: runtimeDir}

	cleanupDaemonRecords(t, store)

	_, stderr, err := runDaemonLifecycle(bin, "start", "--config", firstConfigPath)
	require.NoError(err, stderr)
	_, stderr, err = runDaemonLifecycle(bin, "start", "--config", secondConfigPath)
	require.NoError(err, stderr)
	first := makeRuntimeLegacy(t, store, firstDataDir)
	second := makeRuntimeLegacy(t, store, secondDataDir)

	_, stderr, err = runDaemonLifecycle(bin, "restart", "--config", firstConfigPath)
	require.NoError(err, stderr)
	replacement, _, found, err := daemonruntime.FindVerified(
		t.Context(), store, firstDataDir,
	)
	require.NoError(err)
	require.True(found)
	assert.NotEqual(first.PID, replacement.PID)
	assert.False(daemon.ProcessAlive(first.PID))
	assert.True(daemon.ProcessAlive(second.PID))
}

func TestDaemonRestartRejectsMovedLegacyRuntimeE2E(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	bin := buildForge(t)
	root := t.TempDir()
	firstDataDir := filepath.Join(root, "first-data")
	secondDataDir := filepath.Join(root, "second-data")
	runtimeDir := filepath.Join(root, "config-home")
	require.NoError(os.MkdirAll(firstDataDir, 0o700))
	require.NoError(os.MkdirAll(secondDataDir, 0o700))
	require.NoError(os.MkdirAll(runtimeDir, 0o700))
	var err error
	firstDataDir, err = filepath.EvalSymlinks(firstDataDir)
	require.NoError(err)
	secondDataDir, err = filepath.EvalSymlinks(secondDataDir)
	require.NoError(err)
	t.Setenv("KENN_FORGE_HOME", runtimeDir)
	configPath := filepath.Join(runtimeDir, "config.toml")
	writeMinimalConfig(t, configPath, firstDataDir, reserveFreePort(t))
	store := daemon.RuntimeStore{Dir: runtimeDir}

	cleanupDaemonRecords(t, store)

	_, stderr, err := runDaemonLifecycle(bin, "start", "--config", configPath)
	require.NoError(err, stderr)
	first := makeRuntimeLegacy(t, store, firstDataDir)

	writeMinimalConfig(t, configPath, secondDataDir, reserveFreePort(t))
	_, stderr, err = runDaemonLifecycle(bin, "restart", "--config", configPath)
	require.Error(err)
	assert.Contains(stderr, "stop it before changing data_dir")
	assert.True(daemon.ProcessAlive(first.PID))
	records, err := store.List()
	require.NoError(err)
	require.Len(records, 1)
	assert.Equal(first.PID, records[0].PID)
}

func TestDetachedServeRejectsChangedDataDirectoryE2E(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	bin := buildForge(t)
	root := t.TempDir()
	dataDir := filepath.Join(root, "configured-data")
	expectedDataDir := filepath.Join(root, "locked-data")
	require.NoError(os.MkdirAll(dataDir, 0o700))
	require.NoError(os.MkdirAll(expectedDataDir, 0o700))
	cfgPath := filepath.Join(root, "config.toml")
	writeMinimalConfig(t, cfgPath, dataDir, reserveFreePort(t))
	expectedDataDir, err := filepath.EvalSymlinks(expectedDataDir)
	require.NoError(err)

	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	cmd := procutil.CommandContext(ctx, bin, "serve", "--config", cfgPath)
	cmd.Env = append(
		os.Environ(),
		"KENN_FORGE_EXPECTED_DATA_DIR="+expectedDataDir,
	)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	err = cmd.Run()

	require.Error(err)
	require.NoError(ctx.Err(), "serve must reject the mismatch without waiting for cancellation")
	assert.Contains(stderr.String(), "background launch expected data_dir")
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
	installFailingGitHubCLI(t)
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

func installFailingGitHubCLI(t *testing.T) {
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

	// `kenn-forge daemon status` while the lock is held before WriteMetadata:
	// reports running, but metadata is unavailable.
	startupStatusCmd := procutil.Command(bin, "daemon", "status", "--config", cfgPath)
	var startupStatusOut bytes.Buffer
	startupStatusCmd.Stdout = &startupStatusOut
	startupStatusCmd.Stderr = os.Stderr
	require.NoError(startupStatusCmd.Run())
	require.Contains(startupStatusOut.String(), "running (metadata unavailable: missing")
	require.Contains(startupStatusOut.String(), dataDir)
	require.Contains(startupStatusOut.String(), runtimelock.LockPath(dataDir))

	// `kenn-forge daemon status --json`: same lock-held/missing-metadata state.
	startupJSONCmd := procutil.Command(bin, "daemon", "status", "--json", "--config", cfgPath)
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
	first := procutil.Command(bin, "serve", "--config", cfgPath)
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
	second := procutil.Command(bin, "serve", "--config", cfgPath)
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

	// `kenn-forge daemon status` against the same config: reports running with
	// metadata.
	statusCmd := procutil.Command(bin, "daemon", "status", "--config", cfgPath)
	var statusOut bytes.Buffer
	statusCmd.Stdout = &statusOut
	statusCmd.Stderr = os.Stderr
	require.NoError(statusCmd.Run())
	require.Contains(statusOut.String(), "running")
	require.Contains(statusOut.String(), dataDir)
	require.Contains(statusOut.String(), "pid:")
	require.Contains(statusOut.String(), "port:         "+strconv.Itoa(port))

	// `kenn-forge daemon status --json`: same data, JSON shape.
	jsonCmd := procutil.Command(bin, "daemon", "status", "--json", "--config", cfgPath)
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

	// `kenn-forge daemon status` now reports not-running.
	statusCmd2 := procutil.Command(bin, "daemon", "status", "--config", cfgPath)
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
