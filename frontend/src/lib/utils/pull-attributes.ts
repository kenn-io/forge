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
