import { readTriFilter, writeTriFilter, type TriFilter } from "../utils/tri-filter.js";

export type InvolvesMeSurface = "pulls" | "issues" | "activity";

const STORAGE_KEYS: Record<InvolvesMeSurface, string> = {
  pulls: "kenn-forge:filters:pulls:involves-me",
  issues: "kenn-forge:filters:issues:involves-me",
  activity: "kenn-forge:filters:activity:involves-me",
};

export function readInvolvesMeFilter(surface: InvolvesMeSurface): TriFilter {
  return readTriFilter(STORAGE_KEYS[surface]);
}

export function writeInvolvesMeFilter(surface: InvolvesMeSurface, value: TriFilter): void {
  writeTriFilter(STORAGE_KEYS[surface], value);
}

export function involvesMeFilterStorageKey(surface: InvolvesMeSurface): string {
  return STORAGE_KEYS[surface];
}
