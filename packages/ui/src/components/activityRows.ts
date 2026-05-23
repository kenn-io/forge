import type { ActivityItem } from "../api/types.js";

export interface CollapsedActivityCommits {
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
  | CollapsedActivityCommits;

export function isCollapsedActivityRow(
  row: ActivityRow,
): row is CollapsedActivityCommits {
  return "kind" in row && row.kind === "collapsed";
}

export function collapseActivityCommitRuns(
  items: ActivityItem[],
): ActivityRow[] {
  const result: ActivityRow[] = [];
  let i = 0;

  while (i < items.length) {
    const item = items[i]!;
    if (item.activity_type !== "commit") {
      result.push(item);
      i++;
      continue;
    }

    let j = i + 1;
    while (j < items.length) {
      const next = items[j]!;
      if (
        next.activity_type !== "commit"
        || next.author !== item.author
        || next.repo_owner !== item.repo_owner
        || next.repo_name !== item.repo_name
        || next.item_number !== item.item_number
      ) {
        break;
      }
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
        author: item.author,
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
