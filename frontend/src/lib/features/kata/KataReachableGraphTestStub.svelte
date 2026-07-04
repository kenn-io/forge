<script lang="ts">
  import type { KataTaskAPI, KataTaskSummary } from "../../api/kata/taskTypes.js";
  import type { KataGraphLayoutDirection } from "./kataReachableGraph.js";

  interface Props {
    api: KataTaskAPI;
    sourceIssue: KataTaskSummary;
    selectedUID: string | null;
    layoutDirection?: KataGraphLayoutDirection | undefined;
    onBack: () => void;
    onSelectIssue: (uid: string) => void;
    onGraphTasksLoaded?: ((tasks: readonly KataTaskSummary[]) => void) | undefined;
  }

  let {
    api,
    sourceIssue,
    selectedUID,
    layoutDirection = "LR",
    onBack,
    onSelectIssue,
    onGraphTasksLoaded,
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

  let nodes = $state.raw<StubGraphNode[]>([]);
  let error = $state<string | null>(null);

  $effect(() => {
    const abort = new AbortController();
    error = null;
    void api
      .reachableGraph(sourceIssue.project_id, sourceIssue.uid, { depth: "full", hide_done: false }, { signal: abort.signal })
      .then((graph) => {
        if (abort.signal.aborted) return;
        onGraphTasksLoaded?.(graph.nodes);
        nodes = graph.nodes.map((task) => ({
          id: task.uid,
          title: task.title,
          idLabel: task.short_id,
          priorityLabel: taskPriorityLabel(task.priority),
          selectable: true,
        }));
      })
      .catch((caught: unknown) => {
        if (abort.signal.aborted) return;
        error = caught instanceof Error ? caught.message : "Could not load graph.";
      });
    return () => abort.abort();
  });

  $effect(() => {
    if (selectedUID && !nodes.some((node) => node.id === selectedUID)) {
      nodes = [...nodes, {
        id: selectedUID,
        title: selectedUID,
        idLabel: selectedUID.slice(-4),
        priorityLabel: null,
        selectable: true,
      }];
    }
  });

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
  {#if error}
    <p role="alert">{error}</p>
  {/if}
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
