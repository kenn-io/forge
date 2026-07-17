# Archive and Live Sync Consolidation Implementation Plan

> **Status: Superseded.** This plan's architectural approach is replaced by `docs/superpowers/specs/2026-07-16-archive-single-ingestion-correction-design.md` and its implementation plan. Do not execute the remaining tasks in this document.

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make regular sync and archive sync share provider paging, revision-aware dataset publication, and provider-host admission while preserving live work priority and ramping archive use of expiring hourly surplus.

**Architecture:** Keep archive state responsible only for historical selection, durable cursors, retries, coverage, and reports. Move request admission into a provider-host coordinator used at every archive page boundary; expose canonical page readers that whole-dataset regular-sync helpers collect; and route both live and archive child datasets through the same transactional write helpers. A complete live observation conditionally marks matching archive datasets complete in the same transaction without depending on archive state.

**Tech Stack:** Go 1.26, SQLite via `modernc.org/sqlite`, provider SDKs, `testify`.

## Global Constraints

- Repository identity remains `(platform, platform_host, owner, name)`.
- Live interactive, open, watched, notification, and current-index work outranks all archive work.
- Live sync never waits for archive claims, cursors, staged pages, retries, or completeness state.
- Archive provider work is page-bounded and may delay live work by at most one already-started request.
- Archive work never spends the hard live floor or the provider-reported rate reserve.
- Unknown or implausible reset timestamps use the conservative `f = 0` archive release policy.
- Tests use `go test ... -shuffle=on`, testify, and no `-v` or `-count=1`.
- No compatibility shim or duplicate normal/archive provider implementation may be introduced.

---

### Task 1: Shared Provider-Host Admission and Reset Ramp

**Files:**
- Create: `internal/github/provider_work.go`
- Create: `internal/github/provider_work_test.go`
- Modify: `internal/github/sync.go`
- Modify: `internal/archive/scheduler.go`
- Modify: `internal/archive/scheduler_test.go`
- Modify: `internal/archive/service.go`
- Modify: `internal/archive/service_test.go`

**Interfaces:**
- Produces: `ProviderWorkCoordinator`, `ProviderWorkRequest`, `ProviderWorkAdmission`, and `ProviderOperationCosts` in `internal/github`.
- `ProviderWorkRequest` contains provider-host key, priority, declared request cost, current time, budget, rate tracker, and operation costs.
- `ProviderWorkCoordinator.Admit(context.Context, ProviderWorkRequest) (ProviderWorkAdmission, error)` returns an admitted context and release function or a retry time/detail.
- Archive `AdmissionController.Admit` remains the package-neutral boundary but delegates to the coordinator.

- [ ] Write failing table-driven tests covering priority order, hard-floor calculation, `f²` release at start/middle/end, unknown reset, budget smaller than the floor, provider reserve, and reset clearing archive spend.
- [ ] Run `go test ./internal/github -run 'TestProviderWorkCoordinator|TestArchiveAdmission' -shuffle=on` and confirm failures are due to missing coordinator behavior.
- [ ] Implement the coordinator with one host condition/mutex, per-priority active counts, page-scoped leases, live-first admission, and archive cumulative spend tracking.
- [ ] Replace `Syncer.providerWork`, `beginProviderWork`, `higherPriorityProviderWorkActive`, and archive-only host locks with the coordinator.
- [ ] Ensure every archive request reacquires admission and no database work holds a provider lease.
- [ ] Run `go test ./internal/github ./internal/archive -run 'TestProviderWorkCoordinator|TestArchiveScheduler|TestArchiveAdmission|TestArchiveWorkPriorities' -shuffle=on`.
- [ ] Commit with `refactor: centralize provider work admission`.

### Task 2: Canonical Page Readers and Whole-Dataset Collectors

**Files:**
- Modify: `internal/platform/client.go`
- Create: `internal/platform/page_collect.go`
- Create: `internal/platform/page_collect_test.go`
- Modify: `internal/github/client.go`
- Modify: `internal/github/archive_client.go`
- Modify: `internal/github/client_test.go`
- Modify: `internal/github/archive_client_test.go`
- Modify: `internal/platform/gitlab/client.go`
- Modify: `internal/platform/gitlab/archive.go`
- Modify: `internal/platform/gitlab/client_test.go`
- Modify: `internal/platform/gitlab/archive_test.go`
- Modify: `internal/platform/gitealike/provider.go`
- Modify: `internal/platform/gitealike/archive.go`
- Modify: `internal/platform/gitealike/archive_test.go`

**Interfaces:**
- Produces: `platform.CollectArchivePages[T](ctx, firstCursor, fetch)` with cursor validation and context cancellation.
- Canonical page operations remain the `ArchiveReader` dataset methods.
- Whole-dataset `IssueReader` and `MergeRequestReader` methods collect canonical pages instead of constructing separate provider requests.

- [ ] Write failing collector tests for multiple pages, progress-only pages, repeated cursors, cancellation, and provider errors.
- [ ] Write provider tests asserting regular whole-dataset methods and archive page methods hit the same request shape and normalize identical rows.
- [ ] Run provider package tests and confirm the new equivalence tests fail against duplicate paths.
- [ ] Implement `CollectArchivePages`.
- [ ] Extract GitHub one-page comment, review, and review-thread methods onto the shared client; make both archive methods and existing whole-list methods use them.
- [ ] Make GitLab and gitealike whole-list event/thread methods collect their archive page methods where dataset semantics match exactly; retain provider-only aggregation for datasets that are not archive-equivalent.
- [ ] Remove superseded endpoint construction and normalization code.
- [ ] Run `go test ./internal/platform/... ./internal/github -run 'TestCollectArchivePages|Archive|Comments|Reviews|ReviewThreads|Events' -shuffle=on`.
- [ ] Commit with `refactor: share provider dataset page readers`.

### Task 3: Shared Revision-Aware Child Dataset Publication

**Files:**
- Modify: `internal/db/types.go`
- Modify: `internal/db/queries_snapshot_children.go`
- Modify: `internal/db/queries_archive.go`
- Modify: `internal/db/queries_snapshot_children_test.go`
- Modify: `internal/db/queries_archive_test.go`
- Modify: `internal/github/sync.go`
- Modify: `internal/archive/hydrate.go`

**Interfaces:**
- Produces: `db.IssueDatasetPublication` and `db.MergeRequestDatasetPublication` with parent ID, expected revision, complete dataset flags, and normalized rows.
- Produces: `DB.PublishIssueDatasets` and `DB.PublishMergeRequestDatasets`.
- Archive callers may include an `ArchiveDatasetKey`; live callers omit it and conditionally complete matching archive work by domain identity.

- [ ] Write failing DB tests proving live publication marks a matching current-revision archive dataset complete atomically, ignores absent/paused/stale archive state, and rolls back domain plus archive state together on error.
- [ ] Write failing parity tests proving archive and live publication produce identical domain rows.
- [ ] Run `go test ./internal/db -run 'TestPublish(Issue|MergeRequest)Datasets|TestArchiveDatasetPublish' -shuffle=on`.
- [ ] Extract existing issue/MR child transaction helpers into the two publication methods.
- [ ] Refactor `PublishArchiveIssueComments`, `PublishArchiveMREvents`, and `PublishArchiveReviewThreads` to call the shared transaction helpers rather than duplicate event/thread SQL.
- [ ] Refactor normal sync child commits to use the same publication methods and set completeness only after a full page sequence.
- [ ] Keep stale archive completion conditional: zero affected archive rows is success for live sync.
- [ ] Run `go test ./internal/db ./internal/github ./internal/archive -run 'TestPublish|TestArchive.*Publish|Test.*Snapshot|Test.*Hydration' -shuffle=on`.
- [ ] Commit with `refactor: unify live and archive dataset publication`.

### Task 4: Remove Superseded Scheduling and Hydration Duplication

**Files:**
- Modify: `internal/archive/scheduler.go`
- Modify: `internal/archive/hydrate.go`
- Modify: `internal/github/sync.go`
- Modify: `internal/github/archive_lifecycle_test.go`
- Modify: `internal/archive/service_test.go`
- Modify: `context/platform-sync-invariants.md`
- Modify: `context/github-sync-invariants.md`

**Interfaces:**
- Consumes the shared coordinator, canonical readers, and publication APIs from Tasks 1–3.
- Produces no compatibility adapter: deleted paths have one direct replacement.

- [ ] Write failing integration tests proving live sync proceeds with paused/corrupt archive state, enabling archive does not change the live request sequence within the hard floor, and live work waits for no more than one in-flight archive page.
- [ ] Run the integration tests and confirm the old scheduler/admission behavior fails them.
- [ ] Delete archive host mutex scheduling and duplicate admission bookkeeping.
- [ ] Delete provider request/normalization and DB publication helpers made unreachable by Tasks 2–3.
- [ ] Update context docs to name the canonical coordinator, page reader, and publication APIs.
- [ ] Run `go test ./internal/archive/... ./internal/github ./internal/platform/... ./internal/db -shuffle=on`.
- [ ] Run `make test-short` and `make guardrail-check`.
- [ ] Commit with `refactor: remove parallel archive ingestion paths`.

### Task 5: Final Verification

**Files:**
- No production changes expected.

- [ ] Run `gofmt` on all modified Go files.
- [ ] Run `go test ./internal/archive/... ./internal/github ./internal/platform/... ./internal/db -shuffle=on`.
- [ ] Run `make test-short`.
- [ ] Run `make guardrail-check`.
- [ ] Run `git diff --check` and inspect `git diff --stat origin/main...HEAD` for the expected consolidation.
- [ ] Confirm no normal/archive duplicate endpoint implementation remains with targeted `rg` searches.
- [ ] Commit any verification-only fixes as a new commit; never amend.
