package main

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
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
