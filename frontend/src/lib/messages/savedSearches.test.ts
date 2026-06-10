import { describe, expect, test } from "vite-plus/test";

import {
  QUICK_VIEWS,
  addSavedSearch,
  normalizeSavedSearches,
  removeSavedSearch,
  type SavedSearch,
} from "./savedSearches.js";

describe("QUICK_VIEWS", () => {
  test("ships the canned messages queries", () => {
    expect(QUICK_VIEWS).toEqual([
      { label: "Inbox", query: "label:Inbox" },
      { label: "Has attachments", query: "has:attachment" },
      { label: "Recent", query: "newer_than:30d" },
    ]);
  });
});

describe("addSavedSearch", () => {
  test("appends a new entry with trimmed name and query", () => {
    expect(addSavedSearch([], "  Important  ", "  label:Inbox  ")).toEqual([
      { name: "Important", query: "label:Inbox" },
    ]);
  });

  test("falls back to the trimmed query when the name is empty", () => {
    expect(addSavedSearch([], "   ", "label:Work")).toEqual([{ name: "label:Work", query: "label:Work" }]);
  });

  test("is a no-op when the query is empty after trimming", () => {
    const list: SavedSearch[] = [{ name: "Existing", query: "label:Inbox" }];
    expect(addSavedSearch(list, "Whatever", "   ")).toBe(list);
  });

  test("overwrites in place when the name matches case-insensitively", () => {
    const list: SavedSearch[] = [
      { name: "Inbox", query: "label:Inbox" },
      { name: "Work", query: "label:Work" },
    ];
    expect(addSavedSearch(list, "INBOX", "label:Inbox newer_than:7d")).toEqual([
      { name: "INBOX", query: "label:Inbox newer_than:7d" },
      { name: "Work", query: "label:Work" },
    ]);
  });

  test("truncates oversized names and queries on rune boundaries", () => {
    const [entry] = addSavedSearch([], "✓".repeat(300), "x".repeat(600));
    expect(Array.from(entry!.name)).toHaveLength(200);
    expect(Array.from(entry!.query)).toHaveLength(500);
    expect(entry!.name).not.toContain("�");
    expect(entry!.query).not.toContain("�");
  });

  test("caps the list at 50 by dropping the oldest from the front", () => {
    const list: SavedSearch[] = Array.from({ length: 50 }, (_, i) => ({
      name: `s${i}`,
      query: `label:l${i}`,
    }));
    const next = addSavedSearch(list, "new", "label:new");
    expect(next).toHaveLength(50);
    expect(next[0]?.name).toBe("s1");
    expect(next[49]?.name).toBe("new");
  });
});

describe("removeSavedSearch", () => {
  test("removes the matching entry case-insensitively", () => {
    const list: SavedSearch[] = [
      { name: "Inbox", query: "label:Inbox" },
      { name: "Work", query: "label:Work" },
    ];
    expect(removeSavedSearch(list, "inbox")).toEqual([{ name: "Work", query: "label:Work" }]);
  });

  test("is a no-op when the name is not present", () => {
    const list: SavedSearch[] = [{ name: "Inbox", query: "label:Inbox" }];
    expect(removeSavedSearch(list, "Missing")).toBe(list);
  });
});

describe("normalizeSavedSearches", () => {
  test("drops malformed entries and empty queries", () => {
    const out = normalizeSavedSearches([
      null,
      "string",
      { name: "x" },
      { name: "x", query: 5 },
      { name: "empty", query: "  " },
      { name: "ok", query: " newer_than:7d " },
    ]);

    expect(out).toEqual([{ name: "ok", query: "newer_than:7d" }]);
  });

  test("falls back from empty or non-string names to the query", () => {
    const out = normalizeSavedSearches([
      { name: "  ", query: "has:attachment" },
      { name: 42, query: "label:Inbox" },
    ]);

    expect(out).toEqual([
      { name: "has:attachment", query: "has:attachment" },
      { name: "label:Inbox", query: "label:Inbox" },
    ]);
  });

  test("dedupes by case-insensitive name and keeps the later value in place", () => {
    const out = normalizeSavedSearches([
      { name: "Recent", query: "q1" },
      { name: "Other", query: "q2" },
      { name: "RECENT", query: "q3" },
    ]);
    expect(out).toEqual([
      { name: "RECENT", query: "q3" },
      { name: "Other", query: "q2" },
    ]);
  });

  test("dedupe of a name already evicted by the cap re-appends instead of disappearing", () => {
    const seed: unknown[] = Array.from({ length: 60 }, (_, i) => ({ name: `n${i}`, query: "q" }));
    seed.push({ name: "n0", query: "REAPPEARED" });
    const out = normalizeSavedSearches(seed);
    expect(out).toHaveLength(50);
    expect(out[out.length - 1]).toEqual({ name: "n0", query: "REAPPEARED" });
    expect(out.some((s) => s.name === "n0" && s.query === "q")).toBe(false);
  });

  test("caps distinct rows at 50 by keeping the most recent tail", () => {
    const out = normalizeSavedSearches(Array.from({ length: 60 }, (_, i) => ({ name: `n${i}`, query: "q" })));

    expect(out).toHaveLength(50);
    expect(out[0]).toEqual({ name: "n10", query: "q" });
    expect(out[49]).toEqual({ name: "n59", query: "q" });
  });
});
