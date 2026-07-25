# Generalized Pane Layout Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Let PR, issue, and activity detail surfaces rearrange their panes — conversation, diff, and inline workspace — using the same splittable/draggable tree the Workspaces tab already uses, with any pane maximizable.

**Architecture:** Extend the existing surface-agnostic `tabbed-panel-layout.ts` / `TabbedPanelTree.svelte` primitive with persistence, zoom, collapse, and a flatten fallback. Add a per-surface store (`paneLayout.svelte.ts`) and one host component (`DetailPaneLayout.svelte`). Rewire the three detail surfaces onto it, deleting `WorkspaceDockPanel`, `PRListView`'s hand-rolled split, and the `dockModes` triad.

**Tech Stack:** Svelte 5 runes, TypeScript, Vitest via Vite+ (`vp test`), `@kenn-io/kit-ui`, `@lucide/svelte`.

**Design spec:** `docs/superpowers/specs/2026-07-25-generalized-pane-layout-design.md`. Read it before Task 1; it records why several obvious-looking simplifications are wrong.

**On task granularity:** Tasks 1-3 carry literal test and implementation code because they define the interfaces every later task consumes. Tasks 4-11 specify files, exact interfaces, deletions, and test gates but not literal bodies — they rewire existing components whose current shape is the real specification, and dictating their bodies here would invent details that only survive contact with the code. Anyone executing 4-11 should read the named files first.

## Global Constraints

- Never run `npm`. Use `bun install`; run tests as `./node_modules/.bin/vp test` from `frontend/`.
- Run `vp test` from `frontend/`, never from `packages/ui/` — the Svelte plugin and the `../packages/ui` include live in the frontend config.
- Never run bare `vp fmt`; format only named files.
- Browser specs (`*.browser.svelte.ts`) must live under `frontend/src`. The browser project includes only `src/**/*.browser.svelte.ts` (`frontend/vite.config.ts:214`); a browser spec beside a `packages/ui` component runs never.
- Every new `@lucide/svelte/icons/<name>` import must also be added to `optimizeDeps.include` in `frontend/vite.config.ts` (`context/testing.md`).
- No emojis in code or output. Datetimes UTC. Use testify-style `assert`/`expect` per existing suites, never `t.Fatal` equivalents.
- No backwards-compatibility shims, adapters, or fallbacks without express permission (`CLAUDE.md`). Old localStorage keys are deleted, not migrated.
- Commit after every task. Never amend. Never bypass hooks. Never change branches.

---

## File Structure

| File | Responsibility |
| --- | --- |
| `packages/ui/src/components/shared/tabbed-panel-layout.ts` (modify) | Pure tree model. Gains layout-state type, parse/serialize, availability pruning, flatten, zoom/collapse helpers. |
| `packages/ui/src/components/shared/tabbed-panel-layout.test.ts` (create) | Unit tests for all of the above. No test file exists today. |
| `packages/ui/src/components/shared/TabbedPanelTree.svelte` (modify) | Gains `leafActions` snippet, zoom rendering with divider suppression, and threaded effective visibility. |
| `packages/ui/src/stores/paneLayout.svelte.ts` (create) | Per-surface persisted layout store; owns the six zoom/focus rules. |
| `packages/ui/src/components/shared/DetailPaneLayout.svelte` (create) | Host: tab spec in, `TabbedPanelTree` out. Owns flatten threshold and the leaf icon cluster. |
| `packages/ui/src/components/shared/PaneLeafActions.svelte` (create) | The three-icon cluster (split right, split down, maximize/restore). |
| `packages/ui/src/item-workspace-claim.svelte.ts` (create) | Shared claim/release/invalidate lifecycle, extracted from the three views. |
| `packages/ui/src/views/PRListView.svelte` (modify) | Drop `.detail-split-layout`, `renderWorkspaceDock`, split-view prefs; render `DetailPaneLayout`. |
| `packages/ui/src/views/IssueListView.svelte` (modify) | Same, two tabs. |
| `packages/ui/src/views/ActivityFeedView.svelte` (modify) | Own the pane tree; stop embedding the two list views for detail. |
| `frontend/src/lib/stores/workspace-host.svelte.ts` (modify) | Delete `dockModes`; derive mode from the layout store. |
| `packages/ui/src/components/workspace/WorkspaceDockPanel.svelte` (delete) | Replaced by `DetailPaneLayout` plus its reopen strip. |
| `frontend/src/lib/components/terminal/WorkflowSplitTree.svelte` (modify) | Namespace its drag scope to `workspace:<id>` so detail panes cannot cross into it. |
| `frontend/tests/e2e-full/{00-inline-workspace-continuity,detail-action-buttons,activity-drawer}.spec.ts` (modify) | Real-backend specs asserting DOM this work deletes. |

---


### Tasks 1-2: Layout state, persistence, pruning — PARTLY DONE (897f1162b)

Implemented in `packages/ui/src/components/shared/tabbed-panel-layout.ts` with
`packages/ui/src/components/shared/tabbed-panel-layout.test.ts` (15 cases).
**The committed code is authoritative; do not re-derive it from this plan.**

Final exported surface, as landed and after roborev review:

```ts
interface TabbedPanelLayoutState {
  version: 1;
  tree: TabbedPanelNode;
  zoomedLeafID: string | null;
  hiddenTabKeys: string[];        // per-TAB, not per-leaf
  lastFocusedTabKey: string | null;
}
defaultTabbedPanelLayout(knownTabs, tree?): TabbedPanelLayoutState
parseTabbedPanelLayout(raw, knownTabs, defaultTree?): TabbedPanelLayoutState
serializeTabbedPanelLayout(state): string
pruneTabbedPanelTreeToAvailable(node, availableTabs): TabbedPanelNode | null
collectTabbedPanelLeafIDs(node): string[]
```

Three corrections from review that later tasks must respect:

- **`hiddenTabKeys` is per-tab.** A leaf-keyed collapse breaks when the workspace
  shares a leaf with the conversation: hiding it would take the conversation down
  too. Callers compute the render tree as
  `pruneTabbedPanelTreeToAvailable(tree, available.filter(t => !hidden.includes(t)))`.
- **Surfaces pass their own `defaultTree`.** The generic single-leaf default
  contradicts the intended first-run arrangement, and Activity's `commit` tab must
  be in its initial tree or it could never appear.
- **`lastFocusedTabKey` is the single surface-level winner** used both for the
  flattened leaf's active tab and for which of two visible panes the route
  follows.

Still owed from Task 2 (not yet written): `FLATTENED_TABBED_PANEL_LEAF_ID` and
`flattenTabbedPanelTree(node, preferredActiveTabKey?)`, per the spec's Responsive
fallback section. Write these with tests before Task 5 consumes them.

---

### Task 3: `leafActions` snippet, zoom rendering, threaded visibility

**Files:**
- Modify: `packages/ui/src/components/shared/TabbedPanelTree.svelte`
- Create: `frontend/src/lib/components/design-system/PaneTreeZoom.browser.svelte.ts`

**Interfaces:**
- Consumes: `zoomedLeafID` from `TabbedPanelLayoutState` — passed as a prop, not the whole state, so the component stays store-agnostic. Hidden tabs never reach this component: the caller already pruned them out of the tree it passes (they are indistinguishable from unavailable tabs here, which is the point).
- Produces new `TabbedPanelTree` props:
  - `leafActions?: Snippet<[TabbedPanelLeaf]>` — rendered once per leaf, right-aligned in the tab strip, receiving the leaf so callers get its id.
  - `zoomedLeafID?: string | null`
  - `ancestorHidden?: boolean` — internal, passed only by the recursive `<Self>` calls, default `false`.

Rendering rules:
- When `zoomedLeafID` names a leaf in this subtree, that leaf renders at full size and every sibling subtree is wrapped `hidden` + `inert` — mounted, so scroll position and the workspace slot survive.
- **Hide the `SplitResizeHandle` on every split along the path to the zoomed leaf**, and pass `disabled` to it. Hiding only the sibling child leaves ancestor dividers visible and draggable over a supposedly full-size pane, silently mutating invisible ratios. Today's dock hides its own handle for the same reason (`WorkspaceDockPanel.svelte:250`).
- **Thread effective visibility down the recursion via `ancestorHidden`.** `renderPane`'s second argument is `!ancestorHidden && node.activeTabKey === tabKey`. Computing it per leaf reports `true` for a pane hidden by an ancestor's zoom, and the inline workspace's host placement and focus read this value.
- There is no collapsed-leaf rendering here. A hidden tab is pruned upstream, so its panel is never mounted — which is exactly what makes the workspace slot unmount and reopening register a fresh slot element (see spec, Workspace pane). The reopen strip belongs to `DetailPaneLayout`, below the tree.

- [ ] **Step 1: Write the failing browser spec**

Create `frontend/src/lib/components/design-system/PaneTreeZoom.browser.svelte.ts`. Read the sibling `DesignSystemPanel.browser.svelte.ts` first for mount helpers and assertion style. Assert:
- a zoomed leaf's sibling subtree has `inert`;
- the zoomed leaf's panel fills the host (`getBoundingClientRect().width` within a pixel of the host's);
- **no `.kit-split-resize-handle` is visible** while zoomed (computed `display`, not just absence);
- `renderPane` receives `false` for a leaf's *active* tab when an ancestor's other branch holds the zoom — the regression that would silently break workspace focus.

- [ ] **Step 2: Run to verify failure**

Run from `frontend/`: `./node_modules/.bin/vp test --project browser PaneTreeZoom`
Expected: FAIL — props not supported.

- [ ] **Step 3: Implement in `TabbedPanelTree.svelte`**

Add `leafActions`, `zoomedLeafID`, and `ancestorHidden` with defaults; thread all three through both recursive `<Self>` calls. Then:
- Leaf branch: render `{#if leafActions}<div class="tabbed-panel-leaf-actions">{@render leafActions(node)}</div>{/if}` after the `{#each}` inside `.tabbed-panel-tabs`. Pass `!ancestorHidden && node.activeTabKey === tabKey` as `renderPane`'s second argument.
- Split branch: compute `zoomInFirst` / `zoomInSecond` with `collectTabbedPanelLeafIDs`. When either holds the zoom, give that child `flex: 1 1 100%`, pass `ancestorHidden={true}` to the other child while wrapping it `hidden inert`, and skip rendering the `SplitResizeHandle` entirely.
- Style: `.tabbed-panel-leaf-actions { margin-left: auto; display: inline-flex; align-items: center; padding-right: 4px; }`.

- [ ] **Step 4: Run to verify pass, plus both existing consumers**

Run from `frontend/`:
```
./node_modules/.bin/vp test --project browser PaneTreeZoom
./node_modules/.bin/vp test --project unit TabbedPanelTree
./node_modules/.bin/vp test --project browser DesignSystemPanel
```
Expected: all PASS. The last two prove the new optional props did not disturb `WorkflowSplitTree` or the design-system surface.

- [ ] **Step 5: Commit** via the `kenn:commit` skill.

---

### Task 4: `paneLayout.svelte.ts` store

**Files:**
- Create: `packages/ui/src/stores/paneLayout.svelte.ts`
- Test: `packages/ui/src/stores/paneLayout.svelte.test.ts`

**Interfaces:**
- Consumes: everything Task 1 and Task 2 produce.
- Produces:
  ```ts
  export type PaneSurfaceKey = "prs" | "issues" | "activity";
  export interface PaneLayoutStore {
    readonly surface: PaneSurfaceKey;
    readonly dragScope: string;                 // `detail:${surface}`
    renderTree(availableTabs: readonly string[]): TabbedPanelNode | null;
    zoomedLeafID(): string | null;
    hiddenTabKeys(): readonly string[];
    lastFocusedTabKey(): string | null;
    noteFocused(tabKey: string): void;
    setHidden(tabKey: string, hidden: boolean): void;
    activateTab(tabKey: string): void;
    moveTabBefore(source: string, target: string): void;
    appendTabToLeaf(source: string, leafID: string): void;
    splitTab(source: string, leafID: string, direction: TabbedPanelDirection, placement: "before" | "after"): void;
    setRatio(splitID: string, ratio: number): void;
    toggleZoom(leafID: string): void;
    clearZoom(): void;
    leafIDForTab(tabKey: string): string | null;
    reset(): void;
  }
  export function getPaneLayoutStore(surface: PaneSurfaceKey, knownTabs: readonly string[]): PaneLayoutStore;
  ```
- Persistence key: `middleman-pane-layout-v1:<surface>`, written through try/catch like `WorkspaceDockPanel`'s existing helpers.
- `toggleZoom` refuses while `getStackDepth() > 0` (import from `../stores/keyboard/modal-stack.svelte.js`).

Tests must cover, one `it` each: per-surface isolation (two surfaces do not share a key); a malformed stored value falling back to default; zoom refused while a modal is open; `clearZoom` on identity change; `setHidden` surviving a round trip, and hiding a tab that shares a leaf leaving its neighbours visible. Use `vi.stubGlobal`/`localStorage` per the existing `terminalSettingsPersistence.test.ts` conventions — read that file first.

- [ ] **Step 1** Write the failing tests described above.
- [ ] **Step 2** Run `./node_modules/.bin/vp test --project unit paneLayout` from `frontend/`. Expected FAIL.
- [ ] **Step 3** Implement the store.
- [ ] **Step 4** Run the same command. Expected PASS.
- [ ] **Step 5** Commit via the `kenn:commit` skill.

---

### Task 5: `DetailPaneLayout.svelte` and `PaneLeafActions.svelte`

**Files:**
- Create: `packages/ui/src/components/shared/DetailPaneLayout.svelte`
- Create: `packages/ui/src/components/shared/PaneLeafActions.svelte`
- Modify: `frontend/vite.config.ts` (four new icons into `optimizeDeps.include`)
- Test: `packages/ui/src/components/shared/DetailPaneLayout.test.ts`

**Interfaces:**
- Consumes: Task 3's props, Task 4's store.
- Produces:
  ```ts
  export interface PaneTabSpec {
    key: string;
    label: string;
    available: boolean;
    icon?: Snippet | undefined;
  }
  // DetailPaneLayout props
  interface Props {
    surface: PaneSurfaceKey;
    tabs: PaneTabSpec[];
    renderPane: Snippet<[string, boolean]>;   // (tabKey, effectivelyVisible)
    reopenStrip?: Snippet<[readonly string[]]> | undefined;   // hidden-but-available tabs
    flattenBelowPx?: number;                  // default 1280
    defaultTree: TabbedPanelNode;             // surface-specific first-run layout
    /** Route-bound tab, if the surface has one. Controlled: the host owns the URL. */
    routeTabKey?: string | undefined;
    /** A tab was clicked. Surfaces route this through navigate(). */
    onSelectTab?: ((tabKey: string) => void) | undefined;
    /** A visible pane took focus. Surfaces route this through replaceUrl(). */
    onPaneFocus?: ((tabKey: string) => void) | undefined;
  }
  ```
- `renderPane`'s second argument is **effective visibility**: active in its leaf AND not hidden by a zoom elsewhere AND its leaf not collapsed.
- Flatten: measure the host with `ResizeObserver` exactly as `PRListView` does today; below `flattenBelowPx`, render `flattenTabbedPanelTree(renderTree, activeTabKey)` and pass **only** `onSelectTab` — no `leafActions`, no mutation callbacks, so every structural interaction is read-only.
- Icons in `PaneLeafActions.svelte`: `square-split-horizontal` (split right, `direction: "horizontal"`, `placement: "after"`), `square-split-vertical` (split down, `"vertical"`, `"after"`), `maximize`/`minimize` (zoom toggle). Both split buttons `disabled` when `leaf.tabs.length <= 1`. Every button carries `title` and `aria-label`.

- [ ] **Step 1** Write failing jsdom tests: both split buttons disabled on a single-tab leaf; maximize label flips to Restore when the leaf is zoomed; an unavailable tab renders no panel; flattened mode renders no leaf-action cluster; and — pinning the whole point of the icon choice — clicking Split right dispatches `direction: "horizontal"` while Split down dispatches `"vertical"`, both with `placement: "after"`. Without that last assertion the suite passes with the two icons transposed.
- [ ] **Step 2** Run `./node_modules/.bin/vp test --project unit DetailPaneLayout`. Expected FAIL.
- [ ] **Step 3** Implement both components; add the four icon paths to `optimizeDeps.include`; export `DetailPaneLayout`, `PaneLeafActions`, `getPaneLayoutStore`, and the `PaneTabSpec` / `PaneSurfaceKey` / `PaneLayoutStore` types from `packages/ui/src/index.ts`. Without the barrel export the frontend cannot import them and the build fails.
- [ ] **Step 4** Run the same command. Expected PASS.
- [ ] **Step 5** Commit via the `kenn:commit` skill.

---

### Task 6: Shared item-workspace claim lifecycle

**Files:**
- Create: `packages/ui/src/item-workspace-claim.svelte.ts`
- Modify: `packages/ui/src/views/PRListView.svelte:294-321`, `packages/ui/src/views/IssueListView.svelte:89-116`
- Test: `packages/ui/src/item-workspace-claim.svelte.test.ts`

Extract the three effects currently duplicated in both views (claim-or-release on detail match, release on unmount, refetch on identity invalidation) into one function taking accessor callbacks. Behavior must not change; the two views' existing suites are the regression gate.

- [ ] **Step 1** Write tests asserting: claim when the detail matches, release when it does not, release on teardown, and refetch fired only for a matching invalidated identity.
- [ ] **Step 2** Run `./node_modules/.bin/vp test --project unit item-workspace-claim`. Expected FAIL.
- [ ] **Step 3** Implement, then replace both views' effects with calls to it.
- [ ] **Step 4** Run `./node_modules/.bin/vp test --project unit "PRListView|IssueListView|item-workspace-claim"`. Expected PASS with no changes to the view suites.
- [ ] **Step 5** Commit via the `kenn:commit` skill.

---

### Task 7: Rewire `PRListView`

**Files:**
- Modify: `packages/ui/src/views/PRListView.svelte`

Delete: the `DetailTab` split-view state (`splitViewEnabled`, `committedSplitRatio`, `dragSplitWidth`, `splitResizeStartWidth`), `loadSplitViewPreference`, `loadSplitViewRatio`, `setSplitViewEnabled`, `clampSplitRatio`, `splitPaneBounds`, `clampSplitPaneWidth`, the three resize handlers, the `.detail-tabs` markup and `.detail-split-*` styles, the "Split view" toggle, the `renderWorkspaceDock` prop, and the `pr-detail-split-view` / `pr-detail-split-ratio` keys.

Add: one `DetailPaneLayout surface="prs"` with tabs `conversation` (always available), `files` (always available), `workspace` (available when `inlineWorkspace` holds a claim for `claimIdentity`). Keep `filesScrollPositions` and the `{#key}` guard around `DiffFilesLayout` exactly as they are — the diff pane must keep its scroll memory. Route binding per the spec's Route and active tab section: a tab click keeps `navigate()`; a focus-derived change while split apart uses `replaceUrl()`.

- [x] **Step 1** Update `PRListView.test.ts` and `PRListView.workspaceDraft.test.ts` for the new structure; add a case asserting the diff pane keeps its scroll offset across a tab switch.
- [x] **Step 2** Run `./node_modules/.bin/vp test --project unit PRListView`. Expected FAIL.
- [x] **Step 3** Implement.
- [x] **Step 4** Run `./node_modules/.bin/vp test --project unit PRListView`, then the App browser specs that assert detail-tab chrome: `./node_modules/.bin/vp test --project browser "detail-code-wrap|palette-pr-detail-commands|navigation"`. Expected PASS.
- [x] **Step 5** Commit via the `kenn:commit` skill.

---

### Task 8: Rewire `IssueListView`

**Files:**
- Modify: `packages/ui/src/views/IssueListView.svelte`

Delete the `renderWorkspaceDock` prop, the `WorkspaceDockPanel` import and usage, and the `.detail-host` wrapper that exists only to host it. Add one `DetailPaneLayout surface="issues"` with two tabs: `conversation` (always available, rendering the existing `IssueDetail` with its current props) and `workspace` (available when `inlineWorkspace` holds a claim for `claimIdentity`). There is no diff pane and no route-bound tab here, so no `navigate()`/`replaceUrl()` handling is needed — issues have a single detail route. Keep the `CollapsibleSidebar`, the `IssueList` sidebar snippet, and the "Select an issue" placeholder branch exactly as they are.

- [x] **Step 1** Update `IssueListView.test.ts`.
- [x] **Step 2** Run `./node_modules/.bin/vp test --project unit IssueListView`. Expected FAIL.
- [x] **Step 3** Implement.
- [x] **Step 4** Run the same command. Expected PASS.
- [x] **Step 5** Commit via the `kenn:commit` skill.

---

### Task 9: Derive dock mode; delete `WorkspaceDockPanel`

Highest-risk task. Read the spec's Zoom and Workspace pane sections and every comment in the two files below before writing code.

**Files:**
- Modify: `frontend/src/lib/stores/workspace-host.svelte.ts`
- Delete: `packages/ui/src/components/workspace/WorkspaceDockPanel.svelte`, `WorkspaceDockPanel.test.ts`, `WorkspaceDockPanelTestController.svelte.ts`
- Move: the behavioral cases in `WorkspaceDockPanel.browser.svelte.ts` onto the new layout, keeping the file name
- Test: `frontend/src/lib/stores/workspace-host.test.ts`

Replace `dockModes` with derivation from the surface's `PaneLayoutStore`:
- `expanded` — the workspace tab's leaf is `zoomedLeafID`
- `collapsed` — `"workspace"` is in `hiddenTabKeys`, or the workspace tab is absent from the render tree
- `split` — otherwise

`setDockMode` maps onto `toggleZoom` / `setHidden`. `focusTerminal` keeps its total-no-op modal guard and its best-effort-then-pending-flag sequence unchanged.

All six behaviors from the spec's Zoom section need a test. The two that a tree-derived rewrite most easily breaks, and which `workspace-host.test.ts` already covers today, are: a same-identity claim re-assert must not un-zoom (`:543`), and a collapsed dock keeps its claim (`:476`).

- [x] **Step 1** Extend `workspace-host.test.ts` with one case per behavior, keeping the two existing cases above passing unchanged.
- [x] **Step 2** Run `./node_modules/.bin/vp test --project unit workspace-host`. Expected FAIL.
- [x] **Step 3** Implement; delete the three files.
- [x] **Step 4** Run `./node_modules/.bin/vp test --project unit workspace-host` and `--project browser WorkspaceDockPanel`. Expected PASS.
- [x] **Step 5** Commit via the `kenn:commit` skill.

---

### Task 10: Restructure `ActivityFeedView`

**Files:**
- Modify: `packages/ui/src/views/ActivityFeedView.svelte`
- Create: `packages/ui/src/components/detail/PullDetailPane.svelte`

Stop embedding `PRListView` / `IssueListView` for the detail. Render one `DetailPaneLayout surface="activity"` whose panes are `conversation` (dispatching to `PullDetail` or `IssueDetail` on selection kind), `files` (available only for a PR selection), `commit` (available only for a commit selection, rendering `CommitDiffPanel`), and `workspace`. Use Task 6's shared claim lifecycle here directly.

Leave the Activity rail completely alone: `ACTIVITY_PANE_WIDTH_KEY`, collapse state, `minDetailPaneWidth`, its own `SplitResizeHandle`, and the Escape handler all stay. `PullDetailPane.svelte` exists so PRs mode and Activity share the conversation/files bodies without duplicating prop threading.

- [x] **Step 1** Update `ActivityFeedView.test.ts`: a PR selection offers three panes plus workspace; an issue selection offers no `files` pane; a commit selection offers the `commit` pane; switching PR to issue does not remount the workspace slot.
- [x] **Step 2** Run `./node_modules/.bin/vp test --project unit ActivityFeedView`. Expected FAIL.
- [x] **Step 3** Implement.
- [x] **Step 4** Run `./node_modules/.bin/vp test --project unit ActivityFeedView` plus `--project browser "activity"`. Expected PASS.
- [x] **Step 5** Commit via the `kenn:commit` skill.

---

### Task 11: Palette commands and full-suite verification

**Files:**
- Modify: the PR-detail command registration exercised by `frontend/src/App.palette-pr-detail-commands.browser.svelte.ts`

Add Split right, Split down, Maximize/Restore, and Reset layout for the active surface. Reset layout is surface-scoped and exists **only** here, never in a leaf cluster.

- [x] **Step 1** Extend `App.palette-pr-detail-commands.browser.svelte.ts` with the new entries.
- [x] **Step 2** Run `./node_modules/.bin/vp test --project browser palette-pr-detail-commands`. Expected FAIL.
- [x] **Step 3** Implement.
- [x] **Step 4** Full gate, per `CLAUDE.md`'s pre-push rule: `./node_modules/.bin/vp test` (whole suite, both projects) from `frontend/`, then the affected Playwright suite. Then `make lint`.
- [x] **Step 5** Commit via the `kenn:commit` skill.

The commands live in `frontend/src/lib/stores/keyboard/actions.ts` under the
`detail` scope (`pane.splitRight`, `pane.splitDown`, `pane.toggleZoom`,
`pane.restore`, `pane.reset`), keyed off the surface the current page arranges so
the Workspaces tree stays out of reach. Maximize and Restore are two entries
gated on the current zoom rather than one relabelled entry, matching how the
palette filters by label. `PaneLayoutStore.canSplitTab` exists so the split
entries do not surface as dead rows on a single-tab leaf.

---

### Task 12: Update the real-backend Playwright specs

**Files:**
- Modify: `frontend/tests/e2e-full/00-inline-workspace-continuity.spec.ts`
- Modify: `frontend/tests/e2e-full/detail-action-buttons.spec.ts`
- Modify: `frontend/tests/e2e-full/activity-drawer.spec.ts`
- Rewrite: `frontend/tests/e2e/pr-split-view.spec.ts` → `pr-detail-panes.spec.ts`
- Modify: `frontend/tests/e2e/inline-review.spec.ts`, `frontend/tests/e2e-full/{diff-view,diff-highlight-screenshot,mobile-routes}.spec.ts`

The three real-backend specs assert the dock and split-view DOM this work deletes, and they are the only coverage of behavior no component test can reach: workspace creation against a real backend, claim switching as the selection changes, live-terminal continuity across those switches, and collapse/reopen. Updating their selectors is mandatory, not a re-run.

Add the three missing cases, each in the narrowest lane that observes it:
- the ratio drag and the 720px breakpoint (ordinary 1280px window keeps its split, below 720 flattens with no structural control, and the split survives coming back) in `frontend/tests/e2e/pr-detail-panes.spec.ts` — real pointer drag and real container geometry, no backend state involved;
- flattened-mode control suppression also in `DetailPaneLayout.test.ts` at a mocked 600px width, which is where the control set itself is owned;
- terminal continuity across a PR-to-issue selection change in `ActivityFeedView.test.ts` (slot element identity) plus the real-backend liveness case in `00-inline-workspace-continuity.spec.ts`.

- [x] **Step 1** Read all three specs and list every selector they use that this work removes.
- [x] **Step 2** Run the three specs unchanged against the new UI to confirm they fail, so their coverage is proven live rather than assumed: `./node_modules/.bin/vp exec playwright test --config playwright-e2e.config.ts --project=chromium tests/e2e-full/00-inline-workspace-continuity.spec.ts tests/e2e-full/detail-action-buttons.spec.ts tests/e2e-full/activity-drawer.spec.ts`
- [x] **Step 3** Update the selectors and add the cases above.
- [x] **Step 4** Re-run both Playwright suites whole, since the pane chrome change reaches specs beyond the three: `./node_modules/.bin/vp exec playwright test --config playwright-e2e.config.ts --project=chromium` and `./node_modules/.bin/vp exec playwright test --config playwright.config.ts --project=chromium`. Expected PASS.
- [x] **Step 5** Commit via the `kenn:commit` skill.

---

### Task 13: Namespace the Workspaces drag scope

**Files:**
- Modify: `frontend/src/lib/components/terminal/WorkflowSplitTree.svelte`
- Test: extend the existing `TabbedPanelTree` drag coverage

`WorkflowSplitTree` passes the raw `workspaceId` as `dragScope`, and scope comparison is plain string equality, so a workspace whose id equalled a surface key would collide with the detail scopes. Prefix it `workspace:`.

- [ ] **Step 1** Write a failing test asserting a `detail:prs` payload is rejected by a `workspace:<id>` scope and vice versa.
- [ ] **Step 2** Run `./node_modules/.bin/vp test --project unit tabbed-panel`. Expected FAIL.
- [ ] **Step 3** Prefix the scope in `WorkflowSplitTree.svelte`.
- [ ] **Step 4** Run `./node_modules/.bin/vp test --project unit "tabbed-panel"` and `--project unit WorkspaceTerminalView`. Expected PASS.
- [ ] **Step 5** Commit via the `kenn:commit` skill.

---

## Deferred (do not build)

- The list rail joining the tree.
- Kata, Docs, Reviews surfaces.
- Named layout presets.
- Keyboard tab reordering / splitting / resizing — declared out of scope in the spec's accessibility section. Do not add a test claiming to cover it.
- Any ratio-projection layer that reattributes a resize to an intent-tree descendant. The trivial policy is deliberate; see the spec.
