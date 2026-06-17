import { canonicalProvider } from "../api/provider-routes.js";

export interface RepoFilterIdentity {
  provider?: string | null;
  platformHost?: string | null;
  repoPath?: string | null;
  isGlob?: boolean | null;
}

function normalizeProvider(provider: string | null | undefined): string {
  const trimmed = provider?.trim() ?? "";
  return trimmed ? canonicalProvider(trimmed) : "";
}

function concreteIdentities(repos: readonly RepoFilterIdentity[]) {
  return repos
    .map((repo) => {
      const concreteValue = concreteRepoFilterValue(repo);
      if (!concreteValue) return null;
      return {
        repo,
        concreteValue,
        provider: normalizeProvider(repo.provider),
      };
    })
    .filter((entry): entry is NonNullable<typeof entry> => entry !== null);
}

function providerCountsByConcreteValue(repos: readonly RepoFilterIdentity[]): Map<string, Set<string>> {
  const providers = new Map<string, Set<string>>();
  for (const identity of concreteIdentities(repos)) {
    if (!identity.provider) continue;
    let values = providers.get(identity.concreteValue);
    if (!values) {
      values = new Set<string>();
      providers.set(identity.concreteValue, values);
    }
    values.add(identity.provider);
  }
  return providers;
}

export function concreteRepoFilterValue(repo: RepoFilterIdentity): string | null {
  const repoPath = repo.repoPath?.trim();
  const platformHost = repo.platformHost?.trim();
  if (!repoPath || !platformHost || repo.isGlob) return null;
  return `${platformHost}/${repoPath}`;
}

export function providerQualifiedRepoFilterValue(repo: RepoFilterIdentity): string | null {
  const provider = normalizeProvider(repo.provider);
  const concreteValue = concreteRepoFilterValue(repo);
  return provider && concreteValue ? `${provider}|${concreteValue}` : concreteValue;
}

export function providerQualifiedRepoFilterLabel(repo: RepoFilterIdentity): string | null {
  const provider = normalizeProvider(repo.provider);
  const concreteValue = concreteRepoFilterValue(repo);
  return provider && concreteValue ? `${provider}/${concreteValue}` : concreteValue;
}

export function repoFilterValueNeedsProvider(repo: RepoFilterIdentity, repos: readonly RepoFilterIdentity[]): boolean {
  const concreteValue = concreteRepoFilterValue(repo);
  if (!concreteValue) return false;
  return (providerCountsByConcreteValue(repos).get(concreteValue)?.size ?? 0) > 1;
}

export function canonicalRepoFilterValue(
  repo: RepoFilterIdentity,
  repos: readonly RepoFilterIdentity[],
): string | null {
  if (repoFilterValueNeedsProvider(repo, repos)) {
    return providerQualifiedRepoFilterValue(repo);
  }
  return concreteRepoFilterValue(repo);
}

export function displayRepoFilterValue(value: string): string {
  const separator = value.indexOf("|");
  if (separator === -1) return value;
  return `${value.slice(0, separator)}/${value.slice(separator + 1)}`;
}

function currentCanonicalValues(repos: readonly RepoFilterIdentity[]): Set<string> {
  const values = new Set<string>();
  for (const repo of repos) {
    const value = canonicalRepoFilterValue(repo, repos);
    if (value) values.add(value);
  }
  return values;
}

export function normalizeRepoFilterValue(selected: string, repos: readonly RepoFilterIdentity[]): string {
  const value = selected.trim();
  if (!value) return "";

  if (currentCanonicalValues(repos).has(value)) return value;

  const pipeSeparator = value.indexOf("|");
  if (pipeSeparator !== -1) {
    const provider = normalizeProvider(value.slice(0, pipeSeparator));
    const concreteValue = value.slice(pipeSeparator + 1);
    for (const identity of concreteIdentities(repos)) {
      if (identity.provider !== provider || identity.concreteValue !== concreteValue) continue;
      return canonicalRepoFilterValue(identity.repo, repos) ?? value;
    }
    return value;
  }

  for (const identity of concreteIdentities(repos)) {
    if (!identity.provider) continue;
    const legacyValue = `${identity.provider}/${identity.concreteValue}`;
    if (legacyValue === value) {
      return canonicalRepoFilterValue(identity.repo, repos) ?? value;
    }
  }
  return value;
}

export function parseRepoFilterSelection(selected: string | undefined): string[] {
  return (selected ?? "")
    .split(",")
    .map((part) => part.trim())
    .filter((part) => part !== "");
}

export function serializeRepoFilterSelection(values: readonly string[]): string | undefined {
  const unique = Array.from(new Set(values.map((value) => value.trim()).filter((value) => value !== "")));
  return unique.length > 0 ? unique.join(",") : undefined;
}

export function normalizeRepoFilterSelection(
  selected: string | undefined,
  repos: readonly RepoFilterIdentity[],
): string | undefined {
  return serializeRepoFilterSelection(
    parseRepoFilterSelection(selected).map((value) => normalizeRepoFilterValue(value, repos)),
  );
}
