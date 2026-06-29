<script lang="ts">
  import ArrowLeftIcon from "@lucide/svelte/icons/arrow-left";
  import { Background, BackgroundVariant, Controls, MiniMap, SvelteFlow } from "@xyflow/svelte";
  import "@xyflow/svelte/dist/style.css";

  import type { KataTaskDetail, KataTaskSummary } from "../../api/kata/taskTypes.js";
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

  function selectNode(node: KataGraphNode): void {
    if (!node.data.selectable) return;
    onSelectIssue(node.id);
  }
</script>

<section class="kata-graph-pane" aria-label="Reachable task graph">
  <header class="graph-toolbar">
    <button type="button" class="toolbar-button" aria-label="Back to task list" onclick={onBack}>
      <ArrowLeftIcon size={14} strokeWidth={1.9} aria-hidden="true" />
      <span>Tasks</span>
    </button>
    <div class="graph-source">
      <span class="source-id">{source?.qualified_id ?? sourceUID}</span>
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
        fitView
        nodesDraggable={false}
        nodesConnectable={false}
        elementsSelectable={true}
        onnodeclick={({ node }) => selectNode(node as KataGraphNode)}
      >
        <Controls />
        <MiniMap />
        <Background variant={BackgroundVariant.Dots} gap={14} size={1} />
      </SvelteFlow>
    </div>
    <div class="graph-node-list" aria-label="Reachable task nodes">
      {#each graph.nodes as node (node.id)}
        <button
          type="button"
          class={node.class}
          disabled={!node.data.selectable}
          onclick={() => selectNode(node)}
        >
          <span class="node-title">{node.data.title}</span>
          <span class="node-meta">
            {node.data.idLabel}
            {#if node.data.priorityLabel}<span class="node-priority">{node.data.priorityLabel}</span>{/if}
          </span>
        </button>
      {/each}
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
    flex-direction: column;
    gap: 2px;
  }

  .graph-source strong {
    min-width: 0;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
    color: var(--text-primary);
    font-size: var(--font-size-sm);
  }

  .source-id,
  .node-meta {
    color: var(--text-muted);
    font-family: var(--font-mono);
    font-size: var(--font-size-xs);
  }

  .graph-canvas {
    flex: 1 1 auto;
    min-height: 360px;
  }

  .graph-node-list {
    flex: 0 0 auto;
    display: flex;
    flex-wrap: wrap;
    gap: 8px;
    padding: 10px 14px;
    border-top: 1px solid var(--border-muted);
    background: var(--bg-surface);
  }

  .graph-node-list :global(button) {
    max-width: 240px;
    border: 1px solid var(--border-default);
    border-radius: 6px;
    background: var(--bg-primary);
    color: var(--text-primary);
    display: inline-flex;
    flex-direction: column;
    align-items: flex-start;
    gap: 2px;
    padding: 6px 8px;
    text-align: left;
    cursor: pointer;
  }

  .graph-node-list :global(button:disabled) {
    cursor: default;
    opacity: 0.62;
  }

  :global(.kata-graph-node) {
    border: 1px solid var(--border-default);
    border-radius: 6px;
    background: var(--bg-primary);
    color: var(--text-primary);
    padding: 8px 10px;
    box-shadow: var(--shadow-sm);
  }

  :global(.kata-graph-node--closed) {
    opacity: 0.62;
  }

  :global(.kata-graph-node--source) {
    border-color: var(--accent-blue);
  }

  :global(.kata-graph-node--selected) {
    box-shadow: 0 0 0 2px color-mix(in srgb, var(--accent-blue) 30%, transparent);
  }

  :global(.kata-graph-node--uncached) {
    border-style: dashed;
    color: var(--text-muted);
  }

  .node-title {
    display: block;
    max-width: 220px;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
    font-weight: 650;
  }

  .node-priority {
    margin-left: 6px;
    color: var(--accent-blue);
    font-weight: 700;
  }

  .graph-empty {
    margin: 16px;
    color: var(--text-muted);
    font-size: var(--font-size-sm);
  }
</style>
