import { describe, expect, it } from "vitest";
import type { ActivityItem } from "../api/types.js";
import {
  collapseActivityCommitRuns,
  isCollapsedActivityRow,
} from "./activityRows.js";
import {
  activityBranchKey,
  activityItemKey,
  activityRepoKey,
} from "./activityRows.js";

function item(
  id: string,
  activity_type: string,
  author: string,
): ActivityItem {
  return {
    id,
    cursor: id,
    activity_type,
    repo_owner: "acme",
    repo_name: "widgets",
    item_type: "pr",
    item_number: 7,
    item_title: "Rewrite branch",
    item_url: "https://github.com/acme/widgets/pull/7",
    item_state: "open",
    author,
    created_at: new Date(Number(id) * 1000).toISOString(),
    body_preview: "",
  } as ActivityItem;
}

function branchItem(
  id: string,
  activity_type: string,
  author: string,
  branchName = "main",
): ActivityItem {
  return {
    ...item(id, activity_type, author),
    item_type: "",
    item_number: 0,
    item_title: "",
    item_url: "",
    branch_name: branchName,
    commit_sha: `${id.repeat(8).slice(0, 40)}`,
    body_preview: `Commit ${id}`,
    repo: {
      provider: "github",
      platform_host: "github.com",
      owner: "acme",
      name: "widgets",
      repo_path: "acme/widgets",
    },
  } as ActivityItem;
}

describe("collapseActivityCommitRuns", () => {
  it("collapses three consecutive commits from the same author", () => {
    const rows = collapseActivityCommitRuns([
      item("7", "commit", "alice"),
      item("6", "commit", "alice"),
      item("5", "commit", "alice"),
    ]);

    expect(rows).toHaveLength(1);
    expect(isCollapsedActivityRow(rows[0]!)).toBe(true);
  });

  it("does not collapse across a force-push boundary", () => {
    const rows = collapseActivityCommitRuns([
      item("9", "commit", "alice"),
      item("8", "commit", "alice"),
      item("7", "commit", "alice"),
      item("6", "force_push", "alice"),
      item("5", "commit", "alice"),
      item("4", "commit", "alice"),
      item("3", "commit", "alice"),
    ]);

    expect(rows).toHaveLength(3);
    expect(isCollapsedActivityRow(rows[0]!)).toBe(true);
    expect(
      !isCollapsedActivityRow(rows[1]!)
        && rows[1]!.activity_type,
    ).toBe("force_push");
    expect(isCollapsedActivityRow(rows[2]!)).toBe(true);
  });

  it("rolls up branch commit runs by repo branch and author", () => {
    const rows = collapseActivityCommitRuns([
      branchItem("9", "default_branch_commit", "alice"),
      branchItem("8", "default_branch_commit", "alice"),
      branchItem("7", "default_branch_commit", "alice"),
    ]);

    expect(rows).toHaveLength(1);
    expect(isCollapsedActivityRow(rows[0]!)).toBe(true);
    expect(
      isCollapsedActivityRow(rows[0]!)
        ? rows[0].representative.branch_name
        : undefined,
    ).toBe("main");
  });

  it("does not collapse branch commits across branches", () => {
    const rows = collapseActivityCommitRuns([
      branchItem("9", "default_branch_commit", "alice", "main"),
      branchItem("8", "default_branch_commit", "alice", "main"),
      branchItem("7", "default_branch_commit", "alice", "main"),
      branchItem("6", "default_branch_commit", "alice", "release"),
      branchItem("5", "default_branch_commit", "alice", "release"),
      branchItem("4", "default_branch_commit", "alice", "release"),
    ]);

    expect(rows).toHaveLength(2);
    expect(isCollapsedActivityRow(rows[0]!)).toBe(true);
    expect(isCollapsedActivityRow(rows[1]!)).toBe(true);
  });
});

describe("activityRepoKey / activityItemKey", () => {
  const base = {
    provider: "github",
    platformHost: "github.com",
    owner: "acme",
    name: "widgets",
  };

  it("includes provider so same owner/name on different providers differ", () => {
    const a = activityRepoKey(base);
    const b = activityRepoKey({ ...base, provider: "gitlab" });
    expect(a).not.toBe(b);
  });

  it("includes host so same identity on different hosts differs", () => {
    const a = activityRepoKey(base);
    const b = activityRepoKey({ ...base, platformHost: "ghe.example.com" });
    expect(a).not.toBe(b);
  });

  it("builds an item key as the repo key plus type and number", () => {
    const item = { ...base, itemType: "pr", itemNumber: 42 };
    expect(activityItemKey(item)).toBe(`${activityRepoKey(base)}:pr:42`);
  });

  it("separates a PR and an issue with the same number", () => {
    const pr = { ...base, itemType: "pr", itemNumber: 42 };
    const issue = { ...base, itemType: "issue", itemNumber: 42 };
    expect(activityItemKey(pr)).not.toBe(activityItemKey(issue));
  });

  it("builds a branch key without a PR or issue number", () => {
    const main = { ...base, branchName: "main" };
    const release = { ...base, branchName: "release" };
    expect(activityBranchKey(main)).toBe(`${activityRepoKey(base)}:branch:main`);
    expect(activityBranchKey(main)).not.toBe(activityBranchKey(release));
  });
});
