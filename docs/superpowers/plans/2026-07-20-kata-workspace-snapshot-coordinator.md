# Kata Workspace Snapshot Coordinator Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Replace browser-owned Kata refresh authority with a minimal forward-only Middleman Kata frontend service backed by Kata's generated Go client and a bounded in-memory TTL cache.

**Architecture:** Middleman constructs one typed Kata client per resolved daemon, loads immutable authority snapshots through generated API methods, and caches accepted authority data for five seconds with bounded `ttlcache` plus epoch-fenced singleflight. A two-endpoint frontend service exposes atomic snapshots with request-specific detail/history/graph enrichment and invalidation-only SSE; it does not mirror Kata's full API. The frontend owns authority intent separately from presentation state and accepts one snapshot object atomically.

**Tech Stack:** Go 1.26, `go.kenn.io/kata/pkg/client` v0.11.1, `github.com/jellydator/ttlcache/v3`, `golang.org/x/sync/singleflight`, Huma, Svelte 5, TypeScript, Vite+, Vitest, Playwright.

## Global Constraints

- Forward-only migration: delete replaced frontend read/refresh paths; do not add adapters, legacy fallbacks, or dual reads.
- Middleman stores Kata snapshots only in bounded process memory; no database or filesystem persistence.
- Cache TTL is five seconds and presentation filters are excluded from cache keys.
- Cache capacity is 128 entries; expiry and eviction remove daemon-index keys.
- Selection, history, and graph enrichment occur after the authority cache lookup.
- Every response and invalidation frame carries a process-unique server instance ID.
- Full UID is the only task identity.
- All production behavior is introduced with a failing test first.
- Tests cover owned logic and integration seams, not generated-client or ttlcache implementation behavior.

---

### Task 1: Typed daemon client factory

**Files:**
- Modify: `go.mod`
- Modify: `go.sum`
- Create: `internal/server/kata_client.go`
- Test: `internal/server/kata_client_test.go`

**Interfaces:**
- Produces: `type kataAPIClient interface` containing the generated methods used by snapshots.
- Produces: `func newKataAPIClient(ctx context.Context, daemon kata.Daemon) (kataAPIClient, error)`.

- [ ] **Step 1: Write the failing factory test**

Create a fake HTTP Kata server and assert that a client returned by
`newKataAPIClient` calls `InstanceWithResponse` with the resolved bearer token.
The test must fail because `newKataAPIClient` does not exist.

- [ ] **Step 2: Run the focused test and verify RED**

Run: `go test ./internal/server -run TestNewKataAPIClient -shuffle=on`

Expected: compile failure for undefined `newKataAPIClient`.

- [ ] **Step 3: Add the released Kata module and minimal factory**

Use `go get go.kenn.io/kata@v0.11.1`. Define the interface from generated
`WithResponse` methods plus the convenience client's non-buffering
`StreamEventsRaw`. Construct the convenience client with Middleman's existing
resolved HTTP transport and forwarded bearer editor; do not use the generated
buffered stream method for live SSE.

- [ ] **Step 4: Run the focused test and verify GREEN**

Run: `go test ./internal/server -run TestNewKataAPIClient -shuffle=on`

Expected: PASS.

---

### Task 2: Immutable TTL snapshot cache

**Files:**
- Create: `internal/server/kata_snapshot_cache.go`
- Test: `internal/server/kata_snapshot_cache_test.go`

**Interfaces:**
- Produces: `kataSnapshotKey`, `kataAuthoritySnapshot`, and `kataSnapshotCache`.
- Produces: `get`, `set`, `daemonEpoch`, and `invalidateDaemon` operations.

- [ ] **Step 1: Write failing cache-contract tests**

Cover exact-key reuse, fixed capacity, eviction/index cleanup, and daemon-wide
invalidation using an injected clock or short test TTL. Verify the wrapper's
observable contracts, not ttlcache serialization internals.

- [ ] **Step 2: Verify RED**

Run: `go test ./internal/server -run TestKataSnapshotCache -shuffle=on`

Expected: compile failure for missing cache types.

- [ ] **Step 3: Implement the cache**

Wrap `ttlcache.Cache[kataSnapshotKey, kataAuthoritySnapshot]` with a five-second
default TTL, disabled touch-on-hit, and capacity 128. Keep an index of keys by
daemon solely for invalidation, remove index entries on expiry, deletion, and
capacity eviction, and increment one daemon epoch whenever invalidating.

- [ ] **Step 4: Verify GREEN**

Run: `go test ./internal/server -run TestKataSnapshotCache -shuffle=on`

Expected: PASS.

---

### Task 3: Generated-client authority loader

**Files:**
- Create: `internal/server/kata_snapshot_loader.go`
- Test: `internal/server/kata_snapshot_loader_test.go`

**Interfaces:**
- Produces: `func (l *kataSnapshotLoader) LoadAuthority(ctx context.Context, request kataAuthorityRequest) (kataAuthoritySnapshot, error)`.
- Consumes: `kataAPIClient` and cache types from Tasks 1–2.

- [ ] **Step 1: Write failing loader tests**

Use a small fake `kataAPIClient` interface implementation. Cover:

- complete project catalog, including empty projects;
- global Ready membership;
- project UID resolution followed by project Ready;
- Open/Closed/All list loading;
- normalized relationship fields keyed only by full UID;
- generated non-2xx responses mapped to Middleman upstream errors.

- [ ] **Step 2: Verify RED**

Run: `go test ./internal/server -run TestKataSnapshotLoader -shuffle=on`

Expected: compile failure for the missing loader.

- [ ] **Step 3: Implement minimal typed loading**

Call generated `ReadyIssuesGlobalWithResponse`, `ReadyIssuesWithResponse`,
`ListAllIssuesWithResponse`, and `ListProjectsWithResponse`. Map generated DTOs
into stable Middleman project/task structures and build `member_issue_uids`
before presentation filtering. Do not load selection-dependent data here.

- [ ] **Step 4: Verify GREEN**

Run: `go test ./internal/server -run TestKataSnapshotLoader -shuffle=on`

Expected: PASS.

---

### Task 4: Coordinator, enrichment, and snapshot endpoint

**Files:**
- Create: `internal/server/kata_snapshot.go`
- Create: `internal/server/kata_snapshot_test.go`
- Create: `internal/server/kata_snapshot_enrichment.go`
- Create: `internal/server/kata_snapshot_enrichment_test.go`
- Modify: `internal/server/kata_workspace.go`
- Modify: `internal/server/server.go`

**Interfaces:**
- Produces: `GET /api/v1/kata/tasks/snapshot`.
- Produces: `KataWorkspaceSnapshotResponse` in generated Middleman OpenAPI,
  including projects and optional selected detail/history/graph enrichment.
- Consumes: loader and cache from Tasks 2–3.

- [ ] **Step 1: Write failing coordinator and HTTP tests**

Cover two concurrent identical requests producing one loader call, a subsequent
request hitting the cache, a different daemon missing the cache, invalidation
during an in-flight load discarding the stale result, and malformed or unknown
status/project inputs returning stable problems. Cover two selections sharing
one cached authority snapshot while receiving their own membership-gated
detail/history/graph and `workspace_target` enrichment.

- [ ] **Step 2: Verify RED**

Run: `go test ./internal/server -run 'TestKataSnapshotCoordinator|TestKataSnapshotEndpoint' -shuffle=on`

Expected: missing coordinator/route failures.

- [ ] **Step 3: Implement coordinator and register the route**

Use `singleflight.Group` keyed by canonical authority key plus captured daemon
epoch. Recheck the cache inside the callback, load once, verify the epoch before
publishing, and retry when invalidated in flight. Assign a process-unique server
instance ID and monotonically increasing accepted generation. After the cache
lookup, membership-check and load selected detail/history/graph through
generated methods while preserving Middleman's `workspace_target` enrichment.
Return the atomic response through Huma.

- [ ] **Step 4: Verify GREEN**

Run: `go test ./internal/server -run 'TestKataSnapshotCoordinator|TestKataSnapshotEndpoint' -shuffle=on`

Expected: PASS.

- [ ] **Step 5: Regenerate and verify API artifacts**

Run the repository API generation target immediately after registering the
route. Verify checked-in Go/OpenAPI/TypeScript artifacts before frontend work
begins.

---

### Task 5: Mutation-success cache invalidation

**Files:**
- Modify: `internal/server/kata_proxy.go`
- Test: `internal/server/kata_proxy_test.go`
- Modify: `internal/server/server.go`

**Interfaces:**
- Consumes: coordinator `invalidateDaemon` from Tasks 2 and 4.
- Produces: exactly one daemon invalidation after an accepted mutating proxy
  response.

- [ ] **Step 1: Write failing proxy invalidation tests**

Cover POST, PUT, PATCH, and DELETE 2xx responses invalidating the selected
daemon once. Cover GET, transport failure, and non-2xx mutation responses not
invalidating.

- [ ] **Step 2: Verify RED**

Run: `go test ./internal/server -run TestKataProxyMutationInvalidation -shuffle=on`

Expected: accepted proxy mutations do not yet call the coordinator.

- [ ] **Step 3: Implement the shared invalidation seam**

Record the downstream response status around the existing generic Kata proxy.
After a mutating method returns 2xx, invalidate the already-resolved daemon
exactly once. Future direct Middleman mutation handlers use the same coordinator
method after typed success. Do not add a legacy refresh or dual-write path.

- [ ] **Step 4: Verify GREEN**

Run the focused proxy invalidation tests and existing Kata proxy tests.

---

### Task 6: Per-daemon invalidation-only frontend event stream

**Files:**
- Create: `internal/server/kata_frontend_events.go`
- Test: `internal/server/kata_frontend_events_test.go`
- Modify: `internal/server/kata_workspace.go`

**Interfaces:**
- Produces: `GET /api/v1/kata/tasks/events` SSE.
- Consumes: generated `StreamEventsRaw` and coordinator daemon invalidation.
- Produces: frames containing only server instance ID, daemon ID, invalidation
  epoch, and Middleman frontend cursor.

- [ ] **Step 1: Write failing stream tests**

Use fake generated-client polling/stream bodies. Assert that multiple raw Kata
events produce one cache invalidation and one frontend invalidation frame, raw
payload data is not forwarded, reconnect polling closes the stream gap, two
browser subscribers share one daemon supervisor, and stale browser cursors
receive a compact invalidation/reset frame.

- [ ] **Step 2: Verify RED**

Run: `go test ./internal/server -run TestKataFrontendEventStream -shuffle=on`

Expected: missing stream service/route failures.

- [ ] **Step 3: Implement minimal invalidation streaming**

Create one lazily started supervisor and bounded `EventHub` per daemon. Use
`PollEventsWithResponse` for catch-up, `StreamEventsRaw` for the live connection,
and resume from the server-owned upstream cursor. Batch/coalesce raw events,
invalidate the daemon cache, and broadcast a small invalidation DTO through the
per-daemon hub. Snapshot `event_cursor` is the hub cursor, not Kata's upstream
event ID. Tie supervisor and cache cleanup lifetimes to server shutdown. Do not
persist events or task state.

- [ ] **Step 4: Verify GREEN**

Run: `go test ./internal/server -run TestKataFrontendEventStream -shuffle=on`

Expected: PASS.

---

### Task 7: Frontend snapshot API and atomic store state

**Files:**
- Create: `frontend/src/lib/api/kata/snapshot.ts`
- Create: `frontend/src/lib/api/kata/snapshot.test.ts`
- Modify: `frontend/src/lib/api/kata/taskTypes.ts`
- Modify: `frontend/src/lib/stores/kata-workspace.svelte.ts`
- Modify: `frontend/src/lib/stores/kata-workspace.svelte.test.ts`

**Interfaces:**
- Produces: `KataWorkspaceSnapshot` and `KataWorkspaceState`.
- Produces: `fetchKataWorkspaceSnapshot(intent, options)`.
- Produces: a browser-local request-intent sequence independent of server
  generation.
- Deletes: independent mutable Ready membership acceptance/rollback paths.

- [ ] **Step 1: Write failing API/store tests**

Cover one response atomically installing authority key, projects, membership,
rows, enrichment, epoch, and generation; late responses losing local
request-intent ownership; generation comparison scoped to server instance and
canonical key; a Middleman restart accepting a new instance ID; presentation
filter changes preserving accepted authority without a request; a failed intent
entering `degraded` without displaying prior rows as the failed target; and a
successful retry replacing the degraded state.

- [ ] **Step 2: Verify RED**

Run: `cd frontend && ../node_modules/vite-plus/bin/vp test run --project unit src/lib/api/kata/snapshot.test.ts src/lib/stores/kata-workspace.svelte.test.ts`

Expected: missing snapshot state and API failures.

- [ ] **Step 3: Implement snapshot client and state transition**

Read Middleman's generated API route, model one discriminated authority state
plus separate presentation state, and replace the store's separate project,
task-cache, `readyIssueUIDs`, selected-detail, selected-history, and graph
authority lifecycles with data from the accepted snapshot.

- [ ] **Step 4: Verify GREEN and run Svelte autofixer**

Run the focused tests above, then:

`vp exec -- svelte-mcp svelte-autofixer frontend/src/lib/stores/kata-workspace.svelte.ts`

Expected: focused tests PASS and no Svelte errors.

---

### Task 8: Forward-only workspace, list, graph, and event cutover

**Files:**
- Modify: `frontend/src/lib/features/kata/KataWorkspace.svelte`
- Modify: `frontend/src/lib/features/kata/KataWorkspace.test.ts`
- Modify: `frontend/src/lib/components/kata/KataIssueList.svelte`
- Modify: `frontend/src/lib/components/kata/KataIssueList.test.ts`
- Modify: `frontend/src/lib/features/kata/KataReachableGraph.svelte`
- Modify: `frontend/src/lib/features/kata/KataReachableGraphComponent.test.ts`
- Modify: `frontend/src/lib/api/kata/eventStream.ts`
- Modify: `frontend/src/lib/api/kata/taskClient.ts`
- Modify: `frontend/src/lib/components/kata/KataIssueDiscussion.svelte`
- Modify: `frontend/src/lib/messages/kataMessageLinker.ts`
- Modify: `frontend/src/lib/features/kata/KataWorkspaceEventStream.test.ts`

**Interfaces:**
- Consumes: accepted snapshot state from Task 7 and invalidation frames from
  Task 6.
- Deletes: `readyRefreshRetry`, direct project/list/search/detail/history/graph
  reads, component-local authoritative child reads, sparse task merge caches,
  browser cursor catch-up, and raw event payload state patching.
- Produces: pure presentation projection and intent-derived Retry.

- [ ] **Step 1: Write failing component tests**

Cover projection-only owner/query changes without a network refresh, empty
project navigation, hidden member selection retention, routed non-member
rejection before detail/history/graph loading, graph rendering from snapshot
enrichment, one refresh per invalidation, mutation refresh without response
patching, and ordinary navigation clearing degraded Retry through accepted
state.

- [ ] **Step 2: Verify RED**

Run the focused workspace/list/event unit tests and confirm failures exercise
the old direct-read and special Ready paths.

- [ ] **Step 3: Replace the old paths**

Make `KataIssueList` and `KataReachableGraph` pure renderers, route every
authority refresh through snapshot intent, derive Retry from `degraded`, and
treat SSE messages only as snapshot invalidation triggers. Project link lookup
projects over the accepted entity set, and mutation preconditions use accepted
detail/ETag rather than a direct read. Delete replaced code in the same edit.

- [ ] **Step 4: Verify GREEN and Svelte analysis**

Run the focused tests and the Svelte autofixer on both modified components.

---

### Task 9: Full-stack integration and cross-browser verification

**Files:**
- Modify: `frontend/tests/e2e-full/kata.spec.ts`
- Update: `docs/superpowers/specs/2026-06-08-kata-docs-msgvault-modes-design.md`

**Interfaces:**
- Verifies the complete Middleman-to-generated-client-to-Kata-to-browser flow.

- [ ] **Step 1: Write failing full-stack regressions**

Add real proxy/server cases for global/project Ready, empty project navigation,
hidden ready child restore, selected `workspace_target`, history and graph
enrichment, invalid routed selection, mutation invalidation, event invalidation,
Middleman restart generation reset, and recovery through ordinary filter/sidebar
navigation. Synchronize on owned HTTP requests or visible state, not arbitrary
delays.

- [ ] **Step 2: Verify RED, then implement any missing integration**

Run only the new Chromium cases until each fails for the intended missing
behavior, then make the minimal integration changes.

- [ ] **Step 3: Run repository verification**

Run focused Go tests, frontend unit/browser checks, Svelte checks, Chromium and
Firefox Kata e2e lanes, `git diff --check`, and the repository's standard
verification target.

- [ ] **Step 4: Commit verified integration work**

Use the required context-sync and commit skills. Do not combine a failing
integration repair with unrelated PR operations.

---

### Task 10: PR evidence refresh

- [ ] Push the exact verified head when the active user workflow authorizes it.
- [ ] Re-check GitHub CI and unresolved threads without deleting or resolving
  comments.
- [ ] Run the explicitly authorized local `roborev-fix` workflow and inspect
  same-head local reviews separately from roborev-ci.
- [ ] Report remaining findings, skipped compatibility/hardening suggestions,
  and exact verification evidence.
