# Kata Task Column Visibility Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add a Kata task-list column picker that hides optional metadata columns immediately and restores the browser-local choice after reload.

**Architecture:** A focused TypeScript helper owns the allowlisted column model and best-effort `localStorage` serialization. A small Svelte popover component owns checkbox interaction and focus-safe dismissal. `KataIssueList.svelte` owns the active visibility state, conditionally renders headers and cells, and derives wide and responsive grid templates from the same state.

**Tech Stack:** Svelte 5 runes, TypeScript, `@kenn-io/kit-ui` popover utilities and Checkbox, Testing Library, Vite+ unit/browser tests, Playwright full-stack tests.

## Global Constraints

- ID and Title remain permanently visible.
- Updated, Priority, Due, Owner, and Tags are independently hideable.
- One browser-local preference applies across every Kata view, project scope, and daemon.
- Enabled columns may still auto-hide at the existing pane-width breakpoints; disabled columns never appear.
- Hiding the active Updated, Priority, or Owner sort resets sorting to Title ascending; other sorting, row selection, tree expansion, task detail behavior, and backend contracts remain unchanged.
- Storage failures are non-fatal and malformed values fall back to all optional columns visible.
- Compact panes keep header actions on one line by visually hiding action labels while retaining icons, titles, and accessible names.
- Browser-tab synchronization is out of scope.
- Use Bun and Vite+ tooling. Never use npm.
- Run the Svelte autofixer on every changed `.svelte` file before completion.

---

## File Map

- Create `frontend/src/lib/components/kata/kataTaskColumns.ts`: column identifiers, labels, defaults, parsing, loading, and persistence.
- Create `frontend/src/lib/components/kata/kataTaskColumns.test.ts`: pure persistence and normalization coverage.
- Create `frontend/src/lib/components/kata/KataColumnPicker.svelte`: trigger, checkbox popover, Show all action, dismissal, and focus restoration.
- Modify `frontend/src/lib/components/kata/KataIssueList.svelte`: state ownership, conditional cells, responsive grid tracks, and header composition.
- Modify `frontend/src/lib/components/kata/KataIssueList.test.ts`: user-visible toggle, restore, reset, fixed-column, and storage-failure coverage.
- Modify `frontend/src/lib/components/kata/KataIssueList.browser.svelte.ts`: keyboard/focus semantics in a real browser.
- Modify `frontend/tests/e2e-full/kata.spec.ts`: floating placement and responsive grid geometry against the seeded backend.

### Task 1: Define and test the persisted column model

**Files:**
- Create: `frontend/src/lib/components/kata/kataTaskColumns.ts`
- Create: `frontend/src/lib/components/kata/kataTaskColumns.test.ts`

**Interfaces:**
- Produces: `KataOptionalTaskColumn`, `KataTaskColumnVisibility`, `KATA_OPTIONAL_TASK_COLUMNS`, `KATA_TASK_COLUMNS_STORAGE_KEY`, `defaultKataTaskColumnVisibility()`, `loadKataTaskColumnVisibility()`, and `persistKataTaskColumnVisibility()`.
- Consumes: browser `localStorage` through the narrow `Pick<Storage, "getItem" | "setItem">` interface.

- [ ] **Step 1: Write failing persistence tests**

Create `kataTaskColumns.test.ts` with these cases:

```ts
import { describe, expect, it, vi } from "vite-plus/test";

import {
  KATA_OPTIONAL_TASK_COLUMNS,
  KATA_TASK_COLUMNS_STORAGE_KEY,
  defaultKataTaskColumnVisibility,
  loadKataTaskColumnVisibility,
  persistKataTaskColumnVisibility,
} from "./kataTaskColumns.js";

describe("Kata task column visibility", () => {
  it("defaults every optional column to visible", () => {
    expect(defaultKataTaskColumnVisibility()).toEqual({
      updated: true,
      priority: true,
      due: true,
      owner: true,
      tags: true,
    });
  });

  it("restores known columns in canonical order and ignores unknown keys", () => {
    const storage = {
      getItem: vi.fn(() => JSON.stringify(["tags", "future-column", "updated", "tags"])),
      setItem: vi.fn(),
    };

    expect(loadKataTaskColumnVisibility(storage)).toEqual({
      updated: true,
      priority: false,
      due: false,
      owner: false,
      tags: true,
    });
  });

  it.each(["not-json", JSON.stringify({ visible: ["updated"] }), JSON.stringify(["updated", 3])])(
    "falls back for malformed storage: %s",
    (raw) => {
      const storage = { getItem: vi.fn(() => raw), setItem: vi.fn() };
      expect(loadKataTaskColumnVisibility(storage)).toEqual(defaultKataTaskColumnVisibility());
    },
  );

  it("falls back when storage reads throw", () => {
    const storage = {
      getItem: vi.fn(() => {
        throw new Error("blocked");
      }),
      setItem: vi.fn(),
    };
    expect(loadKataTaskColumnVisibility(storage)).toEqual(defaultKataTaskColumnVisibility());
  });

  it("persists visible keys and tolerates write failures", () => {
    const setItem = vi.fn();
    persistKataTaskColumnVisibility(
      { updated: true, priority: false, due: true, owner: false, tags: false },
      { getItem: vi.fn(), setItem },
    );
    expect(setItem).toHaveBeenCalledWith(KATA_TASK_COLUMNS_STORAGE_KEY, JSON.stringify(["updated", "due"]));

    expect(() =>
      persistKataTaskColumnVisibility(defaultKataTaskColumnVisibility(), {
        getItem: vi.fn(),
        setItem: vi.fn(() => {
          throw new Error("quota");
        }),
      }),
    ).not.toThrow();
    expect(KATA_OPTIONAL_TASK_COLUMNS.map((column) => column.id)).toEqual([
      "updated",
      "priority",
      "due",
      "owner",
      "tags",
    ]);
  });
});
```

- [ ] **Step 2: Run the new test and verify the red state**

Run from `frontend/`:

```bash
node ../node_modules/vite-plus/bin/vp test run --project unit src/lib/components/kata/kataTaskColumns.test.ts
```

Expected: FAIL because `./kataTaskColumns.js` does not exist.

- [ ] **Step 3: Implement the column model and storage boundary**

Create `kataTaskColumns.ts`:

```ts
export const KATA_TASK_COLUMNS_STORAGE_KEY = "middleman:kata:issue-columns/v1";

export const KATA_OPTIONAL_TASK_COLUMNS = [
  { id: "updated", label: "Updated" },
  { id: "priority", label: "Priority" },
  { id: "due", label: "Due" },
  { id: "owner", label: "Owner" },
  { id: "tags", label: "Tags" },
] as const;

export type KataOptionalTaskColumn = (typeof KATA_OPTIONAL_TASK_COLUMNS)[number]["id"];
export type KataTaskColumnVisibility = Record<KataOptionalTaskColumn, boolean>;
type ColumnStorage = Pick<Storage, "getItem" | "setItem">;

const knownColumns = new Set<string>(KATA_OPTIONAL_TASK_COLUMNS.map((column) => column.id));

export function defaultKataTaskColumnVisibility(): KataTaskColumnVisibility {
  return { updated: true, priority: true, due: true, owner: true, tags: true };
}

function browserStorage(): ColumnStorage | null {
  if (typeof window === "undefined") return null;
  try {
    return window.localStorage;
  } catch {
    return null;
  }
}

export function loadKataTaskColumnVisibility(
  storage: ColumnStorage | null = browserStorage(),
): KataTaskColumnVisibility {
  if (!storage) return defaultKataTaskColumnVisibility();
  try {
    const raw = storage.getItem(KATA_TASK_COLUMNS_STORAGE_KEY);
    if (raw === null) return defaultKataTaskColumnVisibility();
    const parsed: unknown = JSON.parse(raw);
    if (!Array.isArray(parsed) || !parsed.every((value) => typeof value === "string")) {
      return defaultKataTaskColumnVisibility();
    }
    const visible = new Set(parsed.filter((value) => knownColumns.has(value)));
    return Object.fromEntries(
      KATA_OPTIONAL_TASK_COLUMNS.map((column) => [column.id, visible.has(column.id)]),
    ) as KataTaskColumnVisibility;
  } catch {
    return defaultKataTaskColumnVisibility();
  }
}

export function persistKataTaskColumnVisibility(
  visibility: KataTaskColumnVisibility,
  storage: ColumnStorage | null = browserStorage(),
): void {
  if (!storage) return;
  try {
    const visible = KATA_OPTIONAL_TASK_COLUMNS
      .filter((column) => visibility[column.id])
      .map((column) => column.id);
    storage.setItem(KATA_TASK_COLUMNS_STORAGE_KEY, JSON.stringify(visible));
  } catch {
    // Browser storage is best-effort. Keep the in-memory preference usable.
  }
}
```

- [ ] **Step 4: Run the focused test and verify green**

Run the Task 1 test command again.

Expected: PASS with seven test cases, including the three malformed-value table entries.

- [ ] **Step 5: Commit the persisted model**

Invoke the repository-local `context-sync` skill with `--commit`, then invoke the mandatory commit skill and create a commit with subject:

```text
feat: remember Kata task column choices
```

Stage only the two Task 1 files.

### Task 2: Add the column picker and integrate visible grid tracks

**Files:**
- Create: `frontend/src/lib/components/kata/KataColumnPicker.svelte`
- Modify: `frontend/src/lib/components/kata/KataIssueList.svelte:1-168`
- Modify: `frontend/src/lib/components/kata/KataIssueList.svelte:623-846`
- Modify: `frontend/src/lib/components/kata/KataIssueList.svelte:861-1379`
- Modify: `frontend/src/lib/components/kata/KataIssueList.test.ts:66-210`

**Interfaces:**
- Consumes: the Task 1 column constants, visibility type, default factory, loader, and persister.
- Produces: `KataColumnPicker` props `{ visibility, onchange, onShowAll }` and responsive `--table-cols-*` custom properties used by both header and rows.

- [ ] **Step 1: Write failing task-list interaction tests**

Import `KATA_TASK_COLUMNS_STORAGE_KEY` into `KataIssueList.test.ts`, then append these focused cases inside the existing `describe("KataIssueList", ...)` block:

```ts
it("hides an optional column while keeping ID and Title visible", async () => {
  render(KataIssueList, {
    props: { currentView, selectedIssueUID: null, loading: false, onSelect: () => {} },
  });

  const header = screen.getByRole("row");
  const row = screen.getByText("Pay rent").closest("button");
  const table = document.querySelector<HTMLElement>(".table");
  expect(row).not.toBeNull();
  expect(table).not.toBeNull();
  expect(table!.style.getPropertyValue("--table-cols-wide")).toContain("minmax(96px, 200px)");

  await fireEvent.click(screen.getByRole("button", { name: "Columns" }));
  await fireEvent.click(screen.getByRole("checkbox", { name: "Tags" }));

  expect(within(header).getByText("ID")).toBeTruthy();
  expect(within(header).getByText("Title")).toBeTruthy();
  expect(within(header).queryByText("Tags")).toBeNull();
  expect(within(row!).queryByText("home · monthly")).toBeNull();
  expect(table!.style.getPropertyValue("--table-cols-wide")).not.toContain("minmax(96px, 200px)");
  expect(JSON.parse(localStorage.getItem(KATA_TASK_COLUMNS_STORAGE_KEY)!)).toEqual([
    "updated",
    "priority",
    "due",
    "owner",
  ]);
});

it("restores hidden columns and Show all resets the preference", async () => {
  const first = render(KataIssueList, {
    props: { currentView, selectedIssueUID: null, loading: false, onSelect: () => {} },
  });

  await fireEvent.click(screen.getByRole("button", { name: "Columns" }));
  for (const name of ["Priority", "Due", "Owner", "Tags"]) {
    await fireEvent.click(screen.getByRole("checkbox", { name }));
  }
  first.unmount();

  render(KataIssueList, {
    props: { currentView, selectedIssueUID: null, loading: false, onSelect: () => {} },
  });

  const header = screen.getByRole("row");
  expect(within(header).getByText("Updated")).toBeTruthy();
  expect(within(header).queryByText("Priority")).toBeNull();
  expect(within(header).queryByText("Due")).toBeNull();
  expect(within(header).queryByText("Owner")).toBeNull();
  expect(within(header).queryByText("Tags")).toBeNull();

  await fireEvent.click(screen.getByRole("button", { name: "Columns" }));
  await fireEvent.click(screen.getByRole("button", { name: "Show all" }));

  expect(within(header).getByText("Priority")).toBeTruthy();
  expect(within(header).getByText("Due")).toBeTruthy();
  expect(within(header).getByText("Owner")).toBeTruthy();
  expect(within(header).getByText("Tags")).toBeTruthy();
  expect(JSON.parse(localStorage.getItem(KATA_TASK_COLUMNS_STORAGE_KEY)!)).toEqual([
    "updated",
    "priority",
    "due",
    "owner",
    "tags",
  ]);
});

it("keeps column toggles usable when localStorage writes fail", async () => {
  vi.spyOn(Storage.prototype, "setItem").mockImplementation(() => {
    throw new Error("quota");
  });
  render(KataIssueList, {
    props: { currentView, selectedIssueUID: null, loading: false, onSelect: () => {} },
  });

  await fireEvent.click(screen.getByRole("button", { name: "Columns" }));
  await fireEvent.click(screen.getByRole("checkbox", { name: "Owner" }));

  expect(within(screen.getByRole("row")).queryByText("Owner")).toBeNull();
});

it("falls back to all columns for malformed saved data", () => {
  localStorage.setItem(KATA_TASK_COLUMNS_STORAGE_KEY, JSON.stringify({ visible: ["updated"] }));
  render(KataIssueList, {
    props: { currentView, selectedIssueUID: null, loading: false, onSelect: () => {} },
  });

  const header = screen.getByRole("row");
  for (const name of ["Updated", "Priority", "Due", "Owner", "Tags"]) {
    expect(within(header).getByText(name)).toBeTruthy();
  }
});

it("closes the column picker with Escape and restores trigger focus", async () => {
  render(KataIssueList, {
    props: { currentView, selectedIssueUID: null, loading: false, onSelect: () => {} },
  });

  const trigger = screen.getByRole("button", { name: "Columns" });
  await fireEvent.click(trigger);
  expect(screen.getByRole("checkbox", { name: "Updated" })).toBeTruthy();

  await fireEvent.keyDown(document, { key: "Escape" });

  expect(screen.queryByRole("checkbox", { name: "Updated" })).toBeNull();
  expect(document.activeElement).toBe(trigger);
});

it("resets an invisible active sort to Title ascending", async () => {
  render(KataIssueList, {
    props: { currentView, selectedIssueUID: null, loading: false, onSelect: () => {} },
  });

  await fireEvent.click(screen.getByRole("button", { name: "Sort by Priority" }));
  expect(screen.getByRole("button", { name: "Sort by Priority, currently ascending" })).toHaveAttribute("aria-pressed", "true");

  await fireEvent.click(screen.getByRole("button", { name: "Columns" }));
  await fireEvent.click(screen.getByRole("checkbox", { name: "Priority" }));

  expect(screen.queryByRole("button", { name: /Sort by Priority/ })).toBeNull();
  expect(screen.getByRole("button", { name: "Sort by Title, currently ascending" })).toHaveAttribute("aria-pressed", "true");
  expect(JSON.parse(localStorage.getItem("middleman:kata:issue-sort/v1")!)).toEqual({
    key: "title",
    direction: "asc",
  });
});
```

- [ ] **Step 2: Run the Kata issue-list test and verify red**

Run from `frontend/`:

```bash
node ../node_modules/vite-plus/bin/vp test run --project unit src/lib/components/kata/KataIssueList.test.ts
```

Expected: FAIL because the Columns trigger and conditional rendering do not exist.

- [ ] **Step 3: Create the checkbox popover component**

Create `KataColumnPicker.svelte` using:

```svelte
<script lang="ts">
  import Columns3Icon from "@lucide/svelte/icons/columns-3";
  import { Checkbox, autoReposition, dismissable, floatingPopoverStyle } from "@kenn-io/kit-ui";
  import { tick } from "svelte";
  import {
    KATA_OPTIONAL_TASK_COLUMNS,
    type KataTaskColumnVisibility,
  } from "./kataTaskColumns.js";

  interface Props {
    visibility: KataTaskColumnVisibility;
    onchange: (visibility: KataTaskColumnVisibility) => void;
    onShowAll: () => void;
  }

  let { visibility, onchange, onShowAll }: Props = $props();
  let open = $state(false);
  let trigger = $state<HTMLButtonElement>();
  let panel = $state<HTMLDivElement>();
  let panelStyle = $state("");

  const allVisible = $derived(KATA_OPTIONAL_TASK_COLUMNS.every((column) => visibility[column.id]));

  $effect(() => {
    if (!open) return;
    const cleanups = [
      dismissable({ owners: () => [trigger, panel], dismiss: close, escapeFocus: () => trigger }),
      autoReposition(() => panel, position),
    ];
    return () => cleanups.forEach((cleanup) => cleanup());
  });

  function close(): void {
    open = false;
  }

  function position(): void {
    if (!trigger || !panel) return;
    panelStyle = floatingPopoverStyle({
      trigger: trigger.getBoundingClientRect(),
      viewportWidth: window.innerWidth,
      viewportHeight: window.innerHeight,
      popoverWidth: panel.offsetWidth,
      popoverHeight: panel.offsetHeight,
      align: "end",
    });
  }

  async function toggle(): Promise<void> {
    open = !open;
    if (!open) return;
    await tick();
    position();
  }
</script>

<div class="column-picker">
  <button
    bind:this={trigger}
    type="button"
    aria-label="Columns"
    title="Choose visible columns"
    aria-expanded={open}
    onclick={() => void toggle()}
  >
    <Columns3Icon size={13} strokeWidth={2} aria-hidden="true" />
    <span class="action-label">Columns</span>
  </button>
  {#if open}
    <div bind:this={panel} class="column-picker__panel kit-popover-card" style={panelStyle}>
      <div class="column-picker__title">Visible columns</div>
      {#each KATA_OPTIONAL_TASK_COLUMNS as column (column.id)}
        <Checkbox
          checked={visibility[column.id]}
          label={column.label}
          onchange={(checked) => onchange({ ...visibility, [column.id]: checked })}
        />
      {/each}
      <button type="button" class="column-picker__reset" disabled={allVisible} onclick={onShowAll}>
        Show all
      </button>
    </div>
  {/if}
</div>

<style>
  .column-picker {
    position: relative;
    flex-shrink: 0;
  }

  .column-picker > button {
    display: inline-flex;
    align-items: center;
    justify-content: center;
    gap: var(--space-2);
    min-height: 26px;
    padding: 0 var(--space-4);
    border: 1px solid var(--border-default);
    border-radius: var(--radius-sm);
    background: var(--bg-surface);
    color: var(--text-secondary);
    font: inherit;
    font-size: var(--font-size-xs);
    font-weight: 500;
    white-space: nowrap;
    cursor: pointer;
  }

  .column-picker > button:hover,
  .column-picker > button:focus-visible,
  .column-picker > button[aria-expanded="true"] {
    border-color: var(--border-strong);
    color: var(--text-primary);
  }

  .column-picker__panel {
    position: fixed;
    z-index: var(--z-popover);
    display: flex;
    flex-direction: column;
    gap: var(--space-4);
    min-width: 180px;
    max-width: calc(100vw - 16px);
    max-height: calc(100vh - 16px);
    overflow-y: auto;
    padding: var(--space-5);
  }

  .column-picker__title {
    color: var(--text-faint);
    font-size: var(--font-size-3xs);
    font-weight: 600;
    letter-spacing: 0.08em;
    text-transform: uppercase;
  }

  .column-picker__reset {
    margin-top: var(--space-2);
    padding: var(--space-3) var(--space-4) 0;
    border: 0;
    border-top: 1px solid var(--border-muted);
    background: transparent;
    color: var(--accent-blue);
    font: inherit;
    font-size: var(--font-size-xs);
    text-align: left;
    cursor: pointer;
  }

  .column-picker__reset:disabled {
    color: var(--text-faint);
    cursor: default;
  }
</style>
```

Use `kit-popover-card` for shared surface chrome. Do not add a modal or a second overlay implementation.

- [ ] **Step 4: Integrate visibility state and conditional DOM**

In `KataIssueList.svelte`:

1. Import `KataColumnPicker` and the Task 1 helper.
2. Initialize `let columnVisibility = $state(loadKataTaskColumnVisibility());`.
3. Add `setColumnVisibility(next)` and `showAllColumns()` functions that update state first and then persist it.
4. Add a `.header-actions` wrapper that always renders `KataColumnPicker`; keep `.tree-actions` conditional inside it.
5. Wrap each optional header and matching row cell in `{#if columnVisibility.<id>}` blocks. Leave ID and Title unconditional.

Use these state transitions so an optional sort cannot remain active after its control disappears:

```ts
let columnVisibility = $state(loadKataTaskColumnVisibility());

function optionalColumnForSort(key: KataTaskSortKey): KataOptionalTaskColumn | null {
  if (key === "updated" || key === "priority" || key === "owner") return key;
  return null;
}

function setColumnVisibility(next: KataTaskColumnVisibility): void {
  const activeSortColumn = optionalColumnForSort(sort.key);
  if (activeSortColumn && !next[activeSortColumn]) {
    sort = { key: "title", direction: "asc" };
    persistSort(sort);
  }
  columnVisibility = next;
  persistKataTaskColumnVisibility(next);
}

function showAllColumns(): void {
  setColumnVisibility(defaultKataTaskColumnVisibility());
}
```

Replace the conditional top-right action block with this composition:

```svelte
<div class="header-actions">
  <KataColumnPicker
    visibility={columnVisibility}
    onchange={setColumnVisibility}
    onShowAll={showAllColumns}
  />
  {#if hasExpandableVisibleRows || hasAnyExpandedRows || bulkExpanding}
    <div class="tree-actions" aria-label="Task tree controls">
      <button
        class="tree-action"
        type="button"
        aria-label={bulkExpanding ? "Expanding tasks" : "Expand all tasks"}
        title={bulkExpanding ? "Expanding tasks" : "Expand all tasks"}
        disabled={bulkExpanding || allKnownExpandableRowsExpanded}
        aria-busy={bulkExpanding ? "true" : undefined}
        onclick={() => void expandAllVisible()}
      >
        <ListChevronsUpDownIcon size={13} strokeWidth={2} />
        <span class="action-label">{bulkExpanding ? "Expanding" : "Expand all"}</span>
      </button>
      <button
        class="tree-action"
        type="button"
        aria-label="Collapse all tasks"
        title="Collapse all tasks"
        disabled={!hasAnyExpandedRows}
        onclick={collapseAllVisible}
      >
        <ListChevronsDownUpIcon size={13} strokeWidth={2} />
        <span class="action-label">Collapse all</span>
      </button>
    </div>
  {/if}
</div>
```

Use this responsive grid derivation in the script:

```ts
type TaskGridLayout = "wide" | "medium" | "compact" | "narrow";

const TASK_COLUMN_TRACKS: Record<
  TaskGridLayout,
  Record<KataOptionalTaskColumn, string | null>
> = {
  wide: {
    updated: "minmax(64px, 80px)",
    priority: "minmax(68px, 80px)",
    due: "minmax(56px, 70px)",
    owner: "minmax(72px, 110px)",
    tags: "minmax(96px, 200px)",
  },
  medium: {
    updated: "minmax(64px, 80px)",
    priority: "minmax(68px, 80px)",
    due: "minmax(56px, 70px)",
    owner: "minmax(72px, 110px)",
    tags: null,
  },
  compact: {
    updated: "minmax(60px, 76px)",
    priority: "minmax(64px, 78px)",
    due: "minmax(54px, 68px)",
    owner: null,
    tags: null,
  },
  narrow: {
    updated: "minmax(58px, 72px)",
    priority: "minmax(62px, 74px)",
    due: null,
    owner: null,
    tags: null,
  },
};

const TASK_TITLE_TRACKS: Record<TaskGridLayout, string> = {
  wide: "minmax(220px, 1fr)",
  medium: "minmax(220px, 1fr)",
  compact: "minmax(180px, 1fr)",
  narrow: "minmax(140px, 1fr)",
};

function taskGridColumns(layout: TaskGridLayout): string {
  return [
    "var(--table-id-col)",
    TASK_TITLE_TRACKS[layout],
    ...KATA_OPTIONAL_TASK_COLUMNS.flatMap((column) => {
      const track = TASK_COLUMN_TRACKS[layout][column.id];
      return columnVisibility[column.id] && track ? [track] : [];
    }),
  ].join(" ");
}

let wideGridColumns = $derived(taskGridColumns("wide"));
let mediumGridColumns = $derived(taskGridColumns("medium"));
let compactGridColumns = $derived(taskGridColumns("compact"));
let narrowGridColumns = $derived(taskGridColumns("narrow"));
```

Publish the four strings on `.table` with Svelte style directives, set `--table-cols: var(--table-cols-wide)` in the base rule, and replace the three hard-coded container-query templates with `var(--table-cols-medium)`, `var(--table-cols-compact)`, and `var(--table-cols-narrow)`. Keep the existing auto-hide selectors so enabled DOM cells disappear at the same breakpoints as their omitted tracks.

```svelte
<div
  class="table"
  class:table--project-scoped={isProjectScoped}
  style:--table-cols-wide={wideGridColumns}
  style:--table-cols-medium={mediumGridColumns}
  style:--table-cols-compact={compactGridColumns}
  style:--table-cols-narrow={narrowGridColumns}
>
```

```css
.header-actions {
  display: flex;
  align-items: center;
  gap: var(--space-3);
  flex-shrink: 0;
  white-space: nowrap;
}

.table {
  --table-cols: var(--table-cols-wide);
}

@container list (max-width: 880px) {
  .table { --table-cols: var(--table-cols-medium); }
}

@container list (max-width: 680px) {
  .table { --table-cols: var(--table-cols-compact); }
  .header-actions :global(.action-label) {
    position: absolute;
    width: 1px;
    height: 1px;
    overflow: hidden;
    clip: rect(0 0 0 0);
    white-space: nowrap;
    clip-path: inset(50%);
  }
}

@container list (max-width: 520px) {
  .table { --table-cols: var(--table-cols-narrow); }
}
```

- [ ] **Step 5: Run the Svelte autofixer on both changed components**

Run from the repository root:

```bash
cd frontend
../node_modules/.bin/vp exec -- svelte-mcp svelte-autofixer src/lib/components/kata/KataColumnPicker.svelte
../node_modules/.bin/vp exec -- svelte-mcp svelte-autofixer src/lib/components/kata/KataIssueList.svelte
```

Expected: no unresolved Svelte findings. Apply any safe suggested corrections with `apply_patch`, then rerun until clean.

- [ ] **Step 6: Run the focused tests and verify green**

Run from `frontend/`:

```bash
node ../node_modules/vite-plus/bin/vp test run --project unit \
  src/lib/components/kata/kataTaskColumns.test.ts \
  src/lib/components/kata/KataIssueList.test.ts
```

Expected: PASS for both files with no warnings.

- [ ] **Step 7: Extend real-browser keyboard and focus coverage**

Add this case to `KataIssueList.browser.svelte.ts`:

```ts
it("opens the column picker from the keyboard and restores focus on Escape", async () => {
  await page.viewport(980, 620);
  const issue = task();
  render(KataIssueList, {
    props: {
      currentView: currentView(issue),
      selectedIssueUID: issue.uid,
      loading: false,
      onSelect: () => {},
    },
  });

  const trigger = page.getByRole("button", { name: "Columns" });
  await trigger.focus();
  await trigger.press("Enter");
  await expect.element(trigger).toHaveAttribute("aria-expanded", "true");
  const updated = page.getByRole("checkbox", { name: "Updated" });
  await expect.element(updated).toBeVisible();

  await updated.press("Escape");

  await expect.element(trigger).toHaveAttribute("aria-expanded", "false");
  await expect.element(trigger).toHaveFocus();
});
```

Run from `frontend/`:

```bash
node ../node_modules/vite-plus/bin/vp test run --project browser src/lib/components/kata/KataIssueList.browser.svelte.ts
```

Expected: PASS in Chromium.

- [ ] **Step 8: Add full-stack Playwright geometry coverage**

Add a focused test near the existing Kata task-list sorting tests in `frontend/tests/e2e-full/kata.spec.ts`. Use `startKataBackend`, `configureKataHome`, and `startIsolatedE2EServer`, then assert this sequence:

```ts
test("kata task columns float and preserve responsive grid alignment", async ({ page }) => {
  const backend = await startKataBackend();
  const kataHome = await configureKataHome(backend.url);
  const server = await startIsolatedE2EServer();

  try {
    await page.goto(`${server.info.base_url}/kata`);
    await expectKataDaemonSwitcherReady(page);
    const list = page.locator(".issue-list");
    await list.evaluate((element) => {
      element.style.width = "940px";
      element.style.flex = "none";
    });

    const titleHeader = page.locator(".table-header .col-title");
    const titleRow = page.locator(".issue-row .col-title").first();
    const titleWidthBefore = await titleRow.evaluate((element) => element.getBoundingClientRect().width);
    expect(Math.abs((await titleHeader.boundingBox())!.x - (await titleRow.boundingBox())!.x)).toBeLessThanOrEqual(1);

    await page.getByRole("button", { name: "Columns" }).click();
    const panel = page.locator(".column-picker__panel");
    await expect(panel).toBeVisible();
    expect(await panel.evaluate((element) => getComputedStyle(element).position)).toBe("fixed");
    expect(Number(await panel.evaluate((element) => getComputedStyle(element).zIndex))).toBeGreaterThan(0);
    const triggerBox = await page.getByRole("button", { name: "Columns" }).boundingBox();
    const panelBox = await panel.boundingBox();
    expect(triggerBox).not.toBeNull();
    expect(panelBox).not.toBeNull();
    expect(Math.abs(panelBox!.x + panelBox!.width - (triggerBox!.x + triggerBox!.width))).toBeLessThanOrEqual(2);
    expect(panelBox!.y).toBeGreaterThanOrEqual(0);
    expect(panelBox!.x).toBeGreaterThanOrEqual(0);
    expect(panelBox!.x + panelBox!.width).toBeLessThanOrEqual((page.viewportSize()?.width ?? 0) + 1);
    expect(panelBox!.y + panelBox!.height).toBeLessThanOrEqual((page.viewportSize()?.height ?? 0) + 1);
    await page.getByRole("checkbox", { name: "Tags" }).click();
    await expect(page.locator(".table-header .col-tags")).toHaveCount(0);
    await expect(page.locator(".issue-row .col-tags")).toHaveCount(0);
    const titleWidthAfter = await titleRow.evaluate((element) => element.getBoundingClientRect().width);
    expect(titleWidthAfter).toBeGreaterThan(titleWidthBefore);

    await list.evaluate((element) => {
      element.style.width = "640px";
    });
    await expect(page.locator(".table-header .col-owner")).toBeHidden();
    await list.evaluate((element) => {
      element.style.width = "940px";
    });
    await expect(page.locator(".table-header .col-owner")).toBeVisible();
    await expect(page.locator(".table-header .col-tags")).toHaveCount(0);

    await page.setViewportSize({ width: 720, height: 540 });
    const constrainedPanelBox = await panel.boundingBox();
    expect(constrainedPanelBox).not.toBeNull();
    expect(constrainedPanelBox!.x).toBeGreaterThanOrEqual(0);
    expect(constrainedPanelBox!.y).toBeGreaterThanOrEqual(0);
    expect(constrainedPanelBox!.x + constrainedPanelBox!.width).toBeLessThanOrEqual(721);
    expect(constrainedPanelBox!.y + constrainedPanelBox!.height).toBeLessThanOrEqual(541);

    for (const column of ["id", "title", "updated", "priority", "due", "owner"]) {
      const headerBox = await page.locator(`.table-header .col-${column}`).boundingBox();
      const rowBox = await page.locator(`.issue-row .col-${column}`).first().boundingBox();
      expect(headerBox).not.toBeNull();
      expect(rowBox).not.toBeNull();
      expect(Math.abs(headerBox!.x - rowBox!.x)).toBeLessThanOrEqual(1);
      expect(Math.abs(headerBox!.width - rowBox!.width)).toBeLessThanOrEqual(1);
    }
  } finally {
    await server.stop();
    kataHome.restore();
    await backend.close();
  }
});
```

Run from `frontend/`:

```bash
node ./scripts/run-e2e-to-file.ts tests/e2e-full/kata.spec.ts --grep "kata task columns float"
```

Expected: PASS with output recorded in the normal e2e log file.

- [ ] **Step 9: Commit the complete user-visible interaction**

Invoke `context-sync --commit`, then invoke the mandatory commit skill and create a commit with subject:

```text
feat: let maintainers hide Kata task columns
```

Stage only the picker, issue-list component, unit/browser tests, the focused Kata Playwright test, and any Task 1 files changed during integration.

### Task 3: Run final frontend verification

**Files:**
- Verify only; no production file should be added in this task.

**Interfaces:**
- Consumes: the complete Tasks 1 and 2 implementation.
- Produces: evidence that unit logic, real-browser focus behavior, responsive geometry, and the affected frontend checks pass.

- [ ] **Step 1: Run the full unit suite**

Run from `frontend/`:

```bash
node ../node_modules/vite-plus/bin/vp test run --project unit
```

Expected: PASS. Investigate any failure before continuing.

- [ ] **Step 2: Run the full affected browser suite**

Run from `frontend/`:

```bash
node ../node_modules/vite-plus/bin/vp test run --project browser src/lib/components/kata/KataIssueList.browser.svelte.ts
node ./scripts/run-e2e-to-file.ts tests/e2e-full/kata.spec.ts
```

Expected: the browser test and the complete affected Kata Playwright file pass.

- [ ] **Step 3: Run frontend formatting, lint, kit, and Svelte checks**

Run from the repository root:

```bash
make frontend-check
```

Expected: PASS for formatting, lint, `kit-ui-check`, and `svelte-check`.

- [ ] **Step 4: Confirm the final diff and persistence scope**

Run:

```bash
git status --short
git diff --stat HEAD~2..HEAD
git diff HEAD~2..HEAD -- frontend/src/lib/components/kata
```

Expected: only the approved Kata column preference, picker, list integration, unit/browser tests, and focused Kata Playwright coverage are present. No API, backend, route, or migration files change.

- [ ] **Step 5: Apply any verification fixes as a new commit**

If verification required edits, rerun the focused tests, full unit suite, Svelte autofixer for changed `.svelte` files, and `make frontend-check`. Then invoke `context-sync --commit` and the mandatory commit skill. Create a new commit rather than amending, with a subject that states the corrected user-visible outcome.

If no edits were required, do not create an empty commit.
