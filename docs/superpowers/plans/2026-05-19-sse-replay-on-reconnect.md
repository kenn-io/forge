# SSE Replay on Reconnect Implementation Plan

Source spec: `docs/superpowers/specs/2026-05-19-sse-replay-on-reconnect-design.md`.

The work is split into five tasks, each landed in its own commit. Tasks 1 through 4 are sequential because each builds on the previous structure; task 5 is the frontend counterpart that can land after task 4.

## Task 1 — Hub event ids and ring buffer

Add a process-scoped monotonic id to every event the hub broadcasts, and keep a fixed-size ring of the most recent events.

Changes:

- `internal/server/event_hub.go`:
  - Define `RecordedEvent struct { ID uint64; Event Event }` as the in-hub representation.
  - Add `nextEventID uint64`, `ring []RecordedEvent`, `ringHead int`, `ringCount int`, `ringCap int` to `EventHub`.
  - Add `NewEventHubWithCapacity(capacity int) *EventHub` that panics on capacity < 1. Keep `NewEventHub()` defaulting to 256 by calling `NewEventHubWithCapacity(256)`.
  - `Broadcast(Event)` returns the assigned `id` for testability and for the SSE handler to use. Internally:
    - Take the lock, increment `nextEventID`, capture id.
    - If event type is `sync_status`, set `lastSyncStatus` to a `RecordedEvent{ID: id, Event: event}`.
    - Append into the ring; if full, overwrite the oldest slot (`ring[ringHead] = recorded; ringHead = (ringHead+1) % ringCap`); otherwise grow `ringCount` and place at the next free slot.
    - Iterate subscribers and send the `RecordedEvent` over the per-subscriber channel; on full channel, evict via existing `unsubscribeLocked`.
  - Change the subscriber channel element type from `Event` to `RecordedEvent`. Update channel buffer size constant if introduced; otherwise the inline `16` remains.
  - `Subscribe` signature stays `(ctx) (<-chan RecordedEvent, <-chan struct{})`. Cached `sync_status` injection becomes a single `RecordedEvent` send.

- `internal/server/event_hub_test.go`:
  - Update existing tests to read `RecordedEvent` from the channel and assert on `.Event.Type`/`.Event.Data`.
  - Add `TestEventHub_BroadcastAssignsMonotonicIDs`: 5 broadcasts return ids 1..5 in order.
  - Add `TestEventHub_RingRetainsLastN`: with capacity 4, broadcast 10 events; assert `ringRange()` (new exported-for-test method `RingSnapshot() []RecordedEvent`) returns ids 7..10.
  - Add `TestEventHub_CachedSyncStatusPreservesID`: broadcast sync_status with id N, new subscriber receives it carrying the same id.
  - Adjust `TestEventHub_SlowConsumerEvicted` to expect `RecordedEvent` values.

Acceptance:

- `go test ./internal/server -run TestEventHub_ -shuffle=on` green.

```sh
git commit -m "feat: stamp SSE events with monotonic ids and ring-buffer them" -m "Step 1 of restoring durable live updates after a refresh, blip, or sleep/wake."
```

## Task 2 — SSE wire format includes id and ring replay path

Wire the event id through the HTTP handler, accept reconnect cursors, and replay newer ring entries before resuming live delivery.

Changes:

- `internal/server/server.go`:
  - `serveSSE` accepts an additional `cursor *uint64` parameter (nil = no cursor) and `staleEventID uint64` slot if any. Build it directly from the request inside `handleSSE` / `streamEvents`.
  - Replace the `event:`/`data:` write block with `id: <id>\nevent: <type>\ndata: <json>\n\n`.
  - Add a parsing helper `parseLastEventID(r *http.Request) (uint64, bool)` that checks `Last-Event-ID` header first, then `since` query parameter; on parse failure returns `(0, false)`.
  - In `serveSSE`, after subscribe and the initial flush:
    - If cursor present: pull a snapshot of the ring via `hub.RingSnapshotSince(cursor)` (new method, returns `[]RecordedEvent` with id > cursor, or a sentinel return value indicating "stale: cursor older than oldest"). Hub method holds the lock briefly.
    - If sentinel-stale: write one `id: <next>\nevent: reconnect.stale\ndata: {}\n\n` frame using `hub.AssignSyntheticID()` (new method that increments and returns nextEventID under lock without recording in the ring). Skip cached sync_status injection.
    - Else if cursor present and not stale: drain any cached `sync_status` already sitting on the subscriber channel (which Subscribe injected) and discard it to avoid duplicating with replay (alternative: Subscribe takes a flag `injectCached bool`).
    - Else write replay entries in id order, then resume.
  - Refactor: have `EventHub.Subscribe` take an option struct or a second parameter `injectCachedSyncStatus bool`. When the handler knows it will use ring replay, it passes `false`.

- `internal/server/huma_routes.go`:
  - `streamEvents`: unwrap to `*http.Request` via `humago.Unwrap`, pass it down so the SSE serve function can read headers/query.

- `internal/server/event_hub.go`:
  - Add `Subscribe(ctx, injectCached bool)`. Old call sites updated.
  - Add `RingSnapshotSince(cursor uint64) ([]RecordedEvent, bool)`: returns `(replay, stale)`. `stale=true` indicates cursor predates the ring's oldest event AND the ring contains at least one event. If cursor equals the latest id or `>=` head, returns `(nil, false)`.
  - Add `AssignSyntheticID() uint64`: increments nextEventID, returns the new value, does NOT touch the ring or cache. Used only for `reconnect.stale` framing.

Tests added to `internal/server/server_test.go` and `event_hub_test.go`:

- `TestEventHub_RingSnapshotSince_ReplaysNewer`: capacity 8, broadcast ids 1..5, cursor=2 returns events 3,4,5; stale=false.
- `TestEventHub_RingSnapshotSince_StaleCursor`: capacity 4, broadcast ids 1..10, cursor=2 returns nil; stale=true (ring oldest is 7).
- `TestEventHub_RingSnapshotSince_AtOrAheadHead`: cursor=10 with head=10 returns nil, stale=false.
- `TestEventHub_AssignSyntheticIDIncrementsWithoutRecord`: synthetic id is current+1; ring is unchanged.
- `TestSSE_WriteFramesIncludeID`: wire-level test reading the response body and asserting the first frame has `id: N`.
- `TestSSE_NoCursorReplaysCachedSyncStatusOnly`: prime hub with sync_status, broadcast a data_changed, subscribe with no cursor — first frame is the cached sync_status (with its id), no replay of data_changed.
- `TestSSE_LastEventIDHeaderReplays`: broadcast 3 data_changed; open a fresh connection with `Last-Event-ID: 1`; first two frames are events 2 and 3 with matching ids.
- `TestSSE_SinceQueryReplays`: same as above with `?since=1`.
- `TestSSE_HeaderOverridesQueryCursor`: when both present, header wins.
- `TestSSE_StaleCursorEmitsReconnectStale`: capacity 4, broadcast ids 1..10, connect with `Last-Event-ID: 2`; first frame is `event: reconnect.stale` with id=11; subsequent broadcasts arrive normally.
- `TestSSE_InvalidCursorFallsBackToLiveOnly`: `Last-Event-ID: not-a-number` is treated as no cursor (cached sync_status delivered if present, no replay, no stale).
- `TestSSE_CursorAtHeadReplaysNothing`: broadcast 3 events, connect with `Last-Event-ID: 3`; only future events appear.

Acceptance:

- `go test ./internal/server -shuffle=on` green.
- `make lint` and `make vet` clean.

```sh
git commit -m "feat: replay missed SSE events on reconnect via Last-Event-ID" -m "Live updates survive transient network blips and laptop sleep/wake by replaying buffered events when the cursor is still in range, or emitting reconnect.stale to trigger a frontend refetch when it is not."
```

## Task 3 — Config knob for ring size

Surface the ring buffer capacity as a TOML setting and wire it into the hub constructor.

Changes:

- `internal/config/config.go`:
  - Add `SSEBufferSize int` with `toml:"sse_buffer_size"` to the `Config` struct.
  - Default to `defaultSSEBufferSize = 256` if zero on `Load`.
  - In `Validate`, if `SSEBufferSize` is set, require `16 <= SSEBufferSize <= 16384`; otherwise leave the default. Error message: `"config: sse_buffer_size must be between 16 and 16384"`.
  - Add a helper `func (c *Config) SSEBufferSizeOrDefault() int` returning the configured value or the default.
  - Export-as-TOML form (`tomlForm` struct around line 1175): mirror the field with `omitempty`.

- `internal/config/config_test.go` (or wherever validate tests live):
  - Add `TestConfig_SSEBufferSize_DefaultWhenUnset` asserting `Load` produces 256 when absent.
  - Add `TestConfig_SSEBufferSize_RejectsBelowMin` (size = 8) and `TestConfig_SSEBufferSize_RejectsAboveMax` (size = 17000).
  - Add `TestConfig_SSEBufferSize_AcceptsValidRange` (size = 1024).

- `internal/server/server.go`:
  - In `newServer`, replace `hub: NewEventHub()` with `hub: NewEventHubWithCapacity(cfg.SSEBufferSizeOrDefault())`. When `cfg == nil` (test path via `server.New(... nil ...)`), fall back to `NewEventHub()` (defaults to 256).

Acceptance:

- `go test ./internal/config ./internal/server -shuffle=on` green.

```sh
git commit -m "feat: configure SSE replay buffer size via sse_buffer_size" -m "Operators who run many concurrent tabs or notice the ring rolling over on sleep/wake can tune the buffer. Default 256, range 16..16384."
```

## Task 4 — End-to-end wire tests via httptest.NewServer

Add black-box e2e tests under `internal/server/e2etest/` that boot a real HTTP server, open an SSE connection, simulate a disconnect, reopen with `Last-Event-ID`, and assert on the parsed wire frames.

Changes:

- `internal/server/e2etest/sse_replay_test.go` (new):
  - Helper `parseSSEFrames(t, r io.Reader, want int) []sseFrame` that reads up to `want` frames or times out, where `sseFrame { ID string; Event string; Data string }`.
  - `TestE2E_SSEReconnectReplaysMissedEvents`: boot via `httptest.NewServer(srv)`. Broadcast ids 1..3, drain them on a first connection, close it. Broadcast ids 4..5 while disconnected. Open a second connection with `Last-Event-ID: 3`. Assert it receives exactly events 4 and 5 with the right ids, then receives a freshly broadcast event 6.
  - `TestE2E_SSEStaleCursorEmitsReconnectStale`: configure a small buffer (capacity 4) via `newServerWithSSEBufferSize(t, 4)`; broadcast 10 events; second connection with `Last-Event-ID: 2`; assert first frame is `reconnect.stale` with id=11, then subsequent live frames flow.
  - `TestE2E_SSESinceQueryWorks`: same as the header test but using `?since=N`.
  - `TestE2E_SSEFirstConnectGetsCachedSyncStatus`: broadcast sync_status, then connect with no header/query; assert first frame is sync_status with the assigned id.

  Helper `newServerWithSSEBufferSize(t, n int)` constructs a `*server.Server` whose hub has the requested ring size, by loading a tiny TOML config that sets `sse_buffer_size`.

Acceptance:

- `go test ./internal/server/e2etest -shuffle=on` green.
- `make test-short` and `make test` green from repo root.

```sh
git commit -m "test: cover SSE replay end-to-end through a real HTTP server" -m "Wire-level e2e: open SSE, close, reconnect with Last-Event-ID, and assert on the actual id/event/data lines returned by the daemon."
```

## Task 5 — Frontend reconnect.stale handler

Teach the events store to react to the new `reconnect.stale` event by re-running view loaders.

Changes:

- `packages/ui/src/stores/events.svelte.ts`:
  - Add an option `onReconnectStale?: () => void`.
  - Add an `EventSource.addEventListener("reconnect.stale", () => opts.onReconnectStale?.())` in `connect()`.
  - Keep the existing `data_changed` and `sync_status` dispatch as-is.

- `packages/ui/src/stores/container.svelte.ts` (or wherever the events store is wired into the app — call site identified by tracing `createEventsStore` consumers):
  - On `reconnect.stale`, re-run the same actions a `data_changed` triggers (typically `pulls.loadPulls()` and `issues.loadIssues()`) AND force a fresh `sync.refresh()` to refetch `/api/v1/sync/status`.

- `frontend/src/lib/stores/events.svelte.test.ts`:
  - Add `it("fires onReconnectStale for reconnect.stale frames")` mirroring the existing `data_changed` test.
  - Add `it("ignores unknown event types without throwing")` — defensive.

Acceptance:

- `cd frontend && bun install && bun run typecheck && bun run test` green.

```sh
git commit -m "feat(frontend): refetch view state on reconnect.stale events" -m "When the server signals that the SSE gap is too large to replay, the SPA reloads pull, issue, and sync state instead of staying on the stale snapshot."
```

## Out of Scope (Follow-Ups)

- Structured `slog` line on every stale reconnect, with the cursor value, the ring's oldest id, and the assigned synthetic id. Useful for correlating with sleep/wake events.
- Persisting the last seen id in `localStorage` so a hard refresh can also use `?since=N`. The reload path currently refetches everything anyway.
- A `/api/v1/sync/status` follow-up call inside the frontend on `reconnect.stale` if not already implicitly covered by the `sync` store's refresh path.
- Exposing `nextEventID` and `oldestRingID` over an admin endpoint for debugging.
