package main

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/stretchr/testify/require"
	"go.kenn.io/forge/internal/config"
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
