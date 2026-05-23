# Default-Branch Activity Implementation Plan

> **For agentic workers:** REQUIRED: Use superpowers:subagent-driven-development (if subagents available) or superpowers:executing-plans to implement this plan. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Show default-branch commits and clone-detected default-branch force-pushes in Activity by default, with DB-backed filtering, threading, and mobile support.

**Architecture:** Sync records branch activity from existing bare clones into SQLite, then `ListActivity` reads those rows through the existing unified SQL feed. Git operations stay on `gitclone.Manager`; persistence stays in `internal/db`; sync orchestration remains in `internal/github/sync.go`; UI renders new repo-level activity rows without pretending they are PRs or issues.

**Tech Stack:** Go, SQLite migrations, Huma/OpenAPI code generation, Svelte 5, Bun/Vitest, generated Go and TypeScript API clients.

---

## File Structure

- Create `internal/db/migrations/000025_branch_activity.up.sql` and `.down.sql`: branch commit, branch tip, and branch force-push tables plus indexes.
- Create `internal/db/queries_branch_activity.go`: branch activity persistence, tip reads/writes, retention pruning.
- Modify `internal/db/types.go`: branch commit/tip/force-push DB types and repo-level fields on `ActivityItem`.
- Modify `internal/db/queries_activity.go`: extend the Activity union, search, type filters, cursor sorting, and nullable repo-level fields.
- Modify `internal/db/queries_activity_test.go`: DB coverage for ordering, filtering, search, cursors, and retention.
- Create `internal/gitclone/branch_activity.go`: default-branch ref resolution, first-parent branch walks, ancestor checks.
- Modify `internal/gitclone/commits.go`: extend commit metadata if shared with branch walks.
- Modify `internal/gitclone/commits_test.go` or create `internal/gitclone/branch_activity_test.go`: clone-level branch activity behavior.
- Modify `internal/github/sync.go`: per-repo branch activity tracking around successful clone fetches.
- Modify focused sync tests in `internal/github/sync_test.go`: orchestration, fetch-failure semantics, branch rename behavior.
- Modify `internal/server/api_types.go` and `internal/server/huma_routes.go`: optional response fields and commit URL serialization.
- Modify `internal/server/api_test.go`: HTTP e2e coverage with real SQLite.
- Regenerate `frontend/openapi/openapi.yaml`, `internal/apiclient/spec/openapi.json`, `internal/apiclient/generated/client.gen.go`, `packages/ui/src/api/generated/schema.ts`, and `packages/ui/src/api/generated/client.ts` with `make api-generate`.
- Modify `packages/ui/src/stores/activity.svelte.ts`: new visibility filter state and URL/type handling.
- Modify `packages/ui/src/components/activityRows.ts`: support repo-level branch thread keys and commit rollups.
- Modify `packages/ui/src/components/ActivityFeed.svelte`, `ActivityThreaded.svelte`, and `MobileActivityView.svelte`: render branch commit and force-push rows.
- Modify frontend tests in `packages/ui/src/components/*.test.ts` and `packages/ui/src/stores/activity.svelte.test.ts`.

## Global Rules

- Follow TDD: write the failing test, run it and confirm the expected failure, then implement.
- Run Go tests with `-shuffle=on`; do not pass `-count=1`.
- Use Bun only for frontend commands.
- Do not hand-build `/api/v1` URLs in frontend code.
- Commit at the end of each task with a conventional commit message and a body explaining the behavioral reason.
- Do not revert unrelated user changes if the worktree becomes dirty.

---

### Task 1: DB Schema And Branch Activity Persistence

**Files:**
- Create: `internal/db/migrations/000025_branch_activity.up.sql`
- Create: `internal/db/migrations/000025_branch_activity.down.sql`
- Create: `internal/db/queries_branch_activity.go`
- Modify: `internal/db/types.go`
- Modify: `internal/db/queries_activity_test.go`

- [ ] **Step 1: Write failing migration/persistence tests**

Add tests in `internal/db/queries_activity_test.go` or a new `internal/db/queries_branch_activity_test.go`:

```go
func TestBranchActivityPersistence(t *testing.T) {
	t.Run("upserts commits and prunes outside retention", func(t *testing.T) {
		// Arrange repo, two commits inside retention, one old commit.
		// Act: UpsertBranchCommits, PruneBranchActivity.
		// Assert: duplicate SHA updates, old row pruned, recent rows remain.
	})
	t.Run("records force pushes idempotently and tracks tips", func(t *testing.T) {
		// Arrange repo and branch tip.
		// Act: UpsertBranchTip, GetBranchTip, InsertBranchForcePush twice.
		// Assert: one force-push row exists and tip is updated.
	})
	t.Run("prunes old force pushes outside retention", func(t *testing.T) {
		// Arrange one recent and one old force-push row.
		// Act: PruneBranchActivity.
		// Assert: recent row remains and old row is removed.
	})
}
```

Use `openTestDB(t)`, `insertTestRepo(t, ...)`, `require` for setup, and `Assert.New(t)` when a test has more than 3 assertions.

- [ ] **Step 2: Verify RED**

Run:

```bash
go test ./internal/db -run 'TestBranchActivityPersistence' -shuffle=on
```

Expected: fail because tables/types/query methods do not exist.

- [ ] **Step 3: Implement migration and DB methods**

Create migration tables:

```sql
CREATE TABLE middleman_branch_commits (...);
CREATE UNIQUE INDEX idx_branch_commits_repo_sha ON middleman_branch_commits(repo_id, commit_sha);
CREATE INDEX idx_branch_commits_repo_committed ON middleman_branch_commits(repo_id, committed_at DESC);
CREATE INDEX idx_branch_commits_committed ON middleman_branch_commits(committed_at DESC);

CREATE TABLE middleman_branch_tips (...);
CREATE UNIQUE INDEX idx_branch_tips_repo_branch ON middleman_branch_tips(repo_id, branch_name);

CREATE TABLE middleman_branch_force_pushes (...);
CREATE UNIQUE INDEX idx_branch_force_pushes_dedupe ON middleman_branch_force_pushes(repo_id, branch_name, before_sha, after_sha);
CREATE INDEX idx_branch_force_pushes_repo_detected ON middleman_branch_force_pushes(repo_id, detected_at DESC);
CREATE INDEX idx_branch_force_pushes_detected ON middleman_branch_force_pushes(detected_at DESC);
```

Use repository foreign keys with `ON DELETE CASCADE`. Store timestamps in UTC.

Add DB types:

```go
type BranchCommit struct { RepoID int64; BranchName, CommitSHA string; AuthorName, AuthorEmail string; AuthoredAt time.Time; CommitterName, CommitterEmail string; CommittedAt time.Time; Subject string }
type BranchTip struct { RepoID int64; BranchName, TipSHA string; ObservedAt time.Time }
type BranchForcePush struct { RepoID int64; BranchName, BeforeSHA, AfterSHA string; DetectedAt time.Time }
```

Add methods:

```go
func (d *DB) UpsertBranchCommits(ctx context.Context, commits []BranchCommit) error
func (d *DB) GetBranchTip(ctx context.Context, repoID int64, branch string) (*BranchTip, error)
func (d *DB) UpsertBranchTip(ctx context.Context, tip BranchTip) error
func (d *DB) InsertBranchForcePush(ctx context.Context, fp BranchForcePush) error
func (d *DB) PruneBranchActivity(ctx context.Context, before time.Time) error
```

- [ ] **Step 4: Verify GREEN**

Run:

```bash
go test ./internal/db -run 'TestBranchActivityPersistence' -shuffle=on
```

Expected: pass.

- [ ] **Step 5: Commit**

```bash
git add internal/db/migrations/000025_branch_activity.* internal/db/queries_branch_activity.go internal/db/types.go internal/db/queries_branch_activity_test.go internal/db/queries_activity_test.go
git commit -m "feat: persist default-branch activity" -m "Add branch commit, branch tip, and branch force-push storage so Activity can remain SQLite-backed while surfacing default-branch work."
```

---

### Task 2: Git Clone Branch Activity Operations

**Files:**
- Create: `internal/gitclone/branch_activity.go`
- Create or modify: `internal/gitclone/branch_activity_test.go` or `internal/gitclone/commits_test.go`
- Modify: `internal/gitclone/commits.go` if shared commit metadata needs author email, committer fields, or committed time.

- [ ] **Step 1: Write failing gitclone tests**

Add tests:

```go
func TestBranchActivityWalksDefaultBranchFirstParent(t *testing.T) {
	// Build main with a merge commit that has second-parent-only commits.
	// EnsureClone.
	// Call ListBranchCommitsSince(ctx, host, owner, name, "main", since, "")
	// Assert merge commit appears, second-parent commits do not.
}

func TestBranchActivityDetectsForcePush(t *testing.T) {
	// Push commit A, EnsureClone, record old tip.
	// Rewrite main and force-push commit B.
	// EnsureClone again, resolve new tip, assert IsAncestor(old, new) is false.
}

func TestResolveDefaultBranchFallsBackToOriginHEAD(t *testing.T) {
	// Create clone with origin/HEAD and call ResolveDefaultBranch with empty/stale branch.
}
```

- [ ] **Step 2: Verify RED**

Run:

```bash
go test ./internal/gitclone -run 'TestBranchActivity' -shuffle=on
```

Expected: fail because branch activity methods do not exist.

- [ ] **Step 3: Implement clone methods**

Add methods on `gitclone.Manager`:

```go
func (m *Manager) ResolveDefaultBranch(ctx context.Context, host, owner, name, preferred string) (branch string, ref string, err error)
func (m *Manager) ResolveRef(ctx context.Context, host, owner, name, ref string) (string, error)
func (m *Manager) IsAncestor(ctx context.Context, host, owner, name, ancestor, descendant string) (bool, error)
func (m *Manager) ListBranchCommitsSince(ctx context.Context, host, owner, name, ref string, since time.Time, afterSHA string) ([]Commit, error)
```

`ListBranchCommitsSince` must use `git log --first-parent`, newest first. If `afterSHA` is non-empty, walk `afterSHA..<ref>`; otherwise walk commits with `--since=<RFC3339>` on `ref`. Include raw author name/email, committer name/email, authored time, committed time, subject, and SHA.

- [ ] **Step 4: Verify GREEN**

Run:

```bash
go test ./internal/gitclone -run 'TestBranchActivity' -shuffle=on
```

Expected: pass.

- [ ] **Step 5: Commit**

```bash
git add internal/gitclone/branch_activity.go internal/gitclone/branch_activity_test.go internal/gitclone/commits.go internal/gitclone/commits_test.go
git commit -m "feat: read default-branch activity from clones" -m "Add clone-backed helpers for resolving default branches, walking first-parent commits, and detecting rewritten branch tips."
```

---

### Task 3: Activity Query Union And HTTP Shape

**Files:**
- Modify: `internal/db/types.go`
- Modify: `internal/db/queries_activity.go`
- Modify: `internal/db/queries_activity_test.go`
- Modify: `internal/server/api_types.go`
- Modify: `internal/server/huma_routes.go`
- Modify: `internal/server/api_test.go`

- [ ] **Step 1: Write failing DB activity tests**

Extend `TestListActivity` with cases:

```go
t.Run("includes branch commits and force pushes with stable cursor order", ...)
t.Run("repo filters include branch activity only for matching repos", ...)
t.Run("time window uses committed and detected timestamps", ...)
t.Run("search matches branch commit metadata and sha prefixes", ...)
t.Run("type filter can hide default branch activity", ...)
```

Include same-timestamp rows across a PR event and a branch commit, then assert deterministic `(created_at DESC, source DESC, source_id DESC)` order and no duplicate across cursor pages.

- [ ] **Step 2: Verify DB RED**

Run:

```bash
go test ./internal/db -run 'TestListActivity' -shuffle=on
```

Expected: fail because branch activity is not in the union.

- [ ] **Step 3: Extend DB activity model and query**

Add optional repo-level fields to `ActivityItem`: `BranchName`, `CommitSHA`, `BeforeSHA`, `AfterSHA`, `AuthorName`, `AuthorEmail`, `CommitterName`, `CommitterEmail`, `AuthoredAt`, `CommittedAt`, `ActivityURL`.

Extend the SQL union with:

- `source = 'bc'` for branch commits, `created_at = committed_at`, `body_preview = subject`
- `source = 'bfp'` for branch force-pushes, `created_at = detected_at`, `body_preview = before_sha || ' -> ' || after_sha`

Update search to include branch metadata and SHA fields.

- [ ] **Step 4: Verify DB GREEN**

Run:

```bash
go test ./internal/db -run 'TestListActivity|TestBranchActivityPersistence' -shuffle=on
```

Expected: pass.

- [ ] **Step 5: Write failing server e2e tests**

Add tests in `internal/server/api_test.go`:

```go
func TestAPIListActivityReturnsDefaultBranchActivity(t *testing.T) { ... }
func TestAPIListActivityCanHideDefaultBranchActivity(t *testing.T) { ... }
```

Use real SQLite and the generated Go client where possible. Assert optional fields (`BranchName`, `CommitSha`, `BeforeSha`, `AfterSha`, `CommittedAt`) and no fake PR/issue number for branch rows.

- [ ] **Step 6: Verify server RED**

Run:

```bash
go test ./internal/server -run 'TestAPIListActivityReturnsDefaultBranchActivity|TestAPIListActivityCanHideDefaultBranchActivity' -shuffle=on
```

Expected: fail because API response fields/filter behavior are missing.

- [ ] **Step 7: Implement API response shape and filtering**

Extend `activityItemResponse` with optional fields using `omitempty` where appropriate. Derive `activity_url` at serialization time for known providers using provider metadata and commit SHA. Add a query/filter mechanism so clients can hide `default_branch_commit` and `default_branch_force_push` through the existing `types` filter.

- [ ] **Step 8: Verify server GREEN**

Run:

```bash
go test ./internal/server -run 'TestAPIListActivity|TestAPIActivityReturnsUTC|TestAPIListActivityReturnsDefaultBranchActivity|TestAPIListActivityCanHideDefaultBranchActivity' -shuffle=on
```

Expected: pass.

- [ ] **Step 9: Commit**

```bash
git add internal/db/types.go internal/db/queries_activity.go internal/db/queries_activity_test.go internal/server/api_types.go internal/server/huma_routes.go internal/server/api_test.go
git commit -m "feat: expose branch activity in Activity API" -m "Merge persisted branch commits and force-pushes into the SQL-backed Activity feed with stable cursors and explicit repo-level response fields."
```

---

### Task 4: Sync Orchestration

**Files:**
- Modify: `internal/github/sync.go`
- Modify: `internal/github/sync_test.go`
- Modify: `internal/server/api_test.go` only if existing sync-backed server tests are the smallest e2e boundary.

- [ ] **Step 1: Write failing sync tests**

Add focused tests proving:

```go
func TestSyncRepoRecordsDefaultBranchCommits(t *testing.T) { ... }
func TestSyncRepoRecordsDefaultBranchForcePushBeforeUpdatingTip(t *testing.T) { ... }
func TestSyncRepoSkipsBranchActivityWhenCloneFetchFails(t *testing.T) { ... }
func TestSyncRepoDefaultBranchRenameDoesNotRecordForcePush(t *testing.T) { ... }
```

Use existing syncer test fixtures and a real temporary git remote where clone behavior matters.

- [ ] **Step 2: Verify RED**

Run:

```bash
go test ./internal/github -run 'TestSyncRepoRecordsDefaultBranch|TestSyncRepoSkipsBranchActivity|TestSyncRepoDefaultBranchRename' -shuffle=on
```

Expected: fail because sync does not call branch activity tracking.

- [ ] **Step 3: Implement sync step**

In the per-repo sync flow, after reading the previous tip and before/around `EnsureClone`, call the new clone helpers and DB methods in this order:

1. determine stored or fallback default branch
2. read previous tip when branch is known
3. fetch clone
4. resolve current branch/ref/tip
5. read previous tip if branch was only resolved after fetch
6. detect force-push with `IsAncestor`
7. list incremental or retention-window commits
8. upsert commits
9. insert force-push if needed
10. update branch tip
11. prune old branch activity

If clone fetch fails, return without branch activity writes. Keep unrelated PR/issue sync behavior unchanged.

- [ ] **Step 4: Verify GREEN**

Run:

```bash
go test ./internal/github -run 'TestSyncRepoRecordsDefaultBranch|TestSyncRepoSkipsBranchActivity|TestSyncRepoDefaultBranchRename' -shuffle=on
```

Expected: pass.

- [ ] **Step 5: Commit**

```bash
git add internal/github/sync.go internal/github/sync_test.go internal/server/api_test.go
git commit -m "feat: sync default-branch activity from clones" -m "Record first-parent default-branch commits and clone-detected force-pushes during provider-neutral sync without advancing tips on failed fetches."
```

---

### Task 5: OpenAPI And Generated Clients

**Files:**
- Modify generated: `frontend/openapi/openapi.yaml`
- Modify generated: `internal/apiclient/spec/openapi.json`
- Modify generated: `internal/apiclient/generated/client.gen.go`
- Modify generated: `packages/ui/src/api/generated/schema.ts`
- Modify generated: `packages/ui/src/api/generated/client.ts`

- [ ] **Step 1: Verify generation is needed**

Run:

```bash
make api-generate
```

Expected: generated OpenAPI/client files include optional branch activity fields on `ActivityItemResponse`.

- [ ] **Step 2: Verify generated clients compile at targeted boundary**

Run:

```bash
go test ./internal/apiclient/... -shuffle=on
cd packages/ui && bun run typecheck
```

Expected: pass. If `bun run typecheck` exposes frontend call-site type errors, fix those in Task 6 unless they block codegen commit.

- [ ] **Step 3: Commit**

```bash
git add frontend/openapi/openapi.yaml internal/apiclient/spec/openapi.json internal/apiclient/generated/client.gen.go packages/ui/src/api/generated/schema.ts packages/ui/src/api/generated/client.ts
git commit -m "chore: regenerate activity API clients" -m "Refresh OpenAPI and generated clients after adding optional default-branch activity fields."
```

---

### Task 6: Desktop And Mobile Activity UI

**Files:**
- Modify: `packages/ui/src/stores/activity.svelte.ts`
- Modify: `packages/ui/src/components/activityRows.ts`
- Modify: `packages/ui/src/components/ActivityFeed.svelte`
- Modify: `packages/ui/src/components/ActivityThreaded.svelte`
- Modify: `packages/ui/src/views/MobileActivityView.svelte`
- Modify tests: `packages/ui/src/stores/activity.svelte.test.ts`, `packages/ui/src/components/activityRows.test.ts`, `ActivityFeed.test.ts`, `ActivityThreaded.test.ts`, and mobile view tests if present or create one.

Before editing Svelte files, use the `svelte-code-writer` and `svelte-core-bestpractices` skills.

- [ ] **Step 1: Write failing store and row-helper tests**

Add tests proving:

```ts
it("includes default-branch activity by default and hides it when requested", ...)
it("builds repo branch thread keys without a PR or issue number", ...)
it("rolls up branch commit runs by repo branch and author", ...)
```

- [ ] **Step 2: Verify RED**

Run:

```bash
cd packages/ui && bun test src/stores/activity.svelte.test.ts src/components/activityRows.test.ts
```

Expected: fail because hide state and repo-level row helpers are missing.

- [ ] **Step 3: Implement store and row helpers**

Add default-branch visibility state, include branch activity in default type filters, and add URL persistence if the existing activity filter model persists hidden filters. Extend row helpers with branch thread keys based on provider, host, owner, name, and `branch_name`.

- [ ] **Step 4: Verify helper GREEN**

Run:

```bash
cd packages/ui && bun test src/stores/activity.svelte.test.ts src/components/activityRows.test.ts
```

Expected: pass.

- [ ] **Step 5: Write failing component tests**

Add tests proving:

```ts
it("renders branch commits in flat compact and table views", ...)
it("renders default-branch force-push rows", ...)
it("groups branch activity into one threaded repo branch thread", ...)
it("renders branch activity on the mobile Activity route without PR/issue numbers", ...)
```

- [ ] **Step 6: Verify component RED**

Run:

```bash
cd packages/ui && bun test src/components/ActivityFeed.test.ts src/components/ActivityThreaded.test.ts
```

Run frontend mobile tests from the package where they live:

```bash
cd packages/ui && bun test src/views/MobileActivityView.test.ts
```

Expected: fail because rendering support is missing.

- [ ] **Step 7: Implement UI rendering**

Render repo-level rows without `ItemKindChip` for PR/issue identity. Use clear labels:

- commit event: `Commit`, branch chip/text, short SHA, subject, author name, committed time
- force-push event: `Force-pushed`, branch chip/text, short before/after SHAs, detected time
- threaded header: `<branch> updates on <owner>/<repo>`

Keep text within existing compact/table layouts and avoid adding one-off styling when shared chips or existing row styles work.

- [ ] **Step 8: Verify UI GREEN**

Run:

```bash
cd packages/ui && bun test src/components/ActivityFeed.test.ts src/components/ActivityThreaded.test.ts src/views/MobileActivityView.test.ts
cd ../.. && make frontend-check
```

Expected: pass.

- [ ] **Step 9: Commit**

```bash
git add packages/ui/src/stores/activity.svelte.ts packages/ui/src/components/activityRows.ts packages/ui/src/components/ActivityFeed.svelte packages/ui/src/components/ActivityThreaded.svelte packages/ui/src/views/MobileActivityView.svelte packages/ui/src/**/*.test.ts
git commit -m "feat: render default-branch activity in the UI" -m "Show branch commits and force-pushes in desktop, threaded, and mobile Activity views with a visibility filter for hiding default-branch activity."
```

---

### Task 7: Final Verification

**Files:**
- No planned edits unless verification finds a defect.

- [ ] **Step 1: Run focused Go tests**

```bash
go test ./internal/db ./internal/gitclone ./internal/github ./internal/server -shuffle=on
```

Expected: pass.

- [ ] **Step 2: Run frontend checks**

```bash
make frontend-check
```

Expected: pass.

- [ ] **Step 3: Run API generation guard**

```bash
make api-generate
git diff --exit-code frontend/openapi/openapi.yaml internal/apiclient/spec/openapi.json internal/apiclient/generated/client.gen.go packages/ui/src/api/generated/schema.ts packages/ui/src/api/generated/client.ts
```

Expected: no generated drift.

- [ ] **Step 4: Run broad short test suite if time permits**

```bash
make test-short
```

Expected: pass.

- [ ] **Step 5: Commit fixes if verification required edits**

If any fixes were needed:

```bash
git add <changed files>
git commit -m "fix: stabilize default-branch activity" -m "Address verification findings from the branch activity implementation."
```

If no fixes were needed, do not create an empty commit.
