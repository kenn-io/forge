import { canonicalProvider, resolvedPlatformHost } from "../../api/provider-routes.js";
import { repositoryKeyFromWire, repositoryKeyString, sameRepositoryKey } from "../../api/repository-key.js";
import type { WorklistItem, WorklistItemMark } from "../../api/worklists.js";
import type { ListItemTarget } from "../../stores/item-actions.js";
import type { WorklistItemSelection } from "../../routes.js";
import { matchesSearchQuery, type SearchQuery } from "../../utils/search-query.js";

export interface WorklistChangeMarker {
  readonly label: string;
  readonly tone: "muted" | "info";
}

const TERMINAL_STATES = new Set(["merged", "closed"]);

function ciWord(status: string | undefined): string {
  const normalized = (status ?? "").trim().toLowerCase();
  if (normalized === "success" || normalized === "passed") return "passing";
  if (normalized === "failure" || normalized === "failed" || normalized === "error") return "failing";
  if (normalized === "pending" || normalized === "running" || normalized === "queued") return "running";
  return normalized === "" ? "none" : normalized;
}

function reviewWords(decision: string): string {
  const words = decision.trim().replace(/_/g, " ").toLowerCase();
  return words.charAt(0).toUpperCase() + words.slice(1);
}

export function worklistChangeMarkers(item: WorklistItem): WorklistChangeMarker[] {
  if (item.live === null) return [{ label: "No longer synced", tone: "muted" }];
  const changes = item.changes;
  const markers: WorklistChangeMarker[] = [];
  const stateTo = changes.state_to?.trim().toLowerCase();
  const stateFrom = changes.state_from?.trim().toLowerCase();
  if (stateTo === "merged") markers.push({ label: "Merged", tone: "muted" });
  else if (stateTo === "closed") markers.push({ label: "Closed", tone: "muted" });
  else if (stateTo && stateFrom && TERMINAL_STATES.has(stateFrom)) markers.push({ label: "Reopened", tone: "info" });
  if (changes.new_commits) markers.push({ label: "New commits", tone: "info" });
  if (changes.new_comments > 0) {
    markers.push({
      label: `${changes.new_comments} new ${changes.new_comments === 1 ? "comment" : "comments"}`,
      tone: "info",
    });
  }
  if (changes.ci_from !== undefined || changes.ci_to !== undefined) {
    markers.push({ label: `CI ${ciWord(changes.ci_from)} → ${ciWord(changes.ci_to)}`, tone: "info" });
  }
  if (changes.review_to !== undefined || changes.review_from !== undefined) {
    const to = changes.review_to?.trim() ?? "";
    markers.push({ label: to === "" ? "Review cleared" : reviewWords(to), tone: "info" });
  }
  return markers;
}

/** Whether the item is merged, closed, or no longer synced, whatever its state when the agent wrote it. */
export function worklistItemClosed(item: WorklistItem): boolean {
  return item.live === null || TERMINAL_STATES.has(item.live.state.trim().toLowerCase());
}

/** Whether the maintainer has nothing left to do: the item is handled or closed. */
export function worklistItemDone(item: WorklistItem): boolean {
  return item.handled_at !== null || worklistItemClosed(item);
}

/** Whether the item changed or is gone since the agent wrote it. */
export function worklistItemNotFresh(item: WorklistItem): boolean {
  return item.freshness !== "fresh";
}

/** Row identity by provider-verified repository identity; owner/name is a mutable route. */
export function worklistItemKey(item: WorklistItem): string {
  const { provider, platform_host } = item.repo;
  const repositoryKey = repositoryKeyFromWire(item.repo);
  const repository = repositoryKey ? repositoryKeyString(repositoryKey) : "";
  return `${item.item_type}:${canonicalProvider(provider)}:${resolvedPlatformHost(provider, platform_host)}:${repository}#${item.number}`;
}

/** Whether a mark names the item, by provider-verified repository identity. */
export function worklistMarkMatchesItem(mark: WorklistItemMark, item: WorklistItem): boolean {
  return (
    mark.item_type === item.item_type &&
    mark.number === item.number &&
    canonicalProvider(mark.repo.provider) === canonicalProvider(item.repo.provider) &&
    resolvedPlatformHost(mark.repo.provider, mark.repo.platform_host) ===
      resolvedPlatformHost(item.repo.provider, item.repo.platform_host) &&
    sameRepositoryKey(repositoryKeyFromWire(mark.repo), repositoryKeyFromWire(item.repo))
  );
}

export function worklistItemSelection(
  item: WorklistItem,
  detailTab: WorklistItemSelection["detailTab"] = "conversation",
): WorklistItemSelection {
  return {
    itemType: item.item_type,
    provider: item.repo.provider,
    platformHost: item.repo.platform_host,
    repositoryKey: repositoryKeyFromWire(item.repo),
    repoPath: item.repo.repo_path,
    owner: item.repo.owner,
    name: item.repo.name,
    number: item.number,
    detailTab: item.item_type === "pr" ? detailTab : "conversation",
  };
}

export function worklistItemMatchesSelection(
  item: WorklistItem,
  selection: WorklistItemSelection | null | undefined,
): boolean {
  if (!selection) return false;
  return (
    item.item_type === selection.itemType &&
    item.number === selection.number &&
    canonicalProvider(item.repo.provider) === canonicalProvider(selection.provider) &&
    resolvedPlatformHost(item.repo.provider, item.repo.platform_host) ===
      resolvedPlatformHost(selection.provider, selection.platformHost) &&
    selection.repositoryKey !== undefined &&
    sameRepositoryKey(repositoryKeyFromWire(item.repo), selection.repositoryKey)
  );
}

export function worklistItemTarget(item: WorklistItem): ListItemTarget | null {
  if (item.item_type === "pr" && item.pull) return { kind: "pull", item: item.pull };
  if (item.item_type === "issue" && item.issue) return { kind: "issue", item: item.issue };
  return null;
}

export function worklistSections(items: readonly WorklistItem[]): { section: string; items: WorklistItem[] }[] {
  const groups = new Map<string, WorklistItem[]>();
  for (const item of [...items].sort((a, b) => a.position - b.position)) {
    const group = groups.get(item.section);
    if (group) group.push(item);
    else groups.set(item.section, [item]);
  }
  return [...groups].map(([section, sectionItems]) => ({ section, items: sectionItems }));
}

/** Rail row order: the agent's rank grouped by section, or a flat list by score. */
export type WorklistOrder = "agent" | "score";

const ORDER_STORAGE_KEY = "kenn-forge:worklists:order";

export function readWorklistOrder(): WorklistOrder {
  try {
    return globalThis.localStorage?.getItem(ORDER_STORAGE_KEY) === "score" ? "score" : "agent";
  } catch {
    return "agent";
  }
}

export function writeWorklistOrder(order: WorklistOrder): void {
  try {
    if (order === "score") globalThis.localStorage?.setItem(ORDER_STORAGE_KEY, "score");
    else globalThis.localStorage?.removeItem(ORDER_STORAGE_KEY);
  } catch {
    // localStorage can be unavailable in restricted browser contexts.
  }
}

const NOTES_COLLAPSED_STORAGE_KEY = "kenn-forge:worklists:notes-collapsed";

/** Whether the open item's context strip shows only its header line. */
export function readWorklistNotesCollapsed(): boolean {
  try {
    return globalThis.localStorage?.getItem(NOTES_COLLAPSED_STORAGE_KEY) === "1";
  } catch {
    return false;
  }
}

export function writeWorklistNotesCollapsed(collapsed: boolean): void {
  try {
    if (collapsed) globalThis.localStorage?.setItem(NOTES_COLLAPSED_STORAGE_KEY, "1");
    else globalThis.localStorage?.removeItem(NOTES_COLLAPSED_STORAGE_KEY);
  } catch {
    // localStorage can be unavailable in restricted browser contexts.
  }
}

/** Highest score first, ties by agent rank, unscored items last. */
export function worklistItemsByScore(items: readonly WorklistItem[]): WorklistItem[] {
  return [...items].sort((a, b) => {
    if (a.score !== b.score) {
      if (a.score === null) return 1;
      if (b.score === null) return -1;
      return b.score - a.score;
    }
    return a.position - b.position;
  });
}

/** How many items are left to handle, for the worklist picker. */
export function worklistRemainingLabel(remaining: number): string {
  return remaining === 0 ? "done" : `${remaining} left`;
}

/** Which rows the rail shows: open items, closed items, or both. */
export type WorklistStateFilter = "open" | "closed" | "all";

const STATE_FILTER_STORAGE_KEY = "kenn-forge:worklists:state";

export function readWorklistStateFilter(): WorklistStateFilter {
  try {
    const stored = globalThis.localStorage?.getItem(STATE_FILTER_STORAGE_KEY);
    return stored === "closed" || stored === "all" ? stored : "open";
  } catch {
    return "open";
  }
}

export function writeWorklistStateFilter(filter: WorklistStateFilter): void {
  try {
    if (filter === "open") globalThis.localStorage?.removeItem(STATE_FILTER_STORAGE_KEY);
    else globalThis.localStorage?.setItem(STATE_FILTER_STORAGE_KEY, filter);
  } catch {
    // localStorage can be unavailable in restricted browser contexts.
  }
}

export function worklistItemsInState(items: readonly WorklistItem[], filter: WorklistStateFilter): WorklistItem[] {
  if (filter === "all") return [...items];
  const closed = filter === "closed";
  return items.filter((item) => worklistItemClosed(item) === closed);
}

/** The rail's rows in the stored order and state filter, as shown with every section expanded. */
export function worklistStoredRows(items: readonly WorklistItem[]): WorklistItem[] {
  const shown = worklistItemsInState(items, readWorklistStateFilter());
  return readWorklistOrder() === "score"
    ? worklistItemsByScore(shown)
    : worklistSections(shown).flatMap((section) => section.items);
}

/** Whether the item matches a rail search, by its title, repository, number, author, labels, or the agent's words. */
export function worklistItemMatchesSearch(item: WorklistItem, query: SearchQuery): boolean {
  const row = item.pull ?? item.issue;
  const repository = `${item.repo.owner}/${item.repo.name}`;
  return matchesSearchQuery(query, [
    item.title,
    repository,
    `${repository}#${item.number}`,
    `#${item.number}`,
    item.section,
    item.note,
    item.caveat,
    row?.Author,
    item.pull?.AuthorDisplayName,
    ...(row?.labels ?? []).map((label) => label.name),
  ]);
}
