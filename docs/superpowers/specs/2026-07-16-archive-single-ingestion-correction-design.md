# Archive Single-Ingestion Correction Design

**Status:** Proposed correction for PR 671

**Baseline:** `3e914c24` (`github-archiving`)

**Supersedes:** The architectural approach in `docs/superpowers/plans/2026-07-16-archive-sync-consolidation.md`. That plan improved shared helpers but retained archive-specific provider and persistence subsystems. This design replaces that approach.

## Summary

PR 671 must retain the archive product: complete historical collection, resumability under a constrained provider token bucket, explicit completeness and coverage, start/pause/status controls, and deterministic SQLite-only reports. The correction must make those capabilities a policy over middleman's normal provider and ingestion stack rather than a parallel stack.

There will be one page-oriented provider read surface and one incremental domain-ingestion surface. Live sync drains those operations for current work. Archive workers invoke the same operations one page at a time and advance durable cursors in the same transaction that persists each normalized page.

Provider-derived cache data is replaceable. Preserving it is desirable when simple, but it must not justify duplicate readers, writers, repair paths, payload staging, or compatibility scaffolding. Local-only user state that cannot be reconstructed from a provider remains protected.

The design keeps durable cursors for every paginated provider operation. The provider token bucket is too limited to restart large scans from page one after interruption. Once a page and its next cursor commit during a logical scan, middleman does not fetch that page again unless the source revision changes, the provider invalidates the cursor, or an operator explicitly resets that scan.

## Why the Previous Correction Was Insufficient

At baseline `3e914c24`, the PR still has approximately:

- 21,892 net added lines overall;
- 8,497 net added handwritten production lines;
- 10,612 net added test lines;
- 1,949 net added generated lines.

The remaining implementation still contains a second provider interface, archive-prefixed provider methods, separate provider files, staged JSON page payloads, archive-only publication methods, and tests that primarily protect those structures. Sharing low-level helpers did not remove the parallel architecture.

The correction is successful only when the obsolete interfaces, files, methods, schema, and tests are deleted. Passing tests without those deletions is not sufficient.

## Product Scope That Must Remain

The correction retains all user-visible archive capabilities:

- historical issues and pull/merge requests across all states;
- ordinary issue and pull/merge-request comments;
- submitted reviews;
- inline review threads and replies;
- resumable collection under a limited API token bucket;
- archive discovery and full collection modes;
- start, pause, and status operations;
- completeness, coverage, retry, blocked, and terminal states;
- maintenance scans for updated items;
- deterministic Markdown and JSON reports computed only from SQLite;
- existing report API and CLI workflows;
- live-sync priority, provider reserve, and time-released archive budget rules.

Reports are the product outcome and are not a scope-reduction candidate.

## Non-Goals

This correction does not:

- preserve the current PR-local archive schema or staged payload format;
- support rolling downgrade to an earlier PR build;
- retain archive-prefixed provider APIs as adapters;
- retain dual domain writers during migration;
- silently restart rejected cursors from page one;
- remove reports, completeness, or resumability to make the diff smaller;
- rewrite local-only workflow state, drafts, workspace links, or user configuration.

## Core Invariants

### One provider implementation

Each provider has one normalized page operation for each provider dataset. Live and archive callers may choose different query options and consumption patterns, but they do not construct the same endpoint or normalize the same response in separate methods.

### One domain-ingestion implementation

All provider observations enter through exactly two canonical revision-aware ingestion operations: one parent-observation commit and one child-dataset page commit. They are distinct domain operations because a parent observation upserts parent items, creates or refreshes per-item archive work, and advances scan progress, while a dataset page writes child rows scoped to one parent and advances that parent's dataset progress. Neither operation has an archive-only or live-only variant. Archive work may attach durable progress advancement to a page commit; live work may omit it. Domain persistence is identical in both cases and cannot depend on archive state existing or being healthy.

The parent-observation commit has two modes sharing one parent upsert and work-reconciliation implementation:

- an inventory page: many parents from one provider page, advancing a repository-level scan cursor;
- a single-item lookup: one parent from a lookup outcome, advancing that item's `lookup` dataset progress.

A present lookup atomically upserts the refreshed parent snapshot, binds the lookup progress to the resulting parent revision, and reopens child datasets whose bound revision it superseded. A removed, moved, or inaccessible lookup atomically records the item lifecycle outcome and marks the lookup progress terminal; a moved lookup additionally queues the destination repository for prompt follow-up. Lookup commits carry the expected lookup scan generation and are compare-and-swapped exactly like child-dataset commits, so a lookup completed after a forced reset cannot mark the reset progress terminal or upsert a stale parent. There is no third parent writer.

Beneath both operations, every parent snapshot upsert — live sync, inventory page, single-item lookup — flows through one shared revision-advancing parent upsert core. Live sync is the progress-optional mode of that core: it advances the parent revision without touching archive progress, and there are not separate live and archive SQL paths for writing the same parent rows.

### Durable progress for scarce API work

Historical inventory, maintenance scans, and detail datasets all keep durable opaque cursors. A committed page is not fetched again during the same logical scan.

### No durable provider payload staging

Middleman stores normalized domain rows and compact scan progress, not serialized provider page payloads. The domain tables are the durable copy of provider content.

### Live work remains independent and highest priority

Interactive refresh, open/watched detail, notifications, and current index sync outrank archive work. Archive admission is reacquired for each provider page. No database transaction or cursor state holds provider-host admission.

### Replaceable cache, protected local state

Provider-derived rows and archive progress may be reset and rebuilt when necessary. Local-only user state must survive schema and ingestion changes.

## Chosen Approach

Three approaches were considered.

### Rejected: keep staged JSON payloads and share only final publication

This preserves restart behavior but retains a second durable representation of provider content, page reconstruction, payload size accounting, archive-only publication APIs, and a large body of staging tests. It does not meet the consolidation goal.

### Rejected: keep no partial progress and restart scans from page one

This is incompatible with the constrained provider token bucket. Large repositories or long review threads could repeatedly consume the early pages and never reach later pages.

### Chosen: durable cursor plus incremental domain ingestion

Each normalized page is written directly to the ordinary domain tables. The same transaction advances a compact dataset progress row. Inventory and maintenance continue to atomically upsert parent items and advance their existing repository-level cursors. Detail datasets use the same pattern without storing page payloads.

This preserves provider work while deleting payload staging and parallel publication.

## Target Provider Architecture

### Remove the archive provider interface

Delete `platform.ArchiveReader` and `validatingArchiveReader`. Archive capability checks remain provider metadata, but provider reads are exposed through canonical reader interfaces.

No production provider method may be named `ListArchive*` or `GetArchive*`.

### Canonical page operations

The provider-neutral surface exposes normalized page operations conceptually equivalent to:

```go
type Page[T any] struct {
    Items      []T
    NextCursor string
    Exhausted  bool
}

type ItemPageQuery struct {
    State        ItemStateFilter // open or all
    Order        ItemOrder       // created or updated
    UpdatedSince *time.Time
    Cursor       string
}

type ItemLookup[T any] struct {
    Outcome     LookupOutcome // present, removed, moved, inaccessible
    Item        T
    Destination *RepoRef
}

type ProviderReader interface {
    ListIssuesPage(context.Context, RepoRef, ItemPageQuery) (Page[Issue], error)
    ListMergeRequestsPage(context.Context, RepoRef, ItemPageQuery) (Page[MergeRequest], error)
    LookupIssue(context.Context, RepoRef, int) (ItemLookup[Issue], error)
    LookupMergeRequest(context.Context, RepoRef, int) (ItemLookup[MergeRequest], error)
    ListIssueCommentsPage(context.Context, RepoRef, int, string) (Page[IssueEvent], error)
    ListMergeRequestCommentsPage(context.Context, RepoRef, int, string) (Page[MergeRequestEvent], error)
    ListSubmittedReviewsPage(context.Context, RepoRef, int, string) (Page[MergeRequestEvent], error)
    ListReviewThreadsPage(context.Context, RepoRef, int, string) (Page[MergeRequestReviewThread], error)
}
```

The final names may follow existing package conventions, but the shape and ownership are fixed:

- page results are provider-neutral normalized values;
- cursors are opaque to callers;
- provider-specific sort fences, pagination tokens, and nested-thread continuation stay inside the provider implementation;
- live whole-dataset helpers are collectors over these page methods;
- archive workers call the same methods with durable cursors.

### Canonical readers stay contract-validated

The provider-neutral contract checks currently owned by `validatingArchiveReader` move to a provider-neutral validating wrapper around the canonical reader interfaces; they are not deleted with it. The wrapper enforces canonical repository identity on inputs and returned parents, positive item numbers for item-scoped reads, capability declarations before provider calls, typed lookup outcomes, and cursor/page invariants (a returned page must not echo its input cursor as `NextCursor` without being exhausted). Table-driven contract tests cover every provider through this wrapper independently of the removed archive-prefixed interface.

### Optimized bulk observations

The GitHub ETag-gated open-item read followed by the GraphQL bulk fetch remains a supported optimization. A bulk producer is an optional canonical producer: it emits the same provider-neutral normalized parent and child values and commits them through the same two canonical ingestion operations. It must not persist through any other writer and must not be required by any caller; every dataset it covers is also reachable through the canonical page methods. Provider-equivalence gates are qualified accordingly: a bulk observation satisfies equivalence when its committed rows match what the canonical page methods would have produced, without requiring the separate detail requests it avoided.

### Inventory and current-index reads share methods

Open-item sync uses `StateOpen`. Historical inventory uses `StateAll` ordered for stable traversal. Maintenance uses `StateAll` with an overlapped `UpdatedSince` watermark. A provider may choose different endpoint parameters for those queries, but the request construction and normalization live in one method per item type.

### Lookup outcomes are canonical

The existing live `GetIssue`/`GetMergeRequest` and archive `GetArchive*` split is removed. One lookup operation returns a typed outcome. Live callers require `present`; archive callers record removed, moved, or inaccessible outcomes. Provider probing and error classification occur once.

### Provider file layout

The following archive-specific files must disappear:

- `internal/github/archive_client.go`
- `internal/platform/gitlab/archive.go`
- `internal/platform/gitealike/archive.go`
- `internal/platform/archive_reader.go`

Necessary provider code moves into canonical files named by dataset responsibility, such as inventory pages, detail pages, lookup, or normalization. Renaming alone is not consolidation: duplicate request and normalization code must be deleted before the old files are removed.

## Durable Archive State

### Repository-level scan progress

Keep durable repository cursors because they protect scarce provider requests:

- historical issue cursor and exhausted flag;
- historical merge-request cursor and exhausted flag;
- fixed maintenance scan boundary;
- maintenance issue cursor and exhausted flag;
- maintenance merge-request cursor and exhausted flag;
- last fully completed maintenance watermark.

Each repository-level scan (historical issues, historical merge requests, maintenance issues, maintenance merge requests) also carries its own scan generation, status (including `blocked`), per-generation page count, and sanitized last-error metadata. Cursor commits are bound to the scan generation with compare-and-swap semantics, so a response from before an explicit reset cannot advance the new generation.

`CommitArchiveInventoryPage` or its replacement continues to atomically:

1. upsert the normalized parent snapshots from one provider page;
2. create or refresh per-item archive work;
3. advance the corresponding repository cursor;
4. mark exhaustion when the final page commits.

A crash cannot commit item rows without the cursor or the cursor without the item rows.

### Item state

Keep one archive item row for provider inventory identity and lifecycle:

```text
repo_id
item_type
item_number
provider_item_id
provider_created_at
provider_updated_at
lifecycle_state
refresh_reason
```

Dataset status, cursor, retry, and error fields move out of repeated item columns and into generic dataset progress rows. Parent item hydration (the lookup that fetches or refreshes the parent before any child dataset can be scanned) is itself modeled as the `lookup` dataset, so item-scoped transient failures, retries, backoff, and terminal outcomes use the same progress machinery as child datasets instead of dedicated item columns.

### Dataset progress

Replace `middleman_archive_dataset_pages` and the three status columns on each archive item with one compact row per item and dataset:

```text
repo_id
item_type
item_number
dataset                 -- lookup, comments, reviews, inline_comments
parent_revision
scan_generation
next_cursor
last_input_cursor       -- cursor that produced the current committed state
page_count              -- pages committed in the current generation
status                  -- pending, running, complete, unsupported, blocked, failed, terminal
observed_count
attempt_count
next_retry_at
last_error_code
last_error_detail
started_at
completed_at
updated_at
```

Primary key:

```text
(repo_id, item_type, item_number, dataset)
```

The row contains progress metadata only. It never stores provider response bodies or normalized page payloads.

### Scan generation

A dataset scan generation increments when:

- a dataset is first scanned;
- the parent provider revision changes before completion;
- inventory or maintenance observes a newer parent provider revision after completion;
- an operator explicitly resets an invalid scan.

Completed datasets reopen: when a maintenance or inventory observation advances `provider_updated_at` past the revision a completed dataset was bound to, that dataset atomically increments its generation, clears its cursor, and returns to `pending` for the new parent revision. Existing rows are retained until the new generation completes and reconciles. Without this transition, activity added after initial completion would never be collected while reports still claimed completeness.

The generation identifies observations belonging to one logical complete-snapshot attempt. Restarting an affected item/dataset does not reset repository inventory, maintenance scans, or unrelated datasets.

### Pagination bounds

Cursor compare-and-swap alone cannot stop an alternating or longer cursor cycle, including cycles of progress-only or empty pages. Every scan — repository inventory, maintenance, and item datasets — enforces a per-generation maximum of 10,000 pages using its durable `page_count`. Exceeding the bound, or receiving a page whose `NextCursor` equals its input cursor without exhaustion, marks that scan `blocked`: progress is retained for diagnostics, no further provider requests are spent automatically, and recovery requires an explicit reset.

The durable block reason records what was detected, not a claim the system cannot make: an in-window detected cycle or provider-invalidated cursor blocks as `invalid_cursor`; reaching the page bound blocks as `page_bound`, which may be either a legitimately oversized scan or a cycle the bounded cursor memory could not see across restarts. Continuation past a `page_bound` block is therefore an explicit operator classification, not an automatic distinction, and each continuation grants at most one further bounded window, so a misclassified cycle costs one bounded window per explicit operator action. Tests cover multi-cursor cycles, empty-page loops, and oversized-but-valid scans separately.

## Incremental Ingestion

### One page commit API

Live and archive callers use one database operation conceptually equivalent to:

```go
type DatasetPageCommit struct {
    Parent           DomainParentRef
    ExpectedRevision int64
    Dataset          DatasetKind
    ScanGeneration   int64
    Rows              DatasetRows
    Final             bool
    Progress          *DatasetProgressAdvance // nil for live-only ingestion
}

func (d *DB) CommitDatasetPage(ctx context.Context, commit DatasetPageCommit) error
```

The operation owns all domain upsert and dataset reconciliation semantics. There are no public `PublishArchiveIssueComments`, `PublishArchiveMREvents`, or `PublishArchiveReviewThreads` methods.

When `Progress` is present, the transaction also verifies the current progress generation and input cursor, then advances `next_cursor` or marks the dataset complete. When it is absent, domain ingestion succeeds without reading or requiring archive tables.

A complete live observation may conditionally satisfy matching archive progress in the same transaction. Missing, paused, stale, or inconsistent archive progress affects only the conditional archive update and never rejects the live domain write.

### Ordinary comments

Ordinary comments are replaceable provider snapshots.

For archive page commits:

1. upsert normalized comments immediately;
2. mark each comment with the current dataset scan generation;
3. advance the durable cursor in the same transaction;
4. do not delete comments during a partial scan;
5. on the exhausted page, delete ordinary comments for that parent that were not observed in the completed generation;
6. mark the dataset complete and clear the cursor atomically with final reconciliation.

Live sync normally collects a complete comment dataset in memory and submits it as one final commit through the same API. If matching archive progress exists for the current parent revision, the commit marks it complete without another provider request.

The observation-generation field is ingestion metadata, not archive content, and is not exposed through API or reports.

### Submitted reviews and review threads

Reviews and review threads are stable-identity additive history:

1. upsert each normalized page immediately;
2. advance the durable cursor in the same transaction;
3. never delete prior records because a later page sequence omits them;
4. mark the dataset complete when the exhausted page commits.

This retains already-fetched history if a later scan is incomplete or the provider stops returning an older record.

### Parent revision changes

Every detail dataset progress row is bound to a parent domain revision.

If the expected parent revision is stale:

- reject the page commit without advancing the cursor;
- increment the affected dataset generation;
- clear only that dataset cursor;
- mark it pending for the new parent revision;
- retain already-upserted additive history;
- retain ordinary comments until a new generation completes and reconciles them.

Repository inventory and unrelated datasets keep their progress.

### No page replay after commit

The database verifies the expected input cursor before advancing progress. Progress rows persist both the next cursor and the last committed input cursor, which is what makes duplicate delivery distinguishable from unrelated stale delivery: a commit whose input cursor equals the stored `last_input_cursor` for the same generation is the already-committed page and returns a typed already-committed success without writing or advancing anything. A commit whose input cursor equals the stored `next_cursor` advances normally. Any other cursor is rejected as a typed stale-progress error without provider refetch.

## Provider Cursor Failure

Provider cursor rejection is not silently converted into a page-one restart.

On a typed invalid-cursor response:

- mark only the affected scan or dataset blocked;
- retain its last committed cursor and generation for diagnostics;
- expose the provider, repository, item, dataset, and sanitized error through status;
- consume no additional provider requests automatically for that scan.

Recovery requires an explicit progress reset. A scoped reset operation is a required deliverable of this correction — exposed through the archive CLI and API — because blocked scans are otherwise unrecoverable. Automatic unbounded page-one retries are forbidden.

The reset has two modes, and neither clears domain content or unrelated cursors:

- **restart**: a new generation for only the affected scan, cursor cleared. This is the only valid recovery for a provider-invalidated cursor or a detected cursor cycle.
- **continue**: page counter cleared, cursor and generation retained, granting a legitimately oversized scan another page-bound window from where it stopped. Continue is refused for `invalid_cursor` blocks, where the retained cursor is known bad.

Transient transport, authentication, and rate-limit failures retain the cursor and use existing retry/backoff policy.

## Archive Work and Scheduling

`internal/archive` remains responsible for policy and durable work selection:

- choose repository inventory work;
- choose maintenance scan work;
- choose pending item/dataset work;
- track coverage and completeness;
- record retries and terminal outcomes;
- expose status and report readiness.

It must not contain provider SDK request construction, provider response normalization, or duplicate domain writers.

### Request boundary

For every provider page:

1. archive selects durable work;
2. archive requests provider-host admission with declared cost;
3. the canonical provider page method executes;
4. archive releases provider admission;
5. the normalized page and cursor commit to SQLite;
6. archive selects the next page or yields.

Database work never retains provider admission. Live work registers first, cancels an active cancellable archive request, and waits only for that one request lease to release.

### Priority and budget

The existing live-floor and quadratic surplus-release policy remains:

- all live work outranks archive work;
- provider reserve is absolute;
- archive spends only released surplus above the hard live floor;
- every wire attempt, including authentication retries, is counted;
- unknown reset timing releases no archive surplus;
- archive admission is reconsidered at every page boundary.

This coordinator is shared infrastructure, not an archive scheduler dependency for live work.

## Reports

Report behavior remains in scope and should not be redesigned merely for code reduction.

Reports:

- read only SQLite;
- make no provider requests;
- run in one read-only transaction;
- include deterministic ordering and existing size limits;
- expose coverage and partial/blocked state accurately;
- never treat a partially scanned dataset as complete.

Incrementally ingested rows may be visible while collection is incomplete, but report coverage must state that the relevant repository or dataset is incomplete. A report cannot claim complete coverage until all required inventory and dataset progress rows are complete or explicitly unsupported/terminal according to product policy.

## Schema and Migration Policy

Migration `000039_historical_activity_archive` is PR-local and rewriteable. It must create the final target schema directly.

The corrected migration must:

- retain repository inventory and maintenance cursor fields;
- create generic dataset progress rows;
- omit `middleman_archive_dataset_pages` entirely;
- omit payload, page-number, byte-count, and page-reconstruction schema;
- remove repeated comments/reviews/inline status columns from archive item rows;
- add only the minimal ingestion-generation metadata required for ordinary-comment reconciliation;
- preserve local-only domain state and relationships from the previous released schema;
- drop superseded regular-backfill columns as already specified.

No migration or runtime code supports the intermediate PR-local archive schema. Developers who applied an earlier version of migration 39 must reset their local provider cache/database to the released schema baseline and rerun migrations. This is acceptable because that schema has not shipped.

Migration tests are forward-only. They verify a database at the previous released schema upgrades to the final schema, preserves local-only/domain rows, and passes SQLite integrity and foreign-key checks. Downgrade behavior is not a product contract and receives no dedicated tests.

## Cache Reset Policy

The implementation should preserve provider-derived state when the final schema can consume it directly without extra runtime paths. Otherwise it may discard and refetch that state.

It is acceptable to reset:

- PR-local archive repository state;
- archive item work rows;
- dataset progress;
- staged page payloads from the abandoned design;
- provider-derived issue, MR, comment, review, or thread cache rows when necessary for a clean migration in a resettable environment.

It is not acceptable to discard:

- workflow state;
- review drafts created in middleman;
- workspace and worktree links;
- user configuration;
- docs, messages, or Kata state;
- any other local-only record that cannot be reconstructed from a provider.

Provider-derived parent rows (issues, merge requests, comments) that protected local state references — through foreign keys or stored identifiers — must be retained or safely reattached, never deleted out from under their dependents. Before discarding any provider-derived table during migration, the implementation enumerates its foreign-key dependents and confirms none carries protected local state.

No compatibility adapter, dual read, dual write, or repair gate is added without explicit user approval.

## Error Handling

Errors remain typed and provider-scoped.

- Authentication and permission failures block the affected repository and resume after credential change.
- Rate limits preserve cursor progress and retry at the provider reset or shared budget retry time.
- Transient failures preserve progress and use bounded backoff.
- Invalid provider contracts on a page read — an echoed cursor, a malformed page, a wrong-parent item — block only the affected scan or dataset without cursor advancement; contract failures that are repository-wide (wrong repository identity, capability misdeclaration) block the repository.
- Invalid cursors block the affected scan and require explicit reset.
- Removed, moved, and inaccessible item lookups mark only the item terminal according to current product rules.
- Stale parent revisions reset only the affected dataset generation.
- Archive bookkeeping failures do not roll back or reject an otherwise valid live ingestion.

Error detail exposed through status remains sanitized.

## Concurrency and Idempotency

The design assumes duplicate scheduling and process interruption are normal.

- Repository inventory page commits use cursor compare-and-swap semantics.
- Dataset page commits use parent revision, generation, and input cursor compare-and-swap semantics.
- Stable provider identities make page upserts idempotent.
- A repeated final page cannot reconcile twice against a different generation.
- Live and archive writes serialize through SQLite transactions and revision checks, not process-local ownership assumptions.
- Restarting the daemon resumes from committed cursors.
- Partial ordinary-comment scans do not delete rows.
- Partial additive scans retain valid history.

## Required Deletions

The implementation is incomplete until these structures are gone:

### Provider layer

- `platform.ArchiveReader`
- `validatingArchiveReader`
- every `ListArchive*` method
- every `GetArchive*` method
- archive-only provider endpoint and normalization implementations
- files named `archive_client.go` or provider `archive.go` after their canonical code has moved

### Persistence layer

- `middleman_archive_dataset_pages`
- `ArchiveDatasetPage`
- `ArchiveDatasetStage`
- `GetArchiveDatasetStage`
- `CommitArchiveDatasetPage`
- `LoadArchiveDatasetPages`
- `PublishArchiveIssueComments`
- `PublishArchiveMREvents`
- `PublishArchiveReviewThreads`
- stored provider page payloads and JSON reconstruction
- payload page/record/byte accounting used only by staging

### Tests

Delete tests whose only purpose is to preserve:

- the `ArchiveReader` wrapper;
- archive-prefixed provider methods;
- staged JSON payload reconstruction;
- intermediate PR-local migration shapes;
- archive-only publication APIs;
- downgrade behavior.

Replace them with smaller behavior tests for canonical page equivalence, durable cursor advancement, incremental ingestion, revision reset, live independence, and report completeness.

## Structural Acceptance Gates

Before the correction can be declared complete, all of the following searches must have no production matches except explicit historical discussion in documentation:

```text
ArchiveReader
validatingArchiveReader
ListArchive
GetArchive
PublishArchive
middleman_archive_dataset_pages
ArchiveDatasetPage
ArchiveDatasetStage
```

Additional gates:

- `internal/archive` imports no provider SDK.
- Each provider dataset has one request/normalization implementation.
- Live whole-dataset reads are collectors over canonical page methods.
- Archive page commits write domain rows and cursor progress atomically.
- No provider response payload is persisted for later reconstruction.
- No live write requires archive state.
- No automatic invalid-cursor recovery restarts from page one.
- Reports retain existing user-visible behavior.
- Canonical readers keep provider-neutral contract validation with table-driven tests.
- Every scan enforces a per-generation page bound; exceeding it blocks the scan.
- A scoped reset for blocked scans is exposed through the archive CLI and API.

## Quantitative Acceptance Gates

Line count is not the architecture, but it prevents another helper-only consolidation from being accepted.

Baseline at `3e914c24`:

- current PR handwritten production net: approximately +8,497 lines;
- current PR total net: approximately +21,892 lines.

The corrective implementation, measured against `3e914c24` and excluding this design and its implementation plan, must:

- delete at least 2,000 net handwritten production lines;
- delete at least 4,000 net total lines after obsolete tests are removed;
- leave the PR at no more than approximately +6,500 net handwritten production lines;
- leave the PR at no more than approximately +18,000 net total lines.

If the structural gates pass but these thresholds do not, implementation stops for explicit user review rather than declaring success. Generated-file churn does not count toward the handwritten production target.

## Verification Strategy

### Provider equivalence

For each provider and dataset, tests prove that live collection and archive page consumption call the same canonical method and produce identical normalized rows.

### Cursor durability

Tests interrupt after an early page, restart the service, and prove the next provider request uses the committed cursor rather than page one. This covers inventory, maintenance, comments, reviews, and review threads.

### Transaction boundaries

Tests prove:

- domain rows and cursor advancement commit together;
- a failed transaction commits neither;
- duplicate page delivery is idempotent and distinguishable from stale delivery;
- stale revision or generation cannot advance progress;
- final ordinary-comment reconciliation occurs only at exhaustion;
- additive datasets never delete omitted history;
- a completed dataset reopens for a newer observed parent revision;
- cursor cycles and empty-page loops hit the page bound and block the scan.

### Live independence

Tests prove live sync succeeds with absent, paused, failed, or inconsistent archive state and can conditionally complete matching progress without another provider request.

### Priority and budget

Existing tests continue to prove live preemption, one-request maximum interference, provider reserve, hard live floor, per-attempt accounting, and quadratic release toward reset.

### Reports

Existing report behavior tests remain. Coverage tests prove partial dataset progress cannot be reported as complete.

### Migration

A forward migration test starts from the previous released schema, applies the final rewritten migration 39, verifies local-only/domain rows, and runs `PRAGMA integrity_check` and `PRAGMA foreign_key_check`.

### Full workflow

One full-stack test drives the user-visible workflow through the real HTTP API and SQLite: start an archive, commit multiple provider pages across a service restart, pause and resume, read status and completeness, and generate deterministic partial and complete reports.

The blocked-scan recovery path gets the same treatment: block a scan, reset it through the real API or CLI, prove a stale pre-reset page commit is rejected by the new generation, prove unrelated scan and dataset state is untouched, and resume collection from the reset scope.

### Final audit

The final review records:

- exact `origin/main...HEAD` totals by handwritten production, tests, generated files, docs, and migrations;
- exact corrective delta from `3e914c24`;
- structural search results for every deletion gate;
- the remaining largest production files and why each is necessary.

## Implementation Order

Work proceeds in vertical slices so every committed stage pairs a coherent schema with the runtime code that uses it, and so no stage needs a temporary adapter or leaves both an old and a new implementation alive.

1. Record baseline metrics and a function-by-function deletion map.
2. Rewrite migration 39 to the final schema together with the canonical ingestion operations (parent-inventory page commit and child-dataset page commit) and the compact dataset progress rows, converting the archive service and live child writers in the same slice so no committed stage depends on the superseded staging schema.
3. Introduce the canonical provider page and lookup interfaces without adapters.
4. Convert one provider at a time as a vertical slice: introduce its canonical operations, convert both its live and archive callers, add parity tests, and delete its old live and archive implementations before starting the next provider.
5. Convert ordinary comments, reviews, and threads to incremental ingestion semantics as each provider slice lands.
6. Delete payload staging, archive-only publication, and obsolete schema/types/tests.
7. Delete `ArchiveReader`, archive-prefixed provider methods, and archive provider files.
8. Add the full-stack workflow test: through the real HTTP API and SQLite, start an archive, commit multiple pages across a service restart, pause and resume, verify status and completeness, and generate deterministic partial and complete reports.
9. Verify reports, completeness, live priority, and cursor resumability.
10. Enforce structural and quantitative gates before claiming completion.

Within each slice, the old implementation is deleted before proceeding to the next dataset or provider. A temporary adapter may not be committed. If a canonical method cannot replace the old path, implementation stops to revise the design rather than adding another permanent layer.
