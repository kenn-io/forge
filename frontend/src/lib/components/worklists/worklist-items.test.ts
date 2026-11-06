import { describe, expect, it } from "vite-plus/test";
import { mockIssueFixture, mockPullFixture } from "../../../test/mockApiFetch.js";
import { worklistItem, worklistResponse, worklistSummary } from "../../../test/worklistFixtures.js";
import type { WorklistItem } from "../../api/worklists.js";
import { repositoryIdKey } from "../../api/repository-key.js";
import { buildWorklistRoute, parseWorklistSelection } from "../../routes.js";
import {
  worklistChangeMarkers,
  worklistItemKey,
  worklistItemMatchesFilters,
  worklistItemMatchesSelection,
  worklistItemSelection,
  worklistItemsByScore,
  worklistItemsInState,
  worklistPickerOptions,
  worklistSections,
} from "./worklist-items.js";

describe("worklistChangeMarkers", () => {
  it("names merged and closed items in a muted tone", () => {
    const merged = worklistItem({
      position: 0,
      item_type: "pr",
      number: 1,
      freshness: "gone",
      changes: { new_commits: false, new_comments: 0, state_from: "open", state_to: "merged" },
    });
    const closed = worklistItem({
      position: 1,
      item_type: "issue",
      number: 2,
      freshness: "gone",
      changes: { new_commits: false, new_comments: 1, state_from: "open", state_to: "closed" },
    });

    expect(worklistChangeMarkers(merged)).toEqual([{ label: "Merged", tone: "muted" }]);
    expect(worklistChangeMarkers(closed)).toEqual([
      { label: "Closed", tone: "muted" },
      { label: "1 new comment", tone: "info" },
    ]);
  });

  it("names a reopen, an approval, and a CI change", () => {
    const item = worklistItem({
      position: 0,
      item_type: "pr",
      number: 1,
      freshness: "changed",
      changes: {
        new_commits: true,
        new_comments: 0,
        state_from: "closed",
        state_to: "open",
        ci_from: "pending",
        ci_to: "success",
        review_from: "REVIEW_REQUIRED",
        review_to: "APPROVED",
      },
    });

    expect(worklistChangeMarkers(item).map((marker) => marker.label)).toEqual([
      "Reopened",
      "New commits",
      "CI running → passing",
      "Approved",
    ]);
  });

  it("files a merge-queued pull request under closed until the queue ejects it", () => {
    const queuedState = {
      state: "open",
      head_sha: "a".repeat(40),
      comment_count: 1,
      ci_status: "success",
      review_decision: "",
      last_activity_at: "2026-10-19T09:00:00Z",
      merge_queued: true,
    };
    const queued = worklistItem({ position: 0, item_type: "pr", number: 1, freshness: "gone", live: queuedState });
    const ejected = worklistItem({
      position: 0,
      item_type: "pr",
      number: 1,
      live: { ...queuedState, merge_queued: false },
    });

    expect(worklistChangeMarkers(queued)).toEqual([{ label: "In merge queue", tone: "muted" }]);
    expect(worklistItemsInState([queued], "open")).toEqual([]);
    expect(worklistItemsInState([queued], "closed")).toEqual([queued]);
    expect(worklistChangeMarkers(ejected)).toEqual([]);
    expect(worklistItemsInState([ejected], "open")).toEqual([ejected]);
  });

  it("names a cleared review decision", () => {
    const item = worklistItem({
      position: 0,
      item_type: "pr",
      number: 1,
      changes: { new_commits: false, new_comments: 0, review_from: "CHANGES_REQUESTED" },
    });

    expect(worklistChangeMarkers(item).map((marker) => marker.label)).toEqual(["Review cleared"]);
  });
});

describe("worklistItemMatchesFilters", () => {
  const workspace = { id: "ws-1", status: "ready" };

  function pull(number: number, fields: Record<string, unknown>, item: Partial<WorklistItem> = {}): WorklistItem {
    return worklistItem({
      position: number,
      item_type: "pr",
      number,
      pull: { ...mockPullFixture(42), Number: number, ...fields } as unknown as WorklistItem["pull"],
      ...item,
    });
  }

  function issue(number: number, fields: Record<string, unknown>, item: Partial<WorklistItem> = {}): WorklistItem {
    return worklistItem({
      position: number,
      item_type: "issue",
      number,
      issue: { ...mockIssueFixture(7), Number: number, ...fields } as unknown as WorklistItem["issue"],
      ...item,
    });
  }

  function matching(items: WorklistItem[], filters: Parameters<typeof worklistItemMatchesFilters>[1]): number[] {
    return items.filter((item) => worklistItemMatchesFilters(item, filters)).map((item) => item.number);
  }

  it("matches any option within a group and every selected group, as the PR list does", () => {
    const items = [
      pull(1, { IsDraft: false, CIStatus: "success", CIChecksJSON: "[]" }),
      pull(2, { IsDraft: false, CIStatus: "failure", CIChecksJSON: "[]" }),
      pull(3, { IsDraft: true, CIStatus: "success", CIChecksJSON: "[]" }, { involves_viewer: true }),
      pull(4, { IsDraft: false, CIStatus: "pending", CIChecksJSON: "[]" }),
    ];

    expect(matching(items, [])).toEqual([1, 2, 3, 4]);
    expect(matching(items, ["passed_ci", "failed_ci"])).toEqual([1, 2, 3]);
    expect(matching(items, ["ready", "passed_ci"])).toEqual([1]);
    expect(matching(items, ["draft", "ready", "passed_ci"])).toEqual([1, 3]);
    expect(matching(items, ["involves_me", "passed_ci"])).toEqual([3]);
  });

  it("requires a workspace on top of the other filters", () => {
    const items = [
      pull(1, { MergeableState: "dirty", workspace }),
      pull(2, { MergeableState: "dirty" }),
      pull(3, { MergeableState: "clean", workspace }),
      issue(4, { workspace }),
    ];

    expect(matching(items, ["has_workspace"])).toEqual([1, 3, 4]);
    expect(matching(items, ["has_workspace", "merge_conflicts"])).toEqual([1]);
  });

  it("requires a pull request without merge conflicts on top of the other filters", () => {
    const items = [
      pull(1, { MergeableState: "dirty", IsDraft: false }),
      pull(2, { MergeableState: "clean", IsDraft: false }),
      pull(3, { MergeableState: "clean", IsDraft: true }),
      issue(4, {}),
    ];

    expect(matching(items, ["no_merge_conflicts"])).toEqual([2, 3]);
    expect(matching(items, ["ready", "no_merge_conflicts"])).toEqual([2]);
  });

  it("matches issues by involvement and workspace but never by pull request attributes", () => {
    const items = [
      issue(1, {}, { involves_viewer: true }),
      issue(2, {}),
      pull(3, { IsDraft: true }),
      worklistItem({ position: 4, item_type: "pr", number: 4, live: null, involves_viewer: false }),
    ];

    expect(matching(items, ["draft"])).toEqual([3]);
    expect(matching(items, ["involves_me"])).toEqual([1]);
    expect(matching(items, ["draft", "involves_me"])).toEqual([]);
  });
});

describe("worklistSections", () => {
  it("orders sections by their best-ranked item and items by rank", () => {
    const items = [
      worklistItem({ position: 2, item_type: "pr", number: 3, section: "Ready" }),
      worklistItem({ position: 1, item_type: "pr", number: 2, section: "Blocked" }),
      worklistItem({ position: 0, item_type: "pr", number: 1, section: "Ready" }),
    ];

    expect(worklistSections(items).map((group) => [group.section, group.items.map((item) => item.number)])).toEqual([
      ["Ready", [1, 3]],
      ["Blocked", [2]],
    ]);
  });
});

describe("worklistPickerOptions", () => {
  it("sorts worklists by title, ignoring case and comparing numbers by value", () => {
    const summaries = [
      worklistSummary(worklistResponse(1, "Roundup - Widgets")),
      worklistSummary(worklistResponse(2, "release batch 10", [])),
      worklistSummary(worklistResponse(3, "Release batch 9")),
      worklistSummary(worklistResponse(4, "acme triage")),
    ];

    expect(worklistPickerOptions(summaries).map(({ name, label }) => [name, label])).toEqual([
      ["4", "acme triage"],
      ["3", "Release batch 9"],
      ["2", "release batch 10"],
      ["1", "Roundup - Widgets"],
    ]);
    expect(worklistPickerOptions(summaries).find((option) => option.name === "2")?.meta).toBe("done");
  });
});

describe("worklistItemsByScore", () => {
  it("puts the highest score first, breaks ties by rank, and puts unscored items last", () => {
    const items = [
      worklistItem({ position: 0, item_type: "pr", number: 1, score: null }),
      worklistItem({ position: 1, item_type: "pr", number: 2, score: 5 }),
      worklistItem({ position: 2, item_type: "issue", number: 3, score: 9 }),
      worklistItem({ position: 3, item_type: "pr", number: 4, score: 5 }),
      worklistItem({ position: 4, item_type: "pr", number: 5, score: null }),
    ];

    expect(worklistItemsByScore(items).map((item) => item.number)).toEqual([3, 2, 4, 1, 5]);
    expect(items.map((item) => item.number)).toEqual([1, 2, 3, 4, 5]);
  });
});

describe("worklistItemMatchesSelection", () => {
  const item = worklistItem({ position: 0, item_type: "pr", number: 42 });
  const selection = {
    itemType: "pr" as const,
    provider: "github",
    platformHost: "github.com",
    repositoryKey: repositoryIdKey(1001),
    owner: "acme",
    name: "widgets",
    repoPath: "acme/widgets",
    number: 42,
    detailTab: "conversation" as const,
  };

  it("matches a provider alias, a provider in another case, and an omitted default host", () => {
    expect(worklistItemMatchesSelection(item, { ...selection, provider: "gh" })).toBe(true);
    expect(worklistItemMatchesSelection(item, { ...selection, provider: "GitHub" })).toBe(true);
    expect(worklistItemMatchesSelection(item, { ...selection, provider: "gh", platformHost: undefined })).toBe(true);
  });

  it("keeps the host, item type, and number in the identity", () => {
    expect(worklistItemMatchesSelection(item, { ...selection, platformHost: "example.com" })).toBe(false);
    expect(worklistItemMatchesSelection(item, { ...selection, itemType: "issue" })).toBe(false);
    expect(worklistItemMatchesSelection(item, { ...selection, number: 43 })).toBe(false);
    expect(worklistItemMatchesSelection(item, { ...selection, provider: "gitlab" })).toBe(false);
  });

  it("tells apart repositories that share a route by their stable identity, through the URL", () => {
    // A repository renamed away and a new one created under its old name.
    const renamedAway = worklistItem({ position: 0, item_type: "pr", number: 42 });
    const reusedName = worklistItem({
      position: 1,
      item_type: "pr",
      number: 42,
      repo: { ...renamedAway.repo, platform_repo_id: 3003 },
    });
    expect(worklistItemKey(renamedAway)).not.toBe(worklistItemKey(reusedName));

    const route = buildWorklistRoute(5, worklistItemSelection(reusedName));
    const selected = parseWorklistSelection(route.slice(route.indexOf("?")));
    expect([renamedAway, reusedName].filter((candidate) => worklistItemMatchesSelection(candidate, selected))).toEqual([
      reusedName,
    ]);
  });
});
