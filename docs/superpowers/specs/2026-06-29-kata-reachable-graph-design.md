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
- a depth filter with `Full`, `1 edge`, `2 edges`, and `3 edges` options;
- a `Hide done` toggle.

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

Clicking a cached node calls the existing `selectIssue(uid)` flow. Disabled
placeholder nodes represent uncached linked peers and cannot be selected. If a
placeholder came from a link that included a peer uid, graph mode schedules a
background detail fetch for that uid and replaces the placeholder once the
workspace cache receives the task. If only a short id is known, graph mode may
search exactly within the peer project and cache exact matches. Background graph
population failures do not replace the normal detail/request error surface; the
placeholder remains visible. Graph population is queued through the workspace
store and must pause/abort while a user-driven task detail selection is active;
there should not be a graph-side issue refresh competing with the selected-task
detail refresh. UID-backed placeholders use the final task uid as their Svelte
Flow node id so the node updates in place when cached data arrives.
When a relationship peer includes `uid`, that uid is authoritative: missing
UID-backed peers render and fetch by uid, and the builder must not attach the
edge to another cached task just because the short id matches.

## Data Model

The graph uses a local frontend cache plus queued background population for
visible missing references. The component itself does not call the Kata daemon.

`KataWorkspaceStore` keeps a `Map<string, KataTaskSummary>` keyed by `uid`. The
cache is populated from:

- current view and search results;
- selected task detail;
- selected detail children;
- graph-triggered background loads for uncached relationship references;
- child rows loaded by list expansion;
- task mutation responses and event-driven refreshes.

The graph builder receives:

- the source task uid;
- cached task summaries;
- the currently selected task detail, when available;
- the done-filter flag;
- the graph depth filter.

Reachability is computed by walking cached relationships:

- parent edges from `parent_short_id`;
- child edges by matching another cached task's `parent_short_id`;
- blocking edges from `blocks` and `blocked_by`;
- related edges from `related`;
- detail edges from `KataTaskDetail.links` for the selected/source task.

Reciprocal declarations between the same two tasks collapse to one edge by
edge kind plus unordered node pair. This applies to summary-derived edges and
source-detail `KataTaskDetail.links` edges for `parent`, `blocks`, and
`related`. The graph should not render parallel inverse arrows for the same
relationship kind.

When duplicate inverse edges conflict, the first observed directed edge wins.
Cached summary traversal is processed before source-detail links, and
source-detail link order is preserved. This keeps the edge arrow,
`ariaLabel`, and selected-node adjacent relation stable: a cached `blocks`
edge from source to peer remains "source blocks peer" even if detail links also
contain the inverse. Detail-only reciprocal links keep the first detail-link
direction.

Relationship matching prefers `uid`. When only `short_id` is available, the
builder resolves it inside the same project. Ambiguous short ids do not select a
random task; they render an uncached placeholder only when the peer id can still
be displayed. The pure builder returns unresolved peer references alongside the
nodes and edges so `KataWorkspaceStore` can populate missing cached data without
making the graph component own daemon calls.

The depth filter is applied during traversal, not after rendering. `Full` is
the default unless a later persisted user preference explicitly changes it.
`1 edge` includes only nodes and edges directly connected to the source; `2
edges` and `3 edges` expand that many relationship hops; `Full` expands the
reachable closure from the current cache/background loads. Missing refs outside
the selected depth are not requested until the user widens the depth.

Depth changes only affect the current graph render and newly reported missing
refs. Narrowing the graph depth does not cancel a store-owned graph fetch
already in flight, and a completed fetch may still enter the workspace cache
even if the ref is no longer visible. Re-rendering at the narrower depth stops
enqueueing newly hidden refs; widening depth later reports those refs again and
lets the single store graph-load queue populate them.

## Component Plan

`KataWorkspace.svelte` owns graph mode:

- `listMode: "tasks" | "reachableGraph"`;
- `graphSourceUID: string | null`;
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
- a registered custom task node type that renders title, id label, status,
  priority, source and selected markers, and cached/placeholder state directly
  inside the Svelte Flow canvas;
- a real full-node button inside the custom node as the single activation
  target for keyboard users. Pointer activation is delegated to the Svelte Flow
  node click handler. Both paths call the same cached-node selection handler
  exactly once;
- hidden `Handle` anchors inside the custom node so Svelte Flow can route edges
  without showing connection handles as visible UI;
- native Svelte Flow edge markers (`MarkerType.ArrowClosed`) on `markerEnd` to
  show relationship direction, rather than text labels such as `blocks`;
- relationship kind is communicated by the edge style contract and accessible
  edge label: blocking edges use the primary accent, parent edges use secondary
  text color, related edges are dashed, and each edge carries a kind-specific
  `ariaLabel`. Do not put text labels on every edge in the canvas.
- node accessible labels include source/selected state, title, qualified id,
  cached status, and adjacent relationship state so the visible short-id
  subtitle is not the only disambiguator;
- themed `Controls` and `MiniMap` chrome; MiniMap node colors come from the
  documented `nodeColor`/`nodeStrokeColor` callbacks;
- `onnodeclick` to select cached nodes for pointer activation.

A pure `kataReachableGraph.ts` module builds nodes and edges. It performs the
depth-limited reachability traversal, creates marker-backed edges, and assigns
stable layered positions so tests can assert the graph without depending on
browser layout.
Nodes include explicit Svelte Flow width/height values so edge endpoints are
based on stable bounds instead of shifting after custom node measurement.
There is no duplicate card/button list below the canvas.

Graph mode snapshots the source task detail when launched from detail so
source-only `KataTaskDetail.links` remain in the graph after the user selects a
different reachable node. The currently selected detail task still controls the
right-hand task detail pane.

The `window.__middleman_kata_graph_debug` bridge is a test/debug affordance, not
a supported product API. Tests should call `reset()` before assertions, and the
graph component clears the bridge on unmount so stale node/event snapshots do not
leak across graph sessions.

## Error And Empty States

If graph mode opens before the source task exists in cache, show an empty graph
pane with a back-to-list action. If the source exists but has no reachable peers,
show a single source node.

The graph does not surface daemon errors directly. It reflects whatever the
workspace cache already knows. Normal detail selection errors continue to use the
existing request error path.

## Testing

Add unit tests for the graph builder:

- source-only graph;
- parent and child reachability;
- blocks, blocked-by, and related reachability;
- inverse parent/child and blocks/blocked_by declarations dedupe to one edge;
- summary/detail reciprocal edges dedupe through the same edge path;
- detail-only reciprocal edges keep the first detail-link direction;
- UID-backed reverse edges do not fall back through cached short-id matches;
- placeholder peer handling;
- done filtering;
- priority and status node metadata;
- graph node subtitles use the short task id even when a qualified id is
  available;
- no ambiguous short-id random matching.
- unresolved graph peer references are returned for background fetching.
- UID-backed missing peers do not fall back to cached short-id matches.
- graph depth filters prune traversal at 1, 2, and 3 edges.

Add Svelte tests for workspace integration:

- clicking a task row graph button replaces the task list pane with the graph;
- clicking back restores the task list;
- graph nodes display task titles and priority markers;
- clicking a cached graph node selects the task and updates the detail pane;
- pressing Enter/Space on a focused graph task node selects the task;
- selecting a detail-only linked node does not remove it from the graph;
- uncached graph references with peer uids trigger background detail fetches and
  replace disabled placeholders once cached;
- adjacent graph nodes are themed by their relationship direction to the
  selected task;
- adjacent relation backgrounds do not overwrite status accents;
- `Hide done` removes done nodes.
- changing graph depth filters rendered nodes.
- graph depth filters suppress out-of-depth missing refs until widening the
  graph depth exposes them.
- browser coverage verifies nonblank canvas nodes, hidden handles, native edge
  markers, themed controls/minimap, and the absence of a duplicate node-list
  fallback. It also covers both Enter and Space keyboard activation.
- full-stack e2e coverage opens graph mode from the workspace, selects a cached
  graph node, confirms detail selection changes, verifies the source graph
  remains visible/stable after selection, exercises uncached UID-backed graph
  population through the real proxy path, and returns to the task list.

## Dependencies

Add `@xyflow/svelte` to `frontend/package.json` using `bun install` so the Bun
lockfile remains authoritative.
