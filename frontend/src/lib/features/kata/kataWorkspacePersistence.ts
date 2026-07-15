import type { KataTaskSearchFilters, KataTaskViewName } from "../../api/kata/taskTypes.js";

export const KATA_WORKSPACE_STATE_STORAGE_KEY = "middleman:kata:workspace-state/v1";

export interface KataPersistedWorkspaceState {
  view: KataTaskViewName;
  filters: KataTaskSearchFilters;
  selectedIssueUID: string | null;
}

interface StoredKataWorkspaceState {
  version: 1;
  daemons: Record<string, KataPersistedWorkspaceState>;
}

type StorageReadResult = { kind: "state"; state: StoredKataWorkspaceState } | { kind: "reset" } | { kind: "failed" };

const viewNames = new Set<KataTaskViewName>(["inbox", "today", "upcoming", "deadlines", "all", "logbook"]);
const statusFilters = new Set<KataTaskSearchFilters["status"]>(["open", "closed", "all"]);

function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === "object" && value !== null && !Array.isArray(value);
}

function createDaemonMap(): Record<string, KataPersistedWorkspaceState> {
  return Object.create(null) as Record<string, KataPersistedWorkspaceState>;
}

function hasDaemon(map: Record<string, KataPersistedWorkspaceState>, daemonID: string): boolean {
  return Object.prototype.hasOwnProperty.call(map, daemonID);
}

function cloneState(state: KataPersistedWorkspaceState): KataPersistedWorkspaceState {
  return {
    view: state.view,
    filters: {
      scope:
        state.filters.scope.kind === "all"
          ? { kind: "all" }
          : { kind: "project", project_uid: state.filters.scope.project_uid },
      status: state.filters.status,
      owner: state.filters.owner,
      label: state.filters.label,
      query: state.filters.query,
    },
    selectedIssueUID: state.selectedIssueUID,
  };
}

function parseState(value: unknown): KataPersistedWorkspaceState | null {
  if (!isRecord(value) || !viewNames.has(value.view as KataTaskViewName) || !isRecord(value.filters)) {
    return null;
  }

  const { filters } = value;
  if (
    !statusFilters.has(filters.status as KataTaskSearchFilters["status"]) ||
    typeof filters.owner !== "string" ||
    typeof filters.label !== "string" ||
    typeof filters.query !== "string" ||
    (typeof value.selectedIssueUID !== "string" && value.selectedIssueUID !== null)
  ) {
    return null;
  }

  const scope = filters.scope;
  if (!isRecord(scope) || (scope.kind !== "all" && scope.kind !== "project")) {
    return null;
  }
  if (scope.kind === "project" && (typeof scope.project_uid !== "string" || scope.project_uid.length === 0)) {
    return null;
  }

  return {
    view: value.view as KataTaskViewName,
    filters: {
      scope: scope.kind === "all" ? { kind: "all" } : { kind: "project", project_uid: scope.project_uid as string },
      status: filters.status as KataTaskSearchFilters["status"],
      owner: filters.owner,
      label: filters.label,
      query: filters.query,
    },
    selectedIssueUID: value.selectedIssueUID,
  };
}

function parseStorage(value: unknown): StoredKataWorkspaceState | null {
  if (!isRecord(value) || value.version !== 1 || !isRecord(value.daemons)) {
    return null;
  }

  const daemons = createDaemonMap();
  for (const [daemonID, state] of Object.entries(value.daemons)) {
    const parsed = parseState(state);
    if (parsed) {
      daemons[daemonID] = parsed;
    }
  }

  return { version: 1, daemons };
}

function readStorage(): StorageReadResult {
  if (typeof window === "undefined") {
    return { kind: "failed" };
  }

  let raw: string | null;
  try {
    raw = window.localStorage.getItem(KATA_WORKSPACE_STATE_STORAGE_KEY);
  } catch {
    return { kind: "failed" };
  }

  if (raw === null) {
    return { kind: "state", state: { version: 1, daemons: createDaemonMap() } };
  }

  try {
    const state = parseStorage(JSON.parse(raw));
    // A corrupt or unsupported top-level value is reset only by a later valid save.
    return state ? { kind: "state", state } : { kind: "reset" };
  } catch {
    return { kind: "reset" };
  }
}

function writeStorage(state: StoredKataWorkspaceState): void {
  if (typeof window === "undefined") {
    return;
  }

  try {
    window.localStorage.setItem(KATA_WORKSPACE_STATE_STORAGE_KEY, JSON.stringify(state));
  } catch {
    // Local storage can be unavailable or full; persistence must not disrupt the workspace.
  }
}

function copyDaemons(
  daemons: Record<string, KataPersistedWorkspaceState>,
): Record<string, KataPersistedWorkspaceState> {
  const copy = createDaemonMap();
  for (const [daemonID, state] of Object.entries(daemons)) {
    copy[daemonID] = cloneState(state);
  }
  return copy;
}

export function loadKataWorkspaceState(daemonID: string): KataPersistedWorkspaceState | null {
  if (daemonID.length === 0) {
    return null;
  }

  const result = readStorage();
  if (result.kind !== "state" || !hasDaemon(result.state.daemons, daemonID)) {
    return null;
  }

  const state = result.state.daemons[daemonID];
  return state ? cloneState(state) : null;
}

export function saveKataWorkspaceState(daemonID: string, state: KataPersistedWorkspaceState): void {
  const parsed = parseState(state);
  if (daemonID.length === 0 || !parsed) {
    return;
  }

  const result = readStorage();
  if (result.kind === "failed") {
    return;
  }

  // Whole-map writes are intentionally last-writer-wins; a valid save resets corrupt or unsupported top-level data.
  const daemons = result.kind === "state" ? copyDaemons(result.state.daemons) : createDaemonMap();
  daemons[daemonID] = cloneState(parsed);
  writeStorage({ version: 1, daemons });
}

export function clearKataWorkspaceSelection(daemonID: string): void {
  if (daemonID.length === 0) {
    return;
  }

  const result = readStorage();
  if (result.kind !== "state" || !hasDaemon(result.state.daemons, daemonID)) {
    return;
  }

  // Whole-map writes are intentionally last-writer-wins; orphan IDs persist until explicitly cleared.
  const daemons = copyDaemons(result.state.daemons);
  const state = daemons[daemonID];
  if (!state) {
    return;
  }
  daemons[daemonID] = { ...state, selectedIssueUID: null };
  writeStorage({ version: 1, daemons });
}

export function clearKataWorkspaceState(daemonID: string): void {
  if (daemonID.length === 0) {
    return;
  }

  const result = readStorage();
  if (result.kind !== "state") {
    return;
  }

  const daemons = copyDaemons(result.state.daemons);
  if (hasDaemon(daemons, daemonID)) {
    delete daemons[daemonID];
  }
  // Whole-map writes are intentionally last-writer-wins; orphan IDs persist until explicitly cleared.
  writeStorage({ version: 1, daemons });
}
