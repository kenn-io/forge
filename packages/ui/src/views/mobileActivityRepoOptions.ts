import type { ConfigRepo } from "../api/types.js";

export interface MobileActivityRepoOption {
  value: string;
  label: string;
  triggerLabel?: string;
}

function concreteRepoSelectorValue(repo: ConfigRepo): string | null {
  const repoPath = repo.repo_path?.trim();
  const platformHost = repo.platform_host?.trim();
  if (!repoPath || !platformHost || repo.is_glob) return null;
  return `${platformHost}/${repoPath}`;
}

function providerRepoSelectorValue(repo: ConfigRepo): string | null {
  const provider = repo.provider?.trim();
  const concreteValue = concreteRepoSelectorValue(repo);
  return provider && concreteValue ? `${provider}/${concreteValue}` : concreteValue;
}

export function buildMobileActivityRepoOptions(repos: ConfigRepo[]): MobileActivityRepoOption[] {
  const valuesByRepoPath = new Map<string, Set<string>>();
  const providersByConcreteValue = new Map<string, Set<string>>();
  for (const repo of repos) {
    const value = concreteRepoSelectorValue(repo);
    if (!value) continue;
    const repoPath = repo.repo_path.trim();
    let values = valuesByRepoPath.get(repoPath);
    if (!values) {
      values = new Set<string>();
      valuesByRepoPath.set(repoPath, values);
    }
    values.add(value);

    let providers = providersByConcreteValue.get(value);
    if (!providers) {
      providers = new Set<string>();
      providersByConcreteValue.set(value, providers);
    }
    providers.add(repo.provider.trim());
  }

  const seen = new Set<string>();
  const options: MobileActivityRepoOption[] = [];
  for (const repo of repos) {
    const concreteValue = concreteRepoSelectorValue(repo);
    if (!concreteValue) continue;
    const providerCollision = (providersByConcreteValue.get(concreteValue)?.size ?? 0) > 1;
    const value = providerCollision ? providerRepoSelectorValue(repo) : concreteValue;
    if (!value || seen.has(value)) continue;
    seen.add(value);
    const repoPath = repo.repo_path.trim();
    const triggerLabel = providerCollision || (valuesByRepoPath.get(repoPath)?.size ?? 0) > 1 ? value : repoPath;
    options.push({ value, label: value, triggerLabel });
  }
  return options.sort((left, right) =>
    left.label.localeCompare(right.label, undefined, {
      sensitivity: "base",
      numeric: true,
    }),
  );
}
