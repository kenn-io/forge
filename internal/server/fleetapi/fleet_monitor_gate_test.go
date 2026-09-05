package fleetapi

import (
	"context"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestFleetMonitorLoopSkipsWhileNoSubscribers(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		assert := assert.New(t)
		require := require.New(t)
		var subscribers atomic.Int32
		gate := newFleetMonitorGate("test", func() bool { return subscribers.Load() > 0 })
		var passes atomic.Int32
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		go runFleetMonitorLoop(ctx, time.Minute, gate, func(context.Context) {
			passes.Add(1)
		})

		// Idle for several intervals: every wake-up is recorded as a skip and
		// nothing runs, not even the initial pass.
		time.Sleep(5 * time.Minute)
		synctest.Wait()
		assert.Equal(int32(0), passes.Load(), "no pass runs without subscribers")
		assert.Greater(gate.skipped(), int64(1), "idle wake-ups are recorded")

		// A subscriber is served within the idle probe interval, not a full
		// interval later.
		subscribers.Store(1)
		time.Sleep(fleetMonitorIdleProbeInterval)
		synctest.Wait()
		require.Equal(int32(1), passes.Load(), "first subscriber triggers a prompt pass")

		// With a subscriber the loop paces on the regular interval.
		time.Sleep(time.Minute)
		synctest.Wait()
		assert.Equal(int32(2), passes.Load())

		// Losing the subscriber returns the loop to skipping.
		skippedBefore := gate.skipped()
		subscribers.Store(0)
		time.Sleep(3 * time.Minute)
		synctest.Wait()
		assert.Equal(int32(2), passes.Load(), "passes stop once subscribers leave")
		assert.Greater(gate.skipped(), skippedBefore)
	})
}

func TestFleetMonitorGateWithoutSubscriberSourceAlwaysRuns(t *testing.T) {
	gate := newFleetMonitorGate("test", nil)
	assert.True(t, gate.active())
	assert.Equal(t, int64(0), gate.skipped())
}
