package server

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestEventHubGenerationReadsWithoutBroadcast(t *testing.T) {
	h := NewEventHub()
	defer h.Close()

	g0 := h.Generation()
	h.Broadcast(Event{Type: "data_changed", Data: struct{}{}})
	assert.Equal(t, g0+1, h.Generation(), "Generation must advance by exactly one per Broadcast")
}

func TestEventHub_SubscribeReceivesBroadcast(t *testing.T) {
	hub := NewEventHub()
	defer hub.Close()

	ch, _ := hub.Subscribe(t.Context(), true)
	hub.Broadcast(Event{Type: "data_changed", Data: struct{}{}})

	select {
	case ev := <-ch:
		assert.Equal(t, "data_changed", ev.Event.Type)
	case <-time.After(time.Second):
		require.FailNow(t, "timed out waiting for event")
	}
}

func TestEventHub_UnsubscribeOnContextCancel(t *testing.T) {
	hub := NewEventHub()
	defer hub.Close()

	ctx, cancel := context.WithCancel(t.Context())
	ch, _ := hub.Subscribe(ctx, true)
	cancel()

	// Receive blocks until the cleanup goroutine closes the channel,
	// up to a generous safety timeout. No fixed sleep — the test
	// completes as soon as the channel is actually closed.
	select {
	case _, ok := <-ch:
		assert.False(t, ok, "channel should be closed after context cancel")
	case <-time.After(time.Second):
		require.FailNow(t, "channel was not closed within 1s of context cancel")
	}
}

func TestEventHub_ConcurrentBroadcastSafety(t *testing.T) {
	hub := NewEventHub()
	defer hub.Close()

	ch, _ := hub.Subscribe(t.Context(), true)

	done := make(chan struct{})
	go func() {
		for i := range 100 {
			hub.Broadcast(Event{Type: "sync_status", Data: i})
		}
		close(done)
	}()
	go func() {
		for i := range 100 {
			hub.Broadcast(Event{Type: "data_changed", Data: i})
		}
	}()

	<-done
	// Drain channel - should not panic
	for len(ch) > 0 {
		<-ch
	}
}

func TestEventHub_SlowConsumerEvicted(t *testing.T) {
	hub := NewEventHub()
	defer hub.Close()

	ch, _ := hub.Subscribe(t.Context(), true)

	// Fill buffer (16) + one more to trigger eviction
	for i := range 17 {
		hub.Broadcast(Event{Type: "data_changed", Data: i})
	}

	// Drain buffered events; channel should close
	count := 0
	for range ch {
		count++
	}
	assert.Equal(t, 16, count, "should receive exactly buffer-size events before close")
}

func TestEventHub_SyncStatusCachedForNewSubscribers(t *testing.T) {
	hub := NewEventHub()
	defer hub.Close()

	hub.Broadcast(Event{Type: "sync_status", Data: map[string]bool{"running": true}})

	ch, _ := hub.Subscribe(t.Context(), true)

	select {
	case ev := <-ch:
		assert.Equal(t, "sync_status", ev.Event.Type)
	case <-time.After(time.Second):
		require.FailNow(t, "expected cached sync_status")
	}
}

func TestEventHub_DataChangedNotCached(t *testing.T) {
	hub := NewEventHub()
	defer hub.Close()

	hub.Broadcast(Event{Type: "data_changed", Data: struct{}{}})

	ch, _ := hub.Subscribe(t.Context(), true)

	select {
	case <-ch:
		require.FailNow(t, "data_changed should not be cached for new subscribers")
	case <-time.After(50 * time.Millisecond):
		// expected
	}
}

func TestEventHub_NoCacheBeforeAnyBroadcast(t *testing.T) {
	hub := NewEventHub()
	defer hub.Close()

	ch, _ := hub.Subscribe(t.Context(), true)

	select {
	case <-ch:
		require.FailNow(t, "expected no pre-loaded event")
	case <-time.After(50 * time.Millisecond):
		// expected
	}
}

func TestEventHub_CacheUpdatedOnLatestSyncStatus(t *testing.T) {
	hub := NewEventHub()
	defer hub.Close()

	hub.Broadcast(Event{Type: "sync_status", Data: "t1"})
	hub.Broadcast(Event{Type: "sync_status", Data: "t2"})

	ch, _ := hub.Subscribe(t.Context(), true)

	ev := <-ch
	assert.Equal(t, "t2", ev.Event.Data, "new subscriber should get the latest cached status")
}

func TestEventHub_CachedStatusesPreserveIDOrder(t *testing.T) {
	assert := assert.New(t)

	hub := NewEventHub()
	defer hub.Close()

	configID := hub.Broadcast(Event{
		Type: "config.changed",
		Data: map[string]any{"valid": true},
	})
	syncID := hub.Broadcast(Event{Type: "sync_status", Data: "running"})

	ch, _ := hub.Subscribe(t.Context(), true)

	first := <-ch
	second := <-ch
	assert.Equal(configID, first.ID)
	assert.Equal("config.changed", first.Event.Type)
	assert.Equal(syncID, second.ID)
	assert.Equal("sync_status", second.Event.Type)
}

func TestEventHub_SubscribeOrderingWithBroadcast(t *testing.T) {
	assert := assert.New(t)

	hub := NewEventHub()
	defer hub.Close()

	hub.Broadcast(Event{Type: "sync_status", Data: "cached"})

	ch, _ := hub.Subscribe(t.Context(), true)
	hub.Broadcast(Event{Type: "data_changed", Data: "live"})

	ev1 := <-ch
	assert.Equal("sync_status", ev1.Event.Type)
	assert.Equal("cached", ev1.Event.Data)

	ev2 := <-ch
	assert.Equal("data_changed", ev2.Event.Type)
	assert.Equal("live", ev2.Event.Data)
}

func TestEventHub_CloseUnsubscribesAll(t *testing.T) {
	hub := NewEventHub()

	ch1, done := hub.Subscribe(t.Context(), true)
	ch2, _ := hub.Subscribe(t.Context(), true)

	hub.Close()

	// done channel should be closed
	select {
	case <-done:
	case <-time.After(time.Second):
		require.FailNow(t, "done channel should be closed")
	}

	// subscriber channels should be closed
	_, ok1 := <-ch1
	assert.False(t, ok1)
	_, ok2 := <-ch2
	assert.False(t, ok2)
}

func TestEventHub_ConfigChangedCachedForNewSubscribers(t *testing.T) {
	hub := NewEventHub()
	defer hub.Close()

	hub.Broadcast(Event{
		Type: "config.changed",
		Data: map[string]any{"valid": true},
	})

	ch, _ := hub.Subscribe(t.Context(), true)

	select {
	case ev := <-ch:
		assert.Equal(t, "config.changed", ev.Event.Type)
	case <-time.After(time.Second):
		require.FailNow(t, "expected cached config.changed event")
	}
}

func TestEventHub_LatestConfigStatusReplayedToLateSubscriber(t *testing.T) {
	assert := assert.New(t)

	hub := NewEventHub()
	defer hub.Close()

	hub.Broadcast(Event{
		Type: "config.changed",
		Data: map[string]any{"valid": false, "error": "first"},
	})
	hub.Broadcast(Event{
		Type: "config.changed",
		Data: map[string]any{"valid": true},
	})

	ch, _ := hub.Subscribe(t.Context(), true)

	ev := <-ch
	assert.Equal("config.changed", ev.Event.Type)
	data, ok := ev.Event.Data.(map[string]any)
	require.True(t, ok)
	assert.Equal(true, data["valid"], "subscriber should see the most recent config status")
}

func TestEventHub_BroadcastAfterSlowConsumerEviction(t *testing.T) {
	hub := NewEventHub()
	defer hub.Close()

	// Subscribe slow consumer — never read
	_, _ = hub.Subscribe(t.Context(), true)

	// Fill + overflow to evict
	for i := range 17 {
		hub.Broadcast(Event{Type: "data_changed", Data: i})
	}

	// New broadcast should not panic (evicted subscriber gone)
	hub.Broadcast(Event{Type: "data_changed", Data: "after-eviction"})
}

func TestEventHub_BroadcastAssignsMonotonicIDs(t *testing.T) {
	hub := NewEventHub()
	defer hub.Close()

	assert := assert.New(t)
	for i := uint64(1); i <= 5; i++ {
		got := hub.Broadcast(Event{Type: "data_changed", Data: i})
		assert.Equal(i, got, "id %d should match broadcast order", i)
	}
}

func TestEventHub_BroadcastIDStartsAtOne(t *testing.T) {
	hub := NewEventHub()
	defer hub.Close()
	assert.Equal(t, uint64(1), hub.Broadcast(Event{Type: "data_changed", Data: nil}))
}

func TestEventHub_RingSnapshotSince_ReplaysNewer(t *testing.T) {
	assert := assert.New(t)
	hub := NewEventHubWithCapacity(8)
	defer hub.Close()

	for i := 1; i <= 5; i++ {
		hub.Broadcast(Event{Type: "data_changed", Data: i})
	}

	replay, stale := hub.RingSnapshotSince(2)
	assert.False(stale)
	assert.Len(replay, 3)
	for i, want := range []uint64{3, 4, 5} {
		assert.Equal(want, replay[i].ID)
	}
}

func TestEventHub_RingSnapshotSince_StaleCursor(t *testing.T) {
	hub := NewEventHubWithCapacity(4)
	defer hub.Close()

	for i := 1; i <= 10; i++ {
		hub.Broadcast(Event{Type: "data_changed", Data: i})
	}

	// Oldest in the ring is id 7 (10-4+1). Cursor 2 is stale.
	replay, stale := hub.RingSnapshotSince(2)
	assert.True(t, stale)
	assert.Nil(t, replay)
}

func TestEventHub_RingSnapshotSince_AtOrAheadHead(t *testing.T) {
	assert := assert.New(t)
	hub := NewEventHubWithCapacity(8)
	defer hub.Close()

	for i := 1; i <= 3; i++ {
		hub.Broadcast(Event{Type: "data_changed", Data: i})
	}

	replay, stale := hub.RingSnapshotSince(3)
	assert.False(stale)
	assert.Empty(replay)

	replay, stale = hub.RingSnapshotSince(99)
	assert.True(stale)
	assert.Empty(replay)
}

func TestEventHub_RingSnapshotSince_EmptyRing(t *testing.T) {
	assert := assert.New(t)
	hub := NewEventHub()
	defer hub.Close()

	replay, stale := hub.RingSnapshotSince(0)
	assert.False(stale)
	assert.Empty(replay)

	replay, stale = hub.RingSnapshotSince(1)
	assert.True(stale)
	assert.Empty(replay)
}

func TestEventHub_RingSnapshotSince_AdjacentCursorIsLive(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	// Cursor exactly one less than oldest is "the next event we missed
	// is the oldest one in the ring" — not stale.
	hub := NewEventHubWithCapacity(4)
	defer hub.Close()

	for i := 1; i <= 10; i++ {
		hub.Broadcast(Event{Type: "data_changed", Data: i})
	}

	// oldest = 7; cursor = 6 means client saw event 6 and missed 7,8,9,10.
	replay, stale := hub.RingSnapshotSince(6)
	assert.False(stale)
	require.Len(replay, 4)
	assert.Equal(uint64(7), replay[0].ID)
	assert.Equal(uint64(10), replay[3].ID)
}

func TestEventHub_AssignSyntheticIDIncrementsWithoutRecord(t *testing.T) {
	assert := assert.New(t)
	hub := NewEventHub()
	defer hub.Close()

	hub.Broadcast(Event{Type: "data_changed", Data: 1})
	hub.Broadcast(Event{Type: "data_changed", Data: 2})

	syn := hub.AssignSyntheticID()
	assert.Equal(uint64(3), syn)

	// Ring should still contain 2 events (the synthetic id did not record).
	replay, stale := hub.RingSnapshotSince(0)
	assert.False(stale)
	assert.Len(replay, 2)
	assert.Equal(uint64(1), replay[0].ID)
	assert.Equal(uint64(2), replay[1].ID)

	// Next real broadcast continues from after the synthetic id.
	next := hub.Broadcast(Event{Type: "data_changed", Data: 3})
	assert.Equal(uint64(4), next)
}

func TestEventHub_ReplaySnapshotSinceAssignsStaleIDBeforeFutureBroadcast(t *testing.T) {
	assert := assert.New(t)
	hub := NewEventHub()
	defer hub.Close()

	replay, staleID, stale := hub.ReplaySnapshotSince(99)
	assert.True(stale)
	assert.Empty(replay)
	assert.Equal(uint64(1), staleID)

	next := hub.Broadcast(Event{Type: "data_changed", Data: "after-stale"})
	assert.Equal(uint64(2), next)
}

func TestEventHub_CapacityRetainsLatest(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	hub := NewEventHubWithCapacity(3)
	defer hub.Close()

	for i := 1; i <= 7; i++ {
		hub.Broadcast(Event{Type: "data_changed", Data: i})
	}

	replay, stale := hub.RingSnapshotSince(4)
	assert.False(stale)
	require.Len(replay, 3)
	assert.Equal(uint64(5), replay[0].ID)
	assert.Equal(uint64(6), replay[1].ID)
	assert.Equal(uint64(7), replay[2].ID)
}

func TestNewEventHubWithCapacity_RejectsZero(t *testing.T) {
	assert.Panics(t, func() {
		_ = NewEventHubWithCapacity(0)
	})
}

func TestEventHub_CachedSyncStatusPreservesID(t *testing.T) {
	hub := NewEventHub()
	defer hub.Close()

	wantID := hub.Broadcast(Event{Type: "sync_status", Data: "v1"})

	ch, _ := hub.Subscribe(t.Context(), true)
	select {
	case ev := <-ch:
		assert.Equal(t, wantID, ev.ID)
		assert.Equal(t, "sync_status", ev.Event.Type)
	case <-time.After(time.Second):
		require.FailNow(t, "expected cached sync_status")
	}
}

func TestEventHub_SubscribeWithoutCachedSkipsInjection(t *testing.T) {
	hub := NewEventHub()
	defer hub.Close()

	hub.Broadcast(Event{Type: "sync_status", Data: "v1"})

	ch, _ := hub.Subscribe(t.Context(), false)
	select {
	case ev := <-ch:
		require.FailNowf(t, "unexpected", "expected no cached inject; got %+v", ev)
	case <-time.After(50 * time.Millisecond):
		// expected
	}
}
