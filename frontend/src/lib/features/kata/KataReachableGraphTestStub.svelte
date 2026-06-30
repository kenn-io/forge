<script lang="ts">
  import type { KataLinkPeer, KataTaskDetail, KataTaskSummary } from "../../api/kata/taskTypes.js";
  import type { KataGraphLayoutDirection, KataGraphMissingRef } from "./kataReachableGraph.js";

  interface Props {
    sourceUID: string;
    selectedUID: string | null;
    tasks: readonly KataTaskSummary[];
    selectedDetail?: KataTaskDetail | null | undefined;
    layoutDirection?: KataGraphLayoutDirection | undefined;
    onBack: () => void;
    onSelectIssue: (uid: string) => void;
    onRequestMissingTasks?: ((refs: readonly KataGraphMissingRef[]) => void) | undefined;
  }

  let {
    sourceUID,
    selectedUID,
    tasks,
    selectedDetail = null,
    layoutDirection = "LR",
    onBack,
    onSelectIssue,
    onRequestMissingTasks,
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

  function addTask(nodes: Map<string, StubGraphNode>, task: KataTaskSummary): void {
    nodes.set(task.uid, {
      id: task.uid,
      title: task.title,
      idLabel: task.short_id,
      priorityLabel: taskPriorityLabel(task.priority),
      selectable: true,
    });
  }

  function missingRefID(peer: KataLinkPeer, projectUID: string): string {
    return peer.uid ?? `missing:${projectUID}:${peer.short_id}`;
  }

  function addPeer(
    nodes: Map<string, StubGraphNode>,
    missingRefs: Map<string, KataGraphMissingRef>,
    taskByUID: Map<string, KataTaskSummary>,
    projectUID: string,
    peer: KataLinkPeer,
  ): void {
    const cached = peer.uid ? taskByUID.get(peer.uid) : undefined;
    if (cached) {
      addTask(nodes, cached);
      return;
    }
    const id = missingRefID(peer, projectUID);
    nodes.set(id, {
      id,
      title: peer.short_id,
      idLabel: peer.short_id,
      priorityLabel: null,
      selectable: false,
    });
    missingRefs.set(id, {
      uid: peer.uid,
      projectUID,
      shortID: peer.short_id,
    });
  }

  function collectSummaryPeers(
    nodes: Map<string, StubGraphNode>,
    missingRefs: Map<string, KataGraphMissingRef>,
    taskByUID: Map<string, KataTaskSummary>,
    task: KataTaskSummary,
  ): void {
    for (const peer of task.blocks ?? []) addPeer(nodes, missingRefs, taskByUID, task.project_uid, peer);
    for (const peer of task.blocked_by ?? []) addPeer(nodes, missingRefs, taskByUID, task.project_uid, peer);
    for (const peer of task.related ?? []) addPeer(nodes, missingRefs, taskByUID, task.project_uid, peer);
    if (task.parent) addPeer(nodes, missingRefs, taskByUID, task.project_uid, task.parent);
  }

  let graph = $derived.by(() => {
    const taskByUID = new Map(tasks.map((task) => [task.uid, task]));
    if (selectedDetail) {
      taskByUID.set(selectedDetail.issue.uid, selectedDetail.issue);
      for (const child of selectedDetail.children ?? []) taskByUID.set(child.uid, child);
    }

    const nodes = new Map<string, StubGraphNode>();
    const missingRefs = new Map<string, KataGraphMissingRef>();
    const source = taskByUID.get(sourceUID);
    if (source) {
      addTask(nodes, source);
      collectSummaryPeers(nodes, missingRefs, taskByUID, source);
    }
    if (selectedUID) {
      const selectedTask = taskByUID.get(selectedUID);
      if (selectedTask) addTask(nodes, selectedTask);
    }
    if (selectedDetail?.issue.uid === sourceUID) {
      for (const link of selectedDetail.links) {
        addPeer(nodes, missingRefs, taskByUID, selectedDetail.issue.project_uid, link.from);
        addPeer(nodes, missingRefs, taskByUID, selectedDetail.issue.project_uid, link.to);
      }
      for (const child of selectedDetail.children ?? []) addTask(nodes, child);
    }
    return {
      nodes: [...nodes.values()],
      missingRefs: [...missingRefs.values()],
    };
  });
  let requestedMissingRefs = $state("");
  let missingRefKey = $derived(
    graph.missingRefs.map((ref) => `${ref.uid ?? ""}:${ref.projectUID}:${ref.shortID}`).join("\n"),
  );

  $effect(() => {
    if (!onRequestMissingTasks || graph.missingRefs.length === 0 || missingRefKey === requestedMissingRefs) return;
    requestedMissingRefs = missingRefKey;
    onRequestMissingTasks(graph.missingRefs);
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
  {#each graph.nodes as node (node.id)}
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
