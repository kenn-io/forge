# Bounded Diff Context Prefetch Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan directly in the current agent, task-by-task. Never use subagent-driven development. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Proactively load syntax context for all eligible diff files with visible-file priority and at most four concurrent file loads.

**Approved spec/design:** `docs/superpowers/specs/2026-07-31-bounded-diff-context-prefetch-design.md`

**Architecture:** A plain TypeScript scheduler owns queue ordering, idle dispatch, concurrency, and cancellation. `DiffView` scopes one scheduler to the current diff, while `DiffFile` forwards it and `PierreFileDiff` registers only syntax-context work.

**Tech Stack:** TypeScript, Svelte 5 runes, Vitest through Vite+, `IntersectionObserver`, `requestIdleCallback` with a delayed timer fallback.

## Global Constraints

- Run at most four full-context file tasks concurrently.
- Foreground files inside the existing 600px observer margin outrank queued background files.
- Background work starts only from a deferred background callback.
- Reset and teardown cancel queued work and fence stale active completions while
  retaining their slots until the shared requests settle.
- A failed speculative request retries only after the file becomes foreground.
- Manual context expansion remains immediate.
- Preserve standalone `PierreFileDiff` behavior when no scheduler is supplied.

---

### Task 1: Context prefetch scheduler

**Files:**

- Create: `packages/ui/src/components/diff/diff-context-prefetch.ts`
- Create: `packages/ui/src/components/diff/diff-context-prefetch.test.ts`

**Interfaces:**

- Produces: `createDiffContextPrefetchScheduler({ concurrency, scheduleDeferred? })`
- Produces: `DiffContextPrefetchScheduler.schedule(id, priority, run)` returning a handle with `setPriority(priority)` and `cancel()`
- Produces: `DiffContextPrefetchScheduler.reset()`, `setGeneration(identity)`, and `dispose()`
- Priority values are `"foreground" | "background"`; task callbacks receive an `AbortSignal`

- [x] **Step 1: Write failing scheduler tests**

  Add focused tests whose task promises are controlled by the test. Assert that
  four tasks start, a fifth waits, the fifth starts after one settles,
  foreground work runs before queued background work, `setPriority()` promotes
  and immediately starts queued work, background registration waits for the
  injected deferred callback, reset aborts active signals while removing queued
  work, and stale completion from the prior generation retains its slot until
  settlement without mutating the new generation.

- [x] **Step 2: Run the scheduler test and verify RED**

  Run from `frontend/`: `node ../node_modules/vite-plus/bin/vp test run --project unit ../packages/ui/src/components/diff/diff-context-prefetch.test.ts`

  Expected: FAIL because `diff-context-prefetch.ts` does not exist.

- [x] **Step 3: Implement the minimal scheduler**

  Use two ordered queues, one active-task map, one scheduled-idle cancellation
  function, and a monotonically increasing generation. Foreground scheduling
  drains synchronously. Background scheduling requests one idle drain. Task
  settlement always releases its global slot, while only current-generation
  queues remain eligible to drain. `setGeneration()` is idempotent so both the
  view and mounted components can align before scheduling.

- [x] **Step 4: Run the scheduler test and verify GREEN**

  Run from `frontend/`: `node ../node_modules/vite-plus/bin/vp test run --project unit ../packages/ui/src/components/diff/diff-context-prefetch.test.ts`

  Expected: PASS with no warnings.

### Task 2: Svelte integration

**Files:**

- Modify: `packages/ui/src/components/diff/DiffView.svelte`
- Modify: `packages/ui/src/components/diff/DiffFile.svelte`
- Modify: `packages/ui/src/components/diff/PierreFileDiff.svelte`
- Modify: `packages/ui/src/components/diff/PierreFileDiff.test.ts`

**Interfaces:**

- Consumes: `DiffContextPrefetchScheduler` from Task 1
- `DiffFile` accepts optional `contextPrefetchScheduler`
- `PierreFileDiff` accepts optional `contextPrefetchScheduler`
- The registered task calls the existing syntax-context loader and uses the current `active` value to set foreground/background priority

- [x] **Step 1: Write the failing component test**

  Render an inactive sparse `PierreFileDiff` with a fake scheduler that captures
  the registered task. Assert no preview call happens at registration, invoke
  the captured task, and assert both old/new preview loads occur. Add focused
  cases proving component cleanup calls the handle's `cancel()`, an aborted
  controlled preview completion does not render or write an error, and manual
  expansion loads immediately even while the proactive task remains queued.
  This catches removal of proactive registration while retaining the existing
  offscreen no-eager-load guarantee.

- [x] **Step 2: Run the component test and verify RED**

  Run from `frontend/`: `node ../node_modules/vite-plus/bin/vp test run --project unit ../packages/ui/src/components/diff/PierreFileDiff.test.ts`

  Expected: FAIL because `PierreFileDiff` does not accept or use the scheduler.

- [x] **Step 3: Integrate the scheduler**

  Create one four-worker scheduler in `DiffView`. Define its identity as
  provider, platform host, repository path, item number, and
  `diffStore.getFilePreviewGeneration()`, align when that identity changes,
  dispose it on unmount, and pass the scheduler and identity through `DiffFile`.
  `PierreFileDiff` registers only when full syntax context is needed, promotes
  the handle when `active` changes, and cancels on dependency cleanup. Thread
  the task signal through the syntax loader and check it before every state
  write, full-context render, and error update; do not pass it into the shared
  preview-cache request. Keep the existing direct visible-load branch when no
  scheduler is present. Treat a background failure as speculative and register
  one foreground retry before using the existing terminal failure latch.

  Mock the scheduler factory in `DiffView.test.ts`, change the store's reactive
  file-preview generation through a real `createDiffStore`, and assert that the
  existing scheduler aligns. This proves the complete generation wiring rather
  than only the pure scheduler method.

- [x] **Step 4: Run component and scheduler tests and verify GREEN**

  Run from `frontend/`: `node ../node_modules/vite-plus/bin/vp test run --project unit ../packages/ui/src/components/diff/diff-context-prefetch.test.ts ../packages/ui/src/components/diff/PierreFileDiff.test.ts ../packages/ui/src/components/diff/DiffFile.test.ts ../packages/ui/src/components/diff/DiffView.test.ts`

  Expected: PASS with no warnings.

### Task 3: Full-stack context-prefetch regression

**Files:**

- Modify: `internal/testutil/diff_repo.go`
- Modify: `cmd/e2e-server/main.go`
- Modify: `frontend/tests/e2e-full/diff-view.spec.ts`

- [x] **Step 1: Add a real syntax-gap fixture revision**

  Keep the default and alternate fixture heads unchanged. Add a third revision
  with eight modified TypeScript files whose separated hunks carry syntax state,
  plus an isolated E2E endpoint that selects that revision.

- [x] **Step 2: Prove the workflow through the real HTTP API**

  Hold real file-preview responses, trigger a whitespace refresh, and assert
  unresolved work never exceeds four file tasks/eight side requests across the
  generation boundary. Release the responses, verify a distant file's two sides
  were proactively requested, then scroll to it and assert no loading placeholder
  remains.

### Task 4: Verification and commit

**Files:**

- Verify all files modified by Tasks 1 and 2.

**Interfaces:**

- Consumes the completed scheduler and Svelte integration.
- Produces a committed, locally verified change.

- [x] **Step 1: Run Svelte analysis and package checks**

  Run from the repository root:

  - `node node_modules/vite-plus/bin/vp exec -- svelte-mcp svelte-autofixer packages/ui/src/components/diff/DiffView.svelte`
  - `node node_modules/vite-plus/bin/vp exec -- svelte-mcp svelte-autofixer packages/ui/src/components/diff/DiffFile.svelte`
  - `node node_modules/vite-plus/bin/vp exec -- svelte-mcp svelte-autofixer packages/ui/src/components/diff/PierreFileDiff.svelte`
  - `node node_modules/vite-plus/bin/vp run ui-package-check`
  - `node node_modules/vite-plus/bin/vp fmt --check packages/ui/src/components/diff docs/superpowers/specs/2026-07-31-bounded-diff-context-prefetch-design.md docs/superpowers/plans/2026-07-31-bounded-diff-context-prefetch.md --no-error-on-unmatched-pattern --threads=1`
  - `node node_modules/vite-plus/bin/vp lint packages/ui/src/components/diff --no-error-on-unmatched-pattern`

- [x] **Step 2: Run the full frontend unit suite**

  Run from `frontend/`: `node ../node_modules/vite-plus/bin/vp test run --project unit`

  Expected: all tests pass.

- [x] **Step 3: Run the affected browser suite**

  Run from the repository root:
  `node node_modules/vite-plus/bin/vp run kenn-forge-frontend#test:e2e --project=chromium tests/e2e-full/diff-view.spec.ts`

  Expected: all diff-view tests pass.

- [x] **Step 4: Synchronize context and commit**

  Run the repository-local context synchronization workflow with `--commit`,
  review the staged diff, then create a conventional commit through the
  hook-enforced commit workflow. Do not amend or bypass hooks.
