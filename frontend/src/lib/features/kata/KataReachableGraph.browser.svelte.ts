import { beforeEach, describe, expect, it, vi } from "vite-plus/test";
import { page } from "vite-plus/test/browser";
import { render } from "vitest-browser-svelte";

import "../../../app.css";

import { pressKey } from "../../../test/browserAppHarness.js";
import type {
  KataReachableGraphEdge,
  KataReachableGraphQuery,
  KataReachableGraphResponse,
  KataTaskAPI,
  KataTaskSummary,
} from "../../api/kata/taskTypes.js";
import KataReachableGraph from "./KataReachableGraph.svelte";

const graphPreferencesStorageKey = "middleman:kata:reachableGraphPreferences/v1";

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
    title: overrides.title ?? "Root browser task",
    status: overrides.status ?? "open",
    metadata: overrides.metadata ?? {},
    revision: overrides.revision ?? 1,
    author: overrides.author ?? "middleman",
    priority: overrides.priority,
    blocks: overrides.blocks,
    closed_reason: overrides.closed_reason,
    created_at: overrides.created_at ?? "2026-06-29T12:00:00Z",
    updated_at: overrides.updated_at ?? "2026-06-29T12:00:00Z",
  };
}

function graphAPI(
  source: KataTaskSummary,
  nodes: KataTaskSummary[],
  edges: KataReachableGraphEdge[] = [],
): KataTaskAPI {
  return {
    reachableGraph: vi.fn(async (_projectID: number, _ref: string, query: KataReachableGraphQuery = {}) => {
      const depthLimit =
        query.depth === undefined || query.depth === "full" ? Number.POSITIVE_INFINITY : Number(query.depth);
      const distanceByUID = new Map<string, number>([[source.uid, 0]]);
      const queue = [source.uid];
      while (queue.length > 0) {
        const uid = queue.shift()!;
        const distance = distanceByUID.get(uid) ?? 0;
        if (distance >= depthLimit) continue;
        for (const edge of edges) {
          const nextUID = edge.from_uid === uid ? edge.to_uid : edge.to_uid === uid ? edge.from_uid : null;
          if (!nextUID || distanceByUID.has(nextUID)) continue;
          distanceByUID.set(nextUID, distance + 1);
          queue.push(nextUID);
        }
      }
      const visibleNodes =
        query.hide_done === true
          ? nodes.filter(
              (node) => distanceByUID.has(node.uid) && (node.uid === source.uid || node.closed_reason !== "done"),
            )
          : nodes.filter((node) => distanceByUID.has(node.uid));
      const visible = new Set(visibleNodes.map((node) => node.uid));
      return {
        source_uid: source.uid,
        depth: query.depth ?? "full",
        hide_done: query.hide_done === true,
        nodes: visibleNodes,
        edges: edges.filter((edge) => visible.has(edge.from_uid) && visible.has(edge.to_uid)),
        unresolved_refs: [],
        fetched_at: "2026-06-29T12:00:00Z",
      } satisfies KataReachableGraphResponse;
    }),
  } as unknown as KataTaskAPI;
}

function graphEdge(
  from: KataTaskSummary,
  to: KataTaskSummary,
  kind: KataReachableGraphEdge["kind"] = "blocks",
  layout = true,
): KataReachableGraphEdge {
  return { from_uid: from.uid, to_uid: to.uid, kind, layout };
}

interface RenderedNodeBox {
  x: number;
  y: number;
  width: number;
  height: number;
}

function renderedNodeBoxes(container: HTMLElement): Map<string, RenderedNodeBox> {
  const graph = container.querySelector<HTMLElement>(".graph-canvas");
  expect(graph).toBeTruthy();
  const graphRect = graph!.getBoundingClientRect();
  return new Map(
    [...container.querySelectorAll<HTMLElement>(".svelte-flow__node")]
      .map((node) => {
        const id = node.dataset.id;
        expect(id).toBeTruthy();
        const rect = node.getBoundingClientRect();
        return [
          id!,
          {
            x: Math.round(rect.x - graphRect.x),
            y: Math.round(rect.y - graphRect.y),
            width: Math.round(rect.width),
            height: Math.round(rect.height),
          },
        ] as const;
      })
      .sort(([left], [right]) => left.localeCompare(right, undefined, { numeric: true, sensitivity: "base" })),
  );
}

function expectRenderedNodeBoxesStable(
  actual: Map<string, RenderedNodeBox>,
  expected: Map<string, RenderedNodeBox>,
): void {
  expect([...actual.keys()]).toEqual([...expected.keys()]);
  for (const [id, actualBox] of actual) {
    const expectedBox = expected.get(id);
    expect(expectedBox).toBeTruthy();
    expect(Math.abs(actualBox.x - expectedBox!.x)).toBeLessThanOrEqual(1);
    expect(Math.abs(actualBox.y - expectedBox!.y)).toBeLessThanOrEqual(1);
    expect(Math.abs(actualBox.width - expectedBox!.width)).toBeLessThanOrEqual(1);
    expect(Math.abs(actualBox.height - expectedBox!.height)).toBeLessThanOrEqual(1);
  }
}

async function waitForStableRenderedNodeBoxes(
  container: HTMLElement,
  expectedCount: number,
): Promise<Map<string, RenderedNodeBox>> {
  let lastSignature = "";
  let stableReads = 0;
  await vi.waitFor(() => {
    expect(container.querySelectorAll(".svelte-flow__node")).toHaveLength(expectedCount);
    const boxes = renderedNodeBoxes(container);
    const signature = JSON.stringify([...boxes.entries()]);
    if (signature === lastSignature) {
      stableReads += 1;
    } else {
      stableReads = 0;
      lastSignature = signature;
    }
    expect(stableReads).toBeGreaterThanOrEqual(1);
  });
  return renderedNodeBoxes(container);
}

function resolvedColor(scope: HTMLElement, color: string): string {
  const probe = document.createElement("span");
  probe.style.color = color;
  scope.append(probe);
  const resolved = getComputedStyle(probe).color;
  probe.remove();
  return resolved;
}

async function ensureGraphFilterMenuOpen(): Promise<void> {
  if (!document.querySelector(".graph-filter-menu .kit-filter-dropdown__panel")) {
    await page.getByRole("button", { name: /Graph filters/ }).click();
  }
  await vi.waitFor(() => {
    expect(document.querySelector(".graph-filter-menu .kit-filter-dropdown__panel")).toBeTruthy();
  });
}

function findGraphFilterItem(sectionTitle: string, itemLabel: string): HTMLButtonElement {
  const dropdown = document.querySelector<HTMLElement>(".graph-filter-menu .kit-filter-dropdown__panel");
  expect(dropdown).toBeTruthy();
  let inSection = false;
  for (const element of dropdown!.children) {
    if (element.classList.contains("kit-filter-dropdown__section-title")) {
      inSection = element.textContent?.trim() === sectionTitle;
      continue;
    }
    if (element.classList.contains("kit-filter-dropdown__divider")) {
      inSection = false;
      continue;
    }
    if (!inSection || !element.classList.contains("kit-filter-dropdown__item")) continue;
    const label = element.querySelector(".kit-filter-dropdown__label")?.textContent?.trim();
    if (label === itemLabel) return element as HTMLButtonElement;
  }
  throw new Error(`Missing graph filter item: ${sectionTitle} / ${itemLabel}`);
}

async function selectGraphFilterItem(sectionTitle: string, itemLabel: string): Promise<void> {
  await ensureGraphFilterMenuOpen();
  findGraphFilterItem(sectionTitle, itemLabel).click();
  await vi.waitFor(() => {
    expect(findGraphFilterItem(sectionTitle, itemLabel).classList.contains("active")).toBe(true);
  });
}

function graphFilterDetailText(container: HTMLElement): string {
  return container.querySelector(".graph-filter-menu .kit-filter-dropdown__trigger-detail")?.textContent?.trim() ?? "";
}

describe("KataReachableGraph (browser)", () => {
  beforeEach(() => {
    localStorage.removeItem(graphPreferencesStorageKey);
  });

  it("renders nonblank Svelte Flow nodes and selects them from the canvas", async () => {
    const root = task({
      uid: "issue-root",
      short_id: "root",
      title: "Root browser task",
      priority: 0,
      blocks: [{ uid: "issue-linked", short_id: "linked" }],
    });
    const linked = task({
      uid: "issue-linked",
      short_id: "linked",
      title: "Linked browser task",
      priority: 1,
      status: "closed",
      closed_reason: "done",
    });
    const onSelectIssue = vi.fn();
    const { container } = render(KataReachableGraph, {
      props: {
        api: graphAPI(root, [root, linked], [graphEdge(root, linked)]),
        sourceIssue: root,
        selectedUID: root.uid,
        onBack: () => {},
        onSelectIssue,
      },
    });

    await expect.element(page.getByRole("region", { name: "Reachable task graph" })).toBeVisible();
    await vi.waitFor(() => {
      expect(container.querySelectorAll(".svelte-flow__node").length).toBeGreaterThanOrEqual(2);
    });
    expect(container.querySelector(".svelte-flow__controls")).toBeTruthy();
    expect(container.querySelector(".svelte-flow__minimap")).toBeTruthy();
    expect(container.querySelector(".svelte-flow__background")).toBeTruthy();
    expect(container.querySelector(".graph-node-list")).toBeNull();
    expect(container.querySelector(".source-id")).toBeNull();
    const graphCanvas = container.querySelector<HTMLElement>(".graph-canvas");
    expect(graphCanvas).toBeTruthy();
    expect(getComputedStyle(graphCanvas!).overflow).toBe("hidden");
    await expect.element(page.getByRole("button", { name: /Graph filters/ })).toBeVisible();
    await selectGraphFilterItem("Context", "1 edge");
    await expect.poll(() => window.__middleman_kata_graph_debug?.snapshot().latestGraph?.contextDepth).toBe("1");
    await selectGraphFilterItem("Direction", "Top to bottom");
    await vi.waitFor(() => {
      expect(graphFilterDetailText(container)).toContain("TB");
    });
    await expect.poll(() => window.__middleman_kata_graph_debug?.snapshot().latestGraph?.layoutDirection).toBe("TB");
    expect(container.querySelector(".kata-graph-pane")?.getAttribute("data-layout-direction")).toBe("TB");
    const controlsButton = container.querySelector<HTMLElement>(".svelte-flow__controls-button");
    const minimap = container.querySelector<SVGSVGElement>(".svelte-flow__minimap");
    expect(controlsButton).toBeTruthy();
    expect(minimap).toBeTruthy();
    expect(getComputedStyle(controlsButton!).backgroundColor).not.toBe("rgb(255, 255, 255)");
    expect(getComputedStyle(minimap!).backgroundColor).not.toBe("rgb(255, 255, 255)");
    const visibleHandles = [...container.querySelectorAll<HTMLElement>(".svelte-flow__handle")].filter(
      (handle) => getComputedStyle(handle).opacity !== "0",
    );
    expect(visibleHandles).toHaveLength(0);
    await vi.waitFor(() => {
      expect(container.querySelectorAll(".svelte-flow__edge-path").length).toBeGreaterThan(0);
    });
    const edgePaths = [...container.querySelectorAll<SVGPathElement>(".svelte-flow__edge-path")];
    expect(edgePaths.length).toBeGreaterThan(0);
    expect(edgePaths.some((edge) => edge.getAttribute("marker-end")?.includes("type=arrowclosed"))).toBe(true);
    expect(container.textContent).not.toContain("blocks ->");
    expect(container.querySelector(".status-marker")).toBeNull();

    const flowNodes = [...container.querySelectorAll<HTMLElement>(".svelte-flow__node")];
    const linkedNode = flowNodes.find((node) => node.textContent?.includes("Linked browser task"));
    const rootNode = flowNodes.find((node) => node.textContent?.includes("Root browser task"));
    expect(rootNode).toBeTruthy();
    expect(linkedNode).toBeTruthy();
    const rect = linkedNode!.getBoundingClientRect();
    expect(rect.width).toBeGreaterThan(0);
    expect(rect.height).toBeGreaterThan(0);
    const rootNodeButton = rootNode!.querySelector<HTMLElement>(".graph-task-node")!;
    const rootButtonStyle = getComputedStyle(rootNodeButton);
    const linkedNodeButton = linkedNode!.querySelector<HTMLElement>(".graph-task-node")!;
    const linkedButtonStyle = getComputedStyle(linkedNodeButton);
    expect(rootNodeButton.classList.contains("graph-task-node--selected")).toBe(true);
    expect(rootButtonStyle.borderColor).not.toBe(linkedButtonStyle.borderColor);
    expect(rootButtonStyle.boxShadow).toContain("0px 0px 0px 5px");
    expect(linkedNodeButton.classList.contains("graph-task-node--relation-blocks")).toBe(true);
    expect(linkedButtonStyle.backgroundColor).not.toBe(rootButtonStyle.backgroundColor);
    expect(linkedButtonStyle.boxShadow).toContain("inset");

    linkedNode!.click();

    expect(onSelectIssue).toHaveBeenCalledWith(linked.uid);

    onSelectIssue.mockClear();
    const linkedButton = [...container.querySelectorAll<HTMLButtonElement>(".graph-task-node")].find((node) =>
      node.textContent?.includes("Linked browser task"),
    );
    expect(linkedButton).toBeTruthy();
    linkedButton!.focus();
    pressKey("Enter", {}, linkedButton!);

    expect(onSelectIssue).toHaveBeenCalledWith(linked.uid);

    onSelectIssue.mockClear();
    pressKey(" ", {}, linkedButton!);

    expect(onSelectIssue).toHaveBeenCalledTimes(1);
    expect(onSelectIssue).toHaveBeenCalledWith(linked.uid);
  });

  it("keeps light-mode ambient edges quieter than body text", async () => {
    const root = task({
      uid: "issue-root",
      short_id: "root",
      title: "Root browser task",
      blocks: [{ uid: "issue-one", short_id: "one" }],
    });
    const one = task({
      uid: "issue-one",
      short_id: "one",
      title: "One edge task",
      blocks: [{ uid: "issue-two", short_id: "two" }],
    });
    const two = task({
      uid: "issue-two",
      short_id: "two",
      title: "Two edge task",
    });
    const { container } = render(KataReachableGraph, {
      props: {
        api: graphAPI(root, [root, one, two], [graphEdge(root, one), graphEdge(one, two)]),
        sourceIssue: root,
        selectedUID: root.uid,
        onBack: () => {},
        onSelectIssue: () => {},
      },
    });

    await vi.waitFor(() => {
      expect(container.querySelector(".kata-graph-edge--ambient .svelte-flow__edge-path")).toBeTruthy();
    });
    const graphPane = container.querySelector<HTMLElement>(".kata-graph-pane");
    const ambientEdge = container.querySelector<SVGPathElement>(".kata-graph-edge--ambient .svelte-flow__edge-path");
    expect(graphPane).toBeTruthy();
    expect(ambientEdge).toBeTruthy();

    const ambientStroke = getComputedStyle(ambientEdge!).stroke;
    expect(ambientStroke).toBe(resolvedColor(graphPane!, "var(--kata-graph-edge-ambient)"));
    expect(ambientStroke).not.toBe(resolvedColor(graphPane!, "var(--text-secondary)"));
  });

  it("follows split direction until the user chooses a graph direction", async () => {
    const root = task({
      uid: "issue-root",
      short_id: "root",
      title: "Root browser task",
      blocks: [{ uid: "issue-linked", short_id: "linked" }],
    });
    const linked = task({
      uid: "issue-linked",
      short_id: "linked",
      title: "Linked browser task",
    });
    const { container, rerender } = render(KataReachableGraph, {
      props: {
        api: graphAPI(root, [root, linked], [graphEdge(root, linked)]),
        sourceIssue: root,
        selectedUID: root.uid,
        layoutDirection: "TB",
        onBack: () => {},
        onSelectIssue: () => {},
      },
    });

    await expect.element(page.getByRole("button", { name: /Graph filters/ })).toBeVisible();
    await vi.waitFor(() => {
      expect(graphFilterDetailText(container)).toContain("Follow TB");
    });
    await ensureGraphFilterMenuOpen();
    expect(findGraphFilterItem("Direction", "Follow split").classList.contains("active")).toBe(true);
    await expect
      .poll(() => {
        const raw = localStorage.getItem(graphPreferencesStorageKey);
        return raw ? JSON.parse(raw).layoutDirection : undefined;
      })
      .toBeNull();

    await selectGraphFilterItem("Direction", "Left to right");
    await vi.waitFor(() => {
      expect(graphFilterDetailText(container)).toContain("Pinned LR");
    });
    await expect
      .poll(() => {
        const raw = localStorage.getItem(graphPreferencesStorageKey);
        return raw ? JSON.parse(raw).layoutDirection : undefined;
      })
      .toBe("LR");

    await selectGraphFilterItem("Direction", "Follow split");
    await vi.waitFor(() => {
      expect(graphFilterDetailText(container)).toContain("Follow TB");
    });
    await expect
      .poll(() => {
        const raw = localStorage.getItem(graphPreferencesStorageKey);
        return raw ? JSON.parse(raw).layoutDirection : undefined;
      })
      .toBeNull();

    await rerender({
      api: graphAPI(root, [root, linked], [graphEdge(root, linked)]),
      sourceIssue: root,
      selectedUID: root.uid,
      layoutDirection: "LR",
      onBack: () => {},
      onSelectIssue: () => {},
    });
    await vi.waitFor(() => {
      expect(graphFilterDetailText(container)).toContain("Follow LR");
    });
    await expect.poll(() => window.__middleman_kata_graph_debug?.snapshot().latestGraph?.layoutDirection).toBe("LR");
  });

  it("restores graph control preferences from localStorage", async () => {
    localStorage.setItem(
      graphPreferencesStorageKey,
      JSON.stringify({
        depthLimit: "2",
        contextDepth: "1",
        layoutMode: "elk",
        layoutDirection: "TB",
      }),
    );
    const root = task({
      uid: "issue-root",
      short_id: "root",
      title: "Root browser task",
      blocks: [{ uid: "issue-linked", short_id: "linked" }],
    });
    const linked = task({
      uid: "issue-linked",
      short_id: "linked",
      title: "Linked browser task",
      priority: 1,
    });
    const { container } = render(KataReachableGraph, {
      props: {
        api: graphAPI(root, [root, linked], [graphEdge(root, linked)]),
        sourceIssue: root,
        selectedUID: root.uid,
        onBack: () => {},
        onSelectIssue: () => {},
      },
    });

    await expect.element(page.getByRole("button", { name: /Graph filters/ })).toBeVisible();
    await vi.waitFor(() => {
      expect(graphFilterDetailText(container)).toBe("2 edges · 1 edge · ELK · Pinned TB");
    });
    await expect.poll(() => window.__middleman_kata_graph_debug?.snapshot().latestGraph?.layoutMode).toBe("elk");
    await expect.poll(() => window.__middleman_kata_graph_debug?.snapshot().latestGraph?.layoutDirection).toBe("TB");

    await selectGraphFilterItem("Context", "3 edges");
    await selectGraphFilterItem("Direction", "Left to right");

    await expect
      .poll(() => {
        const raw = localStorage.getItem(graphPreferencesStorageKey);
        return raw ? JSON.parse(raw) : null;
      })
      .toMatchObject({
        depthLimit: "2",
        contextDepth: "3",
        layoutMode: "elk",
        layoutDirection: "LR",
      });
  });

  it("switches to ELK layout without freezing current node data", async () => {
    const root = task({
      uid: "issue-root",
      short_id: "root",
      title: "Root browser task",
      priority: 0,
      blocks: [{ uid: "issue-linked", short_id: "linked" }],
    });
    const linked = task({
      uid: "issue-linked",
      short_id: "linked",
      title: "Linked browser task",
      priority: 1,
    });
    const { container, rerender } = render(KataReachableGraph, {
      props: {
        api: graphAPI(root, [root, linked], [graphEdge(root, linked)]),
        sourceIssue: root,
        selectedUID: root.uid,
        onBack: () => {},
        onSelectIssue: () => {},
      },
    });

    await expect.element(page.getByRole("button", { name: /Graph filters/ })).toBeVisible();
    await selectGraphFilterItem("Layout", "ELK");
    await expect
      .poll(() => window.__middleman_kata_graph_debug?.snapshot().latestGraph?.layoutReady ?? false)
      .toBe(true);
    expect(
      window.__middleman_kata_graph_debug?.snapshot().events.some((event) => event.kind === "graph-layout-complete"),
    ).toBe(true);

    const updatedRoot = { ...root, title: "Root title after refresh" };
    const updatedLinked = { ...linked, title: "Linked title after refresh" };
    await rerender({
      api: graphAPI(updatedRoot, [updatedRoot, updatedLinked], [graphEdge(updatedRoot, updatedLinked)]),
      sourceIssue: updatedRoot,
      selectedUID: linked.uid,
      onBack: () => {},
      onSelectIssue: () => {},
    });

    await vi.waitFor(() => {
      expect(container.textContent).toContain("Linked title after refresh");
    });
    expect(container.textContent).toContain("Root title after refresh");
    expect(container.textContent).not.toContain("Root browser task");
  });

  it("keeps context as emphasis without widening the depth node set", async () => {
    const root = task({
      uid: "issue-root",
      short_id: "root",
      title: "Root browser task",
      blocks: [{ uid: "issue-one", short_id: "one" }],
    });
    const one = task({
      uid: "issue-one",
      short_id: "one",
      title: "One edge task",
      blocks: [{ uid: "issue-two", short_id: "two" }],
    });
    const two = task({
      uid: "issue-two",
      short_id: "two",
      title: "Two edge task",
    });
    const { container } = render(KataReachableGraph, {
      props: {
        api: graphAPI(root, [root, one, two], [graphEdge(root, one), graphEdge(one, two)]),
        sourceIssue: root,
        selectedUID: root.uid,
        onBack: () => {},
        onSelectIssue: () => {},
      },
    });

    await selectGraphFilterItem("Depth", "1 edge");
    await expect.element(page.getByRole("button", { name: /Graph filters/ })).toBeVisible();
    await vi.waitFor(() => {
      expect(container.querySelectorAll(".svelte-flow__node").length).toBe(2);
    });
    await selectGraphFilterItem("Context", "1 edge");
    await vi.waitFor(() => {
      expect(container.querySelectorAll(".svelte-flow__node").length).toBe(2);
    });
    const hiddenByDepthNode = [...container.querySelectorAll<HTMLElement>(".svelte-flow__node")].find((node) =>
      node.textContent?.includes("Two edge task"),
    );
    expect(hiddenByDepthNode).toBeUndefined();
    const oneEdgeNode = [...container.querySelectorAll<HTMLElement>(".svelte-flow__node")].find((node) =>
      node.textContent?.includes("One edge task"),
    );
    expect(oneEdgeNode).toBeTruthy();
    const oneEdgeButton = oneEdgeNode!.querySelector<HTMLElement>(".graph-task-node")!;
    expect(oneEdgeButton.classList.contains("graph-task-node--depth-context")).toBe(false);
  });

  it("keeps full-depth rendered layout stable across context and selection emphasis", async () => {
    const root = task({
      uid: "issue-root",
      short_id: "root",
      title: "Root browser task",
      blocks: [{ uid: "issue-one", short_id: "one" }],
    });
    const one = task({
      uid: "issue-one",
      short_id: "one",
      title: "One edge task",
      blocks: [{ uid: "issue-two", short_id: "two" }],
    });
    const two = task({
      uid: "issue-two",
      short_id: "two",
      title: "Two edge task",
      blocks: [{ uid: "issue-three", short_id: "three" }],
    });
    const three = task({
      uid: "issue-three",
      short_id: "three",
      title: "Three edge task",
    });
    const { container, rerender } = render(KataReachableGraph, {
      props: {
        api: graphAPI(
          root,
          [root, one, two, three],
          [graphEdge(root, one), graphEdge(one, two), graphEdge(two, three)],
        ),
        sourceIssue: root,
        selectedUID: root.uid,
        onBack: () => {},
        onSelectIssue: () => {},
      },
    });

    const initialBoxes = await waitForStableRenderedNodeBoxes(container, 4);

    await selectGraphFilterItem("Context", "1 edge");
    await expect.poll(() => window.__middleman_kata_graph_debug?.snapshot().latestGraph?.contextDepth).toBe("1");
    expectRenderedNodeBoxesStable(await waitForStableRenderedNodeBoxes(container, 4), initialBoxes);

    await rerender({
      api: graphAPI(root, [root, one, two, three], [graphEdge(root, one), graphEdge(one, two), graphEdge(two, three)]),
      sourceIssue: root,
      selectedUID: two.uid,
      onBack: () => {},
      onSelectIssue: () => {},
    });

    await vi.waitFor(() => {
      const selected = container.querySelector<HTMLElement>('[data-id="issue-two"] .graph-task-node');
      expect(selected?.classList.contains("graph-task-node--selected")).toBe(true);
    });
    expect(
      container
        .querySelector('[data-id="issue-root"] .graph-task-node')
        ?.classList.contains("graph-task-node--depth-context"),
    ).toBe(true);
    expectRenderedNodeBoxesStable(await waitForStableRenderedNodeBoxes(container, 4), initialBoxes);
  });

  it("keeps graph toolbar filters visible in a narrow pane", async () => {
    const root = task({
      uid: "issue-root",
      short_id: "root",
      title: "Root browser task with a longer title",
      blocks: [{ uid: "issue-linked", short_id: "linked" }],
    });
    const linked = task({
      uid: "issue-linked",
      short_id: "linked",
      title: "Linked browser task",
    });
    const { container } = render(KataReachableGraph, {
      props: {
        api: graphAPI(root, [root, linked], [graphEdge(root, linked)]),
        sourceIssue: root,
        selectedUID: root.uid,
        onBack: () => {},
        onSelectIssue: () => {},
      },
    });
    container.style.width = "520px";
    container.style.height = "460px";

    await expect.element(page.getByRole("button", { name: /Graph filters/ })).toBeVisible();
    const toolbar = container.querySelector<HTMLElement>(".graph-toolbar");
    const graphFilterMenu = container.querySelector<HTMLElement>(".graph-filter-menu");
    const graphFilterButton = container.querySelector<HTMLElement>(".graph-filter-menu .kit-filter-dropdown__btn");
    expect(toolbar).toBeTruthy();
    expect(graphFilterMenu).toBeTruthy();
    expect(graphFilterButton).toBeTruthy();
    expect(container.querySelector(".depth-filter")).toBeNull();
    expect(container.querySelector(".context-filter")).toBeNull();
    expect(container.querySelector(".layout-filter")).toBeNull();
    expect(container.querySelector(".direction-toggle")).toBeNull();
    expect(container.querySelector(".hide-done")).toBeNull();

    await vi.waitFor(() => {
      const toolbarRect = toolbar!.getBoundingClientRect();
      const graphFilterRect = graphFilterButton!.getBoundingClientRect();
      expect(toolbar!.scrollWidth).toBeLessThanOrEqual(toolbar!.clientWidth + 1);
      expect(graphFilterRect.right).toBeLessThanOrEqual(toolbarRect.right + 1);
      expect(graphFilterRect.bottom).toBeLessThanOrEqual(toolbarRect.bottom + 1);
      expect(graphFilterRect.height).toBe(30);
    });

    await selectGraphFilterItem("Visibility", "Hide done");
    await vi.waitFor(() => {
      expect(graphFilterDetailText(container)).toContain("Hide done");
      expect(graphFilterButton!.classList.contains("kit-filter-dropdown__btn--active")).toBe(true);
    });
  });
});
