import { describe, expect, it } from "vite-plus/test";
import type { ActivityItem } from "../api/types.js";
import { collapseActivityRuns, isCollapsedActivityRow } from "./activityRows.js";
import { activityBranchKey, activityItemKey, activityRepoKey } from "./activityRows.js";

function item(id: string, activity_type: string, author: string): ActivityItem {
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

function branchItem(id: string, activity_type: string, author: string, branchName = "main"): ActivityItem {
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

function providerItem(id: string, provider: string): ActivityItem {
  return {
    ...item(id, "commit", "alice"),
    platform_host: "github.com",
    repo: {
      provider,
      platform_host: "github.com",
      owner: "acme",
      name: "widgets",
      repo_path: "acme/widgets",
    },
  } as ActivityItem;
}

describe("collapseActivityRuns", () => {
  it("collapses three consecutive commits from the same author", () => {
    const rows = collapseActivityRuns(
      [item("7", "commit", "alice"), item("6", "commit", "alice"), item("5", "commit", "alice")],
      { rollUpCommits: true },
    );

    expect(rows).toHaveLength(1);
    expect(isCollapsedActivityRow(rows[0]!)).toBe(true);
  });

  it("collapses consecutive comments from the same author", () => {
    const rows = collapseActivityRuns([
      item("7", "comment", "alice"),
      item("6", "comment", "alice"),
      item("5", "comment", "alice"),
    ]);

    expect(rows).toHaveLength(1);
    expect(isCollapsedActivityRow(rows[0]!)).toBe(true);
    expect(isCollapsedActivityRow(rows[0]!) ? rows[0].representative.activity_type : undefined).toBe("comment");
  });

  it("collapses consecutive reviews from the same author", () => {
    const rows = collapseActivityRuns([
      item("7", "review", "alice"),
      item("6", "review", "alice"),
      item("5", "review", "alice"),
    ]);

    expect(rows).toHaveLength(1);
    expect(isCollapsedActivityRow(rows[0]!)).toBe(true);
    expect(isCollapsedActivityRow(rows[0]!) ? rows[0].representative.activity_type : undefined).toBe("review");
  });

  it("can keep consecutive comments and reviews expanded", () => {
    const rows = collapseActivityRuns(
      [
        item("9", "comment", "alice"),
        item("8", "comment", "alice"),
        item("7", "comment", "alice"),
        item("6", "review", "alice"),
        item("5", "review", "alice"),
        item("4", "review", "alice"),
      ],
      { rollUpNonCommitActivity: false },
    );

    expect(rows).toHaveLength(6);
    expect(rows.every((row) => !isCollapsedActivityRow(row))).toBe(true);
  });

  it("does not merge runs of different event types", () => {
    const rows = collapseActivityRuns([
      item("9", "review", "alice"),
      item("8", "review", "alice"),
      item("7", "review", "alice"),
      item("6", "comment", "alice"),
      item("5", "comment", "alice"),
      item("4", "comment", "alice"),
    ]);

    expect(rows).toHaveLength(2);
    expect(isCollapsedActivityRow(rows[0]!)).toBe(true);
    expect(isCollapsedActivityRow(rows[1]!)).toBe(true);
  });

  it("does not collapse runs of fewer than three events", () => {
    const rows = collapseActivityRuns([
      item("3", "comment", "alice"),
      item("2", "review", "alice"),
      item("1", "comment", "alice"),
    ]);

    expect(rows).toHaveLength(3);
    expect(isCollapsedActivityRow(rows[0]!)).toBe(false);
    expect(isCollapsedActivityRow(rows[1]!)).toBe(false);
    expect(isCollapsedActivityRow(rows[2]!)).toBe(false);
  });

  it("does not collapse across a force-push boundary", () => {
    const rows = collapseActivityRuns(
      [
        item("9", "commit", "alice"),
        item("8", "commit", "alice"),
        item("7", "commit", "alice"),
        item("6", "force_push", "alice"),
        item("5", "commit", "alice"),
        item("4", "commit", "alice"),
        item("3", "commit", "alice"),
      ],
      { rollUpCommits: true },
    );

    expect(rows).toHaveLength(3);
    expect(isCollapsedActivityRow(rows[0]!)).toBe(true);
    expect(!isCollapsedActivityRow(rows[1]!) && rows[1]!.activity_type).toBe("force_push");
    expect(isCollapsedActivityRow(rows[2]!)).toBe(true);
  });

  it("rolls up branch commit runs by repo branch and author", () => {
    const rows = collapseActivityRuns(
      [
        branchItem("9", "default_branch_commit", "alice"),
        branchItem("8", "default_branch_commit", "alice"),
        branchItem("7", "default_branch_commit", "alice"),
      ],
      { rollUpCommits: true },
    );

    expect(rows).toHaveLength(1);
    expect(isCollapsedActivityRow(rows[0]!)).toBe(true);
    expect(isCollapsedActivityRow(rows[0]!) ? rows[0].representative.branch_name : undefined).toBe("main");
  });

  it("keeps commit runs expanded when commit roll-up is disabled", () => {
    const rows = collapseActivityRuns(
      [
        item("9", "commit", "alice"),
        item("8", "commit", "alice"),
        item("7", "commit", "alice"),
        item("6", "comment", "alice"),
        item("5", "comment", "alice"),
        item("4", "comment", "alice"),
        branchItem("3", "default_branch_commit", "alice"),
        branchItem("2", "default_branch_commit", "alice"),
        branchItem("1", "default_branch_commit", "alice"),
      ],
      { rollUpCommits: false },
    );

    expect(rows).toHaveLength(7);
    expect(isCollapsedActivityRow(rows[0]!)).toBe(false);
    expect(isCollapsedActivityRow(rows[1]!)).toBe(false);
    expect(isCollapsedActivityRow(rows[2]!)).toBe(false);
    expect(isCollapsedActivityRow(rows[3]!)).toBe(true);
    expect(isCollapsedActivityRow(rows[4]!)).toBe(false);
    expect(isCollapsedActivityRow(rows[5]!)).toBe(false);
    expect(isCollapsedActivityRow(rows[6]!)).toBe(false);
  });

  it("does not collapse branch commits across branches", () => {
    const rows = collapseActivityRuns(
      [
        branchItem("9", "default_branch_commit", "alice", "main"),
        branchItem("8", "default_branch_commit", "alice", "main"),
        branchItem("7", "default_branch_commit", "alice", "main"),
        branchItem("6", "default_branch_commit", "alice", "release"),
        branchItem("5", "default_branch_commit", "alice", "release"),
        branchItem("4", "default_branch_commit", "alice", "release"),
      ],
      { rollUpCommits: true },
    );

    expect(rows).toHaveLength(2);
    expect(isCollapsedActivityRow(rows[0]!)).toBe(true);
    expect(isCollapsedActivityRow(rows[1]!)).toBe(true);
  });

  it("does not collapse commit runs across providers with the same host and repo path", () => {
    const rows = collapseActivityRuns(
      [
        providerItem("9", "github"),
        providerItem("8", "github"),
        providerItem("7", "github"),
        providerItem("6", "gitea"),
        providerItem("5", "gitea"),
        providerItem("4", "gitea"),
      ],
      { rollUpCommits: true },
    );

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
    const b = activityRepoKey({
      ...base,
      platformHost: "ghe.example.com",
    });
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
