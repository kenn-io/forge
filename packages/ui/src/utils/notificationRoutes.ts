import type { NotificationItem } from "../api/types.js";
import { buildRoutedItemRoute } from "../routes.js";

export type NotificationDestination =
  | { kind: "internal"; path: string }
  | { kind: "external"; url: string };

function routedItemDestination(item: NotificationItem): NotificationDestination | null {
  if (!item.item_number || (item.item_type !== "pr" && item.item_type !== "issue")) return null;
  const provider = item.provider;
  const repoPath = item.repo_path || `${item.repo_owner}/${item.repo_name}`;
  if (!provider || !repoPath) return null;
  return {
    kind: "internal",
    path: buildRoutedItemRoute({
      itemType: item.item_type,
      provider,
      platformHost: item.platform_host,
      owner: item.repo_owner,
      name: item.repo_name,
      repoPath,
      number: item.item_number,
    }),
  };
}

export function notificationDestination(
  item: NotificationItem,
): NotificationDestination | null {
  return routedItemDestination(item) ?? (item.web_url ? { kind: "external", url: item.web_url } : null);
}
