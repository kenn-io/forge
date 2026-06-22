package main

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"go.kenn.io/middleman/internal/config"
)

func TestNotificationLoopStopWaitsForInFlightRun(t *testing.T) {
	require := require.New(t)
	parent, cancel := context.WithCancel(t.Context())
	defer cancel()
	handle := newNotificationLoopHandle(parent)
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

	select {
	case <-started:
	case <-time.After(time.Second):
		require.Fail("notification loop did not start")
	}

	stopped := make(chan struct{})
	go func() {
		handle.Stop()
		close(stopped)
	}()

	select {
	case <-stopped:
		require.Fail("Stop returned before in-flight notification run finished")
	case <-time.After(25 * time.Millisecond):
	}

	close(release)
	select {
	case <-finished:
	case <-time.After(time.Second):
		require.Fail("notification run did not finish")
	}
	select {
	case <-stopped:
	case <-time.After(time.Second):
		require.Fail("Stop did not return after notification run finished")
	}
}

func TestNotificationLoopRunsBeforeFirstTickerInterval(t *testing.T) {
	parent, cancel := context.WithCancel(t.Context())
	defer cancel()
	handle := newNotificationLoopHandle(parent)
	defer handle.Stop()

	started := make(chan struct{})
	var startedOnce sync.Once
	handle.startTicker("test notification", time.Hour, func(runCtx context.Context) error {
		startedOnce.Do(func() { close(started) })
		return nil
	})

	require.Eventually(t, func() bool {
		select {
		case <-started:
			return true
		default:
			return false
		}
	}, 200*time.Millisecond, 10*time.Millisecond, "notification loop should run before first ticker interval")
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
