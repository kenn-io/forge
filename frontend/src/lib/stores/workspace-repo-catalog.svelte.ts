import type { WorkspaceListItem } from "../components/terminal/workspace-list-schema.js";
import type { RepoPresetCatalogEntry } from "./repo-presets.js";
import { canonicalRepoFilterValue } from "../utils/repo-filter-values.js";

let entries = $state.raw<readonly RepoPresetCatalogEntry[]>([]);
let ready = $state(false);

export function getWorkspaceRepoCatalog(): readonly RepoPresetCatalogEntry[] {
  return entries;
}

export function isWorkspaceRepoCatalogReady(): boolean {
  return ready;
}

export function setWorkspaceRepoCatalog(workspaces: readonly WorkspaceListItem[] | undefined, complete: boolean): void {
  if (!workspaces) {
    entries = [];
    ready = false;
    return;
  }
  const next: RepoPresetCatalogEntry[] = [];
  const identities = workspaces.flatMap((workspace) => {
    const repo = workspace.repo;
    if (!repo) return [];
    return [
      {
        provider: repo.provider,
        platformHost: repo.platform_host,
        repoPath: repo.repo_path,
        isGlob: false,
        repo,
      },
    ];
  });
  for (const identity of identities) {
    const value = canonicalRepoFilterValue(identity, identities);
    if (!value) continue;
    const entry = {
      value,
      provider: identity.repo.provider,
      platform_host: identity.repo.platform_host,
      platform_repo_id: identity.repo.platform_repo_id ?? "",
      repo_path: identity.repo.repo_path,
    };
    const existing = next.findIndex((candidate) => candidate.value === value);
    if (existing >= 0) next[existing] = entry;
    else next.push(entry);
  }
  entries = next;
  ready = complete;
}
