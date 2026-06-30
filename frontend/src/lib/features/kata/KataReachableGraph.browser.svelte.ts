import { describe, expect, it, vi } from "vite-plus/test";
import { page } from "vite-plus/test/browser";
import { render } from "vitest-browser-svelte";

import "../../../app.css";

import { pressKey } from "../../../test/browserAppHarness.js";
import type { KataTaskSummary } from "../../api/kata/taskTypes.js";
import KataReachableGraph from "./KataReachableGraph.svelte";

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

describe("KataReachableGraph (browser)", () => {
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
        sourceUID: root.uid,
        selectedUID: root.uid,
        tasks: [root, linked],
        selectedDetail: null,
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
        sourceUID: root.uid,
        selectedUID: root.uid,
        tasks: [root, linked],
        selectedDetail: null,
        onBack: () => {},
        onSelectIssue: () => {},
      },
    });

    await expect.element(page.getByRole("combobox", { name: "Graph layout: Compact" })).toBeVisible();
    await page.getByRole("combobox", { name: "Graph layout: Compact" }).click();
    await page.getByRole("option", { name: "ELK" }).click();
    await expect
      .poll(() => window.__middleman_kata_graph_debug?.snapshot().latestGraph?.layoutReady ?? false)
      .toBe(true);
    expect(
      window.__middleman_kata_graph_debug?.snapshot().events.some((event) => event.kind === "graph-layout-complete"),
    ).toBe(true);

    await rerender({
      sourceUID: root.uid,
      selectedUID: linked.uid,
      tasks: [
        { ...root, title: "Root title after refresh" },
        { ...linked, title: "Linked title after refresh" },
      ],
      selectedDetail: null,
      onBack: () => {},
      onSelectIssue: () => {},
    });

    await vi.waitFor(() => {
      expect(container.textContent).toContain("Linked title after refresh");
    });
    expect(container.textContent).toContain("Root title after refresh");
    expect(container.textContent).not.toContain("Root browser task");
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
        sourceUID: root.uid,
        selectedUID: root.uid,
        tasks: [root, linked],
        selectedDetail: null,
        onBack: () => {},
        onSelectIssue: () => {},
      },
    });
    container.style.width = "520px";
    container.style.height = "460px";

    await expect.element(page.getByRole("combobox", { name: "Graph layout: Compact" })).toBeVisible();
    const toolbar = container.querySelector<HTMLElement>(".graph-toolbar");
    const hideDone = container.querySelector<HTMLElement>(".hide-done");
    expect(toolbar).toBeTruthy();
    expect(hideDone).toBeTruthy();

    await vi.waitFor(() => {
      const toolbarRect = toolbar!.getBoundingClientRect();
      const hideDoneRect = hideDone!.getBoundingClientRect();
      expect(toolbar!.scrollWidth).toBeLessThanOrEqual(toolbar!.clientWidth + 1);
      expect(hideDoneRect.right).toBeLessThanOrEqual(toolbarRect.right + 1);
      expect(hideDoneRect.bottom).toBeLessThanOrEqual(toolbarRect.bottom + 1);
    });
  });
});
