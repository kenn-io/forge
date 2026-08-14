const STORAGE_KEY = "kenn-forge-filter-repo";
const PRESET_STORAGE_KEY = "kenn-forge-filter-repo-preset";

export function parseRepoFilterValue(repo: string | undefined): string[] {
  return (repo ?? "")
    .split(",")
    .map((part) => part.trim())
    .filter((part) => part !== "");
}

export function serializeRepoFilterValue(repos: string[]): string | undefined {
  const unique = Array.from(new Set(repos.map((repo) => repo.trim()).filter((repo) => repo !== "")));
  return unique.length > 0 ? unique.join(",") : undefined;
}

function loadPersistedRepo(): string | undefined {
  try {
    return serializeRepoFilterValue(parseRepoFilterValue(localStorage.getItem(STORAGE_KEY) || undefined));
  } catch {
    return undefined;
  }
}

let filterRepo = $state<string | undefined>(loadPersistedRepo());
let filterRepoPresetAffinity = $state<string | undefined>(loadPersistedPresetAffinity());

function loadPersistedPresetAffinity(): string | undefined {
  try {
    return localStorage.getItem(PRESET_STORAGE_KEY)?.trim() || undefined;
  } catch {
    return undefined;
  }
}

function setPresetAffinity(name: string | undefined): void {
  const normalized = name?.trim() || undefined;
  filterRepoPresetAffinity = normalized;
  try {
    if (normalized) localStorage.setItem(PRESET_STORAGE_KEY, normalized);
    else localStorage.removeItem(PRESET_STORAGE_KEY);
  } catch {
    // Storage blocked — affinity still works for this session.
  }
}

export function getGlobalRepo(): string | undefined {
  return filterRepo;
}

export function setGlobalRepo(repo: string | undefined): void {
  const normalized = serializeRepoFilterValue(parseRepoFilterValue(repo));
  filterRepo = normalized;
  try {
    if (normalized !== undefined) {
      localStorage.setItem(STORAGE_KEY, normalized);
    } else {
      localStorage.removeItem(STORAGE_KEY);
    }
  } catch {
    // Storage blocked — filter still works for this session
  }
}

export function getGlobalRepoPresetAffinity(): string | undefined {
  return filterRepoPresetAffinity;
}

export function setGlobalRepoPresetSelection(name: string | undefined, repo: string | undefined): void {
  setPresetAffinity(name);
  setGlobalRepo(repo);
}

export function clearGlobalRepoPresetAffinity(name?: string): void {
  if (name !== undefined && filterRepoPresetAffinity?.toLowerCase() !== name.trim().toLowerCase()) return;
  setPresetAffinity(undefined);
}

export function applyConfigRepo(
  repo:
    | {
        provider?: string;
        host?: string;
        platform_host?: string;
        repo_path?: string;
        owner?: string;
        name?: string;
      }
    | undefined,
  hideSelector: boolean,
): void {
  if (hideSelector) {
    setPresetAffinity(undefined);
    const provider = repo?.provider?.trim();
    const host = (repo?.platform_host ?? repo?.host)?.trim();
    const repoPath = (repo?.repo_path ?? (repo?.owner && repo.name ? `${repo.owner}/${repo.name}` : "")).trim();
    if (provider && host && repoPath) {
      filterRepo = `${provider}|${host}/${repoPath}`;
    } else {
      filterRepo = undefined;
    }
  }
}
