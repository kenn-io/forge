import { createRepoLabelFormatter, repoIdentityKey } from "../../utils/repo-label.js";
import type { WorkspaceListItem } from "../terminal/workspace-list-schema.js";
import type { WorkspaceListSort } from "../terminal/workspaceListSort.js";

export interface MobileWorkspaceGroup {
  key: string;
  label: string;
  items: WorkspaceListItem[];
}

function timeValue(value: string | null | undefined): number {
  if (!value) return 0;
  const milliseconds = Date.parse(value);
  return Number.isNaN(milliseconds) ? 0 : milliseconds;
}

export function mobileWorkspaceDisplayName(workspace: WorkspaceListItem): string {
  if (workspace.item_type === "kata_task") {
    return workspace.kata?.title?.trim() || workspace.git_head_ref;
  }
  return workspace.mr_title?.trim() || workspace.git_head_ref;
}

export function mobileWorkspaceItemNumber(workspace: WorkspaceListItem): number | null {
  if (workspace.item_type !== "adhoc") return workspace.item_number;
  const number = workspace.associated_pr_number;
  return number !== null && number !== undefined && number > 0 ? number : null;
}

export function workspaceMatchesMobileSearch(workspace: WorkspaceListItem, rawQuery: string): boolean {
  const query = rawQuery.trim().toLowerCase();
  if (!query) return true;
  const number = mobileWorkspaceItemNumber(workspace);
  const values = [
    mobileWorkspaceDisplayName(workspace),
    workspace.git_head_ref,
    workspace.git_head_ref.replace(/^refs\/heads\//, ""),
    workspace.platform_host,
    workspace.repo_owner,
    workspace.repo_name,
    workspace.repo?.repo_path,
    `${workspace.repo_owner}/${workspace.repo_name}`,
    workspace.fleet_host_key,
    workspace.kata?.short_id,
    workspace.kata?.qualified_id,
    workspace.kata?.title,
    workspace.item_type === "adhoc" ? "new work" : undefined,
    number === null ? undefined : String(number),
    number === null ? undefined : `#${number}`,
  ];
  return values.some((value) => value?.toLowerCase().includes(query));
}

export function sortMobileWorkspaces(
  workspaces: readonly WorkspaceListItem[],
  sort: Exclude<WorkspaceListSort, "repo">,
): WorkspaceListItem[] {
  const stamp =
    sort === "activity"
      ? (workspace: WorkspaceListItem) => timeValue(workspace.tmux_last_output_at) || timeValue(workspace.created_at)
      : sort === "item-activity"
        ? (workspace: WorkspaceListItem) =>
            timeValue(workspace.item_last_activity_at) || timeValue(workspace.created_at)
        : (workspace: WorkspaceListItem) => timeValue(workspace.created_at);
  return [...workspaces].sort((left, right) => stamp(right) - stamp(left) || left.id.localeCompare(right.id));
}

export function groupMobileWorkspaces(
  workspaces: readonly WorkspaceListItem[],
  showOrgNames: boolean,
): MobileWorkspaceGroup[] {
  const identities = workspaces.map((workspace) => ({
    provider: workspace.repo?.provider ?? "",
    platformHost: workspace.platform_host,
    owner: workspace.repo_owner,
    name: workspace.repo_name,
    repoPath: workspace.repo?.repo_path,
  }));
  const formatter = createRepoLabelFormatter(identities, { showOrgNames });
  const groups = new Map<string, MobileWorkspaceGroup>();
  for (const workspace of workspaces) {
    const identity = {
      provider: workspace.repo?.provider ?? "",
      platformHost: workspace.platform_host,
      owner: workspace.repo_owner,
      name: workspace.repo_name,
      repoPath: workspace.repo?.repo_path,
    };
    const key = repoIdentityKey(identity);
    const existing = groups.get(key);
    if (existing) {
      existing.items.push(workspace);
    } else {
      groups.set(key, { key, label: formatter.format(identity), items: [workspace] });
    }
  }
  return Array.from(groups.values());
}
