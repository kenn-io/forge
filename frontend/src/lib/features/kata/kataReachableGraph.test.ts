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

  it("walks parent, child, blocks, blocked_by, and related relationships", () => {
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
      "issue-blocker",
      "issue-child",
      "issue-parent",
      "issue-related",
      "issue-root",
    ]);
    expect(graph.edges.map((edge) => edge.id).sort()).toEqual([
      "blocks:issue-blocker:issue-root",
      "blocks:issue-root:issue-blocked",
      "parent:issue-parent:issue-root",
      "parent:issue-root:issue-child",
      "related:issue-root:issue-related",
    ]);
    expect(graph.edges.find((edge) => edge.id === "blocks:issue-root:issue-blocked")).toMatchObject({
      markerEnd: { type: MarkerType.ArrowClosed, color: "var(--accent-blue)" },
      ariaLabel: "blocks relationship from issue-root to issue-blocked",
    });
    expect(graph.edges.find((edge) => edge.id === "parent:issue-root:issue-child")).toMatchObject({
      markerEnd: { type: MarkerType.ArrowClosed, color: "var(--text-secondary)" },
    });
    expect(graph.nodes.find((node) => node.id === root.uid)).toMatchObject({
      position: { x: 0, y: 0 },
      sourcePosition: Position.Right,
      targetPosition: Position.Left,
    });
    expect(graph.nodes.filter((node) => node.id !== root.uid).every((node) => node.position.x > 0)).toBe(true);
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
  });
});
