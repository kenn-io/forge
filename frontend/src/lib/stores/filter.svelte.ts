const STORAGE_KEY = "middleman-filter-repo";

export function parseRepoFilterValue(repo: string | undefined): string[] {
  return (repo ?? "")
    .split(",")
    .map((part) => part.trim())
    .filter((part) => part !== "");
}

export function serializeRepoFilterValue(repos: string[]): string | undefined {
  const unique = Array.from(
    new Set(repos.map((repo) => repo.trim()).filter((repo) => repo !== "")),
  );
  return unique.length > 0 ? unique.join(",") : undefined;
}

function loadPersistedRepo(): string | undefined {
  try {
    return serializeRepoFilterValue(
      parseRepoFilterValue(localStorage.getItem(STORAGE_KEY) || undefined),
    );
  } catch {
    return undefined;
  }
}

let filterRepo = $state<string | undefined>(loadPersistedRepo());

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

export function applyConfigRepo(
  repo: { owner: string; name: string } | undefined,
  hideSelector: boolean,
): void {
  if (hideSelector) {
    if (repo) {
      filterRepo = `${repo.owner}/${repo.name}`;
    } else {
      filterRepo = undefined;
    }
  }
}
