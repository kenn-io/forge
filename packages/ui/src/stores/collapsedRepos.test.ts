import { afterEach, beforeEach, describe, expect, it, vi } from "vite-plus/test";
import { createCollapsedReposStore } from "./collapsedRepos.svelte.js";

const PULLS_KEY = "middleman:collapsedRepos:pulls";
const ISSUES_KEY = "middleman:collapsedRepos:issues";

beforeEach(() => {
  localStorage.clear();
});

afterEach(() => {
  vi.restoreAllMocks();
});

describe("createCollapsedReposStore — defaults", () => {
  it("reports every repo as expanded on a fresh store", () => {
    const store = createCollapsedReposStore();
    expect(store.isCollapsed("pulls", "acme/widgets")).toBe(false);
    expect(store.isCollapsed("pulls", "acme/tools")).toBe(false);
    expect(store.isCollapsed("issues", "acme/widgets")).toBe(false);
    expect(store.isCollapsed("issues", "acme/tools")).toBe(false);
  });

  it("treats missing localStorage keys as empty sets", () => {
    expect(localStorage.getItem(PULLS_KEY)).toBeNull();
    expect(localStorage.getItem(ISSUES_KEY)).toBeNull();
    const store = createCollapsedReposStore();
    expect(store.isCollapsed("pulls", "acme/widgets")).toBe(false);
    expect(store.isCollapsed("issues", "acme/widgets")).toBe(false);
  });
});

describe("createCollapsedReposStore — toggle", () => {
  it("flips a repo's collapsed state on each call", () => {
    const store = createCollapsedReposStore();
    store.toggle("pulls", "acme/widgets");
    expect(store.isCollapsed("pulls", "acme/widgets")).toBe(true);
    store.toggle("pulls", "acme/widgets");
    expect(store.isCollapsed("pulls", "acme/widgets")).toBe(false);
  });

  it("keeps surfaces independent for the same repo key", () => {
    const store = createCollapsedReposStore();
    store.toggle("pulls", "acme/widgets");
    expect(store.isCollapsed("pulls", "acme/widgets")).toBe(true);
    expect(store.isCollapsed("issues", "acme/widgets")).toBe(false);

    store.toggle("issues", "acme/widgets");
    expect(store.isCollapsed("pulls", "acme/widgets")).toBe(true);
    expect(store.isCollapsed("issues", "acme/widgets")).toBe(true);

    store.toggle("pulls", "acme/widgets");
    expect(store.isCollapsed("pulls", "acme/widgets")).toBe(false);
    expect(store.isCollapsed("issues", "acme/widgets")).toBe(true);
  });

  it("keeps repo keys within a surface independent", () => {
    const store = createCollapsedReposStore();
    store.toggle("pulls", "acme/widgets");
    expect(store.isCollapsed("pulls", "acme/widgets")).toBe(true);
    expect(store.isCollapsed("pulls", "acme/tools")).toBe(false);
  });
});

describe("createCollapsedReposStore — persistence", () => {
  it("reads pre-seeded pulls state from localStorage", () => {
    localStorage.setItem(PULLS_KEY, JSON.stringify(["acme/widgets"]));
    const store = createCollapsedReposStore();
    expect(store.isCollapsed("pulls", "acme/widgets")).toBe(true);
    expect(store.isCollapsed("pulls", "acme/tools")).toBe(false);
    expect(store.isCollapsed("issues", "acme/widgets")).toBe(false);
  });

  it("reads pre-seeded issues state from localStorage", () => {
    localStorage.setItem(ISSUES_KEY, JSON.stringify(["acme/tools"]));
    const store = createCollapsedReposStore();
    expect(store.isCollapsed("issues", "acme/tools")).toBe(true);
    expect(store.isCollapsed("pulls", "acme/tools")).toBe(false);
  });

  it("writes only to the surface's own storage key on toggle", () => {
    const store = createCollapsedReposStore();
    store.toggle("pulls", "acme/widgets");

    const pullsRaw = localStorage.getItem(PULLS_KEY);
    const issuesRaw = localStorage.getItem(ISSUES_KEY);
    expect(pullsRaw).not.toBeNull();
    expect(JSON.parse(pullsRaw!)).toEqual(["acme/widgets"]);
    expect(issuesRaw).toBeNull();
  });

  it("persists toggle across store instances via localStorage", () => {
    const first = createCollapsedReposStore();
    first.toggle("pulls", "acme/widgets");

    const second = createCollapsedReposStore();
    expect(second.isCollapsed("pulls", "acme/widgets")).toBe(true);
  });
});

describe("createCollapsedReposStore — error handling", () => {
  it("falls back to an empty set on malformed JSON", () => {
    localStorage.setItem(PULLS_KEY, "{not json");
    const store = createCollapsedReposStore();
    expect(store.isCollapsed("pulls", "acme/widgets")).toBe(false);
    expect(store.isCollapsed("issues", "acme/widgets")).toBe(false);
  });

  it("falls back to an empty set on non-array JSON", () => {
    localStorage.setItem(PULLS_KEY, JSON.stringify({ bad: "shape" }));
    const store = createCollapsedReposStore();
    expect(store.isCollapsed("pulls", "acme/widgets")).toBe(false);
  });

  it("keeps in-memory toggle working when setItem throws", () => {
    const spy = vi.spyOn(Storage.prototype, "setItem").mockImplementation(() => {
      throw new Error("QuotaExceededError");
    });
    const store = createCollapsedReposStore();
    expect(() => store.toggle("pulls", "acme/widgets")).not.toThrow();
    expect(store.isCollapsed("pulls", "acme/widgets")).toBe(true);
    store.toggle("pulls", "acme/widgets");
    expect(store.isCollapsed("pulls", "acme/widgets")).toBe(false);
    spy.mockRestore();
  });

  it("keeps in-memory toggle working when getItem throws at construction", () => {
    const spy = vi.spyOn(Storage.prototype, "getItem").mockImplementation(() => {
      throw new Error("SecurityError");
    });
    const store = createCollapsedReposStore();
    expect(store.isCollapsed("pulls", "acme/widgets")).toBe(false);
    spy.mockRestore();
    expect(() => store.toggle("pulls", "acme/widgets")).not.toThrow();
    expect(store.isCollapsed("pulls", "acme/widgets")).toBe(true);
  });
});
