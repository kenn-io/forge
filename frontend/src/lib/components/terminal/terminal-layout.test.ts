import { describe, expect, it } from "vite-plus/test";
import {
  addSessionToTree,
  closeSessionInTree,
  collectSessionKeys,
  countLeaves,
  createLeaf,
  normalizeTerminalLayout,
  splitPane,
  splitEdgeFromPoint,
  splitPlacementForEdge,
  splitSessionIntoPane,
  updateSplitRatio,
  parseTerminalLayout,
} from "./terminal-layout";

describe("terminal layout tree", () => {
  it("splits leaves without encoding session structure in pane ids", () => {
    const root = createLeaf("ws-1_a", "leaf-a");
    const split = splitPane(root, "leaf-a", "ws-1_b", "horizontal", "split-a");

    expect(collectSessionKeys(split)).toEqual(["ws-1_a", "ws-1_b"]);
    expect(countLeaves(split)).toBe(2);
  });

  it("collapses split nodes when a session closes", () => {
    const root = addSessionToTree(createLeaf("ws-1_a", "leaf-a"), "ws-1_b");

    const closed = closeSessionInTree(root, "ws-1_a");

    expect(collectSessionKeys(closed)).toEqual(["ws-1_b"]);
  });

  it("clamps split ratios", () => {
    const root = splitPane(createLeaf("ws-1_a", "leaf-a"), "leaf-a", "ws-1_b", "vertical", "split-a");

    const updated = updateSplitRatio(root, "split-a", 0.98);

    expect(updated).toMatchObject({ type: "split", ratio: 0.88 });
  });

  it("moves an existing session into another pane as a split", () => {
    const root = splitPane(createLeaf("ws-1_a", "leaf-a"), "leaf-a", "ws-1_b", "horizontal", "split-a");

    const moved = splitSessionIntoPane(root, "leaf-a", "ws-1_b", "vertical", "after");

    expect(collectSessionKeys(moved)).toEqual(["ws-1_a", "ws-1_b"]);
    expect(countLeaves(moved)).toBe(2);
    expect(moved).toMatchObject({
      type: "split",
      direction: "vertical",
    });
  });

  it("maps pointer position to split edges only near pane edges", () => {
    const rect = { left: 10, top: 20, width: 200, height: 100 };

    expect(splitEdgeFromPoint(rect, 110, 22)).toBe("top");
    expect(splitEdgeFromPoint(rect, 207, 70)).toBe("right");
    expect(splitEdgeFromPoint(rect, 110, 118)).toBe("bottom");
    expect(splitEdgeFromPoint(rect, 12, 70)).toBe("left");
    expect(splitEdgeFromPoint(rect, 110, 70)).toBeNull();
    expect(splitEdgeFromPoint({ ...rect, width: 0 }, 12, 70)).toBeNull();
  });

  it("maps split preview edges to split placement", () => {
    expect(splitPlacementForEdge("top")).toEqual({
      direction: "vertical",
      placement: "before",
    });
    expect(splitPlacementForEdge("right")).toEqual({
      direction: "horizontal",
      placement: "after",
    });
    expect(splitPlacementForEdge("bottom")).toEqual({
      direction: "vertical",
      placement: "after",
    });
    expect(splitPlacementForEdge("left")).toEqual({
      direction: "horizontal",
      placement: "before",
    });
  });

  it("normalizes persisted layout against live sessions", () => {
    const root = addSessionToTree(createLeaf("old", "leaf-a"), "live");

    const normalized = normalizeTerminalLayout(
      {
        version: 1,
        open: true,
        dock: "bottom",
        height: 9999,
        activeSessionKey: "old",
        tree: root,
        terminalGroups: [],
        activeTerminalGroupID: null,
        sessionRegions: {
          old: "terminal",
          live: "terminal",
        },
        workflowMode: "grid",
        workflowTree: null,
        activeWorkflowLeafID: null,
        recentWorkflowLeafIDs: [],
        customSessionLabels: {},
      },
      ["live"],
    );

    expect(normalized.height).toBe(560);
    expect(normalized.activeSessionKey).toBe("live");
    expect(collectSessionKeys(normalized.tree)).toEqual(["live"]);
    expect(normalized.sessionRegions).toEqual({ live: "terminal" });
  });

  it("drops malformed persisted pane trees", () => {
    const parsed = parseTerminalLayout(
      JSON.stringify({
        version: 1,
        open: true,
        dock: "top",
        height: 280,
        activeSessionKey: "live",
        tree: {
          type: "split",
          id: "split-a",
          direction: "sideways",
          ratio: 2,
          first: { type: "leaf", id: "leaf-a", sessionKey: "live" },
          second: { type: "leaf", id: "leaf-b" },
        },
        sessionRegions: {
          live: "terminal",
          bad: "elsewhere",
        },
      }),
    );

    expect(parsed.open).toBe(true);
    expect(parsed.dock).toBe("top");
    expect(parsed.tree).toBeNull();
    expect(parsed.sessionRegions).toEqual({ live: "terminal" });
  });
});
