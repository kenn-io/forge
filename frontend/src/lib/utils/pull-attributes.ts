import type { PullRequest } from "../api/types.js";
import { bucketCIChecks, parseCIChecks } from "./ci-buckets.js";
import type { TriFilter } from "./tri-filter.js";

/** A pull request property the PR list and worklists filter on. */
export type PullAttributeFilter =
  | "approved"
  | "not_approved"
  | "draft"
  | "not_draft"
  | "ready"
  | "merge_conflicts"
  | "no_merge_conflicts"
  | "failed_ci"
  | "passed_ci"
  | "roborev_findings"
  | "roborev_clean"
  | "has_workspace"
  | "no_workspace"
  | "merge_queued"
  | "not_merge_queued"
  | "fork"
  | "not_fork";

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

/**
 * Cycles a filter whose section pairs it with its opposite: off, then the filter,
 * then its opposite, then off again.
 */
export function cycleFilterInSections<T>(
  sections: readonly (readonly T[])[],
  selected: readonly T[],
  filter: T,
  opposite: T,
): T[] {
  if (selected.includes(opposite)) return selected.filter((item) => item !== opposite);
  return toggleFilterInSections(sections, selected, selected.includes(filter) ? opposite : filter);
}

/** Which of a filter and its opposite is selected. */
export function triFilterInSelection<T>(selected: readonly T[], filter: T, opposite: T): TriFilter {
  if (selected.includes(filter)) return "require";
  return selected.includes(opposite) ? "exclude" : undefined;
}

/** The PR list's attribute sections, matched as `matchesFilterSections` describes. */
export const PULL_ATTRIBUTE_SECTIONS: readonly (readonly PullAttributeFilter[])[] = [
  ["draft", "not_draft", "ready"],
  ["approved", "not_approved"],
  ["passed_ci", "failed_ci"],
  ["roborev_clean", "roborev_findings"],
  ["merge_conflicts", "no_merge_conflicts"],
  ["merge_queued", "not_merge_queued"],
  ["has_workspace", "no_workspace"],
  ["fork", "not_fork"],
];

export function pullMatchesAttributeFilters(pr: PullRequest, filters: readonly PullAttributeFilter[]): boolean {
  return matchesFilterSections(PULL_ATTRIBUTE_SECTIONS, filters, (filter) => pullMatchesAttribute(pr, filter));
}

export function pullMatchesAttribute(pr: PullRequest, filter: PullAttributeFilter): boolean {
  if (filter === "not_approved") return !pullMatchesAttribute(pr, "approved");
  if (filter === "no_merge_conflicts") return !pullMatchesAttribute(pr, "merge_conflicts");
  if (filter === "no_workspace") return !pullMatchesAttribute(pr, "has_workspace");
  if (filter === "not_merge_queued") return !pullMatchesAttribute(pr, "merge_queued");
  if (filter === "not_fork") return !pullMatchesAttribute(pr, "fork");
  if (filter === "not_draft") return !pr.IsDraft;
  if (filter === "fork") return pr.head_repo_kind === "fork";
  if (filter === "merge_queued") {
    // In the provider merge queue, or set to merge once CI passes.
    return pr.State === "open" && (pr.merge_queue !== undefined || pr.deferred_merge_pending === true);
  }
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
  if (filter === "roborev_findings") {
    return pr.roborev_verdict === "findings";
  }
  if (filter === "roborev_clean") {
    return pr.roborev_verdict === "clean";
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
