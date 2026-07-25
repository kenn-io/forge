import { beforeEach, describe, expect, it } from "vite-plus/test";
import type { TabbedPanelNode } from "../components/shared/tabbed-panel-layout.js";
import { pushModalFrame, resetModalStack } from "./keyboard/modal-stack.svelte.js";
import { PANE_LAYOUT_STORAGE_PREFIX, createPaneLayoutStore } from "./paneLayout.svelte.js";

const TABS = ["conversation", "files", "workspace"];

/**
 * conversation+files in one leaf, workspace split below — the PR default.
 *
 * Written literally with fixed ids rather than built through the tree helpers,
 * which mint fresh ids on every call and would make two "identical" default
 * trees compare unequal.
 */
function defaultTree(): TabbedPanelNode {
  return {
    type: "split",
    id: "split-root",
    direction: "vertical",
    ratio: 0.5,
    first: {
      type: "leaf",
      id: "leaf-detail",
      tabs: ["conversation", "files"],
      activeTabKey: "conversation",
    },
    second: { type: "leaf", id: "leaf-workspace", tabs: ["workspace"], activeTabKey: "workspace" },
  };
}

function store(surface: "prs" | "issues" | "activity" = "prs") {
  return createPaneLayoutStore(surface, TABS, defaultTree());
}

beforeEach(() => {
  localStorage.clear();
  resetModalStack();
});

describe("pane layout store", () => {
  it("namespaces its drag scope so detail panes cannot cross into the Workspaces tree", () => {
    // Scope comparison is plain string equality, so a bare surface key could
    // collide with a workspace id.
    expect(store("prs").dragScope).toBe("detail:prs");
    expect(store("activity").dragScope).toBe("detail:activity");
  });

  it("persists under a per-surface key so surfaces do not share a layout", () => {
    const prs = store("prs");
    prs.splitTab("workspace", "leaf-detail", "horizontal", "after");

    expect(localStorage.getItem(`${PANE_LAYOUT_STORAGE_PREFIX}prs`)).not.toBeNull();
    expect(localStorage.getItem(`${PANE_LAYOUT_STORAGE_PREFIX}issues`)).toBeNull();

    // A second surface reads its own (absent) key and gets the default.
    expect(store("issues").renderTree(TABS)).toEqual(defaultTree());
  });

  it("restores a persisted arrangement in a fresh store", () => {
    const first = store();
    const leafID = first.leafIDForTab("workspace")!;
    first.toggleZoom(leafID);

    expect(store().zoomedLeafID()).toBe(leafID);
  });

  it("falls back to the default tree when the stored value is malformed", () => {
    localStorage.setItem(`${PANE_LAYOUT_STORAGE_PREFIX}prs`, "{not json");
    expect(store().renderTree(TABS)).toEqual(defaultTree());
  });

  it("prunes unavailable tabs from the rendered tree but keeps them stored", () => {
    const s = store();
    const rendered = s.renderTree(["conversation", "files"]);

    expect(rendered?.type).toBe("leaf");
    // Still addressable in the intent tree, so it returns to its own leaf.
    expect(s.leafIDForTab("workspace")).not.toBeNull();
  });

  it("hides a tab without disturbing others sharing its leaf", () => {
    const s = store();
    s.appendTabToLeaf("workspace", "leaf-detail");
    s.setHidden("workspace", true);

    const rendered = s.renderTree(TABS);
    expect(rendered && rendered.type === "leaf" ? rendered.tabs : []).toEqual(["conversation", "files"]);
  });

  it("restores a hidden tab to the leaf it was hidden from", () => {
    const s = store();
    const leafID = s.leafIDForTab("workspace");
    s.setHidden("workspace", true);
    s.setHidden("workspace", false);

    expect(s.leafIDForTab("workspace")).toBe(leafID);
  });

  it("refuses to zoom while a modal frame is open", () => {
    // Zooming a pane over an open dialog would bury it.
    const s = store();
    pushModalFrame("test-dialog", []);
    s.toggleZoom(s.leafIDForTab("workspace")!);

    expect(s.zoomedLeafID()).toBeNull();
  });

  it("toggles zoom off when the same leaf is zoomed again", () => {
    const s = store();
    const leafID = s.leafIDForTab("workspace")!;
    s.toggleZoom(leafID);
    s.toggleZoom(leafID);

    expect(s.zoomedLeafID()).toBeNull();
  });

  it("clears a zoom naming a leaf that no longer renders", () => {
    // A zoom must not outlive what was zoomed: with the workspace unavailable
    // its leaf is gone, and a stale zoom would blank the surface.
    const s = store();
    s.toggleZoom(s.leafIDForTab("workspace")!);

    expect(s.renderTree(["conversation", "files"])).not.toBeNull();
    expect(s.effectiveZoomedLeafID(["conversation", "files"])).toBeNull();
  });

  it("drops the zoom when a tab is split out of the maximized leaf", () => {
    // A split always mints a new leaf while the zoom names an older one, so the
    // pane the user just split off would land hidden behind the zoom.
    const s = store();
    s.toggleZoom("leaf-detail");
    s.splitTab("files", "leaf-detail", "horizontal", "after");

    expect(s.zoomedLeafID()).toBeNull();
    expect(s.leafIDForTab("files")).not.toBe(s.leafIDForTab("conversation"));
  });

  it("keeps the zoom when a split is rejected", () => {
    const s = store();
    s.toggleZoom("leaf-detail");
    // Splitting the only tab out of its own leaf is a no-op, so nothing moved
    // and there is nothing for the zoom to be hiding.
    s.splitTab("workspace", "leaf-workspace", "horizontal", "after");

    expect(s.zoomedLeafID()).toBe("leaf-detail");
  });

  it("tracks the last focused tab across leaves", () => {
    const s = store();
    s.noteFocused("files");
    expect(s.lastFocusedTabKey()).toBe("files");

    s.noteFocused("workspace");
    expect(s.lastFocusedTabKey()).toBe("workspace");
  });

  it("ignores a focus note for a tab it does not know", () => {
    const s = store();
    s.noteFocused("nonsense");
    expect(s.lastFocusedTabKey()).toBeNull();
  });

  it("resets to the surface default", () => {
    const s = store();
    s.splitTab("files", "leaf-detail", "horizontal", "after");
    s.setHidden("workspace", true);
    s.reset();

    expect(s.renderTree(TABS)).toEqual(defaultTree());
    expect(s.zoomedLeafID()).toBeNull();
    expect(s.hiddenTabKeys()).toEqual([]);
  });

  it("applies a ratio change to the stored split", () => {
    const s = store();
    const tree = s.renderTree(TABS);
    const splitID = tree && tree.type === "split" ? tree.id : "";
    s.setRatio(splitID, 0.7);

    const after = store().renderTree(TABS);
    expect(after && after.type === "split" ? after.ratio : 0).toBeCloseTo(0.7);
  });
});
