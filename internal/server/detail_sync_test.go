package server

import (
	"context"
	"sync/atomic"
	"testing"
	"testing/synctest"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestEnqueueDetailSyncOrRerunRunsPendingAfterInFlight(t *testing.T) {
	srv, _, _ := setupTestServer(t)

	synctest.Test(t, func(t *testing.T) {
		assert := assert.New(t)
		require := require.New(t)
		firstStarted := make(chan struct{})
		releaseFirst := make(chan struct{})
		secondDone := make(chan struct{})
		var calls atomic.Int64

		started := srv.syncevents.EnqueueDetailSyncOrRerun("pr:github:github.com:acme/widget#7", nil, func(context.Context) error {
			calls.Add(1)
			close(firstStarted)
			<-releaseFirst
			return nil
		})
		require.True(started)
		synctest.Wait()
		<-firstStarted

		started = srv.syncevents.EnqueueDetailSyncOrRerun("pr:github:github.com:acme/widget#7", nil, func(context.Context) error {
			calls.Add(1)
			close(secondDone)
			return nil
		})
		assert.False(started, "duplicate detail sync should report deduped while queuing a rerun")

		close(releaseFirst)
		synctest.Wait()
		<-secondDone
		assert.Equal(int64(2), calls.Load())
		srv.bg.Wait()
	})
}
