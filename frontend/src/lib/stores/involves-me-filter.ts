export type InvolvesMeSurface = "pulls" | "issues" | "activity";

const STORAGE_KEYS: Record<InvolvesMeSurface, string> = {
  pulls: "kenn-forge:filters:pulls:involves-me",
  issues: "kenn-forge:filters:issues:involves-me",
  activity: "kenn-forge:filters:activity:involves-me",
};

export function readInvolvesMeFilter(surface: InvolvesMeSurface): boolean {
  try {
    return globalThis.localStorage?.getItem(STORAGE_KEYS[surface]) === "1";
  } catch {
    return false;
  }
}

export function writeInvolvesMeFilter(surface: InvolvesMeSurface, enabled: boolean): void {
  try {
    if (enabled) globalThis.localStorage?.setItem(STORAGE_KEYS[surface], "1");
    else globalThis.localStorage?.removeItem(STORAGE_KEYS[surface]);
  } catch {
    // localStorage can be unavailable in restricted browser contexts.
  }
}

export function involvesMeFilterStorageKey(surface: InvolvesMeSurface): string {
  return STORAGE_KEYS[surface];
}
