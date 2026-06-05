import type { RoutedItemRef } from "@middleman/ui/routes";

import type { RecentsState } from "./types.js";

export const RECENTS_KEY = "middleman-palette-recents";
export const MAX_ITEMS = 8;
const STALE_MS = 30 * 24 * 60 * 60 * 1000;

type RecentItem = RecentsState["items"][number];

const EPOCH = new Date(0).toISOString();

function emptyState(): RecentsState {
  return { version: 1, items: [] };
}

function safeGet(): string | null {
  try {
    return localStorage.getItem(RECENTS_KEY);
  } catch {
    return null;
  }
}

function safeSet(value: RecentsState): void {
  try {
    localStorage.setItem(RECENTS_KEY, JSON.stringify(value));
  } catch {
    // localStorage unavailable; nothing else to do.
  }
}

function isPersistedItem(value: unknown): value is { kind: unknown; ref: unknown; lastSelectedAt: unknown } {
  return typeof value === "object" && value !== null;
}

function normalizeTimestamp(value: unknown): string {
  if (typeof value !== "string") return EPOCH;
  // Reject strings that don't parse as a real date so consumers that sort or
  // diff by lastSelectedAt never see NaN. pruneStale also needs this — it
  // filters by Number.isNaN but only after the value has already round-tripped
  // through a write call.
  return Number.isNaN(Date.parse(value)) ? EPOCH : value;
}

function isValidRoutedItemRef(kind: "pr" | "issue", value: unknown): value is RoutedItemRef {
  if (typeof value !== "object" || value === null) return false;
  const r = value as Record<string, unknown>;
  if (r.itemType !== kind) return false;
  if (typeof r.provider !== "string") return false;
  if (typeof r.owner !== "string") return false;
  if (typeof r.name !== "string") return false;
  if (typeof r.repoPath !== "string") return false;
  if (typeof r.number !== "number" || !Number.isFinite(r.number)) return false;
  if (r.platformHost !== undefined && typeof r.platformHost !== "string") {
    return false;
  }
  return true;
}

function normalizeItems(rawItems: unknown[]): RecentItem[] {
  const normalized: RecentItem[] = [];
  for (const raw of rawItems) {
    if (!isPersistedItem(raw)) continue;
    if (raw.kind !== "pr" && raw.kind !== "issue") continue;
    // Validate the ref shape before keeping the item — a corrupt or
    // hand-edited entry like { kind: "pr", ref: null, ... } would
    // otherwise crash the palette every time it opens.
    if (!isValidRoutedItemRef(raw.kind, raw.ref)) continue;
    normalized.push({
      kind: raw.kind,
      ref: raw.ref,
      lastSelectedAt: normalizeTimestamp(raw.lastSelectedAt),
    });
  }
  return normalized;
}

export function readRecents(): RecentsState {
  const raw = safeGet();
  if (!raw) return emptyState();
  let parsed: unknown;
  try {
    parsed = JSON.parse(raw);
  } catch {
    const empty = emptyState();
    safeSet(empty);
    return empty;
  }
  if (
    !parsed ||
    typeof parsed !== "object" ||
    (parsed as { version?: unknown }).version !== 1 ||
    !Array.isArray((parsed as { items?: unknown }).items)
  ) {
    const empty = emptyState();
    safeSet(empty);
    return empty;
  }
  return {
    version: 1,
    items: normalizeItems((parsed as { items: unknown[] }).items),
  };
}

function dedupeKey(kind: "pr" | "issue", ref: RoutedItemRef): string {
  return `${kind}|${JSON.stringify(ref)}`;
}

export function writeRecent(kind: "pr" | "issue", ref: RoutedItemRef): void {
  const state = readRecents();
  const key = dedupeKey(kind, ref);
  const filtered = state.items.filter((item) => dedupeKey(item.kind, item.ref) !== key);
  filtered.unshift({
    kind,
    ref,
    lastSelectedAt: new Date().toISOString(),
  });
  if (filtered.length > MAX_ITEMS) filtered.length = MAX_ITEMS;
  safeSet({ version: 1, items: filtered });
}

export function pruneRecents(filter: (item: RecentItem) => boolean): void {
  const state = readRecents();
  safeSet({ version: 1, items: state.items.filter(filter) });
}

export function pruneStale(now: Date = new Date()): void {
  const cutoff = now.getTime() - STALE_MS;
  pruneRecents((item) => {
    const at = new Date(item.lastSelectedAt).getTime();
    if (Number.isNaN(at)) return false;
    return at >= cutoff;
  });
}
