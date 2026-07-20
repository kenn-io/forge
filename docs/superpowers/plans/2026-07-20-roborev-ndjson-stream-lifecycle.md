# Roborev NDJSON Stream Lifecycle Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Consume roborev's NDJSON event stream without leaking idle or active connections when the reviews UI disconnects or remounts.

**Architecture:** Replace the roborev jobs store's `EventSource` with one abortable fetch session that parses newline-delimited JSON and owns both its `AbortController` and active body reader. A bounded reconnect loop starts only while the store remains connected; a real-TCP reverse-proxy regression test proves downstream cancellation reaches an idle upstream request before another connection is opened.

**Tech Stack:** Svelte 5 rune store, TypeScript Fetch/Streams APIs, Vite+ Vitest, Go `httputil.ReverseProxy`, `httptest`.

## Global Constraints

- Do not use `EventSource` against roborev's `application/x-ndjson` endpoint.
- Teardown must abort both a response that is waiting for headers and an active response body.
- Reconnects must use explicit bounded backoff and stop after teardown.
- Tests must assert owned lifecycle and parsing behavior, not Fetch or Streams library behavior.
- Run frontend commands through Vite+ and never use npm.

---

### Task 1: Abortable roborev NDJSON stream

**Files:**

- Modify: `frontend/src/lib/stores/roborev-jobs.test.ts`
- Modify: `packages/ui/src/stores/roborev/jobs.svelte.ts`
- Modify: `packages/ui/src/views/ReviewsView.svelte`
- Modify: `packages/ui/src/components/workspace/WorkspaceRightSidebar.svelte`

**Interfaces:**

- Consumes: `fetch(url, { headers: { Accept: "application/x-ndjson" }, signal })` and roborev event objects with a string `type` field.
- Produces: `connectEventStream(baseUrl: string)`, `disconnectEventStream(): void`, and `isEventStreamConnected(): boolean` on `JobsStore`.

- [x] **Step 1: Write failing lifecycle and wire-format tests**

Add focused store tests that:

```ts
store.connectEventStream("/api/roborev");
store.disconnectEventStream();
expect(capturedSignal?.aborted).toBe(true);
```

for a fetch that has not returned headers, and that feed split NDJSON chunks through a `ReadableStream`, assert `review.completed` reloads jobs, then disconnect and assert the reader is cancelled. Use fake timers to assert an ended stream reconnects after 1 second and a disconnected stream does not reconnect.

- [x] **Step 2: Run the store test and verify RED**

Run:

```sh
cd frontend && ../node_modules/.bin/vp test run src/lib/stores/roborev-jobs.test.ts --project unit
```

Expected: FAIL because `connectEventStream` and `disconnectEventStream` do not exist and the current implementation constructs `EventSource`.

- [x] **Step 3: Implement the minimal abortable reader**

Replace the `EventSource` fields with one session:

```ts
interface EventStreamSession {
  controller: AbortController;
  reader: ReadableStreamDefaultReader<Uint8Array> | null;
}
```

`connectEventStream` clears any prior session, stores the endpoint, and starts a fetch. The read loop uses `TextDecoder` with a carry buffer, parses complete non-empty lines independently, and calls `loadJobs()` for `job.status_changed` and `review.completed`. The current session alone may update connection state or schedule reconnect. `disconnectEventStream` clears the reconnect timer and endpoint, aborts the controller, cancels the active reader, and marks the stream disconnected. Rename both Svelte callers to the new methods rather than retaining SSE aliases.

- [x] **Step 4: Run the store test and verify GREEN**

Run the Step 2 command. Expected: all tests pass with no unhandled abort errors.

- [x] **Step 5: Validate the changed Svelte modules**

Run:

```sh
vp exec -- svelte-mcp svelte-autofixer ./packages/ui/src/stores/roborev/jobs.svelte.ts --svelte-version 5
vp exec -- svelte-mcp svelte-autofixer ./packages/ui/src/views/ReviewsView.svelte --svelte-version 5
vp exec -- svelte-mcp svelte-autofixer ./packages/ui/src/components/workspace/WorkspaceRightSidebar.svelte --svelte-version 5
```

Expected: no new issues attributable to the stream change.

### Task 2: Reverse-proxy idle cancellation regression

**Files:**

- Modify: `internal/server/roborev_proxy_test.go`

**Interfaces:**

- Consumes: `/api/roborev/api/stream/events` through a real `httptest.Server` and an upstream handler blocked on `r.Context().Done()`.
- Produces: regression coverage proving cancellation crosses the Middleman reverse proxy before a replacement request is opened.

- [x] **Step 1: Write the failing real-TCP lifecycle test**

Start an upstream test server whose first request emits no headers or events, record request start and context cancellation on channels, then cancel the downstream request. Assert the first upstream context closes before issuing and observing a second request. Cancel the second request and wait for its upstream context to close so the test owns all resources.

- [x] **Step 2: Run the proxy test and verify its behavior**

Run:

```sh
go test ./internal/server -run 'TestRoborevProxyCancelsIdleUpstreamBeforeReconnect|TestRoborevNDJSONPassThrough' -shuffle=on
```

Expected: PASS with the existing proxy because its request-context propagation is the required boundary; if it fails, fix only the proxy cancellation defect exposed by the test.

- [x] **Step 3: Align the existing stream test terminology**

Rename `TestRoborevSSEPassThrough` to `TestRoborevNDJSONPassThrough` and keep its streaming assertions unchanged.

### Task 3: Upstream coordination and completion

**Files:**

- Modify only if durable context changed: repository context documentation selected by `context-sync --commit`.

**Interfaces:**

- Consumes: current roborev issue/PR state and the confirmed daemon handler behavior.
- Produces: an upstream issue or existing-thread comment requesting an immediate header flush and idle heartbeat, with the mandatory agent attribution footer.

- [x] **Step 1: Search upstream before creating a duplicate**

Search `roborev-dev/roborev` issues and pull requests for stream header flushing, idle connections, or heartbeat work. If an existing thread covers it, add the confirmed Middleman proxy evidence there; otherwise create a focused issue. End any GitHub text written by the agent with:

```html
<sup>generated by a clanker</sup>
```

- [x] **Step 2: Run affected verification**

Run the narrow store and proxy tests, the full frontend Vitest suite, frontend type/check tooling, and the affected roborev full-stack Playwright suite if its managed daemon can run locally. If the managed e2e lane is unavailable, report the exact environmental blocker and retain the store plus real-TCP proxy evidence.

- [x] **Step 3: Sync context, commit, and close Kata**

Invoke the repository-local `context-sync` skill with `--commit`, then invoke the mandatory commit skill and create a conventional commit without amending or bypassing hooks. Close Kata task `2xx4` with a substantive message and the resulting commit SHA only after all required evidence is present.
