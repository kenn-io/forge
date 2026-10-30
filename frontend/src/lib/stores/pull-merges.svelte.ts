import { SvelteSet } from "svelte/reactivity";
import { canonicalProvider, resolvedPlatformHost, type ProviderRouteRef } from "../api/provider-routes.js";
import { repositoryKeyString } from "../api/repository-key.js";

// Pull requests with a merge request in flight, whichever surface started it
// (the detail view or a row menu). Merges outlive the view that started them,
// so the state is app-wide: every row, detail, and workspace showing the PR
// marks it merging, and none of them starts a second merge.
const merging = new SvelteSet<string>();

export type PullMergeRef = Pick<ProviderRouteRef, "provider" | "platformHost" | "repositoryKey">;

/** Identifies a pull request by verified repository identity; undefined when the repository is unverified. */
export function pullMergeKey(ref: PullMergeRef, number: number): string | undefined {
  const repositoryKey = ref.repositoryKey;
  if (!repositoryKey) return undefined;
  return JSON.stringify([
    canonicalProvider(ref.provider),
    resolvedPlatformHost(ref.provider, ref.platformHost),
    repositoryKeyString(repositoryKey),
    number,
  ]);
}

export function isPullMerging(ref: PullMergeRef, number: number): boolean {
  const key = pullMergeKey(ref, number);
  return key !== undefined && merging.has(key);
}

/** Marks a pull request merging; false when a merge is already in flight. */
export function startPullMerge(key: string): boolean {
  if (merging.has(key)) return false;
  merging.add(key);
  return true;
}

export function finishPullMerge(key: string): void {
  merging.delete(key);
}
