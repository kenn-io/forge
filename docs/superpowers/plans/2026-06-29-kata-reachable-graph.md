# Kata Reachable Graph Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add a `svelte-flow` reachable graph view launched from a Kata task that replaces the task list pane while preserving normal task detail selection.

**Architecture:** Keep graph derivation frontend-local by adding a task summary cache to `KataWorkspaceStore`. A pure graph builder converts cached task summaries and the active detail links into stable Svelte Flow nodes and edges. `KataWorkspace.svelte` owns a list-vs-graph pane mode, while `KataIssueList.svelte`, `KataIssueDetail.svelte`, and `KataReachableGraph.svelte` provide the UI.

**Tech Stack:** Svelte 5 runes, TypeScript, `@xyflow/svelte`, lucide-svelte icons, vite-plus/Vitest with Testing Library Svelte.

---

## File Structure

- Create: `frontend/src/lib/features/kata/kataReachableGraph.ts`
  - Pure graph derivation from cached Kata tasks.
- Create: `frontend/src/lib/features/kata/kataReachableGraph.test.ts`
  - Unit tests for reachability, filtering, relationship resolution, and node metadata.
- Create: `frontend/src/lib/features/kata/KataReachableGraph.svelte`
  - Alternate list-pane graph UI rendered with `@xyflow/svelte`.
- Create: `frontend/src/lib/features/kata/KataReachableGraph.test.ts`
  - Svelte component tests for title rendering, priority markers, hide-done, and node selection.
- Modify: `frontend/src/lib/stores/kata-workspace.svelte.ts`
  - Add a task summary cache populated from workspace data.
- Modify: `frontend/src/lib/stores/kata-workspace.svelte.test.ts`
  - Test cache population from bootstrap, detail selection, and mutation refreshes.
- Modify: `frontend/src/lib/components/kata/KataIssueList.svelte`
  - Add a graph icon action for every visible task row and child row.
- Modify: `frontend/src/lib/components/kata/KataIssueList.test.ts`
  - Test that the graph action does not select the row and calls the graph callback.
- Modify: `frontend/src/lib/components/kata/KataIssueDetail.svelte`
  - Add a graph action in the detail heading.
- Modify: `frontend/src/lib/components/kata/KataIssueDetail.test.ts`
  - Test that the detail graph action calls the callback for the selected task.
- Modify: `frontend/src/lib/features/kata/KataWorkspace.svelte`
  - Add graph pane mode and wire graph node clicks to existing selection.
- Modify: `frontend/src/lib/features/kata/KataWorkspace.test.ts`
  - Test task-button graph launch, back-to-list, node titles, and node-click selection.
- Modify: `frontend/package.json`, `frontend/bun.lock`
  - Add `@xyflow/svelte`.

## Task 1: Add Svelte Flow Dependency

**Files:**
- Modify: `frontend/package.json`
- Modify: `frontend/bun.lock`

- [ ] **Step 1: Install dependency with Bun**

Run:

```bash
cd frontend && bun add @xyflow/svelte
```

Expected: `frontend/package.json` contains `@xyflow/svelte` under dependencies and `frontend/bun.lock` changes.

- [ ] **Step 2: Verify dependency is present**

Run:

```bash
node -e "const p=require('./frontend/package.json'); if (!p.dependencies['@xyflow/svelte']) process.exit(1); console.log(p.dependencies['@xyflow/svelte'])"
```

Expected: prints the installed version range and exits 0.

- [ ] **Step 3: Commit**

```bash
git add frontend/package.json frontend/bun.lock
git commit -m "build: add svelte flow for kata graph" \
  -m $'Kata reachable graphs need the Svelte Flow renderer requested for the pane-level graph view. The dependency is added through Bun so the existing lockfile remains authoritative.\n\nGenerated with Codex\nCo-authored-by: Codex <mariusvniekerk@users.noreply.github.com>'
```

## Task 2: Build Pure Reachable Graph Derivation

**Files:**
- Create: `frontend/src/lib/features/kata/kataReachableGraph.ts`
- Create: `frontend/src/lib/features/kata/kataReachableGraph.test.ts`

- [ ] **Step 1: Write failing graph builder tests**

Create `frontend/src/lib/features/kata/kataReachableGraph.test.ts`:

```ts
import { describe, expect, it } from "vite-plus/test";

import type { KataTaskDetail, KataTaskSummary } from "../../api/kata/taskTypes.js";
import { buildKataReachableGraph } from "./kataReachableGraph.js";

function task(overrides: Partial<KataTaskSummary>): KataTaskSummary {
  return {
    id: 1,
    uid: overrides.uid ?? "issue-root",
    project_id: 7,
    project_uid: overrides.project_uid ?? "project-kata",
    project_name: overrides.project_name ?? "Kata",
    short_id: overrides.short_id ?? "root",
    qualified_id: overrides.qualified_id ?? `Kata#${overrides.short_id ?? "root"}`,
    title: overrides.title ?? "Root task",
    status: overrides.status ?? "open",
    metadata: overrides.metadata ?? {},
    revision: overrides.revision ?? 1,
    author: "middleman",
    priority: overrides.priority,
    labels: overrides.labels,
    parent_short_id: overrides.parent_short_id,
    blocks: overrides.blocks,
    blocked_by: overrides.blocked_by,
    related: overrides.related,
    child_counts: overrides.child_counts,
    created_at: "2026-06-29T12:00:00Z",
    updated_at: "2026-06-29T12:00:00Z",
    closed_reason: overrides.closed_reason,
    closed_at: overrides.closed_at,
  };
}

function detail(issue: KataTaskSummary): KataTaskDetail {
  return { issue: { ...issue, body: "" }, comments: [], labels: [], links: [], children: [] };
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
      blocks: [{ uid: "issue-blocked", short_id: "blocked" }],
      related: [{ uid: "issue-related", short_id: "related" }],
    });
    const parent = task({ uid: "issue-parent", short_id: "parent", title: "Parent" });
    const child = task({ uid: "issue-child", short_id: "child", title: "Child", parent_short_id: "root" });
    const blocker = task({
      uid: "issue-blocker",
      short_id: "blocker",
      title: "Blocker",
      blocked_by: [{ uid: "issue-root", short_id: "root" }],
    });
    const blocked = task({ uid: "issue-blocked", short_id: "blocked", title: "Blocked" });
    const related = task({ uid: "issue-related", short_id: "related", title: "Related" });
    const graph = buildKataReachableGraph({
      sourceUID: root.uid,
      selectedUID: root.uid,
      tasks: [{ ...root, parent_short_id: "parent" }, parent, child, blocker, blocked, related],
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
      "blocks:issue-root:issue-blocked",
      "blocks:issue-root:issue-blocker",
      "parent:issue-parent:issue-root",
      "parent:issue-root:issue-child",
      "related:issue-root:issue-related",
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
    const done = task({ uid: "issue-done", short_id: "done", title: "Done", status: "closed", closed_reason: "done" });
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
```

- [ ] **Step 2: Run test to verify it fails**

Run:

```bash
cd frontend && ./node_modules/.bin/vp test src/lib/features/kata/kataReachableGraph.test.ts
```

Expected: FAIL because `./kataReachableGraph.js` does not exist.

- [ ] **Step 3: Implement graph builder**

Create `frontend/src/lib/features/kata/kataReachableGraph.ts`:

```ts
import type { Edge, Node } from "@xyflow/svelte";

import type { KataLinkPeer, KataTaskDetail, KataTaskLink, KataTaskSummary } from "../../api/kata/taskTypes.js";

export interface KataGraphNodeData extends Record<string, unknown> {
  title: string;
  idLabel: string;
  projectLabel: string;
  status: KataTaskSummary["status"] | "uncached";
  closedReason?: KataTaskSummary["closed_reason"];
  priorityLabel: string | null;
  isSource: boolean;
  isSelected: boolean;
  selectable: boolean;
}

export type KataGraphNode = Node<KataGraphNodeData>;
export type KataGraphEdge = Edge;

interface BuildKataReachableGraphInput {
  sourceUID: string;
  selectedUID: string | null;
  tasks: readonly KataTaskSummary[];
  selectedDetail?: KataTaskDetail | null | undefined;
  hideDone: boolean;
}

interface ResolvedPeer {
  id: string;
  task?: KataTaskSummary;
  projectUID: string;
  shortID: string;
}

function taskKey(projectUID: string, shortID: string): string {
  return `${projectUID}:${shortID}`;
}

function isDone(issue: KataTaskSummary): boolean {
  return issue.status === "closed" && issue.closed_reason === "done";
}

function priorityLabel(priority: number | undefined): string | null {
  return priority === undefined ? null : `P${priority}`;
}

function edge(source: string, target: string, type: "parent" | "blocks" | "related"): KataGraphEdge {
  return {
    id: `${type}:${source}:${target}`,
    source,
    target,
    type: "smoothstep",
    label: type === "parent" ? "parent" : type === "blocks" ? "blocks" : "related",
    class: `kata-graph-edge kata-graph-edge--${type}`,
  };
}

function collectTasks(tasks: readonly KataTaskSummary[]): {
  byUID: Map<string, KataTaskSummary>;
  byProjectShort: Map<string, KataTaskSummary[]>;
} {
  const byUID = new Map<string, KataTaskSummary>();
  const byProjectShort = new Map<string, KataTaskSummary[]>();
  for (const task of tasks) {
    byUID.set(task.uid, task);
    const key = taskKey(task.project_uid, task.short_id);
    byProjectShort.set(key, [...(byProjectShort.get(key) ?? []), task]);
  }
  return { byUID, byProjectShort };
}

function resolvePeer(
  peer: KataLinkPeer,
  projectUID: string,
  byUID: Map<string, KataTaskSummary>,
  byProjectShort: Map<string, KataTaskSummary[]>,
): ResolvedPeer {
  const byPeerUID = peer.uid ? byUID.get(peer.uid) : undefined;
  if (byPeerUID) return { id: byPeerUID.uid, task: byPeerUID, projectUID: byPeerUID.project_uid, shortID: byPeerUID.short_id };
  const matches = byProjectShort.get(taskKey(projectUID, peer.short_id)) ?? [];
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

function appendDetailLinks(
  sourceUID: string,
  links: readonly KataTaskLink[],
  byUID: Map<string, KataTaskSummary>,
  byProjectShort: Map<string, KataTaskSummary[]>,
): KataGraphEdge[] {
  const out: KataGraphEdge[] = [];
  for (const link of links) {
    const from = resolvePeer(link.from, byUID.get(sourceUID)?.project_uid ?? "", byUID, byProjectShort);
    const to = resolvePeer(link.to, byUID.get(sourceUID)?.project_uid ?? from.projectUID, byUID, byProjectShort);
    if (link.type === "parent") out.push(edge(from.id, to.id, "parent"));
    if (link.type === "blocks") out.push(edge(from.id, to.id, "blocks"));
    if (link.type === "related") out.push(edge(from.id, to.id, "related"));
  }
  return out;
}

export function buildKataReachableGraph(input: BuildKataReachableGraphInput): {
  nodes: KataGraphNode[];
  edges: KataGraphEdge[];
} {
  const { byUID, byProjectShort } = collectTasks(input.tasks);
  const source = byUID.get(input.sourceUID);
  if (!source) return { nodes: [], edges: [] };

  const queued = [source.uid];
  const seen = new Set<string>();
  const nodeTasks = new Map<string, KataTaskSummary | undefined>([[source.uid, source]]);
  const edges = new Map<string, KataGraphEdge>();

  const includePeer = (peer: ResolvedPeer) => {
    nodeTasks.set(peer.id, peer.task);
    if (peer.task && !seen.has(peer.task.uid)) queued.push(peer.task.uid);
  };

  while (queued.length > 0) {
    const uid = queued.shift()!;
    if (seen.has(uid)) continue;
    seen.add(uid);
    const task = byUID.get(uid);
    if (!task) continue;

    if (task.parent_short_id) {
      const parent = resolvePeer({ uid: "", short_id: task.parent_short_id }, task.project_uid, byUID, byProjectShort);
      includePeer(parent);
      edges.set(edge(parent.id, task.uid, "parent").id, edge(parent.id, task.uid, "parent"));
    }

    for (const child of input.tasks.filter((candidate) => candidate.project_uid === task.project_uid && candidate.parent_short_id === task.short_id)) {
      includePeer({ id: child.uid, task: child, projectUID: child.project_uid, shortID: child.short_id });
      edges.set(edge(task.uid, child.uid, "parent").id, edge(task.uid, child.uid, "parent"));
    }

    for (const peer of task.blocks ?? []) {
      const resolved = resolvePeer(peer, task.project_uid, byUID, byProjectShort);
      includePeer(resolved);
      edges.set(edge(task.uid, resolved.id, "blocks").id, edge(task.uid, resolved.id, "blocks"));
    }
    for (const peer of task.blocked_by ?? []) {
      const resolved = resolvePeer(peer, task.project_uid, byUID, byProjectShort);
      includePeer(resolved);
      edges.set(edge(resolved.id, task.uid, "blocks").id, edge(resolved.id, task.uid, "blocks"));
    }
    for (const peer of task.related ?? []) {
      const resolved = resolvePeer(peer, task.project_uid, byUID, byProjectShort);
      includePeer(resolved);
      edges.set(edge(task.uid, resolved.id, "related").id, edge(task.uid, resolved.id, "related"));
    }
  }

  for (const detailEdge of appendDetailLinks(source.uid, input.selectedDetail?.links ?? [], byUID, byProjectShort)) {
    edges.set(detailEdge.id, detailEdge);
    const task = byUID.get(detailEdge.source) ?? byUID.get(detailEdge.target);
    if (task) nodeTasks.set(task.uid, task);
  }

  const visibleIDs = new Set<string>();
  const nodes = [...nodeTasks.entries()].flatMap(([id, task], index): KataGraphNode[] => {
    if (task && input.hideDone && isDone(task)) return [];
    visibleIDs.add(id);
    return [
      {
        id,
        type: "default",
        position: { x: (index % 3) * 260, y: Math.floor(index / 3) * 150 },
        data: task
          ? {
              title: task.title,
              idLabel: task.qualified_id || task.short_id,
              projectLabel: task.project_name,
              status: task.status,
              closedReason: task.closed_reason,
              priorityLabel: priorityLabel(task.priority),
              isSource: task.uid === input.sourceUID,
              isSelected: task.uid === input.selectedUID,
              selectable: true,
            }
          : {
              title: id.split(":").at(-1) ?? id,
              idLabel: id.split(":").at(-1) ?? id,
              projectLabel: "",
              status: "uncached",
              priorityLabel: null,
              isSource: false,
              isSelected: false,
              selectable: false,
            },
        class: [
          "kata-graph-node",
          task?.status === "closed" ? "kata-graph-node--closed" : "kata-graph-node--open",
          task?.uid === input.sourceUID ? "kata-graph-node--source" : "",
          task?.uid === input.selectedUID ? "kata-graph-node--selected" : "",
          task ? "" : "kata-graph-node--uncached",
        ].filter(Boolean).join(" "),
        draggable: false,
        selectable: task !== undefined,
      },
    ];
  });

  return {
    nodes,
    edges: [...edges.values()].filter((item) => visibleIDs.has(item.source) && visibleIDs.has(item.target)),
  };
}
```

- [ ] **Step 4: Run graph builder test**

Run:

```bash
cd frontend && ./node_modules/.bin/vp test src/lib/features/kata/kataReachableGraph.test.ts
```

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add frontend/src/lib/features/kata/kataReachableGraph.ts frontend/src/lib/features/kata/kataReachableGraph.test.ts
git commit -m "feat: derive kata reachable graphs from cached tasks" \
  -m $'The reachable graph should reflect local Kata task relationships without recursively querying the daemon. This adds the pure graph builder first so traversal, filtering, and metadata rules are covered before wiring UI state to it.\n\nGenerated with Codex\nCo-authored-by: Codex <mariusvniekerk@users.noreply.github.com>'
```

## Task 3: Add Workspace Task Cache

**Files:**
- Modify: `frontend/src/lib/stores/kata-workspace.svelte.ts`
- Modify: `frontend/src/lib/stores/kata-workspace.svelte.test.ts`

- [ ] **Step 1: Write failing cache tests**

Add tests to `frontend/src/lib/stores/kata-workspace.svelte.test.ts`:

```ts
it("caches tasks from bootstrap views and selected detail children", async () => {
  const parent = issue("issue-parent", "Parent task", "project-kata", { child_counts: { open: 1, total: 1 } });
  const child = issue("issue-child", "Child task", "project-kata", { parent_short_id: parent.short_id });
  const { api } = createWorkspaceAPI([parent]);
  vi.mocked(api.issue).mockResolvedValueOnce(detail(parent, { children: [child] }));
  const store = createKataWorkspaceStore({ api });

  await store.bootstrap("all", parent.uid);

  expect(store.cachedTasks.map((item) => item.uid).sort()).toEqual(["issue-child", "issue-parent"]);
});

it("updates cached task summaries after a mutation refresh", async () => {
  const original = issue("issue-pay-rent", "Pay rent", "project-kata", { priority: 3 });
  const updated = { ...original, priority: 0, revision: original.revision + 1 };
  const { api } = createWorkspaceAPI([original]);
  const store = createKataWorkspaceStore({ api });
  await store.bootstrap("all", original.uid);
  vi.mocked(api.setPriority).mockResolvedValueOnce({ changed: true, issue: updated, etag: '"rev-2"' });
  vi.mocked(api.issues).mockResolvedValueOnce({ view: "all", groups: [{ id: "all", title: "All Open", issues: [updated] }], fetched_at: fetchedAt });
  vi.mocked(api.issue).mockResolvedValueOnce(detail(updated));

  await store.setPriority(original.uid, "middleman", 0);

  expect(store.cachedTasks.find((item) => item.uid === original.uid)?.priority).toBe(0);
});
```

- [ ] **Step 2: Run store test to verify it fails**

Run:

```bash
cd frontend && ./node_modules/.bin/vp test src/lib/stores/kata-workspace.svelte.test.ts -t "caches tasks|updates cached task"
```

Expected: FAIL because `cachedTasks` does not exist.

- [ ] **Step 3: Implement cache API**

In `KataWorkspaceStore`, add:

```ts
  private taskCache = $state.raw<Map<string, KataTaskSummary>>(new Map());
  cachedTasks = $derived([...this.taskCache.values()]);

  private cacheTasks(issues: readonly KataTaskSummary[]): void {
    if (issues.length === 0) return;
    const next = new Map(this.taskCache);
    for (const issue of issues) {
      next.set(issue.uid, issue);
    }
    this.taskCache = next;
  }

  private cacheView(view: Pick<KataTaskViewResponse, "groups">): void {
    this.cacheTasks(view.groups.flatMap((group) => group.issues));
  }

  private cacheDetail(detail: KataTaskDetail): void {
    this.cacheTasks([detail.issue, ...(detail.children ?? [])]);
  }
```

Call `this.cacheView(view)` or `this.cacheTasks(results.issues)` immediately before assigning `currentView` in bootstrap, open view, search updates, capture, and refresh paths. Call `this.cacheDetail(detail)` in `loadSelectedIssue` before assigning `selectedIssue`. Call `this.cacheTasks([result.issue])` in `captureMutationETag` when `result.issue` exists.

- [ ] **Step 4: Run store cache tests**

Run:

```bash
cd frontend && ./node_modules/.bin/vp test src/lib/stores/kata-workspace.svelte.test.ts -t "caches tasks|updates cached task"
```

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add frontend/src/lib/stores/kata-workspace.svelte.ts frontend/src/lib/stores/kata-workspace.svelte.test.ts
git commit -m "feat: cache kata task summaries for graph views" \
  -m $'The reachable graph is intentionally derived from local Kata state, so the workspace store needs a stable cache of task summaries beyond the currently rendered list. This cache gives the graph builder enough context while preserving the existing daemon request flow.\n\nGenerated with Codex\nCo-authored-by: Codex <mariusvniekerk@users.noreply.github.com>'
```

## Task 4: Add Graph Launch Actions To Task Rows And Detail

**Files:**
- Modify: `frontend/src/lib/components/kata/KataIssueList.svelte`
- Modify: `frontend/src/lib/components/kata/KataIssueList.test.ts`
- Modify: `frontend/src/lib/components/kata/KataIssueDetail.svelte`
- Modify: `frontend/src/lib/components/kata/KataIssueDetail.test.ts`

- [ ] **Step 1: Write failing row action test**

Add to `KataIssueList.test.ts`:

```ts
it("opens a graph from a row action without selecting the task", async () => {
  const onSelect = vi.fn();
  const onOpenGraph = vi.fn();
  render(KataIssueList, {
    props: {
      currentView,
      selectedIssueUID: null,
      loading: false,
      onSelect,
      onOpenGraph,
    },
  });

  await fireEvent.click(screen.getByRole("button", { name: "Open reachable graph for Pay rent" }));

  expect(onOpenGraph).toHaveBeenCalledWith(baseIssues[0]);
  expect(onSelect).not.toHaveBeenCalled();
});
```

- [ ] **Step 2: Write failing detail action test**

Add to `KataIssueDetail.test.ts`:

```ts
it("opens the reachable graph for the selected task", async () => {
  const onOpenGraph = vi.fn();
  render(KataIssueDetail, {
    props: makeProps({ onOpenGraph }),
  });

  await fireEvent.click(screen.getByRole("button", { name: "Open reachable graph" }));

  expect(onOpenGraph).toHaveBeenCalledWith(expect.objectContaining({ uid: "issue-pay-rent" }));
});
```

- [ ] **Step 3: Run component tests to verify failure**

Run:

```bash
cd frontend && ./node_modules/.bin/vp test src/lib/components/kata/KataIssueList.test.ts src/lib/components/kata/KataIssueDetail.test.ts -t "opens.*graph"
```

Expected: FAIL because `onOpenGraph` props and buttons do not exist.

- [ ] **Step 4: Implement row and detail actions**

In `KataIssueList.svelte`, import `NetworkIcon` from `@lucide/svelte/icons/network`, add optional prop:

```ts
    onOpenGraph?: ((issue: KataTaskSummary) => void) | undefined;
```

Render inside each row and child row:

```svelte
<span
  role="button"
  tabindex="0"
  class="graph-action"
  aria-label={`Open reachable graph for ${issue.title}`}
  title="Open reachable graph"
  onclick={(event) => {
    event.stopPropagation();
    onOpenGraph?.(issue);
  }}
  onkeydown={(event) => {
    if (event.key !== "Enter" && event.key !== " ") return;
    event.preventDefault();
    event.stopPropagation();
    onOpenGraph?.(issue);
  }}
>
  <NetworkIcon size={13} strokeWidth={1.9} aria-hidden="true" />
</span>
```

In `KataIssueDetail.svelte`, import `NetworkIcon`, add optional prop:

```ts
    onOpenGraph?: ((issue: KataTaskDetail["issue"]) => void) | undefined;
```

Render in `.detail-actions` before the overflow menu:

```svelte
{#if onOpenGraph}
  <button
    type="button"
    class="icon-detail-action"
    aria-label="Open reachable graph"
    title="Open reachable graph"
    onclick={() => onOpenGraph?.(issue.issue)}
  >
    <NetworkIcon size={14} strokeWidth={1.9} aria-hidden="true" />
  </button>
{/if}
```

- [ ] **Step 5: Run component tests**

Run:

```bash
cd frontend && ./node_modules/.bin/vp test src/lib/components/kata/KataIssueList.test.ts src/lib/components/kata/KataIssueDetail.test.ts -t "opens.*graph"
```

Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add frontend/src/lib/components/kata/KataIssueList.svelte frontend/src/lib/components/kata/KataIssueList.test.ts frontend/src/lib/components/kata/KataIssueDetail.svelte frontend/src/lib/components/kata/KataIssueDetail.test.ts
git commit -m "feat: launch kata graphs from task surfaces" \
  -m $'Users need to spawn a reachable graph from a task, not from a detail-embedded section. This adds explicit graph actions to list rows and the selected task heading while keeping row selection behavior separate.\n\nGenerated with Codex\nCo-authored-by: Codex <mariusvniekerk@users.noreply.github.com>'
```

## Task 5: Render Reachable Graph Pane

**Files:**
- Create: `frontend/src/lib/features/kata/KataReachableGraph.svelte`
- Create: `frontend/src/lib/features/kata/KataReachableGraph.test.ts`

- [ ] **Step 1: Write failing graph component tests**

Create `frontend/src/lib/features/kata/KataReachableGraph.test.ts`:

```ts
import { cleanup, fireEvent, render, screen } from "@testing-library/svelte";
import { afterEach, describe, expect, it, vi } from "vite-plus/test";

import type { KataTaskSummary } from "../../api/kata/taskTypes.js";
import KataReachableGraph from "./KataReachableGraph.svelte";

function task(overrides: Partial<KataTaskSummary>): KataTaskSummary {
  return {
    id: 1,
    uid: overrides.uid ?? "issue-root",
    project_id: 7,
    project_uid: "project-kata",
    project_name: "Kata",
    short_id: overrides.short_id ?? "root",
    qualified_id: `Kata#${overrides.short_id ?? "root"}`,
    title: overrides.title ?? "Root task",
    status: overrides.status ?? "open",
    metadata: {},
    revision: 1,
    author: "middleman",
    priority: overrides.priority,
    blocks: overrides.blocks,
    closed_reason: overrides.closed_reason,
    created_at: "2026-06-29T12:00:00Z",
    updated_at: "2026-06-29T12:00:00Z",
  };
}

describe("KataReachableGraph", () => {
  afterEach(cleanup);

  it("renders node titles and priority markers", () => {
    const root = task({ uid: "issue-root", short_id: "root", title: "Root task", priority: 0 });
    render(KataReachableGraph, {
      props: { sourceUID: root.uid, selectedUID: root.uid, tasks: [root], selectedDetail: null, onBack: () => {}, onSelectIssue: () => {} },
    });

    expect(screen.getByText("Root task")).toBeTruthy();
    expect(screen.getByText("P0")).toBeTruthy();
  });

  it("filters done nodes", async () => {
    const root = task({ uid: "issue-root", short_id: "root", blocks: [{ uid: "issue-done", short_id: "done" }] });
    const done = task({ uid: "issue-done", short_id: "done", title: "Done task", status: "closed", closed_reason: "done" });
    render(KataReachableGraph, {
      props: { sourceUID: root.uid, selectedUID: root.uid, tasks: [root, done], selectedDetail: null, onBack: () => {}, onSelectIssue: () => {} },
    });

    expect(screen.getByText("Done task")).toBeTruthy();
    await fireEvent.click(screen.getByRole("checkbox", { name: "Hide done" }));
    expect(screen.queryByText("Done task")).toBeNull();
  });

  it("selects cached nodes and returns to the list", async () => {
    const root = task({ uid: "issue-root", short_id: "root", title: "Root task" });
    const onSelectIssue = vi.fn();
    const onBack = vi.fn();
    render(KataReachableGraph, {
      props: { sourceUID: root.uid, selectedUID: null, tasks: [root], selectedDetail: null, onBack, onSelectIssue },
    });

    await fireEvent.click(screen.getByRole("button", { name: /Root task/ }));
    expect(onSelectIssue).toHaveBeenCalledWith("issue-root");
    await fireEvent.click(screen.getByRole("button", { name: "Back to task list" }));
    expect(onBack).toHaveBeenCalled();
  });
});
```

- [ ] **Step 2: Run graph component test to verify failure**

Run:

```bash
cd frontend && ./node_modules/.bin/vp test src/lib/features/kata/KataReachableGraph.test.ts
```

Expected: FAIL because component does not exist.

- [ ] **Step 3: Implement graph component**

Create `KataReachableGraph.svelte` with:

```svelte
<script lang="ts">
  import ArrowLeftIcon from "@lucide/svelte/icons/arrow-left";
  import { Background, BackgroundVariant, Controls, SvelteFlow } from "@xyflow/svelte";
  import "@xyflow/svelte/dist/style.css";

  import type { KataTaskDetail, KataTaskSummary } from "../../api/kata/taskTypes.js";
  import { buildKataReachableGraph, type KataGraphEdge, type KataGraphNode } from "./kataReachableGraph.js";

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
  let nodes = $derived<KataGraphNode[]>(graph.nodes);
  let edges = $derived<KataGraphEdge[]>(graph.edges);
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

  {#if nodes.length === 0}
    <p class="graph-empty">No cached task data is available for this graph.</p>
  {:else}
    <div class="graph-canvas">
      <SvelteFlow
        {nodes}
        {edges}
        fitView
        nodesDraggable={false}
        nodesConnectable={false}
        elementsSelectable={true}
        onnodeclick={(event) => selectNode(event.detail.node as KataGraphNode)}
      >
        <Controls />
        <Background variant={BackgroundVariant.Dots} gap={14} size={1} />
      </SvelteFlow>
    </div>
    <div class="graph-node-list" aria-hidden="false">
      {#each nodes as node (node.id)}
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
```

Add CSS in the same component for stable dimensions, toolbar layout, node list fallback, and graph node status classes. Keep the canvas height `min-height: 360px` and `height: 100%` so Svelte Flow has dimensions.

Use this component CSS:

```css
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

.graph-source {
  min-width: 0;
  display: flex;
  flex-direction: column;
  gap: 2px;
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
  position: absolute;
  width: 1px;
  height: 1px;
  overflow: hidden;
  clip: rect(0, 0, 0, 0);
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
```

- [ ] **Step 4: Run graph component test**

Run:

```bash
cd frontend && ./node_modules/.bin/vp test src/lib/features/kata/KataReachableGraph.test.ts
```

Expected: PASS.

- [ ] **Step 5: Run Svelte autofixer on new component**

Run:

```bash
frontend/node_modules/.bin/vp exec svelte-mcp svelte-autofixer ./frontend/src/lib/features/kata/KataReachableGraph.svelte
```

Expected: no required fixes.

- [ ] **Step 6: Commit**

```bash
git add frontend/src/lib/features/kata/KataReachableGraph.svelte frontend/src/lib/features/kata/KataReachableGraph.test.ts
git commit -m "feat: render kata reachable graph pane" \
  -m $'The graph view needs its own pane component so it can replace the task list while leaving task detail behavior intact. The component wraps Svelte Flow and keeps accessible node controls available for tests and keyboard interaction.\n\nGenerated with Codex\nCo-authored-by: Codex <mariusvniekerk@users.noreply.github.com>'
```

## Task 6: Wire Graph Mode Into Kata Workspace

**Files:**
- Modify: `frontend/src/lib/features/kata/KataWorkspace.svelte`
- Modify: `frontend/src/lib/features/kata/KataWorkspace.test.ts`

- [ ] **Step 1: Write failing workspace integration tests**

Add to `KataWorkspace.test.ts`:

```ts
it("opens a reachable graph in place of the task list and returns to tasks", async () => {
  const root = issue("issue-root", "Root graph task", "project-kata", {
    priority: 0,
    blocks: [{ uid: "issue-peer", short_id: "peer" }],
  });
  const peer = issue("issue-peer", "Peer graph task", "project-kata", { short_id: "peer", priority: 1 });
  const { api } = createWorkspaceAPI([root, peer]);

  render(KataWorkspace, { props: { api } });

  await waitFor(() => expect(screen.getByRole("button", { name: /Root graph task/ })).toBeTruthy());
  await fireEvent.click(screen.getByRole("button", { name: "Open reachable graph for Root graph task" }));

  expect(screen.getByRole("region", { name: "Reachable task graph" })).toBeTruthy();
  expect(screen.getByText("Root graph task")).toBeTruthy();
  expect(screen.getByText("Peer graph task")).toBeTruthy();
  expect(screen.queryByRole("heading", { name: "All Open" })).toBeNull();

  await fireEvent.click(screen.getByRole("button", { name: "Back to task list" }));
  expect(screen.getByRole("heading", { name: "All Open" })).toBeTruthy();
});

it("clicking a graph node selects the task detail", async () => {
  const root = issue("issue-root", "Root graph task", "project-kata", {
    blocks: [{ uid: "issue-peer", short_id: "peer" }],
  });
  const peer = issue("issue-peer", "Peer graph task", "project-kata", { short_id: "peer" });
  const { api } = createWorkspaceAPI([root, peer]);

  render(KataWorkspace, { props: { api } });

  await waitFor(() => expect(screen.getByRole("button", { name: /Root graph task/ })).toBeTruthy());
  await fireEvent.click(screen.getByRole("button", { name: "Open reachable graph for Root graph task" }));
  await fireEvent.click(screen.getByRole("button", { name: /Peer graph task/ }));

  await waitFor(() => expect(screen.getByRole("heading", { name: "Peer graph task" })).toBeTruthy());
});
```

- [ ] **Step 2: Run workspace tests to verify failure**

Run:

```bash
cd frontend && ./node_modules/.bin/vp test src/lib/features/kata/KataWorkspace.test.ts -t "reachable graph|graph node"
```

Expected: FAIL because graph mode is not wired.

- [ ] **Step 3: Implement workspace graph mode**

In `KataWorkspace.svelte`, import `KataReachableGraph` and add:

```ts
  type ListMode = "tasks" | "reachableGraph";
  let listMode = $state<ListMode>("tasks");
  let graphSourceUID = $state<string | null>(null);

  function openReachableGraph(issue: KataTaskSummary): void {
    graphSourceUID = issue.uid;
    listMode = "reachableGraph";
  }

  function closeReachableGraph(): void {
    listMode = "tasks";
    graphSourceUID = null;
  }
```

In `listPane`, render:

```svelte
{#if listMode === "reachableGraph" && graphSourceUID}
  <KataReachableGraph
    sourceUID={graphSourceUID}
    selectedUID={store.pendingSelectionUID ?? store.selectedIssue?.issue.uid ?? null}
    tasks={store.cachedTasks}
    selectedDetail={store.selectedIssue}
    onBack={closeReachableGraph}
    onSelectIssue={(uid) => {
      void selectIssue(uid);
    }}
  />
{:else}
  <KataSearchPanel ... />
  <KataIssueList ... onOpenGraph={openReachableGraph} />
{/if}
```

Pass `onOpenGraph={openReachableGraph}` to `KataIssueDetail`.

- [ ] **Step 4: Run workspace graph tests**

Run:

```bash
cd frontend && ./node_modules/.bin/vp test src/lib/features/kata/KataWorkspace.test.ts -t "reachable graph|graph node"
```

Expected: PASS.

- [ ] **Step 5: Run Svelte autofixer on edited components**

Run:

```bash
frontend/node_modules/.bin/vp exec svelte-mcp svelte-autofixer ./frontend/src/lib/features/kata/KataWorkspace.svelte
```

Expected: no required fixes.

- [ ] **Step 6: Commit**

```bash
git add frontend/src/lib/features/kata/KataWorkspace.svelte frontend/src/lib/features/kata/KataWorkspace.test.ts
git commit -m "feat: replace kata task list with reachable graph" \
  -m $'The graph should be spawned from a task and occupy the task-list pane, not live inside task detail. Wiring graph mode at the workspace level preserves the existing detail pane and lets graph node clicks use the normal selection flow.\n\nGenerated with Codex\nCo-authored-by: Codex <mariusvniekerk@users.noreply.github.com>'
```

## Task 7: Final Verification

**Files:**
- Verify all modified frontend files.

- [ ] **Step 1: Run focused tests**

Run:

```bash
cd frontend && ./node_modules/.bin/vp test \
  src/lib/features/kata/kataReachableGraph.test.ts \
  src/lib/features/kata/KataReachableGraph.test.ts \
  src/lib/features/kata/KataWorkspace.test.ts \
  src/lib/components/kata/KataIssueList.test.ts \
  src/lib/components/kata/KataIssueDetail.test.ts \
  src/lib/stores/kata-workspace.svelte.test.ts
```

Expected: PASS.

- [ ] **Step 2: Run full Vitest suite required for frontend changes**

Run:

```bash
cd frontend && ./node_modules/.bin/vp test
```

Expected: PASS.

- [ ] **Step 3: Run frontend build/check**

Run:

```bash
make frontend
```

Expected: PASS.

- [ ] **Step 4: Inspect final diff**

Run:

```bash
git status --short
git diff --stat HEAD
```

Expected: no unstaged changes after the final commit, or only intentionally uncommitted local artifacts that are not part of the feature.
