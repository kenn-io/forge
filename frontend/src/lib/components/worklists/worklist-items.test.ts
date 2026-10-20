import { describe, expect, it } from "vite-plus/test";
import { worklistItem } from "../../../test/worklistFixtures.js";
import { repositoryIdKey } from "../../api/repository-key.js";
import { buildWorklistRoute, parseWorklistSelection } from "../../routes.js";
import {
  worklistChangeMarkers,
  worklistItemKey,
  worklistItemMatchesSelection,
  worklistItemSelection,
  worklistItemsByScore,
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
