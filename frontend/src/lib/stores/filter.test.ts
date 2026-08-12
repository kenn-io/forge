import { beforeEach, describe, expect, it } from "vite-plus/test";
import {
  clearGlobalRepoPresetAffinity,
  getGlobalRepo,
  getGlobalRepoPresetAffinity,
  setGlobalRepo,
  setGlobalRepoPresetSelection,
} from "./filter.svelte.js";

describe("global repository preset affinity", () => {
  beforeEach(() => {
    localStorage.clear();
    setGlobalRepoPresetSelection(undefined, undefined);
  });

  it("remembers the source preset while its repository selection is edited", () => {
    setGlobalRepoPresetSelection("Review queue", "github|github.com/acme/widgets,github|github.com/acme/docs");
    setGlobalRepo("github|github.com/acme/widgets");

    expect(getGlobalRepoPresetAffinity()).toBe("Review queue");
    expect(getGlobalRepo()).toBe("github|github.com/acme/widgets");
  });

  it("clears a deleted preset affinity without clearing the repository selection", () => {
    setGlobalRepoPresetSelection("Review queue", "github|github.com/acme/widgets");

    clearGlobalRepoPresetAffinity("Review queue");

    expect(getGlobalRepoPresetAffinity()).toBeUndefined();
    expect(getGlobalRepo()).toBe("github|github.com/acme/widgets");
  });

  it("selecting Global clears both selection and affinity", () => {
    setGlobalRepoPresetSelection("Review queue", "github|github.com/acme/widgets");

    setGlobalRepoPresetSelection(undefined, undefined);

    expect(getGlobalRepoPresetAffinity()).toBeUndefined();
    expect(getGlobalRepo()).toBeUndefined();
  });
});
