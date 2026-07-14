# Kata Project UX Cleanup Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Standardize Kata project navigation, remove accidental project-renaming affordances, and move issue reassignment from the breadcrumb into the explicit issue actions menu.

**Architecture:** Keep all daemon and store capabilities intact while changing only frontend interaction composition. `KataSidebar` adopts the shared grouped-sidebar primitives, while `KataIssueOverflowMenu` owns destination eligibility, search, focus, and retry state for the existing `onMoveIssue` callback.

**Tech Stack:** Svelte 5 runes, TypeScript, `@middleman/ui`, `@kenn-io/kit-ui`, Testing Library, Vite+ Vitest unit/browser projects, and Playwright full-stack tests.

## Global Constraints

- Render system views, supplied Kata areas, and the existing new-project control in that order.
- Areas start expanded; collapse state survives reactive updates while mounted but is not persisted across remounts or reloads.
- Remove only sidebar rename UI and callback wiring; retain Kata client/store rename capability.
- Display the issue project breadcrumb as passive text using the existing name/UID fallback order.
- Expose exact copy **Move to another project** only when an eligible destination exists.
- Exclude the current project and inbox-role projects from destinations; sort names case-insensitively and show open-task counts.
- Preserve workspace-owned mutation error presentation and keep the destination picker open after a handled failure.
- Keep pointer, keyboard, Escape/focus, narrow-width, project creation, navigation, and shared scroll-indicator behavior working.
- Do not modify backend, database, OpenAPI, generated API, daemon contracts, or shared primitives unless implementation reveals an actual shared defect.
- Use Bun/Vite+ tooling only; never invoke npm.

---

## File Map

- `frontend/src/lib/components/kata/KataSidebar.svelte`: shared scroll/group composition, mounted collapse state, navigation-only project rows, and project creation.
- `frontend/src/lib/components/kata/KataSidebar.test.ts`: sidebar grouping, collapse lifetime, ordering, navigation, creation, and absence of rename affordances.
- `frontend/src/lib/features/kata/KataWorkspace.svelte`: remove sidebar rename forwarding and return move success from the existing task wrapper.
- `frontend/src/lib/components/terminal/KataWorkspaceSidebarPane.svelte`: match the boolean issue-move callback contract in the embedded workspace.
- `frontend/src/lib/components/terminal/KataWorkspaceSidebarPane.test.ts`: exercise handled move failure and retry through the embedded host's real task wrapper.
- `frontend/src/lib/components/kata/KataIssueDetail.svelte`: passive project breadcrumb and project/move props forwarded to the overflow menu.
- `frontend/src/lib/components/kata/KataIssueDetail.test.ts`: passive breadcrumb and project-name fallback coverage.
- `frontend/src/lib/components/kata/KataIssueOverflowMenu.svelte`: explicit move action, searchable destination view, pending/retry state, focus restoration, and issue-identity reset.
- `frontend/src/lib/components/kata/KataIssueOverflowMenu.test.ts`: destination rules, sorting, search, success/failure, issue reset, and keyboard dismissal.
- `frontend/tests/e2e-full/kata.spec.ts`: replace obsolete rename workflows, move real-daemon reassignment to More actions, and add one-shot failure/retry coverage in the local Kata backend fixture.

---

### Task 1: Adopt Shared Kata Sidebar Grouping

**Files:**

- Modify: `frontend/src/lib/components/kata/KataSidebar.test.ts`
- Modify: `frontend/src/lib/components/kata/KataSidebar.svelte`

**Interfaces:**

- Consumes: `GroupedSidebarSection` and `SidebarScrollArea` already implemented and exported by `@middleman/ui` in merged PR #662 (`packages/ui/src/components/shared/` and `packages/ui/src/index.ts`).
- Produces: mounted `collapsedAreas: string[]` state and one native project button per visible project.

- [ ] **Step 1: Add failing group and order coverage**

Add a test that renders the existing `areas` fixture and asserts the labeled scroll region contains system navigation before `Personal`, then `Work`, then `New project`; both group buttons begin expanded and expose project counts:

```ts
it("renders system views, expanded area groups, and project creation in order", () => {
  renderSidebar();

  const navigation = screen.getByRole("region", { name: "Kata navigation" });
  const inbox = within(navigation).getByRole("button", { name: /^Inbox\b/ });
  const personal = within(navigation).getByRole("button", { name: /^Personal\s+1$/ });
  const work = within(navigation).getByRole("button", { name: /^Work\s+1$/ });
  const create = within(navigation).getByRole("button", { name: "New project" });

  expect(personal).toHaveAttribute("aria-expanded", "true");
  expect(work).toHaveAttribute("aria-expanded", "true");
  const ordered = [inbox, personal, work, create];
  for (let index = 0; index < ordered.length - 1; index += 1) {
    expect(ordered[index]!.compareDocumentPosition(ordered[index + 1]!)).toBe(Node.DOCUMENT_POSITION_FOLLOWING);
  }
});
```

Extract a local `renderSidebar(overrides = {})` helper in the test file so every render receives the same valid props. Keep it local because no other test file consumes this fixture.

- [ ] **Step 2: Add failing collapse-lifetime coverage**

```ts
it("keeps area collapse state while mounted and resets it after remount", async () => {
  const view = renderSidebar();
  const personal = screen.getByRole("button", { name: /^Personal\s+1$/ });

  await fireEvent.click(personal);
  expect(personal).toHaveAttribute("aria-expanded", "false");
  expect(screen.queryByRole("button", { name: /^Finances\b/ })).toBeNull();

  await view.rerender({ areas: [...areas] });
  expect(screen.getByRole("button", { name: /^Personal\s+1$/ })).toHaveAttribute("aria-expanded", "false");

  view.unmount();
  renderSidebar();
  expect(screen.getByRole("button", { name: /^Personal\s+1$/ })).toHaveAttribute("aria-expanded", "true");
});
```

- [ ] **Step 3: Run the focused test to verify failure**

Run from `frontend/`:

```bash
node ../node_modules/vite-plus/bin/vp test run --project unit src/lib/components/kata/KataSidebar.test.ts
```

Expected: FAIL because area headings are not expandable buttons and the sidebar is not a labeled `SidebarScrollArea` region.

- [ ] **Step 4: Add shared imports and collapse state**

In `KataSidebar.svelte`, import the shared components and add mounted local state:

```ts
import { GroupedSidebarSection, SidebarScrollArea } from "@middleman/ui";

let collapsedAreas = $state<string[]>([]);

function toggleArea(name: string): void {
  collapsedAreas = collapsedAreas.includes(name)
    ? collapsedAreas.filter((area) => area !== name)
    : [...collapsedAreas, name];
}
```

Do not synchronize this state into URL, local storage, or the workspace store.

- [ ] **Step 5: Replace custom scrolling and area sections**

Keep the outer `aside`, but make `SidebarScrollArea` the only scrolling owner:

```svelte
<aside class="kata-sidebar" aria-label="Kata navigation">
  <SidebarScrollArea label="Kata navigation">
    <nav class="kata-nav" aria-label="System views">
      <!-- retain the existing keyed system-view loop unchanged -->
    </nav>

    {#each areas as area (area.name)}
      <GroupedSidebarSection
        label={area.name}
        count={area.projects.length}
        collapsed={collapsedAreas.includes(area.name)}
        onclick={() => toggleArea(area.name)}
      >
        {#each area.projects as project (project.uid)}
          <!-- project row retained here until Task 2 removes rename behavior -->
        {/each}
      </GroupedSidebarSection>
    {/each}

    <div class="project-create">
      <!-- retain existing creation form/control -->
    </div>
  </SidebarScrollArea>
</aside>
```

Render supplied arrays directly; do not sort or auto-expand a selected project’s area.

- [ ] **Step 6: Align row styling with the shared contract**

Remove `overflow: auto` from `.kata-sidebar`. Preserve the existing `900px` narrow breakpoint and bounded sidebar height, but let the nested scroll area shrink with `min-height: 0`. Set row styles through the existing tokens:

```css
.kata-sidebar {
  min-height: 0;
  background: var(--bg-inset);
}

.kata-nav button,
.project-select-button {
  background: var(--sidebar-row-bg, transparent);
  padding: var(--sidebar-row-padding, 6px 10px);
}

.kata-nav button:hover,
.project-select-button:hover {
  background: var(--sidebar-row-hover-bg, var(--bg-surface-hover));
}

.kata-nav button.active,
.project-select-button.active {
  background: var(--bg-row-selected);
}
```

Retain Kata-specific grid/name/count layout and existing focus-visible treatment.

- [ ] **Step 7: Run focused tests and commit**

```bash
node ../node_modules/vite-plus/bin/vp test run --project unit src/lib/components/kata/KataSidebar.test.ts
git add frontend/src/lib/components/kata/KataSidebar.svelte frontend/src/lib/components/kata/KataSidebar.test.ts
git commit -m "feat: group Kata project navigation"
```

Expected: focused test PASS; commit hooks PASS.

---

### Task 2: Make Sidebar Project Rows Navigation-Only

**Files:**

- Modify: `frontend/src/lib/components/kata/KataSidebar.test.ts`
- Modify: `frontend/src/lib/components/kata/KataSidebar.svelte`
- Modify: `frontend/src/lib/features/kata/KataWorkspace.svelte`

**Interfaces:**

- Removes: `KataSidebar` prop `onRenameProject: (id: number, name: string) => Promise<void>`.
- Preserves: `KataWorkspaceStore.renameProject(id, name)` and task-client rename operations.

- [ ] **Step 1: Replace contradictory rename tests with a failing navigation-only test**

Delete `double-clicking a project enters rename mode` and `renames a project from the project row`. Add:

```ts
it("keeps project rows navigation-only without rename affordances", async () => {
  const onOpenProject = vi.fn();
  renderSidebar({
    onOpenProject,
    searchFilters: { ...allScopeFilters, scope: { kind: "project", project_uid: "project-finances" } },
  });

  const finances = screen.getByRole("button", { name: /^Finances\b/ });
  expect(finances).toHaveClass("active");
  expect(screen.queryByRole("button", { name: "Rename Finances" })).toBeNull();
  expect(screen.queryByRole("textbox", { name: "Rename project" })).toBeNull();

  await fireEvent.doubleClick(finances);
  expect(screen.queryByRole("textbox", { name: "Rename project" })).toBeNull();
  expect(onOpenProject).toHaveBeenCalledWith("project-finances");
});
```

Do not assert an exact callback count because a synthetic double-click may dispatch two clicks.

- [ ] **Step 2: Run the focused test to verify failure**

```bash
node ../node_modules/vite-plus/bin/vp test run --project unit src/lib/components/kata/KataSidebar.test.ts
```

Expected: FAIL because the rename button and double-click form still exist.

- [ ] **Step 3: Remove sidebar rename implementation**

Remove from `KataSidebar.svelte`:

```ts
import PencilIcon from "@lucide/svelte/icons/pencil";
onRenameProject: (id: number, name: string) => Promise<void>;
let renamingProjectID = $state<number | null>(null);
let renameDraft = $state("");
let renameSaving = $state(false);
let renameError = $state<string | null>(null);
let renameInput: HTMLInputElement | null = $state(null);
```

Also remove `startRenamingProject`, `cancelRenamingProject`, `submitRenameProject`, the conditional rename form, `ondblclick`, the pencil button, and rename-only CSS. Render each project as a single native button:

```svelte
<button
  type="button"
  class="project-select-button"
  class:active={isProjectActive(project.uid)}
  onclick={() => void onOpenProject(project.uid)}
>
  <span class="project-name">{project.name}</span>
  <span class="project-count count">{project.open_count}</span>
</button>
```

Do not replace it with a `div role="button"` or nested buttons.

- [ ] **Step 4: Remove workspace callback forwarding**

Delete from `KataWorkspace.svelte`:

```ts
async function renameKataProject(id: number, name: string): Promise<void> {
  await store.renameProject(id, name);
}
```

Remove `onRenameProject={renameKataProject}` from `<KataSidebar>`. Do not change `store.renameProject`, task types, task client, or the e2e daemon PATCH handler.

- [ ] **Step 5: Run focused and preservation tests**

```bash
node ../node_modules/vite-plus/bin/vp test run --project unit \
  src/lib/components/kata/KataSidebar.test.ts \
  src/lib/stores/kata-workspace.svelte.test.ts \
  src/lib/api/kata/taskClient.test.ts
```

Expected: PASS, including existing rename API/store behavior.

- [ ] **Step 6: Commit**

```bash
git add frontend/src/lib/components/kata/KataSidebar.svelte \
  frontend/src/lib/components/kata/KataSidebar.test.ts \
  frontend/src/lib/features/kata/KataWorkspace.svelte
git commit -m "feat: make Kata project rows navigation-only"
```

---

### Task 3: Move Issue Reassignment Into More Actions

**Files:**

- Modify: `frontend/src/lib/components/kata/KataIssueOverflowMenu.test.ts`
- Modify: `frontend/src/lib/components/kata/KataIssueOverflowMenu.svelte`
- Modify: `frontend/src/lib/components/kata/KataIssueDetail.svelte`
- Modify: `frontend/src/lib/features/kata/KataWorkspace.svelte`
- Modify: `frontend/src/lib/components/terminal/KataWorkspaceSidebarPane.svelte`
- Create: `frontend/src/lib/components/terminal/KataWorkspaceSidebarPane.test.ts`

**Interfaces:**

- Adds to `KataIssueOverflowMenu` props:
  - `projects: KataProjectSummary[]`
  - `onMoveIssue: (toProjectUID: string) => boolean | Promise<boolean>`
- Changes `KataIssueDetail.onMoveIssue` to the same non-null boolean contract.
- Produces: `menuView: "actions" | "move"`, search query, destination derivation, per-destination pending state, and success/failure-aware close behavior.

- [ ] **Step 1: Extend the test fixture**

Add to `KataIssueOverflowMenu.test.ts`:

```ts
import type { KataProjectSummary, KataTaskDetail } from "../../api/kata/taskTypes.js";

function makeProject(uid: string, name: string, openCount: number, role = ""): KataProjectSummary {
  return {
    id: uid === "project-1" ? 1 : 2,
    uid,
    name,
    metadata: role ? { role } : {},
    open_count: openCount,
    revision: 1,
    created_at: "2026-06-01T12:00:00Z",
  };
}

const projects = [
  makeProject("project-1", "Inbox", 2, "inbox"),
  makeProject("project-alpha", "Alpha", 3),
  makeProject("project-roadmap", "Roadmap", 5),
  { ...makeProject("project-shared-work", "Shared", 2), metadata: { area: "Work" } },
  { ...makeProject("project-shared-home", "Shared", 4), metadata: { area: "Home" } },
  { ...makeProject("project-shared-home-2", "Shared", 1), metadata: { area: "Home" } },
];
```

Update all existing renders to pass `projects` and `onMoveIssue: vi.fn(async () => true)`.

- [ ] **Step 2: Add failing destination eligibility and ordering tests**

```ts
it("hides move when only the current and inbox projects exist", async () => {
  renderMenu({ projects: [makeProject("project-1", "Current", 1), makeProject("inbox", "Inbox", 2, "inbox")] });
  await fireEvent.click(screen.getByRole("button", { name: "More actions" }));
  expect(screen.queryByRole("menuitem", { name: "Move to another project" })).toBeNull();
});

it("shows sorted eligible destinations with open counts", async () => {
  renderMenu({ projects: [...projects].reverse() });
  await fireEvent.click(screen.getByRole("button", { name: "More actions" }));
  await fireEvent.click(screen.getByRole("menuitem", { name: "Move to another project" }));

  const options = screen.getAllByRole("button", { name: /Alpha|Roadmap|Shared/ });
  expect(options.slice(0, 2).map((button) => button.textContent)).toEqual(["Alpha3", "Roadmap5"]);
  expect(screen.getByRole("button", { name: /Shared.*project-shared-home.*4/ })).toBeTruthy();
  expect(screen.getByRole("button", { name: /Shared.*project-shared-home-2.*1/ })).toBeTruthy();
  expect(screen.getByRole("button", { name: /Shared.*Work.*2/ })).toBeTruthy();
  expect(screen.queryByText("Inbox")).toBeNull();
});
```

Set the test issue’s current project to a non-inbox project when necessary so the inbox filter and current-project filter are tested independently.

- [ ] **Step 3: Add failing search and result-contract tests**

```ts
it("filters destinations and closes after a successful move", async () => {
  const onMoveIssue = vi.fn(async () => true);
  renderMenu({ onMoveIssue });

  await openMovePicker();
  await fireEvent.input(screen.getByRole("searchbox", { name: "Find project" }), {
    target: { value: "road" },
  });
  expect(screen.queryByRole("button", { name: /Alpha/ })).toBeNull();
  await fireEvent.click(screen.getByRole("button", { name: /Roadmap/ }));

  expect(onMoveIssue).toHaveBeenCalledWith("project-roadmap");
  expect(screen.queryByRole("searchbox", { name: "Find project" })).toBeNull();
  expect(screen.queryByRole("menu", { name: "Task actions" })).toBeNull();
});

it("keeps the picker open when the workspace reports move failure", async () => {
  const onMoveIssue = vi.fn(async () => false);
  renderMenu({ onMoveIssue });
  await openMovePicker();
  await fireEvent.click(screen.getByRole("button", { name: /Roadmap/ }));

  expect(onMoveIssue).toHaveBeenCalledWith("project-roadmap");
  expect(screen.getByRole("searchbox", { name: "Find project" })).toBeTruthy();
});
```

Implement local `renderMenu(overrides)` and `openMovePicker()` helpers with concrete default props from the existing tests.

- [ ] **Step 4: Add failing issue-identity and stale-operation coverage**

```ts
it("resets the destination view when the selected issue changes", async () => {
  const view = renderMenu();
  await openMovePicker();
  await fireEvent.input(screen.getByRole("searchbox", { name: "Find project" }), {
    target: { value: "road" },
  });

  await view.rerender({ issue: makeIssue("open", { uid: "issue-2" }), projects });
  expect(screen.queryByRole("searchbox", { name: "Find project" })).toBeNull();
  expect(screen.getByRole("button", { name: "More actions" }).getAttribute("aria-expanded")).toBe("false");
});

it("ignores an old A move after navigating A to B to A", async () => {
  let finishOldMove!: (moved: boolean) => void;
  const oldMove = new Promise<boolean>((resolve) => {
    finishOldMove = resolve;
  });
  const onMoveIssue = vi.fn(() => oldMove);
  const view = renderMenu({ onMoveIssue });

  await openMovePicker();
  await fireEvent.click(screen.getByRole("button", { name: /Roadmap/ }));
  await view.rerender({ issue: makeIssue("open", { uid: "issue-b" }), projects });
  await view.rerender({ issue: makeIssue("open", { uid: "issue-1" }), projects });
  await openMovePicker();

  finishOldMove(true);
  await oldMove;
  expect(screen.getByRole("searchbox", { name: "Find project" })).toBeTruthy();
});

it("does not dismiss or resubmit while a move is pending", async () => {
  let finishMove!: (moved: boolean) => void;
  const pendingMove = new Promise<boolean>((resolve) => {
    finishMove = resolve;
  });
  const onMoveIssue = vi.fn(() => pendingMove);
  renderMenu({ onMoveIssue });

  await openMovePicker();
  await fireEvent.click(screen.getByRole("button", { name: /Roadmap/ }));
  await fireEvent.keyDown(screen.getByRole("dialog", { name: "Move to another project" }), { key: "Escape" });
  await fireEvent.mouseDown(document.body);

  expect(screen.getByRole("searchbox", { name: "Find project" })).toBeTruthy();
  expect(screen.getByRole("button", { name: /Roadmap/ })).toBeDisabled();
  expect(onMoveIssue).toHaveBeenCalledTimes(1);
  finishMove(false);
  await pendingMove;
});
```

Adjust `makeIssue` to accept partial issue overrides rather than duplicating fixtures.

- [ ] **Step 5: Run the focused test to verify failure**

```bash
node ../node_modules/vite-plus/bin/vp test run --project unit src/lib/components/kata/KataIssueOverflowMenu.test.ts
```

Expected: FAIL because move props and interaction do not exist.

- [ ] **Step 6: Return handled move success from both workspaces**

In `KataWorkspace.svelte`:

```ts
async function moveSelectedIssue(toProjectUID: string): Promise<boolean> {
  const selected = store.selectedIssue?.issue;
  if (!selected) return false;
  return runViewTask(() => store.moveIssue(selected.uid, actor, toProjectUID));
}
```

Apply the same shape in `KataWorkspaceSidebarPane.svelte`, returning its existing `runTask(...)` result. This keeps error copy owned by `requestError`/`error` and gives the menu a close/retry signal.

Add `frontend/src/lib/components/terminal/KataWorkspaceSidebarPane.test.ts` with a mounted-host fixture that stubs the Kata API bootstrap and move endpoint. Exercise `More actions → Move to another project` through the real pane: one test returns a handled move failure and asserts the pane's alert plus retryable picker, then a retry succeeds and closes it. This is required alongside `KataWorkspace.test.ts`; do not substitute an isolated callback unit test.

- [ ] **Step 7: Forward projects and the move callback**

Change `KataIssueDetail` prop typing to:

```ts
onMoveIssue: (toProjectUID: string) => boolean | Promise<boolean>;
```

Pass:

```svelte
<KataIssueOverflowMenu
  {issue}
  {projects}
  hasChecklist={checklistItems().length > 0 || checklistRevealed}
  hasRecurrence={!canCreateRecurrence}
  onMoveIssue={onMoveIssue}
  onAddChecklist={onRevealChecklist}
  onCreateRecurrence={onCreateRecurrence}
  onDeleteIssue={onDeleteIssue}
/>
```

Update default test callbacks from `async () => {}` to `async () => true`.

- [ ] **Step 8: Implement destinations and explicit menu modes**

In `KataIssueOverflowMenu.svelte`:

```ts
interface Props {
  issue: KataTaskDetail;
  projects: KataProjectSummary[];
  hasChecklist: boolean;
  hasRecurrence: boolean;
  onMoveIssue: (toProjectUID: string) => boolean | Promise<boolean>;
  onAddChecklist: () => void;
  onCreateRecurrence: () => void;
  onDeleteIssue: () => boolean | Promise<boolean>;
}

let menuView = $state<"actions" | "move">("actions");
let moveQuery = $state("");
let movingProjectUID = $state<string | null>(null);
let interactionGeneration = 0;
let moveOperationGeneration = 0;
let activeMoveOperation = $state<number | null>(null);

let eligibleProjects = $derived.by(() =>
  projects
    .filter((project) => project.uid !== issue.issue.project_uid && project.metadata.role !== "inbox")
    .toSorted((a, b) => a.name.localeCompare(b.name, undefined, { sensitivity: "base" })),
);

let duplicateContexts = $derived.by(() => {
  const groups = new Map<string, KataProjectSummary[]>();
  for (const project of eligibleProjects) {
    const key = project.name.trim().toLocaleLowerCase();
    groups.set(key, [...(groups.get(key) ?? []), project]);
  }

  const contexts = new Map<string, string>();
  for (const group of groups.values()) {
    if (group.length < 2) continue;
    const areaCounts = new Map<string, number>();
    for (const project of group) {
      const area = typeof project.metadata.area === "string" ? project.metadata.area.trim() : "";
      if (area) areaCounts.set(area, (areaCounts.get(area) ?? 0) + 1);
    }
    for (const project of group) {
      const area = typeof project.metadata.area === "string" ? project.metadata.area.trim() : "";
      contexts.set(project.uid, area && areaCounts.get(area) === 1 ? area : project.uid);
    }
  }
  return contexts;
});

function destinationContext(project: KataProjectSummary): string | null {
  return duplicateContexts.get(project.uid) ?? null;
}

let filteredProjects = $derived.by(() => {
  const query = moveQuery.trim().toLowerCase();
  return query
    ? eligibleProjects.filter((project) =>
        [project.name, destinationContext(project) ?? ""].some((value) => value.toLowerCase().includes(query)),
      )
    : eligibleProjects;
});
```

Include `eligibleProjects.length > 0` in `hasAnyAction`. Increment `interactionGeneration` and reset `menuView` and `moveQuery` whenever the issue UID changes or a non-pending interaction closes. Do not clear `activeMoveOperation` or `movingProjectUID` from close/reset helpers while a request is pending; dismissal is disabled until that operation settles. Keep the pending destination in the rendered list if reactive project updates would otherwise remove it.

- [ ] **Step 9: Render the explicit action and searchable picker**

Add exact action copy in the normal menu:

```svelte
{#if eligibleProjects.length > 0}
  <li>
    <button type="button" class="overflow-item" role="menuitem" onclick={openMovePicker}>
      Move to another project
    </button>
  </li>
{/if}
```

When `menuView === "move"`, render a popover panel with honest dialog/list semantics, a kit `SearchInput` labeled `Find project`, and native project buttons:

```svelte
<div class="move-picker kit-popover-card" role="dialog" aria-label="Move to another project" onkeydown={handleMoveKeydown}>
  <SearchInput
    bind:value={moveQuery}
    bind:inputEl={moveSearchInput}
    block
    ariaLabel="Find project"
    placeholder="Search projects"
  />
  <div class="move-options" aria-label="Project destinations">
    {#each filteredProjects as project (project.uid)}
      <button
        type="button"
        disabled={movingProjectUID !== null}
        onclick={() => void moveIssue(project.uid)}
      >
        <span class="move-project-name">{project.name}</span>
        {#if destinationContext(project)}
          <span class="move-project-context">{destinationContext(project)}</span>
        {/if}
        <span class="move-project-count">{project.open_count}</span>
      </button>
    {:else}
      <p>No matching projects</p>
    {/each}
  </div>
</div>
```

Declare `let moveSearchInput: HTMLInputElement | undefined = $state();` and focus it after switching to `menuView = "move"` with `await tick()`. Do not add a one-use shared picker abstraction.

- [ ] **Step 10: Handle stale async completion and retry**

```ts
async function moveIssue(projectUID: string): Promise<void> {
  if (activeMoveOperation !== null) return;
  const sourceIssueUID = issue.issue.uid;
  const sourceInteraction = interactionGeneration;
  const operation = ++moveOperationGeneration;
  activeMoveOperation = operation;
  movingProjectUID = projectUID;
  try {
    const moved = await onMoveIssue(projectUID);
    if (
      activeMoveOperation !== operation ||
      interactionGeneration !== sourceInteraction ||
      issue.issue.uid !== sourceIssueUID
    )
      return;
    if (moved !== false) closeMenu();
  } finally {
    if (activeMoveOperation === operation) {
      activeMoveOperation = null;
      movingProjectUID = null;
    }
  }
}
```

Do not duplicate mutation error messages in the menu.

- [ ] **Step 11: Run focused tests and commit**

```bash
node ../node_modules/vite-plus/bin/vp test run --project unit \
  src/lib/components/kata/KataIssueDetail.test.ts \
  src/lib/components/kata/KataIssueOverflowMenu.test.ts \
  src/lib/features/kata/KataWorkspace.test.ts \
  src/lib/components/terminal/KataWorkspaceSidebarPane.test.ts
git add frontend/src/lib/components/kata/KataIssueOverflowMenu.svelte \
  frontend/src/lib/components/kata/KataIssueOverflowMenu.test.ts \
  frontend/src/lib/components/kata/KataIssueDetail.svelte \
  frontend/src/lib/components/kata/KataIssueDetail.test.ts \
  frontend/src/lib/features/kata/KataWorkspace.svelte \
  frontend/src/lib/components/terminal/KataWorkspaceSidebarPane.svelte \
  frontend/src/lib/components/terminal/KataWorkspaceSidebarPane.test.ts
git commit -m "feat: move Kata reassignment into task actions"
```

---

### Task 4: Make the Issue Project Breadcrumb Passive

**Files:**

- Modify: `frontend/src/lib/components/kata/KataIssueDetail.test.ts`
- Modify: `frontend/src/lib/components/kata/KataIssueDetail.svelte`

**Interfaces:**

- Preserves: `currentProjectName(): string` fallback order.
- Removes: breadcrumb-owned destination picker; issue reassignment is already available through the overflow menu added in Task 3 before the breadcrumb control is removed.

- [ ] **Step 1: Replace the breadcrumb-move test**

Remove the existing `moves to a non-inbox project from the crumb picker` test. Add:

```ts
it("renders the current project as passive breadcrumb text", () => {
  renderDetail();
  const detail = screen.getByRole("region", { name: "Task detail" });

  expect(within(detail).getByText("Inbox")).toBeTruthy();
  expect(within(detail).queryByRole("button", { name: /^Move issue from/ })).toBeNull();
  expect(within(detail).queryByRole("combobox", { name: "Move issue project" })).toBeNull();
});
```

- [ ] **Step 2: Cover both fallback levels**

Update the existing fallback test to assert loaded project name text, then add the final UID fallback:

```ts
it("uses the loaded project name when issue project_name is empty", () => {
  renderDetail({ issue: makeIssue({ project_uid: "project-2", project_name: "" }) });
  expect(screen.getByText("Roadmap")).toBeTruthy();
});

it("uses the project UID when no project name can be resolved", () => {
  renderDetail({
    issue: makeIssue({ project_id: 99, project_uid: "project-missing", project_name: "" }),
    projects: [],
  });
  expect(screen.getByText("project-missing")).toBeTruthy();
});
```

- [ ] **Step 3: Run the focused test to verify failure**

```bash
node ../node_modules/vite-plus/bin/vp test run --project unit src/lib/components/kata/KataIssueDetail.test.ts
```

Expected: FAIL because the project remains a `TypeaheadTrigger` button.

- [ ] **Step 4: Render passive text**

In `KataIssueDetail.svelte`, replace the breadcrumb `TypeaheadTrigger` with:

```svelte
<span class="crumb-project" title={currentProjectName()}>{currentProjectName()}</span>
<span class="crumb-sep">/</span>
<span class="crumb-id">{issue.issue.short_id}</span>
```

Keep `currentProjectName()` unchanged. Remove only breadcrumb-picker imports, options, and CSS; retain `TypeaheadTrigger` if other detail controls still use it. Style `.crumb-project` with truncation and muted text, without a border, hover state, cursor, or chevron.

- [ ] **Step 5: Run and commit**

```bash
node ../node_modules/vite-plus/bin/vp test run --project unit src/lib/components/kata/KataIssueDetail.test.ts
git add frontend/src/lib/components/kata/KataIssueDetail.svelte frontend/src/lib/components/kata/KataIssueDetail.test.ts
git commit -m "feat: make Kata project breadcrumbs passive"
```

---

### Task 5: Preserve Escape and Focus Behavior

**Files:**

- Modify: `frontend/src/lib/components/kata/KataIssueOverflowMenu.test.ts`
- Modify: `frontend/src/lib/components/kata/KataIssueOverflowMenu.svelte`
- Create only if jsdom cannot reliably assert focus: `frontend/src/lib/components/kata/KataIssueOverflowMenu.browser.svelte.ts`

**Interfaces:**

- Consumes: kit `SearchInput` behavior where Escape clears a nonempty value before the owner handles an empty-query Escape.
- Produces: empty-query Escape closes the move picker without moving and restores focus to More actions.

- [ ] **Step 1: Add the failing keyboard test**

```ts
it("dismisses an empty move picker with Escape and restores trigger focus", async () => {
  const onMoveIssue = vi.fn(async () => true);
  renderMenu({ onMoveIssue });

  const trigger = screen.getByRole("button", { name: "More actions" });
  trigger.focus();
  await fireEvent.keyDown(trigger, { key: "Enter" });
  await fireEvent.click(screen.getByRole("menuitem", { name: "Move to another project" }));

  const search = screen.getByRole("searchbox", { name: "Find project" });
  await waitFor(() => expect(search).toBe(document.activeElement));
  await fireEvent.keyDown(search, { key: "Escape" });

  expect(onMoveIssue).not.toHaveBeenCalled();
  expect(screen.queryByRole("searchbox", { name: "Find project" })).toBeNull();
  await waitFor(() => expect(trigger).toBe(document.activeElement));
});
```

If jsdom cannot prove focus movement, move this exact interaction to the browser file and keep sorting/filtering/success tests in the unit project.

- [ ] **Step 2: Add two-stage Escape coverage**

```ts
it("clears a move query before Escape dismisses the picker", async () => {
  renderMenu();
  await openMovePicker();
  const search = screen.getByRole("searchbox", { name: "Find project" });
  await fireEvent.input(search, { target: { value: "road" } });

  await fireEvent.keyDown(search, { key: "Escape" });
  expect(search).toHaveValue("");
  expect(screen.getByRole("dialog", { name: "Move to another project" })).toBeTruthy();

  await fireEvent.keyDown(search, { key: "Escape" });
  expect(screen.queryByRole("dialog", { name: "Move to another project" })).toBeNull();
});
```

- [ ] **Step 3: Implement local cleanup and focus restoration**

Bind the trigger and move search input. Handle only the empty-query Escape that reaches the picker owner:

```ts
function handleMoveKeydown(event: KeyboardEvent): void {
  if (event.key !== "Escape" || moveQuery !== "") return;
  event.preventDefault();
  closeMenu();
  queueMicrotask(() => overflowTrigger?.focus());
}
```

Use the same cleanup function for outside-click and issue-change reset, but restore focus only for keyboard dismissal. Do not install a window-level Escape handler.

- [ ] **Step 4: Run the correct lane and commit**

For unit coverage:

```bash
node ../node_modules/vite-plus/bin/vp test run --project unit src/lib/components/kata/KataIssueOverflowMenu.test.ts
```

If the browser test was necessary:

```bash
node ../node_modules/vite-plus/bin/vp test run --project browser src/lib/components/kata/KataIssueOverflowMenu.browser.svelte.ts
```

Then commit the focused interaction fix:

```bash
git add frontend/src/lib/components/kata/KataIssueOverflowMenu.svelte \
  frontend/src/lib/components/kata/KataIssueOverflowMenu.test.ts \
  frontend/src/lib/components/kata/KataIssueOverflowMenu.browser.svelte.ts
git commit -m "fix: preserve focus when dismissing Kata moves"
```

If no browser file exists, omit it from `git add`.

---

### Task 6: Replace Obsolete Full-Stack Rename Workflows

**Files:**

- Modify: `frontend/tests/e2e-full/kata.spec.ts`

**Interfaces:**

- Preserves: seeded real backend, project creation, project scope routing, daemon move endpoint, and rename API fixture support.
- Replaces: four tests that require pencil-button or double-click sidebar renaming.

- [ ] **Step 1: Remove obsolete rename scenarios**

Delete these tests:

```text
kata project rename submits inline input
kata project rows can be renamed by double-clicking
kata project row double-click enters rename
kata project rename input cancels on Escape
```

Keep the fixture PATCH handler because daemon rename capability remains supported.

- [ ] **Step 2: Add a navigation-only project-row test**

```ts
test("kata project rows select scopes without rename controls", async ({ page }) => {
  const projectPatches: string[] = [];
  page.on("request", (request) => {
    if (request.method() === "PATCH" && request.url().includes("/api/v1/projects/1")) {
      projectPatches.push(request.url());
    }
  });

  await page.goto(`${server.info.base_url}/kata`);
  await expectKataDaemonSwitcherReady(page);
  const finances = page.getByRole("button", { name: /^Finances\b/ });
  await expect(page.getByRole("button", { name: "Rename Finances" })).toHaveCount(0);
  await finances.click();
  await expect(page).toHaveURL(/scope=project-finance/);
  await expect(page.getByRole("textbox", { name: "Rename project" })).toHaveCount(0);
  expect(projectPatches).toEqual([]);
});
```

Follow the surrounding tests’ concrete setup sequence: start `startKataBackend()`, call `configureKataHome(backend.url)`, start `startIsolatedE2EServer()`, navigate to `${server.info.base_url}/kata`, and restore/stop all handles in `finally`. Extract a helper only if at least three of the newly edited tests use the identical sequence.

- [ ] **Step 3: Add the double-click regression**

```ts
test("kata project double-click remains navigation-only", async ({ page }) => {
  const projectPatches: string[] = [];
  page.on("request", (request) => {
    if (request.method() === "PATCH" && request.url().includes("/api/v1/projects/1")) {
      projectPatches.push(request.url());
    }
  });

  await page.goto(`${server.info.base_url}/kata`);
  await expectKataDaemonSwitcherReady(page);
  await page.getByRole("button", { name: /^Finances\b/ }).dblclick();
  await expect(page).toHaveURL(/scope=project-finance/);
  await expect(page.getByRole("textbox", { name: "Rename project" })).toHaveCount(0);
  expect(projectPatches).toEqual([]);
});
```

Do not assert exact scoped-list request counts.

- [ ] **Step 4: Update group selectors**

Where the existing test expects area headings, query the new expandable buttons and assert their state:

```ts
await expect(page.getByRole("button", { name: /^Personal\s+1$/ })).toHaveAttribute("aria-expanded", "true");
await expect(page.getByRole("button", { name: /^Work\s+1$/ })).toHaveAttribute("aria-expanded", "true");
```

Keep project creation, system-view switching, and inbox-project hiding tests.

- [ ] **Step 5: Move the real-daemon reassignment flow with keyboard selection**

Rename the existing breadcrumb move test to `kata More actions moves tasks through the configured external daemon` and change only the UI path:

```ts
const moreActions = page.getByRole("button", { name: "More actions" });
await moreActions.focus();
await page.keyboard.press("Enter");
await page.getByRole("menuitem", { name: "Move to another project" }).press("Enter");
const search = page.getByRole("searchbox", { name: "Find project" });
await search.fill("kat");
await page.keyboard.press("Tab");
await page.keyboard.press("Enter");
await expect(page.getByText("Kata", { exact: true })).toBeVisible();
```

Retain the existing assertions for `POST /api/v1/projects/1/issues/issue-rent/actions/move` and backend state changing to the Kata project.

- [ ] **Step 6: Add full-stack failure and retry coverage**

Add `moveFailures?: number[] | undefined` to `KataBackendOptions`, add required `moveFailures: number[]` to `BackendState`, initialize it in `startKataBackend` with `moveFailures: [...(options.moveFailures ?? [])]`, and consume the next status in the move-action handler before mutating state:

```ts
const moveFailure = state.moveFailures.shift();
if (moveFailure !== undefined) {
  writeJSON(res, moveFailure, {
    error: { code: "internal", message: "move failed" },
  });
  return;
}
```

Add:

```ts
test("kata move failure stays retryable through the configured external daemon", async ({ page }) => {
  const backend = await startKataBackend({ moveFailures: [500] });
  const kataHome = await configureKataHome(backend.url);
  const server = await startIsolatedE2EServer();

  try {
    await page.goto(`${server.info.base_url}/kata?issue=issue-rent`);
    const detail = page.getByRole("region", { name: "Task detail" });
    await detail.getByRole("button", { name: "More actions" }).click();
    await detail.getByRole("menuitem", { name: "Move to another project" }).click();
    await detail.getByRole("searchbox", { name: "Find project" }).fill("kat");
    await detail.getByRole("button", { name: /Kata/ }).click();

    await expect(page.getByRole("alert")).toContainText("move failed");
    await expect(detail.getByRole("dialog", { name: "Move to another project" })).toBeVisible();
    expect(backend.state.issues.find((issue) => issue.uid === "issue-rent")?.project_uid).toBe("project-finance");

    await detail.getByRole("button", { name: /Kata/ }).click();
    await expect(detail.getByText("Kata", { exact: true })).toBeVisible();
    expect(backend.state.issues.find((issue) => issue.uid === "issue-rent")?.project_uid).toBe("project-kata");
  } finally {
    await server.stop();
    kataHome.restore();
    await backend.close();
  }
});
```

Assert the actual workspace error wording emitted by the implementation if it wraps the daemon message; branch on the stable visible alert, not a CSS selector.

- [ ] **Step 7: Add narrow-width keyboard cancellation**

```ts
test("kata move picker dismisses at narrow width without moving", async ({ page }) => {
  const moveRequests: string[] = [];
  await page.setViewportSize({ width: 390, height: 844 });
  page.on("request", (request) => {
    if (request.url().includes("/actions/move")) moveRequests.push(request.url());
  });

  await page.goto(`${server.info.base_url}/kata?issue=issue-rent`);
  await expect(page.getByRole("region", { name: "Task detail" })).toBeVisible();
  const trigger = page.getByRole("button", { name: "More actions" });
  await trigger.focus();
  await page.keyboard.press("Enter");
  await page.getByRole("menuitem", { name: "Move to another project" }).press("Enter");
  const picker = page.getByRole("dialog", { name: "Move to another project" });
  await expect(picker).toBeInViewport();
  await page.keyboard.press("Escape");
  await expect(picker).toHaveCount(0);
  await expect(trigger).toBeFocused();
  expect(moveRequests).toEqual([]);
});
```

Use the same backend/home/server setup as the surrounding test, navigate directly to `${server.info.base_url}/kata?issue=issue-rent`, and wait for `page.getByRole("region", { name: "Task detail" })` before interacting.

- [ ] **Step 8: Run targeted full-stack tests**

```bash
node ./scripts/run-e2e-to-file.ts tests/e2e-full/kata.spec.ts --grep "kata (sidebar|project|More actions|move picker|move failure)"
```

Expected: PASS in both configured browser projects. During iteration, `--project=chromium` is acceptable, but this two-browser command must pass before commit.

- [ ] **Step 9: Commit**

```bash
git add frontend/tests/e2e-full/kata.spec.ts
git commit -m "test: cover the cleaned-up Kata project workflows"
```

---

### Task 7: Final Verification

**Files:**

- Verify only; do not edit after these commands without rerunning the affected lane.

**Interfaces:**

- Confirms all spec acceptance criteria and repository pre-push requirements.

- [ ] **Step 1: Run focused unit coverage**

From `frontend/`:

```bash
node ../node_modules/vite-plus/bin/vp test run --project unit \
  src/lib/components/kata/KataSidebar.test.ts \
  src/lib/components/kata/KataIssueDetail.test.ts \
  src/lib/components/kata/KataIssueOverflowMenu.test.ts \
  src/lib/features/kata/KataWorkspace.test.ts \
  src/lib/components/terminal/KataWorkspaceSidebarPane.test.ts \
  src/lib/stores/kata-workspace.svelte.test.ts \
  src/lib/api/kata/taskClient.test.ts
```

Expected: PASS.

- [ ] **Step 2: Run browser component coverage if created**

```bash
node ../node_modules/vite-plus/bin/vp test run --project browser src/lib/components/kata/KataIssueOverflowMenu.browser.svelte.ts
```

Expected: PASS. Skip only when no browser component file was created because unit coverage proved focus behavior reliably.

- [ ] **Step 3: Run frontend package checks**

```bash
node ../node_modules/vite-plus/bin/vp run frontend-package-check
```

Expected: PASS with no Svelte, TypeScript, formatting, or kit-usage errors.

- [ ] **Step 4: Run the complete Vitest projects**

```bash
node ../node_modules/vite-plus/bin/vp test run --project unit
node ../node_modules/vite-plus/bin/vp test run --project browser
```

Expected: PASS. If browser port `63315` is occupied by a sibling worktree, wait and retry; do not kill the other process.

- [ ] **Step 5: Run the complete affected Playwright suites**

```bash
node ./scripts/run-e2e-to-file.ts tests/e2e-full/kata.spec.ts
node ./scripts/run-e2e-to-file.ts tests/e2e-full/sidebar-scroll-indicator.spec.ts
```

Expected: PASS in both configured browser projects. The second file verifies the shared geometry; do not add duplicate Kata-specific scroll math.

- [ ] **Step 6: Exercise the real app flow**

Invoke the repository `verify` skill and drive these observable behaviors:

1. Open Kata navigation and collapse/reopen an area.
2. Select and double-click a project; both remain navigation-only.
3. Create a project and enter its scope.
4. Open an issue and confirm its project breadcrumb is passive.
5. Open More actions, filter destinations, move the issue, and confirm its project changes.
6. Open the move picker again, press Escape, and confirm focus returns without a mutation.

Expected: all six behaviors match the spec in the running app.

- [ ] **Step 7: Inspect the final diff**

Run:

```bash
git diff origin/main...HEAD --check
git status --short
rg -n "onRenameProject|Rename project|Rename Finances|ondblclick" \
  frontend/src/lib/components/kata/KataSidebar.svelte \
  frontend/src/lib/features/kata/KataWorkspace.svelte
rg -n "renameProject" \
  frontend/src/lib/api/kata/taskClient.ts \
  frontend/src/lib/stores/kata-workspace.svelte.ts
```

Expected:

- `git diff --check` has no output.
- Working tree is clean after commits.
- No sidebar rename references remain.
- Client/store rename operations still exist.
- No backend, DB, OpenAPI, generated-client, or unrelated shared-component files changed.

- [ ] **Step 8: Run final code review and simplification**

Invoke `/code-review high` on the branch diff, address verified findings in new commits, then invoke `/simplify` and apply only in-scope quality improvements. Rerun every affected test lane after any edit.

- [ ] **Step 9: Record context decision**

Run:

```bash
scripts/context-sync --check
```

If the implementation only realizes this dated feature spec, mark a concrete no-update decision. If it reveals a reusable frontend invariant or gotcha not already in `context/ui-design-system.md` or `context/ui-interaction-contracts.md`, add a terse anchored context update and commit it separately.
