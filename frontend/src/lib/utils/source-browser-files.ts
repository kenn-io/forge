import type { RepoBrowserCommit, RepoBrowserTreeEntry } from "../api/types.js";
import {
  categorizeDiffFile,
  type DiffFileCategory,
  type DiffFileCategoryCounts,
  type DiffFileCategoryFilter,
} from "./diff-categories.js";

export interface SourceBrowserFileEntry {
  path: string;
  type: string;
  size: number;
  category: DiffFileCategory;
  lastChanged?: RepoBrowserCommit | undefined;
}

export function buildSourceBrowserFileEntries(
  entries: readonly RepoBrowserTreeEntry[] | null | undefined,
  lastChanged?: Record<string, RepoBrowserCommit> | null | undefined,
): SourceBrowserFileEntry[] {
  return (entries ?? []).map((entry) => ({
    path: entry.path,
    type: entry.type,
    size: entry.size,
    category: categorizeDiffFile(entry.path),
    lastChanged: lastChanged?.[entry.path],
  }));
}

export function filterSourceBrowserFileEntriesByCategory(
  entries: readonly SourceBrowserFileEntry[],
  filter: DiffFileCategoryFilter,
): SourceBrowserFileEntry[] {
  if (filter === "all") return [...entries];
  return entries.filter((entry) => entry.category === filter);
}

export function countSourceBrowserFileEntriesByCategory(
  entries: readonly SourceBrowserFileEntry[],
): DiffFileCategoryCounts {
  const counts: DiffFileCategoryCounts = {
    plansDocs: 0,
    generated: 0,
    code: 0,
    tests: 0,
    other: 0,
    all: entries.length,
  };
  for (const entry of entries) {
    counts[entry.category] += 1;
  }
  return counts;
}
