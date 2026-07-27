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

  it("reports only panes a structural edit may target", () => {
    // A hidden pane stays available — that is what makes it reopenable — but it
    // renders nothing, so maximizing it is a dead command and splitting it moves
    // a pane the user cannot see.
    const layout = store(splitTree());
    render(DetailPaneLayoutTestHarness, { layout });
    expect([...(layout.paneRender()?.editableTabs ?? [])].sort()).toEqual(["conversation", "files", "workspace"]);

    fireEvent.click(screen.getByTestId("pane-hide-workspace"));
    expect(layout.paneRender()?.editableTabs).not.toContain("workspace");
  });

  it("publishes no report until the host has been measured", () => {
    // Width decides whether structural edits are allowed at all; defaulting an
    // unmeasured host to "not flattened" offers those commands for a frame on a
    // narrow layout.
    mockWidth(0);
    const layout = store(splitTree());
    render(DetailPaneLayoutTestHarness, { layout });

    expect(layout.paneRender()).toBeNull();
  });

  it("suppresses every structural control while flattened", () => {
    // A flat leaf merges tabs from several stored leaves, so any structural edit
    // would move panes the user cannot currently see.
    mockWidth(600);
    render(DetailPaneLayoutTestHarness, { layout: store(splitTree()) });

    expect(screen.queryByTestId("tabbed-panel-leaf-actions")).toBeNull();
    expect(screen.queryByRole("separator", { name: "Resize detail panes" })).toBeNull();
  });

  it("shows every pane in one strip while flattened", () => {
    mockWidth(600);
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

  it("offers a close control on a hideable pane, completing the round trip", () => {
    // Deleting the workspace dock removes its close button; without this the
    // reopen strip would be a one-way door out of a state nothing can enter.
    const layout = store(splitTree());
    render(DetailPaneLayoutTestHarness, { layout });

    fireEvent.click(screen.getByTestId("pane-hide-workspace"));
    expect(layout.hiddenTabKeys()).toContain("workspace");
    expect(screen.queryByTestId("pane-workspace")).toBeNull();
  });

  it("offers no close control on a pane that is not hideable", () => {
    render(DetailPaneLayoutTestHarness, { layout: store(splitTree()) });
    expect(screen.queryByTestId("pane-hide-conversation")).toBeNull();
  });

  it("clears the zoom when the maximized pane is closed", () => {
    // Otherwise the stored zoom names a leaf with nothing left in it, and a
    // consumer reading it — the workspace dock-mode derivation — reports
    // "expanded" while the pane is gone from the screen.
    const layout = store(splitTree());
    render(DetailPaneLayoutTestHarness, { layout });

    fireEvent.click(screen.getAllByTestId("pane-toggle-zoom")[1]!);
    expect(layout.zoomedLeafID()).toBe("leaf-workspace");

    fireEvent.click(screen.getByTestId("pane-hide-workspace"));
    expect(layout.zoomedLeafID()).toBeNull();
  });

  it("keeps a zoom when a closed pane shared its leaf with a survivor", () => {
    const layout = store(mergedTree());
    render(DetailPaneLayoutTestHarness, { layout });

    fireEvent.click(screen.getByTestId("pane-toggle-zoom"));
    fireEvent.click(screen.getByTestId("pane-hide-workspace"));

    expect(layout.zoomedLeafID()).toBe("leaf-all");
  });

  it("marks each leaf's own tab selected so a split has no unselected tablist", () => {
    render(DetailPaneLayoutTestHarness, { layout: store(splitTree()) });

    // Keying this to one tree-wide value would leave the workspace leaf's
    // tablist reporting nothing selected.
    expect(screen.getByRole("tab", { name: "Conversation" }).getAttribute("aria-selected")).toBe("true");
    expect(screen.getByRole("tab", { name: "Workspace" }).getAttribute("aria-selected")).toBe("true");
    expect(screen.getByRole("tab", { name: "Files" }).getAttribute("aria-selected")).toBe("false");
  });

  it("drops a stored zoom once the maximized pane stops being available", async () => {
    // Masking it at render time is not enough: a workspace pane zoomed,
    // released, then reclaimed would come back maximized over whatever the user
    // moved on to reading.
    const layout = store(splitTree());
    const { rerender } = render(DetailPaneLayoutTestHarness, { layout });

    fireEvent.click(screen.getAllByTestId("pane-toggle-zoom")[1]!);
    expect(layout.zoomedLeafID()).toBe("leaf-workspace");

    await rerender({ layout, workspaceAvailable: false });
    expect(layout.zoomedLeafID()).toBeNull();

    await rerender({ layout, workspaceAvailable: true });
    expect(layout.zoomedLeafID()).toBeNull();
    expect(screen.getByTestId("pane-conversation").getAttribute("data-visible")).toBe("true");
  });

  it("activates the pane a deep link names and drops a zoom hiding it", async () => {
    // The URL is authoritative over stored layout state; without this the route
    // and the screen disagree with no way for the user to tell why.
    const layout = store(splitTree());
    const { rerender } = render(DetailPaneLayoutTestHarness, { layout, routeTabKey: "conversation" });

    fireEvent.click(screen.getAllByTestId("pane-toggle-zoom")[1]!);
    expect(layout.zoomedLeafID()).toBe("leaf-workspace");

    await rerender({ layout, routeTabKey: "files" });

    expect(layout.zoomedLeafID()).toBeNull();
    expect(screen.getByTestId("pane-files").getAttribute("data-visible")).toBe("true");
    expect(screen.getByRole("tab", { name: "Files" }).getAttribute("aria-selected")).toBe("true");
  });

  it("notifies the surface when focus moves into a different pane body", async () => {
    const layout = store(splitTree());
    const onFocusPane = vi.fn();
    render(DetailPaneLayoutTestHarness, { layout, onFocusPane });

    fireEvent.focusIn(screen.getByTestId("pane-workspace"));
    expect(onFocusPane).toHaveBeenCalledWith("workspace");
    expect(layout.lastFocusedTabKey()).toBe("workspace");

    // Repeat focus in the same pane is not a change, so it must not be reported
    // again — a surface that replaces the URL here would do so on every click.
    onFocusPane.mockClear();
    fireEvent.focusIn(screen.getByTestId("pane-workspace"));
    expect(onFocusPane).not.toHaveBeenCalled();
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
