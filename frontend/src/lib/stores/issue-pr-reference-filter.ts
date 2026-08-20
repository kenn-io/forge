const STORAGE_KEY = "kenn-forge:filters:issues:referenced-by-pr";

export function readIssuePRReferenceFilter(): boolean {
  try {
    return globalThis.localStorage?.getItem(STORAGE_KEY) === "1";
  } catch {
    return false;
  }
}

export function writeIssuePRReferenceFilter(enabled: boolean): void {
  try {
    if (enabled) globalThis.localStorage?.setItem(STORAGE_KEY, "1");
    else globalThis.localStorage?.removeItem(STORAGE_KEY);
  } catch {
    // localStorage can be unavailable in restricted browser contexts.
  }
}

export function issuePRReferenceFilterStorageKey(): string {
  return STORAGE_KEY;
}
