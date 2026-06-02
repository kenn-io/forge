export type CollapseSurface = "pulls" | "issues";

const STORAGE_KEYS: Record<CollapseSurface, string> = {
  pulls: "middleman:collapsedRepos:pulls",
  issues: "middleman:collapsedRepos:issues",
};

function readFromStorage(surface: CollapseSurface): Set<string> {
  try {
    const raw = localStorage.getItem(STORAGE_KEYS[surface]);
    if (raw === null) return new Set();
    const parsed: unknown = JSON.parse(raw);
    if (!Array.isArray(parsed)) return new Set();
    return new Set(parsed.filter((v): v is string => typeof v === "string"));
  } catch {
    // localStorage unavailable or corrupt JSON.
    return new Set();
  }
}

function writeToStorage(surface: CollapseSurface, value: Set<string>): void {
  try {
    localStorage.setItem(STORAGE_KEYS[surface], JSON.stringify([...value]));
  } catch {
    // localStorage unavailable (e.g., private browsing quota).
  }
}

export function createCollapsedReposStore() {
  let collapsedInPulls = $state<Set<string>>(readFromStorage("pulls"));
  let collapsedInIssues = $state<Set<string>>(readFromStorage("issues"));

  function isCollapsed(surface: CollapseSurface, repoKey: string): boolean {
    if (surface === "pulls") return collapsedInPulls.has(repoKey);
    return collapsedInIssues.has(repoKey);
  }

  function toggle(surface: CollapseSurface, repoKey: string): void {
    if (surface === "pulls") {
      const next = new Set(collapsedInPulls);
      if (next.has(repoKey)) next.delete(repoKey);
      else next.add(repoKey);
      collapsedInPulls = next;
      writeToStorage("pulls", next);
    } else {
      const next = new Set(collapsedInIssues);
      if (next.has(repoKey)) next.delete(repoKey);
      else next.add(repoKey);
      collapsedInIssues = next;
      writeToStorage("issues", next);
    }
  }

  return {
    isCollapsed,
    toggle,
  };
}

export type CollapsedReposStore = ReturnType<typeof createCollapsedReposStore>;
