import { cleanup, fireEvent, render, screen } from "@testing-library/svelte";
import { afterEach, describe, expect, it, vi } from "vitest";
import TabbedPanelTreeTestHarness from "./TabbedPanelTreeTestHarness.svelte";
import { currentTerminalGeometryIntent, hasTerminalGeometryIntent } from "../terminal/terminalGeometryIntent.js";
import {
  appendTabbedPanelTabToLeaf,
  moveTabbedPanelTabBefore,
  splitTabbedPanelTabIntoLeaf,
  type TabbedPanelNode,
} from "./tabbed-panel-layout";

function fakeDataTransfer(): DataTransfer {
  const data = new Map<string, string>();
  return {
    dropEffect: "none",
    effectAllowed: "none",
    getData: (type: string) => data.get(type) ?? "",
    setData: (type: string, value: string) => {
      data.set(type, value);
    },
    setDragImage: vi.fn(),
  } as unknown as DataTransfer;
}

function mockSplitRect(width = 1000, height = 600): void {
  vi.spyOn(HTMLElement.prototype, "getBoundingClientRect").mockReturnValue({
    width,
    height,
    x: 0,
    y: 0,
    top: 0,
    right: width,
    bottom: height,
    left: 0,
    toJSON: () => ({}),
  });
}

function leafNode(): TabbedPanelNode {
  return {
    type: "leaf",
    id: "leaf-1",
    tabs: ["feed", "detail"],
    activeTabKey: "detail",
  };
}

function splitNode(direction: "horizontal" | "vertical" = "horizontal"): TabbedPanelNode {
  return {
    type: "split",
    id: "split-1",
    direction,
    ratio: 0.4,
    first: leafNode(),
    second: {
      type: "leaf",
      id: "leaf-2",
      tabs: ["files"],
      activeTabKey: "files",
    },
  };
}

describe("TabbedPanelTree", () => {
  afterEach(async () => {
    if (vi.isFakeTimers()) {
      vi.runOnlyPendingTimers();
      vi.useRealTimers();
    } else if (hasTerminalGeometryIntent()) {
      await new Promise((resolve) => setTimeout(resolve, 251));
    }
    cleanup();
    vi.restoreAllMocks();
    vi.unstubAllGlobals();
  });

  it("renders arbitrary tab content, icons, status, and actions", () => {
    render(TabbedPanelTreeTestHarness, {
      props: { node: leafNode() },
    });

    const detailTab = screen.getByRole("tab", { name: /Detail/ });
    expect(detailTab.getAttribute("aria-selected")).toBe("true");
    expect(screen.getByTestId("panel-detail").dataset.active).toBe("true");
    expect(screen.getByTestId("panel-feed").dataset.active).toBe("false");
    expect(screen.getByTestId("icon-detail")).toBeTruthy();
    expect(screen.getByRole("button", { name: "Action Detail" }).className).toContain("tabbed-panel-tab-tool");
    expect(screen.getByRole("tab", { name: "Feed, Feed updating" })).toBeTruthy();
    expect(screen.getByLabelText("Feed updating").classList.contains("kit-status-dot--working")).toBe(true);
  });

  it("reports focus while pointer and wheel leave the active pane unchanged", async () => {
    const onFocusPane = vi.fn();
    const view = render(TabbedPanelTreeTestHarness, {
      props: {
        node: splitNode(),
        activeTabKey: "detail",
        onFocusPane,
      },
    });

    const activeLeaves = () => Array.from(document.querySelectorAll<HTMLElement>(".tabbed-panel-leaf.input-active"));
    expect(activeLeaves()).toHaveLength(1);
    expect(activeLeaves()[0]?.contains(screen.getByTestId("panel-detail"))).toBe(true);

    await fireEvent.pointerDown(screen.getByTestId("panel-files"));
    await fireEvent.wheel(screen.getByTestId("panel-detail"));
    expect(onFocusPane).not.toHaveBeenCalled();

    await fireEvent.focusIn(screen.getByTestId("panel-files"));
    await fireEvent.focusIn(screen.getByTestId("panel-detail"));
    expect(onFocusPane).toHaveBeenNthCalledWith(1, "files", "leaf-2");
    expect(onFocusPane).toHaveBeenNthCalledWith(2, "detail", "leaf-1");

    await view.rerender({
      node: splitNode(),
      activeTabKey: "files",
      onFocusPane,
    });

    expect(activeLeaves()).toHaveLength(1);
    expect(activeLeaves()[0]?.contains(screen.getByTestId("panel-files"))).toBe(true);
  });

  it("keeps nested tab focus scoped to the outer leaf", async () => {
    const onFocusPane = vi.fn();
    render(TabbedPanelTreeTestHarness, {
      props: { node: leafNode(), onFocusPane },
    });
    const outerPanel = screen.getByTestId("panel-detail");
    const nestedLeaf = document.createElement("section");
    nestedLeaf.className = "tabbed-panel-leaf";
    const nestedTab = document.createElement("button");
    nestedTab.dataset.tabbedPanelTabKey = "nested-session";
    nestedLeaf.append(nestedTab);
    outerPanel.append(nestedLeaf);

    await fireEvent.focusIn(nestedTab);

    expect(onFocusPane).toHaveBeenCalledWith("detail", "leaf-1");
  });

  it("shows a moving insertion slot while sorting tabs", async () => {
    vi.stubGlobal("requestAnimationFrame", (callback: FrameRequestCallback) => {
      callback(0);
      return 1;
    });
    render(TabbedPanelTreeTestHarness, {
      props: {
        node: leafNode(),
        onMoveTabBefore: vi.fn(),
        onAppendTabToLeaf: vi.fn(),
      },
    });
    const dataTransfer = fakeDataTransfer();
    const detailTab = screen.getByRole("tab", { name: /Detail/ });
    const feedHost = screen.getByRole("tab", { name: /Feed/ }).closest(".tabbed-panel-tab");
    expect(feedHost).toBeTruthy();

    await fireEvent.dragStart(detailTab, { dataTransfer });
    await fireEvent.dragOver(feedHost!, {
      clientX: -1,
      dataTransfer,
    });

    expect(screen.getByTestId("tabbed-panel-tab-drop-placeholder")).toBeTruthy();
    expect(detailTab.closest(".tabbed-panel-tab")?.classList.contains("dragging")).toBe(true);

    await fireEvent.dragEnd(detailTab);

    expect(screen.queryByTestId("tabbed-panel-tab-drop-placeholder")).toBeNull();
  });

  it("previews and drops tabs into another split leaf", async () => {
    vi.stubGlobal("requestAnimationFrame", (callback: FrameRequestCallback) => {
      callback(0);
      return 1;
    });
    const onMoveTabBefore = vi.fn();
    render(TabbedPanelTreeTestHarness, {
      props: {
        node: splitNode(),
        onMoveTabBefore,
        onAppendTabToLeaf: vi.fn(),
      },
    });
    const dataTransfer = fakeDataTransfer();
    const filesTab = screen.getByRole("tab", { name: /Files/ });
    const feedHost = screen.getByRole("tab", { name: /Feed/ }).closest(".tabbed-panel-tab");
    expect(feedHost).toBeTruthy();
    vi.spyOn(feedHost!, "getBoundingClientRect").mockReturnValue({
      width: 120,
      height: 30,
      x: 100,
      y: 0,
      top: 0,
      right: 220,
      bottom: 30,
      left: 100,
      toJSON: () => ({}),
    });

    await fireEvent.dragStart(filesTab, { dataTransfer });
    await fireEvent.dragOver(feedHost!, {
      clientX: 210,
      dataTransfer,
    });

    expect(screen.getByTestId("tabbed-panel-tab-drop-placeholder")).toBeTruthy();

    await fireEvent.drop(feedHost!, {
      clientX: 210,
      dataTransfer,
    });

    expect(onMoveTabBefore).toHaveBeenCalledWith("files", "detail");

    await fireEvent.dragEnd(filesTab);
  });

  it("appends from empty tab-strip space when sorting is disabled", async () => {
    vi.stubGlobal("requestAnimationFrame", (callback: FrameRequestCallback) => {
      callback(0);
      return 1;
    });
    const onAppendTabToLeaf = vi.fn();
    render(TabbedPanelTreeTestHarness, {
      props: {
        node: splitNode(),
        onAppendTabToLeaf,
      },
    });
    const dataTransfer = fakeDataTransfer();
    const filesTab = screen.getByRole("tab", { name: /Files/ });
    const targetTablist = screen.getAllByRole("tablist", { name: "Test panel tabs" })[0];
    expect(targetTablist).toBeTruthy();

    await fireEvent.dragStart(filesTab, { dataTransfer });
    await fireEvent.dragOver(targetTablist!, {
      clientX: 500,
      dataTransfer,
    });
    await fireEvent.drop(targetTablist!, {
      clientX: 500,
      dataTransfer,
    });

    expect(screen.queryByTestId("tabbed-panel-tab-drop-placeholder")).toBeNull();
    expect(onAppendTabToLeaf).toHaveBeenCalledWith("files", "leaf-1");

    await fireEvent.dragEnd(filesTab);
  });

  it("moves tab state before a target tab", () => {
    const next = moveTabbedPanelTabBefore(leafNode(), "detail", "feed");

    expect(next).toEqual({
      type: "leaf",
      id: "leaf-1",
      tabs: ["detail", "feed"],
      activeTabKey: "feed",
    });
  });

  it("keeps tab state intact when move targets are stale", () => {
    const node = splitNode();

    expect(moveTabbedPanelTabBefore(node, "detail", "missing")).toBe(node);
    expect(appendTabbedPanelTabToLeaf(node, "detail", "missing")).toBe(node);
    expect(splitTabbedPanelTabIntoLeaf(node, "detail", "missing", "horizontal", "after")).toBe(node);
  });

  it("reports horizontal split ratio changes and pixel ARIA values", async () => {
    mockSplitRect();
    Object.defineProperties(HTMLElement.prototype, {
      setPointerCapture: { configurable: true, value: vi.fn() },
      hasPointerCapture: { configurable: true, value: vi.fn(() => true) },
      releasePointerCapture: { configurable: true, value: vi.fn() },
    });
    const onRatioChange = vi.fn();
    render(TabbedPanelTreeTestHarness, {
      props: {
        node: splitNode(),
        onRatioChange,
      },
    });

    const divider = screen.getByRole("separator", {
      name: "Resize test split",
    });
    expect(divider.getAttribute("aria-orientation")).toBe("vertical");
    expect(divider.getAttribute("aria-valuemin")).toBe("120");
    expect(divider.getAttribute("aria-valuemax")).toBe("880");
    expect(divider.getAttribute("aria-valuenow")).toBe("400");

    await fireEvent.pointerDown(divider, { clientX: 400, clientY: 200, pointerId: 1, button: 0 });
    await fireEvent.pointerMove(divider, { clientX: 700, clientY: 500, pointerId: 1 });
    await fireEvent.pointerUp(divider, { clientX: 700, clientY: 500, pointerId: 1 });

    expect(onRatioChange).toHaveBeenCalledWith("split-1", 0.7);

    onRatioChange.mockClear();
    await fireEvent.keyDown(divider, { key: "ArrowLeft" });
    expect(onRatioChange).toHaveBeenCalledWith("split-1", 0.376);

    onRatioChange.mockClear();
    await fireEvent.keyDown(divider, { key: "ArrowUp" });
    expect(onRatioChange).not.toHaveBeenCalled();
  });

  it("marks effective pointer and keyboard panel changes as deliberate geometry", async () => {
    mockSplitRect();
    Object.defineProperties(HTMLElement.prototype, {
      setPointerCapture: { configurable: true, value: vi.fn() },
      hasPointerCapture: { configurable: true, value: vi.fn(() => true) },
      releasePointerCapture: { configurable: true, value: vi.fn() },
    });
    vi.useFakeTimers();
    render(TabbedPanelTreeTestHarness, {
      props: { node: splitNode(), onRatioChange: vi.fn() },
    });
    const divider = screen.getByRole("separator", { name: "Resize test split" });

    await fireEvent.pointerDown(divider, { clientX: 400, pointerId: 1, button: 0 });
    await fireEvent.pointerMove(divider, { clientX: 424, pointerId: 1 });
    expect(hasTerminalGeometryIntent()).toBe(true);
    const pointerGeneration = currentTerminalGeometryIntent();
    await fireEvent.pointerMove(divider, { clientX: 448, pointerId: 1 });
    expect(currentTerminalGeometryIntent()).toBe(pointerGeneration);
    await fireEvent.pointerUp(divider, { clientX: 424, pointerId: 1 });

    vi.runOnlyPendingTimers();
    expect(hasTerminalGeometryIntent()).toBe(false);
    await fireEvent.keyDown(divider, { key: "ArrowRight" });
    expect(hasTerminalGeometryIntent()).toBe(true);
    expect(currentTerminalGeometryIntent()).not.toBe(pointerGeneration);
  });

  it("uses the vertical axis for stacked split resizing", async () => {
    mockSplitRect();
    Object.defineProperties(HTMLElement.prototype, {
      setPointerCapture: { configurable: true, value: vi.fn() },
      hasPointerCapture: { configurable: true, value: vi.fn(() => true) },
      releasePointerCapture: { configurable: true, value: vi.fn() },
    });
    const onRatioChange = vi.fn();
    render(TabbedPanelTreeTestHarness, {
      props: {
        node: splitNode("vertical"),
        onRatioChange,
      },
    });

    const divider = screen.getByRole("separator", { name: "Resize test split" });
    expect(divider.getAttribute("aria-orientation")).toBe("horizontal");
    expect(divider.getAttribute("aria-valuenow")).toBe("240");

    await fireEvent.pointerDown(divider, { clientX: 200, clientY: 240, pointerId: 1, button: 0 });
    await fireEvent.pointerMove(divider, { clientX: 500, clientY: 360, pointerId: 1 });
    await fireEvent.pointerUp(divider, { clientX: 500, clientY: 360, pointerId: 1 });

    expect(onRatioChange).toHaveBeenCalledWith("split-1", expect.closeTo(0.6));

    onRatioChange.mockClear();
    await fireEvent.keyDown(divider, { key: "ArrowDown" });
    expect(onRatioChange).toHaveBeenCalledWith("split-1", 0.44);

    onRatioChange.mockClear();
    await fireEvent.keyDown(divider, { key: "ArrowRight" });
    expect(onRatioChange).not.toHaveBeenCalled();
  });

  it("updates only the targeted split in a mixed-axis tree", async () => {
    mockSplitRect();
    const onRatioChange = vi.fn();
    const nested: TabbedPanelNode = {
      type: "split",
      id: "outer-split",
      direction: "horizontal",
      ratio: 0.5,
      first: leafNode(),
      second: {
        type: "split",
        id: "inner-split",
        direction: "vertical",
        ratio: 0.4,
        first: {
          type: "leaf",
          id: "leaf-2",
          tabs: ["files"],
          activeTabKey: "files",
        },
        second: {
          type: "leaf",
          id: "leaf-3",
          tabs: [],
          activeTabKey: "",
        },
      },
    };
    render(TabbedPanelTreeTestHarness, {
      props: { node: nested, onRatioChange },
    });

    const dividers = screen.getAllByRole("separator", {
      name: "Resize test split",
    });
    expect(dividers).toHaveLength(2);
    await fireEvent.keyDown(dividers[1]!, { key: "ArrowDown" });

    expect(onRatioChange).toHaveBeenCalledTimes(1);
    expect(onRatioChange).toHaveBeenCalledWith("inner-split", 0.44);
  });

  it("disables tab movement and split resizing", async () => {
    const onMoveTabBefore = vi.fn();
    const onAppendTabToLeaf = vi.fn();
    const onSplitTab = vi.fn();
    const onRatioChange = vi.fn();
    render(TabbedPanelTreeTestHarness, {
      props: {
        node: splitNode(),
        disabled: true,
        onMoveTabBefore,
        onAppendTabToLeaf,
        onSplitTab,
        onRatioChange,
      },
    });
    const dataTransfer = fakeDataTransfer();
    const filesTab = screen.getByRole("tab", { name: /Files/ });
    const feedHost = screen.getByRole("tab", { name: /Feed/ }).closest(".tabbed-panel-tab");
    expect(feedHost).toBeTruthy();

    expect(filesTab.hasAttribute("disabled")).toBe(true);
    expect(filesTab.getAttribute("draggable")).toBe("false");

    await fireEvent.dragStart(filesTab, { dataTransfer });
    await fireEvent.dragOver(feedHost!, {
      clientX: 210,
      dataTransfer,
    });
    await fireEvent.drop(feedHost!, {
      clientX: 210,
      dataTransfer,
    });

    expect(screen.queryByTestId("tabbed-panel-tab-drop-placeholder")).toBeNull();
    expect(onMoveTabBefore).not.toHaveBeenCalled();
    expect(onAppendTabToLeaf).not.toHaveBeenCalled();
    expect(onSplitTab).not.toHaveBeenCalled();

    const divider = screen.getByRole("separator", {
      name: "Resize test split",
    });
    expect(divider.hasAttribute("disabled")).toBe(true);
    await fireEvent.pointerDown(divider, { clientX: 400, pointerId: 1 });
    await fireEvent.pointerMove(divider, { clientX: 700, pointerId: 1 });

    expect(onRatioChange).not.toHaveBeenCalled();
  });
});

describe("zoom and leaf actions", () => {
  function splitTree(): TabbedPanelNode {
    return {
      type: "split",
      id: "split-root",
      direction: "horizontal",
      ratio: 0.5,
      first: { type: "leaf", id: "leaf-a", tabs: ["feed", "detail"], activeTabKey: "detail" },
      second: { type: "leaf", id: "leaf-b", tabs: ["files"], activeTabKey: "files" },
    };
  }

  it("hides the sibling branch while keeping its subtree mounted", () => {
    render(TabbedPanelTreeTestHarness, { node: splitTree(), zoomedLeafID: "leaf-b" });

    // Mounted, not destroyed: a reparented singleton pane and any scroll
    // offsets have to survive a zoom.
    const hiddenPanel = screen.getByTestId("panel-detail");
    expect(hiddenPanel).toBeTruthy();

    const hiddenBranch = hiddenPanel.closest<HTMLElement>(".tabbed-panel-split-child");
    expect(hiddenBranch?.hasAttribute("hidden")).toBe(true);
    // Svelte sets `inert` as a DOM property, which jsdom does not reflect back
    // to an attribute — assert the property, which is what actually removes the
    // subtree from focus and pointer reach.
    expect(hiddenBranch?.inert).toBe(true);

    const zoomedBranch = screen.getByTestId("panel-files").closest(".tabbed-panel-split-child");
    expect(zoomedBranch?.classList.contains("zoomed")).toBe(true);
    expect(zoomedBranch?.hasAttribute("hidden")).toBe(false);
  });

  it("reports a zoom-hidden active panel as not visible", () => {
    // The regression this guards: leaf-a's active tab is 'detail', so a
    // per-leaf visibility check would say visible even though an ancestor's
    // other branch holds the zoom. The inline workspace's host placement and
    // focus read this flag.
    render(TabbedPanelTreeTestHarness, { node: splitTree(), zoomedLeafID: "leaf-b" });

    expect(screen.getByTestId("panel-detail").dataset.active).toBe("false");
    expect(screen.getByTestId("panel-files").dataset.active).toBe("true");
  });

  it("marks the active panel visible when nothing is zoomed", () => {
    render(TabbedPanelTreeTestHarness, { node: splitTree() });

    expect(screen.getByTestId("panel-detail").dataset.active).toBe("true");
    expect(screen.getByTestId("panel-files").dataset.active).toBe("true");
  });

  it("removes the divider on the path to a zoomed leaf", () => {
    // Leaving it rendered would put a draggable handle over a supposedly
    // full-size pane, silently changing a ratio the user cannot see.
    render(TabbedPanelTreeTestHarness, { node: splitTree(), zoomedLeafID: "leaf-b" });
    expect(screen.queryByRole("separator", { name: "Resize test split" })).toBeNull();
  });

  it("keeps the divider when no leaf is zoomed", () => {
    render(TabbedPanelTreeTestHarness, { node: splitTree() });
    expect(screen.getByRole("separator", { name: "Resize test split" })).toBeTruthy();
  });

  it("ignores a zoom naming a leaf outside the tree", () => {
    render(TabbedPanelTreeTestHarness, { node: splitTree(), zoomedLeafID: "leaf-ghost" });

    expect(screen.getByRole("separator", { name: "Resize test split" })).toBeTruthy();
    for (const child of document.querySelectorAll(".tabbed-panel-split-child")) {
      expect(child.hasAttribute("hidden")).toBe(false);
    }
  });

  it("renders one leaf action cluster per leaf, carrying that leaf's id", () => {
    render(TabbedPanelTreeTestHarness, { node: splitTree(), withLeafActions: true });

    expect(screen.getAllByTestId("tabbed-panel-leaf-actions")).toHaveLength(2);
    expect(screen.getByTestId("leaf-action-leaf-a")).toBeTruthy();
    expect(screen.getByTestId("leaf-action-leaf-b")).toBeTruthy();
  });

  it("omits the cluster entirely when no leafActions snippet is supplied", () => {
    // How the narrow flattened renderer suppresses structural controls: it
    // simply does not pass the snippet.
    render(TabbedPanelTreeTestHarness, { node: splitTree() });
    expect(screen.queryByTestId("tabbed-panel-leaf-actions")).toBeNull();
  });

  it("lets a leaf action act on its own leaf", async () => {
    const onLeafAction = vi.fn();
    render(TabbedPanelTreeTestHarness, {
      node: splitTree(),
      withLeafActions: true,
      onLeafAction,
    });

    await fireEvent.click(screen.getByTestId("leaf-action-leaf-a"));
    expect(onLeafAction).toHaveBeenCalledWith("leaf-a");
  });

  it("disables a leaf action that cannot apply to a single-tab leaf", () => {
    render(TabbedPanelTreeTestHarness, { node: splitTree(), withLeafActions: true });

    expect(screen.getByTestId("leaf-action-leaf-b").hasAttribute("disabled")).toBe(true);
    expect(screen.getByTestId("leaf-action-leaf-a").hasAttribute("disabled")).toBe(false);
  });
});

describe("TabbedPanelTree keyboard", () => {
  afterEach(() => {
    cleanup();
    vi.restoreAllMocks();
  });

  it("keeps one tab stop per strip and moves selection with the arrow keys", async () => {
    const onSelectTab = vi.fn();
    render(TabbedPanelTreeTestHarness, { node: leafNode(), onSelectTab });

    const tabs = screen.getAllByRole("tab");
    expect(tabs.map((tab) => tab.tabIndex)).toEqual([-1, 0]);

    const detailTab = screen.getByRole("tab", { name: "Detail" });
    detailTab.focus();
    await fireEvent.keyDown(detailTab, { key: "ArrowLeft" });
    expect(onSelectTab).toHaveBeenCalledWith("feed");
    expect(document.activeElement).toBe(screen.getByRole("tab", { name: "Feed, Feed updating" }));

    await fireEvent.keyDown(document.activeElement as HTMLElement, { key: "ArrowLeft" });
    expect(onSelectTab).toHaveBeenLastCalledWith("detail");

    await fireEvent.keyDown(detailTab, { key: "Tab" });
    expect(onSelectTab).toHaveBeenCalledTimes(2);
  });
});
