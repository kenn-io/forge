import { afterEach, beforeEach, describe, expect, it, vi } from "vite-plus/test";

import {
  readDeleteWorkspaceAfterMergePreference,
  writeDeleteWorkspaceAfterMergePreference,
} from "./mergeWorkspaceCleanupPreference.js";

const STORAGE_KEY = "kenn-forge:merge:delete-workspace-after-merge";

describe("merge workspace cleanup preference", () => {
  beforeEach(() => {
    localStorage.clear();
  });

  afterEach(() => {
    vi.restoreAllMocks();
  });

  it("defaults to checked when no preference has been saved", () => {
    expect(readDeleteWorkspaceAfterMergePreference()).toBe(true);
  });

  it.each([
    ["true", true],
    ["false", false],
    ["malformed", true],
  ])("reads %s as %s", (stored, expected) => {
    localStorage.setItem(STORAGE_KEY, stored);

    expect(readDeleteWorkspaceAfterMergePreference()).toBe(expected);
  });

  it("defaults to checked when storage reads fail", () => {
    vi.spyOn(Storage.prototype, "getItem").mockImplementation(() => {
      throw new Error("storage unavailable");
    });

    expect(readDeleteWorkspaceAfterMergePreference()).toBe(true);
  });

  it("persists boolean choices as strings", () => {
    writeDeleteWorkspaceAfterMergePreference(false);
    expect(localStorage.getItem(STORAGE_KEY)).toBe("false");

    writeDeleteWorkspaceAfterMergePreference(true);
    expect(localStorage.getItem(STORAGE_KEY)).toBe("true");
  });

  it("does not throw when storage writes fail", () => {
    vi.spyOn(Storage.prototype, "setItem").mockImplementation(() => {
      throw new Error("storage full");
    });

    expect(() => writeDeleteWorkspaceAfterMergePreference(false)).not.toThrow();
  });
});
