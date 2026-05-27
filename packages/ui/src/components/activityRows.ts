import type { ActivityItem } from "../api/types.js";

export interface CollapsedActivityRun {
  kind: "collapsed";
  id: string;
  author: string;
  count: number;
  earliest: string;
  latest: string;
  representative: ActivityItem;
}

export type ActivityRow =
  | ActivityItem
  | CollapsedActivityRun;

export function isCollapsedActivityRow(
  row: ActivityRow,
): row is CollapsedActivityRun {
  return "kind" in row && row.kind === "collapsed";
}

export function isDefaultBranchCommitActivity(
  item: ActivityItem,
): boolean {
  return item.activity_type === "default_branch_commit";
}

export function isDefaultBranchForcePushActivity(
  item: ActivityItem,
): boolean {
  return item.activity_type === "default_branch_force_push";
}

export function isDefaultBranchActivity(
  item: ActivityItem,
): boolean {
  return isDefaultBranchCommitActivity(item)
    || isDefaultBranchForcePushActivity(item);
}

export function shortSha(sha: string | undefined): string {
  return sha ? sha.slice(0, 7) : "";
}

function repoKeyForItem(item: ActivityItem): string {
  return activityRepoKey({
    provider: item.repo?.provider ?? "",
    platformHost: item.platform_host ?? item.repo?.platform_host ?? "",
    owner: item.repo_owner,
    name: item.repo_name,
  });
}

function activityRunAuthor(item: ActivityItem): string {
  return item.author_name || item.author;
}

function activityRunGroupKey(item: ActivityItem): string | null {
  const author = activityRunAuthor(item);
  if (
    item.activity_type === "commit"
    || item.activity_type === "comment"
    || item.activity_type === "review"
  ) {
    return [
      "item",
      item.activity_type,
      repoKeyForItem(item),
      item.item_type,
      item.item_number,
      author,
    ].join("|");
  }

  if (isDefaultBranchCommitActivity(item)) {
    return [
      "branch",
      repoKeyForItem(item),
      item.branch_name ?? "",
      author,
    ].join("|");
  }

  return null;
}

export function collapseActivityRuns(
  items: ActivityItem[],
): ActivityRow[] {
  const result: ActivityRow[] = [];
  let i = 0;

  while (i < items.length) {
    const item = items[i]!;
    const groupKey = activityRunGroupKey(item);
    if (groupKey === null) {
      result.push(item);
      i++;
      continue;
    }

    let j = i + 1;
    while (j < items.length) {
      const next = items[j]!;
      if (activityRunGroupKey(next) !== groupKey) break;
      j++;
    }

    const count = j - i;
    if (count < 3) {
      for (let k = i; k < j; k++) {
        result.push(items[k]!);
      }
    } else {
      const latest = items[i]!;
      const earliest = items[j - 1]!;
      result.push({
        kind: "collapsed",
        id: `collapsed-${latest.id}-${count}`,
        author: activityRunAuthor(item),
        count,
        earliest: earliest.created_at,
        latest: latest.created_at,
        representative: latest,
      });
    }

    i = j;
  }

  return result;
}

export interface ActivityRepoKeyRef {
  provider: string;
  platformHost: string;
  owner: string;
  name: string;
}

export function activityRepoKey(ref: ActivityRepoKeyRef): string {
  return `${ref.provider}|${ref.platformHost}|${ref.owner}/${ref.name}`;
}

export function activityItemKey(
  ref: ActivityRepoKeyRef & { itemType: string; itemNumber: number },
): string {
  return `${activityRepoKey(ref)}:${ref.itemType}:${ref.itemNumber}`;
}

export function activityBranchKey(
  ref: ActivityRepoKeyRef & { branchName: string },
): string {
  return `${activityRepoKey(ref)}:branch:${ref.branchName}`;
}
