import type { RepoLabelFormatter } from "../../utils/repo-label.js";

interface RepoItem {
  repo: {
    provider: string;
    platform_host: string;
    owner: string;
    name: string;
    repo_path: string;
    private?: boolean | undefined;
  };
}

export interface RepoGroup<T> {
  key: string;
  /** Key for the collapsed-repos store. */
  collapseKey: string;
  label: string;
  isPrivate: boolean | undefined;
  items: T[];
}

/** Repository sections for a list grouped by repo, in the store's order. */
export function repoGroups<T extends RepoItem>(byRepo: Map<string, T[]>, labels: RepoLabelFormatter): RepoGroup<T>[] {
  return [...byRepo.entries()].map(([repo, items]) => ({
    key: `repo:${repo}`,
    collapseKey: repo,
    label: items[0] ? repoItemLabel(items[0], labels) : repo,
    isPrivate: items[0]?.repo.private,
    items,
  }));
}

export function repoItemLabel(item: RepoItem, labels: RepoLabelFormatter): string {
  return labels.format({
    provider: item.repo.provider,
    platformHost: item.repo.platform_host,
    owner: item.repo.owner,
    name: item.repo.name,
    repoPath: item.repo.repo_path,
  });
}
