import type { Label } from "../api/generated/models/label.js";

export type LabelFilterMode = "require" | "exclude";

export interface LabelFilterEntry {
  name: string;
  mode: LabelFilterMode;
}

/** Label filters keyed by lowercased name, so "Bug" in one repository and "bug" in another are one choice. */
export type LabelFilters = ReadonlyMap<string, LabelFilterEntry>;

export interface LabelFilterChoice {
  key: string;
  name: string;
  color: string;
  /** Loaded items carrying the label. */
  count: number;
  mode: LabelFilterMode | undefined;
}

export function labelFilterKey(name: string): string {
  return name.toLowerCase();
}

/** Neutral, then required, then excluded, then neutral again. */
export function cycleLabelFilter(filters: LabelFilters, name: string): LabelFilters {
  const key = labelFilterKey(name);
  const current = filters.get(key);
  const next = new Map(filters);
  if (current === undefined) next.set(key, { name, mode: "require" });
  else if (current.mode === "require") next.set(key, { name: current.name, mode: "exclude" });
  else next.delete(key);
  return next;
}

/** An item matches when it has every required label and none of the excluded ones. */
export function matchesLabelFilters(
  labels: readonly Pick<Label, "name">[] | undefined,
  filters: LabelFilters,
): boolean {
  if (filters.size === 0) return true;
  const present = new Set((labels ?? []).map((label) => labelFilterKey(label.name)));
  for (const [key, entry] of filters) {
    if (present.has(key) !== (entry.mode === "require")) return false;
  }
  return true;
}

/**
 * Choices come from every loaded item, not only the visible ones, so counts and order
 * stay put while the user cycles labels. A selected label no loaded item carries stays
 * listed at the end, so it can still be cleared.
 */
export function labelFilterChoices(
  items: readonly { labels?: readonly Pick<Label, "name" | "color">[] | undefined }[],
  filters: LabelFilters,
): LabelFilterChoice[] {
  const choices = new Map<string, LabelFilterChoice>();
  for (const item of items) {
    for (const label of item.labels ?? []) {
      const key = labelFilterKey(label.name);
      const choice = choices.get(key);
      if (choice) choice.count += 1;
      else choices.set(key, { key, name: label.name, color: label.color, count: 1, mode: filters.get(key)?.mode });
    }
  }
  const loaded = [...choices.values()].sort((a, b) => b.count - a.count || a.name.localeCompare(b.name));
  const missing = [...filters]
    .filter(([key]) => !choices.has(key))
    .map(([key, entry]) => ({ key, name: entry.name, color: "", count: 0, mode: entry.mode }));
  return [...loaded, ...missing];
}

export function labelFilterSummary(filters: LabelFilters): string {
  let required = 0;
  for (const entry of filters.values()) if (entry.mode === "require") required += 1;
  const excluded = filters.size - required;
  const parts = [required > 0 && `${required} req`, excluded > 0 && `${excluded} excl`].filter(Boolean);
  return parts.length === 0 ? "Any" : parts.join(", ");
}

/** The server query for label filters: names every item must carry, and names none may carry. */
export function labelFilterParams(filters: LabelFilters): { labels?: string; exclude_labels?: string } {
  const required: string[] = [];
  const excluded: string[] = [];
  for (const entry of filters.values()) (entry.mode === "require" ? required : excluded).push(entry.name);
  return {
    ...(required.length > 0 ? { labels: required.join(",") } : {}),
    ...(excluded.length > 0 ? { exclude_labels: excluded.join(",") } : {}),
  };
}

/**
 * Label choices that stay put while label filters change. The server applies
 * label filters, so once one is on the loaded items carry only matching
 * labels; choices then come from the items loaded before any label filter.
 */
export function rememberedLabelChoices<T extends { labels?: readonly Pick<Label, "name" | "color">[] | undefined }>(
  items: () => readonly T[],
  filters: () => LabelFilters,
): () => LabelFilterChoice[] {
  let unfiltered: readonly T[] = [];
  return () => {
    const active = filters();
    if (active.size === 0) unfiltered = items();
    return labelFilterChoices(active.size === 0 || unfiltered.length === 0 ? items() : unfiltered, active);
  };
}
