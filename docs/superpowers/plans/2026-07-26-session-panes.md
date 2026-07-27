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

| File                                                                                | Responsibility                                                                                                      |
| ----------------------------------------------------------------------------------- | ------------------------------------------------------------------------------------------------------------------- |
| `frontend/src/lib/stores/session-host.svelte.ts` (create)                           | Registry of live session terminals: parking, per-key slot registration, slot attachment.                            |
| `frontend/src/lib/components/terminal/SessionTerminalPool.svelte` (create)          | Renders one `TerminalPane` per mounted session into the pool; owns the parking node.                                |
| `frontend/src/lib/components/terminal/WorkspaceHost.svelte` (modify)                | Mounts the pool as a sibling of the reparented wrapper so promoted terminals outlive the container.                 |
| `frontend/src/lib/components/terminal/WorkspaceTerminalView.svelte` (modify)        | Renders session slots instead of `TerminalPane`; hides the `Workflow` toolbar and `Home` tab in embedded mode only. |
| `frontend/src/lib/components/terminal/WorkspacePaneControls.svelte` (create)        | The single top-right button and its popover (presets, zoom, options, launch).                                       |
| `frontend/src/lib/components/terminal/WorkspaceLauncherOverlay.svelte` (create)     | The launcher, previously the `Home` tab body.                                                                       |
| `packages/ui/src/stores/pane-surfaces.ts` (modify)                                  | Session keys accepted outside the static vocabulary and never reinserted.                                           |
| `packages/ui/src/components/shared/tabbed-panel-layout.ts` (modify)                 | `normalizeTabbedPanelTree` gains a "keep but never reinsert" class of key.                                          |
| `packages/ui/src/stores/paneLayout.svelte.ts` (modify)                              | Promotion/demotion edits; dropping stale session entries.                                                           |
| `packages/ui/src/views/{PRListView,IssueListView,ActivityFeedView}.svelte` (modify) | Promoted session panes in `paneTabs`, availability from the claimed workspace's sessions.                           |
| `frontend/src/lib/stores/workspace-host.svelte.ts` (modify)                         | Dock mode, Focus Terminal, and visibility restated over the container plus promoted panes.                          |

---

### Task 1: Session host registry

**Files:**

- Create: `frontend/src/lib/stores/session-host.svelte.ts`
- Test: `frontend/src/lib/stores/session-host.test.ts`

**Interfaces:**

- Consumes: nothing. Deliberately standalone, like `workspace-host.svelte.ts`.
- Produces:
  - `type SessionHostKey = string` built by `sessionHostKey(workspaceId: string, hostKey: string | undefined, sessionKey: string, generation: string): SessionHostKey`, where `generation` is the session's `created_at`
  - `registerSessionSlot(key: SessionHostKey, el: HTMLElement | null): void` — the registering element becomes the key's owner
  - `releaseSessionSlot(key: SessionHostKey, el: HTMLElement): void` — a no-op unless `el` still owns the key
  - `setSessionSlotVisible(key: SessionHostKey, el: HTMLElement, visible: boolean): void` — owner-scoped for the same reason
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

Reparenting copies the placement effect from `WorkspaceHost.svelte:82` — park, `await tick()`, append to destination — but **not** its poll for non-zero geometry. The host has to poll: it moves a subtree into slots it knows nothing about, including a `display: none` parking node, and nothing else tells it when the destination is real. Here the slot itself reports whether it is on screen, so polling would only add a failure mode where a destination that legitimately measures zero keeps the terminal inert forever.

The risk that gate covered is real and not gone: activating a terminal at zero height makes the fit addon resize the tmux pane to one row. Two things bound it — the container's slot is inside the workspace host, which is still geometry-gated, and a promoted pane's slot only reports visible once its leaf renders. If a zero-height activation is ever observed, the fix is to make measurable geometry part of the slot's visibility contract rather than to reinstate a poll in the pool.

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
  session changes region without either container calling anything, so a pushed
  mount/unmount would drift; a missed `noteSessionUnmounted` leaves a socket
  attached to nothing. One effect derives the desired set — mounted workflow
  sessions plus the open dock's leaves — and syncs only this workspace's prefix,
  leaving other surfaces' parked terminals alone.
- **Exit routing.** The pool reports an exit by registry key; only this view can
  map that back to a `RuntimeSession`, and the generation in the key keeps a
  relaunched session from being taken for the dead one.

**The terminal dock is pooled too.** Terminal tabs are promotable, so
`TerminalSplitTree` renders `SessionTerminalSlot` instead of its own
`TerminalPane` and the reconcile effect covers both regions. Its desired set is
the dock's tree leaves while the panel is open — a terminal-region session with
no leaf, or a closed panel, has no terminal today and must not gain one just
because the pool could park it. Two things follow:

- `onExit` disappears from the dock's prop chain. Exits arrive by registry key
  through `onSessionExited` like every other session's, and leaving the old prop
  wired would run `handleSessionExit` twice.
- Moving a shell between the dock and the workflow area now reparents one pooled
  terminal instead of destroying one and attaching another, so it keeps its tmux
  session and its scrollback. That is the behavior the region filter used to make
  impossible, and it is what makes the dock promotable in Task 5 for free.

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
- [x] **Step 6** Pool the dock. Failing unit test first: a shell moved from the
      dock to the workflow area and back opens exactly one socket for that
      session and never closes it. A second test guards the hazard pooling
      introduces — a closed panel must hand its terminal back, not park it with
      the socket open. Then extend the continuity e2e: the dock leaf's
      `data-session-host` must reappear inside the workflow slot, which proves
      the pool's own source-to-destination transfer full-stack. That was owed to
      Task 10 for want of two real slots; the dock provides the second one.

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

Reject **semantically** empty parts too, not just undecodable ones: an empty
workspace id or session key names nothing, and only the host segment is allowed
to be empty (that is how the provider default host is spelled).

Task 6 asks the store whether a session pane is already in the tree, so add it
here rather than inventing it there:

```ts
// PaneLayoutStore
/** Whether the STORED tree contains this tab, regardless of availability. */
hasTab(tabKey: string): boolean;
```

- [x] **Step 1: Write the failing tests**

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

- [x] **Step 2** Run `../node_modules/.bin/vp test --project unit tabbed-panel-layout paneLayout session-pane-key`. Expected FAIL.
- [x] **Step 3** Implement, in `packages/ui/src/stores/session-pane-key.ts` (builder, strict parser, workspace matcher) plus the `keepIfStored` predicate threaded through `normalizeTabbedPanelTree`, `parseTabbedPanelLayout`, `PANE_SURFACES`, and `createPaneLayoutStore`. `noteFocused` accepts a validated dynamic key for the same reason.
- [x] **Step 4** Same command. Expected PASS. (24 + 41 + 5.)
- [x] **Step 5** Commit.

---

### Task 5: Promotion and demotion as tree edits

**Files:**

- Modify: `packages/ui/src/stores/paneLayout.svelte.ts` (insert a tab the tree has never held)
- Modify: `packages/ui/src/components/shared/tabbed-panel-layout.ts` (insert/remove primitives)
- Modify: `frontend/src/lib/components/terminal/WorkflowSplitTree.svelte` (drag scope), `DockedTerminalPanel.svelte` / `TerminalSplitTree.svelte` (the dock's own promote control), `WorkspaceTerminalView.svelte` (masking and drop handling)
- Test: `packages/ui/src/stores/paneLayout.svelte.test.ts`, `frontend/src/lib/components/terminal/WorkspaceTerminalView.test.ts`

Inside a detail surface the embedded tree passes `dragScope={surfaceScope}` instead of `workspaceTabDragScope(workspaceId)`; the standalone Workspaces tab keeps the workspace scope. Add a prop rather than branching on a global:

```ts
// WorkflowSplitTree props
dragScope?: string | undefined; // defaults to workspaceTabDragScope(workspaceId)
```

Sharing a scope is not enough: every mutation rejects a source the destination does not already contain. So the layout module gains a primitive that inserts a tab the tree has never held, and the store gains the two writes above it.

```ts
// tabbed-panel-layout.ts
export type TabbedPanelInsertTarget =
  | { kind: "tab"; leafID: string }
  | { kind: "split"; leafID: string; direction: TabbedPanelDirection; placement: "before" | "after" };
/** Hands back the same node when the tab is already held or the leaf is unknown. */
export function insertTabbedPanelTab(node, tabKey, target): TabbedPanelNode | null;
export function removeTabbedPanelTab(node, tabKey): TabbedPanelNode | null;
```

The payload needs no `origin` field: "is this key already in my tree" is the same
question, and both trees can answer it themselves. What the payload does need is a
canonical key — a workspace tab is keyed by session key alone, unique only within
a workspace — so the shared payload carries the full
`session:<workspace>/<host>/<session>` form and the workspace tree translates back
on read, rejecting another workspace's key or a non-session pane.

The workspace tree has its own drag module with its own MIME types, and
`WorkflowSplitTree` overrides the shared read/write hooks, so the two payload
stores are independent today. A session tab dragged while the tree is embedded in a
surface writes BOTH: its own payload for intra-workspace drops, and the shared one
for the detail tree. An intra-workflow drop reads its own first, so there is no
ambiguity.

**One authoritative write.** The surface's stored pane tree is the only record of
a promotion: the pane key is in it, or the session is home. Nothing writes
`sessionRegions`, nothing remembers a source container, and there is no second
tree to keep in step — so there is no two-phase commit and no way to end up with
the tab in both trees or neither. Validate the claim and the session immediately
before the write; a refused drop writes nothing at all.

```ts
// PaneLayoutStore
/** Insert a pane the tree has never held (a promotion). Rejects a key the
 *  surface would prune, so a malformed session key cannot be written in. */
promoteTab(tabKey: string, leafID: string, placement: TabbedPanelTransferPlacement): boolean;
/** Remove a promoted pane (a demotion). No-op when the tree has no such tab. */
demoteTab(tabKey: string): void;
```

**The containers mask, they do not prune.** Both region trees keep the promoted
session's stored entry; what the workspace renders is derived with promoted
entries pruned. Pruning the stored trees would return a demoted session to the
right region and lose its tab order, split, group, and active position — the
placement the user expects back.

The view needs to know which surface it is embedded in to ask. `WorkspaceHost`
already derives `inlineDock` from its `slot` (`prs` | `issues` | `activity` |
`tab` | null), and the first three are exactly `PaneSurfaceKey`, so it passes a
`paneSurface` prop the same way and the view reads that surface's store:

```ts
// WorkspaceTerminalView
const surfaceLayout = $derived(paneSurface ? getPaneLayoutStore(paneSurface) : null);
const promotedSessionKeys = $derived(
  new Set(
    runtimeSessions
      .filter((session) => surfaceLayout?.hasTab(sessionPaneKeyFor(session)) ?? false)
      .map((session) => session.key),
  ),
);
```

`paneSurface` is unset on the standalone Workspaces tab and on the embed routes,
which have no detail panes: a session promoted in the PRs surface is still at
home there, which is correct rather than a gap. The workflow tab descriptors and `terminalSessions`
both subtract `promotedSessionKeys`, the dock's rendered tree prunes leaves whose
session is promoted, and the pool's desired set treats a promoted session as on
screen — the detail pane's slot is what renders it.

Drag cannot be the only way in, but the keyboard path cannot land here: a palette
command has to name a session, and the command layer can only see stores — the
sessions live in the view's local runtime state until Task 6 puts
`promotableSessions()` on the controller. So the promote/demote commands and the
dock's per-session promote control move to Tasks 6 and 7, where the list and the
pane chrome exist. Adding more entry points before Task 6 renders a promoted pane
would only widen a gesture that currently leads nowhere.

Landing in three parts, each with its own tests and commit, because the store, the
masking, and the drag wiring fail independently.

- [x] **Step 1 (store)** Failing tests first: promoting adds and activates a pane and persists it; promoting as a split clears a zoom the new leaf could hide behind; a key the surface would prune, a static pane, an unknown leaf, and a duplicate are all refused with no write; demoting drops the pane and every zoom or hidden entry naming it; demoting a pane the tree does not hold writes nothing. Plus the layout primitives: insert into a leaf activates it, insert as a split mints a leaf beside the target, and both hand back the same tree when they cannot apply.
- [x] **Step 2 (store)** Run `../node_modules/.bin/vp test --project unit paneLayout tabbed-panel-layout`. Expected FAIL, then PASS after implementing `insertTabbedPanelTab`, `removeTabbedPanelTab`, `promoteTab`, `demoteTab`. Commit.
- [ ] **Step 3 (masking)** Failing tests: with a session's pane key in the surface's tree, the workspace container drops it from its workflow strip and its dock leaf while the STORED trees keep it; the pool still mounts it, because the detail pane is what renders it; clearing the pane key puts it back exactly where it was. `WorkspaceHost` passes `paneSurface`, and the standalone tab passes none, so nothing is masked there.
- [x] **Step 4 (drag)** Failing tests: dropping a session pane on the detail tree promotes it into the leaf, or splits it off the edge, it was dropped on; a key the surface would prune is refused; dropping a promoted pane back on the workflow strip demotes it AND places it where it was dropped; a pane key naming another workspace's session of the same name does not move the local one.
- [x] **Step 5** Run `../node_modules/.bin/vp test --project unit paneLayout WorkspaceTerminalView DetailPaneLayout tabbed-panel`. Expected PASS. Commit each part.
- [ ] **Step 6 (owed to Task 6)** The keyboard path, the dock's promote control, and the full-stack promote/demote continuity test — all three need a rendered promoted pane or the controller's session list. Note in Task 6 rather than leaving them implied here.

---

### Task 6: Detail surfaces render promoted session panes

**Files:**

- Modify: `packages/ui/src/views/PRListView.svelte`, `IssueListView.svelte`, `ActivityFeedView.svelte`
- Modify: `packages/ui/src/workspace-inline.ts` (the controller declares the session members)
- Modify: `frontend/src/lib/stores/workspace-host.svelte.ts` (**where the controller is implemented** — without this the views compile against an interface nothing satisfies)
- Test: the three views' unit tests, plus `frontend/src/lib/stores/workspace-host.test.ts` for the implementation

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
  /**
   * The whole pane body for a promoted session, rendered by the view.
   *
   * A snippet rather than an attachment plus a visibility call: splitting them
   * hands the view a visibility API with no owner token, which is exactly the
   * race the owner-scoped registry exists to prevent — a superseded source pane
   * could hide the destination. The frontend supplies `SessionTerminalSlot`,
   * which owns both halves and cannot be called wrong.
   *
   * A getter, not a property: the snippet can only be written in a component, so
   * `WorkspaceHost` registers it with the store and it is null until then.
   */
  sessionPane(): Snippet<[{ paneKey: string; visible: boolean }]> | null;
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

The view renders `{@render inlineWorkspace.sessionPane({ paneKey, visible })}` in
its `renderPane` snippet, passing `DetailPaneLayout`'s own `visible` argument
straight through.

Owed from Task 5 and landed here, because it needs what this task adds: the
`session.promote` / `session.demote` palette commands. A command sees stores, not
components, so which session is "current" has to be published rather than asked
for: the view sets `active` on the session filling the pane — the active workflow
tab, else the open dock's active tab — and `activeHostedSession(surface)` hands
that to the command. Promotion splits it off the workspace pane's leaf rather
than stacking onto it; a tab hidden behind the workspace pane reads as a command
that did nothing.

Still owed and moved to Task 10: the full-stack test that promotes a live session,
demotes it, and proves one attachment survived. It needs a promoted pane driven
through the real app, which is that task's lane.

- [x] **Step 1** Failing tests: a promoted session pane renders its slot for the claimed workspace; selecting an item with a different workspace prunes it while the stored tree keeps it; returning restores it; the pane reports its visibility so a session tabbed behind a sibling goes inert. In `workspace-host.test.ts`: the controller reports the claimed workspace's sessions and nothing when unclaimed.
- [x] **Step 2** Run `../node_modules/.bin/vp test --project unit PRListView IssueListView ActivityFeedView workspace-host WorkspaceTerminalView actions.test`. Expected FAIL. (The three views are only the render half; publication, registry selection, and the palette commands live in the other three.)
- [x] **Step 3** Implement.
- [x] **Step 4** Same command. Expected PASS.
- [x] **Step 5** Commit.

---

### Task 7: One bar — the pane controls popover

**Files:**

- Create: `frontend/src/lib/components/terminal/WorkspacePaneControls.svelte`
- Modify: `packages/ui/src/components/shared/DetailPaneLayout.svelte` (`leafActions:267`)
- Modify: `packages/ui/src/views/{PRListView,IssueListView,ActivityFeedView}.svelte`
- Modify: `frontend/src/App.svelte` (**supplies the controls snippet to the views** — a component test can pass with no production wiring at all)
- Modify: `WorkspaceTerminalView.svelte` (hide `.workspace-toolbar` in embedded mode, `:3125`)
- Test: `frontend/src/lib/components/terminal/WorkspacePaneControls.test.ts`, `packages/ui/src/components/shared/DetailPaneLayout.test.ts`

One `aria-label="Workspace controls"` button in the tab strip's action area, opening a popover holding `WorkflowPresetMenu`, `TerminalZoomControl`, `TerminalOptionsMenu`, and a launch action. Rendered for the container leaf and for any promoted session leaf.

**Embedded only.** The toolbar is hidden when `WorkspaceTerminalView` renders
inside a detail pane; the standalone Workspaces tab keeps it, along with its
`Home` tab, because that tab's chrome is out of scope. Branch on the existing
embedded signal rather than deleting the toolbar outright.

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

**How the controls reach the pane.** Every one of the four is wired to the live
view's state (presets, launch targets, save-in-flight flags, the zoom writer), so
the view keeps them and hands over the rendered chrome: it declares them as a
snippet and registers it with the workspace-host store while embedded, exactly as
it does for a promoted session's pane body. `WorkspacePaneControls` is then only
the button and the popover, and it renders nothing at all when no view is hosted -
which is what keeps the button out of the leaves of surfaces with no workspace.

- [x] **Step 1** Failing tests: `DetailPaneLayout` renders `paneLeafExtras` inside the leaf action area and omits it while flattened; the popover holds the hosted view's controls; it closes on Escape, on outside click, and when the view unregisters under it; a leaf holding neither the container nor a session pane renders no button. In `WorkspaceTerminalView.test.ts`: embedded publishes the controls and drops the toolbar, the standalone tab does the opposite.
- [x] **Step 2** Run `../node_modules/.bin/vp test --project unit WorkspacePaneControls DetailPaneLayout`. Expected FAIL.
- [x] **Step 3** Implement, and hide the toolbar in embedded mode. Do not delete it: the standalone Workspaces tab keeps it.
- [x] **Step 4** Same command plus `--project unit WorkspaceTerminalView PRListView IssueListView ActivityFeedView`, and `--project browser WorkspacePaneControls`. Expected PASS. All three views wire this separately, and only a real browser can tell a usable popover from one clipped by the tab strip.
- [x] **Step 5** Commit.

The launcher action is `LaunchMenu` as it stands; Task 8 replaces it with the
overlay opener, and the "launch action opens the launcher" case belongs there
because the overlay does not exist yet.

---

### Task 8: The launcher overlay replaces the Home tab

**Files:**

- Create: `frontend/src/lib/components/terminal/WorkspaceLauncherOverlay.svelte`
- Modify: `WorkspaceTerminalView.svelte` (drop `home` from `workflowTabDescriptors:511` **in embedded mode only**, and retarget every `selectWorkspaceTab("home")` for that mode)
- Modify: `frontend/src/lib/stores/keyboard/actions.ts` (palette command)
- Test: `frontend/src/lib/components/terminal/WorkspaceLauncherOverlay.test.ts`, `WorkspaceTerminalView.test.ts`, `frontend/tests/e2e-full/00-workspace-launcher.spec.ts`

The overlay wraps today's `WorkspaceHome` body, pushes a modal frame, auto-opens when the workspace has no sessions, and closes when one launches.

**"Nothing to show" includes the dock.** A docked session is not a workflow tab, so
an embedded workspace whose only terminal is in the dock has an empty strip while
being perfectly visible. Auto-open, and the fallback when the active tab vanishes,
both key off `runtimeSessions`, not off the tab list - an overlay over a live
terminal covers the thing the user came for.

**Acceptance coverage lands here, not in Task 10.** Deleting the `Home` tab makes
this overlay the only route to a first session, so "a launch from the overlay
produces a live terminal" is this task's deliverable rather than a later e2e sweep.
The unit lane cannot prove it: it mocks the launch call and the runtime refresh, so
a launch that never reaches tmux passes there. The real-backend spec covers the
round trip a maintainer takes - a session-less workspace auto-opens the overlay,
launching a shell closes it and leaves a shell that executes a command, and the
pane controls popover reopens it afterwards without disturbing that terminal.

**Closing the overlay is gated on the refreshed runtime**, not on the launch call
resolving. The pane can only render what the runtime reports, so closing on a
failed reload would drop the user on an empty pane with the error out of sight.

**The overlay belongs to a workspace, not to the view.** One embedded view serves
every selection on its surface, so both the open state and the once-per-workspace
auto-open guard are keyed by `(workspaceId, hostKey)`; a bare flag carries an open
launcher onto the next workspace's live terminal and then refuses to open the one
that workspace needs. An overlay the VIEW raised over an empty pane also closes
itself once a session appears - a reconnect or a slow first load reports zero
sessions for a moment - while one the user asked for stays until they dismiss it.

**A modal blocks pane zoom** (`toggleZoom` refuses while a frame is open), so every
test that maximizes or expands a pane in a session-less workspace has to dismiss the
launcher first. That is the app's own rule, not test scaffolding.

- [x] **Step 1** Failing tests: embedded mode has no `Home` tab while the standalone Workspaces tab still does; the overlay auto-opens for a session-less workspace, and not while a docked terminal is on screen; a successful launch closes it; a failed launch leaves it open with the error rather than stranding the user on an empty workspace; the palette gets an opener only while a pane hosts the workspace; `Focus Terminal` opens it when there is nothing to focus.
- [x] **Step 2** Run `../node_modules/.bin/vp test --project unit WorkspaceLauncherOverlay WorkspaceTerminalView workspace-host actions.test`. Expected FAIL.
- [x] **Step 3** Implement. Every current `selectWorkspaceTab("home")` becomes `selectFallbackTab()`: the first remaining workflow tab, else the launcher when the workspace has nothing running at all.
- [x] **Step 4** Same command, plus `--project browser WorkspaceHost.browser`, the new `00-workspace-launcher` real-backend spec, and the affected existing ones (continuity, detail-action-buttons). Expected PASS.
- [x] **Step 5** Commit.

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
  (Closed with Task 4, which needed it to persist a promoted pane at all.)
- Focus originates in `DetailPaneLayout`, which reports it to the view via
  `onFocusPane`. Last-focused **session** is per workspace, and only the frontend
  host knows which workspace a claim belongs to, so the views forward a focused
  session pane through `InlineWorkspaceController` rather than tracking it
  themselves.

**Forward every focused pane, and filter in the host.** Two of the three views
already had an `onFocusPane` handler that returns early for anything but its own
route-bound panes, and `IssueListView` had none at all - so a filter on the view
side is three chances to drop the pane the rule depends on, and three places to
teach about session pane keys. The views call `notePaneFocused` first and
unconditionally; the host keeps only the container and the session panes whose key
names a workspace, and files each under the workspace the KEY names rather than the
one currently hosted, so a focus event arriving mid-selection-switch cannot be
filed against the wrong workspace.

**A promoted session's terminal is focused through the pool, not the host.** The
workspace host's parked-focus handshake covers the container only; a promoted
session's live subtree is rendered by the app-level pool outside that wrapper, so
Focus Terminal reaches it through the registry key the view published and focuses
the pool's own wrapper (which needs `tabindex="-1"` for that to land at all).

"The workspace pane" is no longer one leaf:

- `collapsed` — the container and every promoted pane of that workspace are hidden; expanding restores exactly the set that was hidden rather than the default arrangement.
- `expanded` — one of them holds the zoom; expanding zooms the pane holding the workspace's last-focused session, or the container when none is promoted.
- Focus Terminal focuses the pane holding the last-focused session when it is on screen, reveals it when it is not, and opens the launcher when the workspace has no session at all.
- Last-focused session is tracked per workspace and survives promotion, demotion, and selection changes.
- On workspace deletion, drop that workspace's session panes from every surface's stored tree. Nothing else does. There is no session deletion to react to: the only session mutation the API exposes is `stop-host-runtime-session` (a DELETE whose meaning is "stop"), and a stop, an exit, a reconnect gap, and a failed runtime load all leave the placement alone - which is what lets a relaunch reappear in the pane the user put it in.
- Dispose registry entries for a workspace that no surface claims and the Workspaces tab is not showing: parked terminals hold live websockets, so browsing past ten items must not leave ten connections open. (Landed with Task 3's pooling, which is where the hazard appeared; covered by the view's own release-on-switch and release-on-unmount tests.)
- Purge only on an authoritative deletion signal: the workspace-delete path the frontend drives, and a workspace whose load comes back 404. Absence of a session from the runtime is **never** that signal, however the load went - a stop, an exit, a reconnect gap and an outright failure all present as the same absence, so purging on it would throw away exactly the placements a relaunch is supposed to return to.
- What deletion cannot reach stays: a workspace deleted while no surface was mounted leaves its panes in the stored tree until the user demotes them or resets the surface. That is bounded by what the user promoted by hand, so it does not need a cap - and a cap would silently remove a pane the user placed, which is worse than a stale one they can drag away.
- **Dock membership comes from the stored tree, not the live publication.** A stopped or reconnecting session's pane is still part of its workspace's dock: deriving membership from published runtime sessions would leave that pane out of a collapse and then let it reappear on relaunch, flipping the dock from collapsed to split with no user action.
- **The collapse ledger is keyed by `(surface, workspaceId, hostKey)`.** One surface collapses many workspaces in turn, so a surface-only ledger lets B's expand restore A's panes and consume the record A still needed.
- **`expanded` requires an on-screen tab, not leaf membership.** A workspace pane inside the zoomed leaf but behind an active sibling renders nothing, and reporting `expanded` there makes the control refuse the expand the user is asking for.
- **Focus Terminal on a collapsed dock restores the whole ledger**, not just the remembered pane: a container masks the sessions its workspace promoted, so revealing it alone hands back an empty pane while the terminal stays hidden.
- **A promoted pane's focus needs a handshake.** Its slot mounts a render after the reveal, so `focusPromotedSession` records a pending focus that the pool consumes when the wrapper is attached and no longer inert.
- Between generations - a relaunch, a reconnect - the pane renders nothing for that flush and keeps its tab: the pool hands over the new subtree within the same update, so a spinner would flicker. Permanently gone means deleted, not an indefinitely blank pane.

- [x] **Step 1** Failing tests, one per bullet. Deletion and retention need pairs, because a test for the destructive half alone passes just as well against a rule that throws away every placement: deleting a session drops its pane from every surface's stored tree, and deleting a workspace with two promoted panes leaves no session keys behind - while stopping a session, exiting one, a reconnect that briefly reports no sessions, and a runtime load that fails all keep the pane, and a session relaunched under a reused name lands back in it. Then: collapsing and expanding a workspace with one promoted pane restores that pane rather than the default tree; selecting three items in turn leaves only the current workspace's terminals in the registry; `noteFocused` accepts a well-formed session pane key and still rejects a malformed one; focusing a promoted pane and focusing the container both update the workspace's last-focused session.
- [x] **Step 2** Run `../node_modules/.bin/vp test --project unit workspace-host paneLayout WorkspaceTerminalView PRListView IssueListView ActivityFeedView`. Expected FAIL. (This task spans the store, the pane layout, the view that owns the runtime and the deletion paths, and all three views; `workspace-host` alone would miss most of it.)
- [x] **Step 3** Implement.
- [x] **Step 4** Same command, plus `--project browser SessionTerminalPool.browser` for the pooled wrapper's focusability - jsdom will focus anything, so only the browser lane can show that the production wrapper is a focus target at all. Expected PASS.
- [x] **Step 5** Commit.

---

### Task 10: End-to-end coverage and context

**Files:**

- Modify: `frontend/tests/e2e-full/00-inline-workspace-continuity.spec.ts`
- Modify: `frontend/src/App.pane-commands.browser.svelte.ts` (the owed push-vs-replace history cases)
- Modify: `context/ui-interaction-contracts.md`, `context/ui-design-system.md`
- Modify: `docs/superpowers/plans/2026-07-26-session-panes.md` (check the boxes)

The real-backend spec is the only place that proves liveness against a real tmux session. Add: promote a session to a top-level pane and confirm the marker text survives; demote it and confirm the same; change the selected item and come back. Promote a **dock** session too, since its container is not the one promotion was designed around. Drive it through the `session.promote` / `session.demote` palette commands that landed with Task 6 rather than a drag, so the spec exercises the same path a keyboard user takes.

The first of these landed early, with Task 3: "a pooled workflow session keeps its live tmux shell while its host is reparented" moves a real shell out of the dock and follows it through the Workspaces-tab-to-detail-pane reparent and back. Once the dock was pooled that same move became a real slot-to-slot transfer, so the test now also asserts the moved terminal's registry key reappears inside the destination slot — the coverage this task was holding for want of two live slots. Pooling changed how every workflow session renders, so it needed real-backend proof then rather than at the end — the browser-tier pool tests all mount exited sessions and cannot show a websocket surviving.

**The launcher's own spec landed with Task 8**, as
`frontend/tests/e2e-full/00-workspace-launcher.spec.ts`: deleting the `Home` tab is
what makes that overlay the only route to a first session, so proving the launch
round trip was that task's deliverable rather than a later sweep. Nothing about it
is owed here.

Context edits, terse per `context-guide.md`:

- the portal singleton becomes "one live subtree per session key, one mounted slot per key" — landed with the dock pooling, since a container rendering its own `TerminalPane` was a live hazard from that point on;
- session pane keys are kept-if-stored, never reinserted;
- the launcher is an overlay, and `Home` is not a tab in embedded mode.

Also still owed from the pane-visibility work that landed with Task 3: app-level
coverage for the push-vs-replace history rule in the arrangements the render
report newly distinguishes — a route pane covered by another leaf's zoom, and one
tabbed behind a sibling in its own leaf. `App.pane-commands.browser.svelte.ts`
covers command availability, not Back-stack behaviour. The tabbed-behind case
needs a third pane in the leaf, so it wants a claimed workspace: schedule it after
Task 6, when a promoted session gives the views a third pane without one.

- [x] **Step 1** Update the continuity spec's selectors and add its three promotion cases.
- [x] **Step 1b** The two owed history cases landed in `packages/ui/src/views/PRListView.test.ts` instead: both are decided by the view's own render report and route handling, so mounting the view proves them with no browser primitive involved (the arrangements are set by store calls, not by dragging). `App.pane-commands.browser.svelte.ts` keeps command availability.
- [x] **Step 1c** Two more full-stack cases the second review round asked for, both in the real-backend lane because they turn on a live terminal and real pane teardown: a promoted terminal collapsing and coming back with its container, and the palette launcher invoked from a collapsed, zoom-covered, and tabbed-behind pane. The first of these found a crash - a whole split node leaving the on-screen tree hands its still-mounted children an undefined `node`, and `TabbedPanelTree` threw on it and took the surface down, leaving a detail with no panes at all.
- [x] **Step 2** Run `../node_modules/.bin/vp exec playwright test --config playwright-e2e.config.ts --project=chromium tests/e2e-full/00-inline-workspace-continuity.spec.ts tests/e2e-full/00-workspace-launcher.spec.ts`. Expected PASS. (The launcher spec is included even though it landed earlier: the promotion cases share its pane layout and serial page.)
- [x] **Step 3** Full gate: `../node_modules/.bin/vp test`, both Playwright configs whole, `svelte-check`, `make lint`.
- [x] **Step 4** Capture a screenshot of a promoted session pane with the `capture-playwright` skill for the PR body.
- [x] **Step 5** Commit.

---

## Risks

- **Two portals, one DOM.** A session's terminal is inside the pool, but the container's whole `WorkspaceTerminalView` is itself reparented by `WorkspaceHost`. Task 2's sibling placement is what keeps a promoted terminal from being dragged into the parking node with its container; get that wrong and promoting a session while the container is parked blanks the terminal.
- **Double slots.** If both a workspace container and a promoted pane render a slot for one session, registration order silently decides the winner. The invariant is enforced by the containers masking promoted sessions out of what they render; Task 5's tests must cover the transitional flush, and the mask must be derived at render time rather than applied by an effect that could lag a frame.
- **Stored-tree growth.** Session keys accumulate per workspace. Workspace deletion is the only thing that bounds it, which is deliberate: the alternative is dropping panes the user placed.
- **Connection growth.** Every parked terminal holds a websocket, so Task 9's claim-change disposal is what stops browsing a list from opening one connection per workspace visited.
