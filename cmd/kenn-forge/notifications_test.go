package main

import (
	"context"
	"database/sql"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/stretchr/testify/require"
	"go.kenn.io/forge/internal/cli/serve"
	"go.kenn.io/forge/internal/config"
	"go.kenn.io/forge/internal/telemetry"
	"go.kenn.io/forge/internal/testutil/dbtest"
	"go.kenn.io/forge/internal/testutil/serverfake"
	"go.kenn.io/kit/telemetry/posthog"
)

func TestNotificationLoopStopWaitsForInFlightRun(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		require := require.New(t)
		parent, cancel := context.WithCancel(t.Context())
		defer cancel()
		handle := newBackgroundLoopHandle(parent)
		started := make(chan struct{})
		release := make(chan struct{})
		finished := make(chan struct{})
		var startedOnce sync.Once
		var finishedOnce sync.Once
		handle.startTicker("test notification", time.Millisecond, func(runCtx context.Context) error {
			startedOnce.Do(func() { close(started) })
			<-release
			finishedOnce.Do(func() { close(finished) })
			return nil
		})

		synctest.Wait()
		select {
		case <-started:
		default:
			require.Fail("notification loop did not start")
		}

		stopped := make(chan struct{})
		stopErrors := make(chan error, 1)
		go func() {
			stopErrors <- handle.Stop(t.Context())
			close(stopped)
		}()
		synctest.Wait()

		select {
		case <-stopped:
			require.Fail("Stop returned before in-flight notification run finished")
		default:
		}

		close(release)
		synctest.Wait()
		select {
		case <-finished:
		default:
			require.Fail("notification run did not finish")
		}
		select {
		case <-stopped:
			require.NoError(<-stopErrors)
		default:
			require.Fail("Stop did not return after notification run finished")
		}
	})
}

func TestNotificationLoopStopHonorsDeadline(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		require := require.New(t)
		handle := newBackgroundLoopHandle(t.Context())
		started := make(chan struct{})
		release := make(chan struct{})
		var releaseOnce sync.Once
		releaseCallback := func() { releaseOnce.Do(func() { close(release) }) }
		t.Cleanup(releaseCallback)
		handle.startTicker("test notification", time.Hour, func(context.Context) error {
			close(started)
			<-release
			return nil
		})
		synctest.Wait()
		select {
		case <-started:
		default:
			require.Fail("notification loop did not start")
		}
		stopCtx, cancel := context.WithTimeout(t.Context(), 20*time.Millisecond)
		defer cancel()

		err := handle.Stop(stopCtx)

		require.ErrorIs(err, context.DeadlineExceeded)
		releaseCallback()
		synctest.Wait()
	})
}

func TestNotificationLoopRunsBeforeFirstTickerInterval(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		handle := newBackgroundLoopHandle(t.Context())
		var calls atomic.Int64
		handle.startTicker("test notification", time.Hour, func(context.Context) error {
			calls.Add(1)
			return nil
		})

		synctest.Wait()
		require.Equal(t, int64(1), calls.Load(), "notification loop should run before first ticker interval")
		require.NoError(t, handle.Stop(t.Context()))
	})
}

func TestBackgroundLoopWaitsForFirstTickerInterval(t *testing.T) {
	require.Equal(t, 24*time.Hour, databaseOptimizeInterval)

	synctest.Test(t, func(t *testing.T) {
		handle := newBackgroundLoopHandle(t.Context())
		var calls atomic.Int64
		handle.startTickerAfterInterval("test maintenance", 24*time.Hour, func(context.Context) error {
			calls.Add(1)
			return nil
		})

		synctest.Wait()
		require.Zero(t, calls.Load())
		time.Sleep(24 * time.Hour)
		synctest.Wait()
		require.Equal(t, int64(1), calls.Load())
		require.NoError(t, handle.Stop(t.Context()))
	})
}

type fakeTelemetryClient struct {
	mu         sync.Mutex
	disabled   bool
	captures   []map[string]any
	eventNames []string
	onCapture  func(string)
	report     func(context.Context) (posthog.Status, error)
	close      func() error
}

func (f *fakeTelemetryClient) Capture(event string, properties map[string]any) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.eventNames = append(f.eventNames, event)
	f.captures = append(f.captures, properties)
	if f.onCapture != nil {
		f.onCapture(event)
	}
	return nil
}

func (f *fakeTelemetryClient) Close() error {
	if f.close != nil {
		return f.close()
	}
	return nil
}
func (f *fakeTelemetryClient) Enabled() bool { return !f.disabled }

func (f *fakeTelemetryClient) Report(ctx context.Context, event string, properties map[string]any) (posthog.Status, error) {
	if err := f.Capture(event, properties); err != nil {
		return "", err
	}
	f.mu.Lock()
	report := f.report
	f.mu.Unlock()
	if report != nil {
		return report(ctx)
	}
	return posthog.StatusQueued, nil
}

func (f *fakeTelemetryClient) events() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.eventNames)
}

func (f *fakeTelemetryClient) repoCounts() []any {
	f.mu.Lock()
	defer f.mu.Unlock()
	counts := make([]any, 0, len(f.captures))
	for _, properties := range f.captures {
		counts = append(counts, properties["repo_count"])
	}
	return counts
}

func TestTelemetryHeartbeatReportsDailyUntilStopped(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		require := require.New(t)
		handle := newBackgroundLoopHandle(t.Context())
		client := &fakeTelemetryClient{}
		var repos atomic.Int64
		repos.Store(1)
		startTelemetryHeartbeat(handle, client, func() int { return int(repos.Load()) })

		synctest.Wait()
		require.Empty(client.repoCounts())
		time.Sleep(24 * time.Hour)
		synctest.Wait()
		require.Equal([]any{"0-5"}, client.repoCounts())

		repos.Store(37)
		time.Sleep(24 * time.Hour)
		synctest.Wait()
		require.Equal([]any{"0-5", "21-100"}, client.repoCounts())

		require.NoError(handle.Stop(t.Context()))
		time.Sleep(72 * time.Hour)
		synctest.Wait()
		require.Equal([]any{"0-5", "21-100"}, client.repoCounts())
	})
}

func TestNotificationLoopSettingsSnapshotConfig(t *testing.T) {
	require := require.New(t)
	cfg := &config.Config{}
	cfg.Notifications.SyncInterval = "30s"
	cfg.Notifications.PropagationInterval = "45s"
	cfg.Notifications.BatchSize = 12

	settings := notificationLoopSettingsFromConfig(cfg)
	cfg.Notifications.SyncInterval = "5m"
	cfg.Notifications.PropagationInterval = "10m"
	cfg.Notifications.BatchSize = 99

	require.Equal(30*time.Second, settings.syncInterval)
	require.Equal(45*time.Second, settings.propagationInterval)
	require.Equal(12, settings.batchSize)
}

func TestWireSyncFailed(t *testing.T) {
	for _, disabled := range []bool{false, true} {
		t.Run(fmt.Sprintf("disabled=%t", disabled), func(t *testing.T) {
			require := require.New(t)
			client := &fakeTelemetryClient{disabled: disabled}
			callback := syncFailedCapture(client)
			if disabled {
				require.Nil(callback)
				require.Empty(client.events())
				require.Empty(client.captures)
				return
			}
			require.NotNil(callback)
			callback("merge_requests", "provider")
			require.Equal([]string{"sync_failed"}, client.events())
			require.Equal([]map[string]any{{"sync_source": "merge_requests", "reason": "provider"}}, client.captures)
		})
	}
}

func TestRunWiresSyncFailed(t *testing.T) {
	require := require.New(t)
	api := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch req.URL.Path {
		case "/api/v1/version":
			_, _ = io.WriteString(w, `{"version":"1.23.0"}`)
		case "/api/v1/repos/acme/widget":
			_, _ = io.WriteString(w, `{"id":42,"name":"widget","full_name":"acme/widget","owner":{"login":"acme"},"default_branch":"main"}`)
		default:
			w.WriteHeader(http.StatusServiceUnavailable)
		}
	}))
	t.Cleanup(api.Close)
	original := http.DefaultTransport
	http.DefaultTransport = api.Client().Transport
	t.Cleanup(func() { http.DefaultTransport = original })
	host := strings.TrimPrefix(api.URL, "https://")
	root := t.TempDir()
	cfgPath := filepath.Join(root, "config.toml")
	writeMinimalConfig(t, cfgPath, filepath.Join(root, "data"), reserveFreePort(t))
	cfgBody, err := os.ReadFile(cfgPath)
	require.NoError(err)
	require.NoError(os.WriteFile(cfgPath, fmt.Appendf(cfgBody, `
[[platforms]]
type = "gitea"
host = %[1]q
token_env = "SYNC_FAILED_TEST_TOKEN"

[[repos]]
platform = "gitea"
platform_host = %[1]q
owner = "acme"
name = "widget"
`, host), 0o600))
	t.Setenv("SYNC_FAILED_TEST_TOKEN", "test-token")
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	client := &fakeTelemetryClient{onCapture: func(event string) {
		if event == "sync_failed" {
			cancel()
		}
	}}
	err = runWithTelemetry(ctx, serve.Options{ConfigPath: cfgPath}, func(telemetry.Options) telemetry.Client { return client })
	require.NoError(err)
	require.Contains(client.events(), "sync_failed")
	client.mu.Lock()
	defer client.mu.Unlock()
	for i, event := range client.eventNames {
		if event == "sync_failed" {
			require.Equal(map[string]any{"sync_source": "merge_requests", "reason": "provider"}, client.captures[i])
			break
		}
	}
}

func TestSetupFinishedMinute(t *testing.T) {
	database := dbtest.Open(t)
	synctest.Test(t, func(t *testing.T) {
		require := require.New(t)
		handle := newBackgroundLoopHandle(t.Context())
		defer func() { require.NoError(handle.Stop(t.Context())) }()
		client := &fakeTelemetryClient{}
		var ready atomic.Bool
		checked := make(chan struct{}, 1)
		require.NotNil(startSetupFinished(handle, false, database, client, func(context.Context) (bool, error) {
			work := ready.Load()
			checked <- struct{}{}
			return work, nil
		}))
		<-checked
		synctest.Wait()
		require.Empty(client.events())
		ready.Store(true)
		time.Sleep(59 * time.Second)
		synctest.Wait()
		require.Empty(client.events())
		time.Sleep(time.Second)
		synctest.Wait()
		require.Equal([]string{"setup_finished"}, client.events())
		require.Equal([]map[string]any{nil}, client.captures)
	})
}

func TestRunSetupFinishedStartupAndClose(t *testing.T) {
	for _, mode := range []string{"startup", "shutdown", "disabled"} {
		t.Run(mode, func(t *testing.T) {
			require := require.New(t)
			root := t.TempDir()
			cfgPath := filepath.Join(root, "config.toml")
			writeMinimalConfig(t, cfgPath, filepath.Join(root, "data"), reserveFreePort(t))
			cfgBody, err := os.ReadFile(cfgPath)
			require.NoError(err)
			require.NoError(os.WriteFile(cfgPath, append(cfgBody, []byte("\n[[repos]]\nowner = \"acme\"\nname = \"widget\"\n")...), 0o600))
			require.NoError(os.MkdirAll(filepath.Join(root, "data"), 0o700))
			database := dbtest.OpenAt(t, filepath.Join(root, "data", "forge.db"))
			serverfake.SeedIssue(t, database, "acme", "widget", 1, "closed")
			require.NoError(database.Close())
			ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
			defer cancel()
			calls, closed, startup := 0, false, false
			var observationErr error
			var completed bool
			err = runWithTelemetry(ctx, serve.Options{ConfigPath: cfgPath, DisableSync: true}, func(opts telemetry.Options) telemetry.Client {
				if mode == "disabled" {
					cancel()
				}
				return &fakeTelemetryClient{disabled: mode == "disabled", report: func(context.Context) (posthog.Status, error) {
					calls++
					if calls == 1 {
						startup = ctx.Err() == nil
					}
					cancel()
					if mode == "shutdown" && calls == 1 {
						return "", context.Canceled
					}
					return posthog.StatusQueued, nil
				}, close: func() error {
					_, completed, observationErr = opts.Database.AppMetadataValue(t.Context(), setupFinishedKey)
					closed = true
					return nil
				}}
			})
			require.NoError(err)
			require.NoError(observationErr)
			require.Equal(mode != "disabled", completed, "completion must persist before the reporter and DB close")
			require.True(closed)
			require.Equal(mode != "disabled", startup, "the first report must run before shutdown")
			require.Equal(map[string]int{"startup": 1, "shutdown": 2, "disabled": 0}[mode], calls)
		})
	}
}

func TestSetupFinishedRetriesMarkerAfterWriterContention(t *testing.T) {
	require := require.New(t)
	database := dbtest.Open(t)
	var tx *sql.Tx
	var lockErr error
	client := &fakeTelemetryClient{report: func(context.Context) (posthog.Status, error) {
		tx, lockErr = database.WriteDB().BeginTx(t.Context(), nil)
		return posthog.StatusQueued, nil
	}}
	defer func() {
		if tx != nil {
			_ = tx.Rollback()
		}
	}()
	setup := &setupFinished{database: database, reporter: client, hasWork: func(context.Context) (bool, error) { return true, nil }}
	err := setup.capture(t.Context())
	require.NoError(lockErr)
	require.ErrorIs(err, context.DeadlineExceeded)
	_, found, err := database.AppMetadataValue(t.Context(), setupFinishedKey)
	require.NoError(err)
	require.False(found)
	require.NoError(tx.Rollback())
	require.NoError(setup.capture(t.Context()))
	require.NoError(setup.capture(t.Context()))
	require.Equal([]string{"setup_finished"}, client.events())
	marker, found, err := database.AppMetadataValue(t.Context(), setupFinishedKey)
	require.NoError(err)
	require.True(found)
	require.Equal("queued", marker)
}

func TestSetupFinishedCheckAndRestart(t *testing.T) {
	for _, mode := range []string{"queued", "canceled"} {
		t.Run(mode, func(t *testing.T) {
			database := dbtest.Open(t)
			synctest.Test(t, func(t *testing.T) {
				require := require.New(t)
				ctx, cancel := context.WithTimeout(t.Context(), time.Second)
				defer cancel()
				hasWork := func(ctx context.Context) (bool, error) {
					if mode == "canceled" {
						<-ctx.Done()
						return false, ctx.Err()
					}
					return true, nil
				}
				client := &fakeTelemetryClient{}
				setup := &setupFinished{database: database, reporter: client, hasWork: hasWork}
				err := setup.capture(ctx)
				if mode == "canceled" {
					require.ErrorIs(err, context.DeadlineExceeded)
					require.Empty(client.events())
				} else {
					require.NoError(err)
					require.Equal([]string{"setup_finished"}, client.events())
					restarted := &setupFinished{database: database, reporter: client, hasWork: hasWork}
					require.NoError(restarted.capture(t.Context()))
					require.Equal([]string{"setup_finished"}, client.events())
				}
				_, found, err := database.AppMetadataValue(t.Context(), setupFinishedKey)
				require.NoError(err)
				require.Equal(mode == "queued", found)
			})
		})
	}
}

func TestSetupFinishedNonqueuedStatusStopsChecks(t *testing.T) {
	for _, status := range []posthog.Status{posthog.StatusDisabled, posthog.StatusSkipped} {
		t.Run(string(status), func(t *testing.T) {
			require := require.New(t)
			database := dbtest.Open(t)
			checks := 0
			client := &fakeTelemetryClient{report: func(context.Context) (posthog.Status, error) { return status, nil }}
			setup := &setupFinished{database: database, reporter: client, hasWork: func(context.Context) (bool, error) {
				checks++
				return true, nil
			}}
			require.NoError(setup.capture(t.Context()))
			require.NoError(setup.capture(t.Context()))
			require.Equal(1, checks)
			require.Equal([]string{"setup_finished"}, client.events())
			_, found, err := database.AppMetadataValue(t.Context(), setupFinishedKey)
			require.NoError(err)
			require.False(found)
		})
	}
}

func TestSetupFinishedGateHonorsCallerDeadline(t *testing.T) {
	database := dbtest.Open(t)
	entered, release := make(chan struct{}), make(chan struct{})
	client := &fakeTelemetryClient{}
	setup := &setupFinished{database: database, reporter: client, hasWork: func(context.Context) (bool, error) {
		close(entered)
		<-release
		return true, nil
	}}
	first := make(chan error, 1)
	go func() { first <- setup.capture(t.Context()) }()
	<-entered
	//nolint:kennlint // The deadline must expire while the first capture owns the gate.
	ctx, cancel := context.WithTimeout(t.Context(), 50*time.Millisecond)
	defer cancel()
	second := make(chan error, 1)
	go func() { second <- setup.capture(ctx) }()
	select {
	case err := <-second:
		require.ErrorIs(t, err, context.DeadlineExceeded)
	case <-time.After(5 * time.Second):
		close(release)
		<-first
		require.FailNow(t, "capture waited past its deadline for the setup gate")
	}
	close(release)
	require.NoError(t, <-first)
	require.Equal(t, []string{"setup_finished"}, client.events())
}

func TestSetupLoopRegistrationSkipsExecutionWorkers(t *testing.T) {
	for _, worker := range []bool{false, true} {
		t.Run(map[bool]string{false: "daemon", true: "worker"}[worker], func(t *testing.T) {
			database := dbtest.Open(t)
			synctest.Test(t, func(t *testing.T) {
				require := require.New(t)
				client := &fakeTelemetryClient{}
				handle := newBackgroundLoopHandle(t.Context())
				checks := 0
				setup := startSetupFinished(handle, worker, database, client, func(context.Context) (bool, error) {
					checks++
					return false, nil
				})
				synctest.Wait()
				require.NoError(handle.Stop(t.Context()))
				if worker {
					require.Zero(checks, "workers must not start setup checks")
					require.Nil(setup, "workers must not retain a shutdown capture")
				} else {
					require.Equal(1, checks, "the daemon must start its setup check")
					require.NotNil(setup)
				}
			})
		})
	}
}
