# Gitealike Staged Review Hydration Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make Gitea and Forgejo archive hydration admissible with production rate state and resumable when inline review hydration exceeds one archive wire-attempt allowance.

**Architecture:** Provider transports ingest real rate headers and archive admission uses local hourly surplus only for headerless Gitealike hosts. Gitea and Forgejo expose bounded review discovery and per-review hydration to the provider-neutral syncer, which persists partial work in a revision-keyed SQLite stage and atomically swaps the completed dataset into the existing live tables.

**Tech Stack:** Go, SQLite migrations and transactions, Gitea SDK, Forgejo SDK, testify, testcontainers.

## Global Constraints

- Preserve at least the existing provider-specific live floor; at a configured hourly budget of 50, Gitealike archive work may use at most 39 attempts.
- Accept at most ten discovery pages and 100 review identities per merge request.
- Hydrate at most eight reviews in one canonical detail pass.
- Use an archive cost and hard allowance of 38 attempts for Gitea and Forgejo merge requests.
- Keep the previous complete live review dataset visible until a revision-fenced final transaction commits.
- Invalidate staged work when provider `updated_at` or head SHA changes.
- Do not synthesize provider quota or reset facts.
- Do not add an archive-only reader, normalizer, writer, compatibility path, or legacy sync engine.
- Add exactly one migration to this PR and leave shipped migrations unchanged.
- Run Go tests with `-shuffle=on`; do not use `-v`, `-count=1`, or `--no-verify`.

---

### Task 1: Gitealike rate authority and archive admission

**Files:**
- Create: `internal/platform/gitealike/rate.go`
- Create: `internal/platform/gitealike/rate_test.go`
- Modify: `internal/platform/gitea/client.go`
- Modify: `internal/platform/gitea/client_test.go`
- Modify: `internal/platform/forgejo/client.go`
- Modify: `internal/platform/forgejo/client_test.go`
- Modify: `internal/github/budget.go`
- Modify: `internal/github/budget_test.go`
- Modify: `internal/github/sync.go`
- Modify: `internal/github/archive_lifecycle_test.go`

**Interfaces:**
- Produces: `gitealike.RateFromHeaders(http.Header) (ratelimit.Rate, bool)`; it succeeds only for a positive limit, non-negative remaining value, and positive Unix reset timestamp.
- Produces: `(*SyncBudget).LocalArchiveSpendAvailable(liveFloor int) int`, returning `max(limit-liveFloor-spent, 0)` without mutating provider quota state.
- Consumes: `RateTracker.RecordRequest`, `RateTracker.UpdateFromRate`, and the existing clock-hour rollover callback.

- [ ] **Step 1: Write failing transport tests**

  Add one Gitea and one Forgejo client test whose real `httptest.Server` returns `X-RateLimit-Limit: 5000`, `X-RateLimit-Remaining: 4999`, and a future Unix `X-RateLimit-Reset`. Assert the persisted tracker row records one request and the exact provider tuple. Add a shared parser test proving an incomplete tuple is rejected.

- [ ] **Step 2: Verify the transport tests fail**

  Run: `go test ./internal/platform/gitealike ./internal/platform/gitea ./internal/platform/forgejo -run 'TestRateFromHeaders|TestClientRecordsRateLimitHeaders' -shuffle=on`

  Expected: FAIL because Gitealike transports only call `RecordRequest` and `RateFromHeaders` does not exist.

- [ ] **Step 3: Implement real header ingestion**

  Implement `RateFromHeaders` by parsing the standard and `X-` names. In each Gitealike rate-tracking transport, call `RecordRequest`, then call `UpdateFromRate` only when the complete tuple parses. Do not construct a reset time when the header is absent or malformed.

- [ ] **Step 4: Verify the transport tests pass**

  Run: `go test ./internal/platform/gitealike ./internal/platform/gitea ./internal/platform/forgejo -run 'TestRateFromHeaders|TestClientRecordsRateLimitHeaders' -shuffle=on`

  Expected: PASS.

- [ ] **Step 5: Write failing headerless admission tests**

  Add a budget test proving a limit-50 budget with live floor 11 exposes 39 local attempts and never crosses the floor after spend. Add an archive lifecycle test where a Gitea tracker has counted requests but no reset, cost 38 is admitted, the context permits exactly 39 attempts, and GitHub under the same missing-reset conditions remains deferred.

- [ ] **Step 6: Verify headerless admission tests fail**

  Run: `go test ./internal/github -run 'TestLocalArchiveSpendAvailable|TestGitealikeArchiveAdmissionUsesHeaderlessLocalBudget' -shuffle=on`

  Expected: FAIL because `ArchiveSpendAvailable` returns zero without provider reset state.

- [ ] **Step 7: Implement scoped local fallback**

  Add `LocalArchiveSpendAvailable`. In `Syncer.Admit`, use provider-observed `ArchiveSpendAvailable` when a reset exists; only for `platform.KindGitea` and `platform.KindForgejo` with no reset, use `LocalArchiveSpendAvailable`. Keep the tracker reserve gate unchanged when real quota state is known.

- [ ] **Step 8: Verify rate authority and admission**

  Run: `go test ./internal/github ./internal/platform/gitealike ./internal/platform/gitea ./internal/platform/forgejo -run 'TestLocalArchiveSpendAvailable|TestGitealikeArchiveAdmissionUsesHeaderlessLocalBudget|TestRateFromHeaders|TestClientRecordsRateLimitHeaders' -shuffle=on`

  Expected: PASS.

- [ ] **Step 9: Commit**

  Commit the rate authority and scoped fallback with subject `fix: admit headerless gitealike archives` after the required context-sync and commit workflows.

### Task 2: Provider-aware pass cost and paged review interface

**Files:**
- Modify: `internal/platform/client.go`
- Modify: `internal/platform/gitealike/review_hydration.go`
- Modify: `internal/platform/gitea/diff_review.go`
- Modify: `internal/platform/gitea/diff_review_test.go`
- Modify: `internal/platform/forgejo/diff_review.go`
- Modify: `internal/platform/forgejo/diff_review_test.go`
- Modify: `internal/github/sync.go`
- Modify: `internal/github/sync_test.go`

**Interfaces:**
- Produces: `platform.MergeRequestReviewHydrator` with `ListMergeRequestReviewIDs(context.Context, RepoRef, int) ([]string, error)` and `ListMergeRequestReviewThreadsForReview(context.Context, RepoRef, int, string) ([]MergeRequestReviewThread, error)`.
- Produces: `gitealike.MaxReviewHydrationPages = 10`, `MaxReviewHydrationReviews = 100`, and `MaxReviewHydrationReviewsPerPass = 8`.
- Produces: `ArchiveItemSyncCost(Gitea|Forgejo, merge_request) == 38`.

- [ ] **Step 1: Write failing provider paging tests**

  For both Gitea and Forgejo, add tests where discovery follows two pages and returns ordered string IDs without reading comments, and per-review hydration reads only the requested review's comments. Mutating the production reader to fan out during discovery or to accept an eleventh page must fail a test.

- [ ] **Step 2: Verify provider paging tests fail**

  Run: `go test ./internal/platform/gitea ./internal/platform/forgejo -run 'TestListMergeRequestReviewIDs|TestListMergeRequestReviewThreadsForReview' -shuffle=on`

  Expected: FAIL because the paged hydration interface is absent.

- [ ] **Step 3: Extract bounded provider operations**

  Keep provider SDK calls and normalization in their concrete packages. Discovery uses `platform.CollectPages` with a ten-page limit and returns no more than 100 string IDs. Per-review hydration parses one ID, performs one SDK comment read, applies the existing comment limit and normalizer, and maps provider errors through each concrete client boundary.

- [ ] **Step 4: Verify provider paging tests pass**

  Run: `go test ./internal/platform/gitea ./internal/platform/forgejo -run 'TestListMergeRequestReviewIDs|TestListMergeRequestReviewThreadsForReview|TestListMergeRequestReviewThreads' -shuffle=on`

  Expected: PASS.

- [ ] **Step 5: Extend the cost regression test first**

  Add literal expectations of 38 for Gitea and Forgejo merge requests while retaining the existing GitHub, GitLab, and issue expectations.

- [ ] **Step 6: Verify the cost test fails, then implement and pass**

  Run before implementation: `go test ./internal/github -run TestArchiveItemSyncCostIncludesProviderConfirmationAndAuthRetry -shuffle=on`

  Expected before implementation: FAIL with 22 instead of 38. Update `ArchiveItemSyncCost` only for the two Gitealike merge-request cases, rerun the same command, and expect PASS.

- [ ] **Step 7: Commit**

  Commit the bounded provider interface and provider-aware cost with subject `refactor: expose bounded gitealike review pages`.

### Task 3: Provider-neutral durable review stage

**Files:**
- Create: `internal/db/migrations/000040_mr_review_hydration_stage.up.sql`
- Create: `internal/db/migrations/000040_mr_review_hydration_stage.down.sql`
- Create: `internal/db/queries_review_hydration.go`
- Create: `internal/db/queries_review_hydration_test.go`
- Modify: `internal/db/types.go`
- Modify: `internal/db/db_test.go`
- Modify: `internal/db/queries_snapshot_children.go`

**Interfaces:**
- Produces: `db.MRReviewHydrationStage` containing merge-request ID, provider updated time, head SHA, generation, ordered review IDs, next review index, and staged thread records that retain the direct URL needed for final event creation.
- Produces: `(*DB).ReplaceMRReviewHydrationStage(ctx, key, reviewIDs) (MRReviewHydrationStage, error)`.
- Produces: `(*DB).GetMRReviewHydrationStage(ctx, mergeRequestID) (*MRReviewHydrationStage, error)`.
- Produces: `(*DB).AppendMRReviewHydrationStage(ctx, stage, threads, nextReviewIndex) (bool, error)` using generation/key compare-and-swap semantics.
- Produces: `(*DB).CommitMRReviewHydrationStage(ctx, stage, expectedRevision) (bool, error)` that atomically replaces live review threads and review-comment events, then deletes the stage.

- [ ] **Step 1: Write failing migration and stage tests**

  Add DB behavior tests proving: a stage round-trips ordered review IDs; appending a batch advances only the matching generation; replacing after provider time or head changes removes old staged threads and increments generation; live rows remain unchanged while incomplete; final commit swaps live rows and deletes the stage; a stale parent revision leaves live rows and stage unchanged.

- [ ] **Step 2: Verify DB tests fail**

  Run: `go test ./internal/db -run 'TestMRReviewHydrationStage' -shuffle=on`

  Expected: FAIL because migration 40 and stage queries are absent.

- [ ] **Step 3: Add the single PR migration**

  Create `middleman_mr_review_hydration_stages` keyed by `merge_request_id`, with `provider_updated_at`, `head_sha`, `generation`, `review_ids_json`, `next_review_index`, and timestamps. Create `middleman_mr_review_hydration_threads` with the live thread columns plus `direct_url`, `merge_request_id`, and `generation`, cascading from the merge request and unique on merge request, generation, and provider thread ID. The down migration drops staged threads before stages.

- [ ] **Step 4: Implement stage queries and atomic final swap**

  Reuse the existing review-thread normalization and transactional helpers. `CommitMRReviewHydrationStage` verifies parent revision and exact stage key/generation, derives review-comment events from the staged threads, calls the existing missing/live upsert helpers in the same transaction, and deletes only the matching generation after success.

- [ ] **Step 5: Verify migration and DB behavior**

  Run: `go test ./internal/db -run 'TestMRReviewHydrationStage|TestMigrations' -shuffle=on`

  Expected: PASS.

- [ ] **Step 6: Commit**

  Commit the migration and provider-neutral stage storage with subject `feat: stage merge request review hydration`.

### Task 4: Canonical staged hydration coordinator

**Files:**
- Create: `internal/github/review_hydration.go`
- Create: `internal/github/review_hydration_test.go`
- Modify: `internal/github/sync.go`
- Modify: `internal/github/sync_test.go`

**Interfaces:**
- Produces: `(*Syncer).syncStagedMRReviewThreads(ctx, hydrator, repo, mr, expectedRevision) (calls int, complete bool, err error)`.
- Changes: `syncProviderMRReviewThreads` returns completion separately from errors; providers without `MergeRequestReviewHydrator` retain the existing complete-reader transaction.
- Changes: `fetchProviderMRDetail` leaves `detail_fetched_at` unset when review hydration is incomplete without converting normal continuation into a failed archive lookup.

- [ ] **Step 1: Write the failing canonical drain test**

  Add a real SQLite sync test with 17 discovered reviews and one normalized thread per review. Call canonical provider detail sync four times: discovery, reviews 1-8, reviews 9-16, and review 17/final swap. Assert each incomplete pass leaves the old live thread visible and `detail_fetched_at` nil; the final pass atomically exposes 17 new threads and sets `detail_fetched_at`.

- [ ] **Step 2: Verify the drain test fails**

  Run: `go test ./internal/github -run TestGitealikeReviewHydrationCompletesAcrossCanonicalDetailPasses -shuffle=on`

  Expected: FAIL because the current complete reader fans out in one pass.

- [ ] **Step 3: Implement discovery and continuation**

  In `syncProviderMRReviewThreads`, prefer `MergeRequestReviewHydrator`. If no matching stage exists, discover IDs and persist a new generation, then return incomplete. Otherwise read at most eight reviews starting at `next_review_index`, append the normalized threads, and return incomplete until the final review has been persisted.

- [ ] **Step 4: Implement revision-fenced final swap**

  When the stage reaches the end, call `CommitMRReviewHydrationStage` with the current parent revision. A false result returns `errParentSnapshotAdvanced`; provider, authentication, cancellation, page-limit, and allowance errors return without changing live rows or deleting the stage.

- [ ] **Step 5: Verify drain and failure behavior**

  Run: `go test ./internal/github -run 'TestGitealikeReviewHydrationCompletesAcrossCanonicalDetailPasses|TestGitealikeReviewHydrationPreservesLiveDatasetOnFailure|TestGitealikeReviewHydrationInvalidatesChangedSnapshot' -shuffle=on`

  Expected: PASS.

- [ ] **Step 6: Run cross-provider regressions**

  Run: `go test ./internal/github ./internal/db ./internal/platform/gitealike ./internal/platform/gitea ./internal/platform/forgejo ./internal/archive -shuffle=on`

  Expected: PASS.

- [ ] **Step 7: Commit**

  Commit the canonical coordinator with subject `fix: resume gitealike review hydration`.

### Task 5: Production-path container proof

**Files:**
- Modify: `internal/server/gitealike_container_e2e_test.go`

**Interfaces:**
- Consumes: real transport request accounting, header parsing when providers emit it, and headerless local Gitealike admission.
- Removes: direct `tracker.UpdateFromRate` seeding from the shared container assertion.

- [ ] **Step 1: Remove synthetic rate state**

  Move shared fixture DB/tracker construction before client construction, pass the tracker through each provider's `WithRateTracker` option, and delete the manual `UpdateFromRate` call and its now-unused `Rate` construction. This makes the fixture exercise the same transport accounting and admission state as production.

- [ ] **Step 2: Run Forgejo and Gitea container packages concurrently**

  Run the two existing container commands as separate concurrent processes with their existing environment flags and free ports. Each command must include `-shuffle=on`.

  Expected: Forgejo archive hydration passes without seeded tracker state. Gitea reaches the same production admission and staged hydration path; report the known 1.24.6 timeline-label SDK decode defect separately if it remains.

- [ ] **Step 3: Run standard verification**

  Run: `go test ./internal/server ./internal/github ./internal/db ./internal/platform/... ./internal/archive -shuffle=on`

  Expected: PASS for non-container tests.

- [ ] **Step 4: Commit and push**

  Run context sync, the mandatory commit workflow, commit with subject `test: exercise production gitealike archive admission`, and push the current PR branch without bypassing hooks.
