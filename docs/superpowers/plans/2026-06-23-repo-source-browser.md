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
   └─ repo-browser-api                       # PR 1: k7c7
      └─ repo-browser-state-file-ui          # PR 2: 99qr + e2jb
         └─ repo-browser-main-ui             # PR 3: n514 + 10yn + 5v83
            └─ repo-browser-entry-verify     # PR 4: 9vbw + aatz
```

Create branches with:

```bash
git-spice branch create repo-browser-api --no-commit
git-spice branch create repo-browser-state-file-ui --no-commit
git-spice branch create repo-browser-main-ui --no-commit
git-spice branch create repo-browser-entry-verify --no-commit
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
- `internal/server/repo_browser.go`: Huma handlers, `repo_path`-first provider-aware repository lookup, stable error mapping, and clone/fetch orchestration.
- `internal/server/repo_browser_test.go` or `internal/server/e2etest/repo_browser_test.go`: full-stack API plus SQLite coverage.
- `internal/server/api_types.go`: repo browser response/request wire types.
- `internal/server/huma_routes.go`: route registration for `/repo/.../browser/*` and `/host/{platform_host}/repo/.../browser/*`, with required `repo_path` query parameters for repo-browser operations.
- `packages/ui/src/api/provider-routes.ts`: typed repo browser suffixes.
- `frontend/openapi/openapi.yaml`, `internal/apiclient/generated/client.gen.go`, `packages/ui/src/api/generated/schema.ts`, `packages/ui/src/api/generated/client.ts`: checked-in generated artifacts from `make api-generate`. `internal/apiclient/spec/openapi.json` may be regenerated locally but remains ignored and is not committed.
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
- Generated: `frontend/openapi/openapi.yaml`
- Generated: `internal/apiclient/generated/client.gen.go`
- Generated: `packages/ui/src/api/generated/schema.ts`
- Generated: `packages/ui/src/api/generated/client.ts`

- [ ] **Step 1: Create the stack branch**

```bash
git-spice branch create repo-browser-api --no-commit
kata claim k7c7
```

Expected: current branch is `repo-browser-api`; kata claim succeeds or reports already owned by this actor.

- [ ] **Step 2: Pin the backend contract before endpoint code**

Write the API contract in server request/response types before handlers:

- app route shape:
  `/repo/browser?provider={provider}&platform_host={host}&repo_path={repo_path}&ref_type={branch|tag|commit}&ref_name={name}&ref_sha={sha}&path={path}&view={source|preview}`
- API route shape:
  `/repo/{provider}/{owner}/{name}/browser/{operation}?repo_path={repo_path}` and
  `/host/{platform_host}/repo/{provider}/{owner}/{name}/browser/{operation}?repo_path={repo_path}`
- repository lookup key:
  `(provider, platform_host, repo_path)`; owner/name route params are display hints derived from the stored display owner/name and must not drive identity, cache keys, or clone paths for nested providers
- ref semantics:
  branch/tag `ref_name` resolves fresh per request; branch/tag `ref_sha` is a staleness token returned as `ref: { type, name?, resolvedSha, requestedSha?, stale }` on every successful repo-browser response; commit views use `ref_type=commit` plus a full 40-character `ref_sha`
- error/state contract:
  use existing camelCase problem codes for failures; put repo-browser reasons such as `clone_unavailable`, `unavailable_ref`, and `missing_path` in `details.reason`; model truncation, stale-token, binary, oversized, and unsupported-SVG cases as successful response states
- caps:
  `RepoBrowserTreeEntryLimit`, `RepoBrowserBlobSizeLimit`, `RepoBrowserLastChangedBatchMax`, `RepoBrowserHistoryLimit`
- Markdown asset contract:
  separate `asset-metadata` JSON preflight and `asset-bytes` byte routes; metadata emits commit-pinned byte URLs only for renderable assets; path/ref validation, MIME detection, unsupported SVG state with no byte URL, byte route problem envelopes for SVG/non-renderable assets, blob-size caps, conservative branch/tag metadata cache headers, immutable byte cache headers

- [ ] **Step 3: Write failing gitclone tests**

Add table-driven tests in `internal/gitclone/repo_browser_test.go` covering:

```go
func TestRepoBrowserListRefsDisambiguatesBranchAndTag(t *testing.T)
func TestRepoBrowserFetchPrunesDeletedTags(t *testing.T)
func TestRepoBrowserListTreeCapsAndIncludesTrackedDotfiles(t *testing.T)
func TestRepoBrowserReadBlobWorksWhenTreeIsTruncated(t *testing.T)
func TestRepoBrowserReadBlobRejectsTraversalAndLargeFiles(t *testing.T)
func TestRepoBrowserLastChangedBatchCapsPaths(t *testing.T)
func TestRepoBrowserFileHistoryIsBoundedAtSelectedSHA(t *testing.T)
func TestRepoBrowserResponsesIncludeRefMetadata(t *testing.T)
func TestRepoBrowserMarkdownAssetRejectsUnsafeAndOversizedPaths(t *testing.T)
func TestRepoBrowserMarkdownAssetMetadataRejectsSVG(t *testing.T)
func TestRepoBrowserMarkdownAssetBytesRejectsNonRenderableStates(t *testing.T)
```

Each test should create a real temporary Git repository with `t.TempDir()` and run Git commands through the existing test helper pattern used in `internal/gitclone/*_test.go`.
`TestRepoBrowserResponsesIncludeRefMetadata` must exercise tree, blob, history,
commit detail, asset metadata, and asset bytes responses. The asset-bytes
non-renderable test must cover SVG, oversized assets, unsafe traversal, missing
paths, and unknown/non-renderable media types with exact problem code and
`details.reason` assertions.

- [ ] **Step 4: Run gitclone tests red**

```bash
go test ./internal/gitclone -run 'TestRepoBrowser' -shuffle=on
```

Expected: FAIL because repo browser APIs do not exist yet.

- [ ] **Step 5: Implement read-only gitclone operations**

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

type RepoBrowserRepoRef struct {
	Host     string
	Owner    string
	Name     string
	RepoPath string
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
ListRepoBrowserRefs(ctx context.Context, repo RepoBrowserRepoRef, defaultBranch string) ([]RepoBrowserRef, RepoBrowserRef, error)
ListRepoBrowserTree(ctx context.Context, repo RepoBrowserRepoRef, ref RepoBrowserRef) ([]RepoBrowserTreeEntry, bool, error)
ProbeRepoBrowserREADME(ctx context.Context, repo RepoBrowserRepoRef, ref RepoBrowserRef) (string, bool, error)
ReadRepoBrowserBlob(ctx context.Context, repo RepoBrowserRepoRef, ref RepoBrowserRef, path string) (RepoBrowserBlob, error)
ReadRepoBrowserAsset(ctx context.Context, repo RepoBrowserRepoRef, ref RepoBrowserRef, path string) (RepoBrowserBlob, error)
RepoBrowserLastChanged(ctx context.Context, repo RepoBrowserRepoRef, ref RepoBrowserRef, paths []string) (map[string]RepoBrowserCommit, error)
RepoBrowserFileHistory(ctx context.Context, repo RepoBrowserRepoRef, ref RepoBrowserRef, path string) ([]RepoBrowserCommit, error)
RepoBrowserCommitDetail(ctx context.Context, repo RepoBrowserRepoRef, root RepoBrowserRef, path string, sha string) (RepoBrowserCommit, error)
```

All methods must validate repo-relative paths, use `--` before paths, resolve refs statelessly through typed ref inputs, and return typed errors for missing refs/paths. Last-changed batches must use a single bounded Git history walk per batch, not one process per path.

- [ ] **Step 6: Run gitclone tests green**

```bash
go test ./internal/gitclone -run 'TestRepoBrowser' -shuffle=on
```

Expected: PASS.

- [ ] **Step 7: Write failing server API tests**

Add `internal/server/repo_browser_test.go` tests that seed SQLite with tracked repositories and assert through `srv.ServeHTTP`:

```go
func TestRepoBrowserRefsUsesProviderAwareRepoLookup(t *testing.T)
func TestRepoBrowserHostRouteReadsNestedRepoPath(t *testing.T)
func TestRepoBrowserRoutesRequireRepoPathForNestedRepos(t *testing.T)
func TestRepoBrowserBranchSHAReportsStaleRef(t *testing.T)
func TestRepoBrowserTreeTruncationKeepsDirectBlobReadable(t *testing.T)
func TestRepoBrowserBlobReturnsTypedLargeAndBinaryStates(t *testing.T)
func TestRepoBrowserMarkdownAssetReturnsSafeMimeAndCacheHeaders(t *testing.T)
func TestRepoBrowserRejectsUnknownRefAndUnsafePath(t *testing.T)
func TestRepoBrowserContextualHeadFallsBackForForkPullRequests(t *testing.T)
```

- [ ] **Step 8: Run server tests red**

```bash
go test ./internal/server -run 'TestRepoBrowser' -shuffle=on
```

Expected: FAIL because routes are missing.

- [ ] **Step 9: Add Huma route types and handlers**

Add response types in `internal/server/api_types.go` for refs, tree, README probe, blob, Markdown asset, last-changed, history, and commit detail. Add `internal/server/repo_browser.go` with `repo_path`-first provider-aware repo lookup, clone/fetch orchestration, stable errors, and handlers. Register both default-host and host-prefixed routes in `internal/server/huma_routes.go`.

- [ ] **Step 10: Regenerate API clients**

```bash
make api-generate
```

Expected: `frontend/openapi/openapi.yaml`, `internal/apiclient/generated/client.gen.go`, `packages/ui/src/api/generated/schema.ts`, and `packages/ui/src/api/generated/client.ts` include repo browser routes. `internal/apiclient/spec/openapi.json` may be regenerated locally but stays ignored.

- [ ] **Step 11: Run backend/API verification**

```bash
go test ./internal/gitclone -run 'TestRepoBrowser' -shuffle=on
go test ./internal/server -run 'TestRepoBrowser' -shuffle=on
git diff --check
```

Expected: PASS.

- [ ] **Step 12: Commit branch**

```bash
git status --short
git add internal/gitclone/repo_browser.go internal/gitclone/repo_browser_test.go internal/server/repo_browser.go internal/server/repo_browser_test.go internal/server/api_types.go internal/server/huma_routes.go frontend/openapi/openapi.yaml internal/apiclient/generated/client.gen.go packages/ui/src/api/generated/schema.ts packages/ui/src/api/generated/client.ts packages/ui/src/api/provider-routes.ts
git commit -m "feat: add repo browser read APIs" -m "The repo source browser needs a provider-aware local-clone API foundation before UI branches can consume stable generated types. This slice keeps the surface read-only, bounded, and ref-safe so later stack branches do not invent frontend-only models."
git-spice upstack restack --no-prompt
```

## Task 2: Route, Store, And Shared File UI (`99qr`, `e2jb`, branch `repo-browser-state-file-ui`)

**Files:**
- Modify: `frontend/src/lib/stores/router.svelte.ts`
- Modify: `frontend/src/lib/stores/router.test.ts`
- Create: `packages/ui/src/stores/repo-browser.svelte.ts`
- Create: `packages/ui/src/stores/repo-browser.svelte.test.ts`
- Modify: `packages/ui/src/components/diff/PierreFileTree.svelte`
- Modify: `packages/ui/src/components/diff/PierreFileTree.test.ts`
- Create: `packages/ui/src/components/repo-browser/RepoSourceViewer.svelte`
- Create: `packages/ui/src/components/repo-browser/RepoSourceViewer.test.ts`
- Modify: `packages/ui/src/utils/diff-categories.ts` only if a path-only helper must be exported.

- [ ] **Step 1: Create branch and claim tasks**

```bash
git-spice branch create repo-browser-state-file-ui --no-commit
kata claim 99qr
kata claim e2jb
```

- [ ] **Step 2: Add failing route tests**

In `frontend/src/lib/stores/router.test.ts`, add tests for parsing and building `/repo/browser` query URLs with provider, optional platform host, `repo_path`, `ref_type`, `ref_name`, `ref_sha`, `path`, and `view=source|preview`. Include nested repo paths and slash-containing branch names.

- [ ] **Step 3: Add failing store tests**

In `packages/ui/src/stores/repo-browser.svelte.test.ts`, test initial load, README auto-selection, ref switch preserving path, conversion of `notFound`/`details.reason = "missing_path"` problem envelopes into an inline missing-path UI state, reusable ref metadata handling on tree/blob/history/commit/asset responses, tree-truncation state, generated-client route usage, and stale request protection.

- [ ] **Step 4: Add failing shared UI tests**

In `packages/ui/src/components/diff/PierreFileTree.test.ts`, test rendering repository tree entries without diff status. In `packages/ui/src/components/repo-browser/RepoSourceViewer.test.ts`, test text blobs plus successful binary/large states, and problem-envelope missing states. Category filtering is path-only in v1; the Generated category should reuse existing path heuristics from `diff-categories.ts` without adding a new `is_generated` tree-entry requirement.

- [ ] **Step 5: Run route/store/UI tests red**

```bash
cd frontend && ../node_modules/.bin/vp test run --project unit src/lib/stores/router.test.ts ../packages/ui/src/stores/repo-browser.svelte.test.ts ../packages/ui/src/components/diff/PierreFileTree.test.ts ../packages/ui/src/components/repo-browser/RepoSourceViewer.test.ts
```

- [ ] **Step 6: Implement route, store, and shared UI boundary**

Add a repo browser route variant in `router.svelte.ts`. Add `createRepoBrowserStore` in `packages/ui/src/stores/repo-browser.svelte.ts` using generated client routes from Task 1.
Extend `PierreFileTree.svelte` with a narrow prop for repository entries while preserving existing diff behavior. Add a read-only source viewer component that displays text blobs, successful binary/large states, and inline problem-envelope states for missing paths.

- [ ] **Step 7: Run route/store/UI tests green and commit**

```bash
cd frontend && ../node_modules/.bin/vp test run --project unit src/lib/stores/router.test.ts ../packages/ui/src/stores/repo-browser.svelte.test.ts ../packages/ui/src/components/diff/PierreFileTree.test.ts ../packages/ui/src/components/repo-browser/RepoSourceViewer.test.ts
git diff --check
git add frontend/src/lib/stores/router.svelte.ts frontend/src/lib/stores/router.test.ts packages/ui/src/stores/repo-browser.svelte.ts packages/ui/src/stores/repo-browser.svelte.test.ts packages/ui/src/components/diff/PierreFileTree.svelte packages/ui/src/components/diff/PierreFileTree.test.ts packages/ui/src/components/repo-browser/RepoSourceViewer.svelte packages/ui/src/components/repo-browser/RepoSourceViewer.test.ts
git commit -m "feat: add repo browser state and file UI"
git-spice upstack restack --no-prompt
```

## Task 3: Main Browser UI (`n514`, `10yn`, `5v83`, branch `repo-browser-main-ui`)

**Files:**
- Create: `packages/ui/src/components/repo-browser/RepoBrowserSidebar.svelte`
- Create: `packages/ui/src/components/repo-browser/RepoBrowserSidebar.test.ts`
- Create: `packages/ui/src/components/repo-browser/RepoBrowserView.svelte`
- Create: `packages/ui/src/components/repo-browser/RepoBrowserView.test.ts`
- Create or modify Markdown resolver helper near existing Markdown utilities.
- Create: `packages/ui/src/components/repo-browser/RepoBrowserHistoryRail.svelte`
- Create: `packages/ui/src/components/repo-browser/RepoBrowserHistoryRail.test.ts`

- [ ] **Step 1: Create branch and claim tasks**

```bash
git-spice branch create repo-browser-main-ui --no-commit
kata claim n514
kata claim 10yn
kata claim 5v83
```

- [ ] **Step 2: Add failing component tests**

Test sidebar path filter/category counts/last-changed metadata, browser header/ref switch/breadcrumbs/README/source-preview/error states, Markdown asset metadata preflight before URL emission, SVG preflight suppression with a broken-asset affordance, truncated-tree affordances, and history rail commit selection/detail behavior.

- [ ] **Step 3: Run UI tests red**

```bash
cd frontend && ../node_modules/.bin/vp test run --project unit ../packages/ui/src/components/repo-browser/RepoBrowserSidebar.test.ts ../packages/ui/src/components/repo-browser/RepoBrowserView.test.ts ../packages/ui/src/components/repo-browser/RepoBrowserHistoryRail.test.ts
```

- [ ] **Step 4: Implement sidebar, main view, Markdown preview, and history rail**

Use the store and shared file UI from Task 2. Keep this branch as the first branch where the browser can render the complete source-browsing workspace without app-level entry points. Markdown preview must call generated `asset-metadata` before emitting image URLs, only use `asset-bytes` URLs for renderable metadata states, and must not inline or directly request SVG bytes.

- [ ] **Step 5: Run UI tests green and commit**

```bash
cd frontend && ../node_modules/.bin/vp test run --project unit ../packages/ui/src/components/repo-browser/RepoBrowserSidebar.test.ts ../packages/ui/src/components/repo-browser/RepoBrowserView.test.ts ../packages/ui/src/components/repo-browser/RepoBrowserHistoryRail.test.ts
git diff --check
git add packages/ui/src/components/repo-browser
git commit -m "feat: add repo browser interface"
git-spice upstack restack --no-prompt
```

## Task 4: Entry Points And Final Verification (`9vbw`, `aatz`, branch `repo-browser-entry-verify`)

**Files:**
- Modify: `frontend/src/App.svelte`
- Modify: `frontend/src/App.test.ts`
- Modify: `frontend/src/lib/components/repositories/RepoSummaryCard.svelte`
- Modify: `frontend/src/lib/components/repositories/RepoSummaryPage.svelte`
- Modify: `frontend/src/lib/components/repositories/RepoSummaryPage.test.ts`
- Modify: `frontend/src/lib/components/keyboard/Palette.svelte`
- Modify: palette tests that own selected-context commands.
- Create: `frontend/tests/e2e-full/repo-browser.spec.ts`

- [ ] **Step 1: Create branch and claim tasks**

```bash
git-spice branch create repo-browser-entry-verify --no-commit
kata claim 9vbw
kata claim aatz
```

- [ ] **Step 2: Add failing entry point tests**

Test repo card `View repo` navigation. Test command palette visibility for selected activity, PR, issue, selected workspace worktree/project, and ambiguous workspace contexts. PR/MR and workspace commands should use contextual branches only when the branch resolves in the fetched bare clone; fork heads, synthetic provider refs, and local-only workspace commits fall back to the default branch with an inline note.

- [ ] **Step 3: Run entry point tests red**

```bash
cd frontend && ../node_modules/.bin/vp test run --project unit src/App.test.ts src/lib/components/repositories/RepoSummaryPage.test.ts src/lib/components/keyboard/Palette.test.ts
```

- [ ] **Step 4: Implement app route and entry point actions**

Wire the route to `RepoBrowserView`, add `View repo` to repository summary cards, and add the contextual command palette action.

- [ ] **Step 5: Run entry point tests green**

```bash
cd frontend && ../node_modules/.bin/vp test run --project unit src/App.test.ts src/lib/components/repositories/RepoSummaryPage.test.ts src/lib/components/keyboard/Palette.test.ts
```

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

- [ ] Run one full-stack browser/e2e smoke against the real HTTP API:

```bash
cd frontend && MIDDLEMAN_E2E_OUTPUT_FILE=../tmp/repo-browser-e2e.log node ./scripts/run-e2e-to-file.ts --project=chromium tests/e2e-full/repo-browser.spec.ts
```

Expected: seeded repository data opens from the app, renders the file tree, reads a blob, switches refs, and renders Markdown preview assets through the backend endpoint.

- [ ] Audit the spec success criteria against the implementation and add missing tests before closing `aatz`.
- [ ] Commit with `feat: open repo browser from app contexts` and include entry-point tests plus the e2e smoke, or use `test: verify repo source browser stack` only if the branch contains verification-only changes. Do not leave the git-spice branch with no commit.
