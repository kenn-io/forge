import { describe, expect, it } from "vite-plus/test";
import { findMatchingRepoPreset, preferredRepoPreset, projectRepoPresetSelection } from "./repo-presets.js";

const presets = [
  {
    name: "Review queue",
    repos: ["github|github.com/acme/widgets", "gitlab|git.example.com/group/project"],
  },
  {
    name: "Docs",
    repos: ["github|github.com/acme/docs"],
  },
];

describe("repository presets", () => {
  it("matches an ad hoc selection to a preset independent of selection order", () => {
    expect(
      findMatchingRepoPreset(presets, "gitlab|git.example.com/group/project,github|github.com/acme/widgets")?.name,
    ).toBe("Review queue");
  });

  it("projects unavailable repositories out without rewriting the preset", () => {
    expect(projectRepoPresetSelection(presets[0]!, ["github|github.com/acme/widgets"])).toBe(
      "github|github.com/acme/widgets",
    );
    expect(presets[0]!.repos).toEqual(["github|github.com/acme/widgets", "gitlab|git.example.com/group/project"]);
  });

  it("uses an exact match before the source preset and retains the source for variations", () => {
    expect(preferredRepoPreset(presets, "github|github.com/acme/docs", "Review queue")?.name).toBe("Docs");
    expect(preferredRepoPreset(presets, "github|github.com/acme/other", "Review queue")?.name).toBe("Review queue");
  });

  it("has no custom preset for Global", () => {
    expect(findMatchingRepoPreset(presets, undefined)).toBeUndefined();
    expect(preferredRepoPreset(presets, undefined, undefined)).toBeUndefined();
  });
});
