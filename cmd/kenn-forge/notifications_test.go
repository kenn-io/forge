package main

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/stretchr/testify/require"
	"go.kenn.io/forge/internal/cli/serve"
	"go.kenn.io/forge/internal/config"
	"go.kenn.io/forge/internal/db"
	"go.kenn.io/forge/internal/server"
	"go.kenn.io/forge/internal/telemetry"
	"go.kenn.io/forge/internal/testutil/dbtest"
	"go.kenn.io/forge/internal/testutil/reposeed"
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
	report     func(context.Context) (posthog.Status, error)
	close      func() error
}

func (f *fakeTelemetryClient) Capture(event string, properties map[string]any) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.eventNames = append(f.eventNames, event)
	f.captures = append(f.captures, properties)
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

func seedSetupItem(t *testing.T, database *db.DB, kind string) int64 {
	t.Helper()
	repoID, err := reposeed.Seed(t.Context(), database, db.GitHubRepoIdentity("github.com", "acme", "widget"))
	require.NoError(t, err)
	if kind == "issue" {
		serverfake.SeedIssueForRepo(t, database, repoID, "github.com", "acme", "widget", 1, "closed", "Example")
	} else {
		serverfake.SeedPRForRepo(t, database, repoID, "github.com", "acme", "widget", 1)
	}
	return repoID
}

func localSetupWorkCheck(t *testing.T, database *db.DB) func(context.Context) (bool, error) {
	t.Helper()
	srv := server.New(database, nil, nil, "/", nil, server.ServerOptions{DisableWorkspaceBackgroundMonitors: true})
	t.Cleanup(func() { require.NoError(t, srv.Shutdown(context.Background())) })
	return srv.HasReadableProviderWork
}

func TestSetupFinishedMinuteAndFilters(t *testing.T) {
	for _, test := range []struct {
		name  string
		kinds []string
	}{
		{"issue", []string{"issue"}},
		{"pr", []string{"pr"}},
		{"inactive", []string{"issue", "pr"}},
		{"removed_upstream", []string{"issue", "pr"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			database := dbtest.Open(t)
			synctest.Test(t, func(t *testing.T) {
				require := require.New(t)
				handle := newBackgroundLoopHandle(t.Context())
				defer func() { require.NoError(handle.Stop(t.Context())) }()
				client := &fakeTelemetryClient{}
				setup := &setupFinished{database: database, reporter: client, hasWork: localSetupWorkCheck(t, database)}
				handle.startTicker("setup", time.Minute, setup.capture)
				synctest.Wait()
				require.Empty(client.events())
				var repoID int64
				for _, kind := range test.kinds {
					repoID = seedSetupItem(t, database, kind)
				}
				switch test.name {
				case "inactive":
					_, err := database.WriteDB().ExecContext(t.Context(), `UPDATE forge_repos SET lifecycle_state = 'inactive' WHERE id = ?`, repoID)
					require.NoError(err)
				case "removed_upstream":
					serverfake.MarkArchiveItemRemovedUpstreamForServerTest(t, database, repoID, db.ArchiveItemTypeIssue, 1)
					serverfake.MarkArchiveItemRemovedUpstreamForServerTest(t, database, repoID, db.ArchiveItemTypeMergeRequest, 1)
				}
				time.Sleep(time.Minute)
				synctest.Wait()
				if test.name == "inactive" || test.name == "removed_upstream" {
					require.Empty(client.events())
					return
				}
				require.Equal([]string{"setup_finished"}, client.events())
				require.Equal([]map[string]any{nil}, client.captures)
			})
		})
	}
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
			seedSetupItem(t, database, "issue")
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
						return posthog.StatusDisabled, nil
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
	seedSetupItem(t, database, "issue")
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
	setup := &setupFinished{database: database, reporter: client, hasWork: localSetupWorkCheck(t, database)}
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
