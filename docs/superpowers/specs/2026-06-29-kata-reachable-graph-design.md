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
- the source task short id and title;
- a `Hide done` toggle.

Each graph node contains:

- task title as the primary text;
- short or qualified id as compact metadata;
- project name when useful for disambiguation;
- a status treatment;
- a priority marker such as `P0` or `P1` when priority is set.

Open tasks use the normal active task tone. Closed tasks are muted. Closed tasks
with `closed_reason = "done"` are hidden when `Hide done` is enabled. Other
closed tasks remain visible because they can still explain the shape of the
reachable graph. The source task and currently selected detail task get distinct
outlines so users can tell the graph root from the detail selection.

Clicking a cached node calls the existing `selectIssue(uid)` flow. Disabled
placeholder nodes represent uncached linked peers and cannot be selected.

## Data Model

The graph uses a local frontend cache rather than recursively fetching from the
Kata daemon.

`KataWorkspaceStore` keeps a `Map<string, KataTaskSummary>` keyed by `uid`. The
cache is populated from:

- current view and search results;
- selected task detail;
- selected detail children;
- child rows loaded by list expansion;
- task mutation responses and event-driven refreshes.

The graph builder receives:

- the source task uid;
- cached task summaries;
- the currently selected task detail, when available;
- the done-filter flag.

Reachability is computed by walking cached relationships:

- parent edges from `parent_short_id`;
- child edges by matching another cached task's `parent_short_id`;
- blocking edges from `blocks` and `blocked_by`;
- related edges from `related`;
- detail edges from `KataTaskDetail.links` for the selected/source task.

Relationship matching prefers `uid`. When only `short_id` is available, the
builder resolves it inside the same project. Ambiguous short ids do not select a
random task; they render an uncached placeholder only when the peer id can still
be displayed.

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
  priority, project/id metadata, source and selected markers, and
  cached/placeholder state directly inside the Svelte Flow canvas;
- a real full-node button inside the custom node so pointer users click the
  canvas node and keyboard users activate the same target with Enter/Space;
- hidden `Handle` anchors inside the custom node so Svelte Flow can route edges
  without showing connection handles as visible UI;
- native Svelte Flow edge markers (`MarkerType.ArrowClosed`) on `markerEnd` to
  show relationship direction, rather than text labels such as `blocks`;
- relationship kind is communicated by the edge style contract and accessible
  edge label: blocking edges use the primary accent, parent edges use secondary
  text color, related edges are dashed, and each edge carries a kind-specific
  `ariaLabel`. Do not put text labels on every edge in the canvas.
- themed `Controls` and `MiniMap` chrome; MiniMap node colors come from the
  documented `nodeColor`/`nodeStrokeColor` callbacks;
- `onnodeclick` to select cached nodes.

A pure `kataReachableGraph.ts` module builds nodes and edges. It performs the
reachability traversal, creates marker-backed edges, and assigns stable layered
positions so tests can assert the graph without depending on browser layout.
Nodes include explicit Svelte Flow width/height values so edge endpoints are
based on stable bounds instead of shifting after custom node measurement.
There is no duplicate card/button list below the canvas; the Svelte Flow node
wrappers are the authoritative click targets.

Graph mode snapshots the source task detail when launched from detail so
source-only `KataTaskDetail.links` remain in the graph after the user selects a
different reachable node. The currently selected detail task still controls the
right-hand task detail pane.

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
- placeholder peer handling;
- done filtering;
- priority and status node metadata;
- no ambiguous short-id random matching.

Add Svelte tests for workspace integration:

- clicking a task row graph button replaces the task list pane with the graph;
- clicking back restores the task list;
- graph nodes display task titles and priority markers;
- clicking a cached graph node selects the task and updates the detail pane;
- pressing Enter/Space on a focused graph task node selects the task;
- selecting a detail-only linked node does not remove it from the graph;
- `Hide done` removes done nodes.
- browser coverage verifies nonblank canvas nodes, hidden handles, native edge
  markers, themed controls/minimap, and the absence of a duplicate node-list
  fallback.

## Dependencies

Add `@xyflow/svelte` to `frontend/package.json` using `bun install` so the Bun
lockfile remains authoritative.
