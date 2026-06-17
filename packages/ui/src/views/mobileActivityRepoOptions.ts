import type { ConfigRepo } from "../api/types.js";
import {
  canonicalRepoFilterValue,
  concreteRepoFilterValue,
  providerQualifiedRepoFilterLabel,
  repoFilterValueNeedsProvider,
} from "../utils/repo-filter-values.js";

export interface MobileActivityRepoOption {
  value: string;
  label: string;
  triggerLabel?: string;
}

function repoFilterIdentity(repo: ConfigRepo) {
  return {
    provider: repo.provider,
    platformHost: repo.platform_host,
    repoPath: repo.repo_path,
    isGlob: repo.is_glob,
  };
}

export function buildMobileActivityRepoOptions(repos: ConfigRepo[]): MobileActivityRepoOption[] {
  const valuesByRepoPath = new Map<string, Set<string>>();
  for (const repo of repos) {
    const value = concreteRepoFilterValue(repoFilterIdentity(repo));
    if (!value) continue;
    const repoPath = repo.repo_path.trim();
    let values = valuesByRepoPath.get(repoPath);
    if (!values) {
      values = new Set<string>();
      valuesByRepoPath.set(repoPath, values);
    }
    values.add(value);
  }

  const identities = repos.map(repoFilterIdentity);
  const seen = new Set<string>();
  const options: MobileActivityRepoOption[] = [];
  for (const repo of repos) {
    const identity = repoFilterIdentity(repo);
    const concreteValue = concreteRepoFilterValue(identity);
    if (!concreteValue) continue;
    const providerCollision = repoFilterValueNeedsProvider(identity, identities);
    const value = canonicalRepoFilterValue(identity, identities);
    if (!value || seen.has(value)) continue;
    seen.add(value);
    const repoPath = repo.repo_path.trim();
    const label = providerCollision ? providerQualifiedRepoFilterLabel(identity) : value;
    if (!label) continue;
    const triggerLabel = providerCollision || (valuesByRepoPath.get(repoPath)?.size ?? 0) > 1 ? label : repoPath;
    options.push({ value, label, triggerLabel });
  }
  return options.sort((left, right) =>
    left.label.localeCompare(right.label, undefined, {
      sensitivity: "base",
      numeric: true,
    }),
  );
}
