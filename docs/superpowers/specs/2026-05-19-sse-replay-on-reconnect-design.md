# SSE Replay on Reconnect Design

## Goal

Make the SSE stream durable across browser refresh, transient network blips, and laptop sleep/wake so the dashboard catches up automatically instead of getting stuck on stale state until manual refresh.

## Current Behavior

`internal/server/event_hub.go` runs the live stream with two pieces of state:

- A fan-out map of subscriber channels (`map[uint64]chan Event`, capacity 16 each), evicted on full-channel writes so a slow consumer never blocks the broadcaster.
- A single cached `sync_status` `Event`, replayed once to every new subscriber on `Subscribe`.

`internal/server/server.go` handles `/api/v1/events` by calling `s.hub.Subscribe(r.Context())` and forwarding each broadcast as `event: <type>\ndata: <json>\n\n`. No `id:` line. No buffer of recent events. Every event but the latest `sync_status` is fire-and-forget.

When the connection drops, EventSource auto-reconnects (browser behavior) and the server starts a fresh subscription with no knowledge of what was missed. Anything broadcast during the gap is lost.

## Symptom

A user closes the laptop with a PR in "syncing" state, opens it thirty minutes later, and the UI is stuck on the pre-sleep view. A PR that merged on the server still shows as open. The sync status panel does not catch up because the events that would have driven it were broadcast to a dead subscriber and never reached the next one.

## Design Decision

Add a process-scoped, monotonically increasing event id to every SSE frame and keep a fixed-size ring buffer of recent events in the hub. On reconnect, the client passes the last id it saw (via the standard `Last-Event-ID` header or a `?since=N` query parameter), the server replays everything still in the ring that is newer, and only then resumes live delivery. If the requested cursor predates the ring, the server emits exactly one synthetic `reconnect.stale` frame before resuming, and the frontend interprets that as "the gap is too big, refetch view state from scratch."

The cached-last-`sync_status` behavior stays in place for first-time subscribers who have no cursor: it remains the only way a brand-new tab learns the daemon's current sync state without a round-trip to `/api/v1/sync/status`.

Slow-consumer eviction stays unchanged. A subscriber whose buffer fills is dropped from the live fan-out; its next reconnect goes through the same replay path as any other reconnect.

## Wire Format

Every broadcast frame carries an id:

```
id: 42
event: data_changed
data: {}

```

Ids are monotonically increasing `uint64` values scoped to the hub's lifetime. The first event after process start is id 1. Restarting the daemon resets the counter to 1, since a fresh `EventHub` starts at 1.

The synthetic `reconnect.stale` frame is just another framed event with its own id; it carries no payload of its own (`data: {}`):

```
id: 124
event: reconnect.stale
data: {}

```

The `reconnect.stale` frame is the first frame the client sees on a stale reconnect. It is not buffered into the ring and never replayed; it is emitted only at the moment the server detects a stale cursor and is followed immediately by live delivery.

## Ring Buffer

The hub owns a fixed-size ring buffer of recently broadcast events. Size is configurable via a new top-level TOML field `sse_buffer_size`, default 256, validated to be `>= 16` and `<= 16384`. A size of zero is rejected; the feature is always on. The field lives at the same level as `port` and `base_path` rather than under a nested `[server]` table because no such table exists today and the project conventionally keeps server-shaped knobs at the top level.

The buffer stores each broadcast event along with its assigned id. Inserts overwrite the oldest slot once full. The buffer is guarded by the hub's existing `sync.Mutex`; `Broadcast` already holds the lock while iterating subscribers, so the ring insert costs one slice write inside the same critical section.

A `sync_status` event is still recorded as the cached latest-status entry in addition to being stored in the ring buffer. Cached delivery to new no-cursor subscribers happens before ring replay would; both paths are exclusive (a connection with a cursor never gets the cached entry).

## Reconnect Protocol

The client signals its cursor in one of two ways. The server checks them in this priority order:

1. `Last-Event-ID` request header (HTML5 SSE standard; EventSource sets it automatically on reconnect).
2. `since` query parameter (`/api/v1/events?since=42`), useful for explicit first-connect resumption from local-storage or for non-browser clients.

If both are present, the header wins. If the value fails to parse as a non-negative `uint64`, the server logs at debug level and proceeds as if no cursor was sent.

Server behavior on subscribe, in order:

1. If no cursor is supplied:
   - If a `sync_status` is cached, emit it as the first frame using its cached id (the id it had when broadcast).
   - Resume live delivery.
2. If a cursor `N` is supplied:
   - If the ring is empty or the buffer's oldest id is `> N + 1`, emit one `reconnect.stale` frame with the current next-id assigned, then resume live delivery without replay or cached injection.
   - Otherwise, walk the ring and emit every entry with id `> N`, in id order, then resume live delivery without cached injection. (If `N` is already at or above the hub's current head, the replay loop emits nothing, which is fine.)
3. The handler then loops over the live channel exactly as it does today.

`reconnect.stale` is emitted at most once per connection.

The "next-id" assigned to a synthetic `reconnect.stale` frame consumes a real id slot. This keeps client cursors monotonic across stale boundaries: after a stale, the client's next observed id will be one greater than the stale frame's id.

## Frontend Behavior

The Svelte `createEventsStore` does not need to thread `Last-Event-ID` manually. `EventSource` injects the header on browser-driven reconnects. The store gains one new listener:

- `reconnect.stale`: re-run the same loaders that run on a `data_changed` plus refetch `/api/v1/sync/status`. This is the catch-up path when the ring buffer could not bridge the gap.

The store does not need to track or report the latest seen id back to the server explicitly; the browser carries it. For non-browser callers (smoke tests, future CLIs) the `?since=N` query parameter is the fallback.

## Subscriber Channel Capacity

The per-subscriber Go channel stays at capacity 16. Ring replay copies events from the ring into the subscriber's channel before the live broadcaster starts feeding it. If the channel fills during replay (subscriber is genuinely slow to read off the network), the subscriber is dropped and the connection is closed. From the client's standpoint this is the same as any other transport-level disconnect; its next reconnect goes through the same replay path with whatever cursor it carried.

## Slow-Consumer Eviction

The existing eviction path is unchanged. Slow-consumer events still trigger `unsubscribeLocked` and a channel close. Replay is bounded by the ring size (default 256, max 16384), so a worst-case replay of a maxed-out ring writes at most that many events into a 16-slot channel; the eviction path triggers if the client is too slow to keep up, exactly as it does in live operation.

## Observability

No new metrics in this change; the daemon log already records `sse: marshal event` failures, which is the dominant failure mode that would otherwise be invisible. A future follow-up may emit a structured log line when a stale reconnect happens so operators can correlate it with sleep/wake events.

## Compatibility

Adding `id:` lines to SSE frames is backwards-compatible: every conforming SSE client (including `EventSource` and `curl --silent`) tolerates additional fields.

Older clients that do not understand the new `reconnect.stale` event type ignore unknown event names per the HTML5 spec, but lose the stale-cursor refetch behavior. Since the SPA ships from the same binary as the server, in practice the frontend update lands atomically with the server update. Cross-version skew is only a concern for the brief window of an in-place upgrade, where the worst case is the same as today's behavior: the next manual refresh or sync poll rehydrates state.

## Configuration

Default config in `EnsureDefault` does not need to mention `sse_buffer_size`; the feature is enabled with a sensible default and only power users adjusting it would set the field. Validation lives in `(*Config).Validate`, alongside the existing range checks for `port`, `sync_budget_per_hour`, and friends.

If `sse_buffer_size` is set to a value outside the allowed range, `Load` fails with a descriptive error and the daemon refuses to start, matching the existing pattern for other server-shaped knobs.

## Non-Goals

- No persistent buffer. Events still live only in memory; restarting the daemon resets ids and clears the ring.
- No delivery acknowledgement protocol. Best-effort in-memory replay is the contract; if the buffer rolls past the cursor the client refetches.
- No per-subscriber backpressure beyond the existing slow-consumer eviction.
- No change to the event schema or to which broadcasts the daemon emits.
- No frontend-side persisting of the last seen id across reload. Browser reload starts a fresh `EventSource` connection, which, for a brand-new page load, is the correct moment to refetch state from scratch anyway. The reload case is covered by the SPA's existing startup data fetches.
