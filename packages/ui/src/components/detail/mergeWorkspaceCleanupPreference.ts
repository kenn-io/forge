const STORAGE_KEY = "kenn-forge:merge:delete-workspace-after-merge";

function resolveStorage(storage?: Storage): Storage | undefined {
  if (storage) return storage;
  try {
    return globalThis.localStorage;
  } catch {
    return undefined;
  }
}

export function readDeleteWorkspaceAfterMergePreference(storage?: Storage): boolean {
  try {
    return resolveStorage(storage)?.getItem(STORAGE_KEY) !== "false";
  } catch {
    return true;
  }
}

export function writeDeleteWorkspaceAfterMergePreference(value: boolean, storage?: Storage): void {
  try {
    resolveStorage(storage)?.setItem(STORAGE_KEY, String(value));
  } catch {
    // Storage can be unavailable in private browsing or embedded contexts.
  }
}
