# Terminal Sessions As Detail Panes Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Let a workspace's terminal sessions be promoted out of the inline workspace pane into top-level detail panes, and collapse the three bars above a terminal into one.

**Architecture:** A second portal layer, keyed by session, mirrors the existing whole-workspace one: each mounted session's `TerminalPane` is rendered once into an app-level pool and reparented into whichever slot renders it — the workspace container's tab panel or a promoted `session:<key>` pane in the surface's `PaneLayoutStore`. Promotion is an ordinary tab drag whose effect depends on which tree accepted the drop.

**Tech Stack:** Svelte 5 runes, `@middleman/ui` shared components, Vitest (unit + browser projects), Playwright (mock and real-backend).

## Global Constraints

- Design source of truth: `docs/superpowers/specs/2026-07-26-session-panes-design.md`. Its predecessor `2026-07-25-generalized-pane-layout-design.md` still governs the detail pane tree.
- Session pane keys are `session:` followed by `encodeURIComponent(workspaceId)`, `encodeURIComponent(hostKey ?? "")`, and `encodeURIComponent(sessionKey)` joined with `/`, matching `sessionHostKey`'s encoding minus the generation. **Percent-encoding is not decorative:** session keys are opaque and routinely contain colons (`ws-1:helper`), so a plain `session:<workspaceId>:<hostKey>:<sessionKey>` cannot be parsed back. The empty local host encodes as the empty segment. The parser rejects anything with the wrong segment count or an undecodable segment. `WorkspaceTerminalView`'s existing `workflowTabKeyForSession` (`:841`) mints the workspace-local `session:<sessionKey>` for its own tree; provide one builder and one parser for the detail-tree form and use them on both sides.
- Registry keys additionally carry the session's `created_at` generation; layout keys deliberately do not. A relaunched session reappears where the user put it, without inheriting the dead generation's subtree.
- Optimize for one to three sessions per item. Do not add pooling, eviction, or virtualization for more.
- Exactly one slot may be mounted per session key at a time, exactly as the existing host allows one slot per surface.
- A slot registers its **visibility**, not just its element. An inactive tab panel stays mounted with `visibility: hidden` (`TabbedPanelTree`), so element presence does not mean the terminal is on screen. `TerminalPane.active` for a pooled session is derived from the registered visibility of its slot and from nothing else; a terminal left `active` behind a hidden tab steals focus and fights the visible one for keystrokes.
- Every `@lucide/svelte/icons/<name>` import added anywhere must also be added to `optimizeDeps.include` in `frontend/vite.config.ts`.
- **`packages/ui` cannot import from `frontend/`.** The registry, the pool, and every terminal component live under `frontend/`, while the three detail views live in `packages/ui`. Anything a view needs from the terminal side crosses through `InlineWorkspaceController` (declared in `packages/ui/src/workspace-inline.ts`, implemented in `frontend/src/lib/stores/workspace-host.svelte.ts`) — the same seam the existing workspace pane already uses for its slot attachment. Tasks 6 and 7 extend that interface rather than reaching across the boundary.
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
- Create: `frontend/src/lib/components/terminal/PooledSessionTerminal.svelte`
- Modify: `frontend/src/lib/components/terminal/WorkspaceHost.svelte`
- Test: `frontend/src/lib/components/terminal/SessionTerminalPool.browser.svelte.ts`

**Interfaces:**

- Consumes: Task 1's registry; `TerminalPane` (`websocketPath`, `active`, `disabled`, `onExit`, `initialStatus`).
- Produces: `SessionTerminalPool`, which takes **no props**. It renders
  `mountedSessions()` directly, because the sessions it must keep live are not
  the ones any single view knows about — a promoted session belongs to no view's
  tab strip. Per session it renders `PooledSessionTerminal`, whose `active` comes
  from `isSessionSlotVisible(hostKey)`: only the slot knows whether its tab panel
  is the visible one.
- Exits route back through the registry (`noteSessionExited` / `onSessionExited`)
  rather than a callback on the descriptor. The pool has no access to runtime
  session records, and a callback captured in a descriptor goes stale the moment
  the session's status changes.

The pool is a **sibling** of `.workspace-host-wrapper`, not a child. A promoted session must survive the container being parked, and the wrapper is exactly what gets parked.

Reparenting copies the placement effect from `WorkspaceHost.svelte:82`: park, `await tick()`, append to destination, reveal on non-zero geometry via `requestAnimationFrame`.

Each session's wrapper lives in its own child component so the placement effect
is per instance rather than a map of effects. Two consequences:

- The child's teardown calls `wrapper.remove()`. Svelte cannot remove a node the
  component reparented out of its own fragment, so an unmounted session would
  otherwise leave a dead terminal in whatever slot last held it.
- `mountedSessions()` must stay append-only. A keyed `{#each}` inserts a new item
  before the next item's first node, and those nodes have been moved into
  slots — appending keeps the anchor the block's own trailing one.

- [x] **Step 1: Write the failing browser test**

`SessionTerminalPool.browser.svelte.ts`, five cases: one subtree moved between
slots and still the same node; two sessions live at once; a session parked when
its slot unregisters; a mounted-but-hidden slot leaving its terminal inert; and
an unmounted session's wrapper removed from wherever it was parented. Sessions
are mounted with `status: "exited"` so `TerminalPane` skips the WebSocket connect
against a backend this tier does not run.

- [x] **Step 2: Run and watch it fail**

Run: `cd frontend && ../node_modules/.bin/vp test --project browser SessionTerminalPool`
Expected: FAIL, component missing.

- [x] **Step 3: Implement the pool**

- [x] **Step 4: Mount it from `WorkspaceHost.svelte`**

As a sibling of the wrapper. Not fed from the claimed workspace: the pool reads
the registry, which is what lets a promoted session outlive its container.

- [x] **Step 5: Run the browser tests**

Run: `cd frontend && ../node_modules/.bin/vp test --project browser SessionTerminalPool WorkspaceHost InlineWorkspacePane`
Expected: PASS. (15 tests.)

- [x] **Step 6: Commit.**

---

### Task 3: WorkspaceTerminalView renders slots, not terminals

**Files:**

- Create: `frontend/src/lib/components/terminal/SessionTerminalSlot.svelte`
- Create: `frontend/src/lib/components/terminal/WorkspaceTerminalViewTestHarness.svelte`
- Modify: `frontend/src/lib/components/terminal/WorkspaceTerminalView.svelte` (the session branch of `renderTab`)
- Modify: `frontend/src/lib/components/terminal/WorkspaceTerminalView.test.ts` (mount through the harness)

The inline `TerminalPane` becomes a slot. `SessionTerminalSlot` owns both halves
of registration — the element via an attachment, the visibility via its own
effect scoped to that element — so no call site can register one without the
other:

```svelte
{#if session && isSessionTerminalMounted(session.key)}
  <SessionTerminalSlot hostKey={sessionHostKeyFor(session)} visible={active && hostVisible} />
{/if}
```

`visible` is `renderTab`'s own argument ANDed with `hostVisible`, so a session
behind an inactive tab, or inside a parked workspace host, is inert. Task 6's
promoted pane passes `DetailPaneLayout`'s `renderPane(tabKey, visible)` for the
same reason.

Two things the view keeps owning:

- **The registry mirror is reconciled from state, not pushed per call site.** A
  session moved into the terminal dock stays in `mountedSessionKeys` while the
  dock renders its own pane, so a missed `noteSessionUnmounted` would leave two
  sockets on one tmux session. One effect derives the desired set (mounted AND
  workflow-region) and syncs only this workspace's prefix, leaving other
  surfaces' parked terminals alone.
- **Exit routing.** The pool reports an exit by registry key; only this view can
  map that back to a `RuntimeSession`, and the generation in the key keeps a
  relaunched session from being taken for the dead one.

**The terminal dock is out of scope.** Terminal-region sessions are rendered by
`TerminalSplitTree` with its own split machinery and are not promotable, so
pooling them would be a large change for no part of this feature. The reconcile
filter above is what keeps the two paths from both attaching.

- [x] **Step 1** Write the failing browser test — covered by Task 2's
      `SessionTerminalPool.browser.svelte.ts`, which exercises the slot contract
      the view now implements.
- [x] **Step 2** Implement the slot component and the view changes.
- [x] **Step 3** Point `WorkspaceTerminalView.test.ts` at a harness that mounts
      the pool alongside the view. Terminals no longer live in the view's own
      subtree, so mounting it alone yields slots and no terminal — five tests
      caught exactly that.
- [x] **Step 4** Run `../node_modules/.bin/vp test --project unit WorkspaceTerminalView session-host` and `--project browser SessionTerminalPool WorkspaceHost`. Expected PASS. (65 + 11 + 15.)
- [x] **Step 5** Commit.

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

`PANE_SURFACES` grows `keepIfStored: isSessionPaneKey`, where `isSessionPaneKey`
is the strict parser from the Global Constraints — a prefix check would accept a
malformed key from an older build and keep it in the tree forever.

Add the builder and parser beside it:

```ts
/** `session:` + encoded workspace / host / session, joined with `/`. */
export function sessionPaneKey(workspaceId: string, hostKey: string | undefined, sessionKey: string): string;
/** Null unless every segment decodes and the count is exactly three. */
export function parseSessionPaneKey(
  key: string,
): { workspaceId: string; hostKey: string | undefined; sessionKey: string } | null;
export function isSessionPaneKey(key: string): boolean;
```

- [ ] **Step 1: Write the failing tests**

```ts
const agentPane = sessionPaneKey("ws-1", undefined, "ws-1:helper");

it("round-trips a session key that contains the separator characters", () => {
  // Session keys are opaque and routinely contain colons, and workspace ids
  // could contain slashes: two different sessions must never spell one key.
  expect(parseSessionPaneKey(agentPane)).toEqual({
    workspaceId: "ws-1",
    hostKey: undefined,
    sessionKey: "ws-1:helper",
  });
  expect(sessionPaneKey("a/b", undefined, "s")).not.toBe(sessionPaneKey("a", "b", "s"));
  expect(parseSessionPaneKey("session:only-two/parts")).toBeNull();
  expect(parseSessionPaneKey("session:ws-1//%zz")).toBeNull();
});

it("keeps a stored session pane and never reinserts one", () => {
  const stored = { type: "leaf", id: "l", tabs: ["conversation", agentPane], activeTabKey: "conversation" };
  const kept = normalizeTabbedPanelTree(stored, ["conversation", "files"], "conversation", isSessionPaneKey);
  expect(collectTabbedPanelTabKeys(kept)).toContain(agentPane);

  // Removing it must stick: reinsertion is for new static panes, and a session
  // pane exists only because the user promoted it.
  const without = { type: "leaf", id: "l", tabs: ["conversation"], activeTabKey: "conversation" };
  const still = normalizeTabbedPanelTree(without, ["conversation", "files"], "conversation", isSessionPaneKey);
  expect(collectTabbedPanelTabKeys(still)).not.toContain(agentPane);
});

it("prunes a malformed session key rather than keeping it forever", () => {
  const stored = { type: "leaf", id: "l", tabs: ["conversation", "session:bogus"], activeTabKey: "conversation" };
  const kept = normalizeTabbedPanelTree(stored, ["conversation"], "conversation", isSessionPaneKey);
  expect(collectTabbedPanelTabKeys(kept)).not.toContain("session:bogus");
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
 * Compute — do not commit — the tree this drop would produce.
 *
 * Returns null when the drop is refused. A `commit` that mutated the
 * destination and left the caller to remove the source is not atomic: nothing
 * rolls the destination back if the source's removal is the half that fails,
 * and Svelte's batching hides the intermediate paint without preventing the
 * inconsistent state.
 */
planTransferIn(tabKey: string, leafID: string, placement: TabbedPanelTransferPlacement): TabbedPanelNode | null;
/** The same for the source side: the tree with the tab gone, or null if it has none. */
planTransferOut(tabKey: string): TabbedPanelNode | null;
/** Replace this tree wholesale. Only ever called by the coordinator below. */
commitTree(tree: TabbedPanelNode): void;
```

Both plans are computed first; only if **neither** is null does the coordinator
call `commitTree` on each. Either half returning null aborts with no write at
all, so a refused or impossible transfer cannot leave the tab in both trees or
in neither.

```ts
/** Nothing is written until both sides have produced a tree. */
export function transferTab(args: {
  from: { planTransferOut(tabKey: string): TabbedPanelNode | null; commitTree(tree: TabbedPanelNode): void };
  to: { planTransferIn(...): TabbedPanelNode | null; commitTree(tree: TabbedPanelNode): void };
  tabKey: string;
  leafID: string;
  placement: TabbedPanelTransferPlacement;
}): boolean;
```

- [ ] **Step 1** Failing tests: dropping a session on the detail tree adds the pane and removes the workflow tab in the same flush; dropping it back reverses both; a destination that refuses the drop leaves both trees byte-identical; a **source** that cannot produce a removal leaves the destination byte-identical too; a promoted pane dropped on the Workspaces tab's tree is rejected on scope; a session that disappears mid-drag cancels rather than half-applying.
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

**The boundary.** A view in `packages/ui` cannot import the registry, the slot
component, or anything else under `frontend/`. `InlineWorkspaceController` is the
existing seam — it already hands the workspace pane its `slotAttachment` — so it
grows the session equivalents, keeping the terminal side entirely in `frontend/`:

```ts
// packages/ui/src/workspace-inline.ts
export interface PromotableSession {
  /** The layout key: `session:` + encoded workspace / host / session. */
  paneKey: string;
  label: string;
}

export interface InlineWorkspaceController {
  // ...existing members
  /** Sessions of the currently claimed workspace, or [] when nothing is claimed. */
  promotableSessions(): readonly PromotableSession[];
  /** Portal attachment for one promoted session's pane body. */
  sessionSlotAttachment(paneKey: string): Attachment<HTMLElement>;
  /** Publish whether that pane is on screen; the pool gates `active` on it. */
  noteSessionPaneVisible(paneKey: string, visible: boolean): void;
}
```

`paneTabs` then gains one entry per session the stored tree already contains:

```ts
const sessionTabs = $derived<PaneTabSpec[]>(
  (inlineWorkspace?.promotableSessions() ?? []).map((session) => ({
    key: session.paneKey,
    label: session.label,
    // Only panes the user promoted; availability never conjures one.
    available: paneLayout.hasTab(session.paneKey),
    hideable: true,
  })),
);
```

- [ ] **Step 1** Failing tests: a promoted session pane renders its slot for the claimed workspace; selecting an item with a different workspace prunes it while the stored tree keeps it; returning restores it; the pane reports its visibility so a session tabbed behind a sibling goes inert.
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

`WorkspacePaneControls` lives under `frontend/`, so the three views cannot render
it directly. Each view receives it as a snippet prop supplied by the frontend App
shell — the same direction as the controller: `packages/ui` declares the hole,
`frontend/` fills it.

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
- Modify: `packages/ui/src/stores/paneLayout.svelte.ts` (`noteFocused` accepts validated dynamic keys)
- Modify: `packages/ui/src/views/{PRListView,IssueListView,ActivityFeedView}.svelte` (report a focused session pane back through the controller)
- Test: `frontend/src/lib/stores/workspace-host.test.ts`, `packages/ui/src/stores/paneLayout.svelte.test.ts`, the three views' tests

**Two wiring gaps this task has to close, not assume.**

- `noteFocused` drops any key outside its static `knownTabs`, so a promoted
  session pane could never become last-focused and every rule below would key off
  a stale value. It must accept a key that parses as a session pane for this
  surface, on the same "keep if stored, never reinsert" footing as the tree.
- Focus originates in `DetailPaneLayout`, which reports it to the view via
  `onFocusPane`. Last-focused **session** is per workspace, and only the frontend
  host knows which workspace a claim belongs to, so the views forward a focused
  session pane through `InlineWorkspaceController` rather than tracking it
  themselves.

"The workspace pane" is no longer one leaf:

- `collapsed` — the container and every promoted pane of that workspace are hidden; expanding restores exactly the set that was hidden rather than the default arrangement.
- `expanded` — one of them holds the zoom; expanding zooms the pane holding the workspace's last-focused session, or the container when none is promoted.
- Focus Terminal focuses the pane holding the last-focused session when it is on screen, reveals it when it is not, and opens the launcher when the workspace has no session at all.
- Last-focused session is tracked per workspace and survives promotion, demotion, and selection changes.
- On session end or workspace deletion, drop that session's pane from every surface's stored tree.
- Dispose registry entries for a workspace that no surface claims and the Workspaces tab is not showing: parked terminals hold live websockets, so browsing past ten items must not leave ten connections open.

- [ ] **Step 1** Failing tests, one per bullet, including: deleting a workspace with two promoted panes leaves no session keys in any surface's stored tree; collapsing and expanding a workspace with one promoted pane restores that pane rather than the default tree; selecting three items in turn leaves only the current workspace's terminals in the registry; `noteFocused` accepts a well-formed session pane key and still rejects a malformed one; focusing a promoted pane and focusing the container both update the workspace's last-focused session.
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
