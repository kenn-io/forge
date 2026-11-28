import type { RepoPreset } from "../api/types.js";
import type { RepoPresetRepository as GeneratedRepoPresetRepository } from "../api/generated/models/index.js";
import {
  repositoryKeyFromWire,
  repositoryKeyToRequiredWire,
  sameRepositoryKey,
  type RepositoryKey,
} from "../api/repository-key.js";
import { parseRepoFilterValue, serializeRepoFilterValue } from "./filter.svelte.js";

export type RepoPresetRepository = GeneratedRepoPresetRepository;

export interface RepoPresetCatalogEntry {
  value: string;
  provider: string;
  platform_host: string;
  repo_path: string;
  repositoryKey: RepositoryKey | undefined;
  private?: boolean | undefined;
}

function selectionKey(repos: readonly string[]): string {
  return [...new Set(repos)].sort().join("\n");
}

export function findMatchingRepoPreset(
  presets: readonly RepoPreset[],
  selected: string | undefined,
  availableRepos: readonly RepoPresetCatalogEntry[],
  affinity?: string | undefined,
): RepoPreset | undefined {
  const selectedRepos = parseRepoFilterValue(selected);
  if (selectedRepos.length === 0) return undefined;
  const selectedKey = selectionKey(selectedRepos);
  const matching = presets.filter(
    (preset) => selectionKey(parseRepoFilterValue(projectRepoPresetSelection(preset, availableRepos))) === selectedKey,
  );
  if (matching.length < 2 || !affinity) return matching[0];
  return matching.find((preset) => preset.name.toLowerCase() === affinity.toLowerCase()) ?? matching[0];
}

export function projectRepoPresetSelection(
  preset: Pick<RepoPreset, "repos">,
  availableRepos: readonly RepoPresetCatalogEntry[],
): string | undefined {
  const values = preset.repos.flatMap((repo) => {
    const key = repositoryKeyFromWire(repo);
    if (!key) return [];
    const match = availableRepos.find(
      (candidate) =>
        candidate.provider === repo.provider &&
        candidate.platform_host === repo.platform_host &&
        sameRepositoryKey(candidate.repositoryKey, key),
    );
    return match ? [match.value] : [];
  });
  if (values.length === 0) return undefined;
  return serializeRepoFilterValue(values);
}

export function repoPresetRepositoriesForSelection(
  selected: string | undefined,
  availableRepos: readonly RepoPresetCatalogEntry[],
): RepoPresetRepository[] | undefined {
  const values = parseRepoFilterValue(selected);
  const repos = values.map((value) => availableRepos.find((repo) => repo.value === value));
  if (repos.some((repo) => !repo?.repositoryKey)) return undefined;
  return repos.map((repo) => {
    if (!repo?.repositoryKey) throw new Error("repository catalog changed during preset save");
    return {
      provider: repo.provider,
      platform_host: repo.platform_host,
      ...repositoryKeyToRequiredWire(repo.repositoryKey),
      repo_path: repo.repo_path,
    };
  });
}

export function preferredRepoPreset(
  presets: readonly RepoPreset[],
  selected: string | undefined,
  affinity: string | undefined,
  availableRepos: readonly RepoPresetCatalogEntry[],
): RepoPreset | undefined {
  const matching = findMatchingRepoPreset(presets, selected, availableRepos, affinity);
  if (matching) return matching;
  if (!affinity) return undefined;
  const affinityKey = affinity.toLowerCase();
  return presets.find((preset) => preset.name.toLowerCase() === affinityKey);
}

export interface RepoTeamOrg {
  platform_host: string;
  owner: string;
}

// The GitHub organizations a team lookup may target: the distinct host and
// owner pairs of configured GitHub repositories, globs included. The server
// rejects any pair outside its configuration before calling GitHub.
export function githubTeamOrgs(
  configured: readonly { provider: string; platform_host: string; owner: string }[],
): RepoTeamOrg[] {
  const orgs = new Map<string, RepoTeamOrg>();
  for (const repo of configured) {
    if (repo.provider !== "github" || !repo.platform_host || !repo.owner) continue;
    const key = `${repo.platform_host.toLowerCase()}/${repo.owner.toLowerCase()}`;
    if (!orgs.has(key)) orgs.set(key, { platform_host: repo.platform_host, owner: repo.owner });
  }
  return [...orgs.values()];
}

// Maps a team's repositories to selector values through the preset projection,
// so they match by stable GitHub ID on the same host and survive a rename.
export function selectionForTeamRepositories(
  teamRepos: readonly { platform_repo_id: number; repo_path: string }[],
  platformHost: string,
  availableRepos: readonly RepoPresetCatalogEntry[],
): string | undefined {
  return projectRepoPresetSelection(
    {
      repos: teamRepos.map((repo) => ({
        provider: "github",
        platform_host: platformHost,
        platform_repo_id: repo.platform_repo_id,
        repo_path: repo.repo_path,
      })),
    },
    availableRepos,
  );
}
