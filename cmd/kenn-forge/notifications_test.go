package main

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/stretchr/testify/require"
	"go.kenn.io/forge/internal/config"
	"go.kenn.io/forge/internal/db"
	"go.kenn.io/forge/internal/testutil/dbtest"
	"go.kenn.io/forge/internal/testutil/reposeed"
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
func (f *fakeTelemetryClient) Enabled() bool { return true }

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
	require := require.New(t)
	repoID, err := reposeed.Seed(t.Context(), database, db.GitHubRepoIdentity("github.com", "acme", "widget"))
	require.NoError(err)
	if kind == "issue" {
		_, err = database.UpsertIssue(t.Context(), &db.Issue{RepoID: repoID, Number: 1, Title: "Example", State: "closed"})
	} else {
		_, err = database.UpsertMergeRequest(t.Context(), &db.MergeRequest{RepoID: repoID, Number: 1, Title: "Example", State: "merged"})
	}
	require.NoError(err)
	return repoID
}

func TestSetupFinishedStartupAndMinuteCheck(t *testing.T) {
	for _, kind := range []string{"issue", "pr"} {
		for _, cached := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/cached=%t", kind, cached), func(t *testing.T) {
				database := dbtest.Open(t)
				synctest.Test(t, func(t *testing.T) {
					require := require.New(t)
					if cached {
						seedSetupItem(t, database, kind)
					} else {
						_, err := reposeed.Seed(t.Context(), database, db.GitHubRepoIdentity("github.com", "acme", "widget"))
						require.NoError(err)
					}
					handle := newBackgroundLoopHandle(t.Context())
					client := &fakeTelemetryClient{}
					startSetupFinishedLoop(handle, database, client)
					synctest.Wait()
					if !cached {
						require.Empty(client.events())
						seedSetupItem(t, database, kind)
						time.Sleep(time.Minute - time.Nanosecond)
						synctest.Wait()
						require.Empty(client.events())
						time.Sleep(time.Nanosecond)
						synctest.Wait()
					}
					require.Equal([]string{"setup_finished"}, client.events())
					client.mu.Lock()
					properties := slices.Clone(client.captures)
					client.mu.Unlock()
					require.Equal([]map[string]any{nil}, properties)
					_, found, err := database.AppMetadataValue(t.Context(), setupFinishedKey)
					require.NoError(err)
					require.True(found)
					time.Sleep(2 * time.Minute)
					synctest.Wait()
					require.NoError(handle.Stop(t.Context()))
					// A new process sees the durable marker and exits without reporting.
					handle = newBackgroundLoopHandle(t.Context())
					startSetupFinishedLoop(handle, database, client)
					synctest.Wait()
					require.NoError(handle.Stop(t.Context()))
					require.Equal([]string{"setup_finished"}, client.events())
				})
			})
		}
	}
}

func TestSetupFinishedExcludesInvisibleItems(t *testing.T) {
	for _, kind := range []string{"issue", "pr"} {
		for _, hidden := range []string{"inactive", "removed_upstream"} {
			t.Run(kind+"/"+hidden, func(t *testing.T) {
				require := require.New(t)
				database := dbtest.Open(t)
				repoID := seedSetupItem(t, database, kind)
				if hidden == "inactive" {
					_, err := database.WriteDB().ExecContext(t.Context(), `UPDATE forge_repos SET lifecycle_state = 'inactive' WHERE id = ?`, repoID)
					require.NoError(err)
				} else {
					itemType := "issue"
					if kind == "pr" {
						itemType = "merge_request"
					}
					_, err := database.WriteDB().ExecContext(t.Context(), `INSERT INTO forge_archive_items (repo_id, item_type, item_number, provider_item_id, provider_created_at, provider_updated_at, lifecycle_state) VALUES (?, ?, 1, 'item-1', CURRENT_TIMESTAMP, CURRENT_TIMESTAMP, 'removed_upstream')`, repoID, itemType)
					require.NoError(err)
				}
				client := &fakeTelemetryClient{}
				done, err := captureSetupFinished(t.Context(), database, client)
				require.NoError(err)
				require.False(done)
				require.Empty(client.events())
				_, found, err := database.AppMetadataValue(t.Context(), setupFinishedKey)
				require.NoError(err)
				require.False(found)
			})
		}
	}
}

func TestSetupFinishedRetriesUnqueuedReports(t *testing.T) {
	for _, outcome := range []string{"disabled", "error", "queued-with-error", "cancelled", "timeout"} {
		t.Run(outcome, func(t *testing.T) {
			database := dbtest.Open(t)
			synctest.Test(t, func(t *testing.T) {
				require := require.New(t)
				seedSetupItem(t, database, "issue")
				ctx, cancel := context.WithCancel(t.Context())
				defer cancel()
				client := &fakeTelemetryClient{report: func(reportCtx context.Context) (posthog.Status, error) {
					_, found, err := database.AppMetadataValue(reportCtx, setupFinishedKey)
					require.NoError(err)
					require.True(found, "claim must precede reporting")
					switch outcome {
					case "disabled":
						return posthog.StatusDisabled, nil
					case "cancelled":
						cancel()
						return "", reportCtx.Err()
					case "timeout":
						<-reportCtx.Done()
						return "", reportCtx.Err()
					case "queued-with-error":
						return posthog.StatusQueued, errors.New("enqueue failed")
					default:
						return "", errors.New("enqueue failed")
					}
				}}
				handle := newBackgroundLoopHandle(ctx)
				startSetupFinishedLoop(handle, database, client)
				synctest.Wait()
				if outcome == "timeout" {
					time.Sleep(300 * time.Millisecond)
					synctest.Wait()
				}
				require.Equal([]string{"setup_finished"}, client.events())
				_, found, err := database.AppMetadataValue(t.Context(), setupFinishedKey)
				require.NoError(err)
				require.False(found, "unqueued report must release the claim, even after cancellation")
				client.mu.Lock()
				client.report = nil
				client.mu.Unlock()
				if outcome == "cancelled" {
					require.NoError(handle.Stop(t.Context()))
					handle = newBackgroundLoopHandle(t.Context())
					startSetupFinishedLoop(handle, database, client)
				} else {
					time.Sleep(time.Minute)
				}
				synctest.Wait()
				require.NoError(handle.Stop(t.Context()))
				require.Equal([]string{"setup_finished", "setup_finished"}, client.events())
				_, found, err = database.AppMetadataValue(t.Context(), setupFinishedKey)
				require.NoError(err)
				require.True(found)
			})
		})
	}
}

func TestSetupFinishedFinalTelemetryClose(t *testing.T) {
	for _, outcome := range []string{"queued", "timeout", "disabled-at-start"} {
		t.Run(outcome, func(t *testing.T) {
			database := dbtest.Open(t)
			synctest.Test(t, func(t *testing.T) {
				require := require.New(t)
				handle := newBackgroundLoopHandle(t.Context())
				client := &fakeTelemetryClient{}
				client.report = func(ctx context.Context) (posthog.Status, error) {
					if outcome == "timeout" {
						<-ctx.Done()
						return "", ctx.Err()
					}
					return posthog.StatusQueued, nil
				}
				startSetupFinishedLoop(handle, database, client)
				synctest.Wait()
				require.Empty(client.events())
				closed := false
				client.close = func() error {
					_, found, err := database.AppMetadataValue(t.Context(), setupFinishedKey)
					require.NoError(err)
					require.Equal(outcome == "queued", found)
					// Each Kit reporter may take its full two-second close budget.
					time.Sleep(2 * posthog.ShutdownTimeout)
					closed = true
					return nil
				}
				started := time.Now()
				errs := runMainShutdown(t.Context(), mainShutdownCallbacks{
					StopBackgroundLoops: handle.Stop,
					ShutdownPrimaryHTTP: func(context.Context) error {
						seedSetupItem(t, database, "pr")
						return nil
					},
					CloseTelemetry: func() error {
						return closeTelemetry(database, client, outcome != "disabled-at-start")
					},
					CloseDatabase: func() error {
						require.True(closed, "reporters close before the database")
						return database.Close()
					},
				})
				require.LessOrEqual(time.Since(started), 4900*time.Millisecond)
				if outcome == "timeout" {
					require.Len(errs, 1)
					require.ErrorIs(errs[0].err, context.DeadlineExceeded)
				} else {
					require.Empty(errs)
				}
				if outcome == "disabled-at-start" {
					require.Empty(client.events())
				} else {
					require.Equal([]string{"setup_finished"}, client.events())
				}
			})
		})
	}
}
