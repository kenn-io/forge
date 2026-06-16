// @vitest-environment jsdom

import { afterEach, beforeEach, describe, expect, it, vi } from "vite-plus/test";
import { createDetailActivityViewStore } from "./detail-activity-view.svelte.js";

const STORAGE_KEY = "middleman-detail-activity-view";

beforeEach(() => {
  localStorage.clear();
});

afterEach(() => {
  vi.restoreAllMocks();
});

describe("createDetailActivityViewStore", () => {
  it("defaults to normal mode", () => {
    const store = createDetailActivityViewStore();
    expect(store.getMode()).toBe("normal");
  });

  it("loads compact mode from localStorage", () => {
    localStorage.setItem(STORAGE_KEY, "compact");

    const store = createDetailActivityViewStore();

    expect(store.getMode()).toBe("compact");
  });

  it("falls back to normal for invalid persisted modes", () => {
    localStorage.setItem(STORAGE_KEY, "dense");

    const store = createDetailActivityViewStore();

    expect(store.getMode()).toBe("normal");
  });

  it("persists valid mode changes", () => {
    const store = createDetailActivityViewStore();

    store.setMode("compact");

    expect(store.getMode()).toBe("compact");
    expect(localStorage.getItem(STORAGE_KEY)).toBe("compact");
  });

  it("keeps in-memory updates when localStorage writes fail", () => {
    vi.spyOn(Storage.prototype, "setItem").mockImplementation(() => {
      throw new Error("storage unavailable");
    });
    const store = createDetailActivityViewStore();

    expect(() => store.setMode("compact")).not.toThrow();

    expect(store.getMode()).toBe("compact");
  });

  it("falls back to normal when localStorage reads fail", () => {
    vi.spyOn(Storage.prototype, "getItem").mockImplementation(() => {
      throw new Error("storage blocked");
    });

    const store = createDetailActivityViewStore();

    expect(store.getMode()).toBe("normal");
  });
});
