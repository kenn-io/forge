import {
  createTabbedPanelLeaf,
  splitTabbedPanelLeaf,
  type TabbedPanelNode,
} from "../components/shared/tabbed-panel-layout.js";
import type { PaneSurfaceKey } from "./paneLayout.svelte.js";
import { isSessionPaneKey } from "./session-pane-key.js";

/**
 * The pane vocabulary of each detail surface, in one place.
 *
 * Both the views and the workspace-host store need it - the views to render, the
 * host to derive the inline workspace's dock mode from the layout - and whichever
 * asked first would otherwise define the surface for everyone. Centralizing it
 * removes that ordering hazard.
 *
 * `tabs` is the surface's KNOWN set, not its currently available one: a stored
 * layout naming a tab outside this list is rejected as malformed, so listing a
 * pane here is what lets it persist at all. Availability is decided per render
 * by the view.
 */
export interface PaneSurfaceDefinition {
  tabs: readonly string[];
  /** Fresh tree per call: the tree helpers mint new node ids on every call. */
  defaultTree: () => TabbedPanelNode;
  /**
   * Dynamic panes this surface accepts outside `tabs`: kept when stored, never
   * reinserted. A promoted terminal session is the only kind today.
   */
  keepIfStored: (tabKey: string) => boolean;
}

/** Each group becomes one leaf, stacked top to bottom in the order given. */
function stackedTree(groups: readonly (readonly string[])[]): TabbedPanelNode {
  const leaves = groups.map((group) => createTabbedPanelLeaf(group, group[0]));
  let tree: TabbedPanelNode = leaves[0]!;
  let anchorID = leaves[0]!.id;
  for (const leaf of leaves.slice(1)) {
    tree = splitTabbedPanelLeaf(tree, anchorID, leaf, "vertical", "after");
    anchorID = leaf.id;
  }
  return tree;
}

/**
 * Defaults reproduce the layout each surface had before panes became
 * rearrangeable: the detail panes in one tab group with the inline workspace
 * docked below it.
 */
export const PANE_SURFACES: Record<PaneSurfaceKey, PaneSurfaceDefinition> = {
  prs: {
    tabs: ["conversation", "files", "workspace"],
    defaultTree: () => stackedTree([["conversation", "files"], ["workspace"]]),
    keepIfStored: isSessionPaneKey,
  },
  issues: {
    tabs: ["conversation", "workspace"],
    defaultTree: () => stackedTree([["conversation"], ["workspace"]]),
    keepIfStored: isSessionPaneKey,
  },
  activity: {
    tabs: ["conversation", "files", "commit", "workspace"],
    defaultTree: () => stackedTree([["conversation", "files", "commit"], ["workspace"]]),
    keepIfStored: isSessionPaneKey,
  },
};
