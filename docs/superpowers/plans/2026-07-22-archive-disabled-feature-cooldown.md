# Archive Disabled-Feature Cooldown Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Route archive inventory, maintenance, and hydration through the existing per-repository feature cooldown while preserving retry state, unaffected archive work, and immediate recovery.

**Architecture:** Archive admission receives `db.ArchiveItemType`, reserves the syncer's existing repository-feature probe before provider-budget admission, and completes that reservation with the provider result. Feature deferral is in-memory scheduling state only: inventory keeps its cursor unchanged, hydration stays pending, and the archive worker excludes the cooled `(repo, item type)` scope while selecting other due work.

**Tech Stack:** Go, SQLite through `internal/db`, provider-neutral archive service, GitHub provider adapter, testify.

## Global Constraints

- A disabled response permits at most one background provider probe per repository feature every 24 hours.
- Issue and merge-request cooldowns are independent; a disabled scope must not block the unaffected scope.
- The existing in-memory cooldown is the only cooldown authority; do not persist a second cooldown deadline.
- Provider-budget deferral remains repository-wide and distinct from feature deferral.
- Deferred hydration retains pending progress and attempt count and is never committed as `present`.
- Restart or a successful explicit probe makes pending archive work immediately eligible.
- Never bypass git hooks and never use `--no-verify`.

---

### Task 1: Add explicit feature-aware archive admission

**Files:**
- Modify: `internal/archive/service.go`
- Modify: `internal/archive/inventory.go`
- Modify: `internal/archive/maintenance.go`
- Modify: `internal/github/feature_cooldown.go`
- Modify: `internal/github/sync.go`
- Modify: `internal/github/pages.go`
- Test: `internal/archive/service_test.go`
- Test: `internal/github/archive_lifecycle_test.go`

**Interfaces:**
- Produce `archive.FeatureDeferral{RetryAt time.Time, Detail string}`.
- Change `Admission.Admit` to accept `db.ArchiveItemType`.
- Replace `AdmissionResult.Release` with `Complete func(error) *FeatureDeferral` and add `FeatureDeferred *FeatureDeferral` for pre-call denial.

- [ ] **Step 1: Write the failing inventory integration test**

Use the real GitHub page adapter, archive service, syncer admission, and SQLite. Return a wrapped raw 410 for issue inventory and an exhausted merge-request page. Assert one issue request, successful merge-request inventory, and one new issue probe only after 24 hours.

- [ ] **Step 2: Verify RED**

```bash
go test ./internal/github -run TestArchiveDisabledIssueInventorySharesCooldownWithoutBlockingMergeRequests -shuffle=on
```

- [ ] **Step 3: Implement the admission contract**

```go
type FeatureDeferral struct {
	RetryAt time.Time
	Detail  string
}

type AdmissionResult struct {
	Allowed         bool
	RetryAt         *time.Time
	Context         context.Context
	FeatureDeferred *FeatureDeferral
	Complete        func(error) *FeatureDeferral
	Detail          string
}
```

Reserve the matching issue or merge-request feature probe before archive budgets. Release it on budget/provider-work denial, release it after non-disabled completion, and renew it after a classified disabled response.

- [ ] **Step 4: Classify archive inventory 410s**

At the start of `archiveTransportError`, map historical issue and merge-request capabilities to `githubRepositoryFeatureDisabled` before generic transport handling.

- [ ] **Step 5: Preserve inventory and maintenance cursors**

Complete admission with the page error. Convert a feature deferral to a typed `errAdmissionDeferred` wrapper before generic scan-failure recording. Pre-call deferral may skip to the unaffected scope in the same provider-host pass; a provider-attempted disabled result ends the bounded pass.

- [ ] **Step 6: Verify GREEN**

```bash
go test ./internal/archive ./internal/github -shuffle=on
```

---

### Task 2: Keep hydration deferral in memory

**Files:**
- Modify: `internal/db/types.go`
- Modify: `internal/db/queries_archive.go`
- Modify: `internal/archive/scheduler.go`
- Modify: `internal/archive/hydrate.go`
- Test: `internal/db/queries_archive_test.go`
- Test: `internal/github/archive_lifecycle_test.go`

**Interfaces:**
- Produce `db.ArchiveItemScope{RepoID int64, ItemType ArchiveItemType}`.
- Add `ClaimArchiveItemOpts.ExcludedScopes []ArchiveItemScope`.
- Hydration returns the existing typed feature-deferral error without updating dataset progress.

- [ ] **Step 1: Write the failing claim-exclusion test**

Seed an older issue and a newer merge request for one repository. Exclude the issue scope and assert `ClaimArchiveItem` selects the merge request.

- [ ] **Step 2: Verify RED**

```bash
go test ./internal/db -run TestArchiveClaimItemExcludesFeatureScope -shuffle=on
```

- [ ] **Step 3: Implement claim-time exclusion**

Build one `NOT IN` predicate per repository from `ExcludedScopes`, preserving existing due-time and stable-order semantics. In `runNextHydrationWork`, add a pre-call feature deferral to the local exclusion list and claim again. A provider-attempted disabled result remains pending and ends the current pass.

- [ ] **Step 4: Write restart and manual-recovery regressions**

After one disabled hydration response, assert lookup progress is pending with zero attempts and no `next_retry_at`. Prove a new syncer can hydrate immediately after restart, and prove a successful explicit repository probe lets the existing service hydrate immediately.

- [ ] **Step 5: Verify GREEN and race safety**

```bash
go test ./internal/db ./internal/archive ./internal/github -shuffle=on
go test -race ./internal/github -run 'TestArchiveDisabledIssueInventorySharesCooldownWithoutBlockingMergeRequests|TestArchiveDisabledIssueHydrationRecoversImmediatelyAfterRestart|TestArchiveDisabledIssueHydrationRecoversAfterManualProbe' -shuffle=on
```

---

### Task 3: Preserve retry bits for cooldown-skipped index scopes

**Files:**
- Modify: `internal/github/sync.go`
- Test: `internal/github/feature_cooldown_test.go`

**Interfaces:**
- Consume `attemptedScope`, `failedScope`, and `disabledScope` in `indexSyncRepo`.
- Clear only `attemptedScope &^ failedScope &^ disabledScope` from `failedRepos`.

- [ ] **Step 1: Write the failing retry-state regression**

Seed an issue failure bit and an active issue cooldown. Run one skipped index cycle and assert the bit remains. Advance 24 hours, run a successful probe, and assert the bit clears.

- [ ] **Step 2: Verify RED**

```bash
go test ./internal/github -run TestCooldownSkippedIssueScopePreservesRetryUntilSuccessfulProbe -shuffle=on
```

- [ ] **Step 3: Clear only successful attempted scopes**

Do not clear failure bits when recording a disabled result. Merge new failures, then clear only scopes that were actually attempted and neither failed nor became disabled.

- [ ] **Step 4: Verify GREEN**

```bash
go test ./internal/github -run 'TestCooldownSkippedIssueScopePreservesRetryUntilSuccessfulProbe|TestDisabledIssueAfterItemFailurePreservesRetryScope' -shuffle=on
```

---

### Task 4: Verify and complete the PR refinement gate

**Files:**
- Modify: `context/retries-and-backoffs.md`
- Verify: all changed Go and documentation files

- [ ] **Step 1: Record the invariant**

Document that archive lanes share the in-memory gate, preserve scan/pending lookup state, and never encode feature cooldown as provider-budget or durable item retry state.

- [ ] **Step 2: Run final local verification**

```bash
gofmt -w internal/archive internal/db internal/github
go test ./internal/db ./internal/archive ./internal/github -shuffle=on
go test -race ./internal/github -run 'TestArchiveDisabledIssueInventorySharesCooldownWithoutBlockingMergeRequests|TestArchiveDisabledIssueHydrationRecoversImmediatelyAfterRestart|TestArchiveDisabledIssueHydrationRecoversAfterManualProbe' -shuffle=on
make nilaway
scripts/context-sync --check
git diff --check
make test-short-precommit
```

- [ ] **Step 3: Commit, push, and verify exact head**

Run context-sync commit mode and the mandatory commit skill. Push without force, then prove local HEAD, origin, and PR head SHA equality with a clean worktree.

- [ ] **Step 4: Complete external gates**

Wait for all exact-head checks and exact-head roborev-ci synthesis. Inspect paginated review threads, PR metadata and mergeability, issue comments, and read-only local roborev state. Do not create a local roborev review or resolve/delete/edit GitHub comments.
