<script lang="ts">
  import { Handle, Position, type NodeProps } from "@xyflow/svelte";

  import type { KataGraphNode, KataGraphNodeData } from "./kataReachableGraph.js";

  let {
    data,
    selected = false,
    sourcePosition = Position.Right,
    targetPosition = Position.Left,
  }: NodeProps<KataGraphNode> & { data: KataGraphNodeData } = $props();

  let statusLabel = $derived.by(() => {
    if (data.status === "uncached") return "Uncached";
    if (data.status !== "closed") return "Open";
    return data.closedReason === "done" ? "Done" : "Closed";
  });
  let tone = $derived.by(() => {
    if (data.status === "uncached") return "uncached";
    if (data.status === "closed" && data.closedReason === "done") return "done";
    if (data.status === "closed") return "closed";
    return "open";
  });
</script>

<div
  class={[
    "graph-task-node",
    `graph-task-node--${tone}`,
    data.isSource ? "graph-task-node--source" : "",
    data.isSelected || selected ? "graph-task-node--selected" : "",
  ]}
>
  <div class="node-title-row">
    <strong title={data.title}>{data.title}</strong>
    {#if data.priorityLabel}
      <span class="priority-marker">{data.priorityLabel}</span>
    {/if}
  </div>
  <div class="node-meta-row">
    <span class="node-id">{data.idLabel}</span>
    <span class={["status-marker", `status-marker--${tone}`]}>{statusLabel}</span>
  </div>
  <Handle
    class="graph-task-handle"
    type="target"
    position={targetPosition}
    isConnectable={false}
    aria-hidden="true"
  />
  <Handle
    class="graph-task-handle"
    type="source"
    position={sourcePosition}
    isConnectable={false}
    aria-hidden="true"
  />
</div>

<style>
  .graph-task-node {
    width: 220px;
    min-height: 58px;
    display: flex;
    flex-direction: column;
    justify-content: center;
    gap: 7px;
    border: 1px solid var(--border-default);
    border-radius: 6px;
    background: var(--bg-primary);
    color: var(--text-primary);
    padding: 9px 10px;
    box-shadow: var(--shadow-sm);
  }

  .graph-task-node--source {
    border-color: var(--accent-blue);
  }

  .graph-task-node--selected {
    box-shadow: 0 0 0 2px color-mix(in srgb, var(--accent-blue) 30%, transparent);
  }

  .graph-task-node--done {
    opacity: 0.62;
  }

  .graph-task-node--uncached {
    border-style: dashed;
    color: var(--text-muted);
  }

  .node-title-row,
  .node-meta-row {
    min-width: 0;
    display: flex;
    align-items: center;
    justify-content: space-between;
    gap: 8px;
  }

  .node-title-row strong {
    min-width: 0;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
    font-size: var(--font-size-sm);
    font-weight: 650;
  }

  .node-meta-row {
    color: var(--text-muted);
    font-family: var(--font-mono);
    font-size: var(--font-size-xs);
  }

  .node-id {
    min-width: 0;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }

  .priority-marker,
  .status-marker {
    flex: 0 0 auto;
    border-radius: 999px;
    font-size: var(--font-size-2xs);
    font-weight: 700;
    line-height: 1;
  }

  .priority-marker {
    background: color-mix(in srgb, var(--accent-blue) 16%, transparent);
    color: var(--accent-blue);
    padding: 3px 5px;
  }

  .status-marker {
    border: 1px solid var(--border-default);
    color: var(--text-secondary);
    padding: 3px 5px;
  }

  .status-marker--open {
    border-color: color-mix(in srgb, var(--accent-green) 35%, var(--border-default));
    color: var(--accent-green);
  }

  .status-marker--done {
    color: var(--text-muted);
  }

  :global(.graph-task-handle) {
    width: 1px;
    height: 1px;
    border: 0;
    background: transparent;
    opacity: 0;
    pointer-events: none;
  }
</style>
