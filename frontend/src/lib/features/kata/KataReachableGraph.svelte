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
  import KataGraphTaskNode from "./KataGraphTaskNode.svelte";
  import { buildKataReachableGraph, type KataGraphNode } from "./kataReachableGraph.js";

  interface Props {
    sourceUID: string;
    selectedUID: string | null;
    tasks: readonly KataTaskSummary[];
    selectedDetail?: KataTaskDetail | null | undefined;
    onBack: () => void;
    onSelectIssue: (uid: string) => void;
  }

  let { sourceUID, selectedUID, tasks, selectedDetail = null, onBack, onSelectIssue }: Props = $props();

  let hideDone = $state(false);
  let graph = $derived(buildKataReachableGraph({ sourceUID, selectedUID, tasks, selectedDetail, hideDone }));
  let source = $derived(tasks.find((task) => task.uid === sourceUID));
  const nodeTypes: NodeTypes = {
    kataTask: KataGraphTaskNode,
  };

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
</script>

<section class="kata-graph-pane" aria-label="Reachable task graph">
  <header class="graph-toolbar">
    <button type="button" class="toolbar-button" aria-label="Back to task list" onclick={onBack}>
      <ArrowLeftIcon size={14} strokeWidth={1.9} aria-hidden="true" />
      <span>Tasks</span>
    </button>
    <div class="graph-source">
      <strong>{source?.title ?? "Reachable graph"}</strong>
    </div>
    <label class="hide-done">
      <input type="checkbox" bind:checked={hideDone} />
      <span>Hide done</span>
    </label>
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
  .hide-done:hover {
    background: var(--bg-hover);
    color: var(--text-primary);
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
