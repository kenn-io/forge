import { afterEach, beforeEach, describe, expect, it, vi } from "vite-plus/test";

import {
  KATA_WORKSPACE_STATE_STORAGE_KEY,
  clearKataWorkspaceSelection,
  clearKataWorkspaceState,
  loadKataWorkspaceState,
  saveKataWorkspaceState,
} from "./kataWorkspacePersistence.js";

const home = {
  view: "today" as const,
  filters: {
    scope: { kind: "project" as const, project_uid: "project-kata" },
    status: "all" as const,
    owner: "Susan",
    label: "work",
    query: "q3",
  },
  selectedIssueUID: "issue-child",
};

beforeEach(() => window.localStorage.clear());
afterEach(() => vi.restoreAllMocks());

describe("Kata workspace persistence", () => {
  it("round-trips independent daemon snapshots", () => {
    saveKataWorkspaceState("home", home);
    saveKataWorkspaceState("work", {
      view: "inbox",
      filters: { scope: { kind: "all" }, status: "open", owner: "", label: "", query: "" },
      selectedIssueUID: "work-issue",
    });

    expect(loadKataWorkspaceState("home")).toEqual(home);
    expect(loadKataWorkspaceState("work")?.selectedIssueUID).toBe("work-issue");
  });

  it("round-trips daemon IDs that collide with object prototypes", () => {
    saveKataWorkspaceState("__proto__", home);
    saveKataWorkspaceState("constructor", { ...home, selectedIssueUID: "constructor-issue" });
    saveKataWorkspaceState("toString", { ...home, selectedIssueUID: "string-issue" });

    clearKataWorkspaceSelection("__proto__");
    clearKataWorkspaceState("constructor");

    expect(loadKataWorkspaceState("__proto__")).toEqual({ ...home, selectedIssueUID: null });
    expect(loadKataWorkspaceState("constructor")).toBeNull();
    expect(loadKataWorkspaceState("toString")?.selectedIssueUID).toBe("string-issue");
  });

  it.each([
    "not json",
    JSON.stringify({ version: 2, daemons: {} }),
    JSON.stringify({ version: 1, daemons: { home: { view: "invalid" } } }),
    JSON.stringify({ version: 1, daemons: { home: { ...home, filters: { ...home.filters, status: "later" } } } }),
  ])("ignores malformed or incompatible data: %s", (raw) => {
    window.localStorage.setItem(KATA_WORKSPACE_STATE_STORAGE_KEY, raw);

    expect(loadKataWorkspaceState("home")).toBeNull();
  });

  it.each(["not json", JSON.stringify({ version: 2, daemons: { work: home } })])(
    "replaces corrupt or incompatible top-level data only on a valid save: %s",
    (raw) => {
      window.localStorage.setItem(KATA_WORKSPACE_STATE_STORAGE_KEY, raw);
      expect(loadKataWorkspaceState("home")).toBeNull();
      expect(window.localStorage.getItem(KATA_WORKSPACE_STATE_STORAGE_KEY)).toBe(raw);

      saveKataWorkspaceState("home", home);

      expect(JSON.parse(window.localStorage.getItem(KATA_WORKSPACE_STATE_STORAGE_KEY)!)).toEqual({
        version: 1,
        daemons: { home },
      });
    },
  );

  it("clears only a stale selection", () => {
    saveKataWorkspaceState("home", home);

    clearKataWorkspaceSelection("home");

    expect(loadKataWorkspaceState("home")).toEqual({ ...home, selectedIssueUID: null });
  });

  it("drops an invalid daemon entry while retaining a valid sibling", () => {
    window.localStorage.setItem(
      KATA_WORKSPACE_STATE_STORAGE_KEY,
      JSON.stringify({
        version: 1,
        daemons: { home: { ...home, view: "invalid" }, work: { ...home, selectedIssueUID: "work-issue" } },
      }),
    );

    expect(loadKataWorkspaceState("home")).toBeNull();
    expect(loadKataWorkspaceState("work")?.selectedIssueUID).toBe("work-issue");
  });

  it("deletes only one daemon snapshot", () => {
    saveKataWorkspaceState("home", home);
    saveKataWorkspaceState("work", { ...home, selectedIssueUID: "work-issue" });

    clearKataWorkspaceState("home");

    expect(loadKataWorkspaceState("home")).toBeNull();
    expect(loadKataWorkspaceState("work")?.selectedIssueUID).toBe("work-issue");
  });

  it("removes invalid entries when clearing an absent daemon", () => {
    window.localStorage.setItem(
      KATA_WORKSPACE_STATE_STORAGE_KEY,
      JSON.stringify({ version: 1, daemons: { home, stale: { view: "invalid" } } }),
    );

    clearKataWorkspaceState("stale");

    expect(JSON.parse(window.localStorage.getItem(KATA_WORKSPACE_STATE_STORAGE_KEY)!)).toEqual({
      version: 1,
      daemons: { home },
    });
  });

  it("does not return aliases of stored snapshots", () => {
    saveKataWorkspaceState("home", home);
    const loaded = loadKataWorkspaceState("home");
    loaded!.filters.scope = { kind: "all" };
    loaded!.filters.owner = "Changed";

    expect(loadKataWorkspaceState("home")).toEqual(home);
  });

  it("does not overwrite sibling snapshots when reading storage fails", () => {
    saveKataWorkspaceState("home", home);
    const getItem = vi.spyOn(Storage.prototype, "getItem").mockImplementation(() => {
      throw new Error("blocked");
    });
    const setItem = vi.spyOn(Storage.prototype, "setItem");

    saveKataWorkspaceState("work", { ...home, selectedIssueUID: "work-issue" });

    expect(setItem).not.toHaveBeenCalled();
    getItem.mockRestore();
    expect(loadKataWorkspaceState("home")).toEqual(home);
    expect(loadKataWorkspaceState("work")).toBeNull();
  });

  it("keeps the session usable when storage throws", () => {
    vi.spyOn(Storage.prototype, "getItem").mockImplementation(() => {
      throw new Error("blocked");
    });
    expect(loadKataWorkspaceState("home")).toBeNull();

    vi.restoreAllMocks();
    vi.spyOn(Storage.prototype, "setItem").mockImplementation(() => {
      throw new Error("quota");
    });
    expect(() => saveKataWorkspaceState("home", home)).not.toThrow();
  });
});
