# Hot PR View Tracking Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Keep the last 10 open PR details the user viewed on a two-minute hot cadence, refresh other recently active PRs every 10 minutes, and serialize watched-MR passes.

**Architecture:** Persist local view recency in a dedicated SQLite table with trigger-backed terminal eviction. Record successful detail reads in the pull API, then union persisted hot identities with explicit watched PRs and warm activity candidates in the GitHub sync scheduler. A pass-level mutex owns the mutable scheduler cadence map.

**Tech Stack:** Go, SQLite migrations and queries, Huma HTTP handlers, testify tests

## Global Constraints

- The hot set contains at most 10 unique open PRs and survives daemon restarts.
- Closed and merged PRs leave the hot table as soon as terminal state is persisted.
- Hot and explicit watched PRs use the configured watched interval, default two minutes.
- Warm active or notification-promoted PRs use a fixed 10-minute interval.
- Existing provider routing, rate limiting, and host cadence behavior remain intact.
- Remove this plan and its design document before the final push.

---

### Task 1: Persist the hot PR MRU

**Files:**
- Create: `internal/db/migrations/000046_hot_merge_requests.up.sql`
- Create: `internal/db/migrations/000046_hot_merge_requests.down.sql`
- Create: `internal/db/queries_hot_merge_requests.go`
- Create: `internal/db/queries_hot_merge_requests_test.go`

**Interfaces:**
- Produces: `(*DB).RecordHotMergeRequestView(context.Context, int64, time.Time) error`
- Produces: `(*DB).ListHotMergeRequestIDs(context.Context, int) ([]int64, error)`

- [ ] **Step 1: Write failing DB behavior tests**

Add tests that seed 12 open PRs, record deterministic view timestamps, revisit one PR, and assert that `ListHotMergeRequestIDs(ctx, 10)` returns 10 unique IDs in MRU order. Add cases proving closed PRs cannot be recorded and `UpdateMRState`/`UpsertMergeRequest` terminal transitions delete existing hot rows.

- [ ] **Step 2: Run the DB tests and confirm the missing migration/query failures**

Run: `go test ./internal/db -run 'Test(Record|List)HotMergeRequest|TestHotMergeRequestTerminalEviction' -count=1`

Expected: FAIL because the migration and DB methods do not exist.

- [ ] **Step 3: Add the migration and minimal DB methods**

Create a table keyed by `merge_request_id`, an MRU index, and an `AFTER UPDATE OF state` trigger whose body deletes on `NEW.state IN ('closed', 'merged')`. Implement `RecordHotMergeRequestView` as one transaction:

```sql
INSERT INTO forge_hot_merge_requests (merge_request_id, viewed_at)
SELECT id, ? FROM forge_merge_requests WHERE id = ? AND state = 'open'
ON CONFLICT(merge_request_id) DO UPDATE SET viewed_at = excluded.viewed_at;

DELETE FROM forge_hot_merge_requests
WHERE merge_request_id NOT IN (
  SELECT merge_request_id FROM forge_hot_merge_requests
  ORDER BY viewed_at DESC, merge_request_id DESC LIMIT 10
);
```

Implement `ListHotMergeRequestIDs` by joining `forge_merge_requests`, requiring `state = 'open'`, ordering by `viewed_at DESC, merge_request_id DESC`, and applying the caller limit.

- [ ] **Step 4: Run focused and full DB tests**

Run: `go test ./internal/db -run 'Test(Record|List)HotMergeRequest|TestHotMergeRequestTerminalEviction' -count=1`

Run: `go test ./internal/db -count=1`

Expected: PASS.

### Task 2: Record successful detail views

**Files:**
- Modify: `internal/server/pullapi/routes.go`
- Modify: `internal/server/api_test.go`

**Interfaces:**
- Consumes: `(*db.DB).RecordHotMergeRequestView(context.Context, int64, time.Time) error`
- Produces: successful `getPull` calls update local hot-view recency after response construction

- [ ] **Step 1: Write a failing pull API test**

Extend pull-detail API coverage to GET an open PR detail, then assert `ListHotMergeRequestIDs(ctx, 10)` contains that PR. Add a terminal PR case proving a successful read does not create a hot row.

- [ ] **Step 2: Run the focused server test and confirm failure**

Run: `go test ./internal/server -run TestAPIGetPullDetailRecordsHotView -count=1`

Expected: FAIL because `getPull` does not record the viewed PR.

- [ ] **Step 3: Record the view after detail response construction**

In `getPull`, after `buildPullDetailResponse` succeeds, call:

```go
if err := s.db.RecordHotMergeRequestView(ctx, mr.ID, s.now().UTC()); err != nil {
    slog.Warn("record hot pull request view", "merge_request_id", mr.ID, "err", err)
}
```

Keep the response successful when this local scheduling hint fails.

- [ ] **Step 4: Run focused and server tests**

Run: `go test ./internal/server -run TestAPIGetPullDetailRecordsHotView -count=1`

Run: `go test ./internal/server/pullapi ./internal/server -run 'TestAPIGetPullDetail' -count=1`

Expected: PASS.

### Task 3: Split hot and warm scheduler cadence

**Files:**
- Modify: `internal/github/sync.go`
- Modify: `internal/github/sync_test.go`
- Modify if needed: `context/github-sync-invariants.md`

**Interfaces:**
- Consumes: `(*db.DB).ListHotMergeRequestIDs(context.Context, int) ([]int64, error)`
- Produces: candidate selection with configured-cadence hot PRs and fixed 10-minute warm PRs

- [ ] **Step 1: Rewrite selection tests for the agreed policy**

Cover these cases in `sync_test.go` with a fixed `now`:

```text
hot viewed, detail fetched 1 minute ago       -> not due on 2-minute interval
hot viewed, detail fetched 2 minutes ago      -> due
warm recent activity, fetched 9 minutes ago   -> not due
warm recent activity, fetched 10 minutes ago  -> due
warm notification hint, fetched 9 minutes ago -> not due
warm notification hint, fetched 10 minutes ago-> due
never-fetched hot or warm                     -> due immediately
```

Also prove a hot PR outside `active_pr_window` remains eligible and terminal rows are absent.

- [ ] **Step 2: Run focused scheduler tests and confirm policy failures**

Run: `go test ./internal/github -run 'TestWatchedMRs|TestActiveMR' -count=1`

Expected: FAIL because hotness is still inferred from 30-minute activity and warm cadence is five minutes.

- [ ] **Step 3: Implement distinct hot and warm candidate paths**

Replace `activeMRHotActivityWindow` with:

```go
const activeMRWarmRefreshInterval = 10 * time.Minute
const maxHotMergeRequests = 10
```

Load the hot ID set once in `watchedMRsForFastSync`. Select hot PRs regardless of activity-window age using the configured watched interval. Select non-hot active/notification candidates within the activity window using `activeMRWarmRefreshInterval`. Preserve explicit `SetWatchedMRs` entries as unconditional configured-cadence candidates and deduplicate all sources with `watchedMRSet`.

- [ ] **Step 4: Run focused and full GitHub tests**

Run: `go test ./internal/github -run 'TestWatchedMRs|TestActiveMR' -count=1`

Run: `go test ./internal/github -count=1`

Expected: PASS.

### Task 4: Serialize watched-MR passes

**Files:**
- Modify: `internal/github/sync.go`
- Modify: `internal/github/sync_test.go`

**Interfaces:**
- Produces: at most one scheduled or immediate `syncWatchedMRs` pass can access provider work and `nextWatchSyncAfter`

- [ ] **Step 1: Write the failing concurrency regression test**

Use a blocking fake provider that increments an atomic in-flight count on `GetPullRequest`. Start one `SyncWatchedMRs`, wait until it reaches the provider, then start a second. Before releasing the first, assert the second has not entered provider work and `maxInFlight == 1`.

- [ ] **Step 2: Run the test and confirm concurrent entry**

Run: `go test ./internal/github -run TestSyncWatchedMRsSerializesConcurrentPasses -count=1`

Expected: FAIL with the second pass entering the blocking provider call.

- [ ] **Step 3: Add pass-level serialization**

Add `watchSyncMu sync.Mutex` to `Syncer` and lock for the full body of `syncWatchedMRs`:

```go
s.watchSyncMu.Lock()
defer s.watchSyncMu.Unlock()
```

This makes `nextWatchSyncAfter` single-owner and prevents duplicate provider work.

- [ ] **Step 4: Run concurrency and race-focused tests**

Run: `go test ./internal/github -run TestSyncWatchedMRsSerializesConcurrentPasses -count=20`

Run: `go test -race ./internal/github -run 'TestSyncWatchedMRsSerializesConcurrentPasses|TestWatchedMRs' -count=1`

Expected: PASS.

### Task 5: Integration verification and PR cleanup

**Files:**
- Modify: PR description for `#815`
- Delete: `docs/superpowers/specs/2026-08-03-hot-pr-view-tracking-design.md`
- Delete: `docs/superpowers/plans/2026-08-03-hot-pr-view-tracking.md`
- Delete: any other `docs/superpowers` file added by this branch

**Interfaces:**
- Produces: final PR diff with implementation, tests, migration, and no branch-added superpowers documents

- [ ] **Step 1: Run proportional verification**

Run: `go test ./internal/db ./internal/github ./internal/server/pullapi -count=1`

Run focused server API tests that cover pull detail and Activity refresh behavior.

Run: `go test ./... -count=1` with repository-safe parallelism if host pressure requires it.

- [ ] **Step 2: Remove temporary documents and verify the diff**

Delete the design and plan files, compare against the PR base, and require that no branch-added path under `docs/superpowers` remains.

- [ ] **Step 3: Run context sync, final review, and commit workflow**

Run `scripts/context-sync --check`, inspect the intended diff, run the mandatory commit and private-data scrub workflows, and create rationale-focused commits without bypassing hooks.

- [ ] **Step 4: Update and push PR #815**

Update the description to define hot as persisted recent views, warm as 10-minute recently active PRs, and mention terminal eviction plus pass serialization. Push the branch and confirm the PR head and checks reflect the new commit.
