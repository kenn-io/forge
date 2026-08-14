import { describe, expect, it } from "vite-plus/test";
import {
  findMatchingRepoPreset,
  preferredRepoPreset,
  projectRepoPresetSelection,
  repoPresetRepositoriesForSelection,
  type RepoPresetCatalogEntry,
} from "./repo-presets.js";

const catalog: RepoPresetCatalogEntry[] = [
  {
    value: "github|github.com/acme/widgets",
    provider: "github",
    platform_host: "github.com",
    platform_repo_id: "R_widgets",
    repo_path: "acme/widgets",
  },
  {
    value: "gitlab|git.example.com/group/project",
    provider: "gitlab",
    platform_host: "git.example.com",
    platform_repo_id: "42",
    repo_path: "group/project",
  },
  {
    value: "github|github.com/acme/docs",
    provider: "github",
    platform_host: "github.com",
    platform_repo_id: "R_docs",
    repo_path: "acme/docs",
  },
];

function persisted(entry: RepoPresetCatalogEntry) {
  const { value: _, ...repo } = entry;
  return repo;
}

const presets = [
  { name: "Review queue", repos: [persisted(catalog[0]!), persisted(catalog[1]!)] },
  { name: "Docs", repos: [persisted(catalog[2]!)] },
];

describe("repository presets", () => {
  it("matches an ad hoc selection to a preset independent of selection order", () => {
    expect(
      findMatchingRepoPreset(presets, "gitlab|git.example.com/group/project,github|github.com/acme/widgets", catalog)
        ?.name,
    ).toBe("Review queue");
  });

  it("projects unavailable repositories out without rewriting the preset", () => {
    expect(projectRepoPresetSelection(presets[0]!, [catalog[0]!])).toBe("github|github.com/acme/widgets");
    expect(presets[0]!.repos).toHaveLength(2);
  });

  it("uses an exact match before the source preset and retains the source for variations", () => {
    expect(preferredRepoPreset(presets, "github|github.com/acme/docs", "Review queue", catalog)?.name).toBe("Docs");
    expect(preferredRepoPreset(presets, "github|github.com/acme/other", "Review queue", catalog)?.name).toBe(
      "Review queue",
    );
  });

  it("has no custom preset for Global", () => {
    expect(findMatchingRepoPreset(presets, undefined, catalog)).toBeUndefined();
    expect(preferredRepoPreset(presets, undefined, undefined, catalog)).toBeUndefined();
  });

  it("uses affinity to disambiguate identical repository sets", () => {
    const duplicate = { ...presets[0]!, name: "Urgent" };
    expect(
      findMatchingRepoPreset(
        [...presets, duplicate],
        "github|github.com/acme/widgets,gitlab|git.example.com/group/project",
        catalog,
        "Urgent",
      )?.name,
    ).toBe("Urgent");
  });

  it("resolves a renamed repository by stable provider identity", () => {
    const renamed = [{ ...catalog[0]!, value: "github|github.com/acme/renamed", repo_path: "acme/renamed" }];
    expect(projectRepoPresetSelection({ name: "Old route", repos: [presets[0]!.repos[0]!] }, renamed)).toBe(
      "github|github.com/acme/renamed",
    );
  });

  it("refuses to save a selection without provider-verified identity", () => {
    expect(
      repoPresetRepositoriesForSelection("github|github.com/acme/widgets", [{ ...catalog[0]!, platform_repo_id: "" }]),
    ).toBeUndefined();
  });
});
