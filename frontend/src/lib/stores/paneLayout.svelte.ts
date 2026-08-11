import {
  activateTabbedPanelTab,
  appendTabbedPanelTabToLeaf,
  insertTabbedPanelTab,
  collectTabbedPanelLeafIDs,
  collectTabbedPanelTabKeys,
  findTabbedPanelLeafByID,
  findTabbedPanelLeafByTab,
  moveTabbedPanelTabBefore,
  parseTabbedPanelLayout,
  removeTabbedPanelTab,
  pruneTabbedPanelTreeToAvailable,
  serializeTabbedPanelLayout,
  splitTabbedPanelTabIntoLeaf,
  updateTabbedPanelSplitRatio,
  type TabbedPanelDirection,
  type TabbedPanelInsertTarget,
  type TabbedPanelLayoutState,
  type TabbedPanelNode,
} from "../components/shared/tabbed-panel-layout.js";
import { getStackDepth } from "./keyboard/modal-stack.svelte.js";
import { PANE_SURFACES } from "./pane-surfaces.js";
import { parseSessionPaneKey, sessionPaneKeyMatchesWorkspace } from "./session-pane-key.js";

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

export const PANE_LAYOUT_STORAGE_PREFIX = "kenn-forge-pane-layout-v1:";

/** The state only the renderer knows, published for consumers outside the tree. */
export interface PaneRenderReport {
  /** The rendered tab containing DOM focus, or null while focus is elsewhere. */
  activeInputTabKey: string | null;
  /**
   * Tabs a structural edit may target: present in the rendered tree and not
   * masked out of it.
   *
   * Not merely "available". A hidden pane is still available — that is what
   * makes it reopenable — but it renders nothing. Neither does a pane whose leaf
   * sits behind another leaf's zoom. In both cases a command that maximized or
   * split it would rearrange a tree the user cannot see.
   */
  editableTabs: readonly string[];
  /**
   * Tabs actually on screen: the active tab of every rendered leaf that no other
   * leaf's zoom is covering.
   *
   * Distinct from `editableTabs`, which includes tabs sitting behind a sibling
   * tab in the same leaf — reachable in one click, and a legitimate command
   * target, but not visible. Only this answers "are both of these on screen at
   * once", which is what the push-vs-replace history rule turns on.
   */
  onScreenTabs: readonly string[];
  /** True while the narrow-width fallback shows one flat strip and disables edits. */
  flattened: boolean;
  /**
   * Tabs rendered in a strip-less leaf (the leaf holds only this tab and it is a
   * solo-chrome key, so its strip is dropped and only the floating cluster
   * remains). The view inside such a pane is the only thing left that can name
   * its content — the workspace pane keeps its inner session strip exactly here.
   */
  soloChromeTabs: readonly string[];
}

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
  /**
   * Every tab the stored tree holds, in tree order.
   *
   * For callers that have to act on panes they cannot name in advance: a promoted
   * session's key carries the session name, so dropping a deleted workspace's panes
   * means reading back what is stored rather than reconstructing keys.
   */
  storedTabKeys(): readonly string[];
  lastFocusedTabKey(): string | null;
  leafIDForTab(tabKey: string): string | null;
  /** Whether this tab is the active one in its own leaf. */
  isTabActive(tabKey: string): boolean;
  /**
   * Whether the STORED tree contains this tab, regardless of availability.
   *
   * A promoted session pane is available only because it is already stored —
   * availability must never conjure one — so the views need to ask.
   */
  hasTab(tabKey: string): boolean;
  /**
   * What the mounted renderer is currently showing, or null when none is mounted.
   *
   * Command-layer consumers cannot derive this: whether a surface has a layout on
   * screen at all, which panes it offers, and whether the narrow-width fallback has
   * flattened it are all decided in the renderer. Ephemeral — never persisted.
   */
  paneRender(): PaneRenderReport | null;
  /** Called by the rendering host; null on teardown. */
  notePaneRender(report: PaneRenderReport | null): void;
  /** True while DOM focus is inside a surface dock outside the pane tree. */
  externalInputActive(): boolean;
  /** Report DOM focus entering or leaving a surface dock outside the pane tree. */
  setExternalInputActive(active: boolean): void;
  /**
   * Whether splitting this tab out would change anything — it shares its leaf.
   * Callers that offer a split control need this to avoid a dead affordance,
   * since the tree model answers a lone-tab split with the same tree.
   */
  canSplitTab(tabKey: string): boolean;
  activateTab(tabKey: string): void;
  noteFocused(tabKey: string): void;
  moveTabBefore(source: string, target: string): void;
  appendTabToLeaf(source: string, leafID: string): void;
  splitTab(source: string, leafID: string, direction: TabbedPanelDirection, placement: "before" | "after"): void;
  setRatio(splitID: string, ratio: number): void;
  toggleZoom(leafID: string): void;
  clearZoom(): void;
  setHidden(tabKey: string, hidden: boolean): void;
  /**
   * Add a dynamic pane the tree has never held, and report whether it landed.
   *
   * This tree is the ONLY record that the pane was promoted: nothing else is
   * written, and the container the session came from masks it out by asking
   * `hasTab`. So the one write is validated here — a key this surface would
   * prune on the next load is refused rather than written and silently lost.
   */
  promoteTab(tabKey: string, target: TabbedPanelInsertTarget): boolean;
  /** Remove a promoted pane, along with any zoom or hidden entry naming it. */
  demoteTab(tabKey: string): void;
  reset(): void;
}

function sameTabList(a: readonly string[], b: readonly string[]): boolean {
  return a.length === b.length && a.every((tab, index) => tab === b[index]);
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
  /** Dynamic panes kept when stored and never reinserted; see PaneSurfaceDefinition. */
  keepIfStored?: (tabKey: string) => boolean,
): PaneLayoutStore {
  let state = $state<TabbedPanelLayoutState>(
    parseTabbedPanelLayout(readStored(surface), knownTabs, defaultTree, keepIfStored),
  );
  let render = $state<PaneRenderReport | null>(null);
  let externalInputActive = $state(false);

  function commit(next: TabbedPanelLayoutState): void {
    state = next;
    writeStored(surface, next);
  }

  function withTree(tree: TabbedPanelNode | null): void {
    // A refused or no-op mutation hands back the same node. Committing it anyway
    // writes storage for nothing and, worse, publishes a new tree object that any
    // effect reading this layout treats as a change.
    if (!tree || tree === state.tree) return;
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
    storedTabKeys: () => collectTabbedPanelTabKeys(state.tree),
    lastFocusedTabKey: () => state.lastFocusedTabKey,

    leafIDForTab: (tabKey) => findTabbedPanelLeafByTab(state.tree, tabKey)?.id ?? null,

    isTabActive: (tabKey) => findTabbedPanelLeafByTab(state.tree, tabKey)?.activeTabKey === tabKey,

    hasTab: (tabKey) => findTabbedPanelLeafByTab(state.tree, tabKey) !== null,

    paneRender: () => render,

    notePaneRender: (report) => {
      if (report === null) {
        render = null;
        externalInputActive = false;
        return;
      }
      const current = render;
      if (
        current !== null &&
        current.activeInputTabKey === report.activeInputTabKey &&
        current.flattened === report.flattened &&
        sameTabList(current.editableTabs, report.editableTabs) &&
        sameTabList(current.onScreenTabs, report.onScreenTabs) &&
        sameTabList(current.soloChromeTabs, report.soloChromeTabs)
      ) {
        return;
      }
      render = {
        activeInputTabKey: report.activeInputTabKey,
        editableTabs: [...report.editableTabs],
        onScreenTabs: [...report.onScreenTabs],
        flattened: report.flattened,
        soloChromeTabs: [...report.soloChromeTabs],
      };
    },

    externalInputActive: () => externalInputActive,

    setExternalInputActive: (active) => {
      if (externalInputActive === active) return;
      externalInputActive = active;
    },

    canSplitTab: (tabKey) => (findTabbedPanelLeafByTab(state.tree, tabKey)?.tabs.length ?? 0) > 1,

    activateTab: (tabKey) => withTree(activateTabbedPanelTab(state.tree, tabKey)),

    noteFocused: (tabKey) => {
      // Dynamic panes count too, or a promoted session could never become
      // last-focused and every rule keyed off it would read a stale value. Still
      // validated: an arbitrary string must not be persisted as the winner.
      if (!knownTabs.includes(tabKey) && !(keepIfStored?.(tabKey) ?? false)) return;
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

    promoteTab: (tabKey, target) => {
      // Only dynamic panes. A static one is never absent from the tree, so
      // promoting it is a caller bug; an unrecognized key would be pruned on the
      // next load, making the promotion evaporate rather than fail.
      if (!(keepIfStored?.(tabKey) ?? false)) return false;
      const tree = insertTabbedPanelTab(state.tree, tabKey, target);
      if (!tree || tree === state.tree) return false;
      // A split mints a leaf no stored zoom can name, so the pane the user just
      // promoted would land behind the zoom. Same reasoning as splitTab.
      commit({
        ...state,
        tree,
        zoomedLeafID: target.kind === "split" ? null : state.zoomedLeafID,
        // Promoting is an explicit request to see it; a hidden entry left from a
        // previous life in this surface would swallow it.
        hiddenTabKeys: state.hiddenTabKeys.filter((key) => key !== tabKey),
      });
      return true;
    },

    demoteTab: (tabKey) => {
      // Dynamic panes only, like promoteTab. A static pane is part of the surface
      // and is never absent from the tree, so removing one would persist a layout
      // the surface cannot describe and no reinsertion would repair.
      if (!(keepIfStored?.(tabKey) ?? false)) return;
      const leaf = findTabbedPanelLeafByTab(state.tree, tabKey);
      if (leaf === null) return;
      const tree = removeTabbedPanelTab(state.tree, tabKey);
      if (!tree) return;
      // A zoom on the leaf the pane lived in alone now names a leaf that is gone,
      // which would blank the surface, and a hidden entry would bring the session
      // back hidden the next time it is promoted.
      const zoomedStillExists =
        state.zoomedLeafID === null || findTabbedPanelLeafByID(tree, state.zoomedLeafID) !== null;
      commit({
        ...state,
        tree,
        zoomedLeafID: zoomedStillExists ? state.zoomedLeafID : null,
        hiddenTabKeys: state.hiddenTabKeys.filter((key) => key !== tabKey),
        // A last-focused key naming a pane that is gone sends every rule keyed off
        // it - the pane commands' target, the flattened strip, the dock
        // derivation - to the wrong pane rather than to none.
        lastFocusedTabKey: state.lastFocusedTabKey === tabKey ? null : state.lastFocusedTabKey,
      });
    },

    reset: () => {
      externalInputActive = false;
      commit(parseTabbedPanelLayout(null, knownTabs, defaultTree, keepIfStored));
    },
  };

  return store;
}

/**
 * Promote a session into its own leaf beside visible workspace content.
 *
 * What every entry point that is not a drag means by "move to a pane": the point
 * of promoting is to see the session next to the work it belongs to, and a tab
 * stacked behind another pane looks like the command did nothing. Shared so the
 * palette command and the dock's own control cannot drift apart.
 *
 * The visibility rule is enforced HERE rather than left to each caller: holding a
 * leaf in the stored tree is not the same as being on screen, and a pane that is
 * closed, tabbed behind a sibling, or covered by another leaf's zoom would have
 * the split grow off screen while the view keeps publishing its sessions. Prefer
 * the workspace, then another visible session from the same workspace, then a
 * visible detail pane. Refuse only when none exists or structural edits are off.
 */
export function promoteSessionBesideWorkspace(layout: PaneLayoutStore, tabKey: string): boolean {
  const render = layout.paneRender();
  if (render === null || render.flattened) return false;

  const target = parseSessionPaneKey(tabKey);
  const sameWorkspaceSession =
    target === null
      ? undefined
      : render.onScreenTabs.find((candidate) =>
          sessionPaneKeyMatchesWorkspace(candidate, target.workspaceId, target.hostKey),
        );
  const focused = layout.lastFocusedTabKey();
  const focusedDetail =
    focused !== null && render.onScreenTabs.includes(focused) && parseSessionPaneKey(focused) === null
      ? focused
      : undefined;
  const firstDetail = render.onScreenTabs.find(
    (candidate) => candidate !== "workspace" && parseSessionPaneKey(candidate) === null,
  );
  const anchorTab = render.onScreenTabs.includes("workspace")
    ? "workspace"
    : (sameWorkspaceSession ?? focusedDetail ?? firstDetail);
  if (anchorTab === undefined) return false;

  const leafID = layout.leafIDForTab(anchorTab);
  if (leafID === null) return false;
  return layout.promoteTab(tabKey, { kind: "split", leafID, direction: "horizontal", placement: "after" });
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
  const created = createPaneLayoutStore(surface, definition.tabs, definition.defaultTree(), definition.keepIfStored);
  stores.set(surface, created);
  return created;
}

export function resetPaneLayoutStoresForTest(): void {
  stores.clear();
  // Also drop the persisted layouts: clearing only the in-memory cache leaves the
  // next store to re-read a previous test's arrangement out of localStorage, and
  // with the inline dock mode derived from the layout that leaks a zoom or a
  // hidden pane across tests under shuffled ordering.
  for (const surface of Object.keys(PANE_SURFACES) as PaneSurfaceKey[]) {
    try {
      localStorage.removeItem(storageKey(surface));
    } catch {
      // Storage blocked; nothing was persisted to clear.
    }
  }
}
