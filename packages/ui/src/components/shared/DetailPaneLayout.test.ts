import { cleanup, fireEvent, render, screen } from "@testing-library/svelte";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import DetailPaneLayoutTestHarness from "./DetailPaneLayoutTestHarness.svelte";
import type { TabbedPanelNode } from "./tabbed-panel-layout";
import { createPaneLayoutStore, type PaneLayoutStore } from "../../stores/paneLayout.svelte";

const TABS = ["conversation", "files", "workspace"];

function mergedTree(): TabbedPanelNode {
  return { type: "leaf", id: "leaf-all", tabs: TABS, activeTabKey: "conversation" };
}

function splitTree(): TabbedPanelNode {
  return {
    type: "split",
    id: "split-root",
    direction: "vertical",
    ratio: 0.5,
    first: { type: "leaf", id: "leaf-detail", tabs: ["conversation", "files"], activeTabKey: "conversation" },
    second: { type: "leaf", id: "leaf-workspace", tabs: ["workspace"], activeTabKey: "workspace" },
  };
}

function mockWidth(width: number): void {
  vi.spyOn(HTMLElement.prototype, "getBoundingClientRect").mockReturnValue({
    width,
    height: 800,
    x: 0,
    y: 0,
    top: 0,
    right: width,
    bottom: 800,
    left: 0,
    toJSON: () => ({}),
  });
}

function store(tree: TabbedPanelNode): PaneLayoutStore {
  return createPaneLayoutStore("prs", TABS, tree);
}

beforeEach(() => {
  localStorage.clear();
  mockWidth(1600);
});

afterEach(() => {
  cleanup();
  vi.restoreAllMocks();
});

describe("detail pane layout", () => {
  it("renders a pane per available tab and omits unavailable ones", () => {
    render(DetailPaneLayoutTestHarness, {
      layout: store(mergedTree()),
      workspaceAvailable: false,
    });

    expect(screen.getByTestId("pane-conversation")).toBeTruthy();
    expect(screen.getByTestId("pane-files")).toBeTruthy();
    expect(screen.queryByTestId("pane-workspace")).toBeNull();
  });

  it("dispatches horizontal for Split right and vertical for Split down", () => {
    // The whole point of the icon choice. Lucide's horizontal/vertical suffix
    // names the arrangement axis, so the names read as inverted; without this
    // the suite would pass with the two icons transposed.
    const layout = store(mergedTree());
    // Stubbed, not merely observed: letting the real split run would restructure
    // the tree mid-test and render a second control cluster.
    const splitTab = vi.spyOn(layout, "splitTab").mockImplementation(() => {});
    render(DetailPaneLayoutTestHarness, { layout });

    fireEvent.click(screen.getByTestId("pane-split-right"));
    expect(splitTab).toHaveBeenLastCalledWith("conversation", "leaf-all", "horizontal", "after");

    fireEvent.click(screen.getByTestId("pane-split-down"));
    expect(splitTab).toHaveBeenLastCalledWith("conversation", "leaf-all", "vertical", "after");
  });

  it("disables both split buttons on a single-tab leaf", () => {
    // Splitting the only tab out of its own leaf is a no-op in the tree model.
    render(DetailPaneLayoutTestHarness, {
      layout: store(splitTree()),
      workspaceAvailable: true,
    });

    const rightButtons = screen.getAllByTestId("pane-split-right");
    const workspaceLeafButton = rightButtons[1];
    expect(workspaceLeafButton?.hasAttribute("disabled")).toBe(true);
    expect(rightButtons[0]?.hasAttribute("disabled")).toBe(false);
  });

  it("flips the maximize control to Restore for the zoomed leaf", () => {
    const layout = store(mergedTree());
    render(DetailPaneLayoutTestHarness, { layout });

    expect(screen.getByRole("button", { name: "Maximize pane" })).toBeTruthy();
    fireEvent.click(screen.getByTestId("pane-toggle-zoom"));
    expect(screen.getByRole("button", { name: "Restore pane size" })).toBeTruthy();
  });

  it("suppresses every structural control while flattened", () => {
    // A flat leaf merges tabs from several stored leaves, so any structural edit
    // would move panes the user cannot currently see.
    mockWidth(900);
    render(DetailPaneLayoutTestHarness, { layout: store(splitTree()) });

    expect(screen.queryByTestId("tabbed-panel-leaf-actions")).toBeNull();
    expect(screen.queryByRole("separator", { name: "Resize detail panes" })).toBeNull();
  });

  it("shows every pane in one strip while flattened", () => {
    mockWidth(900);
    render(DetailPaneLayoutTestHarness, { layout: store(splitTree()) });

    expect(screen.getAllByRole("tablist")).toHaveLength(1);
    expect(screen.getByTestId("pane-workspace")).toBeTruthy();
  });

  it("offers a way back after a pane is hidden", () => {
    // Hiding can empty a leaf, which then stops rendering, so the affordance
    // cannot live inside the tree.
    const layout = store(splitTree());
    layout.setHidden("workspace", true);
    render(DetailPaneLayoutTestHarness, { layout });

    expect(screen.queryByTestId("pane-workspace")).toBeNull();
    fireEvent.click(screen.getByRole("button", { name: "Show Workspace" }));
    expect(layout.hiddenTabKeys()).not.toContain("workspace");
  });

  it("does not offer a way back for a pane that is merely unavailable", () => {
    // Nothing for the user to restore: no workspace is claimed.
    const layout = store(splitTree());
    render(DetailPaneLayoutTestHarness, { layout, workspaceAvailable: false });

    expect(screen.queryByRole("button", { name: "Show Workspace" })).toBeNull();
  });

  it("records the focused tab and notifies the surface on a tab click", () => {
    const layout = store(mergedTree());
    const onSelectTab = vi.fn();
    render(DetailPaneLayoutTestHarness, { layout, onSelectTab });

    fireEvent.click(screen.getByRole("tab", { name: "Files" }));

    expect(onSelectTab).toHaveBeenCalledWith("files");
    expect(layout.lastFocusedTabKey()).toBe("files");
  });
});
