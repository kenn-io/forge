import {
  activateTabbedPanelTab,
  appendTabbedPanelTabToLeaf,
  collectTabbedPanelLeafIDs,
  findTabbedPanelLeafByID,
  findTabbedPanelLeafByTab,
  moveTabbedPanelTabBefore,
  parseTabbedPanelLayout,
  pruneTabbedPanelTreeToAvailable,
  serializeTabbedPanelLayout,
  splitTabbedPanelTabIntoLeaf,
  updateTabbedPanelSplitRatio,
  type TabbedPanelDirection,
  type TabbedPanelLayoutState,
  type TabbedPanelNode,
} from "../components/shared/tabbed-panel-layout.js";
import { getStackDepth } from "./keyboard/modal-stack.svelte.js";
import { PANE_SURFACES } from "./pane-surfaces.js";

export type PaneSurfaceKey = "prs" | "issues" | "activity";

/** One entry per pane a surface can show. Lives here, not in the component, so it is re-exportable. */
export interface PaneTabSpec {
  key: string;
  label: string;
  /** False prunes the pane from the rendered tree while keeping its stored place. */
  available: boolean;
  /** Whether the user can hide this pane (the inline workspace's close action). */
  hideable?: boolean | undefined;
}

export const PANE_LAYOUT_STORAGE_PREFIX = "middleman-pane-layout-v1:";

export interface PaneLayoutStore {
  readonly surface: PaneSurfaceKey;
  /**
   * Namespaced so a detail pane can never be dropped into the Workspaces tree.
   * Scope comparison is plain string equality and the Workspaces tab uses raw
   * workspace ids, so a bare surface key could collide.
   */
  readonly dragScope: string;
  /** The tree to render: stored tree minus unavailable and hidden tabs. */
  renderTree(availableTabs: readonly string[]): TabbedPanelNode | null;
  /** Stored zoom, which may name a leaf that does not currently render. */
  zoomedLeafID(): string | null;
  /** Zoom filtered to leaves that actually render — what the view should use. */
  effectiveZoomedLeafID(availableTabs: readonly string[]): string | null;
  hiddenTabKeys(): readonly string[];
  lastFocusedTabKey(): string | null;
  leafIDForTab(tabKey: string): string | null;
  activateTab(tabKey: string): void;
  noteFocused(tabKey: string): void;
  moveTabBefore(source: string, target: string): void;
  appendTabToLeaf(source: string, leafID: string): void;
  splitTab(source: string, leafID: string, direction: TabbedPanelDirection, placement: "before" | "after"): void;
  setRatio(splitID: string, ratio: number): void;
  toggleZoom(leafID: string): void;
  clearZoom(): void;
  setHidden(tabKey: string, hidden: boolean): void;
  reset(): void;
}

function storageKey(surface: PaneSurfaceKey): string {
  return `${PANE_LAYOUT_STORAGE_PREFIX}${surface}`;
}

function readStored(surface: PaneSurfaceKey): string | null {
  try {
    return localStorage.getItem(storageKey(surface));
  } catch {
    // Storage blocked (private mode, embedded host).
    return null;
  }
}

function writeStored(surface: PaneSurfaceKey, state: TabbedPanelLayoutState): void {
  try {
    localStorage.setItem(storageKey(surface), serializeTabbedPanelLayout(state));
  } catch {
    // Storage blocked; the layout just won't survive a reload.
  }
}

export function createPaneLayoutStore(
  surface: PaneSurfaceKey,
  knownTabs: readonly string[],
  defaultTree: TabbedPanelNode,
): PaneLayoutStore {
  let state = $state<TabbedPanelLayoutState>(parseTabbedPanelLayout(readStored(surface), knownTabs, defaultTree));

  function commit(next: TabbedPanelLayoutState): void {
    state = next;
    writeStored(surface, next);
  }

  function withTree(tree: TabbedPanelNode | null): void {
    if (!tree) return;
    commit({ ...state, tree });
  }

  function visibleTabs(availableTabs: readonly string[]): string[] {
    return availableTabs.filter((tab) => !state.hiddenTabKeys.includes(tab));
  }

  const store: PaneLayoutStore = {
    surface,
    dragScope: `detail:${surface}`,

    renderTree: (availableTabs) => pruneTabbedPanelTreeToAvailable(state.tree, visibleTabs(availableTabs)),

    zoomedLeafID: () => state.zoomedLeafID,

    effectiveZoomedLeafID: (availableTabs) => {
      // A zoom must not outlive what was zoomed. When the zoomed leaf stops
      // rendering — its only tab became unavailable or hidden — reporting it
      // would hide every remaining pane and blank the surface.
      const zoomed = state.zoomedLeafID;
      if (zoomed === null) return null;
      const rendered = pruneTabbedPanelTreeToAvailable(state.tree, visibleTabs(availableTabs));
      return rendered && collectTabbedPanelLeafIDs(rendered).includes(zoomed) ? zoomed : null;
    },

    hiddenTabKeys: () => state.hiddenTabKeys,
    lastFocusedTabKey: () => state.lastFocusedTabKey,

    leafIDForTab: (tabKey) => findTabbedPanelLeafByTab(state.tree, tabKey)?.id ?? null,

    activateTab: (tabKey) => withTree(activateTabbedPanelTab(state.tree, tabKey)),

    noteFocused: (tabKey) => {
      if (!knownTabs.includes(tabKey)) return;
      if (state.lastFocusedTabKey === tabKey) return;
      commit({ ...state, lastFocusedTabKey: tabKey });
    },

    moveTabBefore: (source, target) => withTree(moveTabbedPanelTabBefore(state.tree, source, target)),

    appendTabToLeaf: (source, leafID) => withTree(appendTabbedPanelTabToLeaf(state.tree, source, leafID)),

    splitTab: (source, leafID, direction, placement) => {
      const tree = splitTabbedPanelTabIntoLeaf(state.tree, source, leafID, direction, placement);
      // Rejected splits hand back the same tree; nothing moved, so nothing can
      // be hiding behind the zoom.
      if (!tree || tree === state.tree) return;
      // A split always mints a new leaf, and any surviving zoom names an older
      // one — so the pane the user just split off would land hidden behind the
      // zoom. Arranging panes and maximizing one are mutually exclusive intents.
      commit({ ...state, tree, zoomedLeafID: null });
    },

    setRatio: (splitID, ratio) => withTree(updateTabbedPanelSplitRatio(state.tree, splitID, ratio)),

    toggleZoom: (leafID) => {
      // Zooming over an open dialog would bury it.
      if (getStackDepth() > 0) return;
      commit({ ...state, zoomedLeafID: state.zoomedLeafID === leafID ? null : leafID });
    },

    clearZoom: () => {
      if (state.zoomedLeafID === null) return;
      commit({ ...state, zoomedLeafID: null });
    },

    // Callers still need to reconcile a zoom that stops rendering for reasons the
    // store cannot see (a pane becoming unavailable): see
    // DetailPaneLayout's zoom reconciliation effect.
    setHidden: (tabKey, hidden) => {
      const already = state.hiddenTabKeys.includes(tabKey);
      if (already === hidden) return;
      const hiddenTabKeys = hidden
        ? [...state.hiddenTabKeys, tabKey]
        : state.hiddenTabKeys.filter((key) => key !== tabKey);
      // Hiding the last visible tab of the zoomed leaf must clear the zoom, not
      // just mask it. Closing a maximized pane would otherwise leave a stored
      // zoom naming a leaf with nothing in it, and any consumer reading the
      // stored value — the workspace dock-mode derivation reports "expanded" —
      // would disagree with what is on screen.
      const zoomedLeaf = state.zoomedLeafID ? findTabbedPanelLeafByID(state.tree, state.zoomedLeafID) : null;
      const zoomStillHasContent = zoomedLeaf === null || zoomedLeaf.tabs.some((key) => !hiddenTabKeys.includes(key));
      commit({
        ...state,
        hiddenTabKeys,
        zoomedLeafID: zoomStillHasContent ? state.zoomedLeafID : null,
      });
    },

    reset: () => commit(parseTabbedPanelLayout(null, knownTabs, defaultTree)),
  };

  return store;
}

const stores = new Map<PaneSurfaceKey, PaneLayoutStore>();

/**
 * Cached per surface: one layout per top-level mode, shared by every consumer.
 *
 * The surface key alone is the argument on purpose. The views and the
 * workspace-host store (which derives the inline dock mode from the layout) both
 * reach for the same store, and passing tabs or a default tree here would let
 * whichever called first define the surface for the other. `PANE_SURFACES` is
 * the single definition.
 */
export function getPaneLayoutStore(surface: PaneSurfaceKey): PaneLayoutStore {
  const cached = stores.get(surface);
  if (cached) return cached;
  const definition = PANE_SURFACES[surface];
  const created = createPaneLayoutStore(surface, definition.tabs, definition.defaultTree());
  stores.set(surface, created);
  return created;
}

export function resetPaneLayoutStoresForTest(): void {
  stores.clear();
}
