import { readTriFilter, writeTriFilter, type TriFilter } from "../utils/tri-filter.js";

const STORAGE_KEY = "kenn-forge:filters:issues:referenced-by-pr";

export function readIssuePRReferenceFilter(): TriFilter {
  return readTriFilter(STORAGE_KEY);
}

export function writeIssuePRReferenceFilter(value: TriFilter): void {
  writeTriFilter(STORAGE_KEY, value);
}

export function issuePRReferenceFilterStorageKey(): string {
  return STORAGE_KEY;
}
