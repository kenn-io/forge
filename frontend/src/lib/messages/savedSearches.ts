export interface QuickView {
  label: string;
  query: string;
}

export interface SavedSearch {
  name: string;
  query: string;
}

export const QUICK_VIEWS: readonly QuickView[] = [
  { label: "Inbox", query: "label:Inbox" },
  { label: "Has attachments", query: "has:attachment" },
  { label: "Recent", query: "newer_than:30d" },
];

const MAX_SAVED = 50;
const MAX_NAME_LEN = 200;
const MAX_QUERY_LEN = 500;

export function addSavedSearch(list: SavedSearch[], name: string, query: string): SavedSearch[] {
  const trimmedQuery = query.trim();
  if (!trimmedQuery) return list;

  const trimmedName = name.trim();
  const finalName = truncateRunes(trimmedName || trimmedQuery, MAX_NAME_LEN);
  const finalQuery = truncateRunes(trimmedQuery, MAX_QUERY_LEN);

  const lower = finalName.toLowerCase();
  const idx = list.findIndex((s) => s.name.toLowerCase() === lower);
  if (idx >= 0) {
    const next = [...list];
    next[idx] = { name: finalName, query: finalQuery };
    return next;
  }

  const appended = [...list, { name: finalName, query: finalQuery }];
  return appended.length > MAX_SAVED ? appended.slice(appended.length - MAX_SAVED) : appended;
}

export function removeSavedSearch(list: SavedSearch[], name: string): SavedSearch[] {
  const lower = name.trim().toLowerCase();
  const next = list.filter((s) => s.name.toLowerCase() !== lower);
  return next.length === list.length ? list : next;
}

export function normalizeSavedSearches(input: readonly unknown[]): SavedSearch[] {
  let out: SavedSearch[] = [];
  for (const entry of input) {
    if (typeof entry !== "object" || entry === null) continue;
    const { name, query } = entry as { name?: unknown; query?: unknown };
    if (typeof query !== "string") continue;
    const rawName = typeof name === "string" ? name : "";
    out = addSavedSearch(out, rawName, query);
  }
  return out;
}

function truncateRunes(s: string, maxRunes: number): string {
  const runes = Array.from(s);
  return runes.length > maxRunes ? runes.slice(0, maxRunes).join("") : s;
}
