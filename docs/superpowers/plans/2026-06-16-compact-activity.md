# Compact Activity Implementation Plan

> **For agentic workers:** REQUIRED: Use superpowers:subagent-driven-development (if subagents available) or superpowers:executing-plans to implement this plan. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add a persisted compact activity layout for PR and issue detail views with a shared `View` dropdown.

**Architecture:** Add one localStorage-backed rune store for the shared layout preference, a neutral dropdown component wrapping `FilterDropdown`, and a compact row mode in the shared `EventTimeline`. Keep PR-only timeline filters in the existing storage key while adding layout controls to both PR and issue detail.

**Tech Stack:** Svelte 5 runes, TypeScript, Vitest via vite-plus, Playwright e2e.

---

### Task 1: Shared Detail Activity View Store

**Files:**
- Create: `packages/ui/src/stores/detail-activity-view.svelte.ts`
- Create: `packages/ui/src/stores/detail-activity-view.svelte.test.ts`
- Modify: `packages/ui/src/Provider.svelte`
- Modify: `packages/ui/src/types.ts`

- [ ] **Step 1: Write failing store tests**

Test default mode, persisted valid value, invalid values, storage read/write failures, and live updates across consumers.

- [ ] **Step 2: Run test to verify it fails**

Run: `node node_modules/vite-plus/bin/vp test run packages/ui/src/stores/detail-activity-view.svelte.test.ts`
Expected: FAIL because the store does not exist.

- [ ] **Step 3: Implement the store and wire it into provider context**

Follow `packages/ui/src/stores/grouping.svelte.ts`: read from localStorage, store mode in `$state`, expose `getMode()` and `setMode()`, and catch storage errors.

- [ ] **Step 4: Run store tests**

Run: `node node_modules/vite-plus/bin/vp test run packages/ui/src/stores/detail-activity-view.svelte.test.ts`
Expected: PASS.

### Task 2: Detail Activity View Menu

**Files:**
- Create: `packages/ui/src/components/detail/DetailActivityViewMenu.svelte`
- Create: `packages/ui/src/components/detail/DetailActivityViewMenu.test.ts`
- Modify: `packages/ui/src/components/detail/PRTimelineFilter.svelte`

- [ ] **Step 1: Write failing menu tests**

Test the trigger label is `View`, the detail label reflects `Normal`/`Compact`, layout selections call `onViewModeChange`, PR filter sections are still rendered when provided, and issue-only usage renders only layout choices.

- [ ] **Step 2: Run menu tests to verify failure**

Run: `node node_modules/vite-plus/bin/vp test run packages/ui/src/components/detail/DetailActivityViewMenu.test.ts`
Expected: FAIL because the component does not exist.

- [ ] **Step 3: Implement menu component**

Use `FilterDropdown`; export or reuse PR filter section building as needed without changing `middleman-pr-timeline-filter`.

- [ ] **Step 4: Run menu tests**

Run: `node node_modules/vite-plus/bin/vp test run packages/ui/src/components/detail/DetailActivityViewMenu.test.ts`
Expected: PASS.

### Task 3: Compact Timeline Rows

**Files:**
- Modify: `packages/ui/src/components/detail/EventTimeline.svelte`
- Modify: `packages/ui/src/components/detail/EventTimeline.test.ts`

- [ ] **Step 1: Write failing EventTimeline tests**

Test compact comments/reviews/review comments render as one-line aligned rows, replies become separate rows, reviews show verdict, review comments show file/line context, normal mode still renders markdown cards.

- [ ] **Step 2: Run EventTimeline tests to verify failure**

Run: `node node_modules/vite-plus/bin/vp test run packages/ui/src/components/detail/EventTimeline.test.ts`
Expected: FAIL because compact mode is not implemented for comment/review cards.

- [ ] **Step 3: Implement compact row mode**

Add `displayMode` prop, compact row helpers, raw markdown preview extraction, review verdict/context extraction, and aligned grid CSS. Keep normal mode behavior unchanged.

- [ ] **Step 4: Run EventTimeline tests**

Run: `node node_modules/vite-plus/bin/vp test run packages/ui/src/components/detail/EventTimeline.test.ts`
Expected: PASS.

### Task 4: Detail Integration

**Files:**
- Modify: `packages/ui/src/components/detail/PullDetail.svelte`
- Modify: `packages/ui/src/components/detail/PullDetail.test.ts`
- Modify: `packages/ui/src/components/detail/IssueDetail.svelte`
- Modify: `packages/ui/src/components/detail/IssueDetail.test.ts`

- [ ] **Step 1: Write failing integration tests**

Test PR detail renders `View`, preserves PR filter storage, passes compact mode to timeline, and issue detail renders the layout menu.

- [ ] **Step 2: Run integration tests to verify failure**

Run: `node node_modules/vite-plus/bin/vp test run packages/ui/src/components/detail/PullDetail.test.ts packages/ui/src/components/detail/IssueDetail.test.ts`
Expected: FAIL before integration.

- [ ] **Step 3: Wire components to shared store and menu**

Read `detailActivityView.getMode()`, pass `displayMode`, call `detailActivityView.setMode()`, and keep PR filtering unchanged.

- [ ] **Step 4: Run integration tests**

Run: `node node_modules/vite-plus/bin/vp test run packages/ui/src/components/detail/PullDetail.test.ts packages/ui/src/components/detail/IssueDetail.test.ts`
Expected: PASS.

### Task 5: E2E And Verification

**Files:**
- Add or modify affected Playwright e2e under `frontend/tests/` after locating the existing detail-flow coverage.

- [ ] **Step 1: Write failing Playwright e2e**

Cover toggling compact mode, compact comment/review/review-comment rows, and persistence when navigating to another PR or issue detail.

- [ ] **Step 2: Run focused e2e to verify failure**

Run the focused Playwright command for the selected test file.
Expected: FAIL before implementation or before final selector wiring.

- [ ] **Step 3: Finish selector/test wiring**

Add stable selectors only if needed; prefer user-observable role/text selectors.

- [ ] **Step 4: Run final verification**

Run focused Vitest tests, `node node_modules/vite-plus/bin/vp run ui-package-check`, Svelte autofixer for changed `.svelte` files, and the affected Playwright e2e suite.

- [ ] **Step 5: Commit**

Commit implementation and tests with a conventional message.

### Task 6: Roborev Fix

- [ ] **Step 1: Invoke `$roborev-fix`**

Run the roborev-fix workflow requested by the user after implementation and verification.
