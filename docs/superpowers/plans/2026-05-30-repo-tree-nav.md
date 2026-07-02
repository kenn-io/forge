# Repo Tree Navigation Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Replace the flat repo list in the header repo selector with a collapsible `host -> owner -> repo` tree whose interior rows expand on name-click and select their whole subtree via a tri-state checkbox.

**Architecture:** A pure tree-building module turns the existing `RepoOption[]` into a `host -> owner -> repo` structure; a pure projection flattens single-child levels and applies expansion + filtering into an ordered visible-row list; pure selection helpers derive tri-state and cascade selection. A small Svelte store persists collapsed-node ids (mirroring `collapsedRepos`). `RepoTreeNode.svelte` renders one row. `RepoTypeahead.svelte` is rewired to render the projected rows and drive selection through the unchanged global-filter contract (`setGlobalRepo` with a comma-separated set of leaf `platformHost/repoPath` strings).

**Tech Stack:** Svelte 5 (runes), TypeScript, Vitest + `@testing-library/svelte` for unit/component tests, Playwright (`tests/e2e-full/`) for e2e. Build/test via `bun`.

**Spec:** `docs/superpowers/specs/2026-05-30-repo-tree-nav-design.md`

---

## Conventions for this plan

- Run a single frontend test file with: `cd frontend && bun run test <path-relative-to-frontend>`.
  (`bun run test` runs `vitest run`; a trailing path filters to that file.)
- Run type-checking with: `cd frontend && bun run typecheck`.
- Pure modules and their `*.test.ts` are colocated under `frontend/src/lib/`.
- Do not use `npm`. Use `bun`.

## ARIA decision (locked for this plan)

The spec left ARIA roles open. To avoid churning the existing component tests and the
`tests/e2e-full/` specs that drive the selector by `role="option"` + accessible name,
keep the current semantics:

- container keeps `role="listbox"` and the `.typeahead-list` class;
- every tree row (host, owner, repo, and "All repos") is `role="option"` with the
  `.typeahead-option` class;
- a row's **accessible name** (`aria-label`) is its full path: leaf = its `value`
  (e.g. `github.com/import-lab/api`), owner = `${platformHost}/${ownerPath}`, host =
  `platformHost`. Visible text stays short (just the repo or owner name);
- interior rows carry `aria-expanded`; selected rows carry `aria-selected` and
  `aria-checked` ("true" | "false" | "mixed").

## File Structure

- Create `frontend/src/lib/components/repoTree.ts` — pure tree builder, visible-row projection, selection helpers, and the exported types.
- Create `frontend/src/lib/components/repoTree.test.ts` — unit tests for the above.
- Create `frontend/src/lib/stores/repoTreeExpansion.svelte.ts` — persisted collapsed-id store.
- Create `frontend/src/lib/stores/repoTreeExpansion.test.ts` — store tests.
- Create `frontend/src/lib/components/RepoTreeNode.svelte` — single-row presenter.
- Create `frontend/src/lib/components/RepoTreeNode.test.ts` — row component tests.
- Modify `frontend/src/lib/components/RepoTypeahead.svelte` — extend `RepoOption`, render the tree, mouse + keyboard + filter wiring.
- Modify `frontend/src/lib/components/RepoTypeahead.test.ts` — adapt flat-list assertions to tree rows; add tree behaviors.
- Modify `frontend/tests/e2e-full/repo-filter-multiselect.spec.ts` — add group-select coverage.

---

### Task 1: Tree builder and types

**Files:**
- Create: `frontend/src/lib/components/repoTree.ts`
- Test: `frontend/src/lib/components/repoTree.test.ts`

- [ ] **Step 1: Write the failing test**

Create `frontend/src/lib/components/repoTree.test.ts`:

```ts
import { describe, expect, it } from "vitest";

import { buildRepoTree, type RepoTreeOption } from "./repoTree.js";

function opt(
  platformHost: string,
  repoPath: string,
  provider = "github",
): RepoTreeOption {
  const segments = repoPath.split("/");
  return {
    value: `${platformHost}/${repoPath}`,
    owner: segments.slice(0, -1).join("/"),
    name: segments[segments.length - 1] ?? repoPath,
    provider,
    platformHost,
  };
}

describe("buildRepoTree", () => {
  it("groups host -> owner -> repo and sorts each level", () => {
    const tree = buildRepoTree([
      opt("github.com", "acme/web"),
      opt("github.com", "acme/api"),
      opt("github.com", "widgets/sdk"),
    ]);

    expect(tree).toHaveLength(1);
    const host = tree[0];
    expect(host.kind).toBe("host");
    expect(host.id).toBe("github.com");
    expect(host.label).toBe("github.com");
    expect(host.provider).toBe("github");
    expect(host.children.map((o) => o.label)).toEqual(["acme", "widgets"]);

    const acme = host.children[0];
    expect(acme.id).toBe("github.com/acme");
    expect(acme.children.map((r) => r.label)).toEqual(["api", "web"]);
    expect(acme.children[0].value).toBe("github.com/acme/api");
    expect(acme.children[0].id).toBe("github.com/acme/api");
  });

  it("keeps GitLab nested groups as one slashed owner node", () => {
    const tree = buildRepoTree([
      opt("gitlab.com", "platform/frontend/web-ui", "gitlab"),
    ]);

    const host = tree[0];
    expect(host.children).toHaveLength(1);
    const owner = host.children[0];
    expect(owner.label).toBe("platform/frontend");
    expect(owner.id).toBe("gitlab.com/platform/frontend");
    expect(owner.children[0].label).toBe("web-ui");
    expect(owner.children[0].value).toBe("gitlab.com/platform/frontend/web-ui");
  });

  it("separates hosts and sorts them by label", () => {
    const tree = buildRepoTree([
      opt("gitlab.com", "g/x", "gitlab"),
      opt("github.com", "a/y"),
    ]);
    expect(tree.map((h) => h.label)).toEqual(["github.com", "gitlab.com"]);
  });

  it("uses the first option's provider when a host's providers disagree", () => {
    const tree = buildRepoTree([
      opt("ghe.example.com", "a/x", "github"),
      opt("ghe.example.com", "b/y", "gitlab"),
    ]);
    expect(tree[0].provider).toBe("github");
  });
});
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd frontend && bun run test src/lib/components/repoTree.test.ts`
Expected: FAIL — cannot find module `./repoTree.js` / `buildRepoTree` is not defined.

- [ ] **Step 3: Write minimal implementation**

Create `frontend/src/lib/components/repoTree.ts`:

```ts
export interface RepoTreeOption {
  value: string; // `${platformHost}/${repoPath}`
  owner: string;
  name: string;
  provider: string; // canonical, lowercase
  platformHost: string;
}

export interface RepoLeaf {
  kind: "repo";
  id: string;
  label: string;
  value: string;
}

export interface OwnerNode {
  kind: "owner";
  id: string;
  label: string;
  children: RepoLeaf[];
}

export interface HostNode {
  kind: "host";
  id: string;
  label: string;
  provider: string;
  platformHost: string;
  children: OwnerNode[];
}

export type RepoTreeNodeData = HostNode | OwnerNode | RepoLeaf;

function stripHostPrefix(value: string, platformHost: string): string {
  const prefix = `${platformHost}/`;
  if (value.startsWith(prefix)) return value.slice(prefix.length);
  // Defensive fallback: drop everything up to and including the first slash.
  return value.replace(/^[^/]+\//, "");
}

export function buildRepoTree(
  options: readonly RepoTreeOption[],
): HostNode[] {
  const hosts = new Map<string, HostNode>();

  for (const option of options) {
    const repoPath = stripHostPrefix(option.value, option.platformHost);
    const segments = repoPath.split("/");
    const name = segments[segments.length - 1] ?? repoPath;
    const ownerPath = segments.slice(0, -1).join("/");
    if (ownerPath === "") continue; // malformed value with no owner segment

    let host = hosts.get(option.platformHost);
    if (!host) {
      host = {
        kind: "host",
        id: option.platformHost,
        label: option.platformHost,
        provider: option.provider,
        platformHost: option.platformHost,
        children: [],
      };
      hosts.set(option.platformHost, host);
    }

    let owner = host.children.find((node) => node.label === ownerPath);
    if (!owner) {
      owner = {
        kind: "owner",
        id: `${option.platformHost}/${ownerPath}`,
        label: ownerPath,
        children: [],
      };
      host.children.push(owner);
    }

    owner.children.push({
      kind: "repo",
      id: option.value,
      label: name,
      value: option.value,
    });
  }

  const sorted = [...hosts.values()].sort((a, b) =>
    a.label.localeCompare(b.label),
  );
  for (const host of sorted) {
    host.children.sort((a, b) => a.label.localeCompare(b.label));
    for (const owner of host.children) {
      owner.children.sort((a, b) => a.label.localeCompare(b.label));
    }
  }
  return sorted;
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `cd frontend && bun run test src/lib/components/repoTree.test.ts`
Expected: PASS (4 tests).

- [ ] **Step 5: Commit**

```bash
git add frontend/src/lib/components/repoTree.ts frontend/src/lib/components/repoTree.test.ts
git commit -m "feat: build host/owner/repo tree from repo options"
```

---

### Task 2: Visible-row projection (flatten + expansion + filter)

**Files:**
- Modify: `frontend/src/lib/components/repoTree.ts`
- Test: `frontend/src/lib/components/repoTree.test.ts`

- [ ] **Step 1: Write the failing test**

Append to `frontend/src/lib/components/repoTree.test.ts`:

```ts
import { visibleRows, type VisibleRow } from "./repoTree.js";

const neverCollapsed = () => false;

function labelsAtDepth(rows: VisibleRow[]): Array<[string, number]> {
  return rows.map((row) => [row.node.label, row.depth]);
}

describe("visibleRows", () => {
  it("omits the host node when there is only one host", () => {
    const tree = buildRepoTree([
      opt("github.com", "acme/api"),
      opt("github.com", "acme/web"),
    ]);
    const rows = visibleRows(tree, { isCollapsed: neverCollapsed });
    // owner at depth 0 (host omitted), two leaves at depth 1
    expect(labelsAtDepth(rows)).toEqual([
      ["acme", 0],
      ["api", 1],
      ["web", 1],
    ]);
  });

  it("shows host nodes at depth 0 when more than one host exists", () => {
    const tree = buildRepoTree([
      opt("github.com", "acme/api"),
      opt("gitlab.com", "g/x", "gitlab"),
    ]);
    const rows = visibleRows(tree, { isCollapsed: neverCollapsed });
    expect(rows[0].node.kind).toBe("host");
    expect(rows[0].depth).toBe(0);
    expect(rows.find((r) => r.node.label === "acme")?.depth).toBe(1);
  });

  it("flattens a single-repo owner into one leaf row with no children", () => {
    const tree = buildRepoTree([
      opt("github.com", "acme/api"),
      opt("github.com", "acme/web"),
      opt("github.com", "solo/only"),
    ]);
    const rows = visibleRows(tree, { isCollapsed: neverCollapsed });
    const soloRow = rows.find((r) => r.node.label === "only");
    expect(soloRow).toBeTruthy();
    expect(soloRow!.depth).toBe(0); // at the owner's depth (single host)
    expect(soloRow!.hasChildren).toBe(false);
    // the "solo" owner node itself is not rendered
    expect(rows.some((r) => r.node.label === "solo")).toBe(false);
  });

  it("hides children of a collapsed node", () => {
    const tree = buildRepoTree([
      opt("github.com", "acme/api"),
      opt("github.com", "acme/web"),
    ]);
    const collapsed = (id: string) => id === "github.com/acme";
    const rows = visibleRows(tree, { isCollapsed: collapsed });
    expect(labelsAtDepth(rows)).toEqual([["acme", 0]]);
    expect(rows[0].expanded).toBe(false);
  });

  it("prunes non-matching repos and force-expands matches when filtering", () => {
    const tree = buildRepoTree([
      opt("github.com", "acme/api"),
      opt("github.com", "acme/web"),
      opt("github.com", "widgets/web-sdk"),
    ]);
    // collapse everything; filtering must override collapse
    const rows = visibleRows(tree, {
      isCollapsed: () => true,
      query: "web",
    });
    const labels = rows.map((r) => r.node.label);
    expect(labels).toContain("web");
    expect(labels).toContain("web-sdk");
    expect(labels).not.toContain("api");
  });
});
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd frontend && bun run test src/lib/components/repoTree.test.ts`
Expected: FAIL — `visibleRows` / `VisibleRow` not exported.

- [ ] **Step 3: Write minimal implementation**

Append to `frontend/src/lib/components/repoTree.ts`:

```ts
export interface VisibleRow {
  node: RepoTreeNodeData;
  depth: number;
  hasChildren: boolean;
  expanded: boolean;
}

export interface VisibleRowsOptions {
  isCollapsed: (id: string) => boolean;
  query?: string;
}

export function visibleRows(
  tree: readonly HostNode[],
  { isCollapsed, query }: VisibleRowsOptions,
): VisibleRow[] {
  const q = query?.trim().toLowerCase() ?? "";
  const filtering = q !== "";
  const matches = (leaf: RepoLeaf) =>
    !filtering || leaf.value.toLowerCase().includes(q);

  // Prune to owners/hosts that still have a matching leaf.
  const pruned = tree
    .map((host) => ({
      ...host,
      children: host.children
        .map((owner) => ({
          ...owner,
          children: owner.children.filter(matches),
        }))
        .filter((owner) => owner.children.length > 0),
    }))
    .filter((host) => host.children.length > 0);

  const expandedOf = (id: string) => filtering || !isCollapsed(id);
  const singleHost = pruned.length === 1;
  const rows: VisibleRow[] = [];

  for (const host of pruned) {
    const ownerDepth = singleHost ? 0 : 1;
    if (!singleHost) {
      const hostExpanded = expandedOf(host.id);
      rows.push({ node: host, depth: 0, hasChildren: true, expanded: hostExpanded });
      if (!hostExpanded) continue;
    }
    for (const owner of host.children) {
      if (owner.children.length === 1) {
        rows.push({
          node: owner.children[0],
          depth: ownerDepth,
          hasChildren: false,
          expanded: false,
        });
        continue;
      }
      const ownerExpanded = expandedOf(owner.id);
      rows.push({
        node: owner,
        depth: ownerDepth,
        hasChildren: true,
        expanded: ownerExpanded,
      });
      if (!ownerExpanded) continue;
      for (const leaf of owner.children) {
        rows.push({
          node: leaf,
          depth: ownerDepth + 1,
          hasChildren: false,
          expanded: false,
        });
      }
    }
  }
  return rows;
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `cd frontend && bun run test src/lib/components/repoTree.test.ts`
Expected: PASS (all Task 1 + Task 2 tests).

- [ ] **Step 5: Commit**

```bash
git add frontend/src/lib/components/repoTree.ts frontend/src/lib/components/repoTree.test.ts
git commit -m "feat: project repo tree into a flattened, filtered visible-row list"
```

---

### Task 3: Selection helpers (tri-state + cascade)

**Files:**
- Modify: `frontend/src/lib/components/repoTree.ts`
- Test: `frontend/src/lib/components/repoTree.test.ts`

- [ ] **Step 1: Write the failing test**

Append to `frontend/src/lib/components/repoTree.test.ts`:

```ts
import {
  collectLeafValues,
  nodeSelectionState,
  toggleSubtree,
} from "./repoTree.js";

describe("selection helpers", () => {
  const tree = buildRepoTree([
    opt("github.com", "acme/api"),
    opt("github.com", "acme/web"),
    opt("github.com", "acme/infra"),
  ]);
  const acme = tree[0].children[0];

  it("collects all descendant leaf values", () => {
    expect(collectLeafValues(acme).sort()).toEqual([
      "github.com/acme/api",
      "github.com/acme/infra",
      "github.com/acme/web",
    ]);
    expect(collectLeafValues(acme.children[0])).toEqual([
      "github.com/acme/api",
    ]);
  });

  it("computes tri-state from the active set", () => {
    expect(nodeSelectionState(acme, new Set())).toBe("unchecked");
    expect(
      nodeSelectionState(acme, new Set(["github.com/acme/api"])),
    ).toBe("partial");
    expect(
      nodeSelectionState(
        acme,
        new Set([
          "github.com/acme/api",
          "github.com/acme/web",
          "github.com/acme/infra",
        ]),
      ),
    ).toBe("checked");
  });

  it("adds all subtree leaves when not fully checked", () => {
    expect(toggleSubtree(acme, ["github.com/acme/api"]).sort()).toEqual([
      "github.com/acme/api",
      "github.com/acme/infra",
      "github.com/acme/web",
    ]);
  });

  it("removes all subtree leaves when fully checked", () => {
    const all = [
      "github.com/acme/api",
      "github.com/acme/web",
      "github.com/acme/infra",
    ];
    expect(toggleSubtree(acme, all)).toEqual([]);
  });

  it("toggles a single leaf without touching siblings", () => {
    expect(
      toggleSubtree(acme.children[0], ["github.com/acme/web"]).sort(),
    ).toEqual(["github.com/acme/api", "github.com/acme/web"]);
  });
});
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd frontend && bun run test src/lib/components/repoTree.test.ts`
Expected: FAIL — selection helpers not exported.

- [ ] **Step 3: Write minimal implementation**

Append to `frontend/src/lib/components/repoTree.ts`:

```ts
export type SelectionState = "checked" | "partial" | "unchecked";

export function collectLeafValues(node: RepoTreeNodeData): string[] {
  if (node.kind === "repo") return [node.value];
  const values: string[] = [];
  for (const child of node.children) values.push(...collectLeafValues(child));
  return values;
}

export function nodeSelectionState(
  node: RepoTreeNodeData,
  active: ReadonlySet<string>,
): SelectionState {
  const leaves = collectLeafValues(node);
  if (leaves.length === 0) return "unchecked";
  let selected = 0;
  for (const value of leaves) if (active.has(value)) selected += 1;
  if (selected === 0) return "unchecked";
  if (selected === leaves.length) return "checked";
  return "partial";
}

export function toggleSubtree(
  node: RepoTreeNodeData,
  activeValues: readonly string[],
): string[] {
  const leaves = collectLeafValues(node);
  if (nodeSelectionState(node, new Set(activeValues)) === "checked") {
    const remove = new Set(leaves);
    return activeValues.filter((value) => !remove.has(value));
  }
  const next = [...activeValues];
  const present = new Set(activeValues);
  for (const value of leaves) if (!present.has(value)) next.push(value);
  return next;
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `cd frontend && bun run test src/lib/components/repoTree.test.ts`
Expected: PASS (all repoTree tests).

- [ ] **Step 5: Commit**

```bash
git add frontend/src/lib/components/repoTree.ts frontend/src/lib/components/repoTree.test.ts
git commit -m "feat: derive tri-state selection and subtree toggling for the repo tree"
```

---

### Task 4: Expansion-state store

**Files:**
- Create: `frontend/src/lib/stores/repoTreeExpansion.svelte.ts`
- Test: `frontend/src/lib/stores/repoTreeExpansion.test.ts`

- [ ] **Step 1: Write the failing test**

Create `frontend/src/lib/stores/repoTreeExpansion.test.ts`:

```ts
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { createRepoTreeExpansionStore } from "./repoTreeExpansion.svelte.js";

const KEY = "middleman:repoTreeCollapsed";

beforeEach(() => {
  localStorage.clear();
});

afterEach(() => {
  vi.restoreAllMocks();
});

describe("createRepoTreeExpansionStore", () => {
  it("reports every node expanded on a fresh store", () => {
    const store = createRepoTreeExpansionStore();
    expect(store.isCollapsed("github.com/acme")).toBe(false);
  });

  it("flips collapsed state on each toggle", () => {
    const store = createRepoTreeExpansionStore();
    store.toggle("github.com/acme");
    expect(store.isCollapsed("github.com/acme")).toBe(true);
    store.toggle("github.com/acme");
    expect(store.isCollapsed("github.com/acme")).toBe(false);
  });

  it("persists collapsed ids to localStorage", () => {
    const store = createRepoTreeExpansionStore();
    store.toggle("github.com/acme");
    expect(JSON.parse(localStorage.getItem(KEY)!)).toEqual(["github.com/acme"]);
  });

  it("reads pre-seeded collapsed ids", () => {
    localStorage.setItem(KEY, JSON.stringify(["github.com/acme"]));
    const store = createRepoTreeExpansionStore();
    expect(store.isCollapsed("github.com/acme")).toBe(true);
  });

  it("falls back to expanded on malformed JSON", () => {
    localStorage.setItem(KEY, "{not json");
    const store = createRepoTreeExpansionStore();
    expect(store.isCollapsed("github.com/acme")).toBe(false);
  });

  it("keeps working in memory when setItem throws", () => {
    const spy = vi
      .spyOn(Storage.prototype, "setItem")
      .mockImplementation(() => {
        throw new Error("QuotaExceededError");
      });
    const store = createRepoTreeExpansionStore();
    expect(() => store.toggle("github.com/acme")).not.toThrow();
    expect(store.isCollapsed("github.com/acme")).toBe(true);
    spy.mockRestore();
  });
});
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd frontend && bun run test src/lib/stores/repoTreeExpansion.test.ts`
Expected: FAIL — module not found.

- [ ] **Step 3: Write minimal implementation**

Create `frontend/src/lib/stores/repoTreeExpansion.svelte.ts`:

```ts
const STORAGE_KEY = "middleman:repoTreeCollapsed";

function readFromStorage(): Set<string> {
  try {
    const raw = localStorage.getItem(STORAGE_KEY);
    if (raw === null) return new Set();
    const parsed: unknown = JSON.parse(raw);
    if (!Array.isArray(parsed)) return new Set();
    return new Set(parsed.filter((v): v is string => typeof v === "string"));
  } catch {
    // localStorage unavailable or corrupt JSON.
    return new Set();
  }
}

function writeToStorage(value: Set<string>): void {
  try {
    localStorage.setItem(STORAGE_KEY, JSON.stringify([...value]));
  } catch {
    // localStorage unavailable (e.g. private browsing quota).
  }
}

export function createRepoTreeExpansionStore() {
  let collapsed = $state<Set<string>>(readFromStorage());

  function isCollapsed(id: string): boolean {
    return collapsed.has(id);
  }

  function toggle(id: string): void {
    const next = new Set(collapsed);
    if (next.has(id)) next.delete(id);
    else next.add(id);
    collapsed = next;
    writeToStorage(next);
  }

  return { isCollapsed, toggle };
}

export type RepoTreeExpansionStore = ReturnType<
  typeof createRepoTreeExpansionStore
>;
```

- [ ] **Step 4: Run test to verify it passes**

Run: `cd frontend && bun run test src/lib/stores/repoTreeExpansion.test.ts`
Expected: PASS (6 tests).

- [ ] **Step 5: Commit**

```bash
git add frontend/src/lib/stores/repoTreeExpansion.svelte.ts frontend/src/lib/stores/repoTreeExpansion.test.ts
git commit -m "feat: persist repo tree collapsed-node state"
```

---

### Task 5: Row presenter component

**Files:**
- Create: `frontend/src/lib/components/RepoTreeNode.svelte`
- Test: `frontend/src/lib/components/RepoTreeNode.test.ts`

This component renders ONE row from a `VisibleRow` plus derived selection state. It is a
dumb presenter: all logic stays in the parent. The checkbox uses a native
`<input type="checkbox">` with `indeterminate` set for the partial state (the
deferred-styling note in the spec explicitly allows this technique).

The label honors the spec's filter match-highlighting: when the parent passes
`segments` (the output of the existing `highlightSegments` helper), the row renders
`<mark>` runs; with no `segments` it renders the plain label. Keeping the highlight in
the presenter is what lets Task 7 keep reusing `highlightSegments` instead of
orphaning it.

- [ ] **Step 1: Write the failing test**

Create `frontend/src/lib/components/RepoTreeNode.test.ts`:

```ts
import { cleanup, fireEvent, render, screen } from "@testing-library/svelte";
import { afterEach, describe, expect, it, vi } from "vitest";

import RepoTreeNode from "./RepoTreeNode.svelte";

afterEach(() => {
  cleanup();
});

describe("RepoTreeNode", () => {
  it("renders a provider icon for host rows", () => {
    render(RepoTreeNode, {
      props: {
        kind: "host",
        label: "github.com",
        ariaLabel: "github.com",
        provider: "github",
        depth: 0,
        hasChildren: true,
        expanded: true,
        selectionState: "unchecked",
        highlighted: false,
        onToggleExpand: vi.fn(),
        onToggleSelect: vi.fn(),
      },
    });
    expect(screen.getByText("github.com")).toBeTruthy();
    expect(document.querySelector(".provider-icon")).toBeTruthy();
  });

  it("marks the checkbox indeterminate for the partial state", () => {
    render(RepoTreeNode, {
      props: {
        kind: "owner",
        label: "acme",
        ariaLabel: "github.com/acme",
        depth: 0,
        hasChildren: true,
        expanded: true,
        selectionState: "partial",
        highlighted: false,
        onToggleExpand: vi.fn(),
        onToggleSelect: vi.fn(),
      },
    });
    const box = screen.getByRole("checkbox") as HTMLInputElement;
    expect(box.indeterminate).toBe(true);
    expect(box.checked).toBe(false);
  });

  it("calls onToggleExpand when the caret is clicked", async () => {
    const onToggleExpand = vi.fn();
    render(RepoTreeNode, {
      props: {
        kind: "owner",
        label: "acme",
        ariaLabel: "github.com/acme",
        depth: 0,
        hasChildren: true,
        expanded: true,
        selectionState: "unchecked",
        highlighted: false,
        onToggleExpand,
        onToggleSelect: vi.fn(),
      },
    });
    await fireEvent.click(screen.getByLabelText("Toggle acme"));
    expect(onToggleExpand).toHaveBeenCalledOnce();
  });

  it("calls onToggleSelect when the checkbox is clicked", async () => {
    const onToggleSelect = vi.fn();
    render(RepoTreeNode, {
      props: {
        kind: "repo",
        label: "api",
        ariaLabel: "github.com/acme/api",
        depth: 1,
        hasChildren: false,
        expanded: false,
        selectionState: "unchecked",
        highlighted: false,
        onToggleExpand: vi.fn(),
        onToggleSelect,
      },
    });
    await fireEvent.mouseDown(screen.getByRole("checkbox"));
    expect(onToggleSelect).toHaveBeenCalledOnce();
  });

  it("renders highlighted match segments when given segments", () => {
    render(RepoTreeNode, {
      props: {
        kind: "repo",
        label: "web-ui",
        ariaLabel: "github.com/acme/web-ui",
        depth: 1,
        hasChildren: false,
        expanded: false,
        selectionState: "unchecked",
        highlighted: false,
        segments: [
          { text: "web", match: true },
          { text: "-ui", match: false },
        ],
        onToggleExpand: vi.fn(),
        onToggleSelect: vi.fn(),
      },
    });
    const mark = document.querySelector("mark");
    expect(mark?.textContent).toBe("web");
  });
});
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd frontend && bun run test src/lib/components/RepoTreeNode.test.ts`
Expected: FAIL — component does not exist.

- [ ] **Step 3: Write minimal implementation**

Create `frontend/src/lib/components/RepoTreeNode.svelte`:

```svelte
<script lang="ts">
  import ProviderIcon from "./provider/ProviderIcon.svelte";
  import type { SelectionState } from "./repoTree.js";

  interface LabelSegment {
    text: string;
    match: boolean;
  }

  interface Props {
    kind: "host" | "owner" | "repo";
    label: string;
    ariaLabel: string;
    provider?: string;
    depth: number;
    hasChildren: boolean;
    expanded: boolean;
    selectionState: SelectionState;
    highlighted: boolean;
    segments?: LabelSegment[];
    onToggleExpand: () => void;
    onToggleSelect: () => void;
    onHover?: () => void;
  }

  let {
    kind,
    label,
    ariaLabel,
    provider,
    depth,
    hasChildren,
    expanded,
    selectionState,
    highlighted,
    segments,
    onToggleExpand,
    onToggleSelect,
    onHover,
  }: Props = $props();

  let checkboxEl = $state<HTMLInputElement>();

  $effect(() => {
    if (checkboxEl) checkboxEl.indeterminate = selectionState === "partial";
  });

  function rowMouseDown() {
    // Name/body click expands interior rows, selects leaves.
    if (hasChildren) onToggleExpand();
    else onToggleSelect();
  }

  function checkboxMouseDown(event: MouseEvent) {
    event.stopPropagation();
    onToggleSelect();
  }

  function caretClick(event: MouseEvent) {
    event.stopPropagation();
    onToggleExpand();
  }
</script>

<li
  class="typeahead-option repo-tree-row"
  class:highlighted
  role="option"
  aria-label={ariaLabel}
  aria-selected={selectionState === "checked"}
  aria-checked={selectionState === "partial"
    ? "mixed"
    : selectionState === "checked"}
  aria-expanded={hasChildren ? expanded : undefined}
  style:padding-left={`${6 + depth * 14}px`}
  onmousedown={rowMouseDown}
  onmouseenter={() => onHover?.()}
>
  {#if hasChildren}
    <button
      class="repo-tree-caret"
      class:expanded
      aria-label={`Toggle ${label}`}
      onclick={caretClick}
      onmousedown={(event) => event.stopPropagation()}
      type="button"
    >
      <svg viewBox="0 0 10 10" width="10" height="10" aria-hidden="true">
        <polyline
          points="2,3 5,7 8,3"
          fill="none"
          stroke="currentColor"
          stroke-width="1.5"
        />
      </svg>
    </button>
  {:else}
    <span class="repo-tree-caret repo-tree-caret--leaf" aria-hidden="true"></span>
  {/if}

  <input
    bind:this={checkboxEl}
    class="typeahead-checkbox"
    type="checkbox"
    checked={selectionState === "checked"}
    tabindex="-1"
    onmousedown={checkboxMouseDown}
  />

  {#if kind === "host" && provider}
    <ProviderIcon {provider} size={14} class="repo-tree-logo" />
  {/if}

  <span class="repo-tree-label">
    {#if segments}{#each segments as seg, segIndex (`${ariaLabel}-${segIndex}-${seg.text}-${seg.match}`)}{#if seg.match}<mark class="match">{seg.text}</mark>{:else}{seg.text}{/if}{/each}{:else}{label}{/if}
  </span>
</li>

<style>
  .repo-tree-row {
    gap: 6px;
  }

  .repo-tree-caret {
    width: 12px;
    height: 12px;
    display: inline-flex;
    align-items: center;
    justify-content: center;
    flex-shrink: 0;
    padding: 0;
    border: none;
    background: none;
    color: var(--text-muted);
    cursor: pointer;
  }

  .repo-tree-caret svg {
    transform: rotate(-90deg);
    transition: transform 0.12s ease;
  }

  .repo-tree-caret.expanded svg {
    transform: rotate(0deg);
  }

  .repo-tree-caret--leaf {
    cursor: default;
  }

  .repo-tree-label {
    min-width: 0;
    overflow: hidden;
    text-overflow: ellipsis;
  }
</style>
```

- [ ] **Step 4: Run test to verify it passes**

Run: `cd frontend && bun run test src/lib/components/RepoTreeNode.test.ts`
Expected: PASS (5 tests).

- [ ] **Step 5: Commit**

```bash
git add frontend/src/lib/components/RepoTreeNode.svelte frontend/src/lib/components/RepoTreeNode.test.ts
git commit -m "feat: add repo tree row presenter component"
```

---

### Task 6: Extend `RepoOption` with provider and platformHost

**Files:**
- Modify: `frontend/src/lib/components/RepoTypeahead.svelte:49` (the `RepoOption` type and the two `optionFrom*` functions)

This is a prep change: thread the two fields the tree builder needs. Behavior is
unchanged, so existing tests stay green; this task is verified by typecheck plus the
existing test suite.

- [ ] **Step 1: Add the import and extend the type**

In `frontend/src/lib/components/RepoTypeahead.svelte`, add to the existing imports near the top of `<script>`:

```ts
import { canonicalProvider } from "@middleman/ui/api/provider-routes";
import type { RepoTreeOption } from "./repoTree.js";
```

Replace the local `RepoOption` type (currently
`type RepoOption = { value: string; owner: string; name: string };`) with an alias of
the builder's input type, so the two never drift and `buildRepoTree(options)` in Task 7
type-checks without a cast:

```ts
type RepoOption = RepoTreeOption;
```

- [ ] **Step 2: Populate the new fields in both factories**

Replace `optionFromRepo` and `optionFromConfigRepo` with:

```ts
function optionFromRepo(repo: Repo): RepoOption {
  return {
    value: `${repo.PlatformHost}/${repo.Owner}/${repo.Name}`,
    owner: repo.Owner,
    name: repo.Name,
    provider: canonicalProvider(repo.Platform),
    platformHost: repo.PlatformHost,
  };
}

function optionFromConfigRepo(repo: ConfigRepo): RepoOption {
  const path = repo.repo_path || `${repo.owner}/${repo.name}`;
  return {
    value: `${repo.platform_host}/${path}`,
    owner: repo.owner,
    name: repo.name,
    provider: canonicalProvider(repo.provider),
    platformHost: repo.platform_host,
  };
}
```

- [ ] **Step 3: Verify typecheck and existing tests pass**

Run: `cd frontend && bun run typecheck`
Expected: no errors.

Run: `cd frontend && bun run test src/lib/components/RepoTypeahead.test.ts`
Expected: PASS (existing suite still green — behavior unchanged).

- [ ] **Step 4: Commit**

```bash
git add frontend/src/lib/components/RepoTypeahead.svelte
git commit -m "feat: carry provider and platform host on repo options"
```

---

### Task 7: Render the tree with mouse selection and filtering

**Files:**
- Modify: `frontend/src/lib/components/RepoTypeahead.svelte` (open-dropdown markup + state)
- Modify: `frontend/src/lib/components/RepoTypeahead.test.ts`

Replace the flat `{#each filtered}` list with the projected tree rows. Keep the trigger
button, the filter input, the "All repos" row, the `.typeahead-*` classes, blur
handling, and the global-filter contract intact. `visibleRows` already applies
prune + auto-expand for the filter, so wiring `query` into it delivers the filter
behavior.

- [ ] **Step 1: Update component tests for the tree (write failing tests)**

In `frontend/src/lib/components/RepoTypeahead.test.ts`, the existing test
`"allows selecting multiple repositories with checkboxes"` already drives leaf rows by
their full-path accessible name and asserts the comma-separated `onchange` format —
keep it as the leaf-selection regression. Add these tests inside the
`describe("RepoTypeahead", ...)` block:

```ts
it("selecting an owner row selects all repos beneath it", async () => {
  const onchange = vi.fn();
  settingsStore.setConfiguredRepos([
    {
      provider: "github",
      platform_host: "github.com",
      owner: "import-lab",
      name: "api",
      repo_path: "import-lab/api",
      is_glob: false,
      matched_repo_count: 1,
    },
    {
      provider: "github",
      platform_host: "github.com",
      owner: "import-lab",
      name: "web",
      repo_path: "import-lab/web",
      is_glob: false,
      matched_repo_count: 1,
    },
  ]);

  render(RepoTypeahead, { props: { selected: undefined, onchange } });

  await fireEvent.click(screen.getByRole("button", { name: /all repos/i }));
  await fireEvent.mouseDown(
    screen.getByRole("option", { name: "github.com/import-lab" }),
  );

  expect(onchange).toHaveBeenLastCalledWith(
    "github.com/import-lab/api,github.com/import-lab/web",
  );
});

it("filters to matching leaves while keeping their owner visible", async () => {
  settingsStore.setConfiguredRepos([
    {
      provider: "github",
      platform_host: "github.com",
      owner: "import-lab",
      name: "api",
      repo_path: "import-lab/api",
      is_glob: false,
      matched_repo_count: 1,
    },
    {
      provider: "github",
      platform_host: "github.com",
      owner: "import-lab",
      name: "web",
      repo_path: "import-lab/web",
      is_glob: false,
      matched_repo_count: 1,
    },
  ]);

  render(RepoTypeahead, {
    props: { selected: undefined, onchange: vi.fn() },
  });

  await fireEvent.click(screen.getByRole("button", { name: /all repos/i }));
  await fireEvent.input(screen.getByPlaceholderText("Filter repos..."), {
    target: { value: "web" },
  });

  await waitFor(() => {
    expect(
      screen.getByRole("option", { name: "github.com/import-lab/web" }),
    ).toBeTruthy();
    expect(
      screen.queryByRole("option", { name: "github.com/import-lab/api" }),
    ).toBeNull();
  });
});
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `cd frontend && bun run test src/lib/components/RepoTypeahead.test.ts`
Expected: FAIL — owner row `github.com/import-lab` is not found / no cascade yet.

- [ ] **Step 3: Wire the tree into the component script**

In `frontend/src/lib/components/RepoTypeahead.svelte`, add to the imports:

```ts
import RepoTreeNode from "./RepoTreeNode.svelte";
import {
  buildRepoTree,
  visibleRows,
  nodeSelectionState,
  toggleSubtree,
  type VisibleRow,
} from "./repoTree.js";
import { createRepoTreeExpansionStore } from "../stores/repoTreeExpansion.svelte.js";
```

Add derived tree state near the other `$derived` declarations (after `filtered` /
`selectedSet` exist):

```ts
const expansion = createRepoTreeExpansionStore();

const tree = $derived(buildRepoTree(options));

const rows = $derived(
  visibleRows(tree, { isCollapsed: expansion.isCollapsed, query }),
);

function rowAriaLabel(row: VisibleRow): string {
  return row.node.kind === "host" ? row.node.platformHost : row.node.id;
}

function toggleRowSelect(row: VisibleRow) {
  onchange(serializeRepoFilterValue(toggleSubtree(row.node, selectedValues)));
}

function toggleRowExpand(row: VisibleRow) {
  if (row.hasChildren) expansion.toggle(row.node.id);
}
```

Note: `row.node.id` for a leaf equals its `value`; `nodeSelectionState(row.node, selectedSet)`
gives the per-row tri-state. `selectedValues` / `selectedSet` / `serializeRepoFilterValue`
already exist in the component.

Leave the existing `handleKeydown`, `filtered`, and `toggleRepo` in place for now — the
old `handleKeydown` still references `filtered`/`toggleRepo`, so removing them here would
break this task's own typecheck. Task 8 replaces `handleKeydown` and removes the dead
declarations together. After this task, `rows` and `filtered` both exist; that is
expected and temporary.

- [ ] **Step 4: Replace the flat list markup with tree rows**

In the open-dropdown block, replace the `{#each filtered as option ...}` list (the
`<ul class="typeahead-list">` body after the "All repos" `<li>`) so the list reads:

```svelte
<ul class="typeahead-list" role="listbox" onmousedown={preventBlur}>
  <li
    class="typeahead-option"
    class:highlighted={highlightIndex === 0}
    class:selected={selectedValues.length === 0}
    role="option"
    aria-selected={selectedValues.length === 0}
    onmousedown={clearSelection}
    onmouseenter={() => (highlightIndex = 0)}
  >
    <input
      class="typeahead-checkbox"
      type="checkbox"
      checked={selectedValues.length === 0}
      tabindex="-1"
      aria-hidden="true"
    />
    <span>All repos</span>
  </li>
  {#each rows as row, i (row.node.id)}
    <RepoTreeNode
      kind={row.node.kind}
      label={row.node.label}
      ariaLabel={rowAriaLabel(row)}
      provider={row.node.kind === "host" ? row.node.provider : undefined}
      depth={row.depth}
      hasChildren={row.hasChildren}
      expanded={row.expanded}
      selectionState={nodeSelectionState(row.node, selectedSet)}
      highlighted={i + 1 === highlightIndex}
      segments={query !== "" && row.node.kind === "repo"
        ? highlightSegments(row.node.label, query)
        : undefined}
      onToggleExpand={() => toggleRowExpand(row)}
      onToggleSelect={() => toggleRowSelect(row)}
      onHover={() => (highlightIndex = i + 1)}
    />
  {:else}
    <li class="typeahead-empty">No matching repos</li>
  {/each}
</ul>
```

- [ ] **Step 5: Run the tests to verify they pass**

Run: `cd frontend && bun run test src/lib/components/RepoTypeahead.test.ts`
Expected: PASS (existing leaf-selection regression + the two new tests).

- [ ] **Step 6: Typecheck**

Run: `cd frontend && bun run typecheck`
Expected: no errors.

- [ ] **Step 7: Commit**

```bash
git add frontend/src/lib/components/RepoTypeahead.svelte frontend/src/lib/components/RepoTypeahead.test.ts
git commit -m "feat: render the repo selector as an expandable tree with subtree selection"
```

---

### Task 8: Keyboard navigation for the tree

**Files:**
- Modify: `frontend/src/lib/components/RepoTypeahead.svelte` (`handleKeydown`, cheatsheet registration)
- Modify: `frontend/src/lib/components/RepoTypeahead.test.ts`

Extend the existing keyboard handler so arrows move across the visible rows,
left/right expand/collapse, and space/enter act contextually. The existing
`ArrowUp`/`ArrowDown`/`Enter`/`Space`/`Escape` handling iterates a flat list today;
update it to iterate `rows`.

- [ ] **Step 1: Write the failing test**

Add to `frontend/src/lib/components/RepoTypeahead.test.ts`:

```ts
it("collapses and expands the focused owner with arrow keys", async () => {
  settingsStore.setConfiguredRepos([
    {
      provider: "github",
      platform_host: "github.com",
      owner: "import-lab",
      name: "api",
      repo_path: "import-lab/api",
      is_glob: false,
      matched_repo_count: 1,
    },
    {
      provider: "github",
      platform_host: "github.com",
      owner: "import-lab",
      name: "web",
      repo_path: "import-lab/web",
      is_glob: false,
      matched_repo_count: 1,
    },
  ]);

  render(RepoTypeahead, { props: { selected: undefined, onchange: vi.fn() } });

  await fireEvent.click(screen.getByRole("button", { name: /all repos/i }));
  const input = screen.getByPlaceholderText("Filter repos...");

  // leaves visible by default
  expect(
    screen.getByRole("option", { name: "github.com/import-lab/api" }),
  ).toBeTruthy();

  // move highlight onto the owner row (index 1) and collapse it
  await fireEvent.keyDown(input, { key: "ArrowDown" });
  await fireEvent.keyDown(input, { key: "ArrowLeft" });

  await waitFor(() => {
    expect(
      screen.queryByRole("option", { name: "github.com/import-lab/api" }),
    ).toBeNull();
  });

  await fireEvent.keyDown(input, { key: "ArrowRight" });
  await waitFor(() => {
    expect(
      screen.getByRole("option", { name: "github.com/import-lab/api" }),
    ).toBeTruthy();
  });
});

it("toggles selection of the focused row with space", async () => {
  const onchange = vi.fn();
  settingsStore.setConfiguredRepos([
    {
      provider: "github",
      platform_host: "github.com",
      owner: "import-lab",
      name: "api",
      repo_path: "import-lab/api",
      is_glob: false,
      matched_repo_count: 1,
    },
    {
      provider: "github",
      platform_host: "github.com",
      owner: "import-lab",
      name: "web",
      repo_path: "import-lab/web",
      is_glob: false,
      matched_repo_count: 1,
    },
  ]);

  render(RepoTypeahead, { props: { selected: undefined, onchange } });

  await fireEvent.click(screen.getByRole("button", { name: /all repos/i }));
  const input = screen.getByPlaceholderText("Filter repos...");

  // highlight the owner row and select its subtree
  await fireEvent.keyDown(input, { key: "ArrowDown" });
  await fireEvent.keyDown(input, { key: " " });

  expect(onchange).toHaveBeenLastCalledWith(
    "github.com/import-lab/api,github.com/import-lab/web",
  );
});
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `cd frontend && bun run test src/lib/components/RepoTypeahead.test.ts`
Expected: FAIL — arrow-left/right do not expand/collapse; space on owner does not cascade.

- [ ] **Step 3: Replace `handleKeydown`**

Replace the existing `handleKeydown` function with:

```ts
function handleKeydown(e: KeyboardEvent) {
  const total = rows.length + 1; // +1 for the "All repos" row at index 0
  if (e.key === "ArrowDown") {
    e.preventDefault();
    highlightIndex = Math.min(highlightIndex + 1, total - 1);
  } else if (e.key === "ArrowUp") {
    e.preventDefault();
    highlightIndex = Math.max(highlightIndex - 1, 0);
  } else if (e.key === "ArrowRight") {
    e.preventDefault();
    const row = rows[highlightIndex - 1];
    if (row?.hasChildren && !row.expanded) expansion.toggle(row.node.id);
  } else if (e.key === "ArrowLeft") {
    e.preventDefault();
    const row = rows[highlightIndex - 1];
    if (row?.hasChildren && row.expanded) expansion.toggle(row.node.id);
  } else if (e.key === "Enter") {
    e.preventDefault();
    if (highlightIndex === 0) {
      clearSelection();
      return;
    }
    const row = rows[highlightIndex - 1];
    if (!row) return;
    if (row.hasChildren) expansion.toggle(row.node.id);
    else toggleRowSelect(row);
  } else if (e.key === " ") {
    e.preventDefault();
    if (highlightIndex === 0) {
      clearSelection();
      return;
    }
    const row = rows[highlightIndex - 1];
    if (row) toggleRowSelect(row);
  } else if (e.key === "Escape") {
    closeDropdown();
  }
}
```

With this replacement, `filtered` and `toggleRepo` are no longer referenced anywhere.
Delete both — the `filtered` `$derived.by(...)` block and the `toggleRepo` function —
so `bun run typecheck` (`svelte-check --fail-on-warnings`) and `bun run lint` stay
clean. Keep `highlightSegments` (now feeds the leaf `segments` prop from Task 7),
`handleInput`, `query`, `selectedValues`, and `selectedSet`.

- [ ] **Step 4: Add cheatsheet entries for the new bindings**

In the `registerCheatsheetEntries("repo-typeahead", [...])` array (inside `onMount`),
add two entries after the existing `next`/`prev` entries:

```ts
{
  id: "repo-typeahead.expand",
  label: "Expand / collapse group",
  binding: { key: "ArrowRight" },
  scope: "view-pulls",
},
{
  id: "repo-typeahead.toggle-select",
  label: "Select / deselect",
  binding: { key: " " },
  scope: "view-pulls",
},
```

- [ ] **Step 5: Run the tests to verify they pass**

Run: `cd frontend && bun run test src/lib/components/RepoTypeahead.test.ts`
Expected: PASS (all RepoTypeahead behavior tests).

Run: `cd frontend && bun run test src/lib/components/RepoTypeahead.svelte.test.ts`
Expected: PASS (the separate cheatsheet-registration test file — the two new entries register without disturbing the existing `next`/`prev` assertions).

- [ ] **Step 6: Typecheck**

Run: `cd frontend && bun run typecheck`
Expected: no errors.

- [ ] **Step 7: Commit**

```bash
git add frontend/src/lib/components/RepoTypeahead.svelte frontend/src/lib/components/RepoTypeahead.test.ts
git commit -m "feat: add keyboard navigation and selection to the repo tree"
```

---

### Task 9: End-to-end group selection

**Files:**
- Modify: `frontend/tests/e2e-full/repo-filter-multiselect.spec.ts`

Add a test that opens the selector, selects an owner group, and asserts the PR/issue
list filters to that group's repos against the real backend. The existing single-leaf
multiselect test in this file stays as the leaf regression.

- [ ] **Step 1: Inspect the available fixture repos**

Run: `cd frontend && rg -n "acme/|getByRole\\(\"option\"" tests/e2e-full/repo-filter-multiselect.spec.ts`
Expected: shows the existing fixtures used (`github.com/acme/widgets`, `github.com/acme/tools`) and the option-based selection helper.

- [ ] **Step 2: Add the group-select e2e test**

Append to `frontend/tests/e2e-full/repo-filter-multiselect.spec.ts`:

```ts
test("selecting an owner group filters lists to that group's repos", async ({
  page,
}) => {
  await page.goto("/issues");
  await waitForIssueList(page);

  const selector = page.getByTitle("Select repository");
  await selector.click();

  // The owner row's accessible name is the full `${platformHost}/${owner}` path.
  const ownerRow = page.getByRole("option", { name: "github.com/acme" });
  await expect(ownerRow).toBeVisible();
  await ownerRow.click();

  await page.keyboard.press("Escape");

  // Both acme repos end up in the persisted filter set.
  await expect(
    page.evaluate(() => localStorage.getItem("middleman-filter-repo")),
  ).resolves.toContain("github.com/acme/widgets");
  await expect(
    page.evaluate(() => localStorage.getItem("middleman-filter-repo")),
  ).resolves.toContain("github.com/acme/tools");

  await expect(page.getByText("GitLab read-only issue")).toHaveCount(0);
});
```

If the `acme` owner is auto-flattened (only one acme repo in the fixture) or the host
node is shown (more than one host in the fixture), adjust the accessible name to the
actual owner/host path reported by `page.getByRole("option")` — run the test once and
read the failure's available options. Do not change the assertion that the persisted
set contains the group's repos.

- [ ] **Step 3: Run the e2e spec**

Run: `cd frontend && bun run test:e2e -- repo-filter-multiselect`
Expected: PASS (existing multiselect test + the new group-select test).

- [ ] **Step 4: Commit**

```bash
git add frontend/tests/e2e-full/repo-filter-multiselect.spec.ts
git commit -m "test: cover owner-group selection in the repo tree end-to-end"
```

---

### Task 10: Full regression sweep

**Files:** none (verification only)

- [ ] **Step 1: Run the full frontend unit suite**

Run: `cd frontend && bun run test`
Expected: PASS. Pay attention to the selector-adjacent specs (`container-layout`,
`settings-globs`, `mobile-activity-repos`, `link-navigation-repo-sync`,
`embedded-config`) — they rely on `.typeahead-*` classes and option roles that this
plan preserves.

- [ ] **Step 2: Run lint and typecheck**

Run: `cd frontend && bun run lint`
Expected: no errors.

Run: `cd frontend && bun run typecheck`
Expected: no errors.

- [ ] **Step 3: Run the affected e2e specs**

Run: `cd frontend && bun run test:e2e -- repo-filter-multiselect container-layout settings-globs link-navigation-repo-sync`
Expected: PASS.

- [ ] **Step 4: Commit any lint fixes**

If steps 1-3 required fixes, commit them:

```bash
git add -A
git commit -m "fix: resolve lint and test fallout from repo tree nav"
```

If nothing changed, skip this commit.
