import { readTriFilter, writeTriFilter, type TriFilter } from "../utils/tri-filter.js";

export type UnassignedSurface = "pulls" | "issues" | "activity";

const STORAGE_KEYS: Record<UnassignedSurface, string> = {
  pulls: "kenn-forge:filters:pulls:unassigned",
  issues: "kenn-forge:filters:issues:unassigned",
  activity: "kenn-forge:filters:activity:unassigned",
};

export function readUnassignedFilter(surface: UnassignedSurface): TriFilter {
  return readTriFilter(STORAGE_KEYS[surface]);
}

export function writeUnassignedFilter(surface: UnassignedSurface, value: TriFilter): void {
  writeTriFilter(STORAGE_KEYS[surface], value);
}

export function unassignedFilterStorageKey(surface: UnassignedSurface): string {
  return STORAGE_KEYS[surface];
}
