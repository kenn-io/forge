import { describe, expect, it } from "vite-plus/test";
import {
  FLATTENED_TABBED_PANEL_LEAF_ID,
  collectTabbedPanelLeafIDs,
  createTabbedPanelLeaf,
  defaultTabbedPanelLayout,
  flattenTabbedPanelTree,
  parseTabbedPanelLayout,
  pruneTabbedPanelTreeToAvailable,
  serializeTabbedPanelLayout,
  collectTabbedPanelTabKeys,
  normalizeTabbedPanelTree,
  insertTabbedPanelTab,
  findTabbedPanelLeafByTab,
  splitTabbedPanelTabIntoLeaf,
  type TabbedPanelLayoutState,
} from "./tabbed-panel-layout";
import { isSessionPaneKey, sessionPaneKey } from "../../stores/session-pane-key.js";

const TABS = ["conversation", "files", "workspace"];
const AGENT_PANE = sessionPaneKey("ws-1", undefined, "ws-1:helper");

/** Default layout, then workspace split into its own leaf below the rest. */
function splitLayout(): { tree: ReturnType<typeof createTabbedPanelLeaf> | never; firstLeafID: string } {
  const base = defaultTabbedPanelLayout(TABS);
  const firstLeafID = collectTabbedPanelLeafIDs(base.tree)[0]!;
  const tree = splitTabbedPanelTabIntoLeaf(base.tree, "workspace", firstLeafID, "vertical", "after");
  if (!tree) throw new Error("split returned null");
  return { tree: tree as never, firstLeafID };
}

describe("dynamic panes kept but never reinserted", () => {
  function leafWith(tabs: string[]) {
    return { type: "leaf" as const, id: "leaf-1", tabs, activeTabKey: tabs[0]! };
  }

  it("keeps a stored session pane the surface vocabulary does not list", () => {
    const kept = normalizeTabbedPanelTree(
      leafWith(["conversation", AGENT_PANE]),
      ["conversation", "files"],
      "conversation",
      isSessionPaneKey,
    );
    expect(collectTabbedPanelTabKeys(kept)).toContain(AGENT_PANE);
    // Missing static tabs still appear; only the dynamic rule is different.
    expect(collectTabbedPanelTabKeys(kept)).toContain("files");
  });

  it("never reinserts one that is not stored", () => {
    // Reinsertion exists so a newly added static pane shows up for users with a
    // stored layout. A session pane exists only because the user promoted it,
    // so removing it has to stick.
    const still = normalizeTabbedPanelTree(
      leafWith(["conversation"]),
      ["conversation", "files"],
      "conversation",
      isSessionPaneKey,
    );
    expect(collectTabbedPanelTabKeys(still)).not.toContain(AGENT_PANE);
  });

  it("prunes a malformed session key instead of keeping it forever", () => {
    const kept = normalizeTabbedPanelTree(
      leafWith(["conversation", "session:bogus"]),
      ["conversation"],
      "conversation",
      isSessionPaneKey,
    );
    expect(collectTabbedPanelTabKeys(kept)).not.toContain("session:bogus");
  });

  it("prunes every unknown key when no predicate is given", () => {
    const kept = normalizeTabbedPanelTree(leafWith(["conversation", AGENT_PANE]), ["conversation"], "conversation");
    expect(collectTabbedPanelTabKeys(kept)).not.toContain(AGENT_PANE);
  });

  it("survives a serialize and parse round trip", () => {
    const state: TabbedPanelLayoutState = {
      version: 1,
      tree: leafWith(["conversation", AGENT_PANE]),
      zoomedLeafID: null,
      hiddenTabKeys: [],
      lastFocusedTabKey: AGENT_PANE,
    };
    const parsed = parseTabbedPanelLayout(serializeTabbedPanelLayout(state), TABS, undefined, isSessionPaneKey);
    expect(collectTabbedPanelTabKeys(parsed.tree)).toContain(AGENT_PANE);
    expect(parsed.lastFocusedTabKey).toBe(AGENT_PANE);
  });
});

describe("inserting a tab the tree has never held", () => {
  it("appends it to a leaf and makes it that leaf's active tab", () => {
    const { tree, firstLeafID } = splitLayout();
    const next = insertTabbedPanelTab(tree, AGENT_PANE, { kind: "tab", leafID: firstLeafID });

    const leaf = findTabbedPanelLeafByTab(next, AGENT_PANE);
    expect(leaf?.id).toBe(firstLeafID);
    expect(leaf?.activeTabKey).toBe(AGENT_PANE);
    // Insertion adds; it must not move or drop what was already there.
    expect(collectTabbedPanelTabKeys(next)).toEqual(expect.arrayContaining([...TABS, AGENT_PANE]));
  });

  it("mints a new leaf beside the target when splitting", () => {
    const { tree, firstLeafID } = splitLayout();
    const next = insertTabbedPanelTab(tree, AGENT_PANE, {
      kind: "split",
      leafID: firstLeafID,
      direction: "horizontal",
      placement: "before",
    });

    const leaf = findTabbedPanelLeafByTab(next, AGENT_PANE);
    expect(leaf?.id).not.toBe(firstLeafID);
    expect(leaf?.tabs).toEqual([AGENT_PANE]);
    expect(collectTabbedPanelLeafIDs(next)).toContain(firstLeafID);
  });

  it("hands back the same tree when the insert cannot apply", () => {
    const { tree, firstLeafID } = splitLayout();
    // Already held: inserting again would duplicate a pane, and two leaves
    // rendering one session would race for its terminal.
    const withPane = insertTabbedPanelTab(tree, AGENT_PANE, { kind: "tab", leafID: firstLeafID });
    expect(insertTabbedPanelTab(withPane, AGENT_PANE, { kind: "tab", leafID: firstLeafID })).toBe(withPane);
    // Unknown leaf: the caller named a target that is not on screen.
    expect(insertTabbedPanelTab(tree, AGENT_PANE, { kind: "tab", leafID: "no-such-leaf" })).toBe(tree);
    expect(
      insertTabbedPanelTab(tree, AGENT_PANE, {
        kind: "split",
        leafID: "no-such-leaf",
        direction: "vertical",
        placement: "after",
      }),
    ).toBe(tree);
  });
});

describe("tabbed panel layout persistence", () => {
  it("defaults to one leaf holding every known tab", () => {
    const state = defaultTabbedPanelLayout(TABS);
    expect(state.version).toBe(1);
    expect(state.tree.type).toBe("leaf");
    expect(collectTabbedPanelLeafIDs(state.tree)).toHaveLength(1);
    expect(state.zoomedLeafID).toBeNull();
    expect(state.hiddenTabKeys).toEqual([]);
    expect(state.lastFocusedTabKey).toBeNull();
  });

  it("accepts a caller-supplied default tree so a surface controls first-run layout", () => {
    // Surfaces want conversation and files sharing a leaf above the workspace,
    // not every tab in one strip.
    const { tree } = splitLayout();
    const state = defaultTabbedPanelLayout(TABS, tree);
    expect(state.tree).toEqual(tree);
    expect(parseTabbedPanelLayout(null, TABS, tree).tree).toEqual(tree);
  });

  it("round-trips a split tree carrying zoom, hidden tabs, and last focus", () => {
    const { tree } = splitLayout();
    const state: TabbedPanelLayoutState = {
      version: 1,
      tree,
      zoomedLeafID: collectTabbedPanelLeafIDs(tree)[1]!,
      hiddenTabKeys: ["workspace"],
      lastFocusedTabKey: "files",
    };
    expect(parseTabbedPanelLayout(serializeTabbedPanelLayout(state), TABS)).toEqual(state);
  });

  it("hides a tab without disturbing others sharing its leaf", () => {
    // The reason hiding is keyed by tab and not by leaf: closing a workspace
    // merged into the conversation's leaf must not take the conversation down.
    const merged = createTabbedPanelLeaf(TABS, "conversation", "leaf-merged");
    const state: TabbedPanelLayoutState = {
      version: 1,
      tree: merged,
      zoomedLeafID: null,
      hiddenTabKeys: ["workspace"],
      lastFocusedTabKey: null,
    };
    const parsed = parseTabbedPanelLayout(serializeTabbedPanelLayout(state), TABS);
    const visible = TABS.filter((tab) => !parsed.hiddenTabKeys.includes(tab));
    const rendered = pruneTabbedPanelTreeToAvailable(parsed.tree, visible);
    expect(rendered && rendered.type === "leaf" ? rendered.tabs : []).toEqual(["conversation", "files"]);
    expect(parsed.tree.type === "leaf" ? parsed.tree.tabs : []).toContain("workspace");
  });

  it("drops a hidden tab key or last-focus key that is not in the tree", () => {
    const base = defaultTabbedPanelLayout(TABS);
    const raw = JSON.stringify({ ...base, hiddenTabKeys: ["ghost"], lastFocusedTabKey: "ghost" });
    const parsed = parseTabbedPanelLayout(raw, TABS);
    expect(parsed.hiddenTabKeys).toEqual([]);
    expect(parsed.lastFocusedTabKey).toBeNull();
  });

  it("falls back to the default on null, empty, malformed, or wrong-version input", () => {
    for (const raw of [null, "", "{", "[]", "null", '{"version":2,"tree":null}']) {
      const parsed = parseTabbedPanelLayout(raw, TABS);
      expect(parsed.tree.type).toBe("leaf");
      expect(parsed.zoomedLeafID).toBeNull();
      expect(parsed.hiddenTabKeys).toEqual([]);
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
      hiddenTabKeys: [],
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
      hiddenTabKeys: [],
    });
    expect(parseTabbedPanelLayout(raw, TABS).tree.type).toBe("leaf");
  });

  it("drops a zoom naming a leaf that does not exist but keeps a valid one", () => {
    const { tree } = splitLayout();
    const realLeafID = collectTabbedPanelLeafIDs(tree)[1]!;
    const base = defaultTabbedPanelLayout(TABS, tree);
    expect(parseTabbedPanelLayout(JSON.stringify({ ...base, zoomedLeafID: "ghost" }), TABS).zoomedLeafID).toBeNull();
    expect(parseTabbedPanelLayout(JSON.stringify({ ...base, zoomedLeafID: realLeafID }), TABS).zoomedLeafID).toBe(
      realLeafID,
    );
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

describe("flatten fallback", () => {
  it("collects every tab in traversal order under one synthetic leaf", () => {
    const { tree } = splitLayout();
    const flat = flattenTabbedPanelTree(tree);
    expect(flat.type).toBe("leaf");
    expect(flat.id).toBe(FLATTENED_TABBED_PANEL_LEAF_ID);
    expect(flat.tabs).toEqual(["conversation", "files", "workspace"]);
  });

  it("honours a preferred active tab so flattening does not jump panes", () => {
    // "The active tab" is undefined for a tree: each leaf has its own, and the
    // default arrangement has a detail tab and the workspace active in
    // different leaves. The surface's last-focused tab breaks the tie.
    const { tree } = splitLayout();
    expect(flattenTabbedPanelTree(tree, "workspace").activeTabKey).toBe("workspace");
  });

  it("ignores a preferred tab that is absent and falls back to the first leaf", () => {
    const tree = createTabbedPanelLeaf(["conversation", "files"], "files");
    expect(flattenTabbedPanelTree(tree, "workspace").activeTabKey).toBe("files");
  });

  it("is idempotent so repeated narrow renders stay stable", () => {
    const { tree } = splitLayout();
    const once = flattenTabbedPanelTree(tree, "files");
    expect(flattenTabbedPanelTree(once, "files")).toEqual(once);
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
