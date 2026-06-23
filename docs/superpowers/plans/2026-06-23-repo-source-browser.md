# Repo Source Browser Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Build a read-only repository source browser backed by middleman's local bare clone cache, with provider-aware routes, branch/tag deep links, reusable file UI, Markdown preview, and selected-file history.

**Architecture:** Use a git-spice stack with the existing `repo-file-browser` design branch as the base. Each implementation branch must be a reviewable vertical slice with its own tests, and dependent UI branches must consume generated API/client types from the backend branch instead of inventing parallel models.

**Tech Stack:** Go 1.26, Huma, SQLite, `internal/gitclone.Manager`, generated OpenAPI client, Svelte 5 runes, Vite+, Bun, `@pierre/trees`, existing Markdown utilities.

---

## Stack Shape

Use `git-spice` directly; `gs` is not available in this shell.

```text
main
└─ repo-file-browser                         # committed design docs
   └─ repo-browser-api                       # k7c7
      └─ repo-browser-route-store            # 99qr
         └─ repo-browser-shared-file-ui      # e2jb
            └─ repo-browser-sidebar          # n514
               └─ repo-browser-viewer        # 10yn
                  └─ repo-browser-history    # 5v83
                     └─ repo-browser-entry   # 9vbw
                        └─ repo-browser-final-verification # aatz
```

Create branches with:

```bash
git-spice branch create repo-browser-api --no-commit
git-spice branch create repo-browser-route-store --no-commit
git-spice branch create repo-browser-shared-file-ui --no-commit
git-spice branch create repo-browser-sidebar --no-commit
git-spice branch create repo-browser-viewer --no-commit
git-spice branch create repo-browser-history --no-commit
git-spice branch create repo-browser-entry --no-commit
git-spice branch create repo-browser-final-verification --no-commit
```

After each branch commit, run:

```bash
git-spice upstack restack --no-prompt
git-spice log short --no-prompt
```

Do not use `git rebase` directly on this stack.

## Files By Responsibility

- `internal/gitclone/repo_browser.go`: read-only Git operations for refs, tree, blobs, last-changed batches, file history, commit detail, and Markdown asset reads.
- `internal/gitclone/repo_browser_test.go`: temporary Git repository tests for those operations.
- `internal/server/repo_browser.go`: Huma handlers, provider-aware repository lookup, stable error mapping, and clone/fetch orchestration.
- `internal/server/repo_browser_test.go` or `internal/server/e2etest/repo_browser_test.go`: full-stack API plus SQLite coverage.
- `internal/server/api_types.go`: repo browser response/request wire types.
- `internal/server/huma_routes.go`: route registration for `/repo/.../browser/*` and `/host/{platform_host}/repo/.../browser/*`.
- `packages/ui/src/api/provider-routes.ts`: typed repo browser suffixes.
- `packages/ui/src/api/generated/schema.ts`, `internal/apiclient/generated/client.gen.go`: generated artifacts from `make api-generate`.
- `packages/ui/src/stores/repo-browser.svelte.ts`: repo browser store over generated API types.
- `frontend/src/lib/stores/router.svelte.ts`: route parsing/building for repo browser page.
- `packages/ui/src/components/repo-browser/`: shared source browser components.
- `packages/ui/src/components/diff/PierreFileTree.svelte`: narrow adapter extension for full repository tree entries.
- `frontend/src/lib/components/repositories/RepoSummaryCard.svelte`: `View repo` card action.
- `frontend/src/lib/components/keyboard/Palette.svelte`: contextual command palette entry.
- Existing Markdown utilities under `packages/ui/src/utils/markdown.ts` and docs helpers under `frontend/src/lib/components/docs/` should be reused rather than duplicated.

## Task 1: Backend Repo-Code API (`k7c7`, branch `repo-browser-api`)

**Files:**
- Create: `internal/gitclone/repo_browser.go`
- Create: `internal/gitclone/repo_browser_test.go`
- Create: `internal/server/repo_browser.go`
- Create: `internal/server/repo_browser_test.go`
- Modify: `internal/server/api_types.go`
- Modify: `internal/server/huma_routes.go`
- Modify: `packages/ui/src/api/provider-routes.ts`
- Generated: `internal/apiclient/generated/client.gen.go`
- Generated: `packages/ui/src/api/generated/schema.ts`

- [ ] **Step 1: Create the stack branch**

```bash
git-spice branch create repo-browser-api --no-commit
kata claim k7c7
```

Expected: current branch is `repo-browser-api`; kata claim succeeds or reports already owned by this actor.

- [ ] **Step 2: Write failing gitclone tests**

Add table-driven tests in `internal/gitclone/repo_browser_test.go` covering:

```go
func TestRepoBrowserListRefsDisambiguatesBranchAndTag(t *testing.T)
func TestRepoBrowserListTreeCapsAndIncludesTrackedDotfiles(t *testing.T)
func TestRepoBrowserReadBlobRejectsTraversalAndLargeFiles(t *testing.T)
func TestRepoBrowserLastChangedBatchCapsPaths(t *testing.T)
func TestRepoBrowserFileHistoryIsBoundedAtSelectedSHA(t *testing.T)
```

Each test should create a real temporary Git repository with `t.TempDir()` and run Git commands through the existing test helper pattern used in `internal/gitclone/*_test.go`.

- [ ] **Step 3: Run gitclone tests red**

```bash
go test ./internal/gitclone -run 'TestRepoBrowser' -shuffle=on
```

Expected: FAIL because repo browser APIs do not exist yet.

- [ ] **Step 4: Implement read-only gitclone operations**

In `internal/gitclone/repo_browser.go`, define:

```go
const (
	RepoBrowserTreeEntryLimit      = 20000
	RepoBrowserBlobSizeLimit       = 1 << 20
	RepoBrowserLastChangedBatchMax = 250
	RepoBrowserHistoryLimit        = 50
)

type RepoBrowserRefType string

const (
	RepoBrowserRefBranch RepoBrowserRefType = "branch"
	RepoBrowserRefTag    RepoBrowserRefType = "tag"
	RepoBrowserRefCommit RepoBrowserRefType = "commit"
)

type RepoBrowserRef struct {
	Type RepoBrowserRefType
	Name string
	SHA  string
}

type RepoBrowserTreeEntry struct {
	Path string
	Type string
	Size int64
}

type RepoBrowserBlob struct {
	Path       string
	SHA        string
	Size       int64
	MediaType  string
	Encoding   string
	Content    string
	Binary     bool
	TooLarge   bool
}

type RepoBrowserCommit struct {
	SHA        string
	Subject    string
	Body       string
	AuthorName string
	AuthorEmail string
	AuthoredAt time.Time
}
```

Add methods on `*Manager`:

```go
ListRepoBrowserRefs(ctx context.Context, host, owner, name, defaultBranch string) ([]RepoBrowserRef, RepoBrowserRef, error)
ListRepoBrowserTree(ctx context.Context, host, owner, name string, ref RepoBrowserRef) ([]RepoBrowserTreeEntry, bool, error)
ReadRepoBrowserBlob(ctx context.Context, host, owner, name string, ref RepoBrowserRef, path string) (RepoBrowserBlob, error)
RepoBrowserLastChanged(ctx context.Context, host, owner, name string, ref RepoBrowserRef, paths []string) (map[string]RepoBrowserCommit, error)
RepoBrowserFileHistory(ctx context.Context, host, owner, name string, ref RepoBrowserRef, path string) ([]RepoBrowserCommit, error)
RepoBrowserCommitDetail(ctx context.Context, host, owner, name string, ref RepoBrowserRef, sha string) (RepoBrowserCommit, error)
```

All methods must validate repo-relative paths, use `--` before paths, resolve refs through allowlisted refs, and return `ErrNotFound` for missing refs/paths.

- [ ] **Step 5: Run gitclone tests green**

```bash
go test ./internal/gitclone -run 'TestRepoBrowser' -shuffle=on
```

Expected: PASS.

- [ ] **Step 6: Write failing server API tests**

Add `internal/server/repo_browser_test.go` tests that seed SQLite with tracked repositories and assert through `srv.ServeHTTP`:

```go
func TestRepoBrowserRefsUsesProviderAwareRepoLookup(t *testing.T)
func TestRepoBrowserHostRouteReadsNestedRepoPath(t *testing.T)
func TestRepoBrowserBlobReturnsTypedLargeAndBinaryStates(t *testing.T)
func TestRepoBrowserRejectsUnknownRefAndUnsafePath(t *testing.T)
```

- [ ] **Step 7: Run server tests red**

```bash
go test ./internal/server -run 'TestRepoBrowser' -shuffle=on
```

Expected: FAIL because routes are missing.

- [ ] **Step 8: Add Huma route types and handlers**

Add response types in `internal/server/api_types.go` for refs, tree, blob, last-changed, history, and commit detail. Add `internal/server/repo_browser.go` with provider-aware repo lookup, clone/fetch orchestration, stable errors, and handlers. Register both default-host and host-prefixed routes in `internal/server/huma_routes.go`.

- [ ] **Step 9: Regenerate API clients**

```bash
make api-generate
```

Expected: generated Go and TypeScript clients include repo browser routes.

- [ ] **Step 10: Run backend/API verification**

```bash
go test ./internal/gitclone -run 'TestRepoBrowser' -shuffle=on
go test ./internal/server -run 'TestRepoBrowser' -shuffle=on
git diff --check
```

Expected: PASS.

- [ ] **Step 11: Commit branch**

```bash
git status --short
git add internal/gitclone/repo_browser.go internal/gitclone/repo_browser_test.go internal/server/repo_browser.go internal/server/repo_browser_test.go internal/server/api_types.go internal/server/huma_routes.go internal/apiclient/generated/client.gen.go packages/ui/src/api/generated/schema.ts packages/ui/src/api/provider-routes.ts
git commit -m "feat: add repo browser read APIs" -m "The repo source browser needs a provider-aware local-clone API foundation before UI branches can consume stable generated types. This slice keeps the surface read-only, bounded, and ref-safe so later stack branches do not invent frontend-only models."
git-spice upstack restack --no-prompt
```

## Task 2: Route And Store (`99qr`, branch `repo-browser-route-store`)

**Files:**
- Modify: `frontend/src/lib/stores/router.svelte.ts`
- Modify: `frontend/src/lib/stores/router.test.ts`
- Create: `packages/ui/src/stores/repo-browser.svelte.ts`
- Create: `packages/ui/src/stores/repo-browser.svelte.test.ts`

- [ ] **Step 1: Create branch and claim task**

```bash
git-spice branch create repo-browser-route-store --no-commit
kata claim 99qr
```

- [ ] **Step 2: Add failing route tests**

In `frontend/src/lib/stores/router.test.ts`, add tests for parsing and building repo browser URLs with provider, optional platform host, `repo_path`, `ref_type`, `ref_name`, `ref_sha`, `path`, and `view=source|preview`.

- [ ] **Step 3: Add failing store tests**

In `packages/ui/src/stores/repo-browser.svelte.test.ts`, test initial load, README auto-selection, ref switch preserving path, missing path state, and stale request protection.

- [ ] **Step 4: Run route/store tests red**

```bash
cd frontend && ../node_modules/.bin/vp test run --project unit src/lib/stores/router.test.ts ../packages/ui/src/stores/repo-browser.svelte.test.ts
```

- [ ] **Step 5: Implement route and store**

Add a repo browser route variant in `router.svelte.ts`. Add `createRepoBrowserStore` in `packages/ui/src/stores/repo-browser.svelte.ts` using generated client routes from Task 1.

- [ ] **Step 6: Run route/store tests green and commit**

```bash
cd frontend && ../node_modules/.bin/vp test run --project unit src/lib/stores/router.test.ts ../packages/ui/src/stores/repo-browser.svelte.test.ts
git diff --check
git add frontend/src/lib/stores/router.svelte.ts frontend/src/lib/stores/router.test.ts packages/ui/src/stores/repo-browser.svelte.ts packages/ui/src/stores/repo-browser.svelte.test.ts
git commit -m "feat: add repo browser route state"
git-spice upstack restack --no-prompt
```

## Task 3: Shared File UI Boundary (`e2jb`, branch `repo-browser-shared-file-ui`)

**Files:**
- Modify: `packages/ui/src/components/diff/PierreFileTree.svelte`
- Modify: `packages/ui/src/components/diff/PierreFileTree.test.ts`
- Create: `packages/ui/src/components/repo-browser/RepoSourceViewer.svelte`
- Create: `packages/ui/src/components/repo-browser/RepoSourceViewer.test.ts`
- Modify: `packages/ui/src/utils/diff-categories.ts` only if a path-only helper must be exported.

- [ ] Add tests for rendering repository tree entries without diff status.
- [ ] Extend `PierreFileTree.svelte` with a narrow prop for repository entries while preserving existing diff behavior.
- [ ] Add a read-only source viewer component that displays text blobs and typed binary/large/missing states.
- [ ] Verify:

```bash
cd frontend && ../node_modules/.bin/vp test run --project unit ../packages/ui/src/components/diff/PierreFileTree.test.ts ../packages/ui/src/components/repo-browser/RepoSourceViewer.test.ts
git diff --check
```

- [ ] Commit with `feat: reuse file UI for repo browsing`.

## Task 4: Sidebar (`n514`, branch `repo-browser-sidebar`)

**Files:**
- Create: `packages/ui/src/components/repo-browser/RepoBrowserSidebar.svelte`
- Create: `packages/ui/src/components/repo-browser/RepoBrowserSidebar.test.ts`

- [ ] Test path filter, category counts, category filters, selected path, and lazy last-changed display.
- [ ] Implement sidebar using the shared tree adapter and `diff-categories.ts`.
- [ ] Verify:

```bash
cd frontend && ../node_modules/.bin/vp test run --project unit ../packages/ui/src/components/repo-browser/RepoBrowserSidebar.test.ts
git diff --check
```

- [ ] Commit with `feat: add repo browser file sidebar`.

## Task 5: Main Browser View And Markdown (`10yn`, branch `repo-browser-viewer`)

**Files:**
- Create: `packages/ui/src/components/repo-browser/RepoBrowserView.svelte`
- Create: `packages/ui/src/components/repo-browser/RepoBrowserView.test.ts`
- Create or modify Markdown resolver helper near existing Markdown utilities.

- [ ] Test header controls, ref switch behavior, breadcrumbs, README auto-selection, source/preview toggle, repo-relative Markdown links/images, and inline error states.
- [ ] Implement layout with left sidebar, main viewer, branch/tag selector, refresh, open-on-forge, and Markdown preview.
- [ ] Verify:

```bash
cd frontend && ../node_modules/.bin/vp test run --project unit ../packages/ui/src/components/repo-browser/RepoBrowserView.test.ts
git diff --check
```

- [ ] Commit with `feat: add repo browser source view`.

## Task 6: History Rail (`5v83`, branch `repo-browser-history`)

**Files:**
- Create: `packages/ui/src/components/repo-browser/RepoBrowserHistoryRail.svelte`
- Create: `packages/ui/src/components/repo-browser/RepoBrowserHistoryRail.test.ts`
- Modify: `packages/ui/src/components/repo-browser/RepoBrowserView.svelte`

- [ ] Test collapsible rail, bounded commit list, selected commit detail, and open-on-forge links.
- [ ] Implement history rail and wire it to selected-file state.
- [ ] Verify:

```bash
cd frontend && ../node_modules/.bin/vp test run --project unit ../packages/ui/src/components/repo-browser/RepoBrowserHistoryRail.test.ts ../packages/ui/src/components/repo-browser/RepoBrowserView.test.ts
git diff --check
```

- [ ] Commit with `feat: show repo browser file history`.

## Task 7: Entry Points (`9vbw`, branch `repo-browser-entry`)

**Files:**
- Modify: `frontend/src/App.svelte`
- Modify: `frontend/src/App.test.ts`
- Modify: `frontend/src/lib/components/repositories/RepoSummaryCard.svelte`
- Modify: `frontend/src/lib/components/repositories/RepoSummaryPage.svelte`
- Modify: `frontend/src/lib/components/repositories/RepoSummaryPage.test.ts`
- Modify: `frontend/src/lib/components/keyboard/Palette.svelte`
- Modify: palette tests that own selected-context commands.

- [ ] Test repo card `View repo` navigation.
- [ ] Test command palette visibility for selected activity, PR, issue, and selected workspace worktree/project.
- [ ] Implement the app route rendering and entry point actions.
- [ ] Verify:

```bash
cd frontend && ../node_modules/.bin/vp test run --project unit src/App.test.ts src/lib/components/repositories/RepoSummaryPage.test.ts src/lib/components/keyboard/Palette.test.ts
git diff --check
```

- [ ] Commit with `feat: open repo browser from app contexts`.

## Task 8: Final Verification (`aatz`, branch `repo-browser-final-verification`)

**Files:**
- Modify tests only if gaps are found.

- [ ] Run backend affected tests:

```bash
go test ./internal/gitclone ./internal/server -run 'TestRepoBrowser' -shuffle=on
```

- [ ] Run full frontend unit suite after final frontend edits:

```bash
cd frontend && ../node_modules/.bin/vp test run --project unit
```

- [ ] Run package checks:

```bash
node node_modules/vite-plus/bin/vp run frontend-package-check
git diff --check
```

- [ ] Audit the spec success criteria against the implementation and add missing tests before closing `aatz`.
- [ ] Commit final test-only or documentation adjustments with `test: verify repo source browser stack`, or leave the branch with no commit if no gaps are found.
