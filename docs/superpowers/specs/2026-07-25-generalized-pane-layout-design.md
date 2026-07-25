# Generalized Pane Layout Design

Date: 2026-07-25

## Problem

The Workspaces tab has a splittable, draggable pane system. The PR, Issue, and
Activity surfaces have three unrelated ad-hoc layout mechanisms instead, so the
workspace sub-pane inside a PR detail can only ever appear below the detail. A
maintainer cannot drag it to the right to get a list / detail / workspace
three-pane arrangement, and the diff cannot be moved or maximized at all.

Goal: one pane abstraction for everything inside a top-level mode. Top-level
modes stay fixed — panes never move between them.

## What already exists

`packages/ui/src/components/shared/` already holds a surface-agnostic split-tree
system keyed by plain strings:

- `tabbed-panel-layout.ts` — `TabbedPanelNode` = leaf `{tabs[], activeTabKey}` |
  split `{direction, ratio, first, second}`, plus activate, move-before,
  append-to-leaf, split-into-leaf, update-ratio, prune, normalize.
- `TabbedPanelTree.svelte` — recursive renderer: tab strip with drag-sort
  placeholders, center-merge / edge-split drop overlay, `SplitResizeHandle`,
  `renderTab` / `tabIcon` / `tabActions` snippets, `dragScope` isolation.
- `tabbed-panel-drag.ts` — scoped drag payloads.

`WorkflowSplitTree.svelte` is a thin adapter over it and is the only consumer.
`context/vscode-workflow-panel-interaction-spec.md` documents the intended
interaction model.

The PR/Issue/Activity side instead runs:

| Mechanism | Location | Own state |
| --- | --- | --- |
| `CollapsibleSidebar` (list ↔ detail) | all three views | kit-ui, width in store |
| hand-rolled `.detail-split-layout` | `PRListView.svelte` | `pr-detail-split-view`, `pr-detail-split-ratio`, `minSplitViewWidth = 1280`, bespoke "Split view" toggle |
| `WorkspaceDockPanel` → `BottomDock` | all three views | `middleman-workspace-dock-height-<surface>`, `dockModes` triad in `workspace-host.svelte.ts` |

This work deletes the latter two and rewires their surfaces onto the primitive
that already exists.

## Scope

- In: PRs, Issues, Activity. The list/rail stays a fixed `CollapsibleSidebar`;
  only the content beside it becomes a pane tree.
- In: general per-leaf maximize, replacing the workspace dock's
  split/collapsed/expanded triad.
- Out: the list joining the tree; Kata, Docs, Reviews; moving panes between
  top-level modes (prevented by construction, see Drag scope).

## Model

```
tabbed-panel-layout.ts        (extended)
        │
        ├── paneLayout.svelte.ts        new: per-surface persisted store + zoom rules
        │
        ├── DetailPaneLayout.svelte     new: TabbedPanelTree + tab spec + zoom + pane menu
        │       ├── PRListView
        │       ├── IssueListView
        │       └── ActivityFeedView
        │
        └── WorkflowSplitTree.svelte    unchanged (Workspaces tab)
```

### Additions to `tabbed-panel-layout.ts`

- `parseTabbedPanelLayout(raw)` / `serializeTabbedPanelLayout(state)` —
  versioned and validating, defaulting on malformed input, matching
  `parseTerminalLayout`'s existing discipline.
- `zoomedLeafID: string | null` as a sibling field of `tree`, not a node
  property. Zoom is view state over a tree, not a shape of the tree.
- **Intent tree vs render tree.** The persisted tree retains every known tab
  including currently unavailable ones (no claimed workspace → no workspace
  tab). Rendering prunes against the available set. Edits apply to the intent
  tree by leaf ID, which survives pruning, so a workspace pane dragged to the
  right returns to the right when it reappears. No placement heuristics.
- `flattenTabbedPanelTree(tree)` → single leaf preserving tab order and the
  active tab.

### Persistence

One layout per top-level mode, keyed `middleman-pane-layout-v1:<surface>` in
localStorage, where surface is `prs` | `issues` | `activity`. Selecting a
different item never changes the layout.

### Drag scope

`dragScope` is the surface key. A PR pane cannot be dropped into the Workspaces
tree because the drag payload is rejected by scope. The "top-level tabs are
sacrosanct" rule is enforced mechanically, not by convention.

### Effective visibility

`TabbedPanelTree` keeps every tab of a leaf mounted, hiding inactive ones with
`visibility: hidden`, which preserves diff scroll position. `renderTab`'s second
argument changes meaning from "active tab in this leaf" to **effective
visibility**: active in its leaf AND not hidden by a zoom elsewhere. The
workspace pane forwards this to `isHostVisible()`; see Workspace pane.

## Zoom

When `zoomedLeafID` is set and that leaf exists, it renders at full size and
every other subtree stays mounted but `hidden` + `inert` — the same semantics
today's expanded dock applies to the detail side.

`WorkspaceDockPanel` carries five behaviors that must be ported into
`paneLayout.svelte.ts`, not rediscovered later:

1. A claim ending while zoomed un-zooms and returns focus to the detail pane.
2. A panel unmounting while zoomed un-zooms, so the next item cannot open
   hidden behind a full-screen pane.
3. A selection change from item A to item B while zoomed un-zooms, including
   the no-observable-gap case `setClaim` handles today (a reclaim landing in the
   same update as the release).
4. A closing pane reclaims focus only when focus was inside the closing subtree
   or already on `<body>` — never stolen from a control the user moved to.
5. Zoom is refused while modal stack depth is greater than zero.

Rules 1 to 3 are one invariant: a zoom must not outlive what was zoomed.

## Route and active tab

PRs mode has `/pulls/.../files` as a route, so "which of conversation/files is
active" is both URL state and layout state. Rule:

- While conversation and files share a leaf, the route and that leaf's
  `activeTabKey` are a two-way binding, as today.
- Once they are split into separate leaves both are visible; the route follows
  whichever of the two was most recently focused.
- A deep link activates the pane it names, and un-zooms if that pane is hidden
  by a zoom on another leaf.

## Surfaces

Each surface declares a tab spec: key, label, icon, availability, and whether it
is closable. Availability drives pruning; the intent tree preserves position
across availability changes.

| Surface | Tabs | Availability |
| --- | --- | --- |
| PRs | conversation, files, workspace | workspace requires a claim |
| Issues | conversation, workspace | workspace requires a claim |
| Activity | conversation, files, commit diff, workspace | files requires a PR selection; commit diff requires a commit selection; workspace requires a claim |

Default trees reproduce today's arrangement: a leaf holding conversation and
files, split vertically above a leaf holding workspace.

Panes are not closable, only rearranged — except the workspace pane, whose close
action means collapse, supplied through the existing `tabActions` snippet.

## Activity restructure

Activity today embeds `PRListView` and `IssueListView` — whole list views, sidebar
hidden — and wraps both in a single outer `WorkspaceDockPanel` so that switching
a PR selection to an Issue selection does not reparent the terminal.

That wrapper becomes unnecessary. With the workspace pane as a sibling leaf in
Activity's own tree, the slot element belongs to Activity's `DetailPaneLayout`
and does not unmount when the selection kind changes. A PR→Issue switch swaps
the claim, which is a `WorkspaceTerminalView`-internal workspace switch, not a
DOM reparent. Terminals are workspace-owned and independent of which item is
selected.

So Activity renders pane bodies directly — `PullDetail`, `IssueDetail`,
`DiffFilesLayout`, `CommitDiffPanel` — and the double embed and the
`renderWorkspaceDock` prop are deleted.

This requires the claim lifecycle to move out of the list views. The same
~40 lines of claim / release / identity-invalidated effects are currently
triplicated across `PRListView`, `IssueListView`, and partially Activity, which
already derives its own `claimIdentity` and only delegates the claiming. They
become one shared module consumed by all three surfaces. This is a
simplification, not added scope.

Pane bodies shared between PRs mode and Activity (`PullDetail` with differing
`autoSync`, `hideStaleWhileLoading`, `workflowApprovalSync`,
`onStackMemberNavigate`) are factored into a small per-pane component rather
than duplicated prop threading.

## Workspace pane

The workspace terminal is a singleton DOM subtree that `WorkspaceHost.svelte`
`appendChild`s between registered slot elements, revealing it only once
`getBoundingClientRect().height > 0`. A workspace pane is therefore a portal
target, with two consequences:

1. Exactly one slot element exists per surface and must survive ratio drags and
   re-renders. It does: reparenting triggers only on slot-element identity
   change.
2. A hidden but sized panel would let the reveal loop reveal the terminal
   invisibly, so the pane gates `isHostVisible()` on effective visibility.

`dockModes` in `workspace-host.svelte.ts` is deleted. `getDockMode()` becomes
derived from the layout:

- `expanded` — the workspace tab's leaf is the zoomed leaf.
- `collapsed` — the workspace tab is not the active tab of its leaf, or is
  absent from the render tree.
- `split` — otherwise.

`setDockMode()` maps onto layout operations, and `focusTerminal()` activates the
tab and focuses the host, keeping its existing best-effort-then-pending-flag
behavior. This derivation is the most delicate part of the change: the
comment-dense edge-case handling in `WorkspaceDockPanel` and `setClaim` /
`clearClaim` encodes real bugs and must keep passing.

## Responsive fallback

Below a container width of 1280px — the same threshold `PRListView`'s
`minSplitViewWidth` uses today — the renderer uses
`flattenTabbedPanelTree(tree)`: flat tabs, no splits, without mutating the
persisted tree. This replaces `PRListView`'s `detailHostWidth >= 1280` gate and
covers phone, focus, and embed hosts, which pass `hideSidebar` and have no room
for splits. Ratio clamps stay at 0.12–0.88; the flatten threshold, not a
per-leaf pixel minimum, is what prevents unusable narrow panes.

## Discoverability

Dragging is not discoverable and the "Split view" toggle is being deleted.
Replacements:

- A per-leaf icon cluster, right-aligned in the tab strip. Not a dropdown: it
  reuses the existing `tabbed-panel-tab-tool` idiom that per-tab rename, move,
  and close already use — 20x22px icon-only buttons, hidden until the leaf is
  hovered or a button takes focus. Icon-only requires `title` plus `aria-label`
  on every button, as `WorkflowSplitTree.svelte` already does.
- Command palette entries alongside the existing PR-detail commands.

| Action | Icon | Applies |
| --- | --- | --- |
| Split right | `square-split-horizontal` | active tab of this leaf, `direction: "horizontal"`, `placement: "after"` |
| Split down | `square-split-vertical` | active tab of this leaf, `direction: "vertical"`, `placement: "after"` |
| Maximize / Restore | `maximize` / `minimize` | toggles `zoomedLeafID` for this leaf |

All four icons are new to this repository: none of
`square-split-horizontal`, `square-split-vertical`, `maximize`, or `minimize`
currently appears in the `optimizeDeps.include` list in
`frontend/vite.config.ts`. Per `context/testing.md`, each must be added there or
the browser-lane CI job fails on a cold cache with "Failed to fetch dynamically
imported module" in unrelated suites, while passing locally on a warm one.

Icon names are verified against `@lucide/svelte@1.23.0`. Lucide's
`horizontal`/`vertical` suffix names the arrangement axis, matching the tree's
own `SplitDirection`: `square-split-horizontal` draws a vertical divider with
halves side by side and pairs with `direction: "horizontal"`. The names align;
do not "correct" them.

Both split buttons are disabled when the leaf holds a single tab.
`splitTabbedPanelTabIntoLeaf` already returns the tree unchanged in that case,
so an enabled button would be a dead control.

Reset layout is surface-scoped, not leaf-scoped, so it appears only in the
palette — never in a per-leaf control, where it would silently act beyond the
leaf the user aimed at.

## Deletions and migration

Deleted: `WorkspaceDockPanel.svelte`, `.detail-split-layout` and its
"Split view" toggle, the `renderWorkspaceDock` prop, `dockModes`, and the
localStorage keys `pr-detail-split-view`, `pr-detail-split-ratio`,
`middleman-workspace-dock-height-<surface>`.

No migration: old keys are dropped and every layout resets once to the default
arrangement, which reproduces today's appearance. Per `no-compat-scaffolding`, a
one-time UI-preference reset does not justify a translation layer.

`BottomDock` no longer appears on these surfaces. The workspace pane is a pane
that happens to default to the bottom position, with no dock-specific chrome.

Conversation and Files adopt the tree's compact tab chrome, replacing today's
larger underlined `.detail-tab` buttons. One tab idiom app-wide is the intent.

## Testing

Following the four axes in `CLAUDE.md`:

- **Vitest, pure:** `tabbed-panel-layout.ts` additions — parse/serialize round
  trips and malformed input, zoom set/clear, flatten preserving order and active
  tab, intent-vs-render pruning including position restoration after a tab
  becomes unavailable and available again.
- **Vitest + jsdom:** `paneLayout.svelte.ts` rules — the five zoom behaviors,
  per-surface persistence isolation, derived dock mode for each of the three
  states, route ↔ active-tab binding including the split-apart and deep-link
  cases. Also the per-leaf icon cluster, which is UI-owned state: both split
  buttons disabled on a single-tab leaf, the maximize/restore toggle following
  `zoomedLeafID`, and an accessible label on every icon-only button.
  `appHarness` mounts where route-derived state matters.
- **Browser tier:** extend the existing `TabbedPanelTree.test.ts` and
  `WorkspaceDockPanel.browser.svelte.ts` for drag between leaves, edge-split
  drop, native focus restoration on pane close, and tab-strip keyboard
  behavior. Do not duplicate coverage the workspace tree already has.
- **Playwright:** only where real geometry is required — a ratio drag producing
  actual pixel widths, and the flatten fallback at a narrow viewport.

Existing suites needing updates: `WorkspaceDockPanel.test.ts`,
`WorkspaceDockPanel.browser.svelte.ts`, `PRListView.test.ts`,
`PRListView.workspaceDraft.test.ts`, `IssueListView.test.ts`,
`ActivityFeedView.test.ts`, and the `App.*.browser.svelte.ts` specs that assert
detail-tab chrome.

## Phasing

1. Extend `tabbed-panel-layout.ts`. Pure, unit-testable, no consumers affected.
2. `paneLayout.svelte.ts` and `DetailPaneLayout.svelte`, including the five zoom
   rules.
3. Extract the shared claim lifecycle out of `PRListView` and `IssueListView`.
4. Rewire PRs and Issues; delete `.detail-split-layout` and its keys.
5. Derive dock mode from the layout; delete `WorkspaceDockPanel` and
   `dockModes`. Highest risk.
6. Restructure Activity onto direct pane bodies; delete the double embed and
   `renderWorkspaceDock`.
7. Pane menu and palette commands.

## Deferred

- The list/rail joining the tree. The pane model must not assume all leaves are
  equal-priority, so this stays possible without being built.
- Kata, Docs, and Reviews surfaces.
- Named layout presets, mirroring the Workspaces tab's workflow presets.
- Multi-tab selection and drag (shift-click ranges) from the VS Code spec.
