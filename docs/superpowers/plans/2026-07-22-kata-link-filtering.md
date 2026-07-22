# Kata Link Filtering Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make Kata issue links inherit the workspace task-status scope, expose relationship filters, and show peer state only when open and closed links are mixed.

**Architecture:** Add a small pure filter model for status and relationship classification. Keep the filter state in `KataWorkspace.svelte` so relationship choices survive selected-task navigation, pass it through `KataIssueDetail.svelte`, and let `KataIssueDiscussion.svelte` hydrate full peer summaries and apply the filters. A focused `KataLinkFilterMenu.svelte` owns the checkbox popover and reuses kit-ui positioning and dismissal utilities.

**Tech Stack:** Svelte 5 runes, TypeScript, `@kenn-io/kit-ui`, `@middleman/ui`, Testing Library, Vite+ Vitest.

## Global Constraints

- Keep the change frontend-only; do not alter Kata or middleman HTTP contracts.
- Default status visibility from the active top-level `open`, `closed`, or `all` filter.
- Keep Parent, Child, Blocks, Blocked by, and Related as relationship filters; do not derive a blocked task status.
- Render Open/Closed state chips only when both task states are enabled.
- Keep the link creation form and link-row navigation behavior unchanged.
- Preserve relationship filter choices across selected-task navigation in the current Kata workspace.
- Reset only the link task-state choices when the top-level status scope changes.
- Use shared checkbox, chip, positioning, and dismissable-overlay primitives.
- Use Svelte 5 runes and keyed each blocks; do not add legacy Svelte event syntax.
- Follow TDD: every production behavior starts with a focused failing test and an observed expected failure.

---

### Task 1: Define the Kata link filter contract

**Files:**
- Create: `frontend/src/lib/features/kata/kataLinkFilters.ts`
- Create: `frontend/src/lib/features/kata/kataLinkFilters.test.ts`

**Interfaces:**
- Consumes: `KataTaskLink`, `KataTaskStatusFilter`, and `KataTaskSummary` from `frontend/src/lib/api/kata/taskTypes.ts`.
- Produces: `KataLinkRelation`, `KataLinkFilters`, `KATA_LINK_RELATIONS`, `createKataLinkFilters`, `applyKataLinkStatusScope`, `relationForKataLink`, and `kataLinkMatchesFilters`.

- [ ] **Step 1: Write failing tests for defaults, relationship direction, scope reset, and unresolved peers**

Create `frontend/src/lib/features/kata/kataLinkFilters.test.ts`:

```ts
import { describe, expect, it } from "vite-plus/test";
import type { KataTaskLink, KataTaskSummary } from "../../api/kata/taskTypes.js";
import {
  applyKataLinkStatusScope,
  createKataLinkFilters,
  kataLinkMatchesFilters,
  relationForKataLink,
} from "./kataLinkFilters.js";

const selectedUID = "issue-selected";

function link(overrides: Partial<KataTaskLink> = {}): KataTaskLink {
  return {
    id: 1,
    project_id: 1,
    from: { uid: selectedUID, short_id: "selected" },
    to: { uid: "issue-peer", short_id: "peer" },
    type: "related",
    author: "maintainer",
    created_at: "2026-07-22T12:00:00Z",
    ...overrides,
  };
}

function peer(status: KataTaskSummary["status"]): KataTaskSummary {
  return {
    id: 2,
    uid: "issue-peer",
    project_id: 1,
    project_uid: "project-1",
    project_name: "Inbox",
    short_id: "peer",
    qualified_id: "Inbox#peer",
    title: "Peer task",
    status,
    metadata: {},
    revision: 1,
    author: "maintainer",
    created_at: "2026-07-22T12:00:00Z",
    updated_at: "2026-07-22T12:00:00Z",
  };
}

describe("kata link filters", () => {
  it.each([
    ["open", { open: true, closed: false }],
    ["closed", { open: false, closed: true }],
    ["all", { open: true, closed: true }],
  ] as const)("defaults task states from the %s scope", (scope, statuses) => {
    expect(createKataLinkFilters(scope).statuses).toEqual(statuses);
  });

  it("classifies relationship direction from the selected task", () => {
    expect(relationForKataLink(link({ type: "parent" }), selectedUID)).toBe("child");
    expect(
      relationForKataLink(
        link({
          type: "parent",
          from: { uid: "issue-parent", short_id: "parent" },
          to: { uid: selectedUID, short_id: "selected" },
        }),
        selectedUID,
      ),
    ).toBe("parent");
    expect(relationForKataLink(link({ type: "blocks" }), selectedUID)).toBe("blocks");
    expect(
      relationForKataLink(
        link({
          type: "blocks",
          from: { uid: "issue-blocker", short_id: "blocker" },
          to: { uid: selectedUID, short_id: "selected" },
        }),
        selectedUID,
      ),
    ).toBe("blocked_by");
  });

  it("resets task states without changing relationship choices", () => {
    const current = createKataLinkFilters("all");
    current.relations.related = false;

    expect(applyKataLinkStatusScope(current, "closed")).toEqual({
      statuses: { open: false, closed: true },
      relations: { ...current.relations, related: false },
    });
  });

  it("matches resolved peers by state and unresolved peers only in a mixed-state view", () => {
    const openOnly = createKataLinkFilters("open");
    const mixed = createKataLinkFilters("all");

    expect(kataLinkMatchesFilters(link(), selectedUID, peer("open"), openOnly)).toBe(true);
    expect(kataLinkMatchesFilters(link(), selectedUID, peer("closed"), openOnly)).toBe(false);
    expect(kataLinkMatchesFilters(link(), selectedUID, undefined, openOnly)).toBe(false);
    expect(kataLinkMatchesFilters(link(), selectedUID, undefined, mixed)).toBe(true);
  });
});
```

- [ ] **Step 2: Run the new test and confirm the expected missing-module failure**

Run from `frontend/`:

```bash
node ../node_modules/vite-plus/bin/vp test run --project unit src/lib/features/kata/kataLinkFilters.test.ts
```

Expected: FAIL because `./kataLinkFilters.js` does not exist.

- [ ] **Step 3: Implement the filter model**

Create `frontend/src/lib/features/kata/kataLinkFilters.ts`:

```ts
import type {
  KataTaskLink,
  KataTaskStatusFilter,
  KataTaskSummary,
} from "../../api/kata/taskTypes.js";

export const KATA_LINK_RELATIONS = ["parent", "child", "blocks", "blocked_by", "related"] as const;

export type KataLinkRelation = (typeof KATA_LINK_RELATIONS)[number];

export interface KataLinkFilters {
  statuses: Record<KataTaskSummary["status"], boolean>;
  relations: Record<KataLinkRelation, boolean>;
}

function statusesForScope(scope: KataTaskStatusFilter): KataLinkFilters["statuses"] {
  return {
    open: scope !== "closed",
    closed: scope !== "open",
  };
}

export function createKataLinkFilters(scope: KataTaskStatusFilter): KataLinkFilters {
  return {
    statuses: statusesForScope(scope),
    relations: {
      parent: true,
      child: true,
      blocks: true,
      blocked_by: true,
      related: true,
    },
  };
}

export function applyKataLinkStatusScope(
  current: KataLinkFilters,
  scope: KataTaskStatusFilter,
): KataLinkFilters {
  return {
    statuses: statusesForScope(scope),
    relations: { ...current.relations },
  };
}

export function relationForKataLink(link: KataTaskLink, selectedUID: string): KataLinkRelation {
  if (link.type === "parent") return link.to.uid === selectedUID ? "parent" : "child";
  if (link.type === "blocks") return link.to.uid === selectedUID ? "blocked_by" : "blocks";
  return "related";
}

export function kataLinkMatchesFilters(
  link: KataTaskLink,
  selectedUID: string,
  peer: KataTaskSummary | undefined,
  filters: KataLinkFilters,
): boolean {
  const relation = relationForKataLink(link, selectedUID);
  if (!filters.relations[relation]) return false;
  if (!peer) return filters.statuses.open && filters.statuses.closed;
  return filters.statuses[peer.status];
}
```

- [ ] **Step 4: Run the focused test and confirm it passes**

Run:

```bash
node ../node_modules/vite-plus/bin/vp test run --project unit src/lib/features/kata/kataLinkFilters.test.ts
```

Expected: PASS.

- [ ] **Step 5: Context-check and commit the filter contract**

From the repository root, invoke the repository-local `context-sync` skill with `--commit`. Then invoke the mandatory commit skill and create a normal hook-verified commit:

```bash
git add frontend/src/lib/features/kata/kataLinkFilters.ts frontend/src/lib/features/kata/kataLinkFilters.test.ts
git commit -m "feat: define Kata link filter semantics" \
  -m "Kata link visibility needs one explicit contract for status inheritance and directional relationship names before the detail UI can apply user-controlled filters."
```

Expected: commit succeeds without `--no-verify`.

---

### Task 2: Build the accessible link-filter popover

**Files:**
- Create: `frontend/src/lib/components/kata/KataLinkFilterMenu.svelte`
- Create: `frontend/src/lib/components/kata/KataLinkFilterMenu.test.ts`

**Interfaces:**
- Consumes: `KataLinkFilters`, `KataLinkRelation`, and `KATA_LINK_RELATIONS` from Task 1.
- Produces: `KataLinkFilterMenu` with props `filters: KataLinkFilters` and `onChange: (next: KataLinkFilters) => void`.

- [ ] **Step 1: Write failing interaction tests for the checkbox menu**

Create `frontend/src/lib/components/kata/KataLinkFilterMenu.test.ts`:

```ts
import { cleanup, fireEvent, render, screen } from "@testing-library/svelte";
import { afterEach, describe, expect, it, vi } from "vite-plus/test";
import { createKataLinkFilters } from "../../features/kata/kataLinkFilters.js";
import KataLinkFilterMenu from "./KataLinkFilterMenu.svelte";

describe("KataLinkFilterMenu", () => {
  afterEach(cleanup);

  it("emits independent task-state and relationship changes", async () => {
    const filters = createKataLinkFilters("open");
    const onChange = vi.fn();
    render(KataLinkFilterMenu, { props: { filters, onChange } });

    await fireEvent.click(screen.getByRole("button", { name: "Filter links" }));
    await fireEvent.click(screen.getByRole("checkbox", { name: "Closed" }));
    expect(onChange).toHaveBeenLastCalledWith({
      ...filters,
      statuses: { open: true, closed: true },
    });

    await fireEvent.click(screen.getByRole("checkbox", { name: "Blocked by" }));
    expect(onChange).toHaveBeenLastCalledWith({
      ...filters,
      relations: { ...filters.relations, blocked_by: false },
    });
  });

  it("closes on Escape and returns focus to the trigger", async () => {
    render(KataLinkFilterMenu, {
      props: { filters: createKataLinkFilters("all"), onChange: vi.fn() },
    });

    const trigger = screen.getByRole("button", { name: "Filter links" });
    await fireEvent.click(trigger);
    await fireEvent.keyDown(document, { key: "Escape" });

    expect(screen.queryByRole("group", { name: "Link filters" })).toBeNull();
    expect(document.activeElement).toBe(trigger);
  });
});
```

- [ ] **Step 2: Run the tests and confirm the expected missing-component failure**

Run from `frontend/`:

```bash
node ../node_modules/vite-plus/bin/vp test run --project unit src/lib/components/kata/KataLinkFilterMenu.test.ts
```

Expected: FAIL because `KataLinkFilterMenu.svelte` does not exist.

- [ ] **Step 3: Implement the floating checkbox menu**

Create `frontend/src/lib/components/kata/KataLinkFilterMenu.svelte` with these exact behaviors:

```svelte
<script lang="ts">
  import { autoReposition, Checkbox, dismissable, floatingPopoverStyle } from "@kenn-io/kit-ui";
  import FunnelIcon from "@lucide/svelte/icons/funnel";
  import { tick } from "svelte";
  import {
    KATA_LINK_RELATIONS,
    type KataLinkFilters,
    type KataLinkRelation,
  } from "../../features/kata/kataLinkFilters.js";

  interface Props {
    filters: KataLinkFilters;
    onChange: (next: KataLinkFilters) => void;
  }

  let { filters, onChange }: Props = $props();
  let open = $state(false);
  let trigger: HTMLButtonElement | undefined = $state();
  let panel: HTMLDivElement | undefined = $state();
  let panelStyle = $state("");

  const relationLabels: Record<KataLinkRelation, string> = {
    parent: "Parent",
    child: "Child",
    blocks: "Blocks",
    blocked_by: "Blocked by",
    related: "Related",
  };

  $effect(() => {
    if (!open) return;
    const cleanups = [
      dismissable({
        owners: () => [panel, trigger],
        dismiss: close,
        escapeFocus: () => trigger,
      }),
      autoReposition(() => panel, position),
    ];
    return () => cleanups.forEach((cleanup) => cleanup());
  });

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

  function close(): void {
    open = false;
  }

  async function toggle(): Promise<void> {
    open = !open;
    if (!open) return;
    await tick();
    position();
  }

  function changeStatus(status: "open" | "closed", checked: boolean): void {
    onChange({ ...filters, statuses: { ...filters.statuses, [status]: checked } });
  }

  function changeRelation(relation: KataLinkRelation, checked: boolean): void {
    onChange({ ...filters, relations: { ...filters.relations, [relation]: checked } });
  }
</script>

<div class="link-filter-menu">
  <button
    bind:this={trigger}
    type="button"
    class="link-filter-trigger"
    aria-label="Filter links"
    aria-expanded={open}
    onclick={toggle}
  >
    <FunnelIcon size={12} strokeWidth={2} aria-hidden="true" />
    <span>Filters</span>
  </button>

  {#if open}
    <div
      bind:this={panel}
      class="link-filter-panel kit-popover-card"
      style={panelStyle}
      role="group"
      aria-label="Link filters"
    >
      <fieldset>
        <legend>Task state</legend>
        <Checkbox
          label="Open"
          checked={filters.statuses.open}
          onchange={(checked) => changeStatus("open", checked)}
        />
        <Checkbox
          label="Closed"
          checked={filters.statuses.closed}
          onchange={(checked) => changeStatus("closed", checked)}
        />
      </fieldset>
      <fieldset>
        <legend>Relationship</legend>
        {#each KATA_LINK_RELATIONS as relation (relation)}
          <Checkbox
            label={relationLabels[relation]}
            checked={filters.relations[relation]}
            onchange={(checked) => changeRelation(relation, checked)}
          />
        {/each}
      </fieldset>
    </div>
  {/if}
</div>

<style>
  .link-filter-menu {
    position: relative;
  }

  .link-filter-trigger {
    min-height: 26px;
    border: 1px solid var(--border-muted);
    border-radius: var(--radius-sm);
    background: var(--bg-inset);
    color: var(--text-muted);
    display: inline-flex;
    align-items: center;
    gap: 5px;
    padding: 3px 8px;
    font: inherit;
    font-size: var(--font-size-xs);
    cursor: pointer;
  }

  .link-filter-trigger:hover,
  .link-filter-trigger[aria-expanded="true"] {
    border-color: var(--accent-blue);
    color: var(--text-primary);
  }

  .link-filter-trigger:focus-visible {
    outline: var(--focus-ring);
    outline-offset: 2px;
  }

  .link-filter-panel {
    width: 220px;
    padding: 10px;
    display: grid;
    gap: 10px;
  }

  fieldset {
    min-width: 0;
    border: 0;
    margin: 0;
    padding: 0;
    display: grid;
    gap: 7px;
  }

  legend {
    margin: 0 0 3px;
    color: var(--text-muted);
    font-size: var(--font-size-xs);
    font-weight: 650;
    text-transform: uppercase;
  }
</style>
```

- [ ] **Step 4: Run the focused menu tests and confirm they pass**

Run:

```bash
node ../node_modules/vite-plus/bin/vp test run --project unit src/lib/components/kata/KataLinkFilterMenu.test.ts
```

Expected: PASS.

- [ ] **Step 5: Run the Svelte autofixer before committing**

From the repository root:

```bash
./node_modules/.bin/vp exec -- svelte-mcp svelte-autofixer ./frontend/src/lib/components/kata/KataLinkFilterMenu.svelte
```

Expected: no unresolved Svelte correctness findings. Apply any clear suggestions, then rerun the focused test.

- [ ] **Step 6: Context-check and commit the filter menu**

Invoke `context-sync --commit`, then the mandatory commit skill, and commit through hooks:

```bash
git add frontend/src/lib/components/kata/KataLinkFilterMenu.svelte frontend/src/lib/components/kata/KataLinkFilterMenu.test.ts
git commit -m "feat: add Kata link filter controls" \
  -m "Maintainers need compact keyboard-accessible controls for task state and directional relationship visibility without adding permanent toolbar clutter to every link row."
```

Expected: commit succeeds without bypassing hooks.

---

### Task 3: Filter hydrated link rows and connect workspace scope

**Files:**
- Modify: `frontend/src/lib/components/kata/KataIssueDiscussion.svelte:1-205,294-370`
- Modify: `frontend/src/lib/components/kata/KataIssueDiscussion.test.ts:1-430`
- Modify: `frontend/src/lib/components/kata/KataIssueDetail.svelte:1-110,378-388`
- Modify: `frontend/src/lib/components/kata/KataIssueDetail.test.ts:1-145`
- Modify: `frontend/src/lib/features/kata/KataWorkspace.svelte:1-270,2720-2770`
- Modify: `frontend/src/lib/features/kata/KataWorkspace.test.ts`

**Interfaces:**
- Consumes: `KataLinkFilters`, `applyKataLinkStatusScope`, `createKataLinkFilters`, `kataLinkMatchesFilters`, and `relationForKataLink` from Task 1; `KataLinkFilterMenu` from Task 2.
- Produces: controlled props `linkFilters: KataLinkFilters` and `onLinkFiltersChange: (next: KataLinkFilters) => void` threaded from `KataWorkspace` through `KataIssueDetail` to `KataIssueDiscussion`.

- [ ] **Step 1: Write failing discussion tests for inherited state, relationship filtering, chips, counts, and empty states**

Extend `KataIssueDiscussion.test.ts` so `makeAPI` can resolve details by UID:

```ts
function makeAPI(
  options: {
    searchIssues?: KataTaskSummary[];
    issueDetails?: Record<string, KataTaskDetail>;
  } = {},
): KataTaskAPI {
  return {
    search: vi.fn(async () => ({
      filters: { scope: { kind: "all" }, status: "open", owner: "", label: "", query: "" },
      issues: options.searchIssues ?? [],
      fetched_at: "2026-06-01T12:00:00Z",
    })),
    issue: vi.fn(async (uid: string) =>
      options.issueDetails?.[uid] ?? makeIssue({ uid, short_id: uid, title: "Hydrated task" }),
    ),
  } as unknown as KataTaskAPI;
}
```

Import `KataTaskLink` and `createKataLinkFilters`, and add required `linkFilters` / `onLinkFiltersChange` props to every existing render. Update the existing off-view hydration test from `makeAPI({ issueDetail: peerDetail })` to `makeAPI({ issueDetails: { "issue-peer": peerDetail } })`. Then add these focused tests:

```ts
function taskLink(
  id: number,
  peerUID: string,
  peerShortID: string,
  type: KataTaskLink["type"],
): KataTaskLink {
  return {
    id,
    project_id: 1,
    from: { uid: "issue-1", short_id: "I-1" },
    to: { uid: peerUID, short_id: peerShortID },
    type,
    author: "wes",
    created_at: "2026-06-01T12:20:00Z",
  };
}

it("shows only open peers by default and reports the filtered count", async () => {
  const selected = makeIssue();
  selected.links = [
    taskLink(1, "issue-open", "open", "related"),
    taskLink(2, "issue-closed", "closed", "blocks"),
  ];
  render(KataIssueDiscussion, {
    props: {
      issue: selected,
      events: [],
      currentView: { groups: [] },
      api: makeAPI({
        issueDetails: {
          "issue-open": makeIssue({ uid: "issue-open", short_id: "open", title: "Open peer", status: "open" }),
          "issue-closed": makeIssue({ uid: "issue-closed", short_id: "closed", title: "Closed peer", status: "closed" }),
        },
      }),
      activeDaemonId: "home",
      linkFilters: createKataLinkFilters("open"),
      onLinkFiltersChange: vi.fn(),
      onAddComment: vi.fn(async () => true),
      onEditIssue: vi.fn(async () => true),
      onSelectIssue: vi.fn(),
    },
  });

  await waitFor(() => expect(screen.getByRole("button", { name: /Open peer/ })).toBeTruthy());
  expect(screen.queryByRole("button", { name: /Closed peer/ })).toBeNull();
  expect(within(screen.getByRole("region", { name: "Links" })).getByText("1 / 2")).toBeTruthy();
  expect(screen.queryByText("Open", { selector: ".state-badge" })).toBeNull();
});

it("shows state chips only for mixed-state results", async () => {
  const selected = makeIssue();
  selected.links = [
    taskLink(1, "issue-open", "open", "related"),
    taskLink(2, "issue-closed", "closed", "blocks"),
  ];
  render(KataIssueDiscussion, {
    props: {
      issue: selected,
      events: [],
      currentView: { groups: [] },
      api: makeAPI({
        issueDetails: {
          "issue-open": makeIssue({ uid: "issue-open", short_id: "open", title: "Open peer", status: "open" }),
          "issue-closed": makeIssue({ uid: "issue-closed", short_id: "closed", title: "Closed peer", status: "closed" }),
        },
      }),
      activeDaemonId: "home",
      linkFilters: createKataLinkFilters("all"),
      onLinkFiltersChange: vi.fn(),
      onAddComment: vi.fn(async () => true),
      onEditIssue: vi.fn(async () => true),
      onSelectIssue: vi.fn(),
    },
  });

  const links = screen.getByRole("region", { name: "Links" });
  await waitFor(() => expect(within(links).getByRole("button", { name: /Open peer open/ })).toBeTruthy());
  expect(within(links).getByRole("button", { name: /Closed peer closed/ })).toBeTruthy();
  expect(within(links).getByText("Open", { selector: ".state-badge" })).toBeTruthy();
  expect(within(links).getByText("Closed", { selector: ".state-badge" })).toBeTruthy();
});

it("filters directional relationship types and shows the filtered-empty message", async () => {
  const selected = makeIssue();
  selected.links = [
    taskLink(1, "issue-related", "related", "related"),
    taskLink(2, "issue-blocked", "blocked", "blocks"),
  ];
  const filters = createKataLinkFilters("all");
  filters.relations.related = false;
  filters.relations.blocks = false;
  render(KataIssueDiscussion, {
    props: {
      issue: selected,
      events: [],
      currentView: { groups: [] },
      api: makeAPI({
        issueDetails: {
          "issue-related": makeIssue({ uid: "issue-related", short_id: "related", title: "Related peer" }),
          "issue-blocked": makeIssue({ uid: "issue-blocked", short_id: "blocked", title: "Blocked peer" }),
        },
      }),
      activeDaemonId: "home",
      linkFilters: filters,
      onLinkFiltersChange: vi.fn(),
      onAddComment: vi.fn(async () => true),
      onEditIssue: vi.fn(async () => true),
      onSelectIssue: vi.fn(),
    },
  });

  const links = screen.getByRole("region", { name: "Links" });
  await waitFor(() => expect(within(links).getByText("No links match these filters.")).toBeTruthy());
  expect(within(links).queryByText("No links.")).toBeNull();
  expect(within(links).getByText("0 / 2")).toBeTruthy();
});

it("shows a resolving state while a single-state filter waits for peer status", () => {
  const selected = makeIssue();
  selected.links = [taskLink(1, "issue-pending", "pending", "related")];
  const api = makeAPI();
  vi.mocked(api.issue).mockImplementation(() => new Promise<KataTaskDetail>(() => {}));

  render(KataIssueDiscussion, {
    props: {
      issue: selected,
      events: [],
      currentView: { groups: [] },
      api,
      activeDaemonId: "home",
      linkFilters: createKataLinkFilters("open"),
      onLinkFiltersChange: vi.fn(),
      onAddComment: vi.fn(async () => true),
      onEditIssue: vi.fn(async () => true),
      onSelectIssue: vi.fn(),
    },
  });

  expect(within(screen.getByRole("region", { name: "Links" })).getByText("Resolving linked tasks...")).toBeTruthy();
});

it("marks failed peer state as unavailable in a mixed-state view", async () => {
  const selected = makeIssue();
  selected.links = [taskLink(1, "issue-missing", "missing", "related")];
  const api = makeAPI();
  vi.mocked(api.issue).mockRejectedValue(new Error("unavailable"));

  render(KataIssueDiscussion, {
    props: {
      issue: selected,
      events: [],
      currentView: { groups: [] },
      api,
      activeDaemonId: "home",
      linkFilters: createKataLinkFilters("all"),
      onLinkFiltersChange: vi.fn(),
      onAddComment: vi.fn(async () => true),
      onEditIssue: vi.fn(async () => true),
      onSelectIssue: vi.fn(),
    },
  });

  const links = screen.getByRole("region", { name: "Links" });
  await waitFor(() => expect(within(links).getByTitle("Task state unavailable")).toBeTruthy());
  expect(within(links).getByRole("button", { name: /missing state unavailable/ })).toBeTruthy();
});
```

- [ ] **Step 2: Run the discussion test and verify the new assertions fail for missing props and unfiltered rows**

Run from `frontend/`:

```bash
node ../node_modules/vite-plus/bin/vp test run --project unit src/lib/components/kata/KataIssueDiscussion.test.ts
```

Expected: FAIL because the component does not accept controlled filters, hydrates only titles, renders all links, and has no mixed-state chip behavior.

- [ ] **Step 3: Replace title-only hydration with peer-summary hydration**

In `KataIssueDiscussion.svelte`:

1. Import `ItemStateChip` from `@middleman/ui`, `KataTaskSummary`, the filter helpers, and `KataLinkFilterMenu`.
2. Add required props:

```ts
linkFilters: KataLinkFilters;
onLinkFiltersChange: (next: KataLinkFilters) => void;
```

3. Replace `linkTitles` with:

```ts
let hydratedPeers = $state<Record<string, KataTaskSummary | null>>({});
let peerHydrationSignature = $state("");
let pendingPeerKeys = $state<ReadonlySet<string>>(new Set());
```

4. Preserve the existing signature and generation guard, but store `detail.issue` on success and `null` on failure.
5. Resolve current-view peers without an API call:

```ts
function currentViewPeer(uid: string): KataTaskSummary | undefined {
  for (const group of currentView.groups) {
    const found = group.issues.find((candidate) => candidate.uid === uid);
    if (found) return found;
  }
  return undefined;
}

function peerFor(link: KataTaskLink): KataTaskSummary | undefined {
  const uid = linkPeerUID(link);
  return currentViewPeer(uid) ?? hydratedPeers[uid] ?? undefined;
}

function peerHydrationFailed(link: KataTaskLink): boolean {
  return hydratedPeers[linkPeerUID(link)] === null;
}
```

6. Derive visible rows and loading state:

```ts
const visibleLinks = $derived(
  issue.links.filter((link) =>
    kataLinkMatchesFilters(link, issue.issue.uid, peerFor(link), linkFilters),
  ),
);
const showStateChips = $derived(linkFilters.statuses.open && linkFilters.statuses.closed);
const unresolvedPeerCount = $derived(
  issue.links.filter((link) => {
    const uid = linkPeerUID(link);
    return currentViewPeer(uid) === undefined && hydratedPeers[uid] === undefined;
  }).length,
);
```

- [ ] **Step 4: Render the menu, visible count, filtered states, and conditional chips**

Replace the Links header and list body with this structure:

```svelte
<div class="section-header link-section-header">
  <h3>Links</h3>
  <div class="link-header-actions">
    <span>{visibleLinks.length === issue.links.length ? issue.links.length : `${visibleLinks.length} / ${issue.links.length}`}</span>
    {#if unresolvedPeerCount > 0}<span class="link-loading">Resolving {unresolvedPeerCount}</span>{/if}
    <KataLinkFilterMenu filters={linkFilters} onChange={onLinkFiltersChange} />
  </div>
</div>
{#if issue.links.length === 0}
  <p class="link-empty">No links.</p>
{:else if visibleLinks.length === 0 && unresolvedPeerCount > 0}
  <p class="link-empty">Resolving linked tasks...</p>
{:else if visibleLinks.length === 0}
  <p class="link-empty">No links match these filters.</p>
{:else}
  <div class="link-list" aria-busy={unresolvedPeerCount > 0}>
    {#each visibleLinks as link (link.id)}
      {@const peer = peerFor(link)}
      <button
        type="button"
        class="link-row"
        class:link-row--with-state={showStateChips}
        aria-label={`${linkLabel(link)} ${linkPeerShortID(link)} ${peer?.title ?? ""}${showStateChips && peer ? ` ${peer.status}` : ""}${showStateChips && peerHydrationFailed(link) ? " state unavailable" : ""}`.trim()}
        onclick={() => void onSelectIssue(linkPeerUID(link))}
      >
        <span class="link-kind">{linkLabel(link)}</span>
        <span class="link-peer">{linkPeerShortID(link)}</span>
        {#if peer?.title}<span class="link-title">{peer.title}</span>{/if}
        {#if showStateChips && peer}
          <ItemStateChip state={peer.status} size="xs" />
        {:else if showStateChips && peerHydrationFailed(link)}
          <ItemStateChip state="unknown" size="xs" title="Task state unavailable" />
        {/if}
      </button>
    {/each}
  </div>
{/if}
```

Update the row grid so `.link-row` keeps the current three columns and `.link-row--with-state` adds a final max-content column. Add compact header-action and muted `.link-loading` styling; do not duplicate chip geometry locally.

- [ ] **Step 5: Run the discussion tests and confirm they pass**

Run:

```bash
node ../node_modules/vite-plus/bin/vp test run --project unit src/lib/components/kata/KataIssueDiscussion.test.ts
```

Expected: PASS.

- [ ] **Step 6: Thread controlled filter state through detail and workspace**

In `KataIssueDetail.svelte`, add `linkFilters` and `onLinkFiltersChange` to `Props`, destructuring, and the `KataIssueDiscussion` call. Import `createKataLinkFilters` in `KataIssueDetail.test.ts` and update `renderDetail` with:

```ts
linkFilters: createKataLinkFilters("open"),
onLinkFiltersChange: vi.fn(),
```

In `KataWorkspace.svelte`, import the filter model and add state beside the other workspace-owned UI state:

```ts
let linkFilters = $state<KataLinkFilters>(createKataLinkFilters("open"));
let lastLinkStatusScope = $state<KataTaskSearchFilters["status"]>("open");
```

After `listStatusFilter` is defined, synchronize only task state:

```ts
$effect(() => {
  const nextScope = listStatusFilter;
  if (nextScope === lastLinkStatusScope) return;
  lastLinkStatusScope = nextScope;
  linkFilters = applyKataLinkStatusScope(linkFilters, nextScope);
});
```

Pass the controlled state to `KataIssueDetail`:

```svelte
linkFilters={linkFilters}
onLinkFiltersChange={(next) => {
  linkFilters = next;
}}
```

Do not key or reset `linkFilters` on selected issue changes. This is what preserves relationship choices while navigating tasks.

- [ ] **Step 7: Add a workspace-level regression test for status reset and relationship persistence**

Add this test to `KataWorkspace.test.ts`:

```ts
it("keeps relationship filters across task navigation and resets state filters with the workspace scope", async () => {
  const root = issue("issue-root", "Root task", "project-kata");
  const next = issue("issue-next", "Next task", "project-kata");
  const related = issue("issue-related", "Related task", "project-kata");
  const blocked = issue("issue-blocked", "Blocked task", "project-kata");
  const closed = {
    ...issue("issue-closed", "Closed task", "project-kata"),
    status: "closed" as const,
    closed_reason: "done" as const,
    closed_at: fetchedAt,
  };
  const rows = [root, next, related, blocked, closed];
  const links: KataTaskLink[] = [
    {
      id: 1,
      project_id: root.project_id,
      from: { uid: root.uid, short_id: root.short_id },
      to: { uid: related.uid, short_id: related.short_id },
      type: "related",
      author: "fixture-user",
      created_at: fetchedAt,
    },
    {
      id: 2,
      project_id: root.project_id,
      from: { uid: root.uid, short_id: root.short_id },
      to: { uid: blocked.uid, short_id: blocked.short_id },
      type: "blocks",
      author: "fixture-user",
      created_at: fetchedAt,
    },
  ];
  const { api } = createWorkspaceAPI(rows);
  vi.mocked(api.issue).mockImplementation(async (uid: string) => {
    const taskDetail = detail(uid, rows);
    return uid === root.uid ? { ...taskDetail, links } : taskDetail;
  });

  render(KataWorkspaceRouteHost, { props: { api, initialIssue: root.uid } });

  await waitFor(() => expect(screen.getByRole("heading", { name: "Root task" })).toBeTruthy());
  await fireEvent.click(screen.getByRole("button", { name: "Filter links" }));
  await fireEvent.click(screen.getByRole("checkbox", { name: "Related" }));

  const issues = screen.getByRole("main", { name: "Issues" });
  await fireEvent.click(within(issues).getByRole("button", { name: /Next task/ }));
  await waitFor(() => expect(screen.getByRole("heading", { name: "Next task" })).toBeTruthy());
  await fireEvent.click(within(issues).getByRole("button", { name: /Root task/ }));
  await waitFor(() => expect(screen.getByRole("heading", { name: "Root task" })).toBeTruthy());

  await fireEvent.click(screen.getByRole("button", { name: "Filter links" }));
  expect((screen.getByRole("checkbox", { name: "Related" }) as HTMLInputElement).checked).toBe(false);
  await fireEvent.keyDown(document, { key: "Escape" });

  await fireEvent.click(screen.getByRole("combobox", { name: "Status: Open" }));
  await fireEvent.click(screen.getByRole("option", { name: "Closed" }));
  await waitFor(() => expect(within(issues).getByRole("button", { name: /Closed task/ })).toBeTruthy());
  await fireEvent.click(within(issues).getByRole("button", { name: /Closed task/ }));
  await waitFor(() => expect(screen.getByRole("heading", { name: "Closed task" })).toBeTruthy());

  await fireEvent.click(screen.getByRole("button", { name: "Filter links" }));
  expect((screen.getByRole("checkbox", { name: "Open" }) as HTMLInputElement).checked).toBe(false);
  expect((screen.getByRole("checkbox", { name: "Closed" }) as HTMLInputElement).checked).toBe(true);
  expect((screen.getByRole("checkbox", { name: "Related" }) as HTMLInputElement).checked).toBe(false);
});
```

This test uses role-based queries and asserts only the interaction contract. Do not add API-call-count assertions unrelated to link filtering.

- [ ] **Step 8: Run all affected component and workspace tests**

Run from `frontend/`:

```bash
node ../node_modules/vite-plus/bin/vp test run --project unit \
  src/lib/features/kata/kataLinkFilters.test.ts \
  src/lib/components/kata/KataLinkFilterMenu.test.ts \
  src/lib/components/kata/KataIssueDiscussion.test.ts \
  src/lib/components/kata/KataIssueDetail.test.ts \
  src/lib/features/kata/KataWorkspace.test.ts
```

Expected: PASS.

- [ ] **Step 9: Run Svelte analysis on every changed component**

From the repository root:

```bash
./node_modules/.bin/vp exec -- svelte-mcp svelte-autofixer ./frontend/src/lib/components/kata/KataLinkFilterMenu.svelte
./node_modules/.bin/vp exec -- svelte-mcp svelte-autofixer ./frontend/src/lib/components/kata/KataIssueDiscussion.svelte
./node_modules/.bin/vp exec -- svelte-mcp svelte-autofixer ./frontend/src/lib/components/kata/KataIssueDetail.svelte
./node_modules/.bin/vp exec -- svelte-mcp svelte-autofixer ./frontend/src/lib/features/kata/KataWorkspace.svelte
```

Expected: no unresolved Svelte correctness findings.

- [ ] **Step 10: Run the full affected frontend verification**

From the repository root:

```bash
./node_modules/.bin/vp test run --project unit
./node_modules/.bin/vp run frontend-check
```

Expected: both commands exit 0. Do not add Playwright solely to repeat checkbox visibility; the component and workspace tests own this workflow, and no browser-only geometry is changing beyond the already-shared floating-position utilities.

- [ ] **Step 11: Review the final diff and commit the integrated behavior**

Review:

```bash
git status --short
git diff --stat HEAD
git diff HEAD -- frontend/src/lib/components/kata frontend/src/lib/features/kata/KataWorkspace.svelte frontend/src/lib/features/kata/KataWorkspace.test.ts
```

Invoke `context-sync --commit`, then the mandatory commit skill. Commit through hooks:

```bash
git add \
  frontend/src/lib/components/kata/KataIssueDiscussion.svelte \
  frontend/src/lib/components/kata/KataIssueDiscussion.test.ts \
  frontend/src/lib/components/kata/KataIssueDetail.svelte \
  frontend/src/lib/components/kata/KataIssueDetail.test.ts \
  frontend/src/lib/features/kata/KataWorkspace.svelte \
  frontend/src/lib/features/kata/KataWorkspace.test.ts
git commit -m "feat: filter Kata links by task state and relation" \
  -m "Kata detail links should follow the maintainer's active task scope while still allowing local relationship filtering. Mixed-state results retain explicit state labels without repeating redundant chips in single-state views."
```

Expected: commit succeeds without `--no-verify`, and `git status --short` is clean afterward.
