const STORAGE_KEY = "middleman:kata:active_daemon";

function readStoredActiveDaemon(): string | undefined {
  try {
    return localStorage.getItem(STORAGE_KEY) ?? undefined;
  } catch {
    return undefined;
  }
}

let activeKataDaemon = $state<string | undefined>(readStoredActiveDaemon());
let kataDaemonRoster = $state.raw<string[]>([]);
let defaultKataDaemon = $state<string | undefined>(undefined);
let kataDaemonRosterLoaded = $state(false);

export function getActiveKataDaemon(): string | undefined {
  return activeKataDaemon;
}

export function getKataDaemonRoster(): string[] {
  return kataDaemonRoster;
}

export function getKataDaemonRosterLoaded(): boolean {
  return kataDaemonRosterLoaded;
}

export function getDefaultKataDaemon(): string | undefined {
  return defaultKataDaemon;
}

export function setActiveKataDaemon(id: string | undefined): void {
  activeKataDaemon = id;
  try {
    if (id) {
      localStorage.setItem(STORAGE_KEY, id);
    } else {
      localStorage.removeItem(STORAGE_KEY);
    }
  } catch {
    // Storage can be disabled; keep the in-memory selection.
  }
}

export function setKataDaemonRoster(ids: string[], defaultId: string | undefined): void {
  kataDaemonRoster = [...ids];
  defaultKataDaemon = defaultId;
  kataDaemonRosterLoaded = true;
  reconcileActiveKataDaemon(ids, defaultId);
}

export function resetKataDaemonRoster(): void {
  kataDaemonRoster = [];
  defaultKataDaemon = undefined;
  kataDaemonRosterLoaded = false;
}

export function reconcileActiveKataDaemon(ids: string[], defaultId: string | undefined): void {
  if (activeKataDaemon && !ids.includes(activeKataDaemon)) {
    setActiveKataDaemon(defaultId);
  }
}
