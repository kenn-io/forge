import { cleanup, fireEvent, render, screen } from "@testing-library/svelte";
import { afterEach, describe, expect, it, vi } from "vitest";
import TabbedPanelTreeTestHarness from "./TabbedPanelTreeTestHarness.svelte";
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

function leafNode(): TabbedPanelNode {
  return {
    type: "leaf",
    id: "leaf-1",
    tabs: ["feed", "detail"],
    activeTabKey: "detail",
  };
}

function splitNode(): TabbedPanelNode {
  return {
    type: "split",
    id: "split-1",
    direction: "horizontal",
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
  afterEach(() => {
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
    expect(screen.getByRole("tab", { name: /Feed/ }).querySelector(".tabbed-panel-status-dot.running")).toBeTruthy();
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

  it("reports split ratio changes from the divider", async () => {
    Object.defineProperty(HTMLElement.prototype, "setPointerCapture", {
      configurable: true,
      value: vi.fn(),
    });
    Object.defineProperty(HTMLElement.prototype, "releasePointerCapture", {
      configurable: true,
      value: vi.fn(),
    });
    vi.spyOn(HTMLElement.prototype, "getBoundingClientRect").mockImplementation(
      function rectForElement(this: HTMLElement): DOMRect {
        const width = this.classList.contains("tabbed-panel-split") ? 1000 : 100;
        return {
          width,
          height: 600,
          x: 0,
          y: 0,
          top: 0,
          right: width,
          bottom: 600,
          left: 0,
          toJSON: () => ({}),
        };
      },
    );
    const onRatioChange = vi.fn();
    render(TabbedPanelTreeTestHarness, {
      props: {
        node: splitNode(),
        onRatioChange,
      },
    });

    const divider = screen.getByRole("button", {
      name: "Resize test split",
    });
    await fireEvent.pointerDown(divider, { clientX: 400, pointerId: 1 });
    window.dispatchEvent(new MouseEvent("pointermove", { clientX: 700, bubbles: true }));
    window.dispatchEvent(new MouseEvent("pointerup", { clientX: 700, bubbles: true }));

    expect(onRatioChange).toHaveBeenCalledWith("split-1", 0.7);
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

    const divider = screen.getByRole("button", {
      name: "Resize test split",
    });
    expect(divider.hasAttribute("disabled")).toBe(true);
    await fireEvent.pointerDown(divider, { clientX: 400, pointerId: 1 });
    window.dispatchEvent(new MouseEvent("pointermove", { clientX: 700, bubbles: true }));

    expect(onRatioChange).not.toHaveBeenCalled();
  });
});
