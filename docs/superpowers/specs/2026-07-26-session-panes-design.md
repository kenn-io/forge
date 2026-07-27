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

| Question                            | Decision                                                                     |
| ----------------------------------- | ---------------------------------------------------------------------------- |
| Session ↔ detail tree               | One tree. A session can be promoted to a top-level detail pane.              |
| Which sessions are promotable       | All of them. A terminal tab promotes exactly like a workflow tab.            |
| Does a workspace pane still exist   | Yes, as the default container sessions start in and return to.               |
| `Home` tab                          | Deleted **in embedded mode only**; the launcher becomes a transient overlay. |
| Workspace-level controls            | One button, top right of the workspace pane's tab strip, opening a popover.  |
| Terminal liveness                   | Today's single reparented subtree becomes a registry keyed by session.       |
| Session pane identity in the layout | Keyed by session id; the stored intent tree remembers placement.             |

Every decision above applies to the **embedded** workspace — the pane inside a
detail surface. The standalone Workspaces tab keeps its current chrome, `Home`
tab, and `Workflow` toolbar; its layout is out of scope. Embedded and standalone
are therefore two modes of `WorkspaceTerminalView`, not one migration.

Optimize for one to three sessions per item. A workspace with four or more
sessions is already unusual; nothing here needs to scale past that.

Promotion and demotion must also be reachable from the keyboard. Drag is the
discoverable path, not the only one: a palette command promotes the focused
session and demotes a promoted pane.

## Model

### Pane vocabulary

`PANE_SURFACES` currently lists a fixed vocabulary per surface, and
`normalizeTabbedPanelTree` prunes any stored tab outside it while reinserting
every known tab that is missing. Session panes are dynamic, so both halves need
qualifying:

- A pane key naming a workspace, a fleet host, and a session is **accepted** by
  the parser regardless of the surface's static list. Its three parts are
  percent-encoded and `/`-joined rather than colon-separated, because session
  keys are opaque and routinely contain colons (`ws-1:helper`) — a colon form
  could not be parsed back, and two different sessions could spell one key. The
  vocabulary becomes "the static list, plus anything matching the session
  prefix". The workspace and fleet host are part of the key because a session
  key is only unique within a workspace on a host: two workspaces both having an
  `agent` session is the normal case, and a bare `session:agent` would alias
  their placements and their cleanup.
- Session keys are **never reinserted**. Reinsertion exists so a new static pane
  appears for users with a stored layout; a session pane appears only because
  the user promoted it, and must not be conjured into a tree it was removed
  from.
- The default tree is unchanged: conversation/diff in one leaf, the workspace
  container below. No session is in it.

### Promotion and demotion

Dragging a session tab out of the workspace container and dropping it on the
detail tree promotes it. Dragging it back demotes it. Both containers can be the
source — a workflow tab and a terminal tab in the dock promote identically —
because both render pooled slots and neither owns the terminal.

**The detail tree is the only record of a promotion.** A session is promoted in a
surface when that surface's stored pane tree contains its pane key; nothing else
is written. `sessionRegions` keeps owning the session's home container, so
demotion has nothing to remember and nothing to restore.

The workspace containers then **mask** the promoted session at render time: their
rendered trees are derived from the stored trees with promoted entries pruned.
Pruning the stored trees instead would return a demoted session to the right
region but lose its tab order, split, group, and active position — the placement
the user is expecting back.

Two consequences worth stating, because they read as bugs otherwise:

- Promotion is **per surface**, not global. A session promoted in the PRs surface
  still sits in its home container on the Activity surface and on the standalone
  Workspaces tab. Those are two surface-local placements of one session, not two
  representations of one promotion.
- One session still has one live terminal, so if two surfaces were ever visible
  at once one of their slots would be blank. Nothing about promotion changes
  that, and no promotion model could.

The drag still cannot be an ordinary tab move: every existing layout mutation
rejects a source tab the destination tree does not already contain, so the
destination needs a primitive that inserts a tab it has never held. It does not
need to be told where the tab came from — "is this key already in my tree" is the
same question and the tree can answer it. What it does not need either is a
two-phase commit: there is exactly one authoritative write, validated against the
current claim and session list before it lands. A refused drop writes nothing, and
the pool's owner-scoped registration makes the one-frame handoff safe on its own.

What must be canonical is the key. A session's workspace tab is keyed by session
key alone, which is only unique within a workspace, so the shared drag payload
carries the full `session:<workspace>/<host>/<session>` form and the workspace
tree translates back on read — rejecting a key belonging to another workspace, or
one that is not a session pane at all.

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
when no slot does. Every container renders slots, the terminal dock included, so
no container owns a terminal and a session can move between any two of them
without losing its tmux attachment. The invariant weakens from "one live subtree" to "one live
subtree **per session key**, and one mounted slot per session key".

Consequences to hold onto:

- A slot registers under the registry key — workspace, fleet host, session, and
  generation — not under a surface. Surfaces do not appear in the identity at
  all: two surfaces showing one workspace show the same session, and the last
  slot to register owns it. It registers its visibility along with its element. An inactive tab panel keeps
  its slot mounted under `visibility: hidden`, so presence in the DOM does not
  mean the terminal is on screen; a session's terminal is active exactly when its
  slot reports visible. A terminal left active behind a hidden tab claims focus
  and competes for keystrokes with the visible one.
- Parking, the reveal, and the focus-reclaim behaviour are per entry, not global.
- **Last registration wins, and only the owner may release.** Two surfaces can
  claim the same workspace at once (PRs and Activity showing the same item), and
  promotion registers the destination slot while the source is still unmounting.
  A slot that has been superseded may neither clear the registration nor change
  the visibility of the key it no longer owns, or the departing half of a
  promotion strands the terminal in the parking area.
- Registry entries are keyed by workspace, fleet host, session, and the session's
  `created_at` generation. Without the generation, a session relaunched under a
  reused key would adopt the dead session's subtree and its closed socket.
- **Disposing a terminal is not the same as forgetting where its pane was.** A
  session that exits or is stopped loses its live subtree, because the socket is
  gone, but keeps its placement in the stored tree: relaunching under the same
  key must bring it back where the user put it, which is the whole reason layout
  keys omit the generation. Only deleting the session, or its workspace, removes
  the placement — and then from every surface's tree, along with any zoom or
  focus metadata naming it, because promotion is surface-local and a reused
  session key would otherwise resurrect a placement nobody asked for. A closed pane disposes nothing at all — that is what the parking
  area is for.
- It is also disposed when its workspace stops being claimed by any surface and
  is not the one the Workspaces tab is showing. Parked terminals hold live
  websockets, so retaining every workspace a maintainer merely browsed past
  would accumulate connections for the session; the bound is "the current claims
  plus the tab", not a cache with an eviction policy.

Layout keys deliberately omit the generation while registry keys include it: a
session relaunched under the same name should reappear in the pane the user put
it in, but must not inherit the previous generation's live subtree.

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

`DetailPaneLayout` fills `TabbedPanelTree`'s leaf-action slot with its own
split/zoom/close controls and forwards nothing, so it gains a caller snippet
rendered beside them. The button then obeys the structural-chrome rule for free:
leaf actions are already suppressed in a flattened layout.

### The launcher overlay

`Home` stops being a tab. The launcher renders as a transient overlay over the
workspace container:

- auto-opened when the workspace has no sessions, so a fresh workspace still
  lands on the launcher;
- reopened from the popover's launch action, from a palette command, and by
  Focus Terminal when there is nothing to focus;
- dismissed when a session launches, on Escape, and on a click outside.

A launch that fails or times out leaves the overlay open with the error, because
dismissing on the attempt rather than on the session would strand a maintainer on
an empty workspace with no visible way back. Cancelling is the same as Escape.

It participates in the modal stack like every other overlay, so the pane
controls that refuse to act while a modal frame is open keep refusing.

## What this changes elsewhere

- `context/ui-interaction-contracts.md`'s portal-singleton entry is rewritten
  around the keyed registry.
- Focus Terminal, the collapse/expand dock triad, and the dock-mode derivation
  are restated in terms of "the workspace's panes": with sessions promotable,
  "the workspace pane" is no longer a single leaf. Made deterministic:
  - **Collapsed** — the container and every promoted pane of that workspace are
    hidden. Expanding restores exactly the set collapse itself hid, so a pane the
    user had already closed stays closed and collapse remains reversible rather
    than a reset to the default arrangement.
  - **Expanded** — any one of those panes holds the zoom. Expanding zooms the
    pane holding the workspace's last-focused session, or the container when
    none is promoted.
  - **Focus Terminal** — focuses the pane holding the last-focused session of
    that workspace if it is on screen; otherwise reveals it by the existing
    unhide/activate/clear-foreign-zoom sequence; otherwise, with no session at
    all, opens the launcher overlay.
  - Last-focused session is per workspace and survives promotion, demotion, and
    selection changes, since it is what all three rules key off.
- `WorkspaceTerminalView` gains an embedded mode that renders neither the
  `Workflow` toolbar nor a `Home` tab, distinct from the standalone Workspaces
  tab mode which keeps its current chrome.
- The inline-workspace e2e continuity specs need reworking: continuity now has
  to hold across promotion and demotion, not only across slot changes.

## Testing

Lane per the rules in `context/testing.md`:

- **Unit** — session key parsing and the no-reinsert rule; the atomic transfer,
  including a rejected drop leaving both trees untouched; availability pruning
  across a workspace change; intent-tree entries dropped on session and
  workspace deletion; registry disposal when a workspace stops being claimed; a
  relaunched session under a reused key getting a fresh subtree; the launcher's
  auto-open condition; the collapse/expand/Focus Terminal rules above with two
  promoted panes.
- **Vitest browser** — the keyed registry: two session panes live at once, a
  promoted pane keeping its subtree across the promotion, parking on close, and
  focus reclaim when a session pane closes under the user.
- **Playwright, mock** — the popover's contents and the launcher overlay's
  dismiss paths; the drag from the container into the detail tree.
- **Playwright, real backend** — terminal liveness across a promotion, a
  demotion, and an item change, against a real tmux-backed session. Also the
  launcher round trip: auto-open on a session-less workspace, launch a shell,
  reopen from the popover. With `Home` deleted the overlay is the only route to a
  first session, and only this lane can prove a launch produces a live one.

## Out of scope

- The Workspaces tab's own layout and chrome.
- Multiple workspaces claimed by one surface at once.
- Any change to session creation, tmux ownership, or the runtime lifecycle.
