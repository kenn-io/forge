package server

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestEnqueueDetailSyncOrRerunRunsPendingAfterInFlight(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	srv, _ := setupTestServer(t)

	firstStarted := make(chan struct{})
	releaseFirst := make(chan struct{})
	secondDone := make(chan struct{})
	var calls atomic.Int64

	started := srv.enqueueDetailSyncOrRerun("pr:github:github.com:acme/widget#7", nil, func(context.Context) error {
		calls.Add(1)
		close(firstStarted)
		<-releaseFirst
		return nil
	})
	require.True(started)
	require.Eventually(func() bool {
		select {
		case <-firstStarted:
			return true
		default:
			return false
		}
	}, time.Second, time.Millisecond)

	started = srv.enqueueDetailSyncOrRerun("pr:github:github.com:acme/widget#7", nil, func(context.Context) error {
		calls.Add(1)
		close(secondDone)
		return nil
	})
	assert.False(started, "duplicate detail sync should report deduped while queuing a rerun")

	close(releaseFirst)
	require.Eventually(func() bool {
		select {
		case <-secondDone:
			return true
		default:
			return false
		}
	}, time.Second, time.Millisecond)
	assert.Equal(int64(2), calls.Load())
}
