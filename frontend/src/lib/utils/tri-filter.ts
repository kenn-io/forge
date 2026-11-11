import type { LabelFilterMode } from "./label-filter.js";

/** A filter that requires its condition, excludes it, or is off (undefined). */
export type TriFilter = LabelFilterMode | undefined;

/** Off, then required, then excluded, then off again, as label filters cycle. */
export function cycleTriFilter(value: TriFilter): TriFilter {
  if (value === undefined) return "require";
  return value === "require" ? "exclude" : undefined;
}

/** The filter menu row state: a tick when required, a cross when excluded. */
export function triFilterRow(value: TriFilter): { active: boolean; excluded: boolean } {
  return { active: value === "require", excluded: value === "exclude" };
}

/** Reads a browser-local filter; "1" is the value older versions wrote for an enabled filter. */
export function readTriFilter(storageKey: string): TriFilter {
  try {
    const stored = globalThis.localStorage?.getItem(storageKey);
    if (stored === "1") return "require";
    return stored === "0" ? "exclude" : undefined;
  } catch {
    return undefined;
  }
}

export function writeTriFilter(storageKey: string, value: TriFilter): void {
  try {
    if (value === undefined) globalThis.localStorage?.removeItem(storageKey);
    else globalThis.localStorage?.setItem(storageKey, value === "require" ? "1" : "0");
  } catch {
    // localStorage can be unavailable in restricted browser contexts.
  }
}
