package server

import (
	"context"
	"sync"
)

// DefaultSSEBufferSize is the ring-buffer capacity used when no explicit
// size is configured. Sized to cover a typical sleep/wake window of
// burst broadcasts (a few hundred PR refreshes) without being so large
// that a maxed-out replay overwhelms a slow client.
const DefaultSSEBufferSize = 256

// Event represents an SSE event to broadcast.
type Event struct {
	Type string
	Data any
}

// RecordedEvent is an Event stamped with its monotonically-increasing
// ID. The hub assigns the ID at broadcast time, scoped to its lifetime
// (so daemon restart resets the sequence). Subscribers receive
// RecordedEvent values so the SSE handler can write the id back out on
// the wire and clients can carry it on reconnect.
type RecordedEvent struct {
	ID    uint64
	Event Event
}

// EventHub manages SSE subscribers with fan-out broadcasting, monotonic
// event ids, and a ring buffer of recent events for replay on reconnect.
type EventHub struct {
	mu               sync.Mutex
	subscribers      map[uint64]chan RecordedEvent
	nextSubID        uint64
	nextEventID      uint64
	lastSyncStatus   *RecordedEvent
	lastConfigStatus *RecordedEvent
	ring             []RecordedEvent
	ringHead         int
	ringCount        int
	done             chan struct{}
	closeOnce        sync.Once
	closed           bool // guarded by mu
}

// NewEventHub creates a ready-to-use hub with the default ring capacity.
func NewEventHub() *EventHub {
	return NewEventHubWithCapacity(DefaultSSEBufferSize)
}

// NewEventHubWithCapacity creates a hub whose ring buffer holds at most
// capacity recent events. Capacity must be >= 1; values smaller than 1
// would mean the hub cannot replay anything and break the stale-cursor
// detection logic that needs at least one event to compare against.
func NewEventHubWithCapacity(capacity int) *EventHub {
	if capacity < 1 {
		panic("server: NewEventHubWithCapacity requires capacity >= 1")
	}
	return &EventHub{
		subscribers: make(map[uint64]chan RecordedEvent),
		ring:        make([]RecordedEvent, capacity),
		done:        make(chan struct{}),
	}
}

// Subscribe registers a new subscriber. When injectCached is true and a
// cached sync_status exists, it is pre-loaded onto the subscriber's
// channel so a fresh client with no cursor learns the latest sync state
// without a round-trip. Callers that handle replay themselves (the
// cursor-bearing SSE path) pass false to avoid duplicating cached
// events with ring-replay copies. The latest config.changed event is
// also pre-loaded for fresh subscribers when injectCached is true.
//
// Returns the event channel and the hub's done channel. If the hub is
// already closed, returns an immediately-closed channel and the closed
// done channel so callers can exit cleanly without leaking a goroutine.
func (h *EventHub) Subscribe(
	ctx context.Context, injectCached bool,
) (<-chan RecordedEvent, <-chan struct{}) {
	h.mu.Lock()
	if h.closed {
		h.mu.Unlock()
		ch := make(chan RecordedEvent)
		close(ch)
		return ch, h.done
	}
	id := h.nextSubID
	h.nextSubID++
	ch := make(chan RecordedEvent, 16)
	if injectCached {
		h.enqueueCachedLocked(ch)
	}
	h.subscribers[id] = ch
	h.mu.Unlock()

	go func() {
		<-ctx.Done()
		h.unsubscribe(id)
	}()

	return ch, h.done
}

// enqueueCachedLocked preloads cached status events in monotonic ID order.
// Caller must hold mu.
func (h *EventHub) enqueueCachedLocked(ch chan<- RecordedEvent) {
	var cached []RecordedEvent
	if h.lastSyncStatus != nil {
		cached = append(cached, *h.lastSyncStatus)
	}
	// Replay the latest config event so a client connecting after a
	// parse error still learns the daemon is running on stale config.
	if h.lastConfigStatus != nil {
		cached = append(cached, *h.lastConfigStatus)
	}
	if len(cached) == 2 && cached[1].ID < cached[0].ID {
		cached[0], cached[1] = cached[1], cached[0]
	}
	for _, rec := range cached {
		ch <- rec
	}
}

// unsubscribeLocked removes and closes the subscriber channel.
// Caller must hold mu.
func (h *EventHub) unsubscribeLocked(id uint64) {
	if ch, ok := h.subscribers[id]; ok {
		delete(h.subscribers, id)
		close(ch)
	}
}

// unsubscribe is the locking wrapper for context-cancel cleanup.
func (h *EventHub) unsubscribe(id uint64) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.unsubscribeLocked(id)
}

// Broadcast sends an event to all subscribers, stamps it with the next
// monotonic id, records it in the ring buffer, and updates cached
// latest-status pointers for event types that fresh subscribers need.
// Slow consumers (full channel) are evicted. Returns the assigned id,
// which is useful for tests and for callers that need to correlate the
// broadcast with downstream state.
func (h *EventHub) Broadcast(event Event) uint64 {
	h.mu.Lock()
	defer h.mu.Unlock()

	h.nextEventID++
	rec := RecordedEvent{ID: h.nextEventID, Event: event}

	switch event.Type {
	case "sync_status":
		copyRec := rec
		h.lastSyncStatus = &copyRec
	case "config.changed":
		copyRec := rec
		h.lastConfigStatus = &copyRec
	}

	h.ringStoreLocked(rec)

	for id, ch := range h.subscribers {
		select {
		case ch <- rec:
		default:
			// Slow consumer — evict
			h.unsubscribeLocked(id)
		}
	}

	return rec.ID
}

// Generation returns the current monotonic event counter without
// emitting an event. Safe to call from read-only request handlers.
func (h *EventHub) Generation() uint64 {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.nextEventID
}

// ringStoreLocked appends rec to the ring buffer, overwriting the oldest
// slot once full. Caller must hold mu.
func (h *EventHub) ringStoreLocked(rec RecordedEvent) {
	capacity := len(h.ring)
	if h.ringCount < capacity {
		h.ring[(h.ringHead+h.ringCount)%capacity] = rec
		h.ringCount++
		return
	}
	h.ring[h.ringHead] = rec
	h.ringHead = (h.ringHead + 1) % capacity
}

// RingSnapshotSince returns the recorded events with ID greater than
// cursor in chronological order, plus a stale flag.
//
//	stale=true  : the cursor cannot be verified against this hub's
//	              retained history. It either predates the ring's
//	              oldest event or points beyond this hub lifetime's
//	              last assigned event. The caller should emit a
//	              reconnect.stale frame and skip replay.
//	stale=false : the returned slice is the (possibly empty) replay.
//	              Empty means the cursor is at or ahead of head.
func (h *EventHub) RingSnapshotSince(
	cursor uint64,
) (events []RecordedEvent, stale bool) {
	h.mu.Lock()
	defer h.mu.Unlock()

	return h.ringSnapshotSinceLocked(cursor)
}

// ReplaySnapshotSince returns either the replay snapshot for cursor or,
// when the cursor is stale, an already-assigned synthetic event id for a
// reconnect.stale frame. The stale decision and synthetic id assignment
// happen under the same hub lock so no real broadcast can receive an id
// lower than the stale frame after stale has been observed.
func (h *EventHub) ReplaySnapshotSince(
	cursor uint64,
) (events []RecordedEvent, staleID uint64, stale bool) {
	h.mu.Lock()
	defer h.mu.Unlock()

	events, stale = h.ringSnapshotSinceLocked(cursor)
	if !stale {
		return events, 0, false
	}
	h.nextEventID++
	return nil, h.nextEventID, true
}

// ringSnapshotSinceLocked is the lock-held implementation of
// RingSnapshotSince. Caller must hold mu.
func (h *EventHub) ringSnapshotSinceLocked(
	cursor uint64,
) (events []RecordedEvent, stale bool) {
	if h.ringCount == 0 {
		if cursor > 0 {
			return nil, true
		}
		return nil, false
	}
	if cursor > h.nextEventID {
		return nil, true
	}

	oldest := h.ring[h.ringHead].ID
	if cursor+1 < oldest {
		return nil, true
	}

	out := make([]RecordedEvent, 0, h.ringCount)
	for i := 0; i < h.ringCount; i++ {
		rec := h.ring[(h.ringHead+i)%len(h.ring)]
		if rec.ID > cursor {
			out = append(out, rec)
		}
	}
	return out, false
}

// AssignSyntheticID consumes one event id without recording an entry in
// the ring or updating the cached sync_status. Used by the SSE handler
// to label the synthetic reconnect.stale frame so client cursors remain
// monotonic across stale boundaries.
func (h *EventHub) AssignSyntheticID() uint64 {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.nextEventID++
	return h.nextEventID
}

// SubscriberCount returns the current number of live subscribers.
// Useful for tests that need to synchronize broadcasts with subscriber
// registration.
func (h *EventHub) SubscriberCount() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.subscribers)
}

// Close shuts down the hub: closes the done channel so SSE handlers
// exit, marks the hub closed so future Subscribe calls fail fast,
// then cleans up all subscriber channels.
func (h *EventHub) Close() {
	h.closeOnce.Do(func() {
		close(h.done)
		h.mu.Lock()
		defer h.mu.Unlock()
		h.closed = true
		for id := range h.subscribers {
			h.unsubscribeLocked(id)
		}
	})
}
