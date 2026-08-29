package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/forge/internal/config"
	"go.kenn.io/forge/internal/daemonruntime"
	"go.kenn.io/forge/internal/runtimelock"
	"go.kenn.io/kit/daemon"
)

type recordingLifecycleLock struct {
	events *[]string
}

func (l recordingLifecycleLock) Release() error {
	*l.events = append(*l.events, "unlock")
	return nil
}

type noopLifecycleLock struct{}

func (noopLifecycleLock) Release() error { return nil }

func TestDaemonStartValidatesLoopbackBeforeAcquiringLock(t *testing.T) {
	deps, events := daemonLifecycleTestDeps(t)
	deps.loadConfig = func(string) (*config.Config, error) {
		return &config.Config{Host: "192.0.2.1", DataDir: t.TempDir()}, nil
	}

	err := newDaemonLifecycle(deps).Start(t.Context(), "/config.toml", io.Discard)

	require.ErrorContains(t, err, "loopback TCP listener")
	assert.Empty(t, *events)
}

func TestDaemonStartDirectsVersionMismatchToRestart(t *testing.T) {
	deps, _ := daemonLifecycleTestDeps(t)
	deps.ensureBackground = func(context.Context, string, *config.Config) (daemon.RuntimeRecord, error) {
		return daemon.RuntimeRecord{}, &daemonruntime.VersionMismatchError{
			Running: "v-old", Expected: "v-new",
		}
	}

	err := newDaemonLifecycle(deps).Start(t.Context(), "/config.toml", io.Discard)

	require.Error(t, err)
	assert.ErrorContains(t, err, "kenn-forge daemon restart")
}

func TestDaemonStartRetriesWhenConfigDataDirectoryChangesUnderLock(t *testing.T) {
	deps, events := daemonLifecycleTestDeps(t)
	firstDataDir := t.TempDir()
	secondDataDir := t.TempDir()
	loads := 0
	deps.loadConfig = func(string) (*config.Config, error) {
		loads++
		dataDir := secondDataDir
		if loads == 1 {
			dataDir = firstDataDir
		}
		return &config.Config{Host: "127.0.0.1", DataDir: dataDir}, nil
	}
	deps.acquireLock = func(
		_ context.Context,
		_ daemon.RuntimeStore,
		dataDir string,
	) (daemonLifecycleLock, error) {
		*events = append(*events, "lock:"+dataDir)
		return recordingLifecycleLock{events: events}, nil
	}
	deps.ensureBackground = func(
		_ context.Context,
		_ string,
		cfg *config.Config,
	) (daemon.RuntimeRecord, error) {
		*events = append(*events, "start:"+cfg.DataDir)
		return daemonTestRuntime(42), nil
	}

	err := newDaemonLifecycle(deps).Start(t.Context(), "/config.toml", io.Discard)

	require.NoError(t, err)
	assert.Equal(t, []string{
		"lock:" + firstDataDir,
		"unlock",
		"lock:" + secondDataDir,
		"start:" + secondDataDir,
		"unlock",
	}, *events)
}

func TestDaemonStartRetriesWhenConfigSymlinkIdentityChanges(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	root := t.TempDir()
	firstConfigDir := filepath.Join(root, "first")
	secondConfigDir := filepath.Join(root, "second")
	configAlias := filepath.Join(root, "config-home")
	require.NoError(os.MkdirAll(firstConfigDir, 0o700))
	require.NoError(os.MkdirAll(secondConfigDir, 0o700))
	if err := os.Symlink(firstConfigDir, configAlias); err != nil {
		t.Skipf("config symlink unavailable: %v", err)
	}
	configPath := filepath.Join(configAlias, "config.toml")
	firstConfigPath := filepath.Join(firstConfigDir, "config.toml")
	secondConfigPath := filepath.Join(secondConfigDir, "config.toml")
	firstConfigPath, err := daemonruntime.CanonicalConfigPath(firstConfigPath)
	require.NoError(err)
	secondConfigPath, err = daemonruntime.CanonicalConfigPath(secondConfigPath)
	require.NoError(err)
	dataDir := t.TempDir()

	deps, _ := daemonLifecycleTestDeps(t)
	var lockedPaths, loadedPaths []string
	deps.acquireConfigLock = func(
		_ context.Context,
		_ daemon.RuntimeStore,
		path string,
	) (daemonLifecycleLock, error) {
		lockedPaths = append(lockedPaths, path)
		return noopLifecycleLock{}, nil
	}
	deps.loadConfig = func(path string) (*config.Config, error) {
		loadedPaths = append(loadedPaths, path)
		if len(loadedPaths) == 1 {
			require.NoError(os.Remove(configAlias))
			require.NoError(os.Symlink(secondConfigDir, configAlias))
		}
		return &config.Config{Host: "127.0.0.1", DataDir: dataDir}, nil
	}
	startedPath := ""
	deps.ensureBackground = func(
		_ context.Context,
		path string,
		_ *config.Config,
	) (daemon.RuntimeRecord, error) {
		startedPath = path
		return daemonTestRuntime(42), nil
	}

	err = newDaemonLifecycle(deps).Start(t.Context(), configPath, io.Discard)

	require.NoError(err)
	assert.Equal([]string{firstConfigPath, secondConfigPath}, lockedPaths)
	assert.Equal([]string{configPath, configPath, secondConfigPath}, loadedPaths)
	assert.Equal(secondConfigPath, startedPath)
}

func TestDaemonStartSerializesConfigAcrossDataDirectoryChanges(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	firstDataDir := t.TempDir()
	secondDataDir := t.TempDir()
	store := daemon.RuntimeStore{Dir: t.TempDir()}
	deps := defaultDaemonLifecycleDeps()
	deps.store = func() (daemon.RuntimeStore, error) { return store, nil }
	var configMu sync.RWMutex
	currentDataDir := firstDataDir
	deps.loadConfig = func(string) (*config.Config, error) {
		configMu.RLock()
		defer configMu.RUnlock()
		return &config.Config{
			Host: "127.0.0.1", DataDir: currentDataDir,
		}, nil
	}
	firstEntered := make(chan struct{})
	secondEntered := make(chan struct{})
	releaseFirst := make(chan struct{})
	deps.ensureBackground = func(
		_ context.Context,
		_ string,
		cfg *config.Config,
	) (daemon.RuntimeRecord, error) {
		if cfg.DataDir == firstDataDir {
			close(firstEntered)
			<-releaseFirst
		} else {
			close(secondEntered)
		}
		return daemon.RuntimeRecord{}, errors.New("test start complete")
	}
	lifecycle := newDaemonLifecycle(deps)
	firstResult := make(chan error, 1)
	go func() {
		firstResult <- lifecycle.Start(t.Context(), "/config.toml", io.Discard)
	}()
	<-firstEntered

	configMu.Lock()
	currentDataDir = secondDataDir
	configMu.Unlock()
	secondResult := make(chan error, 1)
	go func() {
		secondResult <- lifecycle.Start(t.Context(), "/config.toml", io.Discard)
	}()
	enteredConcurrently := false
	select {
	case <-secondEntered:
		enteredConcurrently = true
	case <-time.After(250 * time.Millisecond):
	}
	close(releaseFirst)
	require.Error(<-firstResult)
	require.Error(<-secondResult)
	assert.False(enteredConcurrently, "second start entered before the first published its runtime")
}

func TestDaemonStopReportsNotRunning(t *testing.T) {
	deps, _ := daemonLifecycleTestDeps(t)
	var output bytes.Buffer

	err := newDaemonLifecycle(deps).Stop(t.Context(), "/config.toml", &output)

	require.NoError(t, err)
	assert.Contains(t, output.String(), "not running")
}

func TestDaemonStopRefusesUnauthenticatedRunningProcess(t *testing.T) {
	deps, _ := daemonLifecycleTestDeps(t)
	deps.readStatus = func(string) (runtimelock.Status, error) {
		return runtimelock.Status{Running: true}, nil
	}
	signals := 0
	deps.signal = func(int) error { signals++; return nil }

	err := newDaemonLifecycle(deps).Stop(t.Context(), "/config.toml", io.Discard)

	require.ErrorContains(t, err, "could not be authenticated")
	assert.Zero(t, signals)
}

func TestDaemonStopRemovesOnlyVerifiedRuntimeRecordAfterExit(t *testing.T) {
	deps, _ := daemonLifecycleTestDeps(t)
	recordPath := filepath.Join(t.TempDir(), "daemon.41.json")
	deps.findVerified = func(context.Context, daemon.RuntimeStore, string) (daemon.RuntimeRecord, daemon.PingInfo, bool, error) {
		return daemon.RuntimeRecord{PID: 41, SourcePath: recordPath}, daemon.PingInfo{PID: 41}, true, nil
	}
	var removed []string
	deps.remove = func(path string) error {
		removed = append(removed, path)
		return nil
	}

	err := newDaemonLifecycle(deps).Stop(t.Context(), "/config.toml", io.Discard)

	require.NoError(t, err)
	assert.Equal(t, []string{recordPath}, removed)
}

func TestDaemonStopDoesNotEscalateAfterGracefulTimeout(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	deps, _ := daemonLifecycleTestDeps(t)
	deps.findVerified = func(context.Context, daemon.RuntimeStore, string) (daemon.RuntimeRecord, daemon.PingInfo, bool, error) {
		return daemon.RuntimeRecord{PID: 41, SourcePath: "/runtime/daemon.41.json"}, daemon.PingInfo{PID: 41}, true, nil
	}
	signals := 0
	deps.signal = func(int) error { signals++; return nil }
	var waitTimeout time.Duration
	deps.waitForExit = func(_ context.Context, _ int, timeout time.Duration) bool {
		waitTimeout = timeout
		return false
	}
	removes := 0
	deps.remove = func(string) error { removes++; return nil }

	err := newDaemonLifecycle(deps).Stop(t.Context(), "/config.toml", io.Discard)

	require.Error(err)
	require.ErrorContains(err, "will not force-kill")
	require.ErrorContains(err, "terminate it manually")
	assert.Equal(1, signals)
	assert.GreaterOrEqual(waitTimeout, 50*time.Second)
	assert.Zero(removes)
}

func TestDaemonRestartValidatesBeforeSignaling(t *testing.T) {
	deps, events := daemonLifecycleTestDeps(t)
	deps.loadConfig = func(string) (*config.Config, error) {
		return &config.Config{Host: "192.0.2.1", DataDir: t.TempDir()}, nil
	}

	err := newDaemonLifecycle(deps).Restart(t.Context(), "/config.toml", io.Discard)

	require.ErrorContains(t, err, "loopback TCP listener")
	assert.Empty(t, *events)
}

func TestDaemonRestartValidatesNodeIDBeforeSignaling(t *testing.T) {
	deps, events := daemonLifecycleTestDeps(t)
	deps.ensureNodeID = func(string) (string, error) {
		return "", errors.New("invalid node ID")
	}

	err := newDaemonLifecycle(deps).Restart(t.Context(), "/config.toml", io.Discard)

	require.ErrorContains(t, err, "invalid node ID")
	assert.NotContains(t, *events, "signal")
}

func TestDaemonRestartStartsWhenNotRunning(t *testing.T) {
	assert := assert.New(t)
	deps, events := daemonLifecycleTestDeps(t)
	var output bytes.Buffer

	err := newDaemonLifecycle(deps).Restart(t.Context(), "/config.toml", &output)

	require.NoError(t, err)
	assert.Equal([]string{"lock", "find", "status", "start", "unlock"}, *events)
	assert.Contains(output.String(), "was not running")
	assert.Contains(output.String(), "kenn-forge running at")
}

func TestDaemonRestartHoldsOneLockAcrossStopAndStart(t *testing.T) {
	deps, events := daemonLifecycleTestDeps(t)
	deps.findVerified = func(context.Context, daemon.RuntimeStore, string) (daemon.RuntimeRecord, daemon.PingInfo, bool, error) {
		*events = append(*events, "find")
		return daemon.RuntimeRecord{
			PID: 41, SourcePath: "/runtime/daemon.41.json",
		}, daemon.PingInfo{PID: 41}, true, nil
	}
	deps.signal = func(int) error { *events = append(*events, "signal"); return nil }
	deps.waitForExit = func(context.Context, int, time.Duration) bool {
		*events = append(*events, "wait")
		return true
	}
	deps.ensureBackground = func(context.Context, string, *config.Config) (daemon.RuntimeRecord, error) {
		*events = append(*events, "start")
		return daemonTestRuntime(42), nil
	}

	err := newDaemonLifecycle(deps).Restart(t.Context(), "/config.toml", io.Discard)

	require.NoError(t, err)
	assert.Equal(t, []string{
		"lock", "find", "signal", "wait", "remove", "start", "unlock",
	}, *events)
}

func TestDaemonRestartReturnsReplacementFailureWithoutSuccessOutput(t *testing.T) {
	deps, _ := daemonLifecycleTestDeps(t)
	deps.findVerified = func(context.Context, daemon.RuntimeStore, string) (daemon.RuntimeRecord, daemon.PingInfo, bool, error) {
		return daemon.RuntimeRecord{PID: 41, SourcePath: "/runtime/daemon.41.json"}, daemon.PingInfo{PID: 41}, true, nil
	}
	deps.ensureBackground = func(context.Context, string, *config.Config) (daemon.RuntimeRecord, error) {
		return daemon.RuntimeRecord{}, errors.New("replacement failed")
	}
	var output bytes.Buffer

	err := newDaemonLifecycle(deps).Restart(t.Context(), "/config.toml", &output)

	require.ErrorContains(t, err, "replacement failed")
	assert.Empty(t, output.String())
}

func TestDaemonStatusUsesConfigAttributedRuntimeDataDirectory(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	deps, _ := daemonLifecycleTestDeps(t)
	currentDataDir := t.TempDir()
	runtimeDataDir := t.TempDir()
	canonicalRuntimeDataDir, err := config.CanonicalDataDir(runtimeDataDir)
	require.NoError(err)
	configPath := filepath.Join(t.TempDir(), "config.toml")
	canonicalConfigPath, err := daemonruntime.CanonicalConfigPath(configPath)
	require.NoError(err)
	store := daemon.RuntimeStore{Dir: t.TempDir()}
	record := daemon.NewRuntimeRecord(
		daemonruntime.Service, "dev",
		daemon.Endpoint{Network: daemon.NetworkTCP, Address: "127.0.0.1:8091"},
	)
	record.Metadata = map[string]string{
		"config_path": canonicalConfigPath,
		"data_dir":    runtimeDataDir,
		"base_path":   "/",
	}
	_, err = store.Write(record)
	require.NoError(err)
	deps.loadConfig = func(string) (*config.Config, error) {
		return &config.Config{DataDir: currentDataDir}, nil
	}
	deps.store = func() (daemon.RuntimeStore, error) { return store, nil }
	deps.findVerifiedRecord = func(
		context.Context, daemon.RuntimeRecord, string,
	) (daemon.PingInfo, bool, error) {
		return daemon.PingInfo{}, true, nil
	}
	deps.readStatus = func(string) (runtimelock.Status, error) {
		return runtimelock.Status{
			Running: true,
			Metadata: &runtimelock.Metadata{
				PID: record.PID, ConfigPath: canonicalConfigPath,
			},
		}, nil
	}
	var gotDataDir string
	var gotJSON bool
	var gotWriter io.Writer
	deps.writeRuntimeStatus = func(dataDir string, asJSON bool, stdout io.Writer) error {
		gotDataDir, gotJSON, gotWriter = dataDir, asJSON, stdout
		return nil
	}
	var output bytes.Buffer

	err = newDaemonLifecycle(deps).Status(t.Context(), configPath, true, &output)

	require.NoError(err)
	assert.Equal(canonicalRuntimeDataDir, gotDataDir)
	assert.True(gotJSON)
	assert.Same(&output, gotWriter)
}

func daemonLifecycleTestDeps(t *testing.T) (daemonLifecycleDeps, *[]string) {
	t.Helper()
	events := &[]string{}
	dataDir := t.TempDir()
	return daemonLifecycleDeps{
		loadConfig: func(string) (*config.Config, error) {
			return &config.Config{Host: "127.0.0.1", DataDir: dataDir}, nil
		},
		store: func() (daemon.RuntimeStore, error) {
			return daemon.RuntimeStore{Dir: t.TempDir()}, nil
		},
		acquireConfigLock: func(
			context.Context,
			daemon.RuntimeStore,
			string,
		) (daemonLifecycleLock, error) {
			return noopLifecycleLock{}, nil
		},
		acquireLock: func(context.Context, daemon.RuntimeStore, string) (daemonLifecycleLock, error) {
			*events = append(*events, "lock")
			return recordingLifecycleLock{events: events}, nil
		},
		findVerified: func(context.Context, daemon.RuntimeStore, string) (daemon.RuntimeRecord, daemon.PingInfo, bool, error) {
			*events = append(*events, "find")
			return daemon.RuntimeRecord{}, daemon.PingInfo{}, false, nil
		},
		readStatus: func(string) (runtimelock.Status, error) {
			*events = append(*events, "status")
			return runtimelock.Status{}, nil
		},
		ensureNodeID: func(string) (string, error) {
			return "0123456789abcdef0123456789abcdef", nil
		},
		ensureBackground: func(context.Context, string, *config.Config) (daemon.RuntimeRecord, error) {
			*events = append(*events, "start")
			return daemonTestRuntime(42), nil
		},
		signal: func(int) error {
			*events = append(*events, "signal")
			return nil
		},
		waitForExit: func(context.Context, int, time.Duration) bool {
			*events = append(*events, "wait")
			return true
		},
		remove: func(string) error {
			*events = append(*events, "remove")
			return nil
		},
		writeRuntimeStatus: writeRuntimeStatus,
	}, events
}

func daemonTestRuntime(pid int) daemon.RuntimeRecord {
	return daemon.RuntimeRecord{
		PID: pid, Network: daemon.NetworkTCP, Address: "127.0.0.1:8091",
		Metadata: map[string]string{"base_path": "/"},
	}
}
