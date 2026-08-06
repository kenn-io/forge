// @vitest-environment jsdom

import { afterEach, beforeEach, describe, expect, it, vi } from "vite-plus/test";
import { createDetailActivityViewStore } from "./detail-activity-view.svelte.js";

const STORAGE_KEY = "kenn-forge-detail-activity-view";
const ORDER_STORAGE_KEY = "kenn-forge-detail-timeline-order";

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

  it("defaults to grouped timeline order", () => {
    const store = createDetailActivityViewStore();
    expect(store.getOrder()).toBe("grouped");
  });

  it("loads chronological order from localStorage", () => {
    localStorage.setItem(ORDER_STORAGE_KEY, "chronological");

    const store = createDetailActivityViewStore();

    expect(store.getOrder()).toBe("chronological");
  });

  it("falls back to grouped for invalid persisted orders", () => {
    localStorage.setItem(ORDER_STORAGE_KEY, "reverse");

    const store = createDetailActivityViewStore();

    expect(store.getOrder()).toBe("grouped");
  });

  it("persists valid order changes", () => {
    const store = createDetailActivityViewStore();

    store.setOrder("chronological");

    expect(store.getOrder()).toBe("chronological");
    expect(localStorage.getItem(ORDER_STORAGE_KEY)).toBe("chronological");
  });

  it("keeps in-memory order updates when localStorage writes fail", () => {
    vi.spyOn(Storage.prototype, "setItem").mockImplementation(() => {
      throw new Error("storage unavailable");
    });
    const store = createDetailActivityViewStore();

    expect(() => store.setOrder("chronological")).not.toThrow();

    expect(store.getOrder()).toBe("chronological");
  });
});
