# Archive Disabled-Feature Cooldown Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Route archive inventory, maintenance, and hydration through the existing per-repository feature cooldown while preserving unaffected archive work and pending hydration state.

**Architecture:** Make archive admission aware of `db.ArchiveItemType`, reserve the syncer's existing repository-feature probe before provider-budget admission, and complete admission with the provider result. Represent cooldown as an explicit feature deferral carrying a retry deadline; inventory keeps its cursor unchanged, while hydration persists an item-level retry without incrementing attempts or committing `present`.

**Tech Stack:** Go, SQLite through `internal/db`, provider-neutral archive service, GitHub provider adapter, testify.

## Global Constraints

- A disabled response permits at most one background provider probe per repository feature every 24 hours.
- Issue and merge-request cooldowns are independent; a disabled scope must not block the unaffected scope.
- The existing in-memory cooldown is the only cooldown authority; do not add a schema migration or second store.
- Provider-budget deferral remains repository-wide and distinct from feature deferral.
- Deferred hydration retains progress and attempt count and is never committed as `present`.
- Never bypass git hooks and never use `--no-verify`.

---

### Task 1: Persist hydration deferral without recording a failed attempt

**Files:**
- Modify: `internal/db/queries_dataset_progress.go`
- Test: `internal/db/queries_archive_test.go`

**Interfaces:**
- Consumes: `ArchiveItemSyncCommit` generation identity.
- Produces: `func (d *DB) DeferArchiveItemSync(context.Context, ArchiveItemSyncCommit, time.Time) error`.

- [ ] **Step 1: Write the failing database regression**

Add `TestDeferArchiveItemSyncPreservesProgressAndAttempts` beside the existing item failure test. Use this setup before calling the proposed method:

```go
assert := assert.New(t)
require := require.New(t)
ctx := t.Context()
d := openTestDB(t)
now := archiveTestTime()
retryAt := now.Add(24 * time.Hour)
repoID := insertTestRepoWithHost(t, d, "acme", "widget", "github.com")
require.NoError(d.StartFullArchives(ctx, []int64{repoID}, now))
insertArchiveItemForTest(t, d, repoID, ArchiveItemTypeIssue, 1, now)
insertArchiveProgressForTest(
	t, d, repoID, ArchiveItemTypeIssue, 1,
	ArchiveDatasetLookup, ArchiveDatasetProgressPending,
)
progress, err := d.GetDatasetProgress(
	ctx, repoID, ArchiveItemTypeIssue, 1, ArchiveDatasetLookup,
)
require.NoError(err)
require.NoError(d.DeferArchiveItemSync(ctx, ArchiveItemSyncCommit{
	RepoID: repoID, ItemType: ArchiveItemTypeIssue, ItemNumber: 1,
	ScanGeneration: progress.ScanGeneration, Now: now,
}, retryAt))
progress, err = d.GetDatasetProgress(
	ctx, repoID, ArchiveItemTypeIssue, 1, ArchiveDatasetLookup,
)
require.NoError(err)
```

Then assert:

```go
assert.Equal(ArchiveDatasetProgressPending, progress.Status)
assert.Zero(progress.AttemptCount)
require.NotNil(progress.NextRetryAt)
assert.Equal(retryAt, progress.NextRetryAt.UTC())
```

- [ ] **Step 2: Run the test and verify RED**

```bash
go test ./internal/db -run TestDeferArchiveItemSyncPreservesProgressAndAttempts -shuffle=on
```

Expected: compile failure because `DeferArchiveItemSync` does not exist.

- [ ] **Step 3: Implement generation-fenced item deferral**

Add beside `FailArchiveItemSync`:

```go
func (d *DB) DeferArchiveItemSync(
	ctx context.Context,
	commit ArchiveItemSyncCommit,
	retryAt time.Time,
) error {
	commit.Now = canonicalUTCTime(commit.Now)
	retryAt = canonicalUTCTime(retryAt)
	_, err := d.rw.ExecContext(ctx, `
		UPDATE middleman_archive_dataset_progress
		SET next_retry_at = ?, updated_at = ?
		WHERE repo_id = ? AND item_type = ? AND item_number = ?
		  AND dataset = 'lookup' AND scan_generation = ?
		  AND status IN ('pending', 'running', 'failed')`,
		formatDatasetProgressTime(retryAt), formatDatasetProgressTime(commit.Now),
		commit.RepoID, commit.ItemType, commit.ItemNumber, commit.ScanGeneration,
	)
	if err != nil {
		return fmt.Errorf("defer archive item sync: %w", err)
	}
	return nil
}
```

Do not clear status, attempt count, or earlier failure detail.

- [ ] **Step 4: Verify GREEN and commit**

```bash
go test ./internal/db -run TestDeferArchiveItemSyncPreservesProgressAndAttempts -shuffle=on
go test ./internal/db -shuffle=on
git add internal/db/queries_dataset_progress.go internal/db/queries_archive_test.go
git commit -m "fix: preserve archive hydration while feature-gated"
```

Run context-sync commit mode and the mandatory commit skill before the commit.

---

### Task 2: Add explicit feature-aware archive admission

**Files:**
- Modify: `internal/archive/service.go`
- Modify: `internal/archive/scheduler.go`
- Modify: `internal/archive/inventory.go`
- Modify: `internal/archive/maintenance.go`
- Modify: `internal/archive/hydrate.go`
- Modify: `internal/github/feature_cooldown.go`
- Modify: `internal/github/sync.go`
- Modify: `internal/github/pages.go`
- Test: `internal/archive/service_test.go`
- Test: `internal/github/archive_lifecycle_test.go`
- Test support: `internal/github/sync_test.go`

**Interfaces:**
- Produces: `archive.FeatureDeferral{RetryAt time.Time, Detail string}`.
- Changes: `Admission.Admit(context.Context, platform.RepoRef, db.ArchiveItemType, int)`.
- Changes: `AdmissionResult` adds `FeatureDeferred *FeatureDeferral` and replaces `Release func()` with `Complete func(error) *FeatureDeferral`.
- Consumes: `DB.DeferArchiveItemSync` from Task 1.

- [ ] **Step 1: Extend `mockClient` for the real GitHub archive page adapter**

Add function fields and methods matching the private `pageClient` interface:

```go
func (m *mockClient) ListInventoryIssuesPage(
	ctx context.Context, owner, repo, sortBy, cursor, since string,
) ([]*gh.Issue, string, bool, error)

func (m *mockClient) ListInventoryPullRequestsPage(
	ctx context.Context, owner, repo, sortBy string, page int,
) ([]*gh.PullRequest, bool, error)
```

The default methods return an exhausted empty page; configured closures record calls and return canned results.

Add a mutable archive clock in `archive_lifecycle_test.go` so the service scheduler and syncer gate advance together:

```go
type archiveLifecycleClock struct{ now func() time.Time }

func (c archiveLifecycleClock) Now() time.Time { return c.now() }
```

- [ ] **Step 2: Write the failing inventory integration regression**

Add `TestArchiveDisabledIssueInventorySharesCooldownWithoutBlockingMergeRequests` in `archive_lifecycle_test.go`. Use `NewSyncer`, `syncer.clients`, `archive.NewService`, and `dbtest.Open`. Set both clocks from the same mutable `now` value:

```go
syncer.now = func() time.Time { return now }
clock := archiveLifecycleClock{now: func() time.Time { return now }}
service, err := archive.NewService(
	database, syncer.clients, syncer, syncer, nil, clock,
)
```

The issue page closure returns:

```go
fmt.Errorf("list archive issues: %w", &gh.ErrorResponse{
	Response: &http.Response{StatusCode: http.StatusGone},
	Message:  "Issues are disabled for this repo",
})
```

The merge-request page returns an exhausted empty page. Run the service three times and assert one issue request, one merge-request request, incomplete issue inventory, and complete merge-request inventory. Advance `syncer.now` by `repositoryFeatureProbeInterval`, run again, and assert a second issue request.

- [ ] **Step 3: Verify RED**

```bash
go test ./internal/github -run TestArchiveDisabledIssueInventorySharesCooldownWithoutBlockingMergeRequests -shuffle=on
```

Expected: issue inventory repeats or merge-request inventory cannot advance because archive admission has no feature scope.

- [ ] **Step 4: Define the admission contract**

In `internal/archive/service.go`:

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

type Admission interface {
	Admit(context.Context, platform.RepoRef, db.ArchiveItemType, int) (AdmissionResult, error)
}
```

Update archive admission fakes and direct admission tests mechanically: pass the correct item type, return a no-op `Complete`, and replace `Release()` with `Complete(nil)`.

- [ ] **Step 5: Return a retry time when the shared gate denies admission**

Extract the existing cooldown `beginProbe` body into `beginProbeWithRetry`, returning `(probe, due, retryAt)`. Preserve the two-result wrapper for existing lanes. Return the stored future deadline for an active cooldown and `now.Add(time.Second)` when another probe reservation is in flight.

Add `beginRepositoryFeatureProbeWithRetry` on `Syncer`.

- [ ] **Step 6: Reserve the feature probe before archive budgets**

Change `Syncer.Admit` to map `ArchiveItemTypeIssue` to `RepositoryFeatureIssues` and `ArchiveItemTypeMergeRequest` to `RepositoryFeatureMergeRequests`. Rebuild the internal `RepoRef` from the full platform reference and call `beginRepositoryFeatureProbeWithRetry` before rate, budget, and provider-work checks.

On feature denial return:

```go
archive.AdmissionResult{FeatureDeferred: &archive.FeatureDeferral{
	RetryAt: retryAt,
	Detail:  "repository feature cooldown active",
}}
```

On every later budget/provider-work denial, call `probe.release()` first.

Refactor `recordRepositoryFeatureDisabled` through a new helper that returns the exact stored deadline:

```go
func (s *Syncer) recordRepositoryFeatureDisabledUntil(
	repo RepoRef, feature string, err error,
) (time.Time, bool)
```

The existing boolean method delegates to it so all current callers retain their contract. For admitted archive work, return an idempotent completion using that exact deadline:

```go
var once sync.Once
var deferred *archive.FeatureDeferral
complete := func(cause error) *archive.FeatureDeferral {
	once.Do(func() {
		if disabled := repositoryFeatureDisabledError(repo, feature, cause); disabled != nil {
			nextProbe, _ := s.recordRepositoryFeatureDisabledUntil(repo, feature, disabled)
			deferred = &archive.FeatureDeferral{RetryAt: nextProbe, Detail: disabled.Error()}
		} else {
			probe.release()
		}
		releaseProviderRequest()
	})
	return deferred
}
```

Keep the existing archive budget context and attempt allowance unchanged.

- [ ] **Step 7: Thread completion through archive operations**

Change `Service.admit` to accept item type and return `func(error) *FeatureDeferral`. Represent a pre-call feature denial with:

```go
type featureDeferredError struct {
	FeatureDeferral
	providerAttempted bool
}

func (e *featureDeferredError) Error() string { return e.Detail }
func (e *featureDeferredError) Unwrap() error { return errAdmissionDeferred }
```

Inventory and maintenance sample preemption, call `Complete(err)`, and turn a non-nil result into `featureDeferredError{providerAttempted: true}` before generic failure recording. This preserves scan cursor and generation.

Hydration handles pre-call and post-call deferral before creating any `ArchiveLookupPresent` commit:

```go
func (s *Service) deferHydration(
	ctx context.Context, work db.ArchiveItemWork, deferred FeatureDeferral,
) error {
	err := s.db.DeferArchiveItemSync(ctx, db.ArchiveItemSyncCommit{
		RepoID: work.RepoID, ItemType: work.ItemType, ItemNumber: work.ItemNumber,
		ScanGeneration: work.ScanGeneration, Now: s.now(),
	}, deferred.RetryAt)
	if err != nil {
		return err
	}
	return errAdmissionDeferred
}
```

- [ ] **Step 8: Let unaffected inventory and maintenance scopes proceed**

Add `featureDeferredBeforeProvider(error) bool`. Bootstrap inventory continues to the other item type only for a pre-call deferral. Normal/discovery inventory keeps a local set keyed by `(repo.ID, itemType)` and asks `nextInventoryWork` for another candidate after a pre-call deferral. A provider-attempted disabled result still ends the bounded pass.

In `promptMaintenance`, retain a pre-call issue deferral, offer merge-request maintenance its turn, then return the retained deferral. Hydration needs no scheduler loop because item-level `next_retry_at` removes it from `ClaimArchiveItem` until due.

- [ ] **Step 9: Classify archive inventory 410s**

At the start of `archiveTransportError`, map `HistoricalIssues` and `HistoricalMergeRequests` to their repository feature and call `githubRepositoryFeatureDisabled` before generic transport mapping:

```go
switch capability {
case platform.ArchiveCapabilityHistoricalIssues:
	if disabled := githubRepositoryFeatureDisabled(p.host, platform.RepositoryFeatureIssues, err); disabled != nil {
		return disabled
	}
case platform.ArchiveCapabilityHistoricalMergeRequests:
	if disabled := githubRepositoryFeatureDisabled(p.host, platform.RepositoryFeatureMergeRequests, err); disabled != nil {
		return disabled
	}
}
```

- [ ] **Step 10: Verify GREEN and commit**

```bash
go test ./internal/github -run 'TestArchiveDisabledIssueInventorySharesCooldownWithoutBlockingMergeRequests|TestArchiveAdmission|TestArchivePreempted' -shuffle=on
go test ./internal/archive -shuffle=on
go test ./internal/github -shuffle=on
git add internal/archive internal/github/feature_cooldown.go internal/github/sync.go internal/github/pages.go internal/github/archive_lifecycle_test.go internal/github/sync_test.go
git commit -m "fix: share disabled-feature cooldown with archive work"
```

Run context-sync commit mode and the mandatory commit skill before the commit.

---

### Task 3: Prove disabled hydration remains pending

**Files:**
- Test: `internal/github/archive_lifecycle_test.go`

**Interfaces:**
- Consumes: feature-aware admission and `DB.DeferArchiveItemSync`.
- Produces: integration proof that disabled hydration is deferred, not successful.

- [ ] **Step 1: Write the hydration integration regression**

Add `TestArchiveDisabledIssueHydrationRemainsPendingUntilCooldownExpires`. Inventory returns one closed issue and exhausts both streams. `GetIssue` returns the same wrapped raw disabled 410. After hydration and one additional worker pass, assert:

```go
assert.Equal(db.ArchiveDatasetProgressPending, progress.Status)
assert.Zero(progress.AttemptCount)
require.NotNil(progress.NextRetryAt)
assert.Equal(now.Add(repositoryFeatureProbeInterval), progress.NextRetryAt.UTC())
assert.Equal(int32(1), hydrationCalls.Load())
```

Advance `syncer.now` by 24 hours, run again, and assert exactly one new hydration request.

- [ ] **Step 2: Run the test**

```bash
go test ./internal/github -run TestArchiveDisabledIssueHydrationRemainsPendingUntilCooldownExpires -shuffle=on
```

Expected: pass. If it fails, return to Task 2 and correct the hydration deferral boundary before continuing; do not add fallback or compatibility behavior in this task.

- [ ] **Step 3: Run focused race coverage and commit**

```bash
go test -race ./internal/github -run 'TestArchiveDisabledIssueInventorySharesCooldownWithoutBlockingMergeRequests|TestArchiveDisabledIssueHydrationRemainsPendingUntilCooldownExpires' -shuffle=on
git add internal/github/archive_lifecycle_test.go internal/archive/hydrate.go internal/github/sync.go
git commit -m "test: cover archive hydration feature deferral"
```

Run context-sync commit mode and the mandatory commit skill before the commit.

---

### Task 4: Record the invariant and run the exact-head refinement gate

**Files:**
- Modify: `context/retries-and-backoffs.md`
- Verify: all changed Go and documentation files

**Interfaces:**
- Produces: durable context and exact-head proof for PR completion.

- [ ] **Step 1: Add the durable context claim**

```markdown
Archive inventory, maintenance, and hydration share this gate; feature deferral preserves
scan cursors and pending item lookup state instead of recording provider-budget wait or
lookup success. (`internal/github/sync.go::Admit`)
```

- [ ] **Step 2: Run final local verification**

```bash
gofmt -w internal/archive/service.go internal/archive/scheduler.go internal/archive/inventory.go internal/archive/maintenance.go internal/archive/hydrate.go internal/db/queries_dataset_progress.go internal/db/queries_archive_test.go internal/github/feature_cooldown.go internal/github/sync.go internal/github/pages.go internal/github/archive_lifecycle_test.go internal/github/sync_test.go
go test ./internal/db ./internal/archive ./internal/github -shuffle=on
go test -race ./internal/github -run 'TestArchiveDisabledIssueInventorySharesCooldownWithoutBlockingMergeRequests|TestArchiveDisabledIssueHydrationRemainsPendingUntilCooldownExpires' -shuffle=on
scripts/context-sync --check
git diff --check
make test-short-precommit
```

- [ ] **Step 3: Commit context, push, and verify exact head**

```bash
git add context/retries-and-backoffs.md
git commit -m "docs: include archive work in feature cooldown policy"
git push origin HEAD:fix/disabled-feature-sync-cooldown
git rev-parse HEAD
git rev-parse origin/fix/disabled-feature-sync-cooldown
gh pr view 719 --json headRefOid --jq .headRefOid
```

Run context-sync commit mode and the mandatory commit skill before the commit. All three SHAs must match and `git status --short` must be empty.

- [ ] **Step 4: Complete the PR gate**

Wait for all exact-head checks and the exact-head roborev-ci synthesis. Inspect paginated review threads, PR metadata and mergeability, all issue comments, and read-only local roborev state. Completion requires no unresolved actionable thread and no exact-head Medium-or-higher finding. Do not create a local roborev review and do not resolve, delete, minimize, or edit any GitHub comment or thread.
