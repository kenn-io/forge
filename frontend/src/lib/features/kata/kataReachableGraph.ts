import { MarkerType, Position, type Edge, type Node } from "@xyflow/svelte";

import type { KataLinkPeer, KataTaskDetail, KataTaskLink, KataTaskSummary } from "../../api/kata/taskTypes.js";

export interface KataGraphNodeData extends Record<string, unknown> {
  label: string;
  title: string;
  idLabel: string;
  projectLabel: string;
  qualifiedLabel: string;
  accessibleLabel: string;
  status: KataTaskSummary["status"] | "uncached";
  closedReason?: KataTaskSummary["closed_reason"] | undefined;
  priorityLabel: string | null;
  isSource: boolean;
  isSelected: boolean;
  selectable: boolean;
  adjacentRelation: KataGraphAdjacentRelation;
}

export type KataGraphAdjacentRelation = "blocks" | "blockedBy" | "child" | "parent" | "related" | null;
export type KataGraphNode = Node<KataGraphNodeData, "kataTask">;
export type KataGraphEdge = Edge<KataGraphEdgeData>;

export interface KataGraphMissingRef {
  uid?: string | undefined;
  projectUID: string;
  shortID: string;
}

interface KataGraphEdgeData extends Record<string, unknown> {
  kind: GraphEdgeKind;
}

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
  childrenByParent: Map<string, KataTaskSummary[]>;
  reverseBlocksByUID: Map<string, KataTaskSummary[]>;
  reverseBlocksByProjectShort: Map<string, KataTaskSummary[]>;
  reverseBlockedByUID: Map<string, KataTaskSummary[]>;
  reverseBlockedByProjectShort: Map<string, KataTaskSummary[]>;
  reverseRelatedByUID: Map<string, KataTaskSummary[]>;
  reverseRelatedByProjectShort: Map<string, KataTaskSummary[]>;
}

interface ResolvedPeer {
  id: string;
  task?: KataTaskSummary | undefined;
  uid?: string | undefined;
  projectUID: string;
  shortID: string;
}

type GraphEdgeKind = "parent" | "blocks" | "related";

const KATA_GRAPH_X_SPACING = 320;
const KATA_GRAPH_Y_SPACING = 108;
const KATA_GRAPH_NODE_WIDTH = 250;
const KATA_GRAPH_NODE_HEIGHT = 74;

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
  const childrenByParent = new Map<string, KataTaskSummary[]>();
  const reverseBlocksByUID = new Map<string, KataTaskSummary[]>();
  const reverseBlocksByProjectShort = new Map<string, KataTaskSummary[]>();
  const reverseBlockedByUID = new Map<string, KataTaskSummary[]>();
  const reverseBlockedByProjectShort = new Map<string, KataTaskSummary[]>();
  const reverseRelatedByUID = new Map<string, KataTaskSummary[]>();
  const reverseRelatedByProjectShort = new Map<string, KataTaskSummary[]>();
  for (const task of tasks) {
    byUID.set(task.uid, task);
    const key = taskKey(task.project_uid, task.short_id);
    byProjectShort.set(key, [...(byProjectShort.get(key) ?? []), task]);
    if (task.parent_short_id) {
      const parentKey = taskKey(task.project_uid, task.parent_short_id);
      childrenByParent.set(parentKey, [...(childrenByParent.get(parentKey) ?? []), task]);
    }
    indexReversePeers(task, task.blocks ?? [], reverseBlocksByUID, reverseBlocksByProjectShort);
    indexReversePeers(task, task.blocked_by ?? [], reverseBlockedByUID, reverseBlockedByProjectShort);
    indexReversePeers(task, task.related ?? [], reverseRelatedByUID, reverseRelatedByProjectShort);
  }
  return {
    byUID,
    byProjectShort,
    childrenByParent,
    reverseBlocksByUID,
    reverseBlocksByProjectShort,
    reverseBlockedByUID,
    reverseBlockedByProjectShort,
    reverseRelatedByUID,
    reverseRelatedByProjectShort,
  };
}

function indexReversePeers(
  task: KataTaskSummary,
  peers: readonly KataLinkPeer[],
  byUID: Map<string, KataTaskSummary[]>,
  byProjectShort: Map<string, KataTaskSummary[]>,
): void {
  for (const peer of peers) {
    if (peer.uid) {
      byUID.set(peer.uid, [...(byUID.get(peer.uid) ?? []), task]);
    }
    const key = taskKey(task.project_uid, peer.short_id);
    byProjectShort.set(key, [...(byProjectShort.get(key) ?? []), task]);
  }
}

function reversePeerMatches(
  task: KataTaskSummary,
  byUID: Map<string, KataTaskSummary[]>,
  byProjectShort: Map<string, KataTaskSummary[]>,
): KataTaskSummary[] {
  const matches = new Map<string, KataTaskSummary>();
  for (const candidate of byUID.get(task.uid) ?? []) {
    if (candidate.uid !== task.uid && candidate.project_uid === task.project_uid) {
      matches.set(candidate.uid, candidate);
    }
  }
  for (const candidate of byProjectShort.get(taskKey(task.project_uid, task.short_id)) ?? []) {
    if (candidate.uid !== task.uid && candidate.project_uid === task.project_uid) {
      matches.set(candidate.uid, candidate);
    }
  }
  return [...matches.values()];
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
    id: peer.uid || `uncached:${projectUID}:${peer.short_id}`,
    uid: peer.uid || undefined,
    projectUID,
    shortID: peer.short_id,
  };
}

function makeEdge(source: string, target: string, kind: GraphEdgeKind): KataGraphEdge {
  const markerColor =
    kind === "blocks" ? "var(--accent-blue)" : kind === "parent" ? "var(--text-secondary)" : "var(--text-muted)";
  return {
    id: `${kind}:${source}:${target}`,
    source,
    target,
    type: "smoothstep",
    class: `kata-graph-edge kata-graph-edge--${kind}`,
    data: { kind },
    markerEnd: {
      type: MarkerType.ArrowClosed,
      color: markerColor,
      width: 18,
      height: 18,
    },
    ariaLabel: `${kind} relationship from ${source} to ${target}`,
    interactionWidth: 12,
  };
}

function addEdge(edges: Map<string, KataGraphEdge>, source: string, target: string, kind: GraphEdgeKind): void {
  const next = makeEdge(source, target, kind);
  if (!edges.has(next.id)) {
    edges.set(next.id, next);
  }
}

function detailEdges(
  detail: KataTaskDetail | null | undefined,
  sourceUID: string,
  indexes: TaskIndexes,
  missingRefs: Map<string, KataGraphMissingRef>,
): KataGraphEdge[] {
  if (!detail || detail.issue.uid !== sourceUID) return [];
  return detail.links.flatMap((link) => resolveDetailLink(link, detail.issue.project_uid, indexes, missingRefs));
}

function resolveDetailLink(
  link: KataTaskLink,
  projectUID: string,
  indexes: TaskIndexes,
  missingRefs: Map<string, KataGraphMissingRef>,
): KataGraphEdge[] {
  const from = resolvePeer(link.from, projectUID, indexes);
  const to = resolvePeer(link.to, projectUID, indexes);
  rememberMissingRef(from, missingRefs);
  rememberMissingRef(to, missingRefs);
  if (link.type === "parent") return [makeEdge(from.id, to.id, "parent")];
  if (link.type === "blocks") return [makeEdge(from.id, to.id, "blocks")];
  return [makeEdge(from.id, to.id, "related")];
}

function rememberMissingRef(peer: ResolvedPeer, missingRefs: Map<string, KataGraphMissingRef>): void {
  if (peer.task) return;
  missingRefs.set(peer.id, { uid: peer.uid, projectUID: peer.projectUID, shortID: peer.shortID });
}

function includePeer(
  peer: ResolvedPeer,
  queued: string[],
  seen: Set<string>,
  nodeTasks: Map<string, KataTaskSummary | undefined>,
  missingRefs: Map<string, KataGraphMissingRef>,
): void {
  nodeTasks.set(peer.id, peer.task);
  if (peer.task && !seen.has(peer.task.uid)) {
    queued.push(peer.task.uid);
  } else {
    rememberMissingRef(peer, missingRefs);
  }
}

function includeGraphNode(
  id: string,
  indexes: TaskIndexes,
  queued: string[],
  seen: Set<string>,
  nodeTasks: Map<string, KataTaskSummary | undefined>,
): void {
  const task = indexes.byUID.get(id);
  nodeTasks.set(id, task);
  if (task && !seen.has(task.uid)) {
    queued.push(task.uid);
  }
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

function graphNodeSortKey(id: string, task: KataTaskSummary | undefined): string {
  if (!task) return `1:${id}`;
  return `0:${task.project_name}:${task.short_id}:${task.title}:${task.uid}`;
}

function compareGraphNodeEntries(
  [leftID, leftTask]: [string, KataTaskSummary | undefined],
  [rightID, rightTask]: [string, KataTaskSummary | undefined],
  sourceUID: string,
): number {
  if (leftID === sourceUID) return -1;
  if (rightID === sourceUID) return 1;
  return graphNodeSortKey(leftID, leftTask).localeCompare(graphNodeSortKey(rightID, rightTask), undefined, {
    numeric: true,
    sensitivity: "base",
  });
}

function nodeData(
  id: string,
  task: KataTaskSummary | undefined,
  sourceUID: string,
  selectedUID: string | null,
  adjacentRelation: KataGraphAdjacentRelation,
  projectLabel: string,
  missingRef: KataGraphMissingRef | undefined,
): KataGraphNodeData {
  if (!task) {
    const label = missingRef?.shortID ?? id.split(":").at(-1) ?? id;
    return {
      label,
      title: label,
      idLabel: label,
      projectLabel,
      qualifiedLabel: label,
      accessibleLabel: `Uncached linked task ${label}`,
      status: "uncached",
      priorityLabel: null,
      isSource: false,
      isSelected: false,
      selectable: false,
      adjacentRelation,
    };
  }

  return {
    label: task.title,
    title: task.title,
    idLabel: task.short_id,
    projectLabel,
    qualifiedLabel: task.qualified_id || task.short_id,
    accessibleLabel: [
      task.uid === sourceUID ? "Source task" : "Task",
      task.uid === selectedUID ? "selected" : "",
      task.title,
      task.qualified_id || task.short_id,
      task.status === "closed" ? `closed${task.closed_reason ? ` ${task.closed_reason}` : ""}` : "open",
      adjacentRelation ? `adjacent ${adjacentRelation}` : "",
    ]
      .filter(Boolean)
      .join(", "),
    status: task.status,
    closedReason: task.closed_reason,
    priorityLabel: priorityLabel(task.priority),
    isSource: task.uid === sourceUID,
    isSelected: task.uid === selectedUID,
    selectable: true,
    adjacentRelation,
  };
}

function selectedAdjacentRelation(
  edge: KataGraphEdge,
  nodeID: string,
  selectedUID: string | null,
): KataGraphAdjacentRelation {
  if (!selectedUID) return null;
  if (edge.source !== selectedUID && edge.target !== selectedUID) return null;
  if (nodeID !== edge.source && nodeID !== edge.target) return null;
  if (nodeID === selectedUID) return null;

  if (edge.data?.kind === "parent") {
    return edge.source === selectedUID ? "child" : "parent";
  }
  if (edge.data?.kind === "blocks") {
    return edge.source === selectedUID ? "blocks" : "blockedBy";
  }
  return "related";
}

function selectedAdjacentRelations(
  edges: readonly KataGraphEdge[],
  selectedUID: string | null,
): Map<string, KataGraphAdjacentRelation> {
  const relations = new Map<string, KataGraphAdjacentRelation>();
  for (const edge of edges) {
    if (edge.source === selectedUID) {
      mergeAdjacentRelation(relations, edge.target, selectedAdjacentRelation(edge, edge.target, selectedUID));
    } else if (edge.target === selectedUID) {
      mergeAdjacentRelation(relations, edge.source, selectedAdjacentRelation(edge, edge.source, selectedUID));
    }
  }
  return relations;
}

function adjacentRelationPriority(relation: KataGraphAdjacentRelation): number {
  if (relation === "blockedBy") return 5;
  if (relation === "blocks") return 4;
  if (relation === "parent") return 3;
  if (relation === "child") return 2;
  if (relation === "related") return 1;
  return 0;
}

function mergeAdjacentRelation(
  relations: Map<string, KataGraphAdjacentRelation>,
  nodeID: string,
  relation: KataGraphAdjacentRelation,
): void {
  const current = relations.get(nodeID) ?? null;
  if (adjacentRelationPriority(relation) > adjacentRelationPriority(current)) {
    relations.set(nodeID, relation);
  }
}

function layoutNode(
  id: string,
  task: KataTaskSummary | undefined,
  position: { x: number; y: number },
  sourceUID: string,
  selectedUID: string | null,
  adjacentRelation: KataGraphAdjacentRelation,
  projectLabel: string,
  missingRef: KataGraphMissingRef | undefined,
): KataGraphNode {
  return {
    id,
    type: "kataTask",
    position,
    data: nodeData(id, task, sourceUID, selectedUID, adjacentRelation, projectLabel, missingRef),
    class: nodeClass(task, sourceUID, selectedUID),
    draggable: false,
    selectable: task !== undefined,
    sourcePosition: Position.Right,
    targetPosition: Position.Left,
    width: KATA_GRAPH_NODE_WIDTH,
    height: KATA_GRAPH_NODE_HEIGHT,
  };
}

function graphPositions(
  ids: readonly string[],
  edges: readonly KataGraphEdge[],
  sourceUID: string,
): Map<string, { x: number; y: number }> {
  const adjacency = new Map<string, Set<string>>();
  for (const id of ids) {
    adjacency.set(id, new Set());
  }
  for (const edge of edges) {
    adjacency.get(edge.source)?.add(edge.target);
    adjacency.get(edge.target)?.add(edge.source);
  }

  const depthByID = new Map<string, number>([[sourceUID, 0]]);
  const queued = [sourceUID];
  while (queued.length > 0) {
    const id = queued.shift()!;
    const nextDepth = (depthByID.get(id) ?? 0) + 1;
    for (const neighbor of adjacency.get(id) ?? []) {
      if (depthByID.has(neighbor)) continue;
      depthByID.set(neighbor, nextDepth);
      queued.push(neighbor);
    }
  }

  const fallbackDepth = Math.max(0, ...depthByID.values()) + 1;
  const layers = new Map<number, string[]>();
  for (const id of ids) {
    const depth = depthByID.get(id) ?? fallbackDepth;
    layers.set(depth, [...(layers.get(depth) ?? []), id]);
  }

  const positions = new Map<string, { x: number; y: number }>();
  for (const [depth, layerIDs] of layers) {
    const topOffset = -((layerIDs.length - 1) * KATA_GRAPH_Y_SPACING) / 2;
    layerIDs.forEach((id, index) => {
      positions.set(id, { x: depth * KATA_GRAPH_X_SPACING, y: topOffset + index * KATA_GRAPH_Y_SPACING });
    });
  }
  return positions;
}

function visibleProjectLabels(entries: readonly [string, KataTaskSummary | undefined][]): Map<string, string> {
  const projectUIDs = new Set<string>();
  const shortIDCounts = new Map<string, number>();
  for (const [, task] of entries) {
    if (!task) continue;
    projectUIDs.add(task.project_uid);
    shortIDCounts.set(task.short_id, (shortIDCounts.get(task.short_id) ?? 0) + 1);
  }

  const labels = new Map<string, string>();
  for (const [id, task] of entries) {
    if (!task) continue;
    if (projectUIDs.size <= 1 && (shortIDCounts.get(task.short_id) ?? 0) <= 1) continue;
    labels.set(id, task.project_name || task.project_uid);
  }
  return labels;
}

function missingProjectLabel(ref: KataGraphMissingRef | undefined, projectLabels: Map<string, string>): string {
  return projectLabels.size > 0 ? (ref?.projectUID ?? "") : "";
}

export function buildKataReachableGraph(input: BuildKataReachableGraphInput): {
  nodes: KataGraphNode[];
  edges: KataGraphEdge[];
  missingRefs: KataGraphMissingRef[];
} {
  const indexes = collectTasks(input.tasks);
  const source = indexes.byUID.get(input.sourceUID);
  if (!source) return { nodes: [], edges: [], missingRefs: [] };

  const queued = [source.uid];
  const seen = new Set<string>();
  const nodeTasks = new Map<string, KataTaskSummary | undefined>([[source.uid, source]]);
  const edges = new Map<string, KataGraphEdge>();
  const missingRefs = new Map<string, KataGraphMissingRef>();

  while (queued.length > 0) {
    const uid = queued.shift()!;
    if (seen.has(uid)) continue;
    seen.add(uid);

    const task = indexes.byUID.get(uid);
    if (!task) continue;

    if (task.parent_short_id) {
      const parent = resolvePeer({ uid: "", short_id: task.parent_short_id }, task.project_uid, indexes);
      includePeer(parent, queued, seen, nodeTasks, missingRefs);
      addEdge(edges, parent.id, task.uid, "parent");
    }

    for (const child of indexes.childrenByParent.get(taskKey(task.project_uid, task.short_id)) ?? []) {
      includePeer(
        { id: child.uid, task: child, projectUID: child.project_uid, shortID: child.short_id },
        queued,
        seen,
        nodeTasks,
        missingRefs,
      );
      addEdge(edges, task.uid, child.uid, "parent");
    }

    for (const peer of task.blocks ?? []) {
      const resolved = resolvePeer(peer, task.project_uid, indexes);
      includePeer(resolved, queued, seen, nodeTasks, missingRefs);
      addEdge(edges, task.uid, resolved.id, "blocks");
    }

    for (const peer of task.blocked_by ?? []) {
      const resolved = resolvePeer(peer, task.project_uid, indexes);
      includePeer(resolved, queued, seen, nodeTasks, missingRefs);
      addEdge(edges, resolved.id, task.uid, "blocks");
    }

    for (const peer of task.related ?? []) {
      const resolved = resolvePeer(peer, task.project_uid, indexes);
      includePeer(resolved, queued, seen, nodeTasks, missingRefs);
      addEdge(edges, task.uid, resolved.id, "related");
    }

    for (const candidate of reversePeerMatches(task, indexes.reverseBlocksByUID, indexes.reverseBlocksByProjectShort)) {
      includePeer(
        { id: candidate.uid, task: candidate, projectUID: candidate.project_uid, shortID: candidate.short_id },
        queued,
        seen,
        nodeTasks,
        missingRefs,
      );
      addEdge(edges, candidate.uid, task.uid, "blocks");
    }
    for (const candidate of reversePeerMatches(
      task,
      indexes.reverseBlockedByUID,
      indexes.reverseBlockedByProjectShort,
    )) {
      includePeer(
        { id: candidate.uid, task: candidate, projectUID: candidate.project_uid, shortID: candidate.short_id },
        queued,
        seen,
        nodeTasks,
        missingRefs,
      );
      addEdge(edges, task.uid, candidate.uid, "blocks");
    }
    for (const candidate of reversePeerMatches(
      task,
      indexes.reverseRelatedByUID,
      indexes.reverseRelatedByProjectShort,
    )) {
      includePeer(
        { id: candidate.uid, task: candidate, projectUID: candidate.project_uid, shortID: candidate.short_id },
        queued,
        seen,
        nodeTasks,
        missingRefs,
      );
      addEdge(edges, candidate.uid, task.uid, "related");
    }

    if (task.uid === source.uid) {
      for (const detailEdge of detailEdges(input.selectedDetail, source.uid, indexes, missingRefs)) {
        edges.set(detailEdge.id, detailEdge);
        includeGraphNode(detailEdge.source, indexes, queued, seen, nodeTasks);
        includeGraphNode(detailEdge.target, indexes, queued, seen, nodeTasks);
      }
    }
  }

  const visibleIDs = new Set<string>();
  const visibleNodeEntries = [...nodeTasks.entries()]
    .filter(([id, task]) => {
      if (task && id !== input.sourceUID && input.hideDone && isDone(task)) return false;
      return true;
    })
    .sort((left, right) => compareGraphNodeEntries(left, right, input.sourceUID));
  for (const [id] of visibleNodeEntries) {
    visibleIDs.add(id);
  }
  const visibleEdges = [...edges.values()]
    .filter((edge) => visibleIDs.has(edge.source) && visibleIDs.has(edge.target))
    .sort((left, right) => left.id.localeCompare(right.id, undefined, { numeric: true, sensitivity: "base" }));
  const positions = graphPositions(
    visibleNodeEntries.map(([id]) => id),
    visibleEdges,
    input.sourceUID,
  );
  const projectLabels = visibleProjectLabels(visibleNodeEntries);
  const adjacentRelations = selectedAdjacentRelations(visibleEdges, input.selectedUID);
  const nodes = visibleNodeEntries.map(([id, task]) => {
    const missingRef = missingRefs.get(id);
    return layoutNode(
      id,
      task,
      positions.get(id) ?? { x: 0, y: 0 },
      input.sourceUID,
      input.selectedUID,
      adjacentRelations.get(id) ?? null,
      task ? (projectLabels.get(id) ?? "") : missingProjectLabel(missingRef, projectLabels),
      missingRef,
    );
  });

  return {
    nodes,
    edges: visibleEdges,
    missingRefs: [...missingRefs.values()].sort((left, right) =>
      `${left.projectUID}:${left.shortID}:${left.uid ?? ""}`.localeCompare(
        `${right.projectUID}:${right.shortID}:${right.uid ?? ""}`,
        undefined,
        { numeric: true, sensitivity: "base" },
      ),
    ),
  };
}
