import type { PullRequest } from "../api/types.js";
import { bucketCIChecks, parseCIChecks } from "./ci-buckets.js";

/** A pull request property the PR list and worklists filter on. */
export type PullAttributeFilter =
  | "approved"
  | "draft"
  | "ready"
  | "merge_conflicts"
  | "failed_ci"
  | "passed_ci"
  | "has_workspace";

/**
 * Whether the selection matches. Each section holds mutually exclusive options and
 * matches any one selected; every section with a selection must match.
 */
export function matchesFilterSections<T>(
  sections: readonly (readonly T[])[],
  selected: readonly T[],
  matches: (filter: T) => boolean,
): boolean {
  return sections.every((section) => {
    const chosen = section.filter((filter) => selected.includes(filter));
    return chosen.length === 0 || chosen.some(matches);
  });
}

/**
 * Toggles a filter. Selecting an option clears the others in its section, so the
 * menu offers each mutually exclusive pair as a pick-one choice.
 */
export function toggleFilterInSections<T>(sections: readonly (readonly T[])[], selected: readonly T[], filter: T): T[] {
  if (selected.includes(filter)) return selected.filter((item) => item !== filter);
  const siblings = sections.find((section) => section.includes(filter)) ?? [];
  return [...selected.filter((item) => !siblings.includes(item)), filter];
}

/** The PR list's attribute sections, matched as `matchesFilterSections` describes. */
export const PULL_ATTRIBUTE_SECTIONS: readonly (readonly PullAttributeFilter[])[] = [
  ["draft", "ready"],
  ["approved"],
  ["passed_ci", "failed_ci"],
  ["merge_conflicts"],
  ["has_workspace"],
];

export function pullMatchesAttributeFilters(pr: PullRequest, filters: readonly PullAttributeFilter[]): boolean {
  return matchesFilterSections(PULL_ATTRIBUTE_SECTIONS, filters, (filter) => pullMatchesAttribute(pr, filter));
}

export function pullMatchesAttribute(pr: PullRequest, filter: PullAttributeFilter): boolean {
  if (filter === "approved") {
    return pr.ReviewDecision.trim().toUpperCase() === "APPROVED";
  }
  if (filter === "draft") {
    return pr.IsDraft;
  }
  if (filter === "ready") {
    return pr.State === "open" && !pr.IsDraft;
  }
  if (filter === "merge_conflicts") {
    return pr.MergeableState === "dirty";
  }
  if (filter === "failed_ci") {
    return hasFailedCI(pr);
  }
  if (filter === "passed_ci") {
    return hasPassedCI(pr);
  }
  return pr.workspace !== undefined;
}

function hasFailedCI(pr: PullRequest): boolean {
  const status = pr.CIStatus.trim().toLowerCase();
  if (status === "failure" || status === "failed" || status === "error") {
    return true;
  }
  const parsed = parseCIChecks(pr.CIChecksJSON);
  if (parsed.error !== null) return false;
  return bucketCIChecks(parsed.checks).failed.length > 0;
}

function hasPassedCI(pr: PullRequest): boolean {
  const status = pr.CIStatus.trim().toLowerCase();
  if (status !== "" && status !== "success") return false;
  const parsed = parseCIChecks(pr.CIChecksJSON);
  if (parsed.error !== null || (status === "" && parsed.checks.length === 0)) return false;
  const buckets = bucketCIChecks(parsed.checks);
  return buckets.failed.length === 0 && buckets.pending.length === 0 && buckets.unknown.length === 0;
}
