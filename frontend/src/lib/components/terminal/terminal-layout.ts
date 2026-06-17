export type TerminalDock = "bottom" | "top";
export type SplitDirection = "horizontal" | "vertical";
export type SplitEdge = "top" | "right" | "bottom" | "left";
export type SessionRegion = "workflow" | "terminal";
export type WorkflowTabKey = "home" | "terminal" | `session:${string}`;

export interface PaneLeaf {
  type: "leaf";
  id: string;
  sessionKey: string;
}

export interface PaneSplit {
  type: "split";
  id: string;
  direction: SplitDirection;
  ratio: number;
  first: PaneNode;
  second: PaneNode;
}

export type PaneNode = PaneLeaf | PaneSplit;

export interface TerminalGroup {
  id: string;
  activeSessionKey: string | null;
  tree: PaneNode | null;
}

export interface WorkflowLeaf {
  type: "leaf";
  id: string;
  tabs: WorkflowTabKey[];
  activeTabKey: WorkflowTabKey;
}

export interface WorkflowSplit {
  type: "split";
  id: string;
  direction: SplitDirection;
  ratio: number;
  first: WorkflowNode;
  second: WorkflowNode;
}

export type WorkflowNode = WorkflowLeaf | WorkflowSplit;

export interface TerminalLayoutState {
  version: 1;
  open: boolean;
  dock: TerminalDock;
  height: number;
  activeSessionKey: string | null;
  tree: PaneNode | null;
  terminalGroups: TerminalGroup[];
  activeTerminalGroupID: string | null;
  sessionRegions: Record<string, SessionRegion>;
  workflowMode: "tabs" | "grid";
  workflowTree: WorkflowNode | null;
  activeWorkflowLeafID: string | null;
  recentWorkflowLeafIDs: string[];
  customSessionLabels: Record<string, string>;
}

export const DEFAULT_TERMINAL_HEIGHT = 300;
export const MIN_TERMINAL_HEIGHT = 160;
export const MAX_TERMINAL_HEIGHT = 560;
export const MAX_TERMINAL_LEAVES = 4;

const MIN_RATIO = 0.12;
const MAX_RATIO = 0.88;
const SPLIT_EDGE_THRESHOLD = 0.25;

export function defaultTerminalLayout(): TerminalLayoutState {
  return {
    version: 1,
    open: false,
    dock: "bottom",
    height: DEFAULT_TERMINAL_HEIGHT,
    activeSessionKey: null,
    tree: null,
    terminalGroups: [],
    activeTerminalGroupID: null,
    sessionRegions: {},
    workflowMode: "tabs",
    workflowTree: createWorkflowLeaf(["home"]),
    activeWorkflowLeafID: null,
    recentWorkflowLeafIDs: [],
    customSessionLabels: {},
  };
}

export function clampTerminalHeight(value: number): number {
  if (!Number.isFinite(value)) return DEFAULT_TERMINAL_HEIGHT;
  return Math.max(MIN_TERMINAL_HEIGHT, Math.min(MAX_TERMINAL_HEIGHT, Math.round(value)));
}

export function clampRatio(value: number): number {
  if (!Number.isFinite(value)) return 0.5;
  return Math.max(MIN_RATIO, Math.min(MAX_RATIO, value));
}

export function splitEdgeFromPoint(
  rect: Pick<DOMRectReadOnly, "left" | "top" | "width" | "height">,
  clientX: number,
  clientY: number,
): SplitEdge | null {
  if (rect.width <= 0 || rect.height <= 0) return null;
  const x = (clientX - rect.left) / rect.width;
  const y = (clientY - rect.top) / rect.height;
  const distances: Array<{ edge: SplitEdge; distance: number }> = [
    { edge: "top", distance: y },
    { edge: "right", distance: 1 - x },
    { edge: "bottom", distance: 1 - y },
    { edge: "left", distance: x },
  ];
  distances.sort((a, b) => a.distance - b.distance);
  const nearest = distances[0];
  if (!nearest || nearest.distance > SPLIT_EDGE_THRESHOLD) return null;
  return nearest.edge;
}

export function splitPlacementForEdge(edge: SplitEdge): {
  direction: SplitDirection;
  placement: "before" | "after";
} {
  if (edge === "top") return { direction: "vertical", placement: "before" };
  if (edge === "bottom") return { direction: "vertical", placement: "after" };
  if (edge === "left") return { direction: "horizontal", placement: "before" };
  return { direction: "horizontal", placement: "after" };
}

export function createLeaf(sessionKey: string, id = newPaneID()): PaneLeaf {
  return { type: "leaf", id, sessionKey };
}

export function createTerminalGroup(sessionKey: string, id = newPaneID()): TerminalGroup {
  return {
    id,
    activeSessionKey: sessionKey,
    tree: createLeaf(sessionKey),
  };
}

export function createWorkflowLeaf(
  tabs: WorkflowTabKey[],
  activeTabKey = tabs[0] ?? "home",
  id = newPaneID(),
): WorkflowLeaf {
  const uniqueTabs = uniqueWorkflowTabs(tabs.length > 0 ? tabs : ["home"]);
  return {
    type: "leaf",
    id,
    tabs: uniqueTabs,
    activeTabKey: uniqueTabs.includes(activeTabKey) ? activeTabKey : uniqueTabs[0]!,
  };
}

export function splitPane(
  node: PaneNode | null,
  targetLeafID: string | null,
  sessionKey: string,
  direction: SplitDirection,
  id = newPaneID(),
): PaneNode {
  if (!node) return createLeaf(sessionKey, id);
  if (countLeaves(node) >= MAX_TERMINAL_LEAVES) return node;
  const replacement = (leaf: PaneLeaf): PaneSplit => ({
    type: "split",
    id,
    direction,
    ratio: 0.5,
    first: leaf,
    second: createLeaf(sessionKey),
  });
  const targetID = targetLeafID ?? firstLeaf(node)?.id;
  if (!targetID) return node;
  return replaceLeaf(node, targetID, replacement);
}

export function splitSessionIntoPane(
  node: PaneNode | null,
  targetLeafID: string,
  sessionKey: string,
  direction: SplitDirection,
  placement: "before" | "after",
): PaneNode {
  const sourceLeaf = findLeafBySession(node, sessionKey);
  if (sourceLeaf?.id === targetLeafID) {
    return node ?? createLeaf(sessionKey);
  }
  const withoutSource = closeSessionInTree(node, sessionKey) ?? node;
  if (!withoutSource) return createLeaf(sessionKey);
  return replaceLeaf(
    withoutSource,
    targetLeafID,
    (leaf): PaneSplit => ({
      type: "split",
      id: newPaneID(),
      direction,
      ratio: 0.5,
      first: placement === "before" ? createLeaf(sessionKey) : leaf,
      second: placement === "before" ? leaf : createLeaf(sessionKey),
    }),
  );
}

export function addSessionToTree(node: PaneNode | null, sessionKey: string): PaneNode {
  if (!node) return createLeaf(sessionKey);
  if (containsSession(node, sessionKey)) return node;
  return splitPane(node, firstLeaf(node)?.id ?? null, sessionKey, "horizontal");
}

export function closeSessionInTree(node: PaneNode | null, sessionKey: string): PaneNode | null {
  if (!node) return null;
  if (node.type === "leaf") {
    return node.sessionKey === sessionKey ? null : node;
  }
  const first = closeSessionInTree(node.first, sessionKey);
  const second = closeSessionInTree(node.second, sessionKey);
  if (!first) return second;
  if (!second) return first;
  return { ...node, first, second };
}

export function pruneTree(node: PaneNode | null, sessionKeys: readonly string[]): PaneNode | null {
  if (!node) return null;
  const allowed = new Set(sessionKeys);
  return pruneNode(node, allowed);
}

function pruneNode(node: PaneNode, allowed: ReadonlySet<string>): PaneNode | null {
  if (node.type === "leaf") {
    return allowed.has(node.sessionKey) ? node : null;
  }
  const first = pruneNode(node.first, allowed);
  const second = pruneNode(node.second, allowed);
  if (!first) return second;
  if (!second) return first;
  return {
    ...node,
    ratio: clampRatio(node.ratio),
    first,
    second,
  };
}

export function updateSplitRatio(node: PaneNode | null, splitID: string, ratio: number): PaneNode | null {
  if (!node) return null;
  if (node.type === "split" && node.id === splitID) {
    return { ...node, ratio: clampRatio(ratio) };
  }
  if (node.type === "leaf") return node;
  return {
    ...node,
    first: updateSplitRatio(node.first, splitID, ratio) ?? node.first,
    second: updateSplitRatio(node.second, splitID, ratio) ?? node.second,
  };
}

export function firstLeaf(node: PaneNode | null): PaneLeaf | null {
  if (!node) return null;
  if (node.type === "leaf") return node;
  return firstLeaf(node.first) ?? firstLeaf(node.second);
}

export function findLeafBySession(node: PaneNode | null, sessionKey: string): PaneLeaf | null {
  if (!node) return null;
  if (node.type === "leaf") {
    return node.sessionKey === sessionKey ? node : null;
  }
  return findLeafBySession(node.first, sessionKey) ?? findLeafBySession(node.second, sessionKey);
}

export function collectSessionKeys(node: PaneNode | null): string[] {
  if (!node) return [];
  if (node.type === "leaf") return [node.sessionKey];
  return [...collectSessionKeys(node.first), ...collectSessionKeys(node.second)];
}

export function containsSession(node: PaneNode | null, sessionKey: string): boolean {
  return findLeafBySession(node, sessionKey) !== null;
}

export function countLeaves(node: PaneNode | null): number {
  if (!node) return 0;
  if (node.type === "leaf") return 1;
  return countLeaves(node.first) + countLeaves(node.second);
}

export function activeTerminalGroup(layout: TerminalLayoutState): TerminalGroup | null {
  return (
    layout.terminalGroups.find((group) => group.id === layout.activeTerminalGroupID) ?? layout.terminalGroups[0] ?? null
  );
}

export function terminalGroupForSession(groups: readonly TerminalGroup[], sessionKey: string): TerminalGroup | null {
  return groups.find((group) => containsSession(group.tree, sessionKey)) ?? null;
}

export function addTerminalGroup(groups: readonly TerminalGroup[], sessionKey: string): TerminalGroup[] {
  if (terminalGroupForSession(groups, sessionKey)) return [...groups];
  return [...groups, createTerminalGroup(sessionKey)];
}

export function closeSessionInTerminalGroups(groups: readonly TerminalGroup[], sessionKey: string): TerminalGroup[] {
  return groups.flatMap((group) => {
    const tree = closeSessionInTree(group.tree, sessionKey);
    if (!tree) return [];
    return [
      {
        ...group,
        tree,
        activeSessionKey:
          group.activeSessionKey === sessionKey ? (firstLeaf(tree)?.sessionKey ?? null) : group.activeSessionKey,
      },
    ];
  });
}

export function updateTerminalGroupTree(
  groups: readonly TerminalGroup[],
  groupID: string,
  updater: (group: TerminalGroup) => TerminalGroup,
): TerminalGroup[] {
  return groups.map((group) => (group.id === groupID ? updater(group) : group));
}

export function collectWorkflowTabKeys(node: WorkflowNode | null): WorkflowTabKey[] {
  if (!node) return [];
  if (node.type === "leaf") return node.tabs;
  return [...collectWorkflowTabKeys(node.first), ...collectWorkflowTabKeys(node.second)];
}

export function firstWorkflowLeaf(node: WorkflowNode | null): WorkflowLeaf | null {
  if (!node) return null;
  if (node.type === "leaf") return node;
  return firstWorkflowLeaf(node.first) ?? firstWorkflowLeaf(node.second);
}

export function findWorkflowLeafByTab(node: WorkflowNode | null, tabKey: WorkflowTabKey): WorkflowLeaf | null {
  if (!node) return null;
  if (node.type === "leaf") {
    return node.tabs.includes(tabKey) ? node : null;
  }
  return findWorkflowLeafByTab(node.first, tabKey) ?? findWorkflowLeafByTab(node.second, tabKey);
}

export function activateWorkflowTab(node: WorkflowNode | null, tabKey: WorkflowTabKey): WorkflowNode | null {
  if (!node) return null;
  if (node.type === "leaf") {
    return node.tabs.includes(tabKey) ? { ...node, activeTabKey: tabKey } : node;
  }
  return {
    ...node,
    first: activateWorkflowTab(node.first, tabKey) ?? node.first,
    second: activateWorkflowTab(node.second, tabKey) ?? node.second,
  };
}

export function moveWorkflowTabBefore(
  node: WorkflowNode | null,
  sourceTabKey: WorkflowTabKey,
  targetTabKey: WorkflowTabKey,
): WorkflowNode | null {
  if (sourceTabKey === targetTabKey) return node;
  const removed = removeWorkflowTab(node, sourceTabKey);
  const targetTree = removed ?? node;
  return insertWorkflowTabBefore(targetTree, sourceTabKey, targetTabKey);
}

export function appendWorkflowTabToLeaf(
  node: WorkflowNode | null,
  sourceTabKey: WorkflowTabKey,
  leafID: string,
): WorkflowNode | null {
  const removed = removeWorkflowTab(node, sourceTabKey) ?? node;
  return insertWorkflowTabIntoLeaf(removed, sourceTabKey, leafID, "end");
}

export function splitWorkflowTabIntoLeaf(
  node: WorkflowNode | null,
  sourceTabKey: WorkflowTabKey,
  leafID: string,
  direction: SplitDirection,
  placement: "before" | "after",
): WorkflowNode | null {
  const sourceLeaf = findWorkflowLeafByTab(node, sourceTabKey);
  if (sourceLeaf?.id === leafID && sourceLeaf.tabs.length === 1) {
    return node;
  }
  const withoutSource = removeWorkflowTab(node, sourceTabKey) ?? node;
  if (!withoutSource) return createWorkflowLeaf([sourceTabKey]);
  return splitWorkflowLeaf(
    withoutSource,
    leafID,
    createWorkflowLeaf([sourceTabKey], sourceTabKey),
    direction,
    placement,
  );
}

export function updateWorkflowSplitRatio(
  node: WorkflowNode | null,
  splitID: string,
  ratio: number,
): WorkflowNode | null {
  if (!node) return null;
  if (node.type === "split" && node.id === splitID) {
    return { ...node, ratio: clampRatio(ratio) };
  }
  if (node.type === "leaf") return node;
  return {
    ...node,
    first: updateWorkflowSplitRatio(node.first, splitID, ratio) ?? node.first,
    second: updateWorkflowSplitRatio(node.second, splitID, ratio) ?? node.second,
  };
}

export function normalizeWorkflowTree(
  node: WorkflowNode | null,
  availableTabKeys: readonly WorkflowTabKey[],
): WorkflowNode {
  const available = uniqueWorkflowTabs(availableTabKeys.length > 0 ? [...availableTabKeys] : ["home"]);
  const validTabs = new Set<WorkflowTabKey>(available);
  let tree = pruneWorkflowNode(node, validTabs);
  if (!tree) {
    return createWorkflowLeaf(available);
  }
  const presentTabs = new Set(collectWorkflowTabKeys(tree));
  const missingTabs = available.filter((key) => !presentTabs.has(key));
  for (const tabKey of missingTabs) {
    tree = insertWorkflowTabIntoFirstLeaf(tree, tabKey);
  }
  return tree;
}

export function normalizeTerminalLayout(
  layout: TerminalLayoutState,
  sessionKeys: readonly string[],
): TerminalLayoutState {
  const validKeys = new Set(sessionKeys);
  const terminalGroups = normalizeTerminalGroups(layout, validKeys);
  const activeFromLayout =
    layout.activeSessionKey && validKeys.has(layout.activeSessionKey) ? layout.activeSessionKey : null;
  const activeGroupFromID = layout.activeTerminalGroupID
    ? terminalGroups.find((group) => group.id === layout.activeTerminalGroupID)
    : null;
  const activeGroupFromSession = activeFromLayout ? terminalGroupForSession(terminalGroups, activeFromLayout) : null;
  const selectedGroup = activeGroupFromID ?? activeGroupFromSession ?? terminalGroups[0] ?? null;
  const activeTerminalGroupID = selectedGroup?.id ?? null;
  const activeSessionKey =
    activeFromLayout && selectedGroup?.tree && containsSession(selectedGroup.tree, activeFromLayout)
      ? activeFromLayout
      : (selectedGroup?.activeSessionKey ?? firstLeaf(selectedGroup?.tree ?? null)?.sessionKey ?? null);
  const syncedTerminalGroups = terminalGroups.map((group) =>
    group.id === activeTerminalGroupID ? { ...group, activeSessionKey } : group,
  );
  const tree = syncedTerminalGroups.find((group) => group.id === activeTerminalGroupID)?.tree ?? null;
  const sessionRegions: Record<string, SessionRegion> = {};
  for (const key of Object.keys(layout.sessionRegions)) {
    if (validKeys.has(key)) {
      sessionRegions[key] = layout.sessionRegions[key] ?? "workflow";
    }
  }
  return {
    ...layout,
    height: clampTerminalHeight(layout.height),
    dock: layout.dock === "top" ? "top" : "bottom",
    activeSessionKey,
    tree,
    terminalGroups: syncedTerminalGroups,
    activeTerminalGroupID,
    sessionRegions,
    workflowMode: layout.workflowMode === "grid" ? "grid" : "tabs",
    workflowTree: layout.workflowTree,
    activeWorkflowLeafID:
      layout.activeWorkflowLeafID && workflowLeafIDs(layout.workflowTree).includes(layout.activeWorkflowLeafID)
        ? layout.activeWorkflowLeafID
        : (firstWorkflowLeaf(layout.workflowTree)?.id ?? null),
    recentWorkflowLeafIDs: layout.recentWorkflowLeafIDs.filter((id) =>
      workflowLeafIDs(layout.workflowTree).includes(id),
    ),
    customSessionLabels: filterRecordByKeys(layout.customSessionLabels, validKeys),
  };
}

export function parseTerminalLayout(raw: string | null): TerminalLayoutState {
  if (!raw) return defaultTerminalLayout();
  try {
    const parsed = recordFrom(JSON.parse(raw) as unknown);
    if (!parsed || parsed.version !== 1) return defaultTerminalLayout();
    const defaults = defaultTerminalLayout();
    return {
      version: 1,
      open: typeof parsed.open === "boolean" ? parsed.open : defaults.open,
      dock: parsed.dock === "top" ? "top" : "bottom",
      height: clampTerminalHeight(typeof parsed.height === "number" ? parsed.height : defaults.height),
      activeSessionKey: typeof parsed.activeSessionKey === "string" ? parsed.activeSessionKey : null,
      tree: parsePaneNode(parsed.tree),
      terminalGroups: parseTerminalGroups(parsed.terminalGroups),
      activeTerminalGroupID: typeof parsed.activeTerminalGroupID === "string" ? parsed.activeTerminalGroupID : null,
      sessionRegions: parseSessionRegions(parsed.sessionRegions),
      workflowMode: parsed.workflowMode === "grid" ? "grid" : "tabs",
      workflowTree: parseWorkflowNode(parsed.workflowTree),
      activeWorkflowLeafID: typeof parsed.activeWorkflowLeafID === "string" ? parsed.activeWorkflowLeafID : null,
      recentWorkflowLeafIDs: Array.isArray(parsed.recentWorkflowLeafIDs)
        ? parsed.recentWorkflowLeafIDs.filter((id): id is string => typeof id === "string")
        : [],
      customSessionLabels: parseCustomSessionLabels(parsed.customSessionLabels),
    };
  } catch {
    return defaultTerminalLayout();
  }
}

export function isWorkflowTabKey(value: string): value is WorkflowTabKey {
  return value === "home" || value === "terminal" || value.startsWith("session:");
}

function parsePaneNode(value: unknown): PaneNode | null {
  const node = recordFrom(value);
  if (!node || typeof node.id !== "string") return null;
  if (node.type === "leaf") {
    return typeof node.sessionKey === "string" ? { type: "leaf", id: node.id, sessionKey: node.sessionKey } : null;
  }
  if (node.type !== "split") return null;
  const first = parsePaneNode(node.first);
  const second = parsePaneNode(node.second);
  if (!first || !second) return null;
  return {
    type: "split",
    id: node.id,
    direction: node.direction === "vertical" ? "vertical" : "horizontal",
    ratio: clampRatio(typeof node.ratio === "number" ? node.ratio : 0.5),
    first,
    second,
  };
}

function parseTerminalGroups(value: unknown): TerminalGroup[] {
  if (!Array.isArray(value)) return [];
  return value.flatMap((item) => {
    const group = recordFrom(item);
    if (!group || typeof group.id !== "string") return [];
    return [
      {
        id: group.id,
        activeSessionKey: typeof group.activeSessionKey === "string" ? group.activeSessionKey : null,
        tree: parsePaneNode(group.tree),
      } satisfies TerminalGroup,
    ];
  });
}

function parseWorkflowNode(value: unknown): WorkflowNode | null {
  const node = recordFrom(value);
  if (!node || typeof node.id !== "string") return null;
  if (node.type === "leaf") {
    if (!Array.isArray(node.tabs)) return null;
    const tabs = node.tabs.filter((tab): tab is WorkflowTabKey => typeof tab === "string" && isWorkflowTabKey(tab));
    const activeTabKey =
      typeof node.activeTabKey === "string" && isWorkflowTabKey(node.activeTabKey)
        ? node.activeTabKey
        : (tabs[0] ?? "home");
    return createWorkflowLeaf(tabs, activeTabKey, node.id);
  }
  if (node.type !== "split") return null;
  const first = parseWorkflowNode(node.first);
  const second = parseWorkflowNode(node.second);
  if (!first || !second) return null;
  return {
    type: "split",
    id: node.id,
    direction: node.direction === "vertical" ? "vertical" : "horizontal",
    ratio: clampRatio(typeof node.ratio === "number" ? node.ratio : 0.5),
    first,
    second,
  };
}

function parseSessionRegions(value: unknown): Record<string, SessionRegion> {
  const regions = recordFrom(value);
  if (!regions) return {};
  const parsed: Record<string, SessionRegion> = {};
  for (const [key, region] of Object.entries(regions)) {
    if (region === "terminal" || region === "workflow") {
      parsed[key] = region;
    }
  }
  return parsed;
}

function recordFrom(value: unknown): Record<string, unknown> | null {
  return value !== null && typeof value === "object" && !Array.isArray(value)
    ? (value as Record<string, unknown>)
    : null;
}

function parseCustomSessionLabels(value: unknown): Record<string, string> {
  const labels = recordFrom(value);
  if (!labels) return {};
  const parsed: Record<string, string> = {};
  for (const [key, label] of Object.entries(labels)) {
    if (typeof label === "string" && label.trim() !== "") {
      parsed[key] = label;
    }
  }
  return parsed;
}

function filterRecordByKeys(value: Record<string, string>, validKeys: ReadonlySet<string>): Record<string, string> {
  const filtered: Record<string, string> = {};
  for (const [key, label] of Object.entries(value)) {
    if (validKeys.has(key) && label.trim() !== "") {
      filtered[key] = label;
    }
  }
  return filtered;
}

function normalizeTerminalGroups(layout: TerminalLayoutState, validKeys: ReadonlySet<string>): TerminalGroup[] {
  const sourceGroups = layout.terminalGroups.length > 0 ? layout.terminalGroups : legacyTerminalGroups(layout);
  return sourceGroups.flatMap((group) => {
    const tree = pruneTree(group.tree, [...validKeys]);
    if (!tree) return [];
    const activeSessionKey =
      group.activeSessionKey && validKeys.has(group.activeSessionKey)
        ? group.activeSessionKey
        : (firstLeaf(tree)?.sessionKey ?? null);
    return [{ ...group, tree, activeSessionKey }];
  });
}

function legacyTerminalGroups(layout: TerminalLayoutState): TerminalGroup[] {
  if (!layout.tree) return [];
  return [
    {
      id: "terminal-group-legacy",
      activeSessionKey: layout.activeSessionKey,
      tree: layout.tree,
    },
  ];
}

function uniqueWorkflowTabs(tabs: WorkflowTabKey[]): WorkflowTabKey[] {
  const seen = new Set<WorkflowTabKey>();
  const unique: WorkflowTabKey[] = [];
  for (const tab of tabs) {
    if (seen.has(tab)) continue;
    seen.add(tab);
    unique.push(tab);
  }
  return unique;
}

function workflowLeafIDs(node: WorkflowNode | null): string[] {
  if (!node) return [];
  if (node.type === "leaf") return [node.id];
  return [...workflowLeafIDs(node.first), ...workflowLeafIDs(node.second)];
}

function pruneWorkflowNode(node: WorkflowNode | null, validTabs: ReadonlySet<WorkflowTabKey>): WorkflowNode | null {
  if (!node) return null;
  if (node.type === "leaf") {
    const tabs = uniqueWorkflowTabs(node.tabs.filter((tab) => validTabs.has(tab)));
    if (tabs.length === 0) return null;
    return {
      ...node,
      tabs,
      activeTabKey: tabs.includes(node.activeTabKey) ? node.activeTabKey : tabs[0]!,
    };
  }
  const first = pruneWorkflowNode(node.first, validTabs);
  const second = pruneWorkflowNode(node.second, validTabs);
  if (!first) return second;
  if (!second) return first;
  return {
    ...node,
    ratio: clampRatio(node.ratio),
    first,
    second,
  };
}

function removeWorkflowTab(node: WorkflowNode | null, tabKey: WorkflowTabKey): WorkflowNode | null {
  if (!node) return null;
  if (node.type === "leaf") {
    if (!node.tabs.includes(tabKey)) return node;
    const tabs = node.tabs.filter((tab) => tab !== tabKey);
    if (tabs.length === 0) return null;
    return {
      ...node,
      tabs,
      activeTabKey: node.activeTabKey === tabKey ? tabs[0]! : node.activeTabKey,
    };
  }
  const first = removeWorkflowTab(node.first, tabKey);
  const second = removeWorkflowTab(node.second, tabKey);
  if (!first) return second;
  if (!second) return first;
  return { ...node, first, second };
}

function insertWorkflowTabBefore(
  node: WorkflowNode | null,
  sourceTabKey: WorkflowTabKey,
  targetTabKey: WorkflowTabKey,
): WorkflowNode | null {
  if (!node) return createWorkflowLeaf([sourceTabKey], sourceTabKey);
  if (node.type === "leaf") {
    const targetIndex = node.tabs.indexOf(targetTabKey);
    if (targetIndex < 0) return node;
    const tabs = [...node.tabs.slice(0, targetIndex), sourceTabKey, ...node.tabs.slice(targetIndex)];
    return {
      ...node,
      tabs: uniqueWorkflowTabs(tabs),
    };
  }
  return {
    ...node,
    first: insertWorkflowTabBefore(node.first, sourceTabKey, targetTabKey) ?? node.first,
    second: insertWorkflowTabBefore(node.second, sourceTabKey, targetTabKey) ?? node.second,
  };
}

function insertWorkflowTabIntoLeaf(
  node: WorkflowNode | null,
  tabKey: WorkflowTabKey,
  leafID: string,
  placement: "start" | "end",
): WorkflowNode | null {
  if (!node) return createWorkflowLeaf([tabKey], tabKey);
  if (node.type === "leaf") {
    if (node.id !== leafID) return node;
    const tabs = placement === "start" ? [tabKey, ...node.tabs] : [...node.tabs, tabKey];
    return {
      ...node,
      tabs: uniqueWorkflowTabs(tabs),
      activeTabKey: tabKey,
    };
  }
  return {
    ...node,
    first: insertWorkflowTabIntoLeaf(node.first, tabKey, leafID, placement) ?? node.first,
    second: insertWorkflowTabIntoLeaf(node.second, tabKey, leafID, placement) ?? node.second,
  };
}

function insertWorkflowTabIntoFirstLeaf(node: WorkflowNode, tabKey: WorkflowTabKey): WorkflowNode {
  if (node.type === "leaf") {
    return {
      ...node,
      tabs: uniqueWorkflowTabs([...node.tabs, tabKey]),
    };
  }
  return {
    ...node,
    first: insertWorkflowTabIntoFirstLeaf(node.first, tabKey),
  };
}

function splitWorkflowLeaf(
  node: WorkflowNode,
  leafID: string,
  newLeaf: WorkflowLeaf,
  direction: SplitDirection,
  placement: "before" | "after",
): WorkflowNode {
  if (node.type === "leaf") {
    if (node.id !== leafID) return node;
    return {
      type: "split",
      id: newPaneID(),
      direction,
      ratio: 0.5,
      first: placement === "before" ? newLeaf : node,
      second: placement === "before" ? node : newLeaf,
    };
  }
  return {
    ...node,
    first: splitWorkflowLeaf(node.first, leafID, newLeaf, direction, placement),
    second: splitWorkflowLeaf(node.second, leafID, newLeaf, direction, placement),
  };
}

function replaceLeaf(node: PaneNode, leafID: string, replacement: (leaf: PaneLeaf) => PaneNode): PaneNode {
  if (node.type === "leaf") {
    return node.id === leafID ? replacement(node) : node;
  }
  return {
    ...node,
    first: replaceLeaf(node.first, leafID, replacement),
    second: replaceLeaf(node.second, leafID, replacement),
  };
}

function newPaneID(): string {
  if (typeof crypto !== "undefined" && "randomUUID" in crypto) {
    return `pane-${crypto.randomUUID()}`;
  }
  return `pane-${Date.now().toString(36)}-${Math.random().toString(16).slice(2)}`;
}
