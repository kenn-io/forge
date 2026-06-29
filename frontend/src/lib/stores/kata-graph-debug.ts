export type KataGraphDebugEventKind =
  | "detail-load-abort"
  | "detail-load-complete"
  | "detail-load-stale"
  | "detail-load-start"
  | "graph-load-abort"
  | "graph-load-complete"
  | "graph-load-drain-end"
  | "graph-load-drain-join"
  | "graph-load-drain-start"
  | "graph-load-enqueue"
  | "graph-load-error"
  | "graph-load-paused"
  | "graph-load-start"
  | "graph-missing-refs"
  | "graph-render"
  | "selection-start";

export interface KataGraphDebugEvent {
  id: number;
  at: number;
  kind: KataGraphDebugEventKind;
  detail?: Record<string, unknown> | undefined;
}

export interface KataGraphDebugGraphSnapshot {
  sourceUID: string;
  selectedUID: string | null;
  hideDone: boolean;
  nodeIds: string[];
  disabledNodeIds: string[];
  missingRefKeys: string[];
  nodeCount: number;
  edgeCount: number;
}

export interface KataGraphDebugStoreSnapshot {
  queueKeys: string[];
  graphLoadActive: boolean;
  issueRefreshActive: boolean;
  pendingSelectionUID: string | null;
  selectedIssueUID: string | null;
  cachedTaskCount: number;
}

export interface KataGraphDebugSnapshot {
  events: KataGraphDebugEvent[];
  latestGraph?: KataGraphDebugGraphSnapshot | undefined;
  store?: KataGraphDebugStoreSnapshot | undefined;
}

export interface KataGraphDebugAPI {
  snapshot: () => KataGraphDebugSnapshot;
  reset: () => void;
}

const maxEvents = 200;
let nextEventID = 1;
let events: KataGraphDebugEvent[] = [];
let latestGraph: KataGraphDebugGraphSnapshot | undefined;
let store: KataGraphDebugStoreSnapshot | undefined;

function now(): number {
  if (typeof performance !== "undefined" && typeof performance.now === "function") {
    return performance.now();
  }
  return Date.now();
}

function cloneSnapshot(): KataGraphDebugSnapshot {
  return {
    events: events.map((event) => ({ ...event, detail: event.detail ? { ...event.detail } : undefined })),
    latestGraph: latestGraph
      ? {
          ...latestGraph,
          nodeIds: [...latestGraph.nodeIds],
          disabledNodeIds: [...latestGraph.disabledNodeIds],
          missingRefKeys: [...latestGraph.missingRefKeys],
        }
      : undefined,
    store: store ? { ...store, queueKeys: [...store.queueKeys] } : undefined,
  };
}

export function recordKataGraphDebugEvent(kind: KataGraphDebugEventKind, detail?: Record<string, unknown>): void {
  events = [...events, { id: nextEventID++, at: now(), kind, detail }].slice(-maxEvents);
}

export function setKataGraphDebugGraph(snapshot: KataGraphDebugGraphSnapshot): void {
  latestGraph = {
    ...snapshot,
    nodeIds: [...snapshot.nodeIds],
    disabledNodeIds: [...snapshot.disabledNodeIds],
    missingRefKeys: [...snapshot.missingRefKeys],
  };
}

export function setKataGraphDebugStore(snapshot: KataGraphDebugStoreSnapshot): void {
  store = { ...snapshot, queueKeys: [...snapshot.queueKeys] };
}

export function getKataGraphDebugSnapshot(): KataGraphDebugSnapshot {
  return cloneSnapshot();
}

export function resetKataGraphDebug(): void {
  nextEventID = 1;
  events = [];
  latestGraph = undefined;
  store = undefined;
}

export const kataGraphDebug: KataGraphDebugAPI = {
  snapshot: getKataGraphDebugSnapshot,
  reset: resetKataGraphDebug,
};

if (typeof window !== "undefined") {
  window.__middleman_kata_graph_debug = kataGraphDebug;
}
