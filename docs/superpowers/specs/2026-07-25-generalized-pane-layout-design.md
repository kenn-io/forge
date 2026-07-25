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

`WorkflowSplitTree.svelte` is a thin adapter over it and the only *product*
consumer. It is not the only consumer: the shipped `/design-system` page mounts
`DesignSystemTabbedPanelDemo.svelte` with its own harness and a
`DesignSystemPanel.browser.svelte.ts` spec. Any change to the component API or to
`renderTab` semantics has to keep that surface and its browser spec working.
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

- In: PRs, Issues, Activity. The list/rail stays fixed; only the content beside
  it becomes a pane tree. PRs and Issues use `CollapsibleSidebar`; **Activity
  does not** — it owns a bespoke rail with its own `middleman-activity-pane-width`
  key, collapse state, `minDetailPaneWidth` bound, direct `SplitResizeHandle`,
  and Escape-to-close handler (`ActivityFeedView.svelte`). All of that must
  survive untouched; the three rails are not one shared shell.
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
  `parseTerminalLayout`'s existing discipline. "Validating" specifically includes
  **globally unique tab keys and node IDs**: `normalizeTabbedPanelTree`
  deduplicates only within a leaf, and for a singleton portal that is not enough
  — a workspace tab appearing in two leaves would register two slot elements for
  one surface, and duplicate leaf or split IDs would make an edit-by-ID land in
  several nodes at once.
- `zoomedLeafID: string | null` as a sibling field of `tree`, not a node
  property. Zoom is view state over a tree, not a shape of the tree.
- **Intent tree vs render tree.** The persisted tree retains every known tab
  including currently unavailable ones (no claimed workspace → no workspace
  tab). Rendering prunes against the available set. Edits apply to the intent
  tree by leaf ID, so a workspace pane dragged to the right returns to the right
  when it reappears. No placement heuristics.

  Two limits on this, both load-bearing:

  **Leaf IDs survive pruning only for surviving leaves.** A leaf whose every tab
  is unavailable returns `null` and its parent split is replaced by the
  surviving child (`pruneTabbedPanelNode`). Those leaf and split IDs exist only
  in the intent tree and are not addressable from the render tree. Any command
  originating from a synthetic or flattened render leaf therefore needs an
  explicit mapping, not a bare ID lookup.

  **Ratio edits use the trivial projection, deliberately.** A resize writes the
  rendered split's own ID straight into the intent tree, unchanged. For intent
  `S0(A, S1(B, C))` with `B` unavailable, the render tree is `S0(A, C)`;
  dragging to 70/30 writes `S0 = 0.7`, which renders exactly what the user set.
  When `B` returns, `A` keeps 70% and `B`/`C` share 30%. That is correct rather
  than a defect: `B` must take its space from somewhere, and its own subtree's
  share is the right place. Do not build a projection layer that reattributes
  resizes to intent-tree descendants. Splits existing only in intent keep stale
  ratios until they next render, which is harmless.
- `flattenTabbedPanelTree(tree)` → single leaf preserving tab order and the
  active tab.

### Persistence

One layout per top-level mode, keyed `middleman-pane-layout-v1:<surface>` in
localStorage, where surface is `prs` | `issues` | `activity`. Selecting a
different item never changes the layout.

### Drag scope

A PR pane cannot be dropped into the Workspaces tree because the drag payload is
rejected by scope, so the "top-level tabs are sacrosanct" rule is enforced
mechanically rather than by convention. Scope comparison is plain string equality
(`tabbed-panel-drag.ts`) and the Workspaces tree passes a raw `workspaceId`, so
scopes are namespaced — `detail:prs`, `detail:issues`, `detail:activity`,
`workspace:<id>` — rather than using bare surface keys that a workspace ID could
in principle collide with.

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

`WorkspaceDockPanel` carries behaviors that must be ported into
`paneLayout.svelte.ts`, not rediscovered later:

1. A claim ending while zoomed un-zooms and returns focus to the detail pane.
2. A panel unmounting while zoomed un-zooms, so the next item cannot open
   hidden behind a full-screen pane.
3. A selection change from item A to item B while zoomed un-zooms, including
   the no-observable-gap case `setClaim` handles today (a reclaim landing in the
   same update as the release).
4. A closing pane reclaims focus only when focus was inside the closing subtree
   or already on `<body>` — never stolen from a control the user moved to.
5. A **same-identity** claim re-assert must NOT un-zoom; only an identity change
   does. `setClaim` guards this with `!sameIdentity(...)`
   (`workspace-host.svelte.ts:189`) because a mere ref status change on the same
   workspace would otherwise collapse an expanded dock. An effect keyed on the
   claim object rather than the identity reintroduces that bug.
6. While modal stack depth is greater than zero, `focusTerminal()` is a total
   no-op — it does not touch layout and does not move focus
   (`workspace-host.svelte.ts:400`). "Refuse to zoom" is too narrow: the
   activate-tab-and-focus-host path would otherwise pull focus out of an open
   dialog.

Rules 1 to 3 are one invariant: a zoom must not outlive what was zoomed. Rule 5
is its necessary limit — an over-eager reading of 1 to 3 breaks it.

## Route and active tab

PRs mode has `/pulls/.../files` as a route, so "which of conversation/files is
active" is both URL state and layout state. Rule:

- While conversation and files share a leaf, the route and that leaf's
  `activeTabKey` are a two-way binding, as today.
- Once they are split into separate leaves both are visible; the route follows
  whichever of the two was most recently focused.
- A deep link activates the pane it names, and un-zooms if that pane is hidden
  by a zoom on another leaf.

The binding needs an explicit authority and history policy, because `navigate()`
pushes history and emits host navigation events while `replaceUrl()` does not.
Rules: a tab *click* keeps today's `navigate()` semantics; a focus-derived route
update while the panes are split apart uses `replaceUrl()`, so moving between two
simultaneously visible panes does not fill the Back stack. Route-originated
changes must not echo back as focus-originated ones. `TabbedPanelTree` emits tab
selection but no pane-body focus event today, so tracking "most recently focused
of the two" requires adding one — scrolling and programmatic review-thread
navigation must not count as focus changes.

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

`dockModes` in `workspace-host.svelte.ts` is deleted, but **not all three modes
can be derived from the tree.** An earlier draft of this spec claimed
`collapsed` = "the workspace tab is not the active tab of its leaf". That is
unrepresentable in the default layout: when the workspace tab is the only tab in
its leaf, `createTabbedPanelLeaf` guarantees it is the active tab, and
`TabbedPanelTree` renders the active panel visible. Today collapse keeps the
claim while hiding the host, and there is no way to express that by tab
activity alone. Removing the tab from availability instead would contradict
"workspace requires a claim" and lose the claimed-but-hidden state; moving it to
another leaf would destroy its persisted placement.

So the layout state carries an explicit `collapsedLeafIDs: string[]` alongside
`tree` and `zoomedLeafID`. Mode then derives as:

- `expanded` — the workspace tab's leaf is the zoomed leaf.
- `collapsed` — that leaf is in `collapsedLeafIDs`, or the workspace tab is
  absent from the render tree.
- `split` — otherwise.

A collapsed leaf renders as a reopen strip, matching today's
`workspace-dock-reopenstrip`.

**A collapsed workspace leaf must unmount its slot element, not merely hide
it.** Today `{#if dockOpen}` unmounts the dock subtree
(`WorkspaceDockPanel.svelte:182`), so reopening registers a *new* slot element,
which is what makes `WorkspaceHost`'s placement effect rerun and consume
`pendingHostFocus`. If the slot stays mounted and only its visibility changes,
slot identity never changes, placement never reruns, the pending focus is never
consumed, and the terminal silently fails to focus — and worse, the stale flag
can later be consumed by an unrelated reveal after a surface switch, stealing
focus. Effective visibility gating `isHostVisible()` is therefore necessary but
not sufficient: reveal must remain a slot-identity transition.

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
covers the phone and focus presentations, which pass `hideSidebar` and have no
room for splits. Ratio clamps stay at 0.12–0.88; the flatten threshold, not a
per-leaf pixel minimum, is what prevents unusable narrow panes.

The `embed-workspace-detail` route is **not** covered by this: it renders
`WorkspaceRightSidebar` directly (`WorkspaceEmbedShell.svelte:218`) rather than
going through a list view, so it is unaffected either way. An earlier draft
lumped it in with the `hideSidebar` hosts, which was wrong.

**In flattened mode every structural edit is disabled** — no split or maximize
controls, no drag at all, not even reordering within the flat strip: tab
switching only. Reordering is not exempt, because a flat leaf's neighbours can
come from different intent leaves, so a local-looking swap moves a tab between
desktop panes. Mechanically this needs no new prop: `TabbedPanelTree` already
treats an omitted mutation callback as making that interaction read-only, so the
flat renderer passes `onSelectTab` and nothing else. A flat leaf
merges tabs drawn from several intent leaves, so its ID is either synthetic
(making leaf-targeted operations silently no-op) or borrowed from one intent leaf
(targeting the wrong one). Reordering two flattened neighbours that live in
different intent leaves would move a tab between desktop panes the user cannot
see. `context/mobile-ux.md` independently rejects desktop split-pane controls on
phones. Note this also overrides the single-tab rule for the split buttons: a
flat leaf usually holds several tabs, so "disabled only on a single-tab leaf"
would leave them enabled where the renderer cannot show a split.

## Discoverability

Dragging is not discoverable and the "Split view" toggle is being deleted.
Replacements:

- A per-leaf icon cluster, right-aligned in the tab strip. Not a dropdown: it
  borrows the visual idiom of the existing `tabbed-panel-tab-tool` buttons that
  per-tab rename, move, and close use — 20x22px icon-only buttons, hidden until
  the leaf is hovered or a button takes focus. Icon-only requires `title` plus
  `aria-label` on every button, as `WorkflowSplitTree.svelte` already does.

  This needs a **new `leafActions` snippet** on `TabbedPanelTree`. The existing
  `tabActions` snippet receives only a tab descriptor and is rendered inside
  every individual tab, so it can supply neither the enclosing leaf ID that split
  and maximize need nor a single right-aligned cluster per leaf. Without the new
  snippet the controls would be duplicated per tab or resolved by brittle tree
  lookups.
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

**Accepted accessibility scope: rearrangement is pointer-or-palette only.**
`context/ui-design-system.md` records that keyboard tab reordering, keyboard
splitting, and keyboard resizing are not implemented in `TabbedPanelTree`, and
this work does not add them. The palette covers split and maximize, so every
rearrangement except drag-reorder and divider-drag has a keyboard path; resize
retains the existing labelled `SplitResizeHandle`. Extending the primitive with
real keyboard reordering is deferred, and no test in this work should claim to
cover it.

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
- **Browser tier:** drag between leaves, edge-split drop, and native focus
  restoration on pane close. Note `packages/ui/src/.../TabbedPanelTree.test.ts`
  is a **jsdom** spec, not a browser one — the browser project includes only
  `frontend/src/**/*.browser.svelte.ts` (`frontend/vite.config.ts:214`), so a
  browser spec for a `packages/ui` component cannot sit beside it and must live
  under `frontend/src`. Extend `WorkspaceDockPanel.browser.svelte.ts` and
  `DesignSystemPanel.browser.svelte.ts` rather than adding a third harness. Do
  not duplicate coverage the workspace tree already has.
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
