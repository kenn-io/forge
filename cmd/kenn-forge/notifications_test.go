package main

import (
	"context"
	"errors"
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
		repos.Store(3)
		startTelemetryHeartbeat(handle, client, func() int { return int(repos.Load()) })

		synctest.Wait()
		require.Empty(client.repoCounts())
		time.Sleep(24 * time.Hour)
		synctest.Wait()
		require.Equal([]any{3}, client.repoCounts())

		repos.Store(5)
		time.Sleep(24 * time.Hour)
		synctest.Wait()
		require.Equal([]any{3, 5}, client.repoCounts())

		require.NoError(handle.Stop(t.Context()))
		time.Sleep(72 * time.Hour)
		synctest.Wait()
		require.Equal([]any{3, 5}, client.repoCounts())
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

func TestSetupFinishedMinuteAndFilters(t *testing.T) {
	for _, kind := range []string{"issue", "pr"} {
		for _, hidden := range []string{"", "inactive", "removed_upstream"} {
			t.Run(kind+"/"+hidden, func(t *testing.T) {
				database := dbtest.Open(t)
				synctest.Test(t, func(t *testing.T) {
					require := require.New(t)
					handle := newBackgroundLoopHandle(t.Context())
					defer func() { require.NoError(handle.Stop(t.Context())) }()
					client := &fakeTelemetryClient{}
					startSetupFinishedLoop(handle, database, client)
					synctest.Wait()
					require.Empty(client.events())
					repoID := seedSetupItem(t, database, kind)
					switch hidden {
					case "inactive":
						_, err := database.WriteDB().ExecContext(t.Context(), `UPDATE forge_repos SET lifecycle_state = 'inactive' WHERE id = ?`, repoID)
						require.NoError(err)
					case "removed_upstream":
						itemType := "issue"
						if kind == "pr" {
							itemType = "merge_request"
						}
						_, err := database.WriteDB().ExecContext(t.Context(), `INSERT INTO forge_archive_items (repo_id, item_type, item_number, provider_item_id, provider_created_at, provider_updated_at, lifecycle_state) VALUES (?, ?, 1, 'item-1', CURRENT_TIMESTAMP, CURRENT_TIMESTAMP, 'removed_upstream')`, repoID, itemType)
						require.NoError(err)
					}
					time.Sleep(time.Minute)
					synctest.Wait()
					if hidden != "" {
						require.Empty(client.events())
						return
					}
					require.Equal([]string{"setup_finished"}, client.events())
					require.Equal([]map[string]any{nil}, client.captures)
					require.NoError(handle.Stop(t.Context()))
					handle = newBackgroundLoopHandle(t.Context())
					startSetupFinishedLoop(handle, database, client)
					time.Sleep(2 * time.Minute)
					synctest.Wait()
					require.Equal([]string{"setup_finished"}, client.events())
				})
			})
		}
	}
}

func TestRunSetupFinishedStartupAndClose(t *testing.T) {
	for _, mode := range []string{"startup", "shutdown", "error", "disabled"} {
		t.Run(mode, func(t *testing.T) {
			require := require.New(t)
			root := t.TempDir()
			cfgPath := filepath.Join(root, "config.toml")
			writeMinimalConfig(t, cfgPath, filepath.Join(root, "data"), reserveFreePort(t))
			ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
			defer cancel()
			calls, closed, startup := 0, false, false
			err := runWithTelemetry(ctx, serve.Options{ConfigPath: cfgPath, DisableSync: true}, func(opts telemetry.Options) telemetry.Client {
				seedSetupItem(t, opts.Database, "issue")
				if mode == "disabled" {
					cancel()
				}
				return &fakeTelemetryClient{disabled: mode == "disabled", report: func(reportCtx context.Context) (posthog.Status, error) {
					_, found, err := opts.Database.AppMetadataValue(reportCtx, setupFinishedKey)
					require.NoError(err)
					require.False(found, "only accepted reports may persist completion")
					calls++
					if calls == 1 {
						startup = ctx.Err() == nil
					}
					cancel()
					if mode == "error" && calls == 1 {
						return "", errors.New("enqueue failed")
					}
					if mode == "shutdown" && calls == 1 {
						return posthog.StatusDisabled, nil
					}
					return posthog.StatusQueued, nil
				}, close: func() error {
					_, found, err := opts.Database.AppMetadataValue(t.Context(), setupFinishedKey)
					require.NoError(err)
					require.Equal(mode != "disabled", found, "completion must persist before the reporter and DB close")
					closed = true
					return nil
				}}
			})
			require.NoError(err)
			require.True(closed)
			require.Equal(mode != "disabled", startup, "the first report must run before shutdown")
			require.Equal(map[string]int{"startup": 1, "shutdown": 2, "error": 2, "disabled": 0}[mode], calls)
		})
	}
}
