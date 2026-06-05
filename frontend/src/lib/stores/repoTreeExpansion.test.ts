import { afterEach, beforeEach, describe, expect, it, vi } from "vite-plus/test";

import { createRepoTreeExpansionStore } from "./repoTreeExpansion.svelte.js";

const KEY = "middleman:repoTreeCollapsed";

beforeEach(() => {
  localStorage.clear();
});

afterEach(() => {
  vi.restoreAllMocks();
});

describe("createRepoTreeExpansionStore", () => {
  it("reports every node expanded on a fresh store", () => {
    const store = createRepoTreeExpansionStore();
    expect(store.isCollapsed("github.com/acme")).toBe(false);
  });

  it("flips collapsed state on each toggle", () => {
    const store = createRepoTreeExpansionStore();
    store.toggle("github.com/acme");
    expect(store.isCollapsed("github.com/acme")).toBe(true);
    store.toggle("github.com/acme");
    expect(store.isCollapsed("github.com/acme")).toBe(false);
  });

  it("persists collapsed ids to localStorage", () => {
    const store = createRepoTreeExpansionStore();
    store.toggle("github.com/acme");
    expect(JSON.parse(localStorage.getItem(KEY)!)).toEqual(["github.com/acme"]);
  });

  it("reads pre-seeded collapsed ids", () => {
    localStorage.setItem(KEY, JSON.stringify(["github.com/acme"]));
    const store = createRepoTreeExpansionStore();
    expect(store.isCollapsed("github.com/acme")).toBe(true);
  });

  it("falls back to expanded on malformed JSON", () => {
    localStorage.setItem(KEY, "{not json");
    const store = createRepoTreeExpansionStore();
    expect(store.isCollapsed("github.com/acme")).toBe(false);
  });

  it("falls back to expanded on non-array JSON", () => {
    localStorage.setItem(KEY, JSON.stringify({ bad: "shape" }));
    const store = createRepoTreeExpansionStore();
    expect(store.isCollapsed("github.com/acme")).toBe(false);
  });

  it("keeps working in memory when setItem throws", () => {
    const spy = vi.spyOn(Storage.prototype, "setItem").mockImplementation(() => {
      throw new Error("QuotaExceededError");
    });
    const store = createRepoTreeExpansionStore();
    expect(() => store.toggle("github.com/acme")).not.toThrow();
    expect(store.isCollapsed("github.com/acme")).toBe(true);
    spy.mockRestore();
  });
});
