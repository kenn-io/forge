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
  isDepthContext: boolean;
  onSelect?: ((uid: string) => void) | undefined;
  adjacentRelation: KataGraphAdjacentRelation;
  layoutDirection: KataGraphLayoutDirection;
}

export type KataGraphAdjacentRelation = "blocks" | "blockedBy" | "child" | "parent" | "related" | null;
export type KataGraphDepthLimit = "full" | "1" | "2" | "3";
export type KataGraphLayoutDirection = "LR" | "TB";
export type KataGraphNode = Node<KataGraphNodeData, "kataTask">;
export type KataGraphEdge = Edge<KataGraphEdgeData>;

export interface KataGraphMissingRef {
  uid?: string | undefined;
  projectUID: string;
  shortID: string;
}

interface KataGraphEdgeData extends Record<string, unknown> {
  kind: GraphEdgeKind;
  isDepthContext?: boolean | undefined;
}

export interface BuildKataReachableGraphInput {
  sourceUID: string;
  selectedUID: string | null;
  tasks: readonly KataTaskSummary[];
  selectedDetail?: KataTaskDetail | null | undefined;
  hideDone: boolean;
  depthLimit?: KataGraphDepthLimit | undefined;
  showDepthContext?: boolean | undefined;
  layoutDirection?: KataGraphLayoutDirection | undefined;
}

interface TaskIndexes {
  byUID: Map<string, KataTaskSummary>;
  byProjectShort: Map<string, KataTaskSummary[]>;
  childrenByParentUID: Map<string, KataTaskSummary[]>;
  childrenByParent: Map<string, KataTaskSummary[]>;
}

interface ResolvedPeer {
  id: string;
  task?: KataTaskSummary | undefined;
  uid?: string | undefined;
  projectUID: string;
  shortID: string;
}

interface QueuedGraphTask {
  uid: string;
  depth: number;
}

interface CollectedGraph {
  nodeTasks: Map<string, KataTaskSummary | undefined>;
  edges: Map<string, KataGraphEdge>;
  missingRefs: Map<string, KataGraphMissingRef>;
}

type GraphEdgeKind = "parent" | "blocks" | "related";

const KATA_GRAPH_NODE_WIDTH = 250;
const KATA_GRAPH_NODE_HEIGHT = 74;
const KATA_GRAPH_X_SPACING = 320;
const KATA_GRAPH_Y_SPACING = 108;

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

function maxTraversalDepth(limit: KataGraphDepthLimit | undefined): number {
  return limit === undefined || limit === "full" ? Number.POSITIVE_INFINITY : Number(limit);
}

function hasBoundedDepth(limit: KataGraphDepthLimit | undefined): boolean {
  return limit !== undefined && limit !== "full";
}

function collectTasks(tasks: readonly KataTaskSummary[]): TaskIndexes {
  const byUID = new Map<string, KataTaskSummary>();
  const byProjectShort = new Map<string, KataTaskSummary[]>();
  const childrenByParentUID = new Map<string, KataTaskSummary[]>();
  const childrenByParent = new Map<string, KataTaskSummary[]>();
  for (const task of tasks) {
    byUID.set(task.uid, task);
    const key = taskKey(task.project_uid, task.short_id);
    byProjectShort.set(key, [...(byProjectShort.get(key) ?? []), task]);
    if (task.parent?.uid) {
      childrenByParentUID.set(task.parent.uid, [...(childrenByParentUID.get(task.parent.uid) ?? []), task]);
    }
    if (task.parent_short_id && !task.parent?.uid) {
      const parentKey = taskKey(task.project_uid, task.parent_short_id);
      childrenByParent.set(parentKey, [...(childrenByParent.get(parentKey) ?? []), task]);
    }
  }
  return {
    byUID,
    byProjectShort,
    childrenByParentUID,
    childrenByParent,
  };
}

function resolvePeer(peer: KataLinkPeer, projectUID: string, indexes: TaskIndexes): ResolvedPeer {
  if (peer.uid) {
    const byUID = indexes.byUID.get(peer.uid);
    if (byUID) {
      return { id: byUID.uid, task: byUID, projectUID: byUID.project_uid, shortID: byUID.short_id };
    }
    return {
      id: peer.uid,
      uid: peer.uid,
      projectUID,
      shortID: peer.short_id,
    };
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
    kind === "blocks" ? "var(--accent-amber)" : kind === "parent" ? "var(--text-secondary)" : "var(--text-muted)";
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
    selectable: false,
    zIndex: 2,
  };
}

function edgePairID(source: string, target: string, kind: GraphEdgeKind): string {
  return JSON.stringify([kind, ...[source, target].sort((left, right) => left.localeCompare(right))]);
}

function addEdge(edges: Map<string, KataGraphEdge>, source: string, target: string, kind: GraphEdgeKind): void {
  const next = makeEdge(source, target, kind);
  if (edges.has(next.id)) return;
  const nextPairID = edgePairID(source, target, kind);
  for (const edge of edges.values()) {
    if (edge.data?.kind === kind && edgePairID(edge.source, edge.target, kind) === nextPairID) {
      return;
    }
  }
  edges.set(next.id, next);
}

function detailEdges(
  detail: KataTaskDetail | null | undefined,
  sourceUID: string,
  indexes: TaskIndexes,
  missingRefs: Map<string, KataGraphMissingRef>,
): KataGraphEdge[] {
  if (!detail || detail.issue.uid !== sourceUID) return [];
  return detail.links.flatMap((link) =>
    resolveDetailLink(link, detail.issue.project_uid, sourceUID, indexes, missingRefs),
  );
}

function resolveDetailLink(
  link: KataTaskLink,
  projectUID: string,
  sourceUID: string,
  indexes: TaskIndexes,
  missingRefs: Map<string, KataGraphMissingRef>,
): KataGraphEdge[] {
  const from = resolvePeer(link.from, projectUID, indexes);
  const to = resolvePeer(link.to, projectUID, indexes);
  rememberMissingRef(from, missingRefs);
  rememberMissingRef(to, missingRefs);
  if (link.type === "parent") {
    const [parent, child] = parentDetailEndpoints(from, to);
    return [makeEdge(parent.id, child.id, "parent")];
  }
  if (link.type === "blocks") return [makeEdge(from.id, to.id, "blocks")];
  if (from.id === sourceUID) return [makeEdge(from.id, to.id, "related")];
  if (to.id === sourceUID) return [makeEdge(to.id, from.id, "related")];
  return [makeEdge(from.id, to.id, "related")];
}

function isDeclaredParentOf(parent: ResolvedPeer, child: ResolvedPeer): boolean {
  if (!child.task) return false;
  if (child.task.parent?.uid) return child.task.parent.uid === parent.id;
  return child.task.project_uid === parent.projectUID && child.task.parent_short_id === parent.shortID;
}

function parentDetailEndpoints(from: ResolvedPeer, to: ResolvedPeer): [parent: ResolvedPeer, child: ResolvedPeer] {
  if (isDeclaredParentOf(from, to)) return [from, to];
  if (isDeclaredParentOf(to, from)) return [to, from];
  return [from, to];
}

function rememberMissingRef(peer: ResolvedPeer, missingRefs: Map<string, KataGraphMissingRef>): void {
  if (peer.task) return;
  missingRefs.set(peer.id, { uid: peer.uid, projectUID: peer.projectUID, shortID: peer.shortID });
}

function includePeer(
  peer: ResolvedPeer,
  queued: QueuedGraphTask[],
  seen: Set<string>,
  nodeTasks: Map<string, KataTaskSummary | undefined>,
  missingRefs: Map<string, KataGraphMissingRef>,
  depth: number,
): void {
  nodeTasks.set(peer.id, peer.task);
  if (peer.task && !seen.has(peer.task.uid)) {
    queued.push({ uid: peer.task.uid, depth });
  } else {
    rememberMissingRef(peer, missingRefs);
  }
}

function includeGraphNode(
  id: string,
  indexes: TaskIndexes,
  queued: QueuedGraphTask[],
  seen: Set<string>,
  nodeTasks: Map<string, KataTaskSummary | undefined>,
  depth: number,
): void {
  const task = indexes.byUID.get(id);
  nodeTasks.set(id, task);
  if (task && !seen.has(task.uid)) {
    queued.push({ uid: task.uid, depth });
  }
}

function nodeClass(
  task: KataTaskSummary | undefined,
  sourceUID: string,
  selectedUID: string | null,
  isDepthContext: boolean,
): string {
  return [
    "kata-graph-node",
    task?.status === "closed" ? "kata-graph-node--closed" : "kata-graph-node--open",
    task?.uid === sourceUID ? "kata-graph-node--source" : "",
    task?.uid === selectedUID ? "kata-graph-node--selected" : "",
    task ? "" : "kata-graph-node--uncached",
    isDepthContext ? "kata-graph-node--depth-context" : "",
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
  layoutDirection: KataGraphLayoutDirection,
  isDepthContext: boolean,
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
      isDepthContext,
      adjacentRelation,
      layoutDirection,
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
      isDepthContext ? "outside depth context" : "",
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
    isDepthContext,
    adjacentRelation,
    layoutDirection,
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
  layoutDirection: KataGraphLayoutDirection,
  isDepthContext: boolean,
): KataGraphNode {
  const sourcePosition = layoutDirection === "TB" ? Position.Bottom : Position.Right;
  const targetPosition = layoutDirection === "TB" ? Position.Top : Position.Left;
  return {
    id,
    type: "kataTask",
    position,
    data: nodeData(
      id,
      task,
      sourceUID,
      selectedUID,
      adjacentRelation,
      projectLabel,
      missingRef,
      layoutDirection,
      isDepthContext,
    ),
    class: nodeClass(task, sourceUID, selectedUID, isDepthContext),
    draggable: false,
    selectable: task !== undefined,
    sourcePosition,
    targetPosition,
    width: KATA_GRAPH_NODE_WIDTH,
    height: KATA_GRAPH_NODE_HEIGHT,
  };
}

function graphPositions(
  entries: readonly [string, KataTaskSummary | undefined][],
  edges: readonly KataGraphEdge[],
  layoutDirection: KataGraphLayoutDirection,
): Map<string, { x: number; y: number }> {
  const ids = entries.map(([id]) => id);
  const idSet = new Set(ids);
  const stableOrder = new Map(ids.map((id, index) => [id, index]));
  const outgoing = new Map<string, string[]>();
  const indegree = new Map<string, number>();
  for (const id of ids) {
    outgoing.set(id, []);
    indegree.set(id, 0);
  }

  for (const edge of edges) {
    if (!idSet.has(edge.source) || !idSet.has(edge.target)) continue;
    outgoing.set(edge.source, [...(outgoing.get(edge.source) ?? []), edge.target]);
    indegree.set(edge.target, (indegree.get(edge.target) ?? 0) + 1);
  }

  const compareByStableOrder = (left: string, right: string) =>
    (stableOrder.get(left) ?? 0) - (stableOrder.get(right) ?? 0);
  const ready = ids.filter((id) => (indegree.get(id) ?? 0) === 0).sort(compareByStableOrder);
  const topoOrder: string[] = [];

  while (ready.length > 0) {
    const id = ready.shift()!;
    topoOrder.push(id);
    for (const target of [...(outgoing.get(id) ?? [])].sort(compareByStableOrder)) {
      const nextIndegree = (indegree.get(target) ?? 0) - 1;
      indegree.set(target, nextIndegree);
      if (nextIndegree === 0) {
        ready.push(target);
        ready.sort(compareByStableOrder);
      }
    }
  }

  const emitted = new Set(topoOrder);
  for (const id of ids) {
    if (!emitted.has(id)) topoOrder.push(id);
  }

  const rankByID = new Map(ids.map((id) => [id, 0]));
  for (const id of topoOrder) {
    const nextRank = (rankByID.get(id) ?? 0) + 1;
    for (const target of outgoing.get(id) ?? []) {
      rankByID.set(target, Math.max(rankByID.get(target) ?? 0, nextRank));
    }
  }

  const layers = new Map<number, string[]>();
  for (const id of topoOrder) {
    const rank = rankByID.get(id) ?? 0;
    layers.set(rank, [...(layers.get(rank) ?? []), id]);
  }

  const positions = new Map<string, { x: number; y: number }>();
  for (const [depth, layerIDs] of layers) {
    const layerOffset =
      layoutDirection === "TB"
        ? -((layerIDs.length - 1) * KATA_GRAPH_X_SPACING) / 2
        : -((layerIDs.length - 1) * KATA_GRAPH_Y_SPACING) / 2;
    layerIDs.forEach((id, index) => {
      if (layoutDirection === "TB") {
        positions.set(id, { x: layerOffset + index * KATA_GRAPH_X_SPACING, y: depth * KATA_GRAPH_Y_SPACING });
      } else {
        positions.set(id, { x: depth * KATA_GRAPH_X_SPACING, y: layerOffset + index * KATA_GRAPH_Y_SPACING });
      }
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

function collectReachableGraph(
  input: BuildKataReachableGraphInput,
  indexes: TaskIndexes,
  source: KataTaskSummary,
  traversalRoot: KataTaskSummary,
  depthLimit: number,
): CollectedGraph {
  const queued: QueuedGraphTask[] = [{ uid: traversalRoot.uid, depth: 0 }];
  const seen = new Set<string>();
  const nodeTasks = new Map<string, KataTaskSummary | undefined>([[traversalRoot.uid, traversalRoot]]);
  const edges = new Map<string, KataGraphEdge>();
  const missingRefs = new Map<string, KataGraphMissingRef>();

  while (queued.length > 0) {
    const { uid, depth } = queued.shift()!;
    if (seen.has(uid)) continue;
    seen.add(uid);
    if (depth >= depthLimit) continue;
    const nextDepth = depth + 1;

    const task = indexes.byUID.get(uid);
    if (!task) continue;

    const parentPeer = task.parent ?? (task.parent_short_id ? { uid: "", short_id: task.parent_short_id } : null);
    if (parentPeer) {
      const parent = resolvePeer(parentPeer, task.project_uid, indexes);
      includePeer(parent, queued, seen, nodeTasks, missingRefs, nextDepth);
      addEdge(edges, parent.id, task.uid, "parent");
    }

    const children = new Map<string, KataTaskSummary>();
    for (const child of indexes.childrenByParentUID.get(task.uid) ?? []) {
      children.set(child.uid, child);
    }
    for (const child of indexes.childrenByParent.get(taskKey(task.project_uid, task.short_id)) ?? []) {
      children.set(child.uid, child);
    }
    for (const child of children.values()) {
      includePeer(
        { id: child.uid, task: child, projectUID: child.project_uid, shortID: child.short_id },
        queued,
        seen,
        nodeTasks,
        missingRefs,
        nextDepth,
      );
      addEdge(edges, task.uid, child.uid, "parent");
    }

    for (const peer of task.blocks ?? []) {
      const resolved = resolvePeer(peer, task.project_uid, indexes);
      includePeer(resolved, queued, seen, nodeTasks, missingRefs, nextDepth);
      addEdge(edges, task.uid, resolved.id, "blocks");
    }

    for (const peer of task.blocked_by ?? []) {
      const resolved = resolvePeer(peer, task.project_uid, indexes);
      includePeer(resolved, queued, seen, nodeTasks, missingRefs, nextDepth);
      addEdge(edges, resolved.id, task.uid, "blocks");
    }

    for (const peer of task.related ?? []) {
      const resolved = resolvePeer(peer, task.project_uid, indexes);
      includePeer(resolved, queued, seen, nodeTasks, missingRefs, nextDepth);
      addEdge(edges, task.uid, resolved.id, "related");
    }

    if (task.uid === source.uid) {
      for (const detailEdge of detailEdges(input.selectedDetail, source.uid, indexes, missingRefs)) {
        const kind = detailEdge.data?.kind;
        if (!kind) continue;
        addEdge(edges, detailEdge.source, detailEdge.target, kind);
        includeGraphNode(detailEdge.source, indexes, queued, seen, nodeTasks, nextDepth);
        includeGraphNode(detailEdge.target, indexes, queued, seen, nodeTasks, nextDepth);
      }
    }
  }

  return { nodeTasks, edges, missingRefs };
}

function depthContextEdge(edge: KataGraphEdge): KataGraphEdge {
  const kind = edge.data?.kind;
  if (!kind) return edge;
  const className = typeof edge.class === "string" ? edge.class : "";
  const next = {
    ...edge,
    class: `${className} kata-graph-edge--depth-context`.trim(),
    data: { kind, isDepthContext: true },
    zIndex: 0,
  };
  if (!edge.markerEnd || typeof edge.markerEnd !== "object") return next;
  return {
    ...next,
    markerEnd: { ...edge.markerEnd, color: "var(--border-default)" },
  };
}

function compareGraphEdges(left: KataGraphEdge, right: KataGraphEdge): number {
  const leftDepthContext = left.data?.isDepthContext ? 0 : 1;
  const rightDepthContext = right.data?.isDepthContext ? 0 : 1;
  return (
    leftDepthContext - rightDepthContext ||
    left.id.localeCompare(right.id, undefined, { numeric: true, sensitivity: "base" })
  );
}

function hasAlternateBlockPath(
  source: string,
  target: string,
  excludedEdgeID: string,
  outgoing: ReadonlyMap<string, readonly KataGraphEdge[]>,
  depthContext: boolean,
): boolean {
  const queued = [...(outgoing.get(source) ?? [])].filter(
    (edge) => edge.id !== excludedEdgeID && Boolean(edge.data?.isDepthContext) === depthContext,
  );
  const seen = new Set<string>([source]);
  while (queued.length > 0) {
    const edge = queued.shift()!;
    if (edge.target === target) return true;
    if (seen.has(edge.target)) continue;
    seen.add(edge.target);
    queued.push(
      ...(outgoing.get(edge.target) ?? []).filter(
        (next) => next.id !== excludedEdgeID && Boolean(next.data?.isDepthContext) === depthContext,
      ),
    );
  }
  return false;
}

function pruneTransitiveBlockEdges(edges: readonly KataGraphEdge[]): KataGraphEdge[] {
  const outgoing = new Map<string, KataGraphEdge[]>();
  for (const edge of edges) {
    if (edge.data?.kind !== "blocks") continue;
    outgoing.set(edge.source, [...(outgoing.get(edge.source) ?? []), edge]);
  }
  return edges.filter((edge) => {
    if (edge.data?.kind !== "blocks") return true;
    return !hasAlternateBlockPath(edge.source, edge.target, edge.id, outgoing, Boolean(edge.data?.isDepthContext));
  });
}

function sortedMissingRefs(refs: Iterable<KataGraphMissingRef>): KataGraphMissingRef[] {
  return [...refs].sort((left, right) =>
    `${left.projectUID}:${left.shortID}:${left.uid ?? ""}`.localeCompare(
      `${right.projectUID}:${right.shortID}:${right.uid ?? ""}`,
      undefined,
      { numeric: true, sensitivity: "base" },
    ),
  );
}

export function buildKataReachableGraph(input: BuildKataReachableGraphInput): {
  nodes: KataGraphNode[];
  edges: KataGraphEdge[];
  layoutEdges: KataGraphEdge[];
  missingRefs: KataGraphMissingRef[];
} {
  const indexes = collectTasks(input.tasks);
  const source = indexes.byUID.get(input.sourceUID);
  if (!source) return { nodes: [], edges: [], layoutEdges: [], missingRefs: [] };

  const layoutDirection = input.layoutDirection ?? "LR";
  const depthLimit = maxTraversalDepth(input.depthLimit);
  const selectedTask = input.selectedUID ? indexes.byUID.get(input.selectedUID) : undefined;
  const activeTraversalRoot = hasBoundedDepth(input.depthLimit) && selectedTask ? selectedTask : source;
  const activeGraph = collectReachableGraph(input, indexes, source, activeTraversalRoot, depthLimit);
  const showDepthContext = Boolean(hasBoundedDepth(input.depthLimit) && input.showDepthContext);
  const graph = showDepthContext
    ? collectReachableGraph(input, indexes, source, source, Number.POSITIVE_INFINITY)
    : activeGraph;
  const activeIDs = new Set(activeGraph.nodeTasks.keys());
  const activeEdgeIDs = new Set(activeGraph.edges.keys());

  const visibleIDs = new Set<string>();
  const visibleNodeEntries = [...graph.nodeTasks.entries()]
    .filter(([id, task]) => {
      if (task && id !== input.sourceUID && id !== activeTraversalRoot.uid && input.hideDone && isDone(task)) {
        return false;
      }
      return true;
    })
    .sort((left, right) => compareGraphNodeEntries(left, right, input.sourceUID));
  for (const [id] of visibleNodeEntries) {
    visibleIDs.add(id);
  }
  const visibleRelationshipEdges = [...graph.edges.values()]
    .filter((edge) => visibleIDs.has(edge.source) && visibleIDs.has(edge.target))
    .map((edge) =>
      showDepthContext && (!activeEdgeIDs.has(edge.id) || !activeIDs.has(edge.source) || !activeIDs.has(edge.target))
        ? depthContextEdge(edge)
        : edge,
    );
  const visibleEdges = [...visibleRelationshipEdges].sort(compareGraphEdges);
  const layoutRelationshipEdges = showDepthContext
    ? [...graph.edges.values()].filter((edge) => visibleIDs.has(edge.source) && visibleIDs.has(edge.target))
    : visibleRelationshipEdges;
  const layoutEdges = pruneTransitiveBlockEdges(layoutRelationshipEdges).sort(compareGraphEdges);
  const positions = graphPositions(visibleNodeEntries, layoutEdges, layoutDirection);
  const projectLabels = visibleProjectLabels(visibleNodeEntries);
  const adjacentRelations = selectedAdjacentRelations(visibleRelationshipEdges, input.selectedUID);
  const nodes = visibleNodeEntries.map(([id, task]) => {
    const missingRef = graph.missingRefs.get(id);
    return layoutNode(
      id,
      task,
      positions.get(id) ?? { x: 0, y: 0 },
      input.sourceUID,
      input.selectedUID,
      adjacentRelations.get(id) ?? null,
      task ? (projectLabels.get(id) ?? "") : missingProjectLabel(missingRef, projectLabels),
      missingRef,
      layoutDirection,
      showDepthContext && !activeIDs.has(id),
    );
  });

  return {
    nodes,
    edges: visibleEdges,
    layoutEdges,
    missingRefs: sortedMissingRefs(
      [...activeGraph.missingRefs.entries()].filter(([id]) => visibleIDs.has(id)).map(([, ref]) => ref),
    ),
  };
}
