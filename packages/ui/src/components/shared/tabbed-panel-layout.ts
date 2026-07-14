import type { StatusDotStatus } from "@kenn-io/kit-ui";

export type TabbedPanelDirection = "horizontal" | "vertical";
export type TabbedPanelSplitEdge = "top" | "right" | "bottom" | "left";

export interface TabbedPanelStatus {
  value: StatusDotStatus;
  label: string;
}

export interface TabbedPanelDescriptor {
  key: string;
  label: string;
  status?: TabbedPanelStatus | undefined;
}

export interface TabbedPanelLeaf {
  type: "leaf";
  id: string;
  tabs: string[];
  activeTabKey: string;
}

export interface TabbedPanelSplit {
  type: "split";
  id: string;
  direction: TabbedPanelDirection;
  ratio: number;
  first: TabbedPanelNode;
  second: TabbedPanelNode;
}

export type TabbedPanelNode = TabbedPanelLeaf | TabbedPanelSplit;

const MIN_RATIO = 0.12;
const MAX_RATIO = 0.88;
const SPLIT_EDGE_THRESHOLD = 0.25;

export function clampTabbedPanelRatio(value: number): number {
  if (!Number.isFinite(value)) return 0.5;
  return Math.max(MIN_RATIO, Math.min(MAX_RATIO, value));
}

export function tabbedPanelSplitEdgeFromPoint(
  rect: Pick<DOMRectReadOnly, "left" | "top" | "width" | "height">,
  clientX: number,
  clientY: number,
): TabbedPanelSplitEdge | null {
  if (rect.width <= 0 || rect.height <= 0) return null;
  const x = (clientX - rect.left) / rect.width;
  const y = (clientY - rect.top) / rect.height;
  const distances: Array<{ edge: TabbedPanelSplitEdge; distance: number }> = [
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

export function tabbedPanelSplitPlacementForEdge(edge: TabbedPanelSplitEdge): {
  direction: TabbedPanelDirection;
  placement: "before" | "after";
} {
  if (edge === "top") return { direction: "vertical", placement: "before" };
  if (edge === "bottom") return { direction: "vertical", placement: "after" };
  if (edge === "left") return { direction: "horizontal", placement: "before" };
  return { direction: "horizontal", placement: "after" };
}

export function createTabbedPanelLeaf(
  tabs: readonly string[],
  activeTabKey = tabs[0] ?? "panel",
  id = newTabbedPanelID(),
): TabbedPanelLeaf {
  const uniqueTabs = uniqueTabbedPanelTabs(tabs.length > 0 ? [...tabs] : [activeTabKey]);
  return {
    type: "leaf",
    id,
    tabs: uniqueTabs,
    activeTabKey: uniqueTabs.includes(activeTabKey) ? activeTabKey : uniqueTabs[0]!,
  };
}

export function collectTabbedPanelTabKeys(node: TabbedPanelNode | null): string[] {
  if (!node) return [];
  if (node.type === "leaf") return node.tabs;
  return [...collectTabbedPanelTabKeys(node.first), ...collectTabbedPanelTabKeys(node.second)];
}

export function firstTabbedPanelLeaf(node: TabbedPanelNode | null): TabbedPanelLeaf | null {
  if (!node) return null;
  if (node.type === "leaf") return node;
  return firstTabbedPanelLeaf(node.first) ?? firstTabbedPanelLeaf(node.second);
}

export function findTabbedPanelLeafByTab(node: TabbedPanelNode | null, tabKey: string): TabbedPanelLeaf | null {
  if (!node) return null;
  if (node.type === "leaf") {
    return node.tabs.includes(tabKey) ? node : null;
  }
  return findTabbedPanelLeafByTab(node.first, tabKey) ?? findTabbedPanelLeafByTab(node.second, tabKey);
}

export function activateTabbedPanelTab(node: TabbedPanelNode | null, tabKey: string): TabbedPanelNode | null {
  if (!node) return null;
  if (node.type === "leaf") {
    return node.tabs.includes(tabKey) ? { ...node, activeTabKey: tabKey } : node;
  }
  return {
    ...node,
    first: activateTabbedPanelTab(node.first, tabKey) ?? node.first,
    second: activateTabbedPanelTab(node.second, tabKey) ?? node.second,
  };
}

export function moveTabbedPanelTabBefore(
  node: TabbedPanelNode | null,
  sourceTabKey: string,
  targetTabKey: string,
): TabbedPanelNode | null {
  if (sourceTabKey === targetTabKey) return node;
  if (!findTabbedPanelLeafByTab(node, sourceTabKey)) return node;
  if (!findTabbedPanelLeafByTab(node, targetTabKey)) return node;
  const removed = removeTabbedPanelTab(node, sourceTabKey);
  const targetTree = removed ?? node;
  return insertTabbedPanelTabBefore(targetTree, sourceTabKey, targetTabKey);
}

export function appendTabbedPanelTabToLeaf(
  node: TabbedPanelNode | null,
  sourceTabKey: string,
  leafID: string,
): TabbedPanelNode | null {
  if (!findTabbedPanelLeafByTab(node, sourceTabKey)) return node;
  if (!findTabbedPanelLeafByID(node, leafID)) return node;
  const removed = removeTabbedPanelTab(node, sourceTabKey) ?? node;
  return insertTabbedPanelTabIntoLeaf(removed, sourceTabKey, leafID, "end");
}

export function splitTabbedPanelTabIntoLeaf(
  node: TabbedPanelNode | null,
  sourceTabKey: string,
  leafID: string,
  direction: TabbedPanelDirection,
  placement: "before" | "after",
): TabbedPanelNode | null {
  const sourceLeaf = findTabbedPanelLeafByTab(node, sourceTabKey);
  if (!sourceLeaf) return node;
  if (!findTabbedPanelLeafByID(node, leafID)) return node;
  if (sourceLeaf?.id === leafID && sourceLeaf.tabs.length === 1) {
    return node;
  }
  const withoutSource = removeTabbedPanelTab(node, sourceTabKey) ?? node;
  if (!withoutSource) return createTabbedPanelLeaf([sourceTabKey], sourceTabKey);
  return splitTabbedPanelLeaf(
    withoutSource,
    leafID,
    createTabbedPanelLeaf([sourceTabKey], sourceTabKey),
    direction,
    placement,
  );
}

export function updateTabbedPanelSplitRatio(
  node: TabbedPanelNode | null,
  splitID: string,
  ratio: number,
): TabbedPanelNode | null {
  if (!node) return null;
  if (node.type === "split" && node.id === splitID) {
    return { ...node, ratio: clampTabbedPanelRatio(ratio) };
  }
  if (node.type === "leaf") return node;
  return {
    ...node,
    first: updateTabbedPanelSplitRatio(node.first, splitID, ratio) ?? node.first,
    second: updateTabbedPanelSplitRatio(node.second, splitID, ratio) ?? node.second,
  };
}

export function normalizeTabbedPanelTree(
  node: TabbedPanelNode | null,
  availableTabKeys: readonly string[],
  fallbackTabKey = availableTabKeys[0] ?? "panel",
): TabbedPanelNode {
  const available = uniqueTabbedPanelTabs(availableTabKeys.length > 0 ? [...availableTabKeys] : [fallbackTabKey]);
  const validTabs = new Set<string>(available);
  let tree = pruneTabbedPanelNode(node, validTabs);
  if (!tree) {
    return createTabbedPanelLeaf(available, available[0] ?? fallbackTabKey);
  }
  const presentTabs = new Set(collectTabbedPanelTabKeys(tree));
  const missingTabs = available.filter((key) => !presentTabs.has(key));
  for (const tabKey of missingTabs) {
    tree = insertTabbedPanelTabIntoFirstLeaf(tree, tabKey);
  }
  return tree;
}

function uniqueTabbedPanelTabs(tabs: string[]): string[] {
  const seen = new Set<string>();
  const unique: string[] = [];
  for (const tab of tabs) {
    if (seen.has(tab)) continue;
    seen.add(tab);
    unique.push(tab);
  }
  return unique;
}

function findTabbedPanelLeafByID(node: TabbedPanelNode | null, leafID: string): TabbedPanelLeaf | null {
  if (!node) return null;
  if (node.type === "leaf") {
    return node.id === leafID ? node : null;
  }
  return findTabbedPanelLeafByID(node.first, leafID) ?? findTabbedPanelLeafByID(node.second, leafID);
}

function pruneTabbedPanelNode(node: TabbedPanelNode | null, validTabs: ReadonlySet<string>): TabbedPanelNode | null {
  if (!node) return null;
  if (node.type === "leaf") {
    const tabs = uniqueTabbedPanelTabs(node.tabs.filter((tab) => validTabs.has(tab)));
    if (tabs.length === 0) return null;
    return {
      ...node,
      tabs,
      activeTabKey: tabs.includes(node.activeTabKey) ? node.activeTabKey : tabs[0]!,
    };
  }
  const first = pruneTabbedPanelNode(node.first, validTabs);
  const second = pruneTabbedPanelNode(node.second, validTabs);
  if (!first) return second;
  if (!second) return first;
  return {
    ...node,
    ratio: clampTabbedPanelRatio(node.ratio),
    first,
    second,
  };
}

function removeTabbedPanelTab(node: TabbedPanelNode | null, tabKey: string): TabbedPanelNode | null {
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
  const first = removeTabbedPanelTab(node.first, tabKey);
  const second = removeTabbedPanelTab(node.second, tabKey);
  if (!first) return second;
  if (!second) return first;
  return { ...node, first, second };
}

function insertTabbedPanelTabBefore(
  node: TabbedPanelNode | null,
  sourceTabKey: string,
  targetTabKey: string,
): TabbedPanelNode | null {
  if (!node) return createTabbedPanelLeaf([sourceTabKey], sourceTabKey);
  if (node.type === "leaf") {
    const targetIndex = node.tabs.indexOf(targetTabKey);
    if (targetIndex < 0) return node;
    const tabs = [...node.tabs.slice(0, targetIndex), sourceTabKey, ...node.tabs.slice(targetIndex)];
    return {
      ...node,
      tabs: uniqueTabbedPanelTabs(tabs),
    };
  }
  return {
    ...node,
    first: insertTabbedPanelTabBefore(node.first, sourceTabKey, targetTabKey) ?? node.first,
    second: insertTabbedPanelTabBefore(node.second, sourceTabKey, targetTabKey) ?? node.second,
  };
}

function insertTabbedPanelTabIntoLeaf(
  node: TabbedPanelNode | null,
  tabKey: string,
  leafID: string,
  placement: "start" | "end",
): TabbedPanelNode | null {
  if (!node) return createTabbedPanelLeaf([tabKey], tabKey);
  if (node.type === "leaf") {
    if (node.id !== leafID) return node;
    const tabs = placement === "start" ? [tabKey, ...node.tabs] : [...node.tabs, tabKey];
    return {
      ...node,
      tabs: uniqueTabbedPanelTabs(tabs),
      activeTabKey: tabKey,
    };
  }
  return {
    ...node,
    first: insertTabbedPanelTabIntoLeaf(node.first, tabKey, leafID, placement) ?? node.first,
    second: insertTabbedPanelTabIntoLeaf(node.second, tabKey, leafID, placement) ?? node.second,
  };
}

function insertTabbedPanelTabIntoFirstLeaf(node: TabbedPanelNode, tabKey: string): TabbedPanelNode {
  if (node.type === "leaf") {
    return {
      ...node,
      tabs: uniqueTabbedPanelTabs([...node.tabs, tabKey]),
    };
  }
  return {
    ...node,
    first: insertTabbedPanelTabIntoFirstLeaf(node.first, tabKey),
  };
}

function splitTabbedPanelLeaf(
  node: TabbedPanelNode,
  leafID: string,
  newLeaf: TabbedPanelLeaf,
  direction: TabbedPanelDirection,
  placement: "before" | "after",
): TabbedPanelNode {
  if (node.type === "leaf") {
    if (node.id !== leafID) return node;
    return {
      type: "split",
      id: newTabbedPanelID(),
      direction,
      ratio: 0.5,
      first: placement === "before" ? newLeaf : node,
      second: placement === "before" ? node : newLeaf,
    };
  }
  return {
    ...node,
    first: splitTabbedPanelLeaf(node.first, leafID, newLeaf, direction, placement),
    second: splitTabbedPanelLeaf(node.second, leafID, newLeaf, direction, placement),
  };
}

function newTabbedPanelID(): string {
  if (typeof crypto !== "undefined" && "randomUUID" in crypto) {
    return `panel-${crypto.randomUUID()}`;
  }
  return `panel-${Date.now().toString(36)}-${Math.random().toString(16).slice(2)}`;
}
