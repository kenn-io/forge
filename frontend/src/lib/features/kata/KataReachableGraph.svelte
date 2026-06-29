<script lang="ts">
  import ArrowLeftIcon from "@lucide/svelte/icons/arrow-left";
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
    type KataGraphMissingRef,
    type KataGraphNode,
  } from "./kataReachableGraph.js";

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
  let graph = $derived(
    buildKataReachableGraph({ sourceUID, selectedUID, tasks, selectedDetail, hideDone, depthLimit }),
  );
  let source = $derived(tasks.find((task) => task.uid === sourceUID));
  const nodeTypes: NodeTypes = {
    kataTask: KataGraphTaskNode,
  };
  const fitViewOptions = {
    duration: 0,
    padding: 0.12,
  };

  function missingRefKey(ref: KataGraphMissingRef): string {
    return ref.uid ? `uid:${ref.uid}` : `short:${ref.projectUID}:${ref.shortID}`;
  }

  function selectNode(node: KataGraphNode): void {
    if (!node.data.selectable) return;
    onSelectIssue(node.id);
  }

  function minimapData(node: SvelteFlowNode): Partial<KataGraphNode["data"]> {
    return node.data as Partial<KataGraphNode["data"]>;
  }

  function minimapNodeColor(node: SvelteFlowNode): string {
    const data = minimapData(node);
    if (data.status === "closed" && data.closedReason === "done") return "var(--text-muted)";
    if (data.status === "uncached") return "var(--bg-surface-hover)";
    if (data.isSource || data.isSelected) return "var(--accent-blue)";
    return "var(--accent-green)";
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
    const snapshot = {
      sourceUID,
      selectedUID,
      hideDone,
      depthLimit,
      nodeIds: graph.nodes.map((node) => node.id),
      disabledNodeIds: graph.nodes.filter((node) => !node.data.selectable).map((node) => node.id),
      missingRefKeys: graph.missingRefs.map(missingRefKey),
      nodeCount: graph.nodes.length,
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
      <label class="depth-filter">
        <span>Depth</span>
        <select bind:value={depthLimit} aria-label="Graph depth">
          <option value="full">Full</option>
          <option value="1">1 edge</option>
          <option value="2">2 edges</option>
          <option value="3">3 edges</option>
        </select>
      </label>
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
        nodes={graph.nodes}
        edges={graph.edges}
        {nodeTypes}
        fitView
        {fitViewOptions}
        autoPanOnSelection={false}
        defaultMarkerColor={null}
        nodesDraggable={false}
        nodesConnectable={false}
        elementsSelectable={true}
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
  .hide-done:hover {
    background: var(--bg-hover);
    color: var(--text-primary);
  }

  .graph-controls {
    display: inline-flex;
    align-items: center;
    gap: 8px;
  }

  .depth-filter {
    padding: 0 4px 0 8px;
  }

  .depth-filter select {
    min-height: 22px;
    border: 0;
    background: transparent;
    color: var(--text-primary);
    font: inherit;
    font-size: var(--font-size-xs);
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
    stroke: var(--accent-blue);
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
