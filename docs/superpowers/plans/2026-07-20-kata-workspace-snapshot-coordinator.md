# Kata Workspace Snapshot Coordinator Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Replace browser-owned Kata refresh authority with a minimal forward-only Middleman Kata frontend service backed by Kata's generated Go client and a bounded in-memory TTL cache.

**Architecture:** Middleman constructs one typed Kata client per resolved daemon, loads immutable authority snapshots through generated API methods, and caches accepted results for five seconds with `ttlcache` plus singleflight. A two-endpoint frontend service exposes atomic snapshots and invalidation-only SSE; it does not mirror Kata's full API. The frontend consumes one snapshot object and applies owner, label, query, sort, hierarchy, selection, route, and retry logic as pure state transitions.

**Tech Stack:** Go 1.26, `go.kenn.io/kata/pkg/client` v0.11.1, `github.com/jellydator/ttlcache/v3`, `golang.org/x/sync/singleflight`, Huma, Svelte 5, TypeScript, Vite+, Vitest, Playwright.

## Global Constraints

- Forward-only migration: delete replaced frontend read/refresh paths; do not add adapters, legacy fallbacks, or dual reads.
- Middleman stores Kata snapshots only in bounded process memory; no database or filesystem persistence.
- Cache TTL is five seconds and presentation filters are excluded from cache keys.
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

Run: `go test ./internal/server -run TestNewKataAPIClient -count=1`

Expected: compile failure for undefined `newKataAPIClient`.

- [ ] **Step 3: Add the released Kata module and minimal factory**

Use `go get go.kenn.io/kata@v0.11.1`. Define the interface from generated
`WithResponse` methods and construct the client with:

```go
kataclient.NewForTarget(ctx, daemon.URL, kataclient.TargetAuth{
    Token: kataDaemonForwardToken(daemon),
    AllowInsecure: daemon.AllowInsecure,
})
```

- [ ] **Step 4: Run the focused test and verify GREEN**

Run: `go test ./internal/server -run TestNewKataAPIClient -count=1`

Expected: PASS.

---

### Task 2: Immutable TTL snapshot cache

**Files:**
- Create: `internal/server/kata_snapshot_cache.go`
- Test: `internal/server/kata_snapshot_cache_test.go`

**Interfaces:**
- Produces: `kataSnapshotKey`, `kataWorkspaceSnapshot`, and `kataSnapshotCache`.
- Produces: `get`, `set`, and `invalidateDaemon` operations.

- [ ] **Step 1: Write failing cache-contract tests**

Cover exact-key reuse and daemon-wide invalidation using an injected clock or
short test TTL. Do not test ttlcache serialization or cleanup internals.

- [ ] **Step 2: Verify RED**

Run: `go test ./internal/server -run TestKataSnapshotCache -count=1`

Expected: compile failure for missing cache types.

- [ ] **Step 3: Implement the cache**

Wrap `ttlcache.Cache[kataSnapshotKey, kataWorkspaceSnapshot]`, use a five-second
default TTL, and keep an index of keys by daemon solely for invalidation.

- [ ] **Step 4: Verify GREEN**

Run: `go test ./internal/server -run TestKataSnapshotCache -count=1`

Expected: PASS.

---

### Task 3: Generated-client snapshot loader

**Files:**
- Create: `internal/server/kata_snapshot_loader.go`
- Test: `internal/server/kata_snapshot_loader_test.go`

**Interfaces:**
- Produces: `func (l *kataSnapshotLoader) Load(ctx context.Context, request kataSnapshotRequest) (kataWorkspaceSnapshot, error)`.
- Consumes: `kataAPIClient` and cache types from Tasks 1–2.

- [ ] **Step 1: Write failing loader tests**

Use a small fake `kataAPIClient` interface implementation. Cover:

- global Ready membership;
- project UID resolution followed by project Ready;
- Open/Closed/All list loading;
- selected detail loaded only for a member UID;
- generated non-2xx responses mapped to Middleman upstream errors.

- [ ] **Step 2: Verify RED**

Run: `go test ./internal/server -run TestKataSnapshotLoader -count=1`

Expected: compile failure for the missing loader.

- [ ] **Step 3: Implement minimal typed loading**

Call generated `ReadyIssuesGlobalWithResponse`, `ReadyIssuesWithResponse`,
`ListAllIssuesWithResponse`, `ListProjectsWithResponse`, and
`ShowIssueByUIDWithResponse`. Map generated DTOs into one stable Middleman task
summary structure and build `member_issue_uids` before presentation filtering.

- [ ] **Step 4: Verify GREEN**

Run: `go test ./internal/server -run TestKataSnapshotLoader -count=1`

Expected: PASS.

---

### Task 4: Coordinator, singleflight, and snapshot endpoint

**Files:**
- Create: `internal/server/kata_snapshot.go`
- Create: `internal/server/kata_snapshot_test.go`
- Modify: `internal/server/kata_workspace.go`
- Modify: `internal/server/server.go`

**Interfaces:**
- Produces: `GET /api/v1/kata/tasks/snapshot`.
- Produces: `KataWorkspaceSnapshotResponse` in generated Middleman OpenAPI.
- Consumes: loader and cache from Tasks 2–3.

- [ ] **Step 1: Write failing coordinator and HTTP tests**

Cover two concurrent identical requests producing one loader call, a subsequent
request hitting the cache, a different daemon missing the cache, and malformed
status/project inputs returning validation problems.

- [ ] **Step 2: Verify RED**

Run: `go test ./internal/server -run 'TestKataSnapshotCoordinator|TestKataSnapshotEndpoint' -count=1`

Expected: missing coordinator/route failures.

- [ ] **Step 3: Implement coordinator and register the route**

Use `singleflight.Group` keyed by the canonical snapshot key. Recheck the cache
inside the singleflight callback, load once, assign a monotonically increasing
generation, cache the accepted value, and return it through Huma.

- [ ] **Step 4: Verify GREEN**

Run: `go test ./internal/server -run 'TestKataSnapshotCoordinator|TestKataSnapshotEndpoint' -count=1`

Expected: PASS.

---

### Task 5: Invalidation-only frontend event stream

**Files:**
- Create: `internal/server/kata_frontend_events.go`
- Test: `internal/server/kata_frontend_events_test.go`
- Modify: `internal/server/kata_workspace.go`

**Interfaces:**
- Produces: `GET /api/v1/kata/tasks/events` SSE.
- Consumes: generated `StreamEventsRaw` and coordinator daemon invalidation.
- Produces: frames containing only daemon ID, generation, and event cursor.

- [ ] **Step 1: Write failing stream tests**

Use a fake generated-client stream body. Assert that multiple raw Kata events
produce one cache invalidation and one frontend invalidation frame, and that raw
Kata payload data is not forwarded.

- [ ] **Step 2: Verify RED**

Run: `go test ./internal/server -run TestKataFrontendEventStream -count=1`

Expected: missing stream service/route failures.

- [ ] **Step 3: Implement minimal invalidation streaming**

Consume `StreamEventsRaw`, batch/coalesce frames, invalidate the daemon cache,
and broadcast a small invalidation DTO to connected frontend clients. Do not
persist events or task state.

- [ ] **Step 4: Verify GREEN**

Run: `go test ./internal/server -run TestKataFrontendEventStream -count=1`

Expected: PASS.

---

### Task 6: Frontend snapshot API and atomic store state

**Files:**
- Create: `frontend/src/lib/api/kata/snapshot.ts`
- Create: `frontend/src/lib/api/kata/snapshot.test.ts`
- Modify: `frontend/src/lib/api/kata/taskTypes.ts`
- Modify: `frontend/src/lib/stores/kata-workspace.svelte.ts`
- Modify: `frontend/src/lib/stores/kata-workspace.svelte.test.ts`

**Interfaces:**
- Produces: `KataWorkspaceSnapshot` and `KataWorkspaceState`.
- Produces: `fetchKataWorkspaceSnapshot(intent, options)`.
- Deletes: independent mutable Ready membership acceptance/rollback paths.

- [ ] **Step 1: Write failing API/store tests**

Cover one response atomically installing filters, membership, rows, detail, and
generation; a late lower generation being ignored; a failed intent entering
`degraded`; and a successful retry replacing the degraded state.

- [ ] **Step 2: Verify RED**

Run: `cd frontend && ../node_modules/vite-plus/bin/vp test run --project unit src/lib/api/kata/snapshot.test.ts src/lib/stores/kata-workspace.svelte.test.ts`

Expected: missing snapshot state and API failures.

- [ ] **Step 3: Implement snapshot client and state transition**

Read Middleman's generated API route, model one discriminated workspace state,
and replace the store's separate `readyIssueUIDs` lifecycle with membership from
the accepted snapshot.

- [ ] **Step 4: Verify GREEN and run Svelte autofixer**

Run the focused tests above, then:

`vp exec -- svelte-mcp svelte-autofixer frontend/src/lib/stores/kata-workspace.svelte.ts`

Expected: focused tests PASS and no Svelte errors.

---

### Task 7: Forward-only workspace and list migration

**Files:**
- Modify: `frontend/src/lib/features/kata/KataWorkspace.svelte`
- Modify: `frontend/src/lib/features/kata/KataWorkspace.test.ts`
- Modify: `frontend/src/lib/components/kata/KataIssueList.svelte`
- Modify: `frontend/src/lib/components/kata/KataIssueList.test.ts`
- Modify: `frontend/src/lib/features/kata/KataWorkspaceEventStream.test.ts`

**Interfaces:**
- Consumes: accepted snapshot state from Task 5.
- Deletes: `readyRefreshRetry`, component-local authoritative child reads, and event payload state patching.
- Produces: pure presentation projection and intent-derived Retry.

- [ ] **Step 1: Write failing component tests**

Cover projection-only owner/query changes without a network refresh, hidden
member selection retention, routed non-member rejection before detail loading,
and ordinary navigation clearing degraded Retry through accepted state.

- [ ] **Step 2: Verify RED**

Run the focused workspace/list/event unit tests and confirm failures exercise
the old direct-read and special Ready paths.

- [ ] **Step 3: Replace the old paths**

Make `KataIssueList` a pure renderer, route every authority refresh through the
snapshot intent, derive Retry from `degraded`, and treat SSE messages only as
snapshot invalidation triggers. Delete replaced code in the same edit.

- [ ] **Step 4: Verify GREEN and Svelte analysis**

Run the focused tests and the Svelte autofixer on both modified components.

---

### Task 8: Full-stack verification and PR refinement

**Files:**
- Modify: `frontend/tests/e2e-full/kata.spec.ts`
- Update: `docs/superpowers/specs/2026-06-08-kata-docs-msgvault-modes-design.md`

**Interfaces:**
- Verifies the complete Middleman-to-generated-client-to-Kata-to-browser flow.

- [ ] **Step 1: Write failing full-stack regressions**

Add real proxy/server cases for global/project Ready, hidden ready child restore,
invalid routed selection, event invalidation, and recovery through ordinary
filter/sidebar navigation. Synchronize on owned HTTP requests or visible state,
not arbitrary delays.

- [ ] **Step 2: Verify RED, then implement any missing integration**

Run only the new Chromium cases until each fails for the intended missing
behavior, then make the minimal integration changes.

- [ ] **Step 3: Run repository verification**

Run focused Go tests, frontend unit/browser checks, Svelte checks, Chromium and
Firefox Kata e2e lanes, `git diff --check`, and the repository's standard
verification target.

- [ ] **Step 4: Commit, push, and refresh all refine-pr evidence surfaces**

Use the required context-sync and commit skills for every commit. Push the exact
head, then re-check GitHub CI, unresolved threads, same-head roborev-ci, and
same-head local roborev independently.
