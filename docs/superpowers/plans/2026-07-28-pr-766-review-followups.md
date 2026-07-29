# PR 766 Review Follow-ups Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make terminal sessions and workspace controls remain fully operable after the workspace pane retires, and give the infrastructure-heavy Firefox pool regression an appropriate timeout budget.

**Architecture:** Reuse the existing shared detail-pane drag protocol alongside terminal-local drag state, select promotion anchors from the layout's published visible tabs, and render the live workspace control snippet in the externally hosted dock header. Keep terminal activation tied to `hostVisible` while allowing portalled controls and dialogs only when a promoted pane or external dock actually renders their owner.

**Tech Stack:** Svelte 5, TypeScript, Vite+, Testing Library, Playwright.

## Global Constraints

- Preserve all existing drag, promotion, dialog, and lifecycle assertions.
- Do not reactivate terminal renderers while their workspace host is parked.
- Render exactly one visible workspace Delete action.
- Give only the pooled three-lease Playwright test an expanded timeout.
- Run the Svelte autofixer on every modified `.svelte` file.

---

### Task 1: Visible promotion anchors

**Files:**
- Modify: `packages/ui/src/stores/paneLayout.svelte.ts`
- Test: `packages/ui/src/stores/paneLayout.svelte.test.ts`

**Interfaces:**
- Consumes: `PaneLayoutStore.paneRender()`, `lastFocusedTabKey()`, and `leafIDForTab()`.
- Produces: `promoteSessionBesideWorkspace(layout, tabKey): boolean`, with visible workspace, same-workspace promoted-session, and visible detail-pane fallback anchors.

- [ ] **Step 1: Write the failing row-only store tests**

Add cases where `onScreenTabs` excludes `workspace` but includes a same-workspace session and where only a detail tab is visible. Assert the new pane is split beside a visible leaf and malformed/no-visible states still return `false`.

- [ ] **Step 2: Run the store test and verify RED**

Run from `frontend`: `node ../node_modules/vite-plus/bin/vp test run --project unit ../packages/ui/src/stores/paneLayout.svelte.test.ts`

Expected: the row-only promotion assertions fail because the helper currently requires an on-screen workspace tab.

- [ ] **Step 3: Implement visible-anchor selection**

Parse the target session key, prefer `workspace`, then an on-screen session pane matching its workspace and host, then the last-focused on-screen tab or first on-screen detail tab. Split beside that anchor's leaf.

- [ ] **Step 4: Run the store test and verify GREEN**

Run the command from Step 2 and expect all cases to pass.

### Task 2: Dual terminal/detail drag payloads

**Files:**
- Modify: `frontend/src/lib/components/terminal/TerminalSplitTree.svelte`
- Modify: `frontend/src/lib/components/terminal/DockedTerminalPanel.svelte`
- Modify: `frontend/src/lib/components/terminal/WorkspaceTerminalView.svelte`
- Test: `frontend/src/lib/components/terminal/TerminalSplitTree.test.ts`
- Test: `frontend/src/lib/components/terminal/DockedTerminalPanel.test.ts`
- Test harness: `frontend/src/lib/components/terminal/DockedTerminalPanelTestHarness.svelte`

**Interfaces:**
- Consumes: `startTabbedPanelTabDrag`, `clearActiveTabbedPanelDrag`, `PaneLayoutStore.dragScope`, and `sessionPaneKeyFor()`.
- Produces: optional `dragScope` and `paneKeyForSession(sessionKey)` props on terminal trees/panels.

- [ ] **Step 1: Write failing leaf-header and selector drag tests**

Render each drag surface with `dragScope="detail:prs"` and a mapper returning `sessionPaneKey(...)`. Fire `dragstart`, assert `readTabbedPanelTabDrag` returns the pane key, fire `dragend`, and assert both shared and terminal-local drag states clear.

- [ ] **Step 2: Run focused component tests and verify RED**

Run from `frontend`: `node ../node_modules/vite-plus/bin/vp test run --project unit src/lib/components/terminal/TerminalSplitTree.test.ts src/lib/components/terminal/DockedTerminalPanel.test.ts`

Expected: the shared detail-pane payload is absent because terminal drag surfaces publish only runtime-session state.

- [ ] **Step 3: Publish and clear both payloads**

Add the optional scope/mapper props, publish the scoped pane key after the runtime payload, clear both states from every drag-end path, pass the props through recursive split nodes, and supply them from `WorkspaceTerminalView` to internal and external dock placements.

- [ ] **Step 4: Run focused component tests and verify GREEN**

Run the command from Step 2 and expect both test files to pass.

### Task 3: External dock controls and parked-host dialogs

**Files:**
- Modify: `frontend/src/lib/components/terminal/DockedTerminalPanel.svelte`
- Modify: `frontend/src/lib/components/terminal/WorkspaceTerminalView.svelte`
- Test: `frontend/src/lib/components/terminal/DockedTerminalPanel.test.ts`
- Test: `frontend/src/lib/components/terminal/WorkspaceTerminalView.test.ts`
- Browser test: `frontend/src/lib/components/terminal/WorkspaceTerminalView.host-visible.browser.svelte.ts`

**Interfaces:**
- Consumes: `WorkspacePaneControls`, `workspaceControls`, `workspaceStripActions`, and `controlsInPane`.
- Produces: optional `headerActions: Snippet` on `DockedTerminalPanel` and an interaction signal limited to `hostVisible` or an actually rendered promoted-pane/external-dock control owner.

- [ ] **Step 1: Write failing external ownership and dialog tests**

Assert a row-only external dock renders one `Workspace controls` trigger and one strip Delete, a promoted-pane control renders no second Delete, and Launch/Delete invoked while `hostVisible=false` opens their portalled dialogs.

- [ ] **Step 2: Run focused unit/browser tests and verify RED**

Run from `frontend`: `node ../node_modules/vite-plus/bin/vp test run --project unit src/lib/components/terminal/DockedTerminalPanel.test.ts src/lib/components/terminal/WorkspaceTerminalView.test.ts` and `node ../node_modules/vite-plus/bin/vp test run --project browser src/lib/components/terminal/WorkspaceTerminalView.host-visible.browser.svelte.ts`.

Expected: the external dock lacks controls and dialogs remain closed while the parked host is hidden.

- [ ] **Step 3: Add the external control owner and interaction visibility**

Render the optional header snippet inside dock actions. Pass a `WorkspacePaneControls` snippet with `showStripActions=true` only to the external dock. Replace launcher, rename, stop, delete, and force-delete `hostVisible` gates with an interaction signal that requires `hostVisible`, a rendered same-workspace promoted pane, or an external dock; retain `hostVisible` everywhere that controls terminal activation, resizing, shortcuts, and host-local menus.

- [ ] **Step 4: Run focused unit/browser tests and verify GREEN**

Run both commands from Step 2 and expect all cases to pass.

### Task 4: Firefox budget and end-to-end verification

**Files:**
- Modify: `frontend/tests/e2e-full/pool-options.spec.ts`
- Modify if needed for behavioral coverage: `frontend/tests/e2e-full/00-inline-workspace-continuity.spec.ts`

**Interfaces:**
- Consumes: Playwright `test.slow()` and the existing isolated-server helpers.
- Produces: unchanged pool reset assertions with a three-times timeout budget.

- [ ] **Step 1: Mark only the pooled option-combination test slow**

Call `test.slow()` inside `pooled server leases reset cleanly across option combinations`; do not alter assertions, retries, workers, or project-wide timeouts.

- [ ] **Step 2: Run Svelte analysis and frontend verification**

Run `node node_modules/vite-plus/bin/vp exec -- svelte-mcp svelte-autofixer` on each modified Svelte file, apply valid findings, then run `make frontend-check`.

- [ ] **Step 3: Run affected Firefox E2E in isolated state**

Use a fresh temporary config/data root and run the pooled option test plus the inline workspace continuity test with the Firefox real-backend project. Expect all selected tests to pass without retries.

- [ ] **Step 4: Review, commit, and push**

Inspect the complete diff, run `scripts/context-sync --check`, create a verified commit without bypassing hooks, and push the PR branch.
