import type { TerminalLayoutState, WorkflowNode } from "./terminal-layout";

export interface WorkflowPresetSession {
  sourceKey: string;
  targetKey: string;
  region: "workflow" | "terminal";
  label: string;
}

export interface WorkflowPreset {
  id: string;
  name: string;
  createdAt: string;
  updatedAt: string;
  sessions: WorkflowPresetSession[];
  layout: TerminalLayoutState;
}

export function mapWorkflowNodeSessionKeys(
  node: WorkflowNode | null,
  keyMap: Record<string, string>,
): WorkflowNode | null {
  if (!node) return null;
  if (node.type === "leaf") {
    const tabs = node.tabs.flatMap((tab) => {
      if (!tab.startsWith("session:")) return [tab];
      const mapped = keyMap[tab.slice("session:".length)];
      return mapped ? ([`session:${mapped}`] as const) : [];
    });
    if (tabs.length === 0) return null;
    const activeTabKey = node.activeTabKey.startsWith("session:")
      ? keyMap[node.activeTabKey.slice("session:".length)]
      : node.activeTabKey;
    return {
      ...node,
      tabs,
      activeTabKey:
        typeof activeTabKey === "string" && node.activeTabKey.startsWith("session:")
          ? `session:${activeTabKey}`
          : tabs.includes(node.activeTabKey)
            ? node.activeTabKey
            : tabs[0]!,
    };
  }
  const first = mapWorkflowNodeSessionKeys(node.first, keyMap);
  const second = mapWorkflowNodeSessionKeys(node.second, keyMap);
  if (!first) return second;
  if (!second) return first;
  return { ...node, first, second };
}
