import { describe, expect, it } from "vite-plus/test";
import { worklistItem } from "../../../test/worklistFixtures.js";
import {
  worklistChangeMarkers,
  worklistItemMatchesSelection,
  worklistItemTarget,
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

  it("reports an item without a synced row as no longer synced, whatever else changed", () => {
    const item = worklistItem({
      position: 0,
      item_type: "pr",
      number: 1,
      freshness: "gone",
      live: null,
      changes: { new_commits: true, new_comments: 4 },
    });

    expect(worklistChangeMarkers(item)).toEqual([{ label: "No longer synced", tone: "muted" }]);
  });

  it("names a cleared review decision", () => {
    const item = worklistItem({
      position: 0,
      item_type: "pr",
      number: 1,
      changes: { new_commits: false, new_comments: 0, review_from: "CHANGES_REQUESTED", review_to: "" },
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

describe("worklistItemTarget", () => {
  it("offers no action target for an item without a synced row", () => {
    expect(worklistItemTarget(worklistItem({ position: 0, item_type: "pr", number: 1, pull: null }))).toBeNull();
  });
});

describe("worklistItemMatchesSelection", () => {
  const item = worklistItem({ position: 0, item_type: "pr", number: 42 });
  const selection = {
    itemType: "pr" as const,
    provider: "github",
    platformHost: "github.com",
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
});
