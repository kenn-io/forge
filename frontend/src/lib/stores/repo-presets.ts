import type { RepoPreset } from "../api/types.js";
import { parseRepoFilterValue, serializeRepoFilterValue } from "./filter.svelte.js";

function selectionKey(repos: readonly string[]): string {
  return [...new Set(repos)].sort().join("\n");
}

export function findMatchingRepoPreset(
  presets: readonly RepoPreset[],
  selected: string | undefined,
): RepoPreset | undefined {
  const selectedRepos = parseRepoFilterValue(selected);
  if (selectedRepos.length === 0) return undefined;
  const selectedKey = selectionKey(selectedRepos);
  return presets.find((preset) => selectionKey(preset.repos) === selectedKey);
}

export function projectRepoPresetSelection(preset: RepoPreset, availableRepos: readonly string[]): string | undefined {
  const available = new Set(availableRepos);
  return serializeRepoFilterValue(preset.repos.filter((repo) => available.has(repo)));
}

export function preferredRepoPreset(
  presets: readonly RepoPreset[],
  selected: string | undefined,
  affinity: string | undefined,
): RepoPreset | undefined {
  const matching = findMatchingRepoPreset(presets, selected);
  if (matching) return matching;
  if (!affinity) return undefined;
  const affinityKey = affinity.toLowerCase();
  return presets.find((preset) => preset.name.toLowerCase() === affinityKey);
}
