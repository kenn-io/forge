# Archive Single-Ingestion Correction Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

> **Required reading for every task:** `docs/superpowers/specs/2026-07-16-archive-single-ingestion-correction-design.md` (the spec this plan implements). Section references below ("design: …") point there. Tasks that touch `internal/db/migrations/` MUST load the `kenn:db-migration-discipline` skill first; migration 39 is PR-local and is amended in place, never given a sibling migration.

**Goal:** Replace the parallel archive provider/persistence stacks with canonical provider page reads and incremental domain ingestion, deleting `ArchiveReader`, archive-prefixed provider methods, payload staging, and archive-only publication, while preserving every user-visible archive capability.

**Architecture:** One page-oriented provider read surface per dataset shared by live and archive callers; two canonical revision-aware ingestion transactions (parent inventory page, child dataset page); compact durable cursor/progress rows instead of staged JSON payloads; live-first admission and quadratic archive budget unchanged.

**Tech Stack:** Go 1.26, SQLite via `modernc.org/sqlite`, huma, testify.

## Global Constraints

- Repository identity is always `(platform, platform_host, owner, name)`.
- All live work outranks archive work; archive admission is per provider page; database work never holds provider admission.
- No compatibility adapter, dual writer, alias, or repair gate may be committed (CLAUDE.md + design non-goals). Transitional precision: at every commit, each dataset has exactly one request/normalization implementation and each domain row has exactly one writer. Existing archive-prefixed methods may remain as one-line delegates over the canonical implementation between their provider's slice and Task 11 (they are the pre-existing surface, not new scaffolding, and carry no duplicated logic); the staging schema may outlive its last writer (Task 9) until Task 10 removes it, but no commit leaves two live writers for the same rows.
- Structural gates (design: Structural Acceptance Gates): after Task 11 the strings `ArchiveReader`, `validatingArchiveReader`, `ListArchive`, `GetArchive`, `PublishArchive`, `middleman_archive_dataset_pages`, `ArchiveDatasetPage`, `ArchiveDatasetStage` have no production matches.
- Quantitative gates measured against `3e914c24` excluding the design doc and this plan: delete ≥2,000 net handwritten production lines and ≥4,000 net total lines; end ≤ ~+6,500 production / ~+18,000 total vs `origin/main`.
- Tests: `go test ./... -shuffle=on`, testify (`require` for preconditions, `assert` otherwise), no `-v`, no `-count=1`, table-driven, `t.TempDir()`, `openTestDB(t)` for db tests.
- Datetimes are UTC everywhere outside the Svelte presentation layer.
- Each task ends with the affected packages compiling and their tests passing, then a commit (conventional subject explaining why; body per repo convention).

---

### Task 1: Coordinator and budget correctness fixes

Fixes review-confirmed defects in code the correction keeps (design: Priority and budget; Error Handling).

**Files:**
- Modify: `internal/archive/service.go` (`defaultArchiveRetryDecision`, ~line 375)
- Modify: `internal/archive/hydrate.go` (`recordHydrationFailure`, ~line 348), `internal/archive/inventory.go` (`recordInventoryFailure`, ~line 76)
- Modify: `internal/github/graphql.go` (`NewGraphQLFetcher`, ~line 716)
- Modify: `internal/github/sync.go` (archive admission cost declarations), `internal/archive/hydrate.go` (declared costs)
- Verify/modify: `internal/platform/gitea/client.go`, `internal/platform/forgejo/client.go`, `internal/platform/gitlab/client.go` (budget transport beneath auth; no synthesized reset feeding the archive ceiling)
- Test: `internal/archive/service_test.go`, `internal/github/graphql_test.go` (or existing transport tests), `internal/github/archive_lifecycle_test.go`, provider client tests

**Interfaces:**
- Produces: preemption is not a failure — when an archive provider read fails and the request context was canceled by live work (`ctx.Err() != nil` after the canonical read returns), the archive service records **no** failure, does not increment `attempt_count`, and leaves the work claimable (treat exactly like `errAdmissionDeferred`).
- Produces: archive admission cost = worst-case wire attempts. Every archive `admit` call declares `2 × logicalRequests` so a 401-invalidate-retry can never overspend the admitted ceiling or live floor (design: "every wire attempt, including authentication retries, is counted").
- Produces: GitHub GraphQL budget transport sits **beneath** `tokenauth.AuthTransport` (auth retries each counted), matching REST.

**Steps:**

- [ ] **1.1** Write failing tests: (a) archive hydration read returning an error while the admitted request context is canceled records no `last_error_code`, no attempt increment, and the item remains due; (b) a GraphQL 401-then-retry spends 2 on `SyncBudget` via archive context; (c) admission at a ceiling with exactly `cost` remaining is denied when `cost` doubles for retry headroom (adjust existing `TestArchiveAdmissionPreservesProviderReserveForDeclaredCost` expectations).
- [ ] **1.2** Implement: short-circuit on `ctx.Err() != nil` in `recordHydrationFailure`/`recordInventoryFailure` callers before classification; reorder `NewGraphQLFetcher` layering to `budgetTransport` → `graphqlRateTransport` → inside `authRT`'s base; introduce `func archiveAttemptCost(logical int) int { return logical * 2 }` in `internal/archive` and use it at every `admit` call.
- [ ] **1.3** Audit gitea/forgejo/gitlab client construction: budget transport must be beneath the auth transport (counted per attempt); confirm no provider synthesizes a reset timestamp that reaches `archiveBudgetResetAt` — only provider-observed resets release archive surplus. Fix and test any violation found.
- [ ] **1.4** GitHub bulk sync: when a complete bulk response carries zero reviews for an MR that has persisted review history, do not clear `review_decision`; derived fields keep the existing aggregate unless the provider states a new one. Add regression test.
- [ ] **1.5** Run `go test ./internal/archive/... ./internal/github/... ./internal/platform/... -shuffle=on`; commit.

---

### Task 2: Canonical page types, bounded collector, and reader interfaces

Additive platform layer (design: Canonical page operations; Pagination bounds). No provider implements the new interfaces yet; no consumers change.

**Files:**
- Create: `internal/platform/page.go`, `internal/platform/page_test.go`
- Modify: `internal/platform/client.go` (new role interfaces alongside existing), `internal/platform/types.go`
- Modify: `internal/platform/page_collect.go` → fold into `page.go`

**Interfaces (produces — exact):**

```go
type Page[T any] struct {
    Items        []T
    NextCursor   string
    Exhausted    bool
    ProgressOnly bool
}

type ItemStateFilter string
const (
    ItemStateOpen ItemStateFilter = "open"
    ItemStateAll  ItemStateFilter = "all"
)

type ItemOrder string
const (
    ItemOrderCreated ItemOrder = "created"
    ItemOrderUpdated ItemOrder = "updated"
)

type ItemPageQuery struct {
    State        ItemStateFilter
    Order        ItemOrder
    UpdatedSince *time.Time
    Cursor       string
}

type LookupOutcome string
const (
    LookupPresent      LookupOutcome = "present"
    LookupRemoved      LookupOutcome = "removed"
    LookupMoved        LookupOutcome = "moved"
    LookupInaccessible LookupOutcome = "inaccessible"
)

type ItemLookup[T any] struct {
    Outcome     LookupOutcome
    Item        T
    Destination *RepoRef
}

type IssuePageReader interface {
    ListIssuesPage(context.Context, RepoRef, ItemPageQuery) (Page[Issue], error)
    LookupIssue(context.Context, RepoRef, int) (ItemLookup[Issue], error)
    ListIssueCommentsPage(context.Context, RepoRef, int, string) (Page[IssueEvent], error)
}

type MergeRequestPageReader interface {
    ListMergeRequestsPage(context.Context, RepoRef, ItemPageQuery) (Page[MergeRequest], error)
    LookupMergeRequest(context.Context, RepoRef, int) (ItemLookup[MergeRequest], error)
    ListMergeRequestCommentsPage(context.Context, RepoRef, int, string) (Page[MergeRequestEvent], error)
    ListSubmittedReviewsPage(context.Context, RepoRef, int, string) (Page[MergeRequestEvent], error)
    ListReviewThreadsPage(context.Context, RepoRef, int, string) (Page[MergeRequestReviewThread], error)
}

// CollectPages drains fetch from cursor. It errors (typed, platform.ErrProviderContract)
// when a page repeats any previously seen cursor or when maxPages is exceeded.
const MaxCollectPages = 1000
func CollectPages[T any](ctx context.Context, cursor string, fetch func(context.Context, string) (Page[T], error)) ([]T, error)
```

`ArchivePage[T]` is renamed to `Page[T]` (keep `ProgressOnly`), `ArchiveItemResult`/`ArchiveLookupOutcome` become `ItemLookup`/`LookupOutcome`, and `ValidateArchivePage`/`ValidateArchiveItemResult` become `ValidatePage`/`ValidateItemLookup`; update the existing `ArchiveReader` signatures and all provider/service references mechanically in this task (pure rename, no behavior change).

**Steps:**

- [ ] **2.1** Write failing tests for `CollectPages`: happy multi-page drain, missing-cursor error, immediate repeat, alternating A→B→A cycle detected via seen-set, `MaxCollectPages` exceeded, `ProgressOnly` pages allowed but bounded.
- [ ] **2.2** Implement types + `CollectPages` (track every seen cursor in a `map[string]struct{}`); perform the renames; run `go test ./internal/... -shuffle=on`.
- [ ] **2.3** Commit.

---

### Task 3: GitHub provider slice

Single request/normalization implementation per GitHub dataset (design: Target Provider Architecture). GitHub detail pages already have canonical readers (`ListIssueCommentsPage`, `ListReviewsPage`, `ListArchiveReviewThreadsPage` in `archive_client.go`); this task promotes them to the canonical interface and unifies inventory + lookup.

**Files:**
- Create: `internal/github/pages.go` (canonical `IssuePageReader`/`MergeRequestPageReader` implementation on the provider type), moving code from `internal/github/archive_client.go`
- Modify: `internal/github/client.go`, `internal/github/sync.go` (provider methods only)
- Test: `internal/github/pages_test.go` (move/adapt from `archive_client_test.go`)

**Interfaces:**
- Consumes: Task 2 types.
- Produces: the GitHub provider satisfies both page-reader interfaces. `ListIssuesPage`/`ListMergeRequestsPage` dispatch internally: `StateOpen` uses the existing REST open-list request; `StateAll` uses the existing GraphQL historical request; `UpdatedSince != nil` uses the existing updated-scan request. One method per item type owns all three request shapes and their normalization (moving, not copying, the bodies of `ListOpenIssues`/`listArchiveIssues`/`ListIssuesPage(REST)` etc.).
- Produces: `LookupIssue`/`LookupMergeRequest` absorb `GetIssue`/`GetArchiveIssue` classification (`classifyArchiveIssueError` renamed `classifyIssueLookup`); live `GetIssue`/`GetMergeRequest` and archive `GetArchive*` methods become one-line wrappers over the lookup (wrappers die in Tasks 6/11).
- Produces: `ListReviewThreadsPage` keeps a batch size ≥ the live batch (100 threads/page) — one knob, no live/archive divergence.
- Note: the GraphQL bulk fetch (`graphql.go`) is an optional canonical producer (design: Optimized bulk observations) — leave it, but its persisted rows must flow through the Task 7/8 commit APIs when those land.

**Steps:**

- [ ] **3.1** Write parity tests: for each dataset, the canonical method and the legacy method (live and archive entry points) over the same fake transport produce identical requests and identical normalized rows.
- [ ] **3.2** Implement `pages.go`; make every legacy method (`ListOpenIssues`, `ListHistoricalIssues`, `ListUpdatedIssues`, `GetIssue`, `GetArchiveIssue`, MR equivalents, dataset methods) a one-line delegate; delete the duplicated request/normalization bodies.
- [ ] **3.3** `go test ./internal/github/... -shuffle=on`; commit.

---

### Task 4: GitLab provider slice

GitLab currently duplicates pagination/request construction between `client.go` and `archive.go` (only normalizers shared).

**Files:**
- Create: `internal/platform/gitlab/pages.go`
- Modify: `internal/platform/gitlab/client.go`, `internal/platform/gitlab/archive.go`
- Test: `internal/platform/gitlab/pages_test.go` (absorb `archive_test.go` cases)

**Interfaces:**
- Consumes: Task 2 types.
- Produces: gitlab provider satisfies both page-reader interfaces. One internal discussions page fetcher per item type feeds both `List*CommentsPage` (ordinary filter) and `ListReviewThreadsPage` (inline extraction) — the endpoint is constructed once. `ListSubmittedReviewsPage` returns the typed `unsupported_capability` error (capability metadata unchanged).
- Produces: live `ListIssueEvents`/`ListMergeRequestEvents`/`ListMergeRequestComments`/`ListOpenIssues`/`ListOpenMergeRequests`/`GetIssue`/`GetMergeRequest` re-implemented as `CollectPages`/lookup wrappers over the canonical methods; legacy whole-list pagination loops deleted.

**Steps:**

- [ ] **4.1** Parity tests as in 3.1 (live vs archive vs canonical over one fake transport, per dataset).
- [ ] **4.2** Implement; delete duplicated pagination (`listIssueDiscussions` whole-list loop, `gitLabArchivePage` duplication — keep exactly one).
- [ ] **4.3** `go test ./internal/platform/gitlab/... -shuffle=on`; commit.

---

### Task 5: Gitealike provider slice

Closest to done (shared `transport` + normalizers). Fix the review-confirmed gap: live event readers still call `collectPages` directly instead of the canonical pages (roborev 6870).

**Files:**
- Create: `internal/platform/gitealike/pages.go`
- Modify: `internal/platform/gitealike/provider.go`, `internal/platform/gitealike/archive.go`
- Test: `internal/platform/gitealike/pages_test.go`

**Interfaces:**
- Consumes: Task 2 types.
- Produces: canonical methods over the existing `transport`; `ListReviewThreadsPage` returns typed `unsupported_capability`. Live `ListIssueEvents`/`ListMergeRequestEvents` aggregate via `CollectPages` over `ListIssueCommentsPage`/`ListMergeRequestCommentsPage`/`ListSubmittedReviewsPage` plus the provider-specific commit-event fetch (commits are not a correction dataset and stay provider-internal). The unused `ListIssueComments`/`ListMergeRequestComments` wrappers are deleted. `collectPages` (unbounded variant) is deleted in favor of `platform.CollectPages`.

**Steps:**

- [ ] **5.1** Parity tests per dataset; plus a live-path test proving multi-page comments reach the aggregate event list through the canonical page methods.
- [ ] **5.2** Implement; delete `collectPages` and the dead wrappers.
- [ ] **5.3** `go test ./internal/platform/gitealike/... -shuffle=on`; commit.

---

### Task 6: Convert live sync and registry to canonical readers

**Files:**
- Modify: `internal/platform/registry.go` (accessors return validating-wrapped canonical readers), `internal/platform/client.go` (old role-interface read methods removed: `ListOpenIssues`, `GetIssue`, `ListOpenMergeRequests`, `GetMergeRequest` from the role interfaces; `ListIssueEvents`/`ListMergeRequestEvents`/`ListMergeRequestReviewThreads` stay as aggregate members implemented via collectors)
- Create: `internal/platform/reader_validation.go` + `internal/platform/reader_validation_test.go` (provider-neutral validating wrapper — design: Canonical readers stay contract-validated; port checks from `archive_reader.go`: canonical ref, provider identity, capability requirements, positive item numbers, page/lookup validation, moved-destination identity)
- Create: `internal/platform/collect.go` — package-level live helpers:

```go
func ListOpenIssues(ctx context.Context, r IssuePageReader, ref RepoRef) ([]Issue, error)          // CollectPages over ItemPageQuery{State: ItemStateOpen}
func ListOpenMergeRequests(ctx context.Context, r MergeRequestPageReader, ref RepoRef) ([]MergeRequest, error)
func RequireIssue(ctx context.Context, r IssuePageReader, ref RepoRef, number int) (Issue, error)   // LookupIssue; non-present → typed platform.Error (same code GetIssue used)
func RequireMergeRequest(ctx context.Context, r MergeRequestPageReader, ref RepoRef, number int) (MergeRequest, error)
```

- Modify: `internal/github/sync.go` call sites (≈ lines 4676, 4767, 6315, 6431, 6625, 7412 and `GetIssue`/`GetMergeRequest` uses) to the helpers/aggregates
- Test: adapt existing sync tests; delete provider-method tests made redundant

**Steps:**

- [ ] **6.1** Write validating-wrapper contract tests (table-driven, all four providers via fakes): identity mismatch, non-positive item number, missing capability, echoed-cursor page, moved-destination mismatch.
- [ ] **6.2** Implement wrapper + helpers; convert registry and sync call sites; delete the superseded per-provider live method bodies everywhere they became dead.
- [ ] **6.3** `go test ./internal/... -shuffle=on`; commit.

---

### Task 7: Dataset progress schema and the child-dataset commit transaction

Load `kenn:db-migration-discipline` first. Amend `000039_historical_activity_archive.up.sql` **in place** (PR-local). Staging table and item status columns survive until Task 10's final amend so every commit stays coherent.

**Files:**
- Modify: `internal/db/migrations/000039_historical_activity_archive.up.sql` / `.down.sql`
- Create: `internal/db/queries_dataset_progress.go`, `internal/db/queries_dataset_progress_test.go`
- Modify: `internal/db/types.go`

**Schema added by the amend (exact; timestamps TEXT UTC like the rest of migration 39):**

```sql
CREATE TABLE middleman_archive_repo_scans (
    repo_id INTEGER NOT NULL REFERENCES middleman_repos(id) ON DELETE CASCADE,
    scan TEXT NOT NULL CHECK (scan IN ('issue_inventory','merge_request_inventory','maintenance_issues','maintenance_merge_requests')),
    scan_generation INTEGER NOT NULL DEFAULT 1,
    next_cursor TEXT,
    last_input_cursor TEXT,
    page_count INTEGER NOT NULL DEFAULT 0 CHECK (page_count >= 0),
    status TEXT NOT NULL DEFAULT 'pending' CHECK (status IN ('pending','running','complete','blocked','failed')),
    last_error_code TEXT,
    last_error_detail TEXT,
    updated_at TEXT NOT NULL,
    PRIMARY KEY (repo_id, scan)
);

CREATE TABLE middleman_archive_dataset_progress (
    repo_id INTEGER NOT NULL,
    item_type TEXT NOT NULL CHECK (item_type IN ('issue','merge_request')),
    item_number INTEGER NOT NULL CHECK (item_number > 0),
    dataset TEXT NOT NULL CHECK (dataset IN ('lookup','comments','reviews','inline_comments')),
    parent_revision INTEGER NOT NULL DEFAULT 0,
    scan_generation INTEGER NOT NULL DEFAULT 1,
    next_cursor TEXT,
    last_input_cursor TEXT,
    page_count INTEGER NOT NULL DEFAULT 0 CHECK (page_count >= 0),
    status TEXT NOT NULL CHECK (status IN ('pending','running','complete','unsupported','blocked','failed','terminal')),
    observed_count INTEGER NOT NULL DEFAULT 0 CHECK (observed_count >= 0),
    attempt_count INTEGER NOT NULL DEFAULT 0 CHECK (attempt_count >= 0),
    next_retry_at TEXT,
    last_error_code TEXT,
    last_error_detail TEXT,
    started_at TEXT,
    completed_at TEXT,
    updated_at TEXT NOT NULL,
    PRIMARY KEY (repo_id, item_type, item_number, dataset),
    FOREIGN KEY (repo_id, item_type, item_number)
        REFERENCES middleman_archive_items(repo_id, item_type, item_number) ON DELETE CASCADE
);
CREATE INDEX idx_archive_dataset_progress_due
    ON middleman_archive_dataset_progress (repo_id, status, next_retry_at, item_type, item_number);

ALTER TABLE middleman_issue_events ADD COLUMN ingest_generation INTEGER;
ALTER TABLE middleman_mr_events ADD COLUMN ingest_generation INTEGER;
```

`ingest_generation` is ingestion metadata for ordinary-comment reconciliation only — never exposed through API or reports.

**Interfaces (produces — exact):**

```go
type ArchiveScanKind string        // the four scan values above
type DomainParentRef struct { ItemType ArchiveItemType; ID int64 }
type DatasetRows struct {
    IssueComments []IssueEvent
    MRComments    []MREvent
    Reviews       []MREvent
    ReviewThreads []MRReviewThread
    ThreadEvents  []MREvent
}
type DatasetProgressAdvance struct {
    RepoID      int64
    ItemNumber  int
    InputCursor string
    NextCursor  string
}
type DatasetPageCommit struct {
    Parent           DomainParentRef
    ExpectedRevision int64
    Dataset          ArchiveDataset      // gains ArchiveDatasetLookup constant
    ScanGeneration   int64
    Rows             DatasetRows
    Final            bool
    Progress         *DatasetProgressAdvance
}
type StaleDatasetProgressError struct { /* repo/item/dataset, expected+got cursor/generation/revision */ }
type ScanBlockedError struct { /* scope + reason */ }

// ParentLookupCommit is the single-item mode of the canonical parent-observation
// operation (design: One domain-ingestion implementation). It shares the parent
// upsert + work-reconciliation tx core with CommitArchiveInventoryPage — there is
// no third parent writer.
type ParentLookupCommit struct {
    RepoID       int64
    ItemType     ArchiveItemType
    ItemNumber   int
    Outcome      platform.LookupOutcome // present | removed | moved | inaccessible
    Issue        *Issue                 // present + issue
    MergeRequest *MergeRequest          // present + merge_request
    Destination  *RepoIdentity          // moved
    ErrorCode    string                 // terminal outcomes
    ErrorDetail  string
    Now          time.Time
}
// Present: upsert parent snapshot, bind lookup progress to the new parent revision
// (status complete), reopen child datasets whose bound revision was superseded.
// Removed/inaccessible: item lifecycle terminal + lookup progress terminal.
// Moved: as removed, plus queue destination prompt (QueueArchivePromptByIdentity).
func (d *DB) CommitParentLookup(ctx context.Context, commit ParentLookupCommit) error

func (d *DB) CommitDatasetPage(ctx context.Context, commit DatasetPageCommit) error
func (d *DB) GetDatasetProgress(ctx context.Context, repoID int64, itemType ArchiveItemType, itemNumber int, dataset ArchiveDataset) (ArchiveDatasetProgress, error)
func (d *DB) ReopenDatasetsForParent(ctx context.Context, tx *sql.Tx, parent DomainParentRef, repoID int64, itemNumber int, newRevision int64) error
func (d *DB) BlockDatasetProgress(ctx context.Context, key /* repo,item,dataset */, code, detail string) error
func (d *DB) ResetDatasetProgress(ctx context.Context, key) error   // new generation, cursor cleared, pending
```

**`CommitDatasetPage` semantics (design: One page commit API; Ordinary comments; Submitted reviews and review threads; Parent revision changes; No page replay after commit; Pagination bounds):**

1. One transaction. Verify parent `snapshot_revision == ExpectedRevision`; stale → increment that dataset's generation, clear its cursor, mark `pending` for the new revision, return `*StaleDatasetProgressError` (additive rows and unreconciled comments retained).
2. When `Progress != nil`: CAS — `InputCursor == next_cursor` advances (`page_count++`, bound `maxScanPages = 10_000` per generation → exceed marks `blocked` with `last_error_code = "page_bound"` and returns `*ScanBlockedError`); `InputCursor == last_input_cursor` (same generation) is the idempotent replay → return `nil` writing nothing; anything else → `*StaleDatasetProgressError`. Cursor-cycle and invalid-cursor blocks use `last_error_code = "invalid_cursor"` — the durable reason distinguishes oversized scans from cycles for the Task 12 reset modes.
3. Upsert rows: ordinary comments stamped `ingest_generation = ScanGeneration`; reviews/threads plain upserts by stable provider identity (never delete absent history).
4. `Final && Dataset == comments`: delete ordinary comments of that parent whose `ingest_generation` differs (NULL included) in the same transaction; mark progress `complete`, clear cursors.
5. `Final` on additive datasets: mark complete only.
6. `Progress == nil` (live): domain writes only; then conditionally satisfy matching archive progress (same parent revision, non-blocked, non-paused) — failures of that bookkeeping never reject the live write.

**Steps:**

- [ ] **7.1** Amend migration + `.down.sql`; extend the existing forward-migration test to assert new tables/columns and `PRAGMA integrity_check`/`foreign_key_check`.
- [ ] **7.2** Write failing db tests: atomic rows+cursor commit; failed tx commits neither; idempotent replay; stale cursor/generation/revision rejection; comment reconciliation only at exhaustion; additive retention; reopen-on-newer-revision; page-bound blocking; live commit with absent/paused/blocked progress still writes domain rows.
- [ ] **7.3** Implement; `go test ./internal/db/... -shuffle=on`; commit.

---

### Task 8: Live child writers over the shared commit core

**Files:**
- Modify: `internal/db/queries_snapshot_children.go` — re-implement `CommitIssueChildSnapshot` and `CommitMergeRequestChildSnapshot` as compositions of the Task 7 tx core (one transaction, one `commitDatasetPageTx` per complete dataset with `Final: true, Progress: nil`, plus other-event upserts and derived-field updates); `satisfyArchiveDatasetsFromNormalSyncTx` rewritten against `middleman_archive_dataset_progress`.
- Test: `internal/db/queries_snapshot_children_test.go` adaptations; a test proving a complete live observation marks matching archive progress complete without provider requests, and that missing/paused/stale progress never rejects the live write.

**Steps:**

- [ ] **8.1** Write/adapt failing tests, **8.2** implement (the old replace-dataset helpers become the internals of the shared core — exactly one implementation of comment replacement remains), **8.3** `go test ./internal/db/... ./internal/github/... -shuffle=on`; commit.

---

### Task 9: Rewrite `internal/archive` onto canonical readers and incremental ingestion

Deletes payload staging from the archive path (design: Archive Work and Scheduling; Incremental Ingestion).

**Files:**
- Modify: `internal/archive/hydrate.go` (rewrite), `internal/archive/scheduler.go`, `internal/archive/inventory.go`, `internal/archive/maintenance.go`, `internal/archive/service.go`
- Modify: `internal/db/queries_archive.go` (`CommitArchiveInventoryPage` gains scan-generation/page-bound CAS against `middleman_archive_repo_scans`; `ClaimArchiveItem` selects due work from `middleman_archive_dataset_progress`; `MarkArchiveItemHydrated`/`FailArchiveItem` operate on progress rows)
- Test: `internal/archive/service_test.go`, `internal/db/queries_archive_test.go` (delete staging-shape tests, add progress-shape tests)

**Interfaces:**
- Consumes: Task 2 reader interfaces (via registry), Task 7 commit APIs.
- Produces: `hydrateItem` per dataset loop: `admit(archiveAttemptCost(1))` → canonical page method with durable cursor → release → `CommitDatasetPage{Progress: &…}` → next page or yield. No `json.Marshal` of pages, no `GetArchiveDatasetStage`/`LoadArchiveDatasetPages` calls. Parent lookup is the `lookup` dataset (cost `archiveAttemptCost(2)`), persisted through `CommitParentLookup` (Task 7) — present upserts the parent and reopens superseded datasets; removed/moved/inaccessible mark the item terminal; moved queues the destination prompt.
- Produces: inventory/maintenance pages commit through `CommitArchiveInventoryPage` with repo-scan CAS + page bounds; invalid-cursor provider errors mark the scan/dataset `blocked` (never silent page-one restart).
- Produces: error scoping — page-level provider-contract violations (echoed cursor, malformed page, wrong-parent item, including those raised by the Task 6 validating wrapper) block only the affected scan or dataset; only repository-wide failures (identity mismatch, capability misdeclaration, auth) block the repository. Add coverage for an echoed-cursor page blocking one dataset while the repository's other work proceeds.

**Steps:**

- [ ] **9.1** Write failing behavior tests: cursor durability across service restart for inventory, maintenance, comments, reviews, threads (interrupt after page 1, restart, next request uses committed cursor); blocked-on-invalid-cursor; preemption non-failure (from Task 1) still holds through the new loop; live independence (archive paused/failed does not affect live sync).
- [ ] **9.2** Implement rewrite; delete `fetchArchiveDataset` staging generic and friends.
- [ ] **9.3** `go test ./internal/archive/... ./internal/db/... ./internal/github/... -shuffle=on`; commit.

---

### Task 10: Final schema amend and staging/publication deletion

Load `kenn:db-migration-discipline`. Amend migration 39 to the **final** schema (design: Schema and Migration Policy).

**Files:**
- Modify: `internal/db/migrations/000039_historical_activity_archive.up.sql` / `.down.sql` — remove `middleman_archive_dataset_pages` creation entirely; remove from `middleman_archive_items`: `comments_status`, `reviews_status`, `inline_comments_status`, `mirrored_provider_updated_at`, `attempt_count`, `next_retry_at`, `last_error_code`, `last_error_detail`, `hydration_snapshot_updated_at`; drop the superseded prompt/inventory cursor columns from `middleman_archive_repos` that `middleman_archive_repo_scans` replaced; rebuild the two item indexes against progress-row state.
- Delete from `internal/db/queries_archive.go` + `types.go`: `ArchiveDatasetPage`, `ArchiveDatasetStage`, `ArchiveDatasetKey`, `ArchiveDatasetLimitError`, `GetArchiveDatasetStage`, `CommitArchiveDatasetPage`, `LoadArchiveDatasetPages`, `PublishArchiveIssueComments`, `PublishArchiveMREvents`, `PublishArchiveReviewThreads`, `requireArchiveDatasetSnapshotTx`, `clearStagedArchiveDatasetsForNewSnapshotTx`, and every payload page/record/byte accounting helper.
- Delete their tests in `internal/db/queries_archive_test.go`.
- Modify: forward-migration test — previous released schema (migration 38) → final migration 39; assert local-only/domain rows survive, `PRAGMA integrity_check`, `PRAGMA foreign_key_check`; no downgrade tests.

**Steps:**

- [ ] **10.1** Amend migration + delete code/tests, **10.2** `grep -rn "ArchiveDatasetPage\|ArchiveDatasetStage\|PublishArchive\|middleman_archive_dataset_pages" --include='*.go' --include='*.sql' internal/ cmd/` → only historical docs may match, **10.3** `go test ./internal/... -shuffle=on`; commit.

---

### Task 11: Delete the archive provider interface layer

**Files:**
- Delete: `internal/platform/archive_reader.go`, `internal/github/archive_client.go`, `internal/platform/gitlab/archive.go`, `internal/platform/gitealike/archive.go` (canonical code already moved by Tasks 3–5; anything still referenced moves to the canonical files first — moving, never copying)
- Modify: `internal/platform/client.go` (delete `ArchiveReader` interface), `internal/platform/registry.go` (delete `ArchiveReader()` accessor; archive capability gating moves to the canonical accessor the archive service uses)
- Delete/rewrite: `internal/platform/archive_test.go` → contract tests already live in `reader_validation_test.go` (Task 6); delete tests whose only purpose was the wrapper/prefixed methods.
- Modify: every remaining `ListArchive*`/`GetArchive*` caller (should be none after Task 9 — verify).

**Steps:**

- [ ] **11.1** Delete; fix compilation; **11.2** run all structural gate searches from Global Constraints — zero production matches; **11.3** `go test ./internal/... -shuffle=on`; commit.

---

### Task 12: Scoped reset operation (CLI + API)

Blocked scans must be recoverable (design: Provider Cursor Failure).

**Files:**
- Modify: `internal/archive/service.go` — `func (s *Service) ResetScan(ctx context.Context, ref platform.RepoRef, scope ResetScope) error` where `ResetScope{Scan *ArchiveScanKind; ItemType *ArchiveItemType; ItemNumber *int; Dataset *ArchiveDataset; Mode ResetMode}` resets exactly one repo scan or one item dataset; domain content untouched. `ResetMode` is `restart` (new generation, cursor cleared — the only valid recovery for `invalid_cursor` blocks) or `continue` (page counter cleared, cursor and generation retained — for `page_bound` blocks on legitimately oversized scans; refused with a typed error for `invalid_cursor` blocks).
- Modify: `internal/server/archive_routes.go` — `POST /archive/reset` (huma op `reset-archive-scan`; body: repo + scope + mode; 400 on non-blocked/missing target unless `force`).
- Modify: `cmd/middleman/archive_cli.go` — `middleman archive reset --repo … [--scan …|--item TYPE/N --dataset …] [--continue]`.
- Regenerate: `make api-generate` (openapi, Go client, TS schema).
- Test: service + apitest + CLI test.

**Steps:**

- [ ] **12.1** Failing tests (blocked scan → reset → next request from cleared cursor in a new generation; unrelated cursors untouched), **12.2** implement + `make api-generate`, **12.3** `go test ./internal/... ./cmd/... -shuffle=on`; commit.

---

### Task 13: Full-stack coverage

**Files:**
- Modify: `internal/server/apitest/archive_test.go` — one workflow test through real HTTP + SQLite: start → several provider pages → service restart (reopen DB, new Syncer/Service) → resumes from committed cursors (fake provider asserts no page-one refetch) → pause → resume → status/completeness → partial report (coverage says incomplete) → completion → complete report (deterministic bytes).
- Add to `internal/server/apitest/archive_test.go`: blocked-scan recovery through the real API — block a scan (cursor cycle from the fake provider), `POST /archive/reset` (restart mode), prove a stale pre-reset page commit is rejected by the new generation, prove unrelated scan/dataset state is untouched, and collection resumes; plus a `page_bound` block recovered with continue mode retaining the cursor.
- Rewrite: `internal/server/e2etest/archive_snapshot_race_test.go` against the Task 7/8 commit APIs (live and archive writes racing on one parent; live never rejected by archive state).
- Modify: `internal/archive/report_service_test.go` — partial dataset progress can never be reported complete (progress rows in every non-complete status).

**Steps:**

- [ ] **13.1** Write tests, **13.2** make green, **13.3** `go test ./... -shuffle=on` (full run); commit.

---

### Task 14: Final audit and docs truth pass

**Files:**
- Modify: `docs/archive.md` — make every stated invariant true of the final code: single canonical reader/publication path (now real); reviews/review threads described as stable-identity **additive** history (matching Task 7 semantics); ordinary comments as replaceable snapshots reconciled at exhaustion; budget wording: hourly budget accounting independent of provider reset windows, only provider-observed resets release archive surplus, unknown reset ⇒ no surplus (intentional), rate-limit snapshot probes documented as the one read outside provider-work serialization and why.
- Audit artifact: appended section in this plan or the PR description with exact `origin/main...HEAD` totals by category, corrective delta from `3e914c24`, structural search outputs, and the largest remaining production files with one-line justifications.

**Steps:**

- [ ] **14.1** Docs pass; **14.2** run the metrics classifier and all gate searches; verify quantitative gates (≥2,000 production / ≥4,000 total deleted vs `3e914c24`; ≤ ~+6,500 / ~+18,000 vs main) — **if structural gates pass but thresholds do not, STOP for explicit user review**; **14.3** `make test` + `make lint` + `make vet`; commit.
