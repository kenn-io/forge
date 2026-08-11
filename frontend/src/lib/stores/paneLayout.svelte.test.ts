import { beforeEach, describe, expect, it } from "vite-plus/test";
import type { TabbedPanelNode } from "../components/shared/tabbed-panel-layout.js";
import { pushModalFrame, resetModalStack } from "./keyboard/modal-stack.svelte.js";
import {
  PANE_LAYOUT_STORAGE_PREFIX,
  createPaneLayoutStore,
  promoteSessionBesideWorkspace,
} from "./paneLayout.svelte.js";
import { isSessionPaneKey, sessionPaneKey } from "./session-pane-key.js";

const TABS = ["conversation", "files", "workspace"];
const AGENT_PANE = sessionPaneKey("ws-1", undefined, "ws-1:helper");

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
  return createPaneLayoutStore(surface, TABS, defaultTree(), isSessionPaneKey);
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

  it("does not treat remembered focus as a live focus change", () => {
    const s = store();
    s.setExternalInputActive(true);
    expect(s.externalInputActive()).toBe(true);

    s.noteFocused("files");
    expect(s.externalInputActive()).toBe(true);

    s.reset();
    expect(s.externalInputActive()).toBe(false);
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

describe("promoted session panes", () => {
  it("reports whether the stored tree holds a tab", () => {
    const layout = store();
    expect(layout.hasTab("workspace")).toBe(true);
    // Availability must never conjure a promoted pane, so the views ask this
    // instead of assuming.
    expect(layout.hasTab(AGENT_PANE)).toBe(false);
  });

  it("keeps a promoted pane across a reload and still refuses to reinsert it", () => {
    const first = store();
    first.appendTabToLeaf("workspace", "leaf-detail");
    // Stand in for a promotion: put the session pane in the tree by hand, the
    // way Task 5's transfer will.
    localStorage.setItem(
      `${PANE_LAYOUT_STORAGE_PREFIX}prs`,
      JSON.stringify({
        version: 1,
        tree: {
          type: "leaf",
          id: "leaf-1",
          tabs: ["conversation", "files", "workspace", AGENT_PANE],
          activeTabKey: AGENT_PANE,
        },
        zoomedLeafID: null,
        hiddenTabKeys: [],
        lastFocusedTabKey: AGENT_PANE,
      }),
    );

    const reloaded = store();
    expect(reloaded.hasTab(AGENT_PANE)).toBe(true);
    expect(reloaded.lastFocusedTabKey()).toBe(AGENT_PANE);

    // Resetting drops it and nothing puts it back: a session pane exists only
    // because the user promoted it.
    reloaded.reset();
    expect(reloaded.hasTab(AGENT_PANE)).toBe(false);
  });

  it("promotes a session into a leaf, activates it, and persists it", () => {
    const layout = store();
    expect(layout.promoteTab(AGENT_PANE, { kind: "tab", leafID: "leaf-detail" })).toBe(true);

    expect(layout.hasTab(AGENT_PANE)).toBe(true);
    expect(layout.leafIDForTab(AGENT_PANE)).toBe("leaf-detail");
    // The user just dragged it here; landing behind a sibling would read as a
    // dropped drag.
    expect(layout.isTabActive(AGENT_PANE)).toBe(true);
    expect(localStorage.getItem(`${PANE_LAYOUT_STORAGE_PREFIX}prs`)).toContain(AGENT_PANE);
  });

  it("promotes a session as a split beside a leaf", () => {
    const layout = store();
    expect(
      layout.promoteTab(AGENT_PANE, {
        kind: "split",
        leafID: "leaf-detail",
        direction: "horizontal",
        placement: "after",
      }),
    ).toBe(true);

    expect(layout.leafIDForTab(AGENT_PANE)).not.toBe("leaf-detail");
    expect(layout.hasTab(AGENT_PANE)).toBe(true);
    // The pane it split off from is still there: promotion adds a pane, it does
    // not move one.
    expect(layout.hasTab("conversation")).toBe(true);
  });

  it("promotes beside the workspace pane when it is on screen", () => {
    const layout = store();
    const onScreen = {
      activeInputTabKey: "conversation",
      editableTabs: TABS,
      onScreenTabs: TABS,
      flattened: false,
      soloChromeTabs: [],
    };

    // Nothing rendered yet, so there is no evidence the split would land anywhere
    // the user can see it.
    expect(promoteSessionBesideWorkspace(layout, AGENT_PANE)).toBe(false);

    // Flattened: one strip for the whole surface, where structural edits are off.
    layout.notePaneRender({ ...onScreen, flattened: true });
    expect(promoteSessionBesideWorkspace(layout, AGENT_PANE)).toBe(false);
    expect(layout.hasTab(AGENT_PANE)).toBe(false);

    layout.notePaneRender(onScreen);
    expect(promoteSessionBesideWorkspace(layout, AGENT_PANE)).toBe(true);
    // Its own leaf, not a tab stacked behind the workspace pane, which would look
    // like the command did nothing.
    expect(layout.leafIDForTab(AGENT_PANE)).not.toBe("leaf-workspace");
  });

  it("promotes beside an on-screen session from the same workspace after the workspace pane retires", () => {
    const layout = store();
    const reviewerPane = sessionPaneKey("ws-1", undefined, "ws-1:reviewer");
    expect(
      layout.promoteTab(reviewerPane, {
        kind: "split",
        leafID: "leaf-detail",
        direction: "horizontal",
        placement: "after",
      }),
    ).toBe(true);
    layout.notePaneRender({
      activeInputTabKey: "conversation",
      editableTabs: ["conversation", reviewerPane],
      onScreenTabs: ["conversation", reviewerPane],
      flattened: false,
      soloChromeTabs: [],
    });

    expect(promoteSessionBesideWorkspace(layout, AGENT_PANE)).toBe(true);
    expect(layout.hasTab(AGENT_PANE)).toBe(true);
    expect(layout.leafIDForTab(AGENT_PANE)).not.toBe(layout.leafIDForTab(reviewerPane));
  });

  it("promotes beside a visible detail pane in a row-only layout", () => {
    const layout = store();
    layout.noteFocused("conversation");
    layout.notePaneRender({
      activeInputTabKey: "conversation",
      editableTabs: ["conversation", "files"],
      onScreenTabs: ["conversation", "files"],
      flattened: false,
      soloChromeTabs: [],
    });

    expect(promoteSessionBesideWorkspace(layout, AGENT_PANE)).toBe(true);
    expect(layout.hasTab(AGENT_PANE)).toBe(true);
    expect(layout.leafIDForTab(AGENT_PANE)).not.toBe("leaf-detail");
  });

  it("refuses to promote a key the surface would prune, or one already in the tree", () => {
    const layout = store();
    // A malformed session key would be pruned on the next load, so accepting it
    // here would write a pane that silently disappears.
    expect(layout.promoteTab("session:bogus", { kind: "tab", leafID: "leaf-detail" })).toBe(false);
    // A static pane is never absent from the tree, so promoting one is a bug in
    // the caller, not a layout the user asked for.
    expect(layout.promoteTab("conversation", { kind: "tab", leafID: "leaf-detail" })).toBe(false);
    expect(layout.promoteTab(AGENT_PANE, { kind: "tab", leafID: "no-such-leaf" })).toBe(false);
    expect(localStorage.getItem(`${PANE_LAYOUT_STORAGE_PREFIX}prs`)).toBeNull();

    expect(layout.promoteTab(AGENT_PANE, { kind: "tab", leafID: "leaf-detail" })).toBe(true);
    expect(layout.promoteTab(AGENT_PANE, { kind: "tab", leafID: "leaf-workspace" })).toBe(false);
    expect(layout.leafIDForTab(AGENT_PANE)).toBe("leaf-detail");
  });

  it("clears a zoom when promoting into a new leaf", () => {
    const layout = store();
    layout.toggleZoom("leaf-detail");
    layout.promoteTab(AGENT_PANE, {
      kind: "split",
      leafID: "leaf-detail",
      direction: "horizontal",
      placement: "after",
    });

    // The split mints a leaf the zoom cannot name, so keeping the zoom would
    // promote a pane straight into invisibility.
    expect(layout.zoomedLeafID()).toBeNull();
  });

  it("demotes a promoted pane and forgets everything that named it", () => {
    const layout = store();
    layout.promoteTab(AGENT_PANE, {
      kind: "split",
      leafID: "leaf-workspace",
      direction: "vertical",
      placement: "after",
    });
    const promotedLeaf = layout.leafIDForTab(AGENT_PANE);
    expect(promotedLeaf).not.toBeNull();
    layout.setHidden(AGENT_PANE, true);
    layout.toggleZoom(promotedLeaf!);

    layout.demoteTab(AGENT_PANE);

    expect(layout.hasTab(AGENT_PANE)).toBe(false);
    // A stale hidden entry would bring the session back hidden the next time it
    // is promoted, and a zoom naming the leaf it lived in alone would blank the
    // surface.
    expect(layout.hiddenTabKeys()).not.toContain(AGENT_PANE);
    expect(layout.zoomedLeafID()).toBeNull();
  });

  it("refuses to demote a static pane, which the surface always needs", () => {
    const layout = store();
    layout.demoteTab("conversation");

    // Nothing reinserts a static pane, so removing one would persist a layout the
    // surface cannot describe.
    expect(layout.hasTab("conversation")).toBe(true);
  });

  it("gives up the last-focused slot when the pane holding it is demoted", () => {
    const layout = store();
    layout.promoteTab(AGENT_PANE, { kind: "tab", leafID: "leaf-detail" });
    layout.noteFocused(AGENT_PANE);

    layout.demoteTab(AGENT_PANE);

    // A last-focused key naming a pane that is gone sends the pane commands, the
    // flattened strip, and the dock derivation to the wrong pane rather than none.
    expect(layout.lastFocusedTabKey()).toBeNull();
  });

  it("leaves the layout untouched when demoting a pane it does not hold", () => {
    const layout = store();
    layout.demoteTab(AGENT_PANE);
    expect(localStorage.getItem(`${PANE_LAYOUT_STORAGE_PREFIX}prs`)).toBeNull();
  });

  it("accepts a well-formed session pane as last-focused and refuses a malformed one", () => {
    // Without this a promoted pane could never win the last-focused slot, and
    // every rule keyed off it - the flattened strip, Focus Terminal, the dock
    // derivation - would read a stale value.
    const layout = store();
    layout.noteFocused(AGENT_PANE);
    expect(layout.lastFocusedTabKey()).toBe(AGENT_PANE);

    layout.noteFocused("session:bogus");
    expect(layout.lastFocusedTabKey()).toBe(AGENT_PANE);

    layout.noteFocused("not-a-pane-at-all");
    expect(layout.lastFocusedTabKey()).toBe(AGENT_PANE);
  });
});
