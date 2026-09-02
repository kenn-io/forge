export type UnassignedSurface = "pulls" | "issues" | "activity";

const STORAGE_KEYS: Record<UnassignedSurface, string> = {
  pulls: "kenn-forge:filters:pulls:unassigned",
  issues: "kenn-forge:filters:issues:unassigned",
  activity: "kenn-forge:filters:activity:unassigned",
};

export function readUnassignedFilter(surface: UnassignedSurface): boolean {
  try {
    return globalThis.localStorage?.getItem(STORAGE_KEYS[surface]) === "1";
  } catch {
    return false;
  }
}

export function writeUnassignedFilter(surface: UnassignedSurface, enabled: boolean): void {
  try {
    if (enabled) globalThis.localStorage?.setItem(STORAGE_KEYS[surface], "1");
    else globalThis.localStorage?.removeItem(STORAGE_KEYS[surface]);
  } catch {
    // localStorage can be unavailable in restricted browser contexts.
  }
}

export function unassignedFilterStorageKey(surface: UnassignedSurface): string {
  return STORAGE_KEYS[surface];
}
