<script lang="ts">
  import type { KataReachableGraphResponse, KataTaskSummary } from "../../api/kata/taskTypes.js";
  import type { KataGraphLayoutDirection } from "./kataReachableGraph.js";

  interface Props {
    graph: KataReachableGraphResponse;
    sourceIssue: KataTaskSummary;
    selectedUID: string | null;
    layoutDirection?: KataGraphLayoutDirection | undefined;
    onBack: () => void;
    onSelectIssue: (uid: string) => void;
  }

  let {
    graph,
    sourceIssue,
    selectedUID,
    layoutDirection = "LR",
    onBack,
    onSelectIssue,
  }: Props = $props();

  interface StubGraphNode {
    id: string;
    title: string;
    idLabel: string;
    priorityLabel: string | null;
    selectable: boolean;
  }

  function taskPriorityLabel(priority: number | undefined): string | null {
    return priority === undefined ? null : `P${priority}`;
  }

  let nodes = $derived(graph.nodes.map((task) => ({
    id: task.uid,
    title: task.title,
    idLabel: task.short_id,
    priorityLabel: taskPriorityLabel(task.priority),
    selectable: true,
  })));

  function selectNode(node: StubGraphNode): void {
    if (!node.selectable) return;
    onSelectIssue(node.id);
  }

  function selectButtonNode(event: MouseEvent, node: StubGraphNode): void {
    event.stopPropagation();
    selectNode(node);
  }
</script>

<section class="kata-graph-pane" aria-label="Reachable task graph" data-layout-direction={layoutDirection}>
  <button type="button" aria-label="Back to task list" onclick={onBack}>Back to task list</button>
  {#each nodes as node (node.id)}
    <div class="svelte-flow__node" onclick={() => selectNode(node)} onkeydown={() => {}} role="presentation">
      <button
        type="button"
        class="graph-task-node"
        disabled={!node.selectable}
        onclick={(event) => selectButtonNode(event, node)}
      >
        <span>{node.title}</span>
        <span>{node.idLabel}</span>
        {#if node.priorityLabel}
          <span>{node.priorityLabel}</span>
        {/if}
      </button>
    </div>
  {/each}
</section>
