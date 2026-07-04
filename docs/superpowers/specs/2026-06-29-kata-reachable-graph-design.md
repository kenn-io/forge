# Kata Reachable Graph Design

## Goal

Kata task users need a graph view for the task reachable from any task row or
task detail. The graph should use `svelte-flow`, show task titles inside nodes,
make status and priority scannable, and keep the normal task detail workflow:
clicking a graph node selects that task and shows its detail in the existing
detail pane.

The graph is an alternate primary pane for the Kata workspace. It is not part of
the task detail content.

## User Experience

Each task row exposes a compact graph icon button. The selected task detail
heading exposes the same action. Activating either action opens the reachable
graph for that source task and replaces the task list pane. The detail pane stays
mounted, so the source task remains visible until the user clicks another graph
node.

The graph pane toolbar contains:

- a back-to-list button;
- the source task title;
- a single graph filter popover summarizing the current graph depth, context
  emphasis depth, layout engine, and direction. The popover groups graph depth
  (`Full`, `1 edge`, `2 edges`, `3 edges`), context emphasis (`All`, `1 edge`,
  `2 edges`, `3 edges`), layout (`Compact`, `ELK`), direction (`Follow split`,
  `LR`, `TB`), and visibility (`Hide done`) choices so resized graph panes do
  not need to fit several standalone controls. The direction summary must
  distinguish followed and pinned state, for example `Follow TB` vs `Pinned TB`.
  `Hide done` appears in the trigger summary only when enabled so missing done
  tasks are explained without making the default trigger wider.

Each graph node contains:

- task title as the primary text;
- short id as compact metadata, never the qualified id, so node subtitles stay
  stable while graph-node selection loads task detail. The qualified id remains
  available through tooltip/accessibility metadata for disambiguation across
  duplicate titles or cross-project graphs;
- status treatment through node theming, not a visible status pill;
- a priority marker such as `P0` or `P1` when priority is set.

Open tasks use the normal active task tone. Closed tasks are muted. Closed tasks
with `closed_reason = "done"` are hidden when `Hide done` is enabled. Other
closed tasks remain visible because they can still explain the shape of the
reachable graph. The source task and currently selected detail task get distinct
outlines so users can tell the graph root from the detail selection.
Nodes adjacent to the currently selected task use relation-specific background
accents: selected task blocks peer, peer blocks selected task, child, parent, and
related each get distinct tones. Status and relation styling are layered:
status owns the left accent and done opacity, relation owns only the adjacent
background tint. When several relationships connect the selected task to the
same peer, the single adjacent tint uses this priority: peer blocks selected,
selected blocks peer, parent, child, related.

Clicking a cached graph node first emits the routed issue selection, then starts
the same local detail selection used by row clicks. This keeps the URL and
browser history ahead of the detail fetch while avoiding a URL-only graph click
when the route prop is delayed or not re-rendered. When the route prop later
catches up to the same uid, it marks the route as synced but must not start a
duplicate detail request or abort the in-flight local detail request. If the
parent never echoes the routed issue back, local selection remains valid until a
later external route update reconciles or replaces it. Browser Back/Forward
remains useful even when the clicked node's detail load is slow: a route change
back to the previous issue cancels the pending graph-node selection instead of
leaving the detail pane stuck on loading. Any late response from the abandoned
detail request is ignored and must not update the selected task, detail pane, or
graph selection after the route has moved on. When the routed selection callback
is absent, graph nodes use local selection only. If the immediate detail request
fails after the URL has already changed, the URL remains authoritative, the
normal detail request error surface is shown for that routed task, graph mode
stays open, and browser Back returns to the previous routed task.
Disabled placeholder nodes represent unresolved endpoint ids reported by the
Kata graph API and cannot be selected. Middleman does not schedule background
task-detail fetches to populate graph nodes; the native graph response is the
source of truth for reachable nodes and relationships. When a relationship peer
includes `uid`, that uid is authoritative: unresolved UID-backed peers render by
uid, and the builder must not attach the edge to another cached task just
because the short id matches.

Graph mode survives issue-only route changes so graph-node navigation can use
browser history, but closes and invalidates any graph-source detail work when
the routed view or project scope changes. A graph opened in one view/scope must
not remain visible beside another view or scoped project.

## Data Model

Kata 0.2.0 owns reachable graph traversal through
`GET /api/v1/projects/{project_id}/issues/{ref}/graph`. Middleman calls this
endpoint through the Kata proxy with `depth=full|1|2|3` and optional
`hide_done=true`, then renders the returned `nodes`, `edges`, and
`unresolved_refs`. The frontend must not rebuild reachability by walking cached
task summaries, searching short ids, or launching a separate graph-population
queue.

The graph builder receives:

- the source task uid;
- the native reachable graph response;
- the selected task uid;
- the context-emphasis filter;
- the graph layout direction.

The native graph response is authoritative for the graph node and edge set.
Depth and `hide_done` are API parameters and are the only controls that ask Kata
for a different graph. Selecting a graph node, changing context, changing
layout, or changing direction must not refetch or change the graph node set.
`Full` is the default unless a persisted user preference explicitly changes it.
Bounded depths stay rooted at the graph source task passed to the native API.

The server-provided edge direction is canonical: `parent -> child` for parent
links and `blocker -> blocked task` for blocking links. `related` edges are
server-canonical associations. The frontend dedupes exact duplicate edge ids
only; it does not invert, pair-collapse, or reinterpret relationships. The
server also marks layout-pruned edges with `layout: false`. Those edges are
still rendered and still drive selected-node adjacency/highlighting, but are
omitted from the edge set handed to Compact/ELK layout.

Context filtering is an emphasis-only operation. It only controls emphasis
distance inside the already rendered graph after the API depth and hide-done
filtering. `All` means every rendered node and edge is in-context; `1 edge`,
`2 edges`, and `3 edges` compute distance from the selected task over the
remaining visible edges. Incoming and outgoing visible edges both count as one
step for emphasis, and hidden/non-rendered tasks do not bridge context distance.
Edge emphasis is traversal-based: an edge is in-context only when the
visible-edge BFS crosses it from a node whose distance is below the selected
context depth; two in-context endpoints do not make an untraversed edge active.
Rendered nodes and edges outside that emphasis traversal are tagged as faded
context; selected-adjacent blocking edges may use the amber accent, while
non-emphasized edges use muted static styling. Context works when depth is
`Full`, so users can inspect a selected task's nearby context without reflowing
the full source graph.

The Svelte Flow node pane must be positioned above the edge pane so edges never
paint on top of task cards. Disable Svelte Flow node-focus autopan so
clicking/focusing a graph task changes selection without panning the viewport.
The graph persists the depth, context, layout, and explicit graph direction
override in a versioned, browser-profile-global localStorage value so new graph
pane sessions restore the last user layout. `layoutDirection: null` means
follow the current workspace split direction. The Direction group includes a
`Follow split` item that clears the persisted direction override back to `null`;
`LR` or `TB` means the user intentionally pinned graph direction. The trigger
summary distinguishes the two states so a matching effective direction is not
secretly pinned. Invalid or unavailable storage falls back to `Full`, `All`,
`Compact`, and the current workspace split direction.

## Component Plan

`KataWorkspace.svelte` owns graph mode:

- `listMode: "tasks" | "reachableGraph"`;
- `graphSourceIssue: KataTaskSummary | null`;
- handlers to open graph mode from list rows and detail actions;
- handler to return to task list mode.

`KataIssueList.svelte` receives an `onOpenGraph` callback and renders a graph
icon button on each task row. The row button stops propagation so opening the
graph does not also select the row unless the row was already selected.

`KataIssueDetail.svelte` receives an `onOpenGraph` callback and renders the same
graph action beside the workspace/detail actions.

`KataReachableGraph.svelte` renders the alternate pane with `@xyflow/svelte`:

- `SvelteFlow` with `nodesDraggable={false}` and `nodesConnectable={false}`;
- `fitView`, `Controls`, `MiniMap`, and `Background`;
- the graph toolbar view controls use the shared `FilterDropdown` popover pattern
  from `@middleman/ui`, not native `<select>` elements or several standalone
  controls, so one compact trigger can expose grouped depth, context, layout,
  direction, and visibility choices while following the app theme and keyboard
  behavior. This is a button-list popover, not an ARIA `menu`: the trigger
  accessible label includes the current summary and exposes expanded state, and
  active popover buttons expose pressed state in addition to the visible
  dot/check treatment. Escape and outside-click dismiss the popover; arrow-key
  menu navigation is not part of this control contract;
- a registered custom task node type that renders title, id label, status,
  priority, source and selected markers, and cached/placeholder state directly
  inside the Svelte Flow canvas;
- selected task nodes use an accent border, visible ring, and subtle selection
  tint so the active task is distinguishable from neutral open nodes,
  relation-tinted adjacent nodes, and source-only nodes;
- a real full-node button inside the custom node for pointer and keyboard
  activation. The Svelte Flow node click handler remains as a wrapper fallback
  for clicks outside the button, and button clicks stop propagation so cached-node
  selection runs exactly once;
- hidden `Handle` anchors inside the custom node so Svelte Flow can route edges
  without showing connection handles as visible UI;
- native Svelte Flow edge markers (`MarkerType.ArrowClosed`) on `markerEnd` to
  show relationship direction, rather than text labels such as `blocks`;
- relationship kind is communicated by the edge style contract and accessible
  edge label: ambient active edges use the neutral active edge color, only
  selected-adjacent blocking edges use the amber accent, related edges are
  dashed, and each edge carries a kind-specific `ariaLabel`. Do not put text
  labels on every edge in the canvas. In light mode, ambient and context edge
  tokens stay lighter than body text so dense overplotting recedes behind task
  cards while selected-adjacent edges remain legible.
- bounded-depth context edges use a muted static stroke and marker color,
  leaving selected-adjacent edges above them;
- node accessible labels include source/selected state, title, qualified id,
  cached status, and adjacent relationship state so the visible short-id
  subtitle is not the only disambiguator;
- themed `Controls` and `MiniMap` chrome; MiniMap node colors come from the
  documented `nodeColor`/`nodeStrokeColor` callbacks;
- `onnodeclick` to select cached nodes for pointer activation.

The graph toolbar must remain usable inside the resized list pane. In narrow
side-by-side panes, keep the back button and source title on the first row and
keep the single graph filter trigger visible on the controls row instead of
clipping or hiding depth, context, layout, direction, or done-filter controls.

A pure `kataReachableGraph.ts` module builds nodes and edges. It performs the
depth-limited reachability traversal, creates marker-backed edges, and computes
the default compact directed layout with a deterministic topological ranker.
Compact is the default because it keeps large task graphs dense enough for the
Kata workspace canvas. The compact layout direction follows the Kata split
presentation: side-by-side panes use left-to-right ranks, while stacked panes
use top-to-bottom ranks with source/target handles on the bottom/top edges of
each node.

`KataReachableGraph.svelte` adds grouped graph filter popover controls for
`Compact`/`ELK` layout and `Follow split`/`LR`/`TB` graph direction. The split
presentation still provides the default direction, but the user can override the
graph itself between left-to-right and top-to-bottom without changing the
workspace pane layout. `Follow split` clears the saved override and resumes
tracking pane orientation; choosing `LR` or `TB` saves an explicit pinned graph
direction across graph pane remounts.
`ELK` delegates node placement to `elkjs` using the active graph
direction, based on the final visible edge set after reciprocal edge
deduplication and done filtering. ELK layout is asynchronous, so the graph renders compact
positions until ELK returns, then stores only ELK coordinates if the graph
signature still matches the active layout request. Node title, status,
selection, and relation data always come from the latest graph nodes; cached
ELK output must never hold an older node object alive.
Nodes include explicit Svelte Flow width/height values so edge endpoints are
based on stable bounds instead of shifting after custom node measurement.
ELK node coordinates are used directly; do not post-process those coordinates
in the Svelte layer, because that breaks the relationship between ELK placement
and Svelte Flow's edge routing.
There is no duplicate card/button list below the canvas.

Graph mode owns a source task detail snapshot. When launched from detail it
uses the already selected source detail; when launched from an unselected list
row it fetches the source detail in the background without changing the current
detail selection. Once loaded, source-only `KataTaskDetail.links` remain in the
full graph after the user selects a different reachable node. The currently
selected detail task still controls the right-hand task detail pane.

The `window.__middleman_kata_graph_debug` bridge is a test/debug affordance, not
a supported product API. Tests should call `reset()` before assertions, and the
graph component clears the bridge on unmount so stale node/event snapshots do not
leak across graph sessions.

## Error And Empty States

If graph mode opens before the source task exists in cache, show an empty graph
pane with a back-to-list action. If the source exists but has no reachable peers,
show a single source node.

The graph surfaces native graph request errors in the graph pane without
replacing the normal task-detail request error path. If a previous graph response
is already visible, keep it visible and show a non-blocking graph alert for the
failed refresh. ELK layout failures are debug-only: keep or fall back to compact
coordinates, record a graph layout error event, and do not show a blocking
user-facing error for layout failure alone.

## Testing

Add unit tests for the graph builder:

- source-only graph;
- rendering nodes and server-provided `parent`, `blocks`, and `related` edges;
- exact duplicate edge-id dedupe without inverse edge reinterpretation;
- `layout: false` edges remain rendered but are omitted from layout edges;
- unresolved refs render disabled UID-backed placeholders without short-id
  matching;
- priority and status node metadata;
- graph node subtitles use the short task id even when a qualified id is
  available;
- empty graph response handling;
- graph context filters only change emphasis at 1, 2, and 3 selected-task
  edges; `All` leaves every rendered node and edge in context.
- context changes and graph-node selection keep the same node set,
  layout edges, and positions while moving only active/faded classification and
  selected-adjacent highlighting.

Add Svelte tests for workspace integration:

- clicking a task row graph button replaces the task list pane with the graph;
- clicking back restores the task list;
- graph nodes display task titles and priority markers;
- clicking a cached graph node selects the task and updates the detail pane;
- pressing Enter/Space on a focused graph task node selects the task;
- graph loading and graph request errors are reflected in the graph pane;
- selected graph nodes do not trigger a new graph request when depth, hide-done,
  source, and daemon are unchanged;
- adjacent graph nodes are themed by their relationship direction to the
  selected task;
- adjacent relation backgrounds do not overwrite status accents;
- `Hide done` removes done nodes.
- context keeps out-of-emphasis nodes visible but faded and renders context
  edges underneath emphasized selected-adjacent edges.
- browser coverage verifies nonblank canvas nodes, hidden handles, native edge
  markers, themed controls/minimap, the graph filter popover, the Compact/ELK
  layout choice, the Follow/LR/TB direction choice, localStorage restoration for
  graph controls, clearing a pinned graph direction back to follow-split mode,
  `Hide done` trigger summary state, and the absence of a duplicate node-list
  fallback. It also covers both Enter and Space keyboard activation.
- full-stack e2e coverage opens graph mode from the workspace, selects a cached
  graph node, confirms detail selection changes, verifies the source graph
  remains visible/stable after selection, and returns to the task list.
- full-stack e2e coverage switches active depth and context through the real
  workspace graph path and verifies context remains emphasis-only by fading
  visible out-of-context edges without changing node ids, positions, or layout
  edge count, while depth is the API-backed control that changes the node set.
- full-stack e2e coverage verifies linked graph nodes arrive from the native
  `/graph` endpoint without a graph-time per-node detail fetch.
- full-stack e2e coverage verifies rendered edge count can stay larger than
  layout edge count when transitive block edges are elided only for layout.
- full-stack e2e coverage stalls a clicked graph-node detail request, verifies
  the URL changes immediately, verifies same-UID route catch-up does not abort or
  duplicate that request, uses browser Back to restore the previous issue, and
  verifies the late response is ignored.
- full-stack e2e coverage opens the graph through the real workspace, verifies
  stacked panes produce top-to-bottom graph direction, toggles to side-by-side
  layout, verifies left-to-right graph direction, and asserts graph toolbar
  filters remain visible without horizontal overflow in the narrowed list pane.

## Dependencies

Add `@xyflow/svelte` and `elkjs` to `frontend/package.json` using `bun install`
so the Bun lockfile remains authoritative.
