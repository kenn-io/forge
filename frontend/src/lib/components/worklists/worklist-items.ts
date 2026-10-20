import { canonicalProvider, resolvedPlatformHost } from "../../api/provider-routes.js";
import type { WorklistItem } from "../../api/worklists.js";
import type { ListItemTarget } from "../../stores/item-actions.js";
import type { WorklistItemSelection } from "../../routes.js";

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

/** Whether the item changed or is gone since the agent wrote it. */
export function worklistItemNotFresh(item: WorklistItem): boolean {
  return item.freshness !== "fresh";
}

export function worklistItemKey(item: WorklistItem): string {
  return `${item.item_type}:${item.repo.provider}:${item.repo.platform_host}:${item.repo.repo_path}#${item.number}`;
}

export function worklistItemSelection(
  item: WorklistItem,
  detailTab: WorklistItemSelection["detailTab"] = "conversation",
): WorklistItemSelection {
  return {
    itemType: item.item_type,
    provider: item.repo.provider,
    platformHost: item.repo.platform_host,
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
    item.repo.repo_path === selection.repoPath
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
