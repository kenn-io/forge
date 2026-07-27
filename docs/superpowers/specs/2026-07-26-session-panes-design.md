# Terminal sessions as first-class detail panes

Follow-on to `2026-07-25-generalized-pane-layout-design.md`, which put
conversation, diff, and the inline workspace on one rearrangeable pane tree per
top-level mode. This spec covers the workspace pane's own contents.

## Problem

Inside a detail surface the inline workspace stacks three bars above the
terminal:

1. the detail pane's tab strip (`Workspace`),
2. `WorkspaceTerminalView`'s `Workflow` toolbar (preset menu, terminal zoom,
   terminal options, launch menu),
3. `WorkflowSplitTree`'s own tab strip (`Home`, `Terminal`, one per session).

Three bars is most of the vertical budget of a pane that exists to show a
terminal. Worse, the third strip belongs to a tree the detail surface cannot
reach: sessions live in `terminalLayout.workflowTree` with drag scope
`workspace:<id>`, while the detail panes live in a `PaneLayoutStore` with scope
`detail:<surface>`, so no session can be dragged out to sit beside the
conversation. The `Home` tab is permanent even though it is a launcher a
maintainer uses once per workspace.

## Decisions

| Question                            | Decision                                                                    |
| ----------------------------------- | --------------------------------------------------------------------------- |
| Session ↔ detail tree               | One tree. A session can be promoted to a top-level detail pane.             |
| Does a workspace pane still exist   | Yes, as the default container sessions start in and return to.              |
| `Home` tab                          | Deleted. The launcher becomes a transient overlay.                          |
| Workspace-level controls            | One button, top right of the workspace pane's tab strip, opening a popover. |
| Terminal liveness                   | Today's single reparented subtree becomes a registry keyed by session.      |
| Session pane identity in the layout | Keyed by session id; the stored intent tree remembers placement.            |

Optimize for one to three sessions per item. A workspace with four or more
sessions is already unusual; nothing here needs to scale past that.

## Model

### Pane vocabulary

`PANE_SURFACES` currently lists a fixed vocabulary per surface, and
`normalizeTabbedPanelTree` prunes any stored tab outside it while reinserting
every known tab that is missing. Session panes are dynamic, so both halves need
qualifying:

- A pane key of the form `session:<sessionKey>` is **accepted** by the parser
  regardless of the surface's static list. The vocabulary becomes "the static
  list, plus anything matching the session prefix".
- Session keys are **never reinserted**. Reinsertion exists so a new static pane
  appears for users with a stored layout; a session pane appears only because
  the user promoted it, and must not be conjured into a tree it was removed
  from.
- The default tree is unchanged: conversation/diff in one leaf, the workspace
  container below. No session is in it.

### Promotion and demotion

Dragging a session tab out of the workspace container and dropping it on the
detail tree promotes it: the session leaves `terminalLayout.workflowTree` and
becomes a `session:<key>` pane in the surface's `PaneLayoutStore`. Dragging it
back demotes it. Both directions are ordinary tab drags — the promotion is a
consequence of which tree accepted the drop, not a separate gesture.

The container stays even when empty, because it is where the launcher lives and
where a newly launched session lands. An empty container renders the launcher
overlay's contents inline rather than an empty tab strip.

### Availability

A `session:<key>` pane is available when the surface's currently claimed
workspace owns a session with that key. Availability is derived at render time
from the same data the container's tab strip uses — never read back from an
effect, per the pane-layout spec's render-time rule.

Selecting an item whose workspace has different sessions prunes the previous
item's session panes out of the rendered tree while the stored intent tree keeps
them, which is what makes "the panes that were showing last time" come back when
that workspace is selected again. Entries are dropped from the intent tree when
their session or workspace is deleted, so storage does not grow without bound.

### Drag scope

Two trees now exchange tabs, and one must stay isolated:

- Inside a detail surface, the embedded workspace container's tree uses the
  surface's scope (`detail:<surface>`) so its tabs can cross into the detail
  tree and back.
- The standalone Workspaces tab keeps `workspace:<id>`. It has no detail panes
  to exchange with, and its per-workspace isolation is the reason that scope was
  namespaced in the first place.

## Terminal liveness

`WorkspaceHost` keeps exactly one live DOM subtree and reparents it between
registered portal slots, which is why exactly one slot may be mounted at a time.
With N session panes that becomes a registry: one live subtree per session,
each reparented into whichever slot renders it and parked in the hidden host
when no slot does. The invariant weakens from "one live subtree" to "one live
subtree **per session key**, and one mounted slot per session key".

Consequences to hold onto:

- A slot registers under `(surface, sessionKey)` rather than `surface`.
- Parking, the geometry-gated reveal, and the focus-reclaim behaviour are per
  entry, not global.
- A session's subtree is disposed when its session ends or its workspace is
  deleted — not when its pane closes, since a closed pane is exactly the case
  the parking area exists for.

Reattaching to tmux on mount instead was considered and rejected: every pane
move, zoom, or navigation would repaint the terminal and refetch scrollback.

## Chrome

### The single control button

The `Workflow` toolbar is deleted. Its four controls move into one button at the
top right of the workspace container's tab strip, which expands a popover
containing:

- workflow presets (save, update, apply, delete),
- terminal zoom,
- terminal options,
- launch — which opens the launcher overlay.

The popover is the way back to the launcher from a shell terminal, which is the
other half of deleting the `Home` tab. A promoted session pane carries the same
button in its own strip, since it is still that workspace's terminal.

### The launcher overlay

`Home` stops being a tab. The launcher renders as a transient overlay over the
workspace container:

- auto-opened when the workspace has no sessions, so a fresh workspace still
  lands on the launcher;
- reopened from the popover's launch action, from a palette command, and by
  Focus Terminal when there is nothing to focus;
- dismissed when a session launches, on Escape, and on a click outside.

It participates in the modal stack like every other overlay, so the pane
controls that refuse to act while a modal frame is open keep refusing.

## What this changes elsewhere

- `context/ui-interaction-contracts.md`'s portal-singleton entry is rewritten
  around the keyed registry.
- Focus Terminal, the collapse/expand dock triad, and the dock-mode derivation
  are restated in terms of "the workspace's panes": with sessions promotable,
  "the workspace pane" is no longer a single leaf. Collapsed means the container
  and every promoted session pane of that workspace are hidden.
- `WorkspaceTerminalView` gains an embedded mode that renders neither the
  `Workflow` toolbar nor a `Home` tab, distinct from the standalone Workspaces
  tab mode which keeps its current chrome.
- The inline-workspace e2e continuity specs need reworking: continuity now has
  to hold across promotion and demotion, not only across slot changes.

## Testing

Lane per the rules in `context/testing.md`:

- **Unit** — session key parsing and the no-reinsert rule; promotion and
  demotion as tree edits; availability pruning across a workspace change;
  intent-tree entries dropped on session and workspace deletion; the launcher's
  auto-open condition.
- **Vitest browser** — the keyed registry: two session panes live at once, a
  promoted pane keeping its subtree across the promotion, parking on close, and
  focus reclaim when a session pane closes under the user.
- **Playwright, mock** — the popover's contents and the launcher overlay's open
  and dismiss paths; the drag from the container into the detail tree.
- **Playwright, real backend** — terminal liveness across a promotion, a
  demotion, and an item change, against a real tmux-backed session. This is the
  one thing no other lane can prove.

## Out of scope

- The Workspaces tab's own layout and chrome.
- Multiple workspaces claimed by one surface at once.
- Any change to session creation, tmux ownership, or the runtime lifecycle.
