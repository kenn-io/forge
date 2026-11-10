import { bucketCIChecks, type CIBucketedChecks } from "../../utils/ci-buckets.js";
import type { WorkspaceListItem } from "./workspace-list-schema.js";

export interface WorkspacePullStatus {
  readonly reviewDecision: string;
  readonly mergeAfterCI: boolean;
  readonly mergeQueued: boolean;
  readonly conflict: boolean;
  readonly commentCount: number;
  readonly checks: CIBucketedChecks;
  /** False when the row has no status icon to draw. */
  readonly hasIcons: boolean;
}

type PullStatusFields = Pick<
  WorkspaceListItem,
  | "associated_pr_number"
  | "item_type"
  | "mr_checks"
  | "mr_comment_count"
  | "mr_deferred_merge_pending"
  | "mr_merge_queued"
  | "mr_mergeable_state"
  | "mr_review_decision"
>;

/** Pull request status of a workspace's linked pull request, or null when it has none. */
export function workspacePullStatus(workspace: PullStatusFields): WorkspacePullStatus | null {
  if (workspace.item_type !== "pull_request" && workspace.associated_pr_number == null) return null;
  const reviewDecision = (workspace.mr_review_decision ?? "").trim().toUpperCase();
  const mergeAfterCI = workspace.mr_deferred_merge_pending === true;
  const mergeQueued = workspace.mr_merge_queued === true;
  const conflict = workspace.mr_mergeable_state === "dirty";
  const commentCount = workspace.mr_comment_count ?? 0;
  const checks = bucketCIChecks(
    (workspace.mr_checks ?? []).map((check) => ({
      app: "",
      name: check.name,
      status: check.status,
      conclusion: check.conclusion ?? "",
      url: check.url ?? "",
    })),
  );
  return {
    reviewDecision,
    mergeAfterCI,
    mergeQueued,
    conflict,
    commentCount,
    checks,
    hasIcons:
      reviewDecision === "APPROVED" ||
      reviewDecision === "CHANGES_REQUESTED" ||
      mergeAfterCI ||
      mergeQueued ||
      conflict ||
      commentCount > 0 ||
      checks.all.length > 0,
  };
}

export type WorkspaceStatusTone = "success" | "warning" | "danger" | "waiting" | "info" | "neutral";

export interface WorkspaceStatusNote {
  readonly text: string;
  readonly tone: WorkspaceStatusTone;
}

export interface WorkspacePullStatusLine {
  readonly label: "Review" | "CI" | "Merge";
  readonly value: string;
  readonly tone: WorkspaceStatusTone;
  /** Names of the failed checks, on the CI line only, when there are few. */
  readonly notes?: readonly WorkspaceStatusNote[];
}

// More failed checks than this are a count only: a long list of names says
// less than the number does.
const maxFailedCheckNames = 3;

function reviewLine(decision: string): WorkspacePullStatusLine | null {
  switch (decision) {
    case "APPROVED":
      return { label: "Review", value: "Approved", tone: "success" };
    case "CHANGES_REQUESTED":
      return { label: "Review", value: "Changes requested", tone: "danger" };
    case "REVIEW_REQUIRED":
      return { label: "Review", value: "Review required", tone: "neutral" };
    default:
      return null;
  }
}

function ciLine(checks: CIBucketedChecks): WorkspacePullStatusLine | null {
  if (checks.all.length === 0) return null;
  const counts = [
    [checks.failed.length, "failed"],
    [checks.pending.length, "running"],
    [checks.unknown.length, "unknown"],
    [checks.passed.length, "passed"],
    [checks.skipped.length, "skipped"],
  ] as const;
  const value = counts
    .filter(([count]) => count > 0)
    .map(([count, name]) => `${count} ${name}`)
    .join(", ");
  const tone = checks.failed.length > 0 ? "danger" : checks.pending.length > 0 ? "warning" : "success";
  if (checks.failed.length === 0 || checks.failed.length > maxFailedCheckNames) return { label: "CI", value, tone };
  const names = checks.failed.map((check) => check.name).join(", ");
  return { label: "CI", value, tone, notes: [{ text: names, tone: "danger" }] };
}

function mergeLine(status: WorkspacePullStatus): WorkspacePullStatusLine | null {
  const states: string[] = [];
  if (status.conflict) states.push("Conflicts");
  if (status.mergeQueued) states.push("In merge queue");
  if (status.mergeAfterCI) states.push("After CI");
  if (states.length === 0) return null;
  return {
    label: "Merge",
    value: states.join(", "),
    tone: status.conflict ? "warning" : "waiting",
  };
}

/** The status lines of the workspace row's hover card, in reading order. */
export function workspacePullStatusLines(status: WorkspacePullStatus): WorkspacePullStatusLine[] {
  return [reviewLine(status.reviewDecision), ciLine(status.checks), mergeLine(status)].filter(
    (line): line is WorkspacePullStatusLine => line !== null,
  );
}
