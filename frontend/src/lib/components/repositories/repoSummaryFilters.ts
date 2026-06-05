import type { RepoFilter, RepoSort } from "./repoSummary.js";

const storageKey = "middleman:repoSummaryFilters";

const validFilters = new Set<RepoFilter>(["all", "prs", "issues", "stale"]);
const validSorts = new Set<RepoSort>(["name", "open-prs", "open-issues", "activity", "stale"]);

export interface RepoSummaryFilters {
  searchQuery: string;
  activeFilter: RepoFilter;
  sortMode: RepoSort;
}

export const defaultRepoSummaryFilters: RepoSummaryFilters = {
  searchQuery: "",
  activeFilter: "all",
  sortMode: "name",
};

function getStorage(): Storage | null {
  try {
    return typeof localStorage === "undefined" ? null : localStorage;
  } catch {
    return null;
  }
}

function normalizeFilters(value: unknown): RepoSummaryFilters {
  if (typeof value !== "object" || value === null) {
    return { ...defaultRepoSummaryFilters };
  }
  const candidate = value as Partial<RepoSummaryFilters>;
  return {
    searchQuery:
      typeof candidate.searchQuery === "string" ? candidate.searchQuery : defaultRepoSummaryFilters.searchQuery,
    activeFilter:
      candidate.activeFilter && validFilters.has(candidate.activeFilter)
        ? candidate.activeFilter
        : defaultRepoSummaryFilters.activeFilter,
    sortMode:
      candidate.sortMode && validSorts.has(candidate.sortMode)
        ? candidate.sortMode
        : defaultRepoSummaryFilters.sortMode,
  };
}

export function loadRepoSummaryFilters(): RepoSummaryFilters {
  const storage = getStorage();
  if (!storage) return { ...defaultRepoSummaryFilters };

  try {
    return normalizeFilters(JSON.parse(storage.getItem(storageKey) ?? "null"));
  } catch {
    return { ...defaultRepoSummaryFilters };
  }
}

export function saveRepoSummaryFilters(filters: RepoSummaryFilters): void {
  const storage = getStorage();
  if (!storage) return;

  try {
    storage.setItem(storageKey, JSON.stringify(normalizeFilters(filters)));
  } catch {
    // Storage blocked - filters still work for the current page instance.
  }
}
