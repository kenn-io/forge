import { describe, expect, it } from "vite-plus/test";
import { MarkerType, Position } from "@xyflow/svelte";

import type { KataTaskDetail, KataTaskSummary } from "../../api/kata/taskTypes.js";
import { buildKataReachableGraph } from "./kataReachableGraph.js";

function task(overrides: Partial<KataTaskSummary> = {}): KataTaskSummary {
  const shortID = overrides.short_id ?? "root";
  return {
    id: overrides.id ?? 1,
    uid: overrides.uid ?? "issue-root",
    project_id: overrides.project_id ?? 7,
    project_uid: overrides.project_uid ?? "project-kata",
    project_name: overrides.project_name ?? "Kata",
    short_id: shortID,
    qualified_id: overrides.qualified_id ?? `Kata#${shortID}`,
    title: overrides.title ?? "Root task",
    body: overrides.body,
    status: overrides.status ?? "open",
    metadata: overrides.metadata ?? {},
    revision: overrides.revision ?? 1,
    owner: overrides.owner,
    author: overrides.author ?? "middleman",
    priority: overrides.priority,
    labels: overrides.labels,
    parent: overrides.parent,
    parent_short_id: overrides.parent_short_id,
    blocks: overrides.blocks,
    blocked_by: overrides.blocked_by,
    related: overrides.related,
    child_counts: overrides.child_counts,
    recurrence_id: overrides.recurrence_id,
    occurrence_key: overrides.occurrence_key,
    created_at: overrides.created_at ?? "2026-06-29T12:00:00Z",
    updated_at: overrides.updated_at ?? "2026-06-29T12:00:00Z",
    closed_reason: overrides.closed_reason,
    closed_at: overrides.closed_at,
    deleted_at: overrides.deleted_at,
  };
}

function detail(issue: KataTaskSummary, overrides: Partial<KataTaskDetail> = {}): KataTaskDetail {
  return {
    issue: { ...issue, body: issue.body ?? "" },
    comments: [],
    labels: [],
    links: [],
    children: [],
    ...overrides,
  };
}

function positionsByID(graph: ReturnType<typeof buildKataReachableGraph>): Record<string, { x: number; y: number }> {
  return Object.fromEntries(graph.nodes.map((node) => [node.id, node.position]));
}

describe("buildKataReachableGraph", () => {
  it("returns a source node with task title and priority metadata", () => {
    const source = task({ priority: 0, title: "Ship reachable graph" });
    const graph = buildKataReachableGraph({
      sourceUID: source.uid,
      selectedUID: source.uid,
      tasks: [source],
      selectedDetail: detail(source),
      hideDone: false,
    });

    expect(graph.nodes).toHaveLength(1);
    expect(graph.nodes[0]?.data).toMatchObject({
      title: "Ship reachable graph",
      priorityLabel: "P0",
      status: "open",
      isSource: true,
      isSelected: true,
      selectable: true,
    });
    expect(graph.edges).toEqual([]);
  });

  it("keeps graph node subtitles stable at the short task id", () => {
    const source = task({
      short_id: "s2te",
      qualified_id: "kenn-core#s2te",
      project_name: "kenn-core",
      title: "Decision: daemon IPC boundary",
    });
    const graph = buildKataReachableGraph({
      sourceUID: source.uid,
      selectedUID: source.uid,
      tasks: [source],
      selectedDetail: detail(source),
      hideDone: false,
    });

    expect(graph.nodes[0]?.data).toMatchObject({
      idLabel: "s2te",
      projectLabel: "",
      qualifiedLabel: "kenn-core#s2te",
      accessibleLabel: "Source task, selected, Decision: daemon IPC boundary, kenn-core#s2te, open",
    });
  });

  it("adds project subtitles when visible nodes need cross-project disambiguation", () => {
    const source = task({
      uid: "issue-source",
      short_id: "same",
      qualified_id: "Core#same",
      project_uid: "project-core",
      project_name: "Core",
      related: [{ uid: "issue-peer", short_id: "same" }],
    });
    const peer = task({
      uid: "issue-peer",
      short_id: "same",
      qualified_id: "Platform#same",
      project_uid: "project-platform",
      project_name: "Platform",
    });

    const graph = buildKataReachableGraph({
      sourceUID: source.uid,
      selectedUID: source.uid,
      tasks: [source, peer],
      selectedDetail: detail(source),
      hideDone: false,
    });

    expect(graph.nodes.find((node) => node.id === source.uid)?.data.projectLabel).toBe("Core");
    expect(graph.nodes.find((node) => node.id === peer.uid)?.data.projectLabel).toBe("Platform");
  });

  it("walks directed child, blocks, and related relationships", () => {
    const root = task({
      uid: "issue-root",
      short_id: "root",
      title: "Root",
      parent_short_id: "parent",
      blocks: [{ uid: "issue-blocked", short_id: "blocked" }],
      related: [{ uid: "issue-related", short_id: "related" }],
    });
    const parent = task({ uid: "issue-parent", short_id: "parent", title: "Parent" });
    const child = task({ uid: "issue-child", short_id: "child", title: "Child", parent_short_id: "root" });
    const blocker = task({
      uid: "issue-blocker",
      short_id: "blocker",
      title: "Blocker",
      blocks: [{ uid: "issue-root", short_id: "root" }],
    });
    const blocked = task({ uid: "issue-blocked", short_id: "blocked", title: "Blocked" });
    const related = task({ uid: "issue-related", short_id: "related", title: "Related" });
    const graph = buildKataReachableGraph({
      sourceUID: root.uid,
      selectedUID: root.uid,
      tasks: [root, parent, child, blocker, blocked, related],
      selectedDetail: detail(root),
      hideDone: false,
    });

    expect(graph.nodes.map((node) => node.id).sort()).toEqual([
      "issue-blocked",
      "issue-child",
      "issue-parent",
      "issue-related",
      "issue-root",
    ]);
    expect(graph.edges.map((edge) => edge.id).sort()).toEqual([
      "blocks:issue-root:issue-blocked",
      "parent:issue-parent:issue-root",
      "parent:issue-root:issue-child",
      "related:issue-root:issue-related",
    ]);
    expect(graph.edges.find((edge) => edge.id === "blocks:issue-root:issue-blocked")).toMatchObject({
      markerEnd: { type: MarkerType.ArrowClosed, color: "var(--accent-amber)" },
      ariaLabel: "blocks relationship from issue-root to issue-blocked",
    });
    expect(graph.edges.find((edge) => edge.id === "parent:issue-root:issue-child")).toMatchObject({
      markerEnd: { type: MarkerType.ArrowClosed, color: "var(--text-secondary)" },
    });
    const positions = positionsByID(graph);
    expect(graph.nodes.find((node) => node.id === root.uid)).toMatchObject({
      sourcePosition: Position.Right,
      targetPosition: Position.Left,
    });
    expect(positions[parent.uid]!.x).toBeLessThan(positions[root.uid]!.x);
    expect(positions[blocker.uid]).toBeUndefined();
    expect(positions[child.uid]!.x).toBeGreaterThan(positions[root.uid]!.x);
    expect(positions[blocked.uid]!.x).toBeGreaterThan(positions[root.uid]!.x);
    expect(positions[related.uid]!.x).toBeGreaterThan(positions[root.uid]!.x);
  });

  it("lays compact nodes top-to-bottom when requested", () => {
    const root = task({
      uid: "issue-root",
      short_id: "root",
      title: "Root",
      blocks: [{ uid: "issue-blocked", short_id: "blocked" }],
    });
    const blocked = task({ uid: "issue-blocked", short_id: "blocked", title: "Blocked" });

    const graph = buildKataReachableGraph({
      sourceUID: root.uid,
      selectedUID: root.uid,
      tasks: [root, blocked],
      selectedDetail: detail(root),
      hideDone: false,
      layoutDirection: "TB",
    });

    const positions = positionsByID(graph);
    expect(graph.nodes.find((node) => node.id === root.uid)).toMatchObject({
      sourcePosition: Position.Bottom,
      targetPosition: Position.Top,
      data: { layoutDirection: "TB" },
    });
    expect(positions[blocked.uid]!.y).toBeGreaterThan(positions[root.uid]!.y);
    expect(positions[blocked.uid]!.x).toBe(positions[root.uid]!.x);
  });

  it("deduplicates inverse parent and blocking edge declarations", () => {
    const root = task({
      uid: "issue-root",
      short_id: "root",
      title: "Root",
      parent_short_id: "parent",
      blocks: [{ uid: "issue-blocked", short_id: "blocked" }],
      blocked_by: [{ uid: "issue-blocked", short_id: "blocked" }],
    });
    const parent = task({
      uid: "issue-parent",
      short_id: "parent",
      title: "Parent",
      parent_short_id: "root",
    });
    const blocked = task({
      uid: "issue-blocked",
      short_id: "blocked",
      title: "Blocked",
      blocks: [{ uid: "issue-root", short_id: "root" }],
      blocked_by: [{ uid: "issue-root", short_id: "root" }],
    });

    const graph = buildKataReachableGraph({
      sourceUID: root.uid,
      selectedUID: root.uid,
      tasks: [root, parent, blocked],
      selectedDetail: detail(root),
      hideDone: false,
    });

    expect(graph.edges.map((edge) => edge.id).sort()).toEqual([
      "blocks:issue-root:issue-blocked",
      "parent:issue-parent:issue-root",
    ]);
    expect(graph.nodes.find((node) => node.id === blocked.uid)?.data.adjacentRelation).toBe("blocks");
    expect(graph.nodes.find((node) => node.id === parent.uid)?.data.adjacentRelation).toBe("parent");
  });

  it("deduplicates reciprocal edges without delimiter collisions", () => {
    const root = task({
      uid: "a:blocks:b",
      short_id: "root",
      title: "Root",
      blocks: [
        { uid: "c", short_id: "c" },
        { uid: "b", short_id: "b" },
      ],
    });
    const c = task({ uid: "c", short_id: "c", title: "C", blocks: [{ uid: "a:blocks:b", short_id: "root" }] });
    const b = task({ uid: "b", short_id: "b", title: "B" });

    const graph = buildKataReachableGraph({
      sourceUID: root.uid,
      selectedUID: root.uid,
      tasks: [root, c, b],
      selectedDetail: detail(root),
      hideDone: false,
    });

    expect(graph.edges.map((edge) => edge.id).sort()).toEqual(["blocks:a:blocks:b:b", "blocks:a:blocks:b:c"]);
  });

  it("deduplicates inverse detail links against cached relationship edges", () => {
    const root = task({
      uid: "issue-root",
      short_id: "root",
      title: "Root",
      blocks: [{ uid: "issue-blocked", short_id: "blocked" }],
    });
    const blocked = task({ uid: "issue-blocked", short_id: "blocked", title: "Blocked" });

    const graph = buildKataReachableGraph({
      sourceUID: root.uid,
      selectedUID: root.uid,
      tasks: [root, blocked],
      selectedDetail: detail(root, {
        links: [
          {
            id: 1,
            project_id: root.project_id,
            from: { uid: blocked.uid, short_id: blocked.short_id },
            to: { uid: root.uid, short_id: root.short_id },
            type: "blocks",
            author: "middleman",
            created_at: "2026-06-29T12:00:00Z",
          },
        ],
      }),
      hideDone: false,
    });

    expect(graph.edges.map((edge) => edge.id)).toEqual(["blocks:issue-root:issue-blocked"]);
    expect(graph.nodes.find((node) => node.id === blocked.uid)?.data.adjacentRelation).toBe("blocks");
  });

  it("deduplicates reciprocal detail-only links with stable related direction", () => {
    const root = task({ uid: "issue-root", short_id: "root", title: "Root" });
    const peer = task({ uid: "issue-peer", short_id: "peer", title: "Peer" });

    const graphFromPeerFirst = buildKataReachableGraph({
      sourceUID: root.uid,
      selectedUID: root.uid,
      tasks: [root, peer],
      selectedDetail: detail(root, {
        links: [
          {
            id: 1,
            project_id: root.project_id,
            from: { uid: peer.uid, short_id: peer.short_id },
            to: { uid: root.uid, short_id: root.short_id },
            type: "related",
            author: "middleman",
            created_at: "2026-06-29T12:00:00Z",
          },
          {
            id: 2,
            project_id: root.project_id,
            from: { uid: root.uid, short_id: root.short_id },
            to: { uid: peer.uid, short_id: peer.short_id },
            type: "related",
            author: "middleman",
            created_at: "2026-06-29T12:00:01Z",
          },
        ],
      }),
      hideDone: false,
    });
    const graphFromRootFirst = buildKataReachableGraph({
      sourceUID: root.uid,
      selectedUID: root.uid,
      tasks: [root, peer],
      selectedDetail: detail(root, {
        links: [
          {
            id: 1,
            project_id: root.project_id,
            from: { uid: root.uid, short_id: root.short_id },
            to: { uid: peer.uid, short_id: peer.short_id },
            type: "related",
            author: "middleman",
            created_at: "2026-06-29T12:00:00Z",
          },
          {
            id: 2,
            project_id: root.project_id,
            from: { uid: peer.uid, short_id: peer.short_id },
            to: { uid: root.uid, short_id: root.short_id },
            type: "related",
            author: "middleman",
            created_at: "2026-06-29T12:00:01Z",
          },
        ],
      }),
      hideDone: false,
    });

    expect(graphFromPeerFirst.edges.map((edge) => edge.id)).toEqual(["related:issue-root:issue-peer"]);
    expect(graphFromRootFirst.edges.map((edge) => edge.id)).toEqual(["related:issue-root:issue-peer"]);
  });

  it("normalizes detail parent links from child-to-parent into parent-to-child graph edges", () => {
    const root = task({ uid: "issue-root", short_id: "root", title: "Root" });
    const child = task({ uid: "issue-child", short_id: "child", title: "Child", parent_short_id: "root" });

    const graph = buildKataReachableGraph({
      sourceUID: root.uid,
      selectedUID: root.uid,
      tasks: [root, child],
      selectedDetail: detail(root, {
        links: [
          {
            id: 1,
            project_id: root.project_id,
            from: { uid: child.uid, short_id: child.short_id },
            to: { uid: root.uid, short_id: root.short_id },
            type: "parent",
            author: "middleman",
            created_at: "2026-06-29T12:00:00Z",
          },
        ],
      }),
      hideDone: false,
    });

    expect(graph.edges.map((edge) => edge.id)).toEqual(["parent:issue-root:issue-child"]);
    expect(graph.nodes.find((node) => node.id === child.uid)?.data.adjacentRelation).toBe("child");
    expect(positionsByID(graph)[child.uid]!.x).toBeGreaterThan(positionsByID(graph)[root.uid]!.x);
  });

  it("preserves already canonical parent-to-child detail links", () => {
    const parent = task({ uid: "issue-parent", short_id: "parent", title: "Parent" });
    const child = task({
      uid: "issue-child",
      short_id: "child",
      title: "Child",
      parent_short_id: "parent",
    });

    const graph = buildKataReachableGraph({
      sourceUID: parent.uid,
      selectedUID: parent.uid,
      tasks: [parent, child],
      selectedDetail: detail(parent, {
        links: [
          {
            id: 1,
            project_id: parent.project_id,
            from: { uid: parent.uid, short_id: parent.short_id },
            to: { uid: child.uid, short_id: child.short_id },
            type: "parent",
            author: "middleman",
            created_at: "2026-06-29T12:00:00Z",
          },
        ],
      }),
      hideDone: false,
    });

    expect(graph.edges.map((edge) => edge.id)).toEqual(["parent:issue-parent:issue-child"]);
    expect(graph.nodes.find((node) => node.id === child.uid)?.data.adjacentRelation).toBe("child");
  });

  it("keeps the parent visible when the source detail declares a parent link", () => {
    const parent = task({ uid: "issue-parent", short_id: "parent", title: "Parent" });
    const child = task({ uid: "issue-child", short_id: "child", title: "Child", parent_short_id: "parent" });

    const graph = buildKataReachableGraph({
      sourceUID: child.uid,
      selectedUID: child.uid,
      tasks: [child, parent],
      selectedDetail: detail(child, {
        links: [
          {
            id: 1,
            project_id: child.project_id,
            from: { uid: child.uid, short_id: child.short_id },
            to: { uid: parent.uid, short_id: parent.short_id },
            type: "parent",
            author: "middleman",
            created_at: "2026-06-29T12:00:00Z",
          },
        ],
      }),
      hideDone: false,
      depthLimit: "full",
    });

    expect(graph.nodes.map((node) => node.id).sort()).toEqual(["issue-child", "issue-parent"]);
    expect(graph.edges.map((edge) => edge.id)).toEqual(["parent:issue-parent:issue-child"]);
    expect(graph.nodes.find((node) => node.id === parent.uid)?.data.adjacentRelation).toBe("parent");
  });

  it("uses parent uid refs before short-id parent matching", () => {
    const source = task({ uid: "issue-source", short_id: "source", title: "Source" });
    const parent = task({ uid: "issue-parent", short_id: "parent", title: "Parent" });
    const sameShortID = task({ uid: "issue-wrong-parent", short_id: "parent", title: "Wrong parent" });
    const child = task({
      uid: "issue-child",
      short_id: "child",
      title: "Child",
      parent: { uid: parent.uid, short_id: sameShortID.short_id },
      parent_short_id: sameShortID.short_id,
      related: [{ uid: source.uid, short_id: source.short_id }],
    });

    const graph = buildKataReachableGraph({
      sourceUID: parent.uid,
      selectedUID: child.uid,
      tasks: [source, child, sameShortID, parent],
      selectedDetail: detail(source),
      hideDone: false,
      depthLimit: "full",
    });

    expect(graph.edges.map((edge) => edge.id).sort()).toContain("parent:issue-parent:issue-child");
    expect(graph.edges.map((edge) => edge.id).sort()).not.toContain("parent:issue-wrong-parent:issue-child");
    expect(graph.nodes.find((node) => node.id === parent.uid)?.data.isSource).toBe(true);
  });

  it("does not discover uid-backed children through a colliding parent short id", () => {
    const parent = task({ uid: "issue-parent", short_id: "parent", title: "Parent" });
    const sameShortID = task({ uid: "issue-wrong-parent", short_id: "parent", title: "Wrong parent" });
    const child = task({
      uid: "issue-child",
      short_id: "child",
      title: "Child",
      parent: { uid: parent.uid, short_id: sameShortID.short_id },
      parent_short_id: sameShortID.short_id,
    });

    const graph = buildKataReachableGraph({
      sourceUID: sameShortID.uid,
      selectedUID: sameShortID.uid,
      tasks: [child, sameShortID, parent],
      selectedDetail: detail(sameShortID),
      hideDone: false,
      depthLimit: "full",
    });

    expect(graph.nodes.map((node) => node.id)).toEqual([sameShortID.uid]);
    expect(graph.edges).toEqual([]);
  });

  it("marks nodes adjacent to the selected task by relationship direction", () => {
    const root = task({
      uid: "issue-root",
      short_id: "root",
      title: "Root",
      parent_short_id: "parent",
      blocks: [{ uid: "issue-blocked", short_id: "blocked" }],
      related: [{ uid: "issue-related", short_id: "related" }],
    });
    const parent = task({ uid: "issue-parent", short_id: "parent", title: "Parent" });
    const child = task({ uid: "issue-child", short_id: "child", title: "Child", parent_short_id: "root" });
    const blocker = task({
      uid: "issue-blocker",
      short_id: "blocker",
      title: "Blocker",
      blocks: [{ uid: "issue-root", short_id: "root" }],
    });
    const blocked = task({ uid: "issue-blocked", short_id: "blocked", title: "Blocked" });
    const related = task({ uid: "issue-related", short_id: "related", title: "Related" });

    const graph = buildKataReachableGraph({
      sourceUID: root.uid,
      selectedUID: root.uid,
      tasks: [root, parent, child, blocker, blocked, related],
      selectedDetail: detail(root),
      hideDone: false,
    });

    expect(graph.nodes.find((node) => node.id === root.uid)?.data.adjacentRelation).toBeNull();
    expect(graph.nodes.find((node) => node.id === parent.uid)?.data.adjacentRelation).toBe("parent");
    expect(graph.nodes.find((node) => node.id === child.uid)?.data.adjacentRelation).toBe("child");
    expect(graph.nodes.find((node) => node.id === blocker.uid)).toBeUndefined();
    expect(graph.nodes.find((node) => node.id === blocked.uid)?.data.adjacentRelation).toBe("blocks");
    expect(graph.nodes.find((node) => node.id === related.uid)?.data.adjacentRelation).toBe("related");
  });

  it("uses deterministic precedence when adjacent nodes have multiple relationships", () => {
    const root = task({
      uid: "issue-root",
      short_id: "root",
      title: "Root",
      blocks: [{ uid: "issue-peer", short_id: "peer" }],
      related: [{ uid: "issue-peer", short_id: "peer" }],
    });
    const peer = task({
      uid: "issue-peer",
      short_id: "peer",
      title: "Peer",
    });

    const graph = buildKataReachableGraph({
      sourceUID: root.uid,
      selectedUID: root.uid,
      tasks: [root, peer],
      selectedDetail: detail(root),
      hideDone: false,
    });

    expect(graph.nodes.find((node) => node.id === peer.uid)?.data.adjacentRelation).toBe("blocks");
  });

  it("keeps node positions stable when cached task order changes", () => {
    const root = task({ uid: "issue-root", short_id: "root", title: "Root" });
    const alpha = task({ uid: "issue-alpha", short_id: "alpha", title: "Alpha", parent_short_id: "root" });
    const beta = task({ uid: "issue-beta", short_id: "beta", title: "Beta", parent_short_id: "root" });
    const gamma = task({ uid: "issue-gamma", short_id: "gamma", title: "Gamma", parent_short_id: "root" });

    const forward = buildKataReachableGraph({
      sourceUID: root.uid,
      selectedUID: root.uid,
      tasks: [root, alpha, beta, gamma],
      selectedDetail: detail(root),
      hideDone: false,
    });
    const reversed = buildKataReachableGraph({
      sourceUID: root.uid,
      selectedUID: root.uid,
      tasks: [gamma, beta, alpha, root],
      selectedDetail: detail(root),
      hideDone: false,
    });

    expect(positionsByID(reversed)).toEqual(positionsByID(forward));
  });

  it("uses selected detail links when the source task is selected", () => {
    const root = task({ uid: "issue-root", short_id: "root" });
    const linked = task({
      uid: "issue-linked",
      short_id: "linked",
      title: "Linked task",
      blocks: [{ uid: "issue-follow-up", short_id: "follow-up" }],
    });
    const followUp = task({ uid: "issue-follow-up", short_id: "follow-up", title: "Follow-up task" });
    const graph = buildKataReachableGraph({
      sourceUID: root.uid,
      selectedUID: root.uid,
      tasks: [root, linked, followUp],
      selectedDetail: detail(root, {
        links: [
          {
            id: 1,
            project_id: root.project_id,
            from: { uid: root.uid, short_id: root.short_id },
            to: { uid: linked.uid, short_id: linked.short_id },
            type: "related",
            author: "middleman",
            created_at: "2026-06-29T12:00:00Z",
          },
        ],
      }),
      hideDone: false,
    });

    expect(graph.nodes.map((node) => node.id).sort()).toEqual(["issue-follow-up", "issue-linked", "issue-root"]);
    expect(graph.edges.map((edge) => edge.id).sort()).toEqual([
      "blocks:issue-linked:issue-follow-up",
      "related:issue-root:issue-linked",
    ]);
  });

  it("limits traversal by edge depth", () => {
    const root = task({
      uid: "issue-root",
      short_id: "root",
      title: "Root",
      blocks: [{ uid: "issue-one", short_id: "one" }],
    });
    const one = task({
      uid: "issue-one",
      short_id: "one",
      title: "One edge",
      blocks: [{ uid: "issue-two", short_id: "two" }],
    });
    const two = task({
      uid: "issue-two",
      short_id: "two",
      title: "Two edges",
      blocks: [{ uid: "issue-three", short_id: "three" }],
    });
    const three = task({ uid: "issue-three", short_id: "three", title: "Three edges" });

    const oneEdge = buildKataReachableGraph({
      sourceUID: root.uid,
      selectedUID: root.uid,
      tasks: [root, one, two, three],
      selectedDetail: detail(root),
      hideDone: false,
      depthLimit: "1",
    });
    const twoEdges = buildKataReachableGraph({
      sourceUID: root.uid,
      selectedUID: root.uid,
      tasks: [root, one, two, three],
      selectedDetail: detail(root),
      hideDone: false,
      depthLimit: "2",
    });
    const full = buildKataReachableGraph({
      sourceUID: root.uid,
      selectedUID: root.uid,
      tasks: [root, one, two, three],
      selectedDetail: detail(root),
      hideDone: false,
      depthLimit: "full",
    });

    expect(oneEdge.nodes.map((node) => node.id).sort()).toEqual(["issue-one", "issue-root"]);
    expect(oneEdge.edges.map((edge) => edge.id)).toEqual(["blocks:issue-root:issue-one"]);
    expect(twoEdges.nodes.map((node) => node.id).sort()).toEqual(["issue-one", "issue-root", "issue-two"]);
    expect(twoEdges.edges.map((edge) => edge.id).sort()).toEqual([
      "blocks:issue-one:issue-two",
      "blocks:issue-root:issue-one",
    ]);
    expect(full.nodes.map((node) => node.id).sort()).toEqual(["issue-one", "issue-root", "issue-three", "issue-two"]);
  });

  it("can keep out-of-depth graph context faded", () => {
    const root = task({
      uid: "issue-root",
      short_id: "root",
      title: "Root",
      blocks: [{ uid: "issue-one", short_id: "one" }],
    });
    const one = task({
      uid: "issue-one",
      short_id: "one",
      title: "One edge",
      blocks: [{ uid: "issue-two", short_id: "two" }],
    });
    const two = task({
      uid: "issue-two",
      short_id: "two",
      title: "Two edges",
      blocks: [{ uid: "issue-three", short_id: "three" }],
    });
    const three = task({ uid: "issue-three", short_id: "three", title: "Three edges" });

    const graph = buildKataReachableGraph({
      sourceUID: root.uid,
      selectedUID: root.uid,
      tasks: [root, one, two, three],
      selectedDetail: detail(root),
      hideDone: false,
      depthLimit: "1",
      showDepthContext: true,
    });

    expect(graph.nodes.map((node) => node.id).sort()).toEqual(["issue-one", "issue-root", "issue-three", "issue-two"]);
    expect(graph.nodes.find((node) => node.id === "issue-root")?.data.isDepthContext).toBe(false);
    expect(graph.nodes.find((node) => node.id === "issue-one")?.data.isDepthContext).toBe(false);
    expect(graph.nodes.find((node) => node.id === "issue-two")?.data.isDepthContext).toBe(true);
    expect(graph.nodes.find((node) => node.id === "issue-three")?.data.isDepthContext).toBe(true);
    expect(graph.edges.find((edge) => edge.id === "blocks:issue-root:issue-one")?.data?.isDepthContext).toBeFalsy();
    expect(graph.edges.find((edge) => edge.id === "blocks:issue-one:issue-two")?.data?.isDepthContext).toBe(true);
    expect(graph.edges.find((edge) => edge.id === "blocks:issue-root:issue-one")?.zIndex).toBeGreaterThan(
      graph.edges.find((edge) => edge.id === "blocks:issue-one:issue-two")?.zIndex ?? -1,
    );
    expect(graph.edges.find((edge) => edge.id === "blocks:issue-one:issue-two")?.markerEnd).toMatchObject({
      color: "var(--border-default)",
    });
    expect(graph.edges.map((edge) => edge.data?.isDepthContext ?? false)).toEqual([true, true, false]);
  });

  it("does not request missing refs that only appear in faded depth context", () => {
    const root = task({
      uid: "issue-root",
      short_id: "root",
      title: "Root",
      blocks: [{ uid: "issue-one", short_id: "one" }],
    });
    const one = task({
      uid: "issue-one",
      short_id: "one",
      title: "One edge",
      blocks: [{ uid: "issue-hidden", short_id: "hidden" }],
    });

    const graph = buildKataReachableGraph({
      sourceUID: root.uid,
      selectedUID: root.uid,
      tasks: [root, one],
      selectedDetail: detail(root),
      hideDone: false,
      depthLimit: "1",
      showDepthContext: true,
    });

    expect(graph.nodes.map((node) => node.id).sort()).toEqual(["issue-hidden", "issue-one", "issue-root"]);
    expect(graph.nodes.find((node) => node.id === "issue-hidden")?.data.isDepthContext).toBe(true);
    expect(graph.missingRefs).toEqual([]);
  });

  it("prunes transitive blocking edges from the visualization", () => {
    const root = task({
      uid: "issue-root",
      short_id: "root",
      title: "Root",
      blocks: [
        { uid: "issue-one", short_id: "one" },
        { uid: "issue-two", short_id: "two" },
      ],
    });
    const one = task({
      uid: "issue-one",
      short_id: "one",
      title: "One edge",
      blocks: [{ uid: "issue-two", short_id: "two" }],
    });
    const two = task({ uid: "issue-two", short_id: "two", title: "Two edges" });

    const graph = buildKataReachableGraph({
      sourceUID: root.uid,
      selectedUID: root.uid,
      tasks: [root, one, two],
      selectedDetail: detail(root),
      hideDone: false,
      depthLimit: "full",
    });

    expect(graph.nodes.map((node) => node.id).sort()).toEqual(["issue-one", "issue-root", "issue-two"]);
    expect(graph.edges.map((edge) => edge.id).sort()).toEqual([
      "blocks:issue-one:issue-two",
      "blocks:issue-root:issue-one",
      "blocks:issue-root:issue-two",
    ]);
    expect(graph.layoutEdges.map((edge) => edge.id).sort()).toEqual([
      "blocks:issue-one:issue-two",
      "blocks:issue-root:issue-one",
    ]);
    expect(graph.nodes.find((node) => node.id === "issue-two")?.data.adjacentRelation).toBe("blocks");
  });

  it("keeps rendered direct edges when faded-context layout prunes them", () => {
    const root = task({
      uid: "issue-root",
      short_id: "root",
      title: "Root",
      blocks: [
        { uid: "issue-one", short_id: "one" },
        { uid: "issue-two", short_id: "two" },
      ],
    });
    const one = task({
      uid: "issue-one",
      short_id: "one",
      title: "One edge",
      blocks: [{ uid: "issue-two", short_id: "two" }],
    });
    const two = task({ uid: "issue-two", short_id: "two", title: "Two edges" });

    const graph = buildKataReachableGraph({
      sourceUID: root.uid,
      selectedUID: root.uid,
      tasks: [root, one, two],
      selectedDetail: detail(root),
      hideDone: false,
      depthLimit: "1",
      showDepthContext: true,
    });

    expect(graph.edges.find((edge) => edge.id === "blocks:issue-one:issue-two")?.data?.isDepthContext).toBe(true);
    expect(graph.edges.map((edge) => edge.id).sort()).toEqual([
      "blocks:issue-one:issue-two",
      "blocks:issue-root:issue-one",
      "blocks:issue-root:issue-two",
    ]);
    expect(graph.layoutEdges.map((edge) => edge.id).sort()).toEqual([
      "blocks:issue-one:issue-two",
      "blocks:issue-root:issue-one",
    ]);
  });

  it("keeps the faded context layout stable when selecting nodes in bounded depth", () => {
    const root = task({
      uid: "issue-root",
      short_id: "root",
      title: "Root",
      blocks: [{ uid: "issue-one", short_id: "one" }],
    });
    const one = task({
      uid: "issue-one",
      short_id: "one",
      title: "One edge",
      blocks: [{ uid: "issue-two", short_id: "two" }],
    });
    const two = task({
      uid: "issue-two",
      short_id: "two",
      title: "Two edges",
      blocks: [{ uid: "issue-three", short_id: "three" }],
    });
    const three = task({ uid: "issue-three", short_id: "three", title: "Three edges" });

    const rootSelected = buildKataReachableGraph({
      sourceUID: root.uid,
      selectedUID: root.uid,
      tasks: [root, one, two, three],
      selectedDetail: detail(root),
      hideDone: false,
      depthLimit: "1",
      showDepthContext: true,
    });
    const twoSelected = buildKataReachableGraph({
      sourceUID: root.uid,
      selectedUID: two.uid,
      tasks: [root, one, two, three],
      selectedDetail: detail(root),
      hideDone: false,
      depthLimit: "1",
      showDepthContext: true,
    });

    expect(twoSelected.nodes.map((node) => node.id)).toEqual(rootSelected.nodes.map((node) => node.id));
    expect(twoSelected.layoutEdges.map((edge) => edge.id)).toEqual(rootSelected.layoutEdges.map((edge) => edge.id));
    expect(positionsByID(twoSelected)).toEqual(positionsByID(rootSelected));
    expect(twoSelected.nodes.find((node) => node.id === two.uid)?.data.isSelected).toBe(true);
    expect(twoSelected.nodes.find((node) => node.id === three.uid)?.data.isDepthContext).toBe(false);
    expect(twoSelected.nodes.find((node) => node.id === root.uid)?.data.isDepthContext).toBe(true);
  });

  it("uses the selected task as the bounded depth root without faded context", () => {
    const root = task({
      uid: "issue-root",
      short_id: "root",
      title: "Root",
      blocks: [{ uid: "issue-one", short_id: "one" }],
    });
    const one = task({
      uid: "issue-one",
      short_id: "one",
      title: "One edge",
      blocks: [{ uid: "issue-two", short_id: "two" }],
    });
    const two = task({
      uid: "issue-two",
      short_id: "two",
      title: "Two edges",
      blocks: [{ uid: "issue-three", short_id: "three" }],
    });
    const three = task({ uid: "issue-three", short_id: "three", title: "Three edges" });

    const oneEdge = buildKataReachableGraph({
      sourceUID: root.uid,
      selectedUID: two.uid,
      tasks: [root, one, two, three],
      selectedDetail: detail(root),
      hideDone: false,
      depthLimit: "1",
    });
    const twoEdges = buildKataReachableGraph({
      sourceUID: root.uid,
      selectedUID: two.uid,
      tasks: [root, one, two, three],
      selectedDetail: detail(root),
      hideDone: false,
      depthLimit: "2",
    });
    const full = buildKataReachableGraph({
      sourceUID: root.uid,
      selectedUID: two.uid,
      tasks: [root, one, two, three],
      selectedDetail: detail(root),
      hideDone: false,
      depthLimit: "full",
    });

    expect(oneEdge.nodes.map((node) => node.id).sort()).toEqual(["issue-three", "issue-two"]);
    expect(oneEdge.edges.map((edge) => edge.id).sort()).toEqual(["blocks:issue-two:issue-three"]);
    expect(twoEdges.nodes.map((node) => node.id).sort()).toEqual(["issue-three", "issue-two"]);
    expect(positionsByID(oneEdge)[one.uid]).toBeUndefined();
    expect(positionsByID(oneEdge)[three.uid]!.x).toBeGreaterThan(positionsByID(oneEdge)[two.uid]!.x);
    expect(positionsByID(full)[root.uid]!.x).toBe(0);
  });

  it("does not include inferred reverse edges in bounded depth", () => {
    const selected = task({
      uid: "issue-selected",
      short_id: "selected",
      title: "Selected",
      blocks: [{ uid: "issue-one", short_id: "one" }],
    });
    const one = task({
      uid: "issue-one",
      short_id: "one",
      title: "One edge",
      blocks: [{ uid: "issue-two", short_id: "two" }],
    });
    const two = task({
      uid: "issue-two",
      short_id: "two",
      title: "Two edges",
      blocks: [{ uid: "issue-three", short_id: "three" }],
    });
    const three = task({ uid: "issue-three", short_id: "three", title: "Three edges" });
    const parent = task({ uid: "issue-parent", short_id: "parent", title: "Parent" });
    const childWithParent = task({
      uid: selected.uid,
      short_id: selected.short_id,
      title: selected.title,
      parent: { uid: parent.uid, short_id: parent.short_id },
      parent_short_id: parent.short_id,
      blocks: selected.blocks,
    });
    const incomingBlocker = task({
      uid: "issue-incoming-blocker",
      short_id: "incoming-blocker",
      title: "Incoming blocker",
      blocks: [{ uid: selected.uid, short_id: selected.short_id }],
    });

    const graph = buildKataReachableGraph({
      sourceUID: selected.uid,
      selectedUID: selected.uid,
      tasks: [childWithParent, parent, incomingBlocker, one, two, three],
      selectedDetail: detail(childWithParent),
      hideDone: false,
      depthLimit: "2",
    });

    expect(graph.nodes.map((node) => node.id).sort()).toEqual([
      "issue-one",
      "issue-parent",
      "issue-selected",
      "issue-two",
    ]);
    expect(graph.edges.map((edge) => edge.id).sort()).toEqual([
      "blocks:issue-one:issue-two",
      "blocks:issue-selected:issue-one",
      "parent:issue-parent:issue-selected",
    ]);
  });

  it("filters done nodes without hiding other closed nodes", () => {
    const root = task({
      uid: "issue-root",
      short_id: "root",
      blocks: [
        { uid: "issue-done", short_id: "done" },
        { uid: "issue-wontfix", short_id: "wontfix" },
      ],
    });
    const done = task({
      uid: "issue-done",
      short_id: "done",
      title: "Done",
      status: "closed",
      closed_reason: "done",
    });
    const wontfix = task({
      uid: "issue-wontfix",
      short_id: "wontfix",
      title: "Wontfix",
      status: "closed",
      closed_reason: "wontfix",
    });
    const graph = buildKataReachableGraph({
      sourceUID: root.uid,
      selectedUID: root.uid,
      tasks: [root, done, wontfix],
      selectedDetail: detail(root),
      hideDone: true,
    });

    expect(graph.nodes.map((node) => node.id).sort()).toEqual(["issue-root", "issue-wontfix"]);
    expect(graph.edges.map((edge) => edge.id)).toEqual(["blocks:issue-root:issue-wontfix"]);
  });

  it("keeps a done source visible when filtering done peers", () => {
    const source = task({
      uid: "issue-done-source",
      short_id: "done-source",
      title: "Done source",
      status: "closed",
      closed_reason: "done",
      blocks: [{ uid: "issue-done-peer", short_id: "done-peer" }],
    });
    const donePeer = task({
      uid: "issue-done-peer",
      short_id: "done-peer",
      title: "Done peer",
      status: "closed",
      closed_reason: "done",
    });

    const graph = buildKataReachableGraph({
      sourceUID: source.uid,
      selectedUID: source.uid,
      tasks: [source, donePeer],
      selectedDetail: detail(source),
      hideDone: true,
    });

    expect(graph.nodes.map((node) => node.id)).toEqual([source.uid]);
    expect(graph.edges).toEqual([]);
  });

  it("does not resolve ambiguous short ids to a random cached task", () => {
    const root = task({ uid: "issue-root", short_id: "root", blocks: [{ uid: "", short_id: "dup" }] });
    const first = task({ uid: "issue-first", short_id: "dup", title: "First", project_uid: "project-kata" });
    const second = task({ uid: "issue-second", short_id: "dup", title: "Second", project_uid: "project-kata" });
    const graph = buildKataReachableGraph({
      sourceUID: root.uid,
      selectedUID: root.uid,
      tasks: [root, first, second],
      selectedDetail: detail(root),
      hideDone: false,
    });

    expect(graph.nodes.map((node) => node.id)).toEqual(["issue-root", "uncached:project-kata:dup"]);
    expect(graph.nodes[1]?.data).toMatchObject({ title: "dup", selectable: false });
    expect(graph.missingRefs).toEqual([{ uid: undefined, projectUID: "project-kata", shortID: "dup" }]);
  });

  it("treats present peer uids as authoritative over cached short-id matches", () => {
    const root = task({ uid: "issue-root", short_id: "root", blocks: [{ uid: "issue-real", short_id: "dup" }] });
    const wrongCachedTask = task({ uid: "issue-wrong", short_id: "dup", title: "Wrong cached task" });

    const graph = buildKataReachableGraph({
      sourceUID: root.uid,
      selectedUID: root.uid,
      tasks: [root, wrongCachedTask],
      selectedDetail: detail(root),
      hideDone: false,
    });

    expect(graph.nodes.map((node) => node.id)).toEqual(["issue-root", "issue-real"]);
    expect(graph.nodes.find((node) => node.id === "issue-real")?.data).toMatchObject({
      title: "dup",
      selectable: false,
    });
    expect(graph.nodes.some((node) => node.id === wrongCachedTask.uid)).toBe(false);
    expect(graph.missingRefs).toEqual([{ uid: "issue-real", projectUID: "project-kata", shortID: "dup" }]);
  });

  it("does not reverse-match uid-backed peers through cached short-id collisions", () => {
    const root = task({ uid: "issue-root", short_id: "root", blocks: [{ uid: "issue-real", short_id: "dup" }] });
    const wrongCachedTask = task({ uid: "issue-wrong", short_id: "dup", title: "Wrong cached task" });

    const graph = buildKataReachableGraph({
      sourceUID: wrongCachedTask.uid,
      selectedUID: wrongCachedTask.uid,
      tasks: [root, wrongCachedTask],
      selectedDetail: detail(wrongCachedTask),
      hideDone: false,
    });

    expect(graph.nodes.map((node) => node.id)).toEqual([wrongCachedTask.uid]);
    expect(graph.edges).toEqual([]);
    expect(graph.missingRefs).toEqual([]);
  });

  it("carries unresolved peer uids for background graph fetching", () => {
    const root = task({
      uid: "issue-root",
      short_id: "root",
      blocks: [{ uid: "issue-missing", short_id: "missing" }],
    });
    const graph = buildKataReachableGraph({
      sourceUID: root.uid,
      selectedUID: root.uid,
      tasks: [root],
      selectedDetail: detail(root),
      hideDone: false,
    });

    expect(graph.nodes.map((node) => node.id)).toEqual(["issue-root", "issue-missing"]);
    expect(graph.missingRefs).toEqual([{ uid: "issue-missing", projectUID: "project-kata", shortID: "missing" }]);

    const populated = buildKataReachableGraph({
      sourceUID: root.uid,
      selectedUID: root.uid,
      tasks: [root, task({ uid: "issue-missing", short_id: "missing", title: "Fetched task" })],
      selectedDetail: detail(root),
      hideDone: false,
    });

    expect(populated.nodes.map((node) => node.id)).toEqual(["issue-root", "issue-missing"]);
    expect(populated.nodes.find((node) => node.id === "issue-missing")?.data.title).toBe("Fetched task");
  });
});
