import { canonicalProvider } from "../api/provider-routes.js";
import type { ConfigRepo } from "../api/types.js";

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
  return provider && concreteValue ? `${provider}|${concreteValue}` : null;
}

export function providerQualifiedRepoFilterLabel(repo: RepoFilterIdentity): string | null {
  const provider = normalizeProvider(repo.provider);
  const concreteValue = concreteRepoFilterValue(repo);
  return provider && concreteValue ? `${provider}/${concreteValue}` : null;
}

export function repoFilterValueNeedsProvider(repo: RepoFilterIdentity, repos: readonly RepoFilterIdentity[]): boolean {
  const concreteValue = concreteRepoFilterValue(repo);
  if (!concreteValue) return false;
  return (providerCountsByConcreteValue(repos).get(concreteValue)?.size ?? 0) > 1;
}

export function canonicalRepoFilterValue(
  repo: RepoFilterIdentity,
  _repos: readonly RepoFilterIdentity[],
): string | null {
  return providerQualifiedRepoFilterValue(repo);
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
  if (pipeSeparator === -1) {
    return "";
  }

  const provider = normalizeProvider(value.slice(0, pipeSeparator));
  const concreteValue = value.slice(pipeSeparator + 1);
  for (const identity of concreteIdentities(repos)) {
    if (identity.provider !== provider || identity.concreteValue !== concreteValue) continue;
    return canonicalRepoFilterValue(identity.repo, repos) ?? value;
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

// interactiveRepoFilterIdentities feeds selectors with the entries a user can
// still pick: server-hidden repositories drop out.
export function interactiveRepoFilterIdentities(repos: readonly ConfigRepo[]): RepoFilterIdentity[] {
  const identities: RepoFilterIdentity[] = [];
  for (const repo of repos) {
    if (repo.hidden_from_ui) continue;
    identities.push({
      provider: repo.provider,
      platformHost: repo.platform_host,
      repoPath: repo.repo_path,
      isGlob: repo.is_glob,
    });
  }
  return identities;
}

function canonicalSelectionValue(value: string): string {
  const separator = value.indexOf("|");
  if (separator === -1) return value;
  return `${normalizeProvider(value.slice(0, separator))}|${value.slice(separator + 1)}`;
}

// normalizeInteractiveRepoFilterSelection normalizes a stored selection
// against the repositories a user can still pick. Selections naming a
// server-hidden exact repository are dropped explicitly: general
// normalization preserves unknown provider-qualified values because
// glob-resolved repositories have no configured entry of their own, so a
// hidden repo would otherwise keep filtering after vanishing from pickers.
export function normalizeInteractiveRepoFilterSelection(
  selected: string | undefined,
  repos: readonly ConfigRepo[],
): string | undefined {
  const hidden = new Set<string>();
  for (const repo of repos) {
    if (!repo.hidden_from_ui || repo.is_glob) continue;
    // Selections created from catalog rows use the provider-verified current
    // route, which diverges from the configured path after a rename — clear
    // against both.
    for (const repoPath of [repo.repo_path, repo.tracked_repo_path]) {
      if (!repoPath) continue;
      const value = providerQualifiedRepoFilterValue({
        provider: repo.provider,
        platformHost: repo.platform_host,
        repoPath,
        isGlob: repo.is_glob,
      });
      if (value) hidden.add(value);
    }
  }
  const remaining = parseRepoFilterSelection(selected).filter((value) => !hidden.has(canonicalSelectionValue(value)));
  return normalizeRepoFilterSelection(serializeRepoFilterSelection(remaining), interactiveRepoFilterIdentities(repos));
}
