# Terminal Sessions As Detail Panes Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Let a workspace's terminal sessions be promoted out of the inline workspace pane into top-level detail panes, and collapse the three bars above a terminal into one.

**Architecture:** A second portal layer, keyed by session, mirrors the existing whole-workspace one: each mounted session's `TerminalPane` is rendered once into an app-level pool and reparented into whichever slot renders it — the workspace container's tab panel or a promoted `session:<key>` pane in the surface's `PaneLayoutStore`. Promotion is an ordinary tab drag whose effect depends on which tree accepted the drop.

**Tech Stack:** Svelte 5 runes, `@middleman/ui` shared components, Vitest (unit + browser projects), Playwright (mock and real-backend).

## Global Constraints

- Design source of truth: `docs/superpowers/specs/2026-07-26-session-panes-design.md`. Its predecessor `2026-07-25-generalized-pane-layout-design.md` still governs the detail pane tree.
- Session pane keys are `session:<workspaceId>:<hostKey>:<sessionKey>`. `WorkspaceTerminalView`'s existing `workflowTabKeyForSession` (`:841`) mints the workspace-local `session:<sessionKey>` for its own tree; the detail-tree key must carry workspace and host too, because a session key is unique only within a workspace on a host. Provide one builder and one parser, and use them on both sides.
- Registry keys additionally carry the session's `created_at` generation; layout keys deliberately do not. A relaunched session reappears where the user put it, without inheriting the dead generation's subtree.
- Optimize for one to three sessions per item. Do not add pooling, eviction, or virtualization for more.
- Exactly one slot may be mounted per session key at a time, exactly as the existing host allows one slot per surface.
- A slot registers its **visibility**, not just its element. An inactive tab panel stays mounted with `visibility: hidden` (`TabbedPanelTree`), so element presence does not mean the terminal is on screen. `TerminalPane.active` for a pooled session is derived from the registered visibility of its slot and from nothing else; a terminal left `active` behind a hidden tab steals focus and fights the visible one for keystrokes.
- Every `@lucide/svelte/icons/<name>` import added anywhere must also be added to `optimizeDeps.include` in `frontend/vite.config.ts`.
- Run frontend tests from `frontend/` with `../node_modules/.bin/vp`. Never `npm`. Never bare `vp fmt` — name the files.
- Commit every task through the `kenn:commit` skill, after `context-sync --commit`.

---

## File Structure

| File                                                                                | Responsibility                                                                                      |
| ----------------------------------------------------------------------------------- | --------------------------------------------------------------------------------------------------- |
| `frontend/src/lib/stores/session-host.svelte.ts` (create)                           | Registry of live session terminals: parking, per-key slot registration, slot attachment.            |
| `frontend/src/lib/components/terminal/SessionTerminalPool.svelte` (create)          | Renders one `TerminalPane` per mounted session into the pool; owns the parking node.                |
| `frontend/src/lib/components/terminal/WorkspaceHost.svelte` (modify)                | Mounts the pool as a sibling of the reparented wrapper so promoted terminals outlive the container. |
| `frontend/src/lib/components/terminal/WorkspaceTerminalView.svelte` (modify)        | Renders session slots instead of `TerminalPane`; loses the `Workflow` toolbar and the `Home` tab.   |
| `frontend/src/lib/components/terminal/WorkspacePaneControls.svelte` (create)        | The single top-right button and its popover (presets, zoom, options, launch).                       |
| `frontend/src/lib/components/terminal/WorkspaceLauncherOverlay.svelte` (create)     | The launcher, previously the `Home` tab body.                                                       |
| `packages/ui/src/stores/pane-surfaces.ts` (modify)                                  | Session keys accepted outside the static vocabulary and never reinserted.                           |
| `packages/ui/src/components/shared/tabbed-panel-layout.ts` (modify)                 | `normalizeTabbedPanelTree` gains a "keep but never reinsert" class of key.                          |
| `packages/ui/src/stores/paneLayout.svelte.ts` (modify)                              | Promotion/demotion edits; dropping stale session entries.                                           |
| `packages/ui/src/views/{PRListView,IssueListView,ActivityFeedView}.svelte` (modify) | Promoted session panes in `paneTabs`, availability from the claimed workspace's sessions.           |
| `frontend/src/lib/stores/workspace-host.svelte.ts` (modify)                         | Dock mode, Focus Terminal, and visibility restated over the container plus promoted panes.          |

---

### Task 1: Session host registry

**Files:**

- Create: `frontend/src/lib/stores/session-host.svelte.ts`
- Test: `frontend/src/lib/stores/session-host.test.ts`

**Interfaces:**

- Consumes: nothing. Deliberately standalone, like `workspace-host.svelte.ts`.
- Produces:
  - `type SessionHostKey = string` built by `sessionHostKey(workspaceId: string, hostKey: string | undefined, sessionKey: string, generation: string): SessionHostKey`, where `generation` is the session's `created_at`
  - `registerSessionSlot(key: SessionHostKey, el: HTMLElement | null): void`
  - `setSessionSlotVisible(key: SessionHostKey, visible: boolean): void`
  - `getSessionSlotElement(key: SessionHostKey): HTMLElement | null`
  - `isSessionSlotVisible(key: SessionHostKey): boolean` — false when no slot is registered
  - `sessionSlotAttachment(key: SessionHostKey): Attachment<HTMLElement>`
  - `registerSessionParking(el: HTMLElement | null): void` / `getSessionParking(): HTMLElement | null`
  - `interface MountedSession { hostKey: SessionHostKey; websocketPath: string; status: string }`
  - `mountedSessions(): readonly MountedSession[]`, `isSessionMounted(key)`, `noteSessionMounted(session)`, `noteSessionUnmounted(key)`
  - `resetSessionHostForTest(): void`

Mirror `registerSlotElement`'s targeted-property-write comment (`workspace-host.svelte.ts:343`): a full-object reassignment inside an attachment effect loops forever.

Two decisions the code forced, both departures from the first draft of this task:

- **Visibility is published separately from the element**, not passed to the
  attachment. An attachment that reads reactive state re-runs through its own
  cleanup, so folding visibility into it would unregister and re-register the
  slot on every tab switch — the pool would park and re-adopt a live terminal for
  a change that moved no DOM.
- **The mounted set holds descriptors, not keys.** Mount state cannot belong to
  `WorkspaceTerminalView`: once a session is promoted, that view renders no slot
  for it and must not unmount it. Whoever reveals a session notes it with the
  websocket path and status the pool needs, promotion changes nothing, and Task 9
  owns disposal.

Key parts are percent-encoded and joined with `/` rather than separated by a raw
NUL: opaque ids must not be able to spell each other's keys, and the result stays
readable in a `data-` attribute.

- [x] **Step 1: Write the failing test**

`frontend/src/lib/stores/session-host.test.ts` covers: the key separating
workspace, host, session, and generation (and parts that contain the separator);
one slot per key; a mounted-but-hidden slot reported as not visible; a session
with no slot never visible even if marked so; visibility not surviving a slot
re-registration; the mounted set updating a changed status in place; and
unmounting dropping the slot, so the pool cannot reparent a subtree that is gone.

- [x] **Step 2: Run it and watch it fail**

Run: `cd frontend && ../node_modules/.bin/vp test --project unit session-host`
Expected: FAIL, module not found.

- [x] **Step 3: Implement the registry**

`frontend/src/lib/stores/session-host.svelte.ts`, to the interface above.
`registerSessionSlot` writes `slotEls[key]` and `slotVisible[key]` as targeted
properties, never a spread reassignment: it runs from inside an attachment's own
effect, and reading every key while writing the same binding makes the effect its
own dependency (`effect_update_depth_exceeded`).

- [x] **Step 4: Run the test**

Run: `cd frontend && ../node_modules/.bin/vp test --project unit session-host`
Expected: PASS. (8 tests.)

- [x] **Step 5: Commit** via the `kenn:commit` skill.

---

### Task 2: The terminal pool and its reparenting

**Files:**

- Create: `frontend/src/lib/components/terminal/SessionTerminalPool.svelte`
- Modify: `frontend/src/lib/components/terminal/WorkspaceHost.svelte`
- Test: `frontend/src/lib/components/terminal/SessionTerminalPool.browser.svelte.ts`, `SessionTerminalPoolHarness.svelte`

**Interfaces:**

- Consumes: Task 1's registry; `TerminalPane` (`websocketPath`, `active`, `disabled`, `onExit`, `initialStatus`).
- Produces: `SessionTerminalPool` with props `{ sessions: PoolSession[] }` where
  `PoolSession = { hostKey: SessionHostKey; websocketPath: string; status: string }`.
  There is no `active` prop: the pool derives it per session from
  `isSessionSlotVisible(session.hostKey)`. The caller cannot know it — only the
  slot that renders the session knows whether its tab panel is the visible one.

The pool is a **sibling** of `.workspace-host-wrapper`, not a child. A promoted session must survive the container being parked, and the wrapper is exactly what gets parked.

Reparenting copies the placement effect from `WorkspaceHost.svelte:82`: park, `await tick()`, append to destination, reveal on non-zero geometry via `requestAnimationFrame`.

- [ ] **Step 1: Write the failing browser test**

```ts
// SessionTerminalPool.browser.svelte.ts
it("moves one live terminal subtree between slots without recreating it", async () => {
  const harness = mount(SessionTerminalPoolHarness, { target, props: { slot: "a" } });
  const wrapper = await vi.waitFor(() => {
    const el = document.querySelector("[data-session-host='ws-1\0\0agent']");
    expect(el).not.toBeNull();
    return el as HTMLElement;
  });
  expect(wrapper.parentElement).toBe(document.querySelector("[data-slot='a']"));

  await harness.setSlot("b");

  // Same node, not an equal one: a recreated wrapper is a dropped websocket and
  // a blank terminal.
  expect(document.querySelector("[data-session-host='ws-1\0\0agent']")).toBe(wrapper);
  expect(wrapper.parentElement).toBe(document.querySelector("[data-slot='b']"));
});

it("keeps two sessions live at once", async () => {
  /* two slots, two wrappers, both parented */
});

it("parks a session whose slot unmounts and keeps it alive", async () => {
  /* slot -> none, wrapper in parking */
});

it("deactivates a terminal whose slot is mounted but hidden", async () => {
  /* two session tabs in one leaf: only the active tab's TerminalPane is active,
     so the hidden one neither claims focus nor resizes to a zero-height box */
});
```

- [ ] **Step 2: Run and watch it fail**

Run: `cd frontend && ../node_modules/.bin/vp test --project browser SessionTerminalPool`
Expected: FAIL, component missing.

- [ ] **Step 3: Implement the pool**

```svelte
<!-- SessionTerminalPool.svelte -->
<div class="session-pool-parking" bind:this={parkingNode} aria-hidden="true"></div>
{#each sessions as session (session.hostKey)}
  <div class="session-host-wrapper" data-session-host={session.hostKey} bind:this={wrappers[session.hostKey]}>
    <TerminalPane
      websocketPath={session.websocketPath}
      reconnectOnExit={false}
      active={isSessionSlotVisible(session.hostKey)}
      initialStatus={session.status}
    />
  </div>
{/each}
```

with one placement effect per session key, reading `getSessionSlotElement(session.hostKey)`.

- [ ] **Step 4: Mount it from `WorkspaceHost.svelte`**

As a sibling of the wrapper, fed from the claimed workspace's mounted sessions.

- [ ] **Step 5: Run the browser tests**

Run: `cd frontend && ../node_modules/.bin/vp test --project browser SessionTerminalPool`
Expected: PASS.

- [ ] **Step 6: Commit.**

---

### Task 3: WorkspaceTerminalView renders slots, not terminals

**Files:**

- Modify: `frontend/src/lib/components/terminal/WorkspaceTerminalView.svelte` (`:3259`, the session branch of `renderTab`)
- Test: `frontend/src/lib/components/terminal/WorkspaceTerminalView.sessionSlots.browser.svelte.ts`

Replace the inline `TerminalPane` with the pool's slot:

```svelte
{#if session && isSessionTerminalMounted(session.key)}
  <!-- Portal target. The terminal itself lives in the app-level pool so it
       survives being promoted out of this tree and back. `visible` is the
       renderTab/renderPane argument both tab strips already pass: an inactive
       tab keeps this slot mounted, and only visibility says the terminal is on
       screen. -->
  <div
    class="session-terminal-slot"
    {@attach sessionSlotAttachment(sessionHostKey(workspaceId, workspaceHostKey, session.key), () => visible)}
  ></div>
{/if}
```

`mountedSessionKeys` (`:774`) stops owning DOM and becomes the pool's input.
Task 6's promoted-pane slot passes the same `visible` argument from
`DetailPaneLayout`'s `renderPane(tabKey, visible)`, so a promoted session behind
an inactive detail tab is inactive for the same reason.

- [ ] **Step 1** Write the failing browser test: switching between two session tabs keeps both terminal subtrees alive and moves neither.
- [ ] **Step 2** Run `../node_modules/.bin/vp test --project browser WorkspaceTerminalView.sessionSlots`. Expected FAIL.
- [ ] **Step 3** Implement, including the `DockedTerminalPanel` path so the bottom dock uses the same slots.
- [ ] **Step 4** Run `../node_modules/.bin/vp test --project browser WorkspaceTerminalView` and `--project unit WorkspaceTerminalView`. Expected PASS.
- [ ] **Step 5** Commit.

---

### Task 4: Session keys in the persisted layout

**Files:**

- Modify: `packages/ui/src/components/shared/tabbed-panel-layout.ts` (`normalizeTabbedPanelTree:185`)
- Modify: `packages/ui/src/stores/pane-surfaces.ts`, `packages/ui/src/stores/paneLayout.svelte.ts`
- Test: `packages/ui/src/components/shared/tabbed-panel-layout.test.ts`, `packages/ui/src/stores/paneLayout.svelte.test.ts`

`normalizeTabbedPanelTree(node, availableTabKeys, fallbackTabKey)` prunes unknown keys and reinserts missing known ones. Session keys need the opposite of both, so the signature gains a predicate:

```ts
export function normalizeTabbedPanelTree(
  node: TabbedPanelNode | null,
  availableTabKeys: readonly string[],
  fallbackTabKey = availableTabKeys[0] ?? "panel",
  /** Keys kept when stored but never inserted — dynamic panes the user placed. */
  keepIfStored: (tabKey: string) => boolean = () => false,
): TabbedPanelNode;
```

`PANE_SURFACES` grows `keepIfStored: (key) => key.startsWith("session:")`.

- [ ] **Step 1: Write the failing tests**

```ts
it("keeps a stored session pane and never reinserts one", () => {
  const stored = { type: "leaf", id: "l", tabs: ["conversation", "session:agent"], activeTabKey: "conversation" };
  const kept = normalizeTabbedPanelTree(stored, ["conversation", "files"], "conversation", isSessionPaneKey);
  expect(collectTabbedPanelTabKeys(kept)).toContain("session:agent");

  // Removing it must stick: reinsertion is for new static panes, and a session
  // pane exists only because the user promoted it.
  const without = { type: "leaf", id: "l", tabs: ["conversation"], activeTabKey: "conversation" };
  const still = normalizeTabbedPanelTree(without, ["conversation", "files"], "conversation", isSessionPaneKey);
  expect(collectTabbedPanelTabKeys(still)).not.toContain("session:agent");
});
```

plus a `paneLayout` round-trip test proving a promoted pane survives serialize/parse.

- [ ] **Step 2** Run `../node_modules/.bin/vp test --project unit tabbed-panel-layout paneLayout`. Expected FAIL.
- [ ] **Step 3** Implement.
- [ ] **Step 4** Same command. Expected PASS.
- [ ] **Step 5** Commit.

---

### Task 5: Promotion and demotion as tree edits

**Files:**

- Modify: `packages/ui/src/stores/paneLayout.svelte.ts`
- Modify: `frontend/src/lib/components/terminal/WorkflowSplitTree.svelte` (drag scope), `WorkspaceTerminalView.svelte` (drop handling)
- Test: `packages/ui/src/stores/paneLayout.svelte.test.ts`, `frontend/src/lib/components/terminal/WorkspaceTerminalView.test.ts`

Inside a detail surface the embedded tree passes `dragScope={surfaceScope}` instead of `workspaceTabDragScope(workspaceId)`; the standalone Workspaces tab keeps the workspace scope. Add a prop rather than branching on a global:

```ts
// WorkflowSplitTree props
dragScope?: string | undefined; // defaults to workspaceTabDragScope(workspaceId)
```

Sharing a scope is not enough to move a tab between trees: every mutation rejects a source the destination does not already contain, and the payload does not say where the tab came from. So the payload gains an origin and the drop becomes an atomic transfer.

```ts
// tabbed-panel-drag.ts
export interface TabbedPanelTabDragPayload {
  scope: string;
  tabKey: string;
  /** Which tree the tab is leaving, so the drop knows whether to transfer. */
  origin: "detail" | "workspace";
}

// paneLayout.svelte.ts
/**
 * Insert a tab this tree does not have, reporting whether it landed.
 *
 * The caller removes it from the source tree only on `true`, and both writes
 * happen in one update: a tab rendered by two trees at once means two portal
 * slots racing for one live terminal.
 */
acceptTransferredTab(tabKey: string, leafID: string, placement: TabbedPanelTransferPlacement): boolean;
```

- [ ] **Step 1** Failing tests: dropping a session on the detail tree adds the pane and removes the workflow tab in the same flush; dropping it back reverses both; a destination that rejects the drop leaves both trees byte-identical; a promoted pane dropped on the Workspaces tab's tree is rejected on scope; a session that disappears mid-drag cancels rather than half-applying.
- [ ] **Step 2** Run `../node_modules/.bin/vp test --project unit paneLayout WorkspaceTerminalView`. Expected FAIL.
- [ ] **Step 3** Implement.
- [ ] **Step 4** Same command. Expected PASS.
- [ ] **Step 5** Commit.

---

### Task 6: Detail surfaces render promoted session panes

**Files:**

- Modify: `packages/ui/src/views/PRListView.svelte`, `IssueListView.svelte`, `ActivityFeedView.svelte`
- Modify: `packages/ui/src/workspace-inline.ts` (the controller exposes the claimed workspace's sessions)
- Test: the three views' unit tests

`paneTabs` gains one entry per session of the claimed workspace that the stored tree already contains:

```ts
const sessionTabs = $derived<PaneTabSpec[]>(
  (workspaceClaim.sessions() ?? []).map((session) => ({
    key: sessionPaneKey(session.key),
    label: session.label,
    // Only panes the user promoted; availability never conjures one.
    available: paneLayout.hasTab(sessionPaneKey(session.key)),
    hideable: true,
  })),
);
```

- [ ] **Step 1** Failing tests: a promoted session pane renders its slot for the claimed workspace; selecting an item with a different workspace prunes it while the stored tree keeps it; returning restores it.
- [ ] **Step 2** Run `../node_modules/.bin/vp test --project unit PRListView IssueListView ActivityFeedView`. Expected FAIL.
- [ ] **Step 3** Implement.
- [ ] **Step 4** Same command. Expected PASS.
- [ ] **Step 5** Commit.

---

### Task 7: One bar — the pane controls popover

**Files:**

- Create: `frontend/src/lib/components/terminal/WorkspacePaneControls.svelte`
- Modify: `packages/ui/src/components/shared/DetailPaneLayout.svelte` (`leafActions:267`)
- Modify: `packages/ui/src/views/{PRListView,IssueListView,ActivityFeedView}.svelte`
- Modify: `WorkspaceTerminalView.svelte` (delete `.workspace-toolbar`, `:3125`)
- Test: `frontend/src/lib/components/terminal/WorkspacePaneControls.test.ts`, `packages/ui/src/components/shared/DetailPaneLayout.test.ts`

One `aria-label="Workspace controls"` button in the tab strip's action area, opening a popover holding `WorkflowPresetMenu`, `TerminalZoomControl`, `TerminalOptionsMenu`, and a launch action. Rendered for the container leaf and for any promoted session leaf.

**The extension point.** `TabbedPanelTree` takes a `leafActions` snippet, but
`DetailPaneLayout` already occupies it with its own split/zoom/close controls and
forwards nothing from the caller — so a promoted session pane has nowhere to put
this button. Give `DetailPaneLayout` one prop and render it inside the same
actions area, before the structural controls:

```ts
// DetailPaneLayout props
/** Caller chrome for a leaf, rendered left of the structural controls. */
paneLeafExtras?: Snippet<[TabbedPanelLeaf]> | undefined;
```

```svelte
{#snippet leafActions(leaf: TabbedPanelLeaf)}
  {@render paneLeafExtras?.(leaf)}
  <!-- existing split / zoom / close buttons -->
{/snippet}
```

It follows the structural controls' own rule: `leafActions` is already suppressed
while flattened (`:223`), so the button disappears with them and nothing extra is
needed for the flattened case. The three views pass a snippet that renders
`WorkspacePaneControls` when the leaf holds the workspace container or one of that
workspace's session panes, and nothing otherwise.

- [ ] **Step 1** Failing tests: `DetailPaneLayout` renders `paneLeafExtras` inside the leaf action area and omits it while flattened; the popover exposes all four groups; it closes on Escape and on outside click; the launch action opens the launcher; a leaf holding neither the container nor a session pane renders no button.
- [ ] **Step 2** Run `../node_modules/.bin/vp test --project unit WorkspacePaneControls DetailPaneLayout`. Expected FAIL.
- [ ] **Step 3** Implement and delete the toolbar.
- [ ] **Step 4** Same command plus `--project unit WorkspaceTerminalView PRListView`. Expected PASS.
- [ ] **Step 5** Commit.

---

### Task 8: The launcher overlay replaces the Home tab

**Files:**

- Create: `frontend/src/lib/components/terminal/WorkspaceLauncherOverlay.svelte`
- Modify: `WorkspaceTerminalView.svelte` (drop `home` from `workflowTabDescriptors:511`, and every `selectWorkspaceTab("home")`)
- Modify: `frontend/src/lib/stores/keyboard/actions.ts` (palette command)
- Test: `frontend/src/lib/components/terminal/WorkspaceLauncherOverlay.test.ts`, `WorkspaceTerminalView.test.ts`

The overlay wraps today's `WorkspaceHome` body, pushes a modal frame, auto-opens when the workspace has no sessions, and closes when one launches.

- [ ] **Step 1** Failing tests: no `Home` tab exists; the overlay auto-opens for a session-less workspace and not otherwise; launching closes it; the palette command and the controls popover both reopen it; `Focus Terminal` opens it when there is nothing to focus.
- [ ] **Step 2** Run `../node_modules/.bin/vp test --project unit WorkspaceLauncherOverlay WorkspaceTerminalView`. Expected FAIL.
- [ ] **Step 3** Implement. Every current `selectWorkspaceTab("home")` becomes either "open the launcher" or "select the first session", decided per call site — the post-delete and post-close ones want the launcher; the initial-load one wants the remembered session.
- [ ] **Step 4** Same command. Expected PASS.
- [ ] **Step 5** Commit.

---

### Task 9: Dock mode, Focus Terminal, and stale entries

**Files:**

- Modify: `frontend/src/lib/stores/workspace-host.svelte.ts`
- Test: `frontend/src/lib/stores/workspace-host.test.ts`

"The workspace pane" is no longer one leaf:

- `collapsed` — the container and every promoted pane of that workspace are hidden; expanding restores exactly the set that was hidden rather than the default arrangement.
- `expanded` — one of them holds the zoom; expanding zooms the pane holding the workspace's last-focused session, or the container when none is promoted.
- Focus Terminal focuses the pane holding the last-focused session when it is on screen, reveals it when it is not, and opens the launcher when the workspace has no session at all.
- Last-focused session is tracked per workspace and survives promotion, demotion, and selection changes.
- On session end or workspace deletion, drop that session's pane from every surface's stored tree.
- Dispose registry entries for a workspace that no surface claims and the Workspaces tab is not showing: parked terminals hold live websockets, so browsing past ten items must not leave ten connections open.

- [ ] **Step 1** Failing tests, one per bullet, including: deleting a workspace with two promoted panes leaves no session keys in any surface's stored tree; collapsing and expanding a workspace with one promoted pane restores that pane rather than the default tree; selecting three items in turn leaves only the current workspace's terminals in the registry.
- [ ] **Step 2** Run `../node_modules/.bin/vp test --project unit workspace-host`. Expected FAIL.
- [ ] **Step 3** Implement.
- [ ] **Step 4** Same command. Expected PASS.
- [ ] **Step 5** Commit.

---

### Task 10: End-to-end coverage and context

**Files:**

- Modify: `frontend/tests/e2e-full/00-inline-workspace-continuity.spec.ts`
- Create: `frontend/tests/e2e-full/00-workspace-launcher.spec.ts`
- Modify: `context/ui-interaction-contracts.md`, `context/ui-design-system.md`
- Modify: `docs/superpowers/plans/2026-07-26-session-panes.md` (check the boxes)

The real-backend spec is the only place that proves liveness against a real tmux session. Add: promote a session to a top-level pane and confirm the marker text survives; demote it and confirm the same; change the selected item and come back.

**The launcher needs the real backend too.** Deleting the `Home` tab makes the
overlay the only route to a first session, and the mock lane cannot prove a
launch produces one: today's full-stack launch coverage is
`00-workspace-sidebar.spec.ts:135`, which only asserts that the example surface's
buttons are disabled. Add a spec covering the round trip a maintainer actually
takes — a workspace with no sessions auto-opens the overlay, launching a shell
from it closes the overlay and leaves a live terminal, and the controls popover
reopens the overlay from inside that shell. Without it the mode has a reachable
dead end: no tab, and an overlay whose only opener is untested.

Context edits, terse per `context-guide.md`:

- the portal singleton becomes "one live subtree per session key, one mounted slot per key";
- session pane keys are kept-if-stored, never reinserted;
- the launcher is an overlay, and `Home` is not a tab.

- [ ] **Step 1** Update the continuity spec's selectors, add its three cases, and write `00-workspace-launcher.spec.ts`.
- [ ] **Step 2** Run `../node_modules/.bin/vp exec playwright test --config playwright-e2e.config.ts --project=chromium tests/e2e-full/00-inline-workspace-continuity.spec.ts tests/e2e-full/00-workspace-launcher.spec.ts`. Expected PASS.
- [ ] **Step 3** Full gate: `../node_modules/.bin/vp test`, both Playwright configs whole, `svelte-check`, `make lint`.
- [ ] **Step 4** Capture a screenshot of a promoted session pane with the `capture-playwright` skill for the PR body.
- [ ] **Step 5** Commit.

---

## Risks

- **Two portals, one DOM.** A session's terminal is inside the pool, but the container's whole `WorkspaceTerminalView` is itself reparented by `WorkspaceHost`. Task 2's sibling placement is what keeps a promoted terminal from being dragged into the parking node with its container; get that wrong and promoting a session while the container is parked blanks the terminal.
- **Double slots.** If both `WorkspaceTerminalView`'s tab panel and a promoted pane render a slot for one session, registration order silently decides the winner. The invariant is enforced by promotion removing the workflow tab; Task 5's tests must cover the transitional flush.
- **Stored-tree growth.** Session keys accumulate per workspace. Task 9's deletion path is the only thing that bounds it.
- **Connection growth.** Every parked terminal holds a websocket, so Task 9's claim-change disposal is what stops browsing a list from opening one connection per workspace visited.
