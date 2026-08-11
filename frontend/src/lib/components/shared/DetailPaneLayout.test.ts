import { cleanup, fireEvent, render, screen, within } from "@testing-library/svelte";
import { flushSync } from "svelte";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import DetailPaneLayoutTestHarness from "./DetailPaneLayoutTestHarness.svelte";
import type { TabbedPanelNode } from "./tabbed-panel-layout";
import { createPaneLayoutStore, type PaneLayoutStore } from "../../stores/paneLayout.svelte";
import { isSessionPaneKey, sessionPaneKey } from "../../stores/session-pane-key";
import { clearActiveTabbedPanelDrag, startTabbedPanelTabDrag } from "./tabbed-panel-drag";

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

/** A surface that accepts promoted session panes, like the real ones do. */
function promotableStore(tree: TabbedPanelNode): PaneLayoutStore {
  return createPaneLayoutStore("prs", TABS, tree, isSessionPaneKey);
}

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

/** Start a drag of a tab this tree has never held, as the workspace pane does. */
function startForeignTabDrag(scope: string, tabKey: string): DataTransfer {
  const dataTransfer = fakeDataTransfer();
  startTabbedPanelTabDrag({ dataTransfer } as unknown as DragEvent, { scope, tabKey });
  return dataTransfer;
}

/**
 * Fire a drag event that actually carries a pointer position.
 *
 * `fireEvent.dragOver`/`fireEvent.drop` drop clientX/clientY here, so the edge
 * maths ran on NaN -- and `NaN > threshold` is false, which made every drop read
 * as the first edge in the list. A centre drop, which is how a tab is appended to
 * a leaf, was impossible to express.
 */
function fireDragEvent(
  target: Element,
  type: "dragover" | "drop",
  init: { dataTransfer: DataTransfer; clientX: number; clientY: number },
): void {
  const event = new Event(type, { bubbles: true, cancelable: true });
  Object.assign(event, init);
  target.dispatchEvent(event);
  // fireEvent awaits a tick for us; a bare dispatch does not, and every assertion
  // here reads rendered DOM.
  flushSync();
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

  it("drops panes covered by another leaf's zoom from the report", () => {
    // A zoom covers its siblings entirely, so their panes are as absent from the
    // screen as a hidden one: maximizing or splitting them rearranges a tree the
    // user cannot see.
    const layout = store(splitTree());
    render(DetailPaneLayoutTestHarness, { layout });

    flushSync(() => layout.toggleZoom("leaf-detail"));
    expect([...(layout.paneRender()?.editableTabs ?? [])].sort()).toEqual(["conversation", "files"]);
    expect(layout.paneRender()?.onScreenTabs).toEqual(["conversation"]);

    flushSync(() => layout.clearZoom());
    expect([...(layout.paneRender()?.editableTabs ?? [])].sort()).toEqual(["conversation", "files", "workspace"]);
  });

  it("reports one on-screen tab per rendered leaf", () => {
    // A tab behind a sibling in the same leaf is one click away, so it stays a
    // legitimate command target, but it is not on screen — which is the question
    // the push-vs-replace history rule asks.
    const layout = store(splitTree());
    render(DetailPaneLayoutTestHarness, { layout });

    expect(layout.paneRender()?.onScreenTabs).toEqual(["conversation", "workspace"]);
    expect(layout.paneRender()?.editableTabs).toContain("files");

    flushSync(() => layout.activateTab("files"));
    expect(layout.paneRender()?.onScreenTabs).toEqual(["files", "workspace"]);
  });

  it("reports the single flat tab as the only one on screen", () => {
    mockWidth(600);
    const layout = store(splitTree());
    render(DetailPaneLayoutTestHarness, { layout });

    // Whatever the stored tree says, a flat strip shows exactly one pane.
    expect(layout.paneRender()?.onScreenTabs).toHaveLength(1);
  });

  it("assigns narrow-layout input ownership to the rendered synthetic leaf", () => {
    mockWidth(600);
    const layout = store(splitTree());
    render(DetailPaneLayoutTestHarness, { layout });
    const conversation = screen.getByTestId("pane-conversation");

    fireEvent.focusIn(conversation);

    expect(conversation.getAttribute("data-input-active")).toBe("true");
    expect(layout.paneRender()?.activeInputTabKey).toBe("conversation");
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

  it("keeps the report published while republishing it", () => {
    // A caller may name a pane from the report — the workspace pane's tab takes
    // its sole session's name — so that label is a tab list input as well as a
    // report output. Clearing the report before each republish flips the label,
    // which changes the tab list, which republishes: the effect never settles and
    // Svelte aborts the whole tree, leaving the surface unresponsive.
    const layout = store(mergedTree());
    render(DetailPaneLayoutTestHarness, { layout, labelFromRender: true });

    expect(screen.getByRole("tab", { name: "Session" })).toBeTruthy();
    expect(layout.paneRender()?.flattened).toBe(false);
  });

  it("renders caller chrome in each leaf's action area", () => {
    // The tab strip is a pane's only chrome, and the structural controls occupy
    // the snippet TabbedPanelTree offers, so a caller with its own control for a
    // pane has nowhere else to put it.
    const layout = store(splitTree());
    render(DetailPaneLayoutTestHarness, { layout, withLeafExtras: true });

    expect(screen.getByTestId("leaf-extra-leaf-detail")).toBeTruthy();
    expect(screen.getByTestId("leaf-extra-leaf-workspace")).toBeTruthy();
  });

  it("suppresses caller chrome along with the structural controls while flattened", () => {
    const layout = store(splitTree());
    mockWidth(600);
    render(DetailPaneLayoutTestHarness, { layout, withLeafExtras: true });

    // One strip for every pane: chrome that named a leaf would be lying about
    // which pane it acts on.
    expect(screen.queryByTestId("leaf-extra-leaf-detail")).toBeNull();
    expect(screen.queryByTestId("leaf-extra-leaf-workspace")).toBeNull();
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
    // Both leaves hold two tabs, so both still draw a strip: a leaf holding only
    // the workspace draws none, and a tablist that is not rendered cannot be the
    // one reporting nothing selected.
    render(DetailPaneLayoutTestHarness, {
      layout: store({
        type: "split",
        id: "split-root",
        direction: "vertical",
        ratio: 0.5,
        first: { type: "leaf", id: "leaf-detail", tabs: ["conversation", "files"], activeTabKey: "conversation" },
        second: { type: "leaf", id: "leaf-workspace", tabs: ["workspace"], activeTabKey: "workspace" },
      }),
    });

    // Keying this to one tree-wide value would leave the second leaf's tablist
    // reporting nothing selected.
    expect(screen.getByRole("tab", { name: "Conversation" }).getAttribute("aria-selected")).toBe("true");
    expect(screen.getByRole("tab", { name: "Files" }).getAttribute("aria-selected")).toBe("false");
  });

  it("drops the strip for a leaf that holds only the workspace, keeping its controls", () => {
    // The workspace pane draws its own strip inside - one tab per session, plus the
    // dock - so a leaf holding it alone stacked two rows to name one thing, and the
    // outer one said only "Workspace". The remaining controls move onto the pane
    // instead without inventing a separate drag affordance.
    render(DetailPaneLayoutTestHarness, { layout: store(splitTree()), withLeafExtras: true });

    const workspaceBody = screen.getByTestId("pane-workspace").closest(".tabbed-panel-body")!;
    expect(workspaceBody.parentElement?.querySelector('[role="tablist"]')).toBeNull();

    // Anchored to the leaf, not the body: a pane that resizes itself (a terminal
    // refitting) would otherwise drag the cluster around with it.
    const cluster = screen.getByTestId("tabbed-panel-solo-actions");
    expect(workspaceBody.contains(cluster)).toBe(false);
    expect(workspaceBody.closest(".tabbed-panel-leaf")?.contains(cluster)).toBe(true);
    expect(within(cluster).queryByRole("button", { name: "Move Workspace" })).toBeNull();
    expect(within(cluster).getByTestId("pane-hide-workspace")).toBeTruthy();
    expect(within(cluster).getByTestId("pane-toggle-zoom")).toBeTruthy();
    expect(within(cluster).getByTestId("leaf-extra-leaf-workspace")).toBeTruthy();

    // The detail leaf holds two tabs, so it keeps the strip it needs to tell them
    // apart.
    expect(screen.getByRole("tab", { name: "Conversation" })).toBeTruthy();
  });

  it("brings the strip back when a second tab lands in the workspace leaf", () => {
    // Two panes in a leaf need a row to choose between them, so the suppression is
    // about the leaf's contents, not about the workspace pane being special.
    const layout = store(splitTree());
    render(DetailPaneLayoutTestHarness, { layout });
    expect(screen.queryByRole("tab", { name: "Workspace" })).toBeNull();

    flushSync(() => layout.appendTabToLeaf("files", "leaf-workspace"));

    const workspaceTab = screen.getByRole("tab", { name: "Workspace" });
    expect(workspaceTab.getAttribute("draggable")).toBe("true");
    expect(screen.queryByTestId("tabbed-panel-solo-actions")).toBeNull();
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

  it("does not invent a focused pane when the focused pane stops rendering", async () => {
    const layout = store(splitTree());
    const { rerender } = render(DetailPaneLayoutTestHarness, { layout, routeTabKey: "conversation" });
    const activePaneKey = () =>
      document.querySelector(".tabbed-panel-leaf.input-active [data-pane-key]")?.getAttribute("data-pane-key");

    expect(activePaneKey()).toBeUndefined();
    expect(layout.paneRender()?.activeInputTabKey).toBeNull();

    fireEvent.focusIn(screen.getByTestId("pane-workspace"));
    expect(activePaneKey()).toBe("workspace");
    expect(layout.paneRender()?.activeInputTabKey).toBe("workspace");

    layout.setHidden("workspace", true);
    flushSync();
    expect(activePaneKey()).toBeUndefined();
    expect(layout.paneRender()?.activeInputTabKey).toBeNull();

    layout.setHidden("workspace", false);
    flushSync();
    fireEvent.focusIn(screen.getByTestId("pane-workspace"));
    await rerender({ layout, routeTabKey: "conversation", workspaceAvailable: false });
    expect(activePaneKey()).toBeUndefined();
    expect(layout.paneRender()?.activeInputTabKey).toBeNull();

    await rerender({ layout, routeTabKey: "conversation", workspaceAvailable: true });
    fireEvent.focusIn(screen.getByTestId("pane-workspace"));
    layout.toggleZoom("leaf-detail");
    flushSync();
    expect(activePaneKey()).toBeUndefined();
    expect(layout.paneRender()?.activeInputTabKey).toBeNull();
  });

  it("moves keyboard ownership only when DOM focus moves", () => {
    const layout = store(splitTree());
    render(DetailPaneLayoutTestHarness, { layout, routeTabKey: "conversation" });
    const activePaneKey = () =>
      document.querySelector(".tabbed-panel-leaf.input-active [data-pane-key]")?.getAttribute("data-pane-key");

    fireEvent.focusIn(screen.getByTestId("pane-workspace"));
    expect(activePaneKey()).toBe("workspace");
    expect(screen.getByTestId("pane-workspace").getAttribute("data-input-active")).toBe("true");

    fireEvent.pointerDown(screen.getByTestId("pane-conversation"));
    fireEvent.wheel(screen.getByTestId("pane-conversation"));
    expect(activePaneKey()).toBe("workspace");
    expect(screen.getByTestId("pane-conversation").getAttribute("data-input-active")).toBe("false");

    layout.setExternalInputActive(true);
    flushSync();
    expect(activePaneKey()).toBeUndefined();
    expect(layout.paneRender()?.activeInputTabKey).toBeNull();

    layout.noteFocused("conversation");
    expect(layout.externalInputActive()).toBe(true);

    fireEvent.focusIn(screen.getByTestId("pane-workspace"));
    expect(layout.externalInputActive()).toBe(false);
    expect(activePaneKey()).toBe("workspace");
    expect(layout.paneRender()?.activeInputTabKey).toBe("workspace");

    fireEvent.focusOut(screen.getByTestId("pane-workspace"), { relatedTarget: document.body });
    expect(activePaneKey()).toBeUndefined();
    expect(layout.paneRender()?.activeInputTabKey).toBeNull();
  });

  it("keeps the focused leaf active when its selected tab changes", () => {
    const layout = store(splitTree());
    render(DetailPaneLayoutTestHarness, { layout, routeTabKey: "conversation" });
    const filesTab = screen.getByRole("tab", { name: "Files" });
    const detailLeaf = filesTab.closest(".tabbed-panel-leaf");

    fireEvent.focusIn(filesTab);
    expect(detailLeaf?.classList.contains("input-active")).toBe(true);

    fireEvent.click(filesTab);
    expect(filesTab.getAttribute("aria-selected")).toBe("true");
    expect(detailLeaf?.classList.contains("input-active")).toBe(true);
    expect(layout.paneRender()?.activeInputTabKey).toBe("files");
  });

  it("reclaims focus when same-leaf tab replacement unmounts the focused body", async () => {
    const layout = store(mergedTree());
    render(DetailPaneLayoutTestHarness, { layout });
    const conversationFocusTarget = screen.getByTestId("pane-focus-target-conversation");

    conversationFocusTarget.focus();
    fireEvent.focusIn(conversationFocusTarget);
    expect(layout.paneRender()?.activeInputTabKey).toBe("conversation");

    flushSync(() => layout.activateTab("files"));

    await vi.waitFor(() => {
      expect(document.activeElement?.classList.contains("detail-pane-layout")).toBe(true);
      expect(layout.paneRender()?.activeInputTabKey).toBeNull();
    });
  });

  it("keeps a zoom on a non-route leaf when the tab list re-derives", async () => {
    // Route authority is a transition, not an invariant. The deep-link effect
    // also tracks `tabs`, whose identity changes on unrelated store state — the
    // zoom itself re-derives the pane-render report — and re-asserting the route
    // there cleared every Expand Terminal / Maximize the moment it was made.
    const layout = store(splitTree());
    const { rerender } = render(DetailPaneLayoutTestHarness, {
      layout,
      routeTabKey: "conversation",
      tabsNonce: 0,
    });

    fireEvent.click(screen.getAllByTestId("pane-toggle-zoom")[1]!);
    expect(layout.zoomedLeafID()).toBe("leaf-workspace");

    await rerender({ layout, routeTabKey: "conversation", tabsNonce: 1 });

    expect(layout.zoomedLeafID()).toBe("leaf-workspace");
    expect(screen.getByTestId("pane-workspace").getAttribute("data-visible")).toBe("true");
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

  it("reports the first focus in a pane the layout already recorded", async () => {
    // Tab activation, a deep link, and a fresh promotion all write
    // `lastFocusedTabKey` without any pane being focused. Deduping against that
    // value swallowed the first real focus, and the workspace host - which keeps
    // its own per-workspace record of the pane the user works in - has no other
    // source for the event.
    const layout = store(splitTree());
    layout.noteFocused("workspace");
    const onFocusPane = vi.fn();
    render(DetailPaneLayoutTestHarness, { layout, onFocusPane });

    fireEvent.focusIn(screen.getByTestId("pane-workspace"));

    expect(onFocusPane).toHaveBeenCalledWith("workspace");
  });

  it("reports focus after an external route transition changes the owner", async () => {
    const layout = store(splitTree());
    const onFocusPane = vi.fn();
    const { rerender } = render(DetailPaneLayoutTestHarness, {
      layout,
      routeTabKey: "conversation",
      onFocusPane,
    });

    fireEvent.focusIn(screen.getByTestId("pane-workspace"));
    expect(onFocusPane).toHaveBeenLastCalledWith("workspace");

    await rerender({ layout, routeTabKey: "files", onFocusPane });
    expect(layout.lastFocusedTabKey()).toBe("files");
    onFocusPane.mockClear();

    fireEvent.focusIn(screen.getByTestId("pane-workspace"));
    expect(onFocusPane).toHaveBeenCalledWith("workspace");
    expect(layout.lastFocusedTabKey()).toBe("workspace");
  });

  it("records the focused pane for a surface that wants no notification", () => {
    // Issues has no per-pane route and passes no callback. The pane keyboard
    // commands act on the last focused pane, so not recording it there would aim
    // them at the route's pane instead of the one the user is working in.
    const layout = store(splitTree());
    render(DetailPaneLayoutTestHarness, { layout });

    fireEvent.focusIn(screen.getByTestId("pane-workspace"));

    expect(layout.lastFocusedTabKey()).toBe("workspace");
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

describe("promoting a session pane by drop", () => {
  const AGENT_PANE = sessionPaneKey("ws-1", undefined, "ws-1:helper");

  afterEach(() => clearActiveTabbedPanelDrag());

  it("adds a dropped session pane to the leaf it landed on", async () => {
    const layout = promotableStore(splitTree());
    render(DetailPaneLayoutTestHarness, { layout });
    const dataTransfer = startForeignTabDrag(layout.dragScope, AGENT_PANE);

    // Onto the middle of the body, not a strip: a leaf holding only the workspace
    // draws no strip of its own, so the body's centre is the append target and its
    // edges are the splits.
    const body = screen.getByTestId("pane-workspace").closest(".tabbed-panel-body")!;
    fireDragEvent(body, "dragover", { dataTransfer, clientX: 800, clientY: 400 });
    fireDragEvent(body, "drop", { dataTransfer, clientX: 800, clientY: 400 });

    // The surface's own mutations all refuse a source they do not already hold,
    // so without a promotion path this drop is silently inert.
    expect(layout.hasTab(AGENT_PANE)).toBe(true);
    expect(layout.leafIDForTab(AGENT_PANE)).toBe("leaf-workspace");
  });

  it("splits a session pane off the leaf edge it was dropped on", async () => {
    const layout = promotableStore(splitTree());
    render(DetailPaneLayoutTestHarness, { layout });
    const dataTransfer = startForeignTabDrag(layout.dragScope, AGENT_PANE);

    // mockWidth makes every rect 1600x800 from the origin, so a drop near x=0
    // lands on the left edge: a split, not a tab.
    const body = screen.getByTestId("pane-workspace").closest(".tabbed-panel-body")!;
    fireDragEvent(body, "dragover", { dataTransfer, clientX: 2, clientY: 400 });
    fireDragEvent(body, "drop", { dataTransfer, clientX: 2, clientY: 400 });

    expect(layout.hasTab(AGENT_PANE)).toBe(true);
    expect(layout.leafIDForTab(AGENT_PANE)).not.toBe("leaf-workspace");
  });

  it("refuses a dropped key this surface would prune", async () => {
    const layout = promotableStore(splitTree());
    render(DetailPaneLayoutTestHarness, { layout });
    const dataTransfer = startForeignTabDrag(layout.dragScope, "session:bogus");

    const body = screen.getByTestId("pane-workspace").closest(".tabbed-panel-body")!;
    fireDragEvent(body, "dragover", { dataTransfer, clientX: 800, clientY: 400 });
    fireDragEvent(body, "drop", { dataTransfer, clientX: 800, clientY: 400 });

    // A malformed key would be pruned on the next load, so accepting it writes a
    // pane that silently disappears.
    expect(layout.hasTab("session:bogus")).toBe(false);
  });
});

describe("drag state after a drop", () => {
  afterEach(() => clearActiveTabbedPanelDrag());

  it("clears the source strip's drag state when the drop moves the tab away", async () => {
    // The dragged tab's own dragend is what clears this, and a drop into another
    // leaf destroys that element before it fires - so the strip the drag started in
    // kept rendering the gap and the dragging styling it left behind, a shadow of a
    // tab that is now somewhere else.
    const layout = store(splitTree());
    render(DetailPaneLayoutTestHarness, { layout, workspaceAvailable: true });

    const source = screen.getByRole("tab", { name: "Conversation" });
    const sourceStrip = source.closest('[role="tablist"]')!;
    const dataTransfer = fakeDataTransfer();
    await fireEvent.dragStart(source, { dataTransfer });
    expect(sourceStrip.className).toContain("drag-sorting");

    // Onto the centre of the workspace pane's body: that leaf holds the workspace
    // alone, so it draws no strip and its body is the whole drop surface.
    const target = screen.getByTestId("pane-workspace").closest(".tabbed-panel-body")!;
    fireDragEvent(target, "dragover", { dataTransfer, clientX: 800, clientY: 400 });
    fireDragEvent(target, "drop", { dataTransfer, clientX: 800, clientY: 400 });

    expect(layout.leafIDForTab("conversation")).toBe("leaf-workspace");
    const remainingStrip = screen.getByRole("tab", { name: "Files" }).closest('[role="tablist"]')!;
    expect(remainingStrip.className).not.toContain("drag-sorting");
    expect(document.querySelectorAll('[data-testid="tabbed-panel-tab-drop-placeholder"]')).toHaveLength(0);
  });

  it("hides a leaf's drop preview when the drag ends elsewhere", async () => {
    // A dragover bubbles through nested trees (the workflow tree lives inside a
    // detail leaf), so this leaf can be previewing a drag whose drop the inner
    // tree consumes. That drop clears the shared payload, so this leaf's own drop
    // handler reads null and returns - only the end-of-drag broadcast can tell it
    // the preview is stale.
    const layout = store(splitTree());
    render(DetailPaneLayoutTestHarness, { layout });

    const source = screen.getByRole("tab", { name: "Conversation" });
    const dataTransfer = fakeDataTransfer();
    await fireEvent.dragStart(source, { dataTransfer });

    const body = screen.getByTestId("pane-workspace").closest(".tabbed-panel-body")!;
    // Near the left edge, so the split preview (not a centre append) is active.
    fireDragEvent(body, "dragover", { dataTransfer, clientX: 10, clientY: 400 });
    expect(body.className).toContain("show-drop-targets");

    clearActiveTabbedPanelDrag();
    flushSync();

    expect(body.className).not.toContain("show-drop-targets");
    expect(document.querySelectorAll(".tabbed-panel-split-preview.active")).toHaveLength(0);
  });
});
