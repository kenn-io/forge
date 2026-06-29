import type { Edge, Node } from "@xyflow/svelte";

import type { KataLinkPeer, KataTaskDetail, KataTaskLink, KataTaskSummary } from "../../api/kata/taskTypes.js";

export interface KataGraphNodeData extends Record<string, unknown> {
  label: string;
  title: string;
  idLabel: string;
  projectLabel: string;
  status: KataTaskSummary["status"] | "uncached";
  closedReason?: KataTaskSummary["closed_reason"] | undefined;
  priorityLabel: string | null;
  isSource: boolean;
  isSelected: boolean;
  selectable: boolean;
}

export type KataGraphNode = Node<KataGraphNodeData>;
export type KataGraphEdge = Edge;

export interface BuildKataReachableGraphInput {
  sourceUID: string;
  selectedUID: string | null;
  tasks: readonly KataTaskSummary[];
  selectedDetail?: KataTaskDetail | null | undefined;
  hideDone: boolean;
}

interface TaskIndexes {
  byUID: Map<string, KataTaskSummary>;
  byProjectShort: Map<string, KataTaskSummary[]>;
}

interface ResolvedPeer {
  id: string;
  task?: KataTaskSummary | undefined;
  projectUID: string;
  shortID: string;
}

type GraphEdgeKind = "parent" | "blocks" | "related";

function taskKey(projectUID: string, shortID: string): string {
  return `${projectUID}:${shortID}`;
}

function isDone(issue: KataTaskSummary): boolean {
  return issue.status === "closed" && issue.closed_reason === "done";
}

function priorityLabel(priority: number | undefined): string | null {
  if (priority === undefined) return null;
  return `P${priority}`;
}

function collectTasks(tasks: readonly KataTaskSummary[]): TaskIndexes {
  const byUID = new Map<string, KataTaskSummary>();
  const byProjectShort = new Map<string, KataTaskSummary[]>();
  for (const task of tasks) {
    byUID.set(task.uid, task);
    const key = taskKey(task.project_uid, task.short_id);
    byProjectShort.set(key, [...(byProjectShort.get(key) ?? []), task]);
  }
  return { byUID, byProjectShort };
}

function resolvePeer(peer: KataLinkPeer, projectUID: string, indexes: TaskIndexes): ResolvedPeer {
  if (peer.uid) {
    const byUID = indexes.byUID.get(peer.uid);
    if (byUID) {
      return { id: byUID.uid, task: byUID, projectUID: byUID.project_uid, shortID: byUID.short_id };
    }
  }

  const matches = indexes.byProjectShort.get(taskKey(projectUID, peer.short_id)) ?? [];
  if (matches.length === 1) {
    const task = matches[0]!;
    return { id: task.uid, task, projectUID: task.project_uid, shortID: task.short_id };
  }

  return {
    id: `uncached:${projectUID}:${peer.short_id}`,
    projectUID,
    shortID: peer.short_id,
  };
}

function makeEdge(source: string, target: string, kind: GraphEdgeKind): KataGraphEdge {
  return {
    id: `${kind}:${source}:${target}`,
    source,
    target,
    type: "smoothstep",
    label: kind === "parent" ? "parent" : kind === "blocks" ? "blocks" : "related",
    class: `kata-graph-edge kata-graph-edge--${kind}`,
  };
}

function addEdge(edges: Map<string, KataGraphEdge>, source: string, target: string, kind: GraphEdgeKind): void {
  const next = makeEdge(source, target, kind);
  edges.set(next.id, next);
}

function detailEdges(
  detail: KataTaskDetail | null | undefined,
  sourceUID: string,
  indexes: TaskIndexes,
): KataGraphEdge[] {
  if (!detail || detail.issue.uid !== sourceUID) return [];
  return detail.links.flatMap((link) => resolveDetailLink(link, detail.issue.project_uid, indexes));
}

function resolveDetailLink(link: KataTaskLink, projectUID: string, indexes: TaskIndexes): KataGraphEdge[] {
  const from = resolvePeer(link.from, projectUID, indexes);
  const to = resolvePeer(link.to, projectUID, indexes);
  if (link.type === "parent") return [makeEdge(from.id, to.id, "parent")];
  if (link.type === "blocks") return [makeEdge(from.id, to.id, "blocks")];
  return [makeEdge(from.id, to.id, "related")];
}

function includePeer(
  peer: ResolvedPeer,
  queued: string[],
  seen: Set<string>,
  nodeTasks: Map<string, KataTaskSummary | undefined>,
): void {
  nodeTasks.set(peer.id, peer.task);
  if (peer.task && !seen.has(peer.task.uid)) {
    queued.push(peer.task.uid);
  }
}

function peerReferencesTask(peer: KataLinkPeer, task: KataTaskSummary): boolean {
  if (peer.uid && peer.uid === task.uid) return true;
  return peer.short_id === task.short_id;
}

function nodeClass(task: KataTaskSummary | undefined, sourceUID: string, selectedUID: string | null): string {
  return [
    "kata-graph-node",
    task?.status === "closed" ? "kata-graph-node--closed" : "kata-graph-node--open",
    task?.uid === sourceUID ? "kata-graph-node--source" : "",
    task?.uid === selectedUID ? "kata-graph-node--selected" : "",
    task ? "" : "kata-graph-node--uncached",
  ]
    .filter(Boolean)
    .join(" ");
}

function nodeData(
  id: string,
  task: KataTaskSummary | undefined,
  sourceUID: string,
  selectedUID: string | null,
): KataGraphNodeData {
  if (!task) {
    const label = id.split(":").at(-1) ?? id;
    return {
      label,
      title: label,
      idLabel: label,
      projectLabel: "",
      status: "uncached",
      priorityLabel: null,
      isSource: false,
      isSelected: false,
      selectable: false,
    };
  }

  return {
    label: task.title,
    title: task.title,
    idLabel: task.qualified_id || task.short_id,
    projectLabel: task.project_name,
    status: task.status,
    closedReason: task.closed_reason,
    priorityLabel: priorityLabel(task.priority),
    isSource: task.uid === sourceUID,
    isSelected: task.uid === selectedUID,
    selectable: true,
  };
}

function layoutNode(
  id: string,
  task: KataTaskSummary | undefined,
  index: number,
  sourceUID: string,
  selectedUID: string | null,
): KataGraphNode {
  return {
    id,
    type: "default",
    position: { x: (index % 3) * 260, y: Math.floor(index / 3) * 150 },
    data: nodeData(id, task, sourceUID, selectedUID),
    class: nodeClass(task, sourceUID, selectedUID),
    draggable: false,
    selectable: task !== undefined,
  };
}

export function buildKataReachableGraph(input: BuildKataReachableGraphInput): {
  nodes: KataGraphNode[];
  edges: KataGraphEdge[];
} {
  const indexes = collectTasks(input.tasks);
  const source = indexes.byUID.get(input.sourceUID);
  if (!source) return { nodes: [], edges: [] };

  const queued = [source.uid];
  const seen = new Set<string>();
  const nodeTasks = new Map<string, KataTaskSummary | undefined>([[source.uid, source]]);
  const edges = new Map<string, KataGraphEdge>();

  while (queued.length > 0) {
    const uid = queued.shift()!;
    if (seen.has(uid)) continue;
    seen.add(uid);

    const task = indexes.byUID.get(uid);
    if (!task) continue;

    if (task.parent_short_id) {
      const parent = resolvePeer({ uid: "", short_id: task.parent_short_id }, task.project_uid, indexes);
      includePeer(parent, queued, seen, nodeTasks);
      addEdge(edges, parent.id, task.uid, "parent");
    }

    for (const child of input.tasks) {
      if (child.project_uid !== task.project_uid || child.parent_short_id !== task.short_id) continue;
      includePeer(
        { id: child.uid, task: child, projectUID: child.project_uid, shortID: child.short_id },
        queued,
        seen,
        nodeTasks,
      );
      addEdge(edges, task.uid, child.uid, "parent");
    }

    for (const peer of task.blocks ?? []) {
      const resolved = resolvePeer(peer, task.project_uid, indexes);
      includePeer(resolved, queued, seen, nodeTasks);
      addEdge(edges, task.uid, resolved.id, "blocks");
    }

    for (const peer of task.blocked_by ?? []) {
      const resolved = resolvePeer(peer, task.project_uid, indexes);
      includePeer(resolved, queued, seen, nodeTasks);
      addEdge(edges, resolved.id, task.uid, "blocks");
    }

    for (const peer of task.related ?? []) {
      const resolved = resolvePeer(peer, task.project_uid, indexes);
      includePeer(resolved, queued, seen, nodeTasks);
      addEdge(edges, task.uid, resolved.id, "related");
    }

    for (const candidate of input.tasks) {
      if (candidate.uid === task.uid || candidate.project_uid !== task.project_uid) continue;
      if ((candidate.blocks ?? []).some((peer) => peerReferencesTask(peer, task))) {
        includePeer(
          { id: candidate.uid, task: candidate, projectUID: candidate.project_uid, shortID: candidate.short_id },
          queued,
          seen,
          nodeTasks,
        );
        addEdge(edges, candidate.uid, task.uid, "blocks");
      }
      if ((candidate.blocked_by ?? []).some((peer) => peerReferencesTask(peer, task))) {
        includePeer(
          { id: candidate.uid, task: candidate, projectUID: candidate.project_uid, shortID: candidate.short_id },
          queued,
          seen,
          nodeTasks,
        );
        addEdge(edges, task.uid, candidate.uid, "blocks");
      }
      if ((candidate.related ?? []).some((peer) => peerReferencesTask(peer, task))) {
        includePeer(
          { id: candidate.uid, task: candidate, projectUID: candidate.project_uid, shortID: candidate.short_id },
          queued,
          seen,
          nodeTasks,
        );
        addEdge(edges, candidate.uid, task.uid, "related");
      }
    }
  }

  for (const detailEdge of detailEdges(input.selectedDetail, source.uid, indexes)) {
    edges.set(detailEdge.id, detailEdge);
    const sourceTask = indexes.byUID.get(detailEdge.source);
    const targetTask = indexes.byUID.get(detailEdge.target);
    nodeTasks.set(detailEdge.source, sourceTask);
    nodeTasks.set(detailEdge.target, targetTask);
  }

  const visibleIDs = new Set<string>();
  const nodes = [...nodeTasks.entries()].flatMap(([id, task], index): KataGraphNode[] => {
    if (task && input.hideDone && isDone(task)) return [];
    visibleIDs.add(id);
    return [layoutNode(id, task, index, input.sourceUID, input.selectedUID)];
  });

  return {
    nodes,
    edges: [...edges.values()].filter((edge) => visibleIDs.has(edge.source) && visibleIDs.has(edge.target)),
  };
}
