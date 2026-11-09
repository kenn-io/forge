package server

import (
	"context"
	"sync/atomic"
	"testing"
	"testing/synctest"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	serverfake "go.kenn.io/forge/internal/testutil/serverfake"
)

func TestEnqueueDetailSyncOrRerunRunsPendingAfterInFlight(t *testing.T) {
	serverfake.RunParallelServerTest(t)
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

func TestEnqueueDetailSyncOrRerunKeepsReplacedCompletion(t *testing.T) {
	serverfake.RunParallelServerTest(t)
	srv, _, _ := setupTestServer(t)

	synctest.Test(t, func(t *testing.T) {
		assert := assert.New(t)
		require := require.New(t)
		const key = "pr:github:github.com:acme/widget#7"
		releaseFirst := make(chan struct{})
		var order []string
		record := func(step string) func(context.Context) error {
			return func(context.Context) error {
				order = append(order, step)
				return nil
			}
		}

		require.True(srv.syncevents.EnqueueDetailSyncOrRerun(key, nil, func(context.Context) error {
			<-releaseFirst
			return nil
		}))
		synctest.Wait()

		// A mutation queues a rerun with follow-up work, then a plain refresh
		// replaces that rerun before the in-flight sync finishes.
		require.False(srv.syncevents.EnqueueDetailSyncOrRerunWithCompletion(
			key, nil, record("mutation sync"),
			func(context.Context) { order = append(order, "mutation completion") },
		))
		require.False(srv.syncevents.EnqueueDetailSyncOrRerun(key, nil, record("refresh sync")))

		close(releaseFirst)
		synctest.Wait()
		srv.bg.Wait()
		assert.Equal([]string{"refresh sync", "mutation completion"}, order)
	})
}
