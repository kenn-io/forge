import type { ConfigRepo, Repo } from "../api/types.js";
import {
  canonicalRepoFilterValue,
  concreteRepoFilterValue,
  providerQualifiedRepoFilterLabel,
  repoFilterValueNeedsProvider,
} from "../utils/repo-filter-values.js";
import { unresolvedInteractiveConfigRepos } from "../utils/repo-visibility.js";

export interface MobileActivityRepoOption {
  value: string;
  label: string;
  triggerLabel?: string;
}

interface MobileActivityRepoIdentity {
  provider: string;
  platformHost: string;
  repoPath: string;
  isGlob: boolean;
}

type CatalogRepoIdentity = Pick<Repo, "Platform" | "PlatformHost" | "Owner" | "Name">;

function configRepoFilterIdentity(repo: ConfigRepo): MobileActivityRepoIdentity {
  return {
    provider: repo.provider,
    platformHost: repo.platform_host,
    repoPath: repo.repo_path,
    isGlob: repo.is_glob,
  };
}

function catalogRepoFilterIdentity(repo: CatalogRepoIdentity): MobileActivityRepoIdentity {
  return {
    provider: repo.Platform,
    platformHost: repo.PlatformHost,
    repoPath: `${repo.Owner}/${repo.Name}`,
    isGlob: false,
  };
}

export function buildMobileActivityRepoOptions(
  configuredRepos: ConfigRepo[],
  catalogRepos: CatalogRepoIdentity[],
): MobileActivityRepoOption[] {
  const interactiveRepos = [
    ...catalogRepos.map(catalogRepoFilterIdentity),
    ...unresolvedInteractiveConfigRepos(configuredRepos).map(configRepoFilterIdentity),
  ];
  const valuesByRepoPath = new Map<string, Set<string>>();
  for (const repo of interactiveRepos) {
    const value = concreteRepoFilterValue(repo);
    if (!value) continue;
    const repoPath = repo.repoPath.trim();
    let values = valuesByRepoPath.get(repoPath);
    if (!values) {
      values = new Set<string>();
      valuesByRepoPath.set(repoPath, values);
    }
    values.add(value);
  }

  const identities = interactiveRepos;
  const seen = new Set<string>();
  const options: MobileActivityRepoOption[] = [];
  for (const repo of interactiveRepos) {
    const identity = repo;
    const concreteValue = concreteRepoFilterValue(identity);
    if (!concreteValue) continue;
    const value = canonicalRepoFilterValue(identity, identities);
    if (!value || seen.has(value)) continue;
    seen.add(value);
    const repoPath = repo.repoPath.trim();
    const providerCollision = repoFilterValueNeedsProvider(identity, identities);
    const label = providerQualifiedRepoFilterLabel(identity);
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
