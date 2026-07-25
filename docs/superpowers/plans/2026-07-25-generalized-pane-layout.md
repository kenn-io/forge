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
| `packages/ui/src/components/shared/TabbedPanelTree.svelte` (modify) | Gains `leafActions` snippet, zoom rendering, collapsed-leaf strip. |
| `packages/ui/src/stores/paneLayout.svelte.ts` (create) | Per-surface persisted layout store; owns the six zoom/focus rules. |
| `packages/ui/src/components/shared/DetailPaneLayout.svelte` (create) | Host: tab spec in, `TabbedPanelTree` out. Owns flatten threshold and the leaf icon cluster. |
| `packages/ui/src/components/shared/PaneLeafActions.svelte` (create) | The three-icon cluster (split right, split down, maximize/restore). |
| `packages/ui/src/item-workspace-claim.svelte.ts` (create) | Shared claim/release/invalidate lifecycle, extracted from the three views. |
| `packages/ui/src/views/PRListView.svelte` (modify) | Drop `.detail-split-layout`, `renderWorkspaceDock`, split-view prefs; render `DetailPaneLayout`. |
| `packages/ui/src/views/IssueListView.svelte` (modify) | Same, two tabs. |
| `packages/ui/src/views/ActivityFeedView.svelte` (modify) | Own the pane tree; stop embedding the two list views for detail. |
| `frontend/src/lib/stores/workspace-host.svelte.ts` (modify) | Delete `dockModes`; derive mode from the layout store. |
| `packages/ui/src/components/workspace/WorkspaceDockPanel.svelte` (delete) | Replaced by `DetailPaneLayout` + collapsed leaf. |

---

### Task 1: Layout state, persistence, and availability pruning

Pure functions only. No Svelte, no DOM.

**Files:**
- Modify: `packages/ui/src/components/shared/tabbed-panel-layout.ts`
- Test: `packages/ui/src/components/shared/tabbed-panel-layout.test.ts` (create)

**Interfaces:**
- Consumes: existing `TabbedPanelNode`, `createTabbedPanelLeaf`, `normalizeTabbedPanelTree`, `clampTabbedPanelRatio` from the same file.
- Produces:
  - `interface TabbedPanelLayoutState { version: 1; tree: TabbedPanelNode; zoomedLeafID: string | null; collapsedLeafIDs: string[] }`
  - `defaultTabbedPanelLayout(tabs: readonly string[]): TabbedPanelLayoutState`
  - `parseTabbedPanelLayout(raw: string | null, knownTabs: readonly string[]): TabbedPanelLayoutState`
  - `serializeTabbedPanelLayout(state: TabbedPanelLayoutState): string`
  - `pruneTabbedPanelTreeToAvailable(node: TabbedPanelNode, availableTabs: readonly string[]): TabbedPanelNode | null`
  - `collectTabbedPanelLeafIDs(node: TabbedPanelNode | null): string[]`

- [ ] **Step 1: Write the failing tests**

Create `packages/ui/src/components/shared/tabbed-panel-layout.test.ts`:

```ts
import { describe, expect, it } from "vite-plus/test";
import {
  collectTabbedPanelLeafIDs,
  createTabbedPanelLeaf,
  defaultTabbedPanelLayout,
  parseTabbedPanelLayout,
  pruneTabbedPanelTreeToAvailable,
  serializeTabbedPanelLayout,
  splitTabbedPanelTabIntoLeaf,
  type TabbedPanelLayoutState,
} from "./tabbed-panel-layout";

const TABS = ["conversation", "files", "workspace"];

describe("tabbed panel layout persistence", () => {
  it("defaults to one leaf holding every known tab", () => {
    const state = defaultTabbedPanelLayout(TABS);
    expect(state.version).toBe(1);
    expect(state.tree.type).toBe("leaf");
    expect(collectTabbedPanelLeafIDs(state.tree)).toHaveLength(1);
    expect(state.zoomedLeafID).toBeNull();
    expect(state.collapsedLeafIDs).toEqual([]);
  });

  it("round-trips a split tree with zoom and collapse", () => {
    const base = defaultTabbedPanelLayout(TABS);
    const leafID = collectTabbedPanelLeafIDs(base.tree)[0]!;
    const tree = splitTabbedPanelTabIntoLeaf(base.tree, "workspace", leafID, "vertical", "after")!;
    const zoomedLeafID = collectTabbedPanelLeafIDs(tree)[1]!;
    const state: TabbedPanelLayoutState = { version: 1, tree, zoomedLeafID, collapsedLeafIDs: [] };
    const parsed = parseTabbedPanelLayout(serializeTabbedPanelLayout(state), TABS);
    expect(parsed).toEqual(state);
  });

  it("falls back to the default on malformed, wrong-version, or null input", () => {
    const fallback = defaultTabbedPanelLayout(TABS);
    for (const raw of [null, "", "{", "[]", '{"version":2,"tree":null}']) {
      const parsed = parseTabbedPanelLayout(raw, TABS);
      expect(parsed.tree.type).toBe("leaf");
      expect(parsed.collapsedLeafIDs).toEqual(fallback.collapsedLeafIDs);
    }
  });

  it("rejects a persisted tree with a duplicate tab key across leaves", () => {
    const dup = {
      version: 1,
      tree: {
        type: "split",
        id: "s0",
        direction: "horizontal",
        ratio: 0.5,
        first: { type: "leaf", id: "l1", tabs: ["conversation", "workspace"], activeTabKey: "conversation" },
        second: { type: "leaf", id: "l2", tabs: ["workspace"], activeTabKey: "workspace" },
      },
      zoomedLeafID: null,
      collapsedLeafIDs: [],
    };
    const parsed = parseTabbedPanelLayout(JSON.stringify(dup), TABS);
    expect(parsed.tree.type).toBe("leaf");
  });

  it("rejects a persisted tree with duplicate node ids", () => {
    const dup = {
      version: 1,
      tree: {
        type: "split",
        id: "dup",
        direction: "horizontal",
        ratio: 0.5,
        first: { type: "leaf", id: "dup", tabs: ["conversation"], activeTabKey: "conversation" },
        second: { type: "leaf", id: "l2", tabs: ["files"], activeTabKey: "files" },
      },
      zoomedLeafID: null,
      collapsedLeafIDs: [],
    };
    expect(parseTabbedPanelLayout(JSON.stringify(dup), TABS).tree.type).toBe("leaf");
  });

  it("drops a zoom or collapse entry naming a leaf that does not exist", () => {
    const base = defaultTabbedPanelLayout(TABS);
    const raw = JSON.stringify({ ...base, zoomedLeafID: "ghost", collapsedLeafIDs: ["ghost"] });
    const parsed = parseTabbedPanelLayout(raw, TABS);
    expect(parsed.zoomedLeafID).toBeNull();
    expect(parsed.collapsedLeafIDs).toEqual([]);
  });
});

describe("availability pruning", () => {
  it("removes unavailable tabs without reinserting them", () => {
    const base = defaultTabbedPanelLayout(TABS);
    const leafID = collectTabbedPanelLeafIDs(base.tree)[0]!;
    const tree = splitTabbedPanelTabIntoLeaf(base.tree, "workspace", leafID, "vertical", "after")!;
    const pruned = pruneTabbedPanelTreeToAvailable(tree, ["conversation", "files"]);
    expect(pruned?.type).toBe("leaf");
    expect(pruned && pruned.type === "leaf" ? pruned.tabs : []).toEqual(["conversation", "files"]);
  });

  it("preserves the surviving leaf's id so intent edits stay addressable", () => {
    const base = defaultTabbedPanelLayout(TABS);
    const leafID = collectTabbedPanelLeafIDs(base.tree)[0]!;
    const tree = splitTabbedPanelTabIntoLeaf(base.tree, "workspace", leafID, "vertical", "after")!;
    const pruned = pruneTabbedPanelTreeToAvailable(tree, ["conversation", "files"]);
    expect(pruned?.id).toBe(leafID);
  });

  it("returns null when nothing is available", () => {
    expect(pruneTabbedPanelTreeToAvailable(createTabbedPanelLeaf(["workspace"]), [])).toBeNull();
  });
});
```

- [ ] **Step 2: Run the tests to verify they fail**

Run from `frontend/`: `./node_modules/.bin/vp test --project unit tabbed-panel-layout`

Expected: FAIL — `defaultTabbedPanelLayout`, `parseTabbedPanelLayout`, `serializeTabbedPanelLayout`, `pruneTabbedPanelTreeToAvailable`, `collectTabbedPanelLeafIDs` are not exported.

- [ ] **Step 3: Implement the additions**

Append to `packages/ui/src/components/shared/tabbed-panel-layout.ts`:

```ts
export interface TabbedPanelLayoutState {
  version: 1;
  tree: TabbedPanelNode;
  zoomedLeafID: string | null;
  collapsedLeafIDs: string[];
}

export function defaultTabbedPanelLayout(tabs: readonly string[]): TabbedPanelLayoutState {
  return { version: 1, tree: createTabbedPanelLeaf(tabs), zoomedLeafID: null, collapsedLeafIDs: [] };
}

export function serializeTabbedPanelLayout(state: TabbedPanelLayoutState): string {
  return JSON.stringify(state);
}

export function collectTabbedPanelLeafIDs(node: TabbedPanelNode | null): string[] {
  if (!node) return [];
  if (node.type === "leaf") return [node.id];
  return [...collectTabbedPanelLeafIDs(node.first), ...collectTabbedPanelLeafIDs(node.second)];
}

function collectTabbedPanelNodeIDs(node: TabbedPanelNode): string[] {
  if (node.type === "leaf") return [node.id];
  return [node.id, ...collectTabbedPanelNodeIDs(node.first), ...collectTabbedPanelNodeIDs(node.second)];
}

// A singleton portal pane (the inline workspace) registers one slot element per
// rendered tab, and edits are applied by node id. A duplicate tab key would
// register two slots for one surface; a duplicate node id would make one edit
// land in several nodes. Reject the persisted tree instead of repairing it.
function hasUniqueTabbedPanelIdentity(node: TabbedPanelNode): boolean {
  const tabs = collectTabbedPanelTabKeys(node);
  const ids = collectTabbedPanelNodeIDs(node);
  return new Set(tabs).size === tabs.length && new Set(ids).size === ids.length;
}

export function pruneTabbedPanelTreeToAvailable(
  node: TabbedPanelNode,
  availableTabs: readonly string[],
): TabbedPanelNode | null {
  return pruneTabbedPanelNode(node, new Set(availableTabs));
}

export function parseTabbedPanelLayout(
  raw: string | null,
  knownTabs: readonly string[],
): TabbedPanelLayoutState {
  const fallback = defaultTabbedPanelLayout(knownTabs);
  if (!raw) return fallback;
  try {
    const parsed: unknown = JSON.parse(raw);
    if (parsed === null || typeof parsed !== "object" || Array.isArray(parsed)) return fallback;
    const record = parsed as Record<string, unknown>;
    if (record.version !== 1) return fallback;
    const tree = parseTabbedPanelNode(record.tree);
    if (!tree || !hasUniqueTabbedPanelIdentity(tree)) return fallback;
    const normalized = normalizeTabbedPanelTree(tree, knownTabs);
    const leafIDs = new Set(collectTabbedPanelLeafIDs(normalized));
    const zoomedLeafID =
      typeof record.zoomedLeafID === "string" && leafIDs.has(record.zoomedLeafID)
        ? record.zoomedLeafID
        : null;
    const collapsedLeafIDs = Array.isArray(record.collapsedLeafIDs)
      ? record.collapsedLeafIDs.filter((id): id is string => typeof id === "string" && leafIDs.has(id))
      : [];
    return { version: 1, tree: normalized, zoomedLeafID, collapsedLeafIDs };
  } catch {
    return fallback;
  }
}

function parseTabbedPanelNode(value: unknown): TabbedPanelNode | null {
  if (value === null || typeof value !== "object" || Array.isArray(value)) return null;
  const node = value as Record<string, unknown>;
  if (typeof node.id !== "string" || node.id === "") return null;
  if (node.type === "leaf") {
    if (!Array.isArray(node.tabs)) return null;
    const tabs = node.tabs.filter((tab): tab is string => typeof tab === "string" && tab !== "");
    if (tabs.length === 0) return null;
    const activeTabKey =
      typeof node.activeTabKey === "string" && tabs.includes(node.activeTabKey)
        ? node.activeTabKey
        : tabs[0]!;
    return { type: "leaf", id: node.id, tabs, activeTabKey };
  }
  if (node.type !== "split") return null;
  const first = parseTabbedPanelNode(node.first);
  const second = parseTabbedPanelNode(node.second);
  if (!first || !second) return null;
  return {
    type: "split",
    id: node.id,
    direction: node.direction === "vertical" ? "vertical" : "horizontal",
    ratio: clampTabbedPanelRatio(typeof node.ratio === "number" ? node.ratio : 0.5),
    first,
    second,
  };
}
```

Note: `parseTabbedPanelLayout` calls `normalizeTabbedPanelTree` with the surface's full known-tab list, so a newly introduced tab appears and a retired one is dropped. It must NOT be given the availability list — availability pruning is a render-time concern and lives in `pruneTabbedPanelTreeToAvailable`.

- [ ] **Step 4: Run the tests to verify they pass**

Run from `frontend/`: `./node_modules/.bin/vp test --project unit tabbed-panel-layout`
Expected: PASS, all cases.

- [ ] **Step 5: Commit**

```bash
git add packages/ui/src/components/shared/tabbed-panel-layout.ts \
        packages/ui/src/components/shared/tabbed-panel-layout.test.ts
git commit  # via the kenn:commit skill
```

---

### Task 2: Flatten fallback

**Files:**
- Modify: `packages/ui/src/components/shared/tabbed-panel-layout.ts`
- Test: `packages/ui/src/components/shared/tabbed-panel-layout.test.ts`

**Interfaces:**
- Produces:
  - `FLATTENED_TABBED_PANEL_LEAF_ID: string`
  - `flattenTabbedPanelTree(node: TabbedPanelNode, preferredActiveTabKey?: string): TabbedPanelLeaf`

- [ ] **Step 1: Write the failing tests**

Append to `tabbed-panel-layout.test.ts`:

```ts
describe("flatten fallback", () => {
  it("collects every tab in traversal order under one synthetic leaf", () => {
    const base = defaultTabbedPanelLayout(TABS);
    const leafID = collectTabbedPanelLeafIDs(base.tree)[0]!;
    const tree = splitTabbedPanelTabIntoLeaf(base.tree, "workspace", leafID, "vertical", "after")!;
    const flat = flattenTabbedPanelTree(tree);
    expect(flat.type).toBe("leaf");
    expect(flat.id).toBe(FLATTENED_TABBED_PANEL_LEAF_ID);
    expect(flat.tabs).toEqual(["conversation", "files", "workspace"]);
  });

  it("honours a preferred active tab so flattening does not jump panes", () => {
    const base = defaultTabbedPanelLayout(TABS);
    const leafID = collectTabbedPanelLeafIDs(base.tree)[0]!;
    const tree = splitTabbedPanelTabIntoLeaf(base.tree, "workspace", leafID, "vertical", "after")!;
    expect(flattenTabbedPanelTree(tree, "workspace").activeTabKey).toBe("workspace");
  });

  it("ignores a preferred tab that is absent and uses the first leaf's active tab", () => {
    const tree = createTabbedPanelLeaf(["conversation", "files"], "files");
    expect(flattenTabbedPanelTree(tree, "workspace").activeTabKey).toBe("files");
  });
});
```

Add `FLATTENED_TABBED_PANEL_LEAF_ID` and `flattenTabbedPanelTree` to the file's import list at the top of the test.

- [ ] **Step 2: Run to verify failure**

Run from `frontend/`: `./node_modules/.bin/vp test --project unit tabbed-panel-layout`
Expected: FAIL — not exported.

- [ ] **Step 3: Implement**

```ts
// Structural edits are disabled while flattened (see the design spec), so a
// constant synthetic id is safe: no mutation is ever applied by this id.
export const FLATTENED_TABBED_PANEL_LEAF_ID = "tabbed-panel-flattened";

export function flattenTabbedPanelTree(
  node: TabbedPanelNode,
  preferredActiveTabKey?: string,
): TabbedPanelLeaf {
  const tabs = collectTabbedPanelTabKeys(node);
  const firstActive = firstTabbedPanelLeaf(node)?.activeTabKey;
  const activeTabKey =
    preferredActiveTabKey !== undefined && tabs.includes(preferredActiveTabKey)
      ? preferredActiveTabKey
      : (firstActive ?? tabs[0]!);
  return { type: "leaf", id: FLATTENED_TABBED_PANEL_LEAF_ID, tabs, activeTabKey };
}
```

- [ ] **Step 4: Run to verify pass**

Run from `frontend/`: `./node_modules/.bin/vp test --project unit tabbed-panel-layout`
Expected: PASS.

- [ ] **Step 5: Commit** (via the `kenn:commit` skill)

---

### Task 3: `leafActions` snippet, zoom rendering, collapsed leaf

**Files:**
- Modify: `packages/ui/src/components/shared/TabbedPanelTree.svelte`
- Create: `frontend/src/lib/components/design-system/PaneTreeZoom.browser.svelte.ts`

**Interfaces:**
- Consumes: Task 1's `TabbedPanelLayoutState` shape (`zoomedLeafID`, `collapsedLeafIDs`) — passed as two props, not the whole state, so the component stays state-store agnostic.
- Produces new `TabbedPanelTree` props:
  - `leafActions?: Snippet<[TabbedPanelLeaf]>` — rendered once per leaf, right-aligned in the tab strip, receiving the leaf so callers get its id.
  - `zoomedLeafID?: string | null`
  - `collapsedLeafIDs?: readonly string[]`
  - `collapsedLeaf?: Snippet<[TabbedPanelLeaf]>` — the reopen strip for a collapsed leaf.

Rendering rules:
- When `zoomedLeafID` names a leaf in this subtree, that leaf renders at full size and every sibling subtree is wrapped `hidden` + `inert` — mounted, so scroll position and the workspace slot survive.
- A leaf in `collapsedLeafIDs` renders `collapsedLeaf` instead of its tab strip and bodies, and **does not render its tab panels at all** — the workspace slot must unmount so reopening changes slot identity (see spec, Workspace pane).

- [ ] **Step 1: Write the failing browser spec**

Create `frontend/src/lib/components/design-system/PaneTreeZoom.browser.svelte.ts`. It mounts the existing `DesignSystemPanelHarness` (extend it with the new props) and asserts: a zoomed leaf's sibling has `inert`; the zoomed leaf's panel has non-zero `getBoundingClientRect().width`; a collapsed leaf renders the `collapsedLeaf` snippet and has no `.tabbed-panel-tab-panel` descendant. Match the file's sibling `DesignSystemPanel.browser.svelte.ts` for mount helpers and assertion style — read it first.

- [ ] **Step 2: Run to verify failure**

Run from `frontend/`: `./node_modules/.bin/vp test --project browser PaneTreeZoom`
Expected: FAIL — props not supported.

- [ ] **Step 3: Implement in `TabbedPanelTree.svelte`**

Add the four props with defaults (`leafActions = undefined`, `zoomedLeafID = null`, `collapsedLeafIDs = []`, `collapsedLeaf = undefined`), thread all four through both recursive `<Self>` calls, and:
- In the leaf branch: if `collapsedLeafIDs.includes(node.id)` and `collapsedLeaf` is provided, render only that snippet inside `.tabbed-panel-leaf`. Otherwise render the strip, then `{#if leafActions}<div class="tabbed-panel-leaf-actions">{@render leafActions(node)}</div>{/if}` after the `{#each}` inside `.tabbed-panel-tabs`, then the body as today.
- In the split branch: compute `firstHasZoom` / `secondHasZoom` via `collectTabbedPanelLeafIDs`; when one side has the zoom, give that child `flex: 1 1 100%` and wrap the other in a `hidden inert` div.

Style additions: `.tabbed-panel-leaf-actions { margin-left: auto; display: inline-flex; align-items: center; padding-right: 4px; }` and reuse the existing `.tabbed-panel-tab-tool` rules by exposing them to the new cluster.

- [ ] **Step 4: Run to verify pass, plus the existing suites**

Run from `frontend/`:
```
./node_modules/.bin/vp test --project browser PaneTreeZoom
./node_modules/.bin/vp test --project unit TabbedPanelTree
./node_modules/.bin/vp test --project browser DesignSystemPanel
```
Expected: all PASS. The last two prove the new optional props did not disturb the two existing consumers.

- [ ] **Step 5: Commit** (via the `kenn:commit` skill)

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
    collapsedLeafIDs(): readonly string[];
    activateTab(tabKey: string): void;
    moveTabBefore(source: string, target: string): void;
    appendTabToLeaf(source: string, leafID: string): void;
    splitTab(source: string, leafID: string, direction: TabbedPanelDirection, placement: "before" | "after"): void;
    setRatio(splitID: string, ratio: number): void;
    toggleZoom(leafID: string): void;
    clearZoom(): void;
    setCollapsed(leafID: string, collapsed: boolean): void;
    leafIDForTab(tabKey: string): string | null;
    reset(): void;
  }
  export function getPaneLayoutStore(surface: PaneSurfaceKey, knownTabs: readonly string[]): PaneLayoutStore;
  ```
- Persistence key: `middleman-pane-layout-v1:<surface>`, written through try/catch like `WorkspaceDockPanel`'s existing helpers.
- `toggleZoom` refuses while `getStackDepth() > 0` (import from `../stores/keyboard/modal-stack.svelte.js`).

Tests must cover, one `it` each: per-surface isolation (two surfaces do not share a key); a malformed stored value falling back to default; zoom refused while a modal is open; `clearZoom` on identity change; and `setCollapsed` surviving a round trip. Use `vi.stubGlobal`/`localStorage` per the existing `terminalSettingsPersistence.test.ts` conventions — read that file first.

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
    collapsedLeaf?: Snippet<[TabbedPanelLeaf]> | undefined;
    flattenBelowPx?: number;                  // default 1280
  }
  ```
- `renderPane`'s second argument is **effective visibility**: active in its leaf AND not hidden by a zoom elsewhere AND its leaf not collapsed.
- Flatten: measure the host with `ResizeObserver` exactly as `PRListView` does today; below `flattenBelowPx`, render `flattenTabbedPanelTree(renderTree, activeTabKey)` and pass **only** `onSelectTab` — no `leafActions`, no mutation callbacks, so every structural interaction is read-only.
- Icons in `PaneLeafActions.svelte`: `square-split-horizontal` (split right, `direction: "horizontal"`, `placement: "after"`), `square-split-vertical` (split down, `"vertical"`, `"after"`), `maximize`/`minimize` (zoom toggle). Both split buttons `disabled` when `leaf.tabs.length <= 1`. Every button carries `title` and `aria-label`.

- [ ] **Step 1** Write failing jsdom tests: both split buttons disabled on a single-tab leaf; maximize label flips to Restore when the leaf is zoomed; an unavailable tab renders no panel; flattened mode renders no leaf-action cluster.
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

- [ ] **Step 1** Update `PRListView.test.ts` and `PRListView.workspaceDraft.test.ts` for the new structure; add a case asserting the diff pane keeps its scroll offset across a tab switch.
- [ ] **Step 2** Run `./node_modules/.bin/vp test --project unit PRListView`. Expected FAIL.
- [ ] **Step 3** Implement.
- [ ] **Step 4** Run `./node_modules/.bin/vp test --project unit PRListView`, then the App browser specs that assert detail-tab chrome: `./node_modules/.bin/vp test --project browser "detail-code-wrap|palette-pr-detail-commands|navigation"`. Expected PASS.
- [ ] **Step 5** Commit via the `kenn:commit` skill.

---

### Task 8: Rewire `IssueListView`

**Files:**
- Modify: `packages/ui/src/views/IssueListView.svelte`

Delete the `renderWorkspaceDock` prop, the `WorkspaceDockPanel` import and usage, and the `.detail-host` wrapper that exists only to host it. Add one `DetailPaneLayout surface="issues"` with two tabs: `conversation` (always available, rendering the existing `IssueDetail` with its current props) and `workspace` (available when `inlineWorkspace` holds a claim for `claimIdentity`). There is no diff pane and no route-bound tab here, so no `navigate()`/`replaceUrl()` handling is needed — issues have a single detail route. Keep the `CollapsibleSidebar`, the `IssueList` sidebar snippet, and the "Select an issue" placeholder branch exactly as they are.

- [ ] **Step 1** Update `IssueListView.test.ts`.
- [ ] **Step 2** Run `./node_modules/.bin/vp test --project unit IssueListView`. Expected FAIL.
- [ ] **Step 3** Implement.
- [ ] **Step 4** Run the same command. Expected PASS.
- [ ] **Step 5** Commit via the `kenn:commit` skill.

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
- `collapsed` — that leaf is in `collapsedLeafIDs`, or the workspace tab is absent from the render tree
- `split` — otherwise

`setDockMode` maps onto `toggleZoom` / `setCollapsed`. `focusTerminal` keeps its total-no-op modal guard and its best-effort-then-pending-flag sequence unchanged.

All six behaviors from the spec's Zoom section need a test. The two that a tree-derived rewrite most easily breaks, and which `workspace-host.test.ts` already covers today, are: a same-identity claim re-assert must not un-zoom (`:543`), and a collapsed dock keeps its claim (`:476`).

- [ ] **Step 1** Extend `workspace-host.test.ts` with one case per behavior, keeping the two existing cases above passing unchanged.
- [ ] **Step 2** Run `./node_modules/.bin/vp test --project unit workspace-host`. Expected FAIL.
- [ ] **Step 3** Implement; delete the three files.
- [ ] **Step 4** Run `./node_modules/.bin/vp test --project unit workspace-host` and `--project browser WorkspaceDockPanel`. Expected PASS.
- [ ] **Step 5** Commit via the `kenn:commit` skill.

---

### Task 10: Restructure `ActivityFeedView`

**Files:**
- Modify: `packages/ui/src/views/ActivityFeedView.svelte`
- Create: `packages/ui/src/components/detail/PullDetailPane.svelte`

Stop embedding `PRListView` / `IssueListView` for the detail. Render one `DetailPaneLayout surface="activity"` whose panes are `conversation` (dispatching to `PullDetail` or `IssueDetail` on selection kind), `files` (available only for a PR selection), `commit` (available only for a commit selection, rendering `CommitDiffPanel`), and `workspace`. Use Task 6's shared claim lifecycle here directly.

Leave the Activity rail completely alone: `ACTIVITY_PANE_WIDTH_KEY`, collapse state, `minDetailPaneWidth`, its own `SplitResizeHandle`, and the Escape handler all stay. `PullDetailPane.svelte` exists so PRs mode and Activity share the conversation/files bodies without duplicating prop threading.

- [ ] **Step 1** Update `ActivityFeedView.test.ts`: a PR selection offers three panes plus workspace; an issue selection offers no `files` pane; a commit selection offers the `commit` pane; switching PR to issue does not remount the workspace slot.
- [ ] **Step 2** Run `./node_modules/.bin/vp test --project unit ActivityFeedView`. Expected FAIL.
- [ ] **Step 3** Implement.
- [ ] **Step 4** Run `./node_modules/.bin/vp test --project unit ActivityFeedView` plus `--project browser "activity"`. Expected PASS.
- [ ] **Step 5** Commit via the `kenn:commit` skill.

---

### Task 11: Palette commands and full-suite verification

**Files:**
- Modify: the PR-detail command registration exercised by `frontend/src/App.palette-pr-detail-commands.browser.svelte.ts`

Add Split right, Split down, Maximize/Restore, and Reset layout for the active surface. Reset layout is surface-scoped and exists **only** here, never in a leaf cluster.

- [ ] **Step 1** Extend `App.palette-pr-detail-commands.browser.svelte.ts` with the new entries.
- [ ] **Step 2** Run `./node_modules/.bin/vp test --project browser palette-pr-detail-commands`. Expected FAIL.
- [ ] **Step 3** Implement.
- [ ] **Step 4** Full gate, per `CLAUDE.md`'s pre-push rule: `./node_modules/.bin/vp test` (whole suite, both projects) from `frontend/`, then the affected Playwright suite. Then `make lint`.
- [ ] **Step 5** Commit via the `kenn:commit` skill.

---

## Deferred (do not build)

- The list rail joining the tree.
- Kata, Docs, Reviews surfaces.
- Named layout presets.
- Keyboard tab reordering / splitting / resizing — declared out of scope in the spec's accessibility section. Do not add a test claiming to cover it.
- Any ratio-projection layer that reattributes a resize to an intent-tree descendant. The trivial policy is deliberate; see the spec.
