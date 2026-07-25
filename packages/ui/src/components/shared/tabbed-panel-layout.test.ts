import { describe, expect, it } from "vite-plus/test";
import {
  collectTabbedPanelLeafIDs,
  createTabbedPanelLeaf,
  defaultTabbedPanelLayout,
  parseTabbedPanelLayout,
  pruneTabbedPanelTreeToAvailable,
  serializeTabbedPanelLayout,
  splitTabbedPanelTabIntoLeaf,
  type TabbedPanelLayoutState,
} from "./tabbed-panel-layout";

const TABS = ["conversation", "files", "workspace"];

/** Default layout, then workspace split into its own leaf below the rest. */
function splitLayout(): { tree: ReturnType<typeof createTabbedPanelLeaf> | never; firstLeafID: string } {
  const base = defaultTabbedPanelLayout(TABS);
  const firstLeafID = collectTabbedPanelLeafIDs(base.tree)[0]!;
  const tree = splitTabbedPanelTabIntoLeaf(base.tree, "workspace", firstLeafID, "vertical", "after");
  if (!tree) throw new Error("split returned null");
  return { tree: tree as never, firstLeafID };
}

describe("tabbed panel layout persistence", () => {
  it("defaults to one leaf holding every known tab", () => {
    const state = defaultTabbedPanelLayout(TABS);
    expect(state.version).toBe(1);
    expect(state.tree.type).toBe("leaf");
    expect(collectTabbedPanelLeafIDs(state.tree)).toHaveLength(1);
    expect(state.zoomedLeafID).toBeNull();
    expect(state.collapsedLeafIDs).toEqual([]);
  });

  it("round-trips a split tree carrying zoom and collapse", () => {
    const { tree } = splitLayout();
    const leafIDs = collectTabbedPanelLeafIDs(tree);
    const state: TabbedPanelLayoutState = {
      version: 1,
      tree,
      zoomedLeafID: leafIDs[1]!,
      collapsedLeafIDs: [leafIDs[0]!],
    };
    expect(parseTabbedPanelLayout(serializeTabbedPanelLayout(state), TABS)).toEqual(state);
  });

  it("falls back to the default on null, empty, malformed, or wrong-version input", () => {
    for (const raw of [null, "", "{", "[]", "null", '{"version":2,"tree":null}']) {
      const parsed = parseTabbedPanelLayout(raw, TABS);
      expect(parsed.tree.type).toBe("leaf");
      expect(parsed.zoomedLeafID).toBeNull();
      expect(parsed.collapsedLeafIDs).toEqual([]);
    }
  });

  it("rejects a persisted tree repeating a tab key across leaves", () => {
    // One workspace tab means one portal slot. Two would register two slot
    // elements for a single surface.
    const raw = JSON.stringify({
      version: 1,
      tree: {
        type: "split",
        id: "s0",
        direction: "horizontal",
        ratio: 0.5,
        first: { type: "leaf", id: "l1", tabs: ["conversation", "workspace"], activeTabKey: "conversation" },
        second: { type: "leaf", id: "l2", tabs: ["workspace"], activeTabKey: "workspace" },
      },
      zoomedLeafID: null,
      collapsedLeafIDs: [],
    });
    expect(parseTabbedPanelLayout(raw, TABS).tree.type).toBe("leaf");
  });

  it("rejects a persisted tree repeating a node id", () => {
    // Edits are applied by node id; a duplicate would land in several nodes.
    const raw = JSON.stringify({
      version: 1,
      tree: {
        type: "split",
        id: "dup",
        direction: "horizontal",
        ratio: 0.5,
        first: { type: "leaf", id: "dup", tabs: ["conversation"], activeTabKey: "conversation" },
        second: { type: "leaf", id: "l2", tabs: ["files"], activeTabKey: "files" },
      },
      zoomedLeafID: null,
      collapsedLeafIDs: [],
    });
    expect(parseTabbedPanelLayout(raw, TABS).tree.type).toBe("leaf");
  });

  it("drops zoom and collapse entries naming a leaf that does not exist", () => {
    const base = defaultTabbedPanelLayout(TABS);
    const raw = JSON.stringify({ ...base, zoomedLeafID: "ghost", collapsedLeafIDs: ["ghost"] });
    const parsed = parseTabbedPanelLayout(raw, TABS);
    expect(parsed.zoomedLeafID).toBeNull();
    expect(parsed.collapsedLeafIDs).toEqual([]);
  });

  it("adds a newly introduced known tab to a previously persisted layout", () => {
    // A tab added to the surface after the layout was stored must appear,
    // otherwise a new pane would be unreachable forever.
    const stored = serializeTabbedPanelLayout(defaultTabbedPanelLayout(["conversation", "files"]));
    const parsed = parseTabbedPanelLayout(stored, TABS);
    expect(parsed.tree.type === "leaf" ? parsed.tree.tabs : []).toContain("workspace");
  });

  it("drops a persisted tab that is no longer known to the surface", () => {
    const stored = serializeTabbedPanelLayout(defaultTabbedPanelLayout([...TABS, "retired"]));
    const parsed = parseTabbedPanelLayout(stored, TABS);
    expect(parsed.tree.type === "leaf" ? parsed.tree.tabs : []).not.toContain("retired");
  });
});

describe("availability pruning", () => {
  it("removes unavailable tabs without reinserting them", () => {
    const { tree } = splitLayout();
    const pruned = pruneTabbedPanelTreeToAvailable(tree, ["conversation", "files"]);
    expect(pruned?.type).toBe("leaf");
    expect(pruned && pruned.type === "leaf" ? pruned.tabs : []).toEqual(["conversation", "files"]);
  });

  it("keeps the surviving leaf's id so intent-tree edits stay addressable", () => {
    const { tree, firstLeafID } = splitLayout();
    expect(pruneTabbedPanelTreeToAvailable(tree, ["conversation", "files"])?.id).toBe(firstLeafID);
  });

  it("returns null when nothing is available", () => {
    expect(pruneTabbedPanelTreeToAvailable(createTabbedPanelLeaf(["workspace"]), [])).toBeNull();
  });

  it("leaves a fully available tree untouched", () => {
    const { tree } = splitLayout();
    expect(pruneTabbedPanelTreeToAvailable(tree, TABS)).toEqual(tree);
  });
});
