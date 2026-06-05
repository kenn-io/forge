const STORAGE_KEY = "middleman:repoTreeCollapsed";

function readFromStorage(): Set<string> {
  try {
    const raw = localStorage.getItem(STORAGE_KEY);
    if (raw === null) return new Set();
    const parsed: unknown = JSON.parse(raw);
    if (!Array.isArray(parsed)) return new Set();
    return new Set(parsed.filter((v): v is string => typeof v === "string"));
  } catch {
    // localStorage unavailable or corrupt JSON.
    return new Set();
  }
}

function writeToStorage(value: Set<string>): void {
  try {
    localStorage.setItem(STORAGE_KEY, JSON.stringify([...value]));
  } catch {
    // localStorage unavailable (e.g. private browsing quota).
  }
}

export function createRepoTreeExpansionStore() {
  let collapsed = $state<Set<string>>(readFromStorage());

  function isCollapsed(id: string): boolean {
    return collapsed.has(id);
  }

  function toggle(id: string): void {
    const next = new Set(collapsed);
    if (next.has(id)) next.delete(id);
    else next.add(id);
    collapsed = next;
    writeToStorage(next);
  }

  return { isCollapsed, toggle };
}

export type RepoTreeExpansionStore = ReturnType<typeof createRepoTreeExpansionStore>;
