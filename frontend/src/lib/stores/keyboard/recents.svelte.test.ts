import { beforeEach, describe, expect, it } from "vite-plus/test";

import type { RoutedItemRef } from "@middleman/ui/routes";

import { MAX_ITEMS, RECENTS_KEY, pruneRecents, pruneStale, readRecents, writeRecent } from "./recents.svelte.js";

const ref = (n: number): RoutedItemRef => ({
  itemType: "pr",
  provider: "github",
  owner: "a",
  name: "b",
  repoPath: "a/b",
  number: n,
});

const issueRef = (n: number): RoutedItemRef => ({
  itemType: "issue",
  provider: "github",
  owner: "a",
  name: "b",
  repoPath: "a/b",
  number: n,
});

beforeEach(() => localStorage.clear());

describe("recents", () => {
  it("returns empty for missing key", () => {
    expect(readRecents()).toEqual({ version: 1, items: [] });
  });

  it("malformed JSON is ignored and overwritten with empty state", () => {
    localStorage.setItem(RECENTS_KEY, "not-json");
    expect(readRecents()).toEqual({ version: 1, items: [] });
    expect(localStorage.getItem(RECENTS_KEY)).toBe(JSON.stringify({ version: 1, items: [] }));
  });

  it("version mismatch is treated as empty", () => {
    localStorage.setItem(RECENTS_KEY, JSON.stringify({ version: 0, items: [] }));
    expect(readRecents().items).toHaveLength(0);
  });

  it("dedupe by kind+ref, max MAX_ITEMS", () => {
    for (let i = 0; i < 10; i++) {
      writeRecent("pr", ref(i));
    }
    const recents = readRecents();
    expect(recents.items).toHaveLength(MAX_ITEMS);
    // Most recent at front: last writes were i=9, 8, 7, ... down to 2.
    expect(recents.items.map((item) => (item.ref as { number: number }).number)).toEqual([9, 8, 7, 6, 5, 4, 3, 2]);
  });

  it("writeRecent puts the newest item at the front", () => {
    writeRecent("pr", ref(1));
    writeRecent("pr", ref(2));
    writeRecent("pr", ref(3));
    const items = readRecents().items;
    expect(items.map((item) => (item.ref as { number: number }).number)).toEqual([3, 2, 1]);
  });

  it("re-adding an item dedupes and moves it to the front", () => {
    writeRecent("pr", ref(1));
    writeRecent("pr", ref(2));
    writeRecent("pr", ref(1));
    const items = readRecents().items;
    expect(items).toHaveLength(2);
    expect(items.map((item) => (item.ref as { number: number }).number)).toEqual([1, 2]);
  });

  it("treats different kinds with the same ref as distinct", () => {
    writeRecent("pr", ref(1));
    writeRecent("issue", issueRef(1));
    const items = readRecents().items;
    expect(items).toHaveLength(2);
    expect(items.map((item) => item.kind)).toEqual(["issue", "pr"]);
  });

  it("pruneRecents drops items rejected by the predicate", () => {
    writeRecent("pr", ref(1));
    writeRecent("pr", ref(2));
    writeRecent("pr", ref(3));
    pruneRecents((item) => (item.ref as { number: number }).number !== 2);
    const items = readRecents().items;
    expect(items.map((item) => (item.ref as { number: number }).number)).toEqual([3, 1]);
  });

  it("pruneStale drops items older than 30 days", () => {
    const now = new Date("2026-05-09T12:00:00Z");
    const fresh = new Date(now.getTime() - 1 * 24 * 60 * 60 * 1000).toISOString();
    const stale = new Date(now.getTime() - 31 * 24 * 60 * 60 * 1000).toISOString();
    localStorage.setItem(
      RECENTS_KEY,
      JSON.stringify({
        version: 1,
        items: [
          { kind: "pr", ref: ref(1), lastSelectedAt: fresh },
          { kind: "pr", ref: ref(2), lastSelectedAt: stale },
        ],
      }),
    );
    pruneStale(now);
    const items = readRecents().items;
    expect(items).toHaveLength(1);
    expect((items[0]!.ref as { number: number }).number).toBe(1);
  });

  it("pruneStale keeps items exactly at the 30-day boundary", () => {
    const now = new Date("2026-05-09T12:00:00Z");
    const exactly30 = new Date(now.getTime() - 30 * 24 * 60 * 60 * 1000).toISOString();
    localStorage.setItem(
      RECENTS_KEY,
      JSON.stringify({
        version: 1,
        items: [{ kind: "pr", ref: ref(1), lastSelectedAt: exactly30 }],
      }),
    );
    pruneStale(now);
    expect(readRecents().items).toHaveLength(1);
  });

  it("drops items with kinds outside pr|issue", () => {
    localStorage.setItem(
      RECENTS_KEY,
      JSON.stringify({
        version: 1,
        items: [
          {
            kind: "pr",
            ref: ref(1),
            lastSelectedAt: "2026-01-01T00:00:00Z",
          },
          {
            kind: "future-kind",
            ref: {},
            lastSelectedAt: "2026-01-01T00:00:00Z",
          },
        ],
      }),
    );
    expect(readRecents().items).toHaveLength(1);
  });

  it("normalizes non-string lastSelectedAt to epoch", () => {
    localStorage.setItem(
      RECENTS_KEY,
      JSON.stringify({
        version: 1,
        items: [
          { kind: "pr", ref: ref(1), lastSelectedAt: 1735689600000 },
          { kind: "pr", ref: ref(2), lastSelectedAt: null },
        ],
      }),
    );
    const items = readRecents().items;
    expect(items).toHaveLength(2);
    const epoch = new Date(0).toISOString();
    expect(items[0]!.lastSelectedAt).toBe(epoch);
    expect(items[1]!.lastSelectedAt).toBe(epoch);
  });

  it("normalizes invalid date strings to epoch", () => {
    // Strings that pass typeof === "string" but parse to NaN must be
    // normalized so consumers (sort, diff, pruneStale) never see NaN.
    localStorage.setItem(
      RECENTS_KEY,
      JSON.stringify({
        version: 1,
        items: [
          { kind: "pr", ref: ref(1), lastSelectedAt: "not-a-date" },
          { kind: "pr", ref: ref(2), lastSelectedAt: "" },
        ],
      }),
    );
    const items = readRecents().items;
    expect(items).toHaveLength(2);
    const epoch = new Date(0).toISOString();
    expect(items[0]!.lastSelectedAt).toBe(epoch);
    expect(items[1]!.lastSelectedAt).toBe(epoch);
  });

  it("drops items whose ref is missing required fields", () => {
    // Corrupt or hand-edited entries — null/empty/non-string ref fields,
    // mismatched itemType — must be dropped so the palette doesn't crash
    // when it dereferences recent.ref.repoPath / .number on render.
    localStorage.setItem(
      RECENTS_KEY,
      JSON.stringify({
        version: 1,
        items: [
          {
            kind: "pr",
            ref: null,
            lastSelectedAt: "2026-01-01T00:00:00Z",
          },
          {
            kind: "pr",
            ref: {},
            lastSelectedAt: "2026-01-01T00:00:00Z",
          },
          {
            kind: "pr",
            ref: {
              itemType: "issue",
              provider: "github",
              owner: "a",
              name: "b",
              repoPath: "a/b",
              number: 1,
            },
            lastSelectedAt: "2026-01-01T00:00:00Z",
          },
          {
            kind: "pr",
            ref: {
              itemType: "pr",
              provider: "github",
              owner: "a",
              name: "b",
              repoPath: "a/b",
              number: "not-a-number",
            },
            lastSelectedAt: "2026-01-01T00:00:00Z",
          },
          {
            kind: "pr",
            ref: ref(7),
            lastSelectedAt: "2026-01-01T00:00:00Z",
          },
        ],
      }),
    );
    const items = readRecents().items;
    expect(items).toHaveLength(1);
    expect((items[0]!.ref as { number: number }).number).toBe(7);
  });

  it("drops items missing a kind field entirely", () => {
    localStorage.setItem(
      RECENTS_KEY,
      JSON.stringify({
        version: 1,
        items: [
          { ref: ref(1), lastSelectedAt: "2026-01-01T00:00:00Z" },
          {
            kind: "pr",
            ref: ref(2),
            lastSelectedAt: "2026-01-01T00:00:00Z",
          },
        ],
      }),
    );
    const items = readRecents().items;
    expect(items).toHaveLength(1);
    expect((items[0]!.ref as { number: number }).number).toBe(2);
  });

  it("returns deeply independent objects across calls", () => {
    writeRecent("pr", ref(1));
    const first = readRecents();
    first.items.length = 0;
    first.items.push({
      kind: "pr",
      ref: ref(99),
      lastSelectedAt: new Date().toISOString(),
    });
    const second = readRecents();
    expect(second.items).toHaveLength(1);
    expect((second.items[0]!.ref as { number: number }).number).toBe(1);
  });

  it("items is treated as empty when the parsed value is null", () => {
    localStorage.setItem(RECENTS_KEY, "null");
    expect(readRecents()).toEqual({ version: 1, items: [] });
    expect(localStorage.getItem(RECENTS_KEY)).toBe(JSON.stringify({ version: 1, items: [] }));
  });

  it("items is treated as empty when items is not an array", () => {
    localStorage.setItem(RECENTS_KEY, JSON.stringify({ version: 1, items: "nope" }));
    expect(readRecents()).toEqual({ version: 1, items: [] });
  });
});
