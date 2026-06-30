<script lang="ts">
  import ELK, { type ElkNode } from "elkjs/lib/elk.bundled.js";
  import ArrowLeftIcon from "@lucide/svelte/icons/arrow-left";
  import { SelectDropdown, type SelectDropdownOption } from "@middleman/ui";
  import {
    Background,
    BackgroundVariant,
    Controls,
    MiniMap,
    SvelteFlow,
    type Node as SvelteFlowNode,
    type NodeTypes,
  } from "@xyflow/svelte";
  import "@xyflow/svelte/dist/style.css";

  import type { KataTaskDetail, KataTaskSummary } from "../../api/kata/taskTypes.js";
  import {
    recordKataGraphDebugEvent,
    resetKataGraphDebug,
    setKataGraphDebugGraph,
  } from "../../stores/kata-graph-debug.js";
  import KataGraphTaskNode from "./KataGraphTaskNode.svelte";
  import {
    buildKataReachableGraph,
    type KataGraphDepthLimit,
    type KataGraphEdge,
    type KataGraphMissingRef,
    type KataGraphNode,
  } from "./kataReachableGraph.js";

  type KataGraphLayoutMode = "compact" | "elk";

  interface Props {
    sourceUID: string;
    selectedUID: string | null;
    tasks: readonly KataTaskSummary[];
    selectedDetail?: KataTaskDetail | null | undefined;
    onBack: () => void;
    onSelectIssue: (uid: string) => void;
    onRequestMissingTasks?: ((refs: readonly KataGraphMissingRef[]) => void) | undefined;
  }

  let {
    sourceUID,
    selectedUID,
    tasks,
    selectedDetail = null,
    onBack,
    onSelectIssue,
    onRequestMissingTasks = undefined,
  }: Props = $props();

  let hideDone = $state(false);
  let depthLimit = $state<KataGraphDepthLimit>("full");
  let layoutMode = $state<KataGraphLayoutMode>("compact");
  let layoutedNodes = $state.raw<KataGraphNode[]>([]);
  let layoutedKey = $state("");
  let graph = $derived(
    buildKataReachableGraph({ sourceUID, selectedUID, tasks, selectedDetail, hideDone, depthLimit }),
  );
  let graphSignature = $derived(graphLayoutSignature(graph.nodes, graph.edges));
  let activeLayoutKey = $derived(`${layoutMode}:${graphSignature}`);
  let flowNodes = $derived(layoutedKey === activeLayoutKey ? layoutedNodes : graph.nodes);
  let interactiveNodes = $derived(flowNodes.map((node) => withNodeActivation(node)));
  let layoutReady = $derived(layoutMode === "compact" || layoutedKey === activeLayoutKey);
  let source = $derived(tasks.find((task) => task.uid === sourceUID));
  let layoutRun = 0;
  const elk = new ELK({
    defaultLayoutOptions: {
      "elk.algorithm": "layered",
      "elk.direction": "RIGHT",
      "elk.edgeRouting": "ORTHOGONAL",
      "elk.spacing.nodeNode": "24",
      "elk.layered.spacing.nodeNodeBetweenLayers": "72",
      "elk.layered.spacing.edgeNodeBetweenLayers": "14",
    },
  });
  const nodeTypes: NodeTypes = {
    kataTask: KataGraphTaskNode,
  };
  const depthOptions: SelectDropdownOption[] = [
    { value: "full", label: "Full" },
    { value: "1", label: "1 edge" },
    { value: "2", label: "2 edges" },
    { value: "3", label: "3 edges" },
  ];
  const layoutOptions: SelectDropdownOption[] = [
    { value: "compact", label: "Compact" },
    { value: "elk", label: "ELK" },
  ];
  const fitViewOptions = {
    duration: 0,
    padding: 0.12,
  };

  function missingRefKey(ref: KataGraphMissingRef): string {
    return ref.uid ? `uid:${ref.uid}` : `short:${ref.projectUID}:${ref.shortID}`;
  }

  function selectNodeID(uid: string): void {
    onSelectIssue(uid);
  }

  function selectNode(node: KataGraphNode): void {
    if (!node.data.selectable) return;
    selectNodeID(node.id);
  }

  function withNodeActivation(node: KataGraphNode): KataGraphNode {
    return {
      ...node,
      data: {
        ...node.data,
        onSelect: selectNodeID,
      },
    };
  }

  function setDepthLimit(value: string): void {
    depthLimit = value as KataGraphDepthLimit;
  }

  function setLayoutMode(value: string): void {
    layoutMode = value as KataGraphLayoutMode;
  }

  function graphLayoutSignature(nodes: readonly KataGraphNode[], edges: readonly KataGraphEdge[]): string {
    return JSON.stringify({
      nodes: nodes.map((node) => node.id),
      edges: edges.map((edge) => [edge.id, edge.source, edge.target]),
    });
  }

  function elkGraph(nodes: readonly KataGraphNode[], edges: readonly KataGraphEdge[]): ElkNode {
    const nodeIDs = new Set(nodes.map((node) => node.id));
    return {
      id: "kata-reachable-graph",
      children: nodes.map((node) => ({
        id: node.id,
        width: node.width ?? 250,
        height: node.height ?? 74,
      })),
      edges: edges
        .filter((edge) => nodeIDs.has(edge.source) && nodeIDs.has(edge.target))
        .map((edge) => ({
          id: edge.id,
          sources: [edge.source],
          targets: [edge.target],
        })),
    };
  }

  function applyElkPositions(nodes: readonly KataGraphNode[], layoutedGraph: ElkNode): KataGraphNode[] {
    const positions = new Map((layoutedGraph.children ?? []).map((node) => [node.id, { x: node.x ?? 0, y: node.y ?? 0 }]));
    return nodes.map((node) => ({
      ...node,
      position: positions.get(node.id) ?? node.position,
    }));
  }

  function minimapData(node: SvelteFlowNode): Partial<KataGraphNode["data"]> {
    return node.data as Partial<KataGraphNode["data"]>;
  }

  function minimapNodeColor(node: SvelteFlowNode): string {
    const data = minimapData(node);
    if (data.status === "closed" && data.closedReason === "done") return "var(--text-muted)";
    if (data.status === "uncached") return "var(--bg-surface-hover)";
    if (data.isSource || data.isSelected) return "var(--accent-blue)";
    return "var(--bg-surface)";
  }

  function minimapNodeStrokeColor(node: SvelteFlowNode): string {
    const data = minimapData(node);
    if (data.isSource || data.isSelected) return "var(--accent-blue)";
    return "var(--border-default)";
  }

  $effect(() => {
    const refs = graph.missingRefs;
    if (refs.length === 0) return;
    recordKataGraphDebugEvent("graph-missing-refs", { keys: refs.map(missingRefKey) });
    onRequestMissingTasks?.(refs);
  });

  $effect(() => {
    const key = activeLayoutKey;
    const nodes = graph.nodes;
    const edges = graph.edges;
    const run = ++layoutRun;
    if (layoutMode === "compact" || nodes.length === 0) {
      layoutedNodes = nodes;
      layoutedKey = key;
      return;
    }

    recordKataGraphDebugEvent("graph-layout-start", { layoutMode, nodeCount: nodes.length, edgeCount: edges.length });
    elk
      .layout(elkGraph(nodes, edges))
      .then((layoutedGraph) => {
        if (run !== layoutRun) return;
        layoutedNodes = applyElkPositions(nodes, layoutedGraph);
        layoutedKey = key;
        recordKataGraphDebugEvent("graph-layout-complete", {
          layoutMode,
          nodeCount: layoutedNodes.length,
          edgeCount: edges.length,
        });
      })
      .catch((error: unknown) => {
        if (run !== layoutRun) return;
        layoutedNodes = nodes;
        layoutedKey = key;
        recordKataGraphDebugEvent("graph-layout-error", {
          layoutMode,
          message: error instanceof Error ? error.message : String(error),
        });
      });
  });

  $effect(() => {
    const snapshot = {
      sourceUID,
      selectedUID,
      hideDone,
      depthLimit,
      layoutMode,
      layoutReady,
      nodeIds: flowNodes.map((node) => node.id),
      edges: graph.edges.map((edge) => ({
        id: edge.id,
        source: edge.source,
        target: edge.target,
        kind: typeof edge.data?.kind === "string" ? edge.data.kind : null,
      })),
      nodePositions: flowNodes.map((node) => ({ id: node.id, x: node.position.x, y: node.position.y })),
      disabledNodeIds: flowNodes.filter((node) => !node.data.selectable).map((node) => node.id),
      missingRefKeys: graph.missingRefs.map(missingRefKey),
      nodeCount: flowNodes.length,
      edgeCount: graph.edges.length,
    };
    setKataGraphDebugGraph(snapshot);
    recordKataGraphDebugEvent("graph-render", snapshot);
  });

  $effect(() => {
    return () => resetKataGraphDebug();
  });
</script>

<section class="kata-graph-pane" aria-label="Reachable task graph">
  <header class="graph-toolbar">
    <button type="button" class="toolbar-button" aria-label="Back to task list" onclick={onBack}>
      <ArrowLeftIcon size={14} strokeWidth={1.9} aria-hidden="true" />
      <span>Tasks</span>
    </button>
    <div class="graph-source">
      <strong title={source?.qualified_id ?? sourceUID}>{source?.title ?? "Reachable graph"}</strong>
    </div>
    <div class="graph-controls">
      <div class="depth-filter">
        <span>Depth</span>
        <SelectDropdown
          class="kata-graph-depth-select"
          title="Graph depth"
          value={depthLimit}
          options={depthOptions}
          onchange={setDepthLimit}
        />
      </div>
      <div class="layout-filter">
        <span>Layout</span>
        <SelectDropdown
          class="kata-graph-layout-select"
          title="Graph layout"
          value={layoutMode}
          options={layoutOptions}
          onchange={setLayoutMode}
        />
      </div>
      <label class="hide-done">
        <input type="checkbox" bind:checked={hideDone} />
        <span>Hide done</span>
      </label>
    </div>
  </header>

  {#if graph.nodes.length === 0}
    <p class="graph-empty">No cached task data is available for this graph.</p>
  {:else}
    <div class="graph-canvas">
      <SvelteFlow
        nodes={interactiveNodes}
        edges={graph.edges}
        {nodeTypes}
        fitView
        {fitViewOptions}
        autoPanOnSelection={false}
        defaultMarkerColor={null}
        nodesDraggable={false}
        nodesConnectable={false}
        elementsSelectable={true}
        edgesFocusable={false}
        elevateEdgesOnSelect={false}
        onnodeclick={({ node }) => selectNode(node as KataGraphNode)}
      >
        <Controls />
        <MiniMap nodeColor={minimapNodeColor} nodeStrokeColor={minimapNodeStrokeColor} />
        <Background variant={BackgroundVariant.Dots} gap={14} size={1} />
      </SvelteFlow>
    </div>
  {/if}
</section>

<style>
  .kata-graph-pane {
    min-width: 0;
    min-height: 0;
    height: 100%;
    display: flex;
    flex-direction: column;
    background: var(--bg-primary);
  }

  .graph-toolbar {
    flex: 0 0 auto;
    display: grid;
    grid-template-columns: auto minmax(0, 1fr) auto;
    align-items: center;
    gap: 10px;
    padding: 10px 14px;
    border-bottom: 1px solid var(--border-default);
    background: var(--bg-surface);
  }

  .toolbar-button,
  .depth-filter,
  .layout-filter,
  .hide-done {
    min-height: 28px;
    display: inline-flex;
    align-items: center;
    gap: 6px;
    border: 1px solid var(--border-default);
    border-radius: 6px;
    background: var(--bg-primary);
    color: var(--text-secondary);
    padding: 4px 8px;
    font: inherit;
    font-size: var(--font-size-xs);
    cursor: pointer;
  }

  .toolbar-button:hover,
  .depth-filter:hover,
  .layout-filter:hover,
  .hide-done:hover {
    background: var(--bg-hover);
    color: var(--text-primary);
  }

  .graph-controls {
    display: inline-flex;
    align-items: center;
    gap: 8px;
  }

  .depth-filter,
  .layout-filter {
    gap: 7px;
    border: 0;
    background: transparent;
    padding: 0;
    cursor: default;
  }

  .depth-filter:hover,
  .layout-filter:hover {
    background: transparent;
    color: var(--text-secondary);
  }

  :global(.kata-graph-depth-select),
  :global(.kata-graph-layout-select) {
    min-width: 104px;
  }

  :global(.kata-graph-depth-select .select-dropdown-trigger),
  :global(.kata-graph-layout-select .select-dropdown-trigger) {
    height: 28px;
    background: var(--bg-primary);
    border-color: var(--border-default);
    color: var(--text-primary);
  }

  :global(.kata-graph-depth-select .select-dropdown-list),
  :global(.kata-graph-layout-select .select-dropdown-list) {
    left: 0;
    right: auto;
  }

  .graph-source {
    min-width: 0;
    display: flex;
    align-items: center;
  }

  .graph-source strong {
    min-width: 0;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
    color: var(--text-primary);
    font-size: var(--font-size-sm);
  }

  .graph-canvas {
    position: relative;
    flex: 1 1 auto;
    min-height: 360px;
    overflow: hidden;
    contain: paint;
  }

  :global(.kata-graph-pane .svelte-flow__controls) {
    border: 1px solid var(--border-default);
    border-radius: 6px;
    box-shadow: var(--shadow-sm);
    overflow: hidden;
  }

  :global(.kata-graph-pane .svelte-flow__controls-button) {
    border: 1px solid var(--border-default);
    border-width: 0 0 1px;
    background: var(--bg-surface-hover);
    color: var(--text-primary);
  }

  :global(.kata-graph-pane .svelte-flow__controls-button:last-child) {
    border-bottom: 0;
  }

  :global(.kata-graph-pane .svelte-flow__controls-button:hover) {
    background: var(--bg-hover);
  }

  :global(.kata-graph-pane .svelte-flow__controls-button svg) {
    fill: currentColor;
  }

  :global(.kata-graph-pane .svelte-flow__minimap) {
    border: 1px solid var(--border-default);
    border-radius: 6px;
    background: var(--bg-primary);
    box-shadow: var(--shadow-sm);
  }

  :global(.kata-graph-pane .svelte-flow__minimap-mask) {
    fill: color-mix(in srgb, var(--bg-primary) 68%, transparent);
    stroke: var(--accent-blue);
  }

  :global(.kata-graph-pane .svelte-flow__minimap-node) {
    fill: var(--bg-hover);
    stroke: var(--border-default);
  }

  :global(.kata-graph-node .svelte-flow__handle) {
    opacity: 0;
    pointer-events: none;
  }

  :global(.kata-graph-edge .svelte-flow__edge-path) {
    stroke-width: 1.8;
  }

  :global(.kata-graph-edge--blocks .svelte-flow__edge-path) {
    stroke: var(--accent-amber);
  }

  :global(.kata-graph-edge--parent .svelte-flow__edge-path) {
    stroke: var(--text-secondary);
  }

  :global(.kata-graph-edge--related .svelte-flow__edge-path) {
    stroke-dasharray: 6 4;
  }

  .graph-empty {
    margin: 16px;
    color: var(--text-muted);
    font-size: var(--font-size-sm);
  }
</style>
