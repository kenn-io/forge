# Kata List In-Place Selection Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Keep the Kata list mounted and preserve the selected row's viewport position across task selection and background row refreshes.

**Architecture:** Key the list only by daemon identity, apply expansion resets through props, and make the workspace's expansion signature structural rather than freshness-based. Anchor a visible selected row across in-place snapshot updates by measuring it before the DOM patch and compensating the scroll container afterward. Retain the explicit reveal path for routed or hidden nested selections.

**Tech Stack:** Svelte 5, TypeScript, Vite+ browser tests, vitest-browser-svelte.

## Global Constraints

- Update the task list in place by default.
- Do not add compatibility paths or persistent Kata state.
- Do not alter historical dated design documents.
- Preserve initial deep-link and nested-task reveal behavior.
- Test owned interaction behavior, not browser-library internals.

---

### Task 1: Make selection refresh structurally stable

**Files:**
- Modify: `frontend/src/lib/features/kata/KataWorkspace.test.ts`
- Modify: `frontend/src/lib/features/kata/KataWorkspace.svelte`
- Modify: `frontend/src/lib/components/kata/KataIssueList.browser.svelte.ts`
- Modify: `frontend/src/lib/components/kata/KataIssueList.svelte`

**Interfaces:**
- Consumes: `KataIssueList`, `KataCurrentView`, `KataTaskSummary`.
- Produces: regressions around the real keyed-list boundary and selected-row position, plus in-place production update behavior.

- [ ] **Step 1: Write the failing workspace regression**

  Render `KataWorkspace` with the existing fake task API, capture the `.table-body`, set a non-zero `scrollTop`, click another visible task, and await accepted detail. Assert that the `.table-body` is the same node and retains the exact scroll offset.

  In `KataIssueList.browser.svelte.ts`, render enough rows to scroll, select a visible middle row, disable native `overflow-anchor` on the scroll container, record the selected row's top coordinate, prepend a new row through `rerender`, and assert that the selected row's top coordinate is unchanged.

- [ ] **Step 2: Run the regression and verify RED**

  Run:

  - `cd frontend && node ../node_modules/vite-plus/bin/vp test run --project unit src/lib/features/kata/KataWorkspace.test.ts`
  - `cd frontend && node ../node_modules/vite-plus/bin/vp test run --project browser src/lib/components/kata/KataIssueList.browser.svelte.ts`

  Expected: the workspace test fails because selection replaces the list element, and the browser test fails because the inserted row moves the selected row.

- [ ] **Step 3: Implement the minimal structural fix**

  Remove `acceptedCurrentView.fetched_at ?? ""` from `currentExpansionSignature()` in `KataWorkspace.svelte`. Change the list key from daemon plus reset generation to daemon only, allowing the existing `resetGeneration` effect to clear expansion in place.

  Add a `$effect.pre` in `KataIssueList.svelte` that tracks snapshot-backed list props, measures an onscreen selected row before the DOM update, waits for `tick()`, and adjusts the same table body's `scrollTop` by the row-position delta. Cancel stale measurements and skip selection changes, offscreen rows, removed rows, and remounted containers.

- [ ] **Step 4: Verify GREEN and nearby behavior**

  Run:

  - `cd frontend && node ../node_modules/vite-plus/bin/vp test run --project browser src/lib/components/kata/KataIssueList.browser.svelte.ts`
  - `cd frontend && node ../node_modules/vite-plus/bin/vp test run --project unit src/lib/components/kata/KataIssueList.test.ts src/lib/features/kata/KataWorkspace.test.ts src/lib/features/kata/KataWorkspaceRouting.test.ts`

  Expected: all tests pass with no warnings.

- [ ] **Step 5: Validate Svelte and the live interaction**

  Run:

  - `vp exec -- svelte-mcp svelte-autofixer ./frontend/src/lib/features/kata/KataWorkspace.svelte --svelte-version 5`
  - `make frontend-check-no-deps`

  Then use Computer Use to select multiple local and federated rows from a scrolled position and confirm the scan line stays fixed.

- [ ] **Step 6: Commit**

  Run the repository context-sync and commit workflows, then commit the spec, plan, regression, and implementation together with a focused bug-fix message.
