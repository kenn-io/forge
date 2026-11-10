import { describe, expect, it } from "vite-plus/test";

import { workspacePullStatus, workspacePullStatusLines } from "./workspace-pull-status.js";

describe("workspacePullStatus", () => {
  it("has no pull request status for an issue workspace", () => {
    expect(workspacePullStatus({ item_type: "issue", mr_review_decision: "approved" })).toBeNull();
  });

  it("reads an ad-hoc workspace's status from its associated pull request", () => {
    const status = workspacePullStatus({
      item_type: "adhoc",
      associated_pr_number: 12,
      mr_mergeable_state: "dirty",
    });

    expect(status?.conflict).toBe(true);
  });

  it("buckets checks that carry no conclusion as pending, not unknown", () => {
    const status = workspacePullStatus({
      item_type: "pull_request",
      mr_checks: [
        { name: "build", status: "in_progress" },
        { name: "lint", status: "completed", conclusion: "failure" },
        { name: "unit", status: "completed", conclusion: "success" },
      ],
    });

    expect(status?.checks.pending.map((check) => check.name)).toEqual(["build"]);
    expect(status?.checks.failed.map((check) => check.name)).toEqual(["lint"]);
    expect(status?.checks.unknown).toEqual([]);
  });
});

describe("workspacePullStatusLines", () => {
  it("names each state and lists the failed checks", () => {
    const status = workspacePullStatus({
      item_type: "pull_request",
      mr_review_decision: "approved",
      mr_deferred_merge_pending: true,
      mr_checks: [
        ...["a", "b", "c", "d", "e"].map((name) => ({ name, status: "completed", conclusion: "failure" })),
        { name: "build", status: "queued" },
        { name: "unit", status: "completed", conclusion: "success" },
      ],
    })!;

    expect(workspacePullStatusLines(status)).toEqual([
      { label: "Review", value: "Approved", tone: "success" },
      {
        label: "CI",
        value: "5 failed, 1 running, 1 passed",
        tone: "danger",
        notes: [
          { text: "a, b, c, d, +1 more", tone: "danger" },
          { text: "build", tone: "warning" },
        ],
      },
      { label: "Merge", value: "After CI", tone: "waiting" },
    ]);
  });

  it("puts a conflict ahead of the merge queue", () => {
    const status = workspacePullStatus({
      item_type: "pull_request",
      mr_mergeable_state: "dirty",
      mr_merge_queued: true,
    })!;

    expect(workspacePullStatusLines(status)).toEqual([
      { label: "Merge", value: "Conflicts, In merge queue", tone: "warning" },
    ]);
  });

  it("has no lines or icons for a pull request with nothing to report", () => {
    const status = workspacePullStatus({ item_type: "pull_request" })!;

    expect(workspacePullStatusLines(status)).toEqual([]);
    expect(status.hasIcons).toBe(false);
  });

  it("names a required review that the row has no icon for", () => {
    const status = workspacePullStatus({ item_type: "pull_request", mr_review_decision: "review_required" })!;

    expect(status.hasIcons).toBe(false);
    expect(workspacePullStatusLines(status)).toEqual([{ label: "Review", value: "Review required", tone: "neutral" }]);
  });
});
