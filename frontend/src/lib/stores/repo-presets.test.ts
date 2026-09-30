import { describe, expect, it } from "vite-plus/test";
import {
  findMatchingRepoPreset,
  preferredRepoPreset,
  projectRepoPresetSelection,
  repoPresetRepositoriesForSelection,
  type RepoPresetCatalogEntry,
} from "./repo-presets.js";
import { repositoryKeyToRequiredWire } from "../api/repository-key.js";

const catalog: RepoPresetCatalogEntry[] = [
  {
    value: "github|github.com/acme/widgets",
    provider: "github",
    platform_host: "github.com",
    repositoryKey: { kind: "id", id: 1102 },
    repo_path: "acme/widgets",
  },
  {
    value: "gitlab|git.example.com/group/project",
    provider: "gitlab",
    platform_host: "git.example.com",
    repositoryKey: { kind: "id", id: 42 },
    repo_path: "group/project",
  },
  {
    value: "github|github.com/acme/docs",
    provider: "github",
    platform_host: "github.com",
    repositoryKey: { kind: "id", id: 1108 },
    repo_path: "acme/docs",
  },
];

function persisted(entry: RepoPresetCatalogEntry) {
  return {
    provider: entry.provider,
    platform_host: entry.platform_host,
    repo_path: entry.repo_path,
    ...repositoryKeyToRequiredWire(entry.repositoryKey!),
  };
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

  it("saves and resolves a Bitbucket Cloud repository by UUID", () => {
    const uuid = "0f5d2a4e-3b1c-4d7e-9a8b-1c2d3e4f5a6b";
    const cloud: RepoPresetCatalogEntry = {
      value: "bitbucket|bitbucket.org/team/cloud-app",
      provider: "bitbucket",
      platform_host: "bitbucket.org",
      repo_path: "team/cloud-app",
      repositoryKey: { kind: "uuid", uuid },
    };
    const saved = repoPresetRepositoriesForSelection(cloud.value, [cloud]);
    expect(saved).toEqual([
      {
        provider: "bitbucket",
        platform_host: "bitbucket.org",
        platform_repo_id: 0,
        bitbucket_repository_uuid: uuid,
        repo_path: "team/cloud-app",
      },
    ]);
    const renamed = { ...cloud, value: "bitbucket|bitbucket.org/team/renamed", repo_path: "team/renamed" };
    expect(projectRepoPresetSelection({ name: "Cloud", repos: saved! }, [renamed])).toBe(renamed.value);
  });

  it("refuses to save a selection without provider-verified identity", () => {
    expect(
      repoPresetRepositoriesForSelection("github|github.com/acme/widgets", [
        { ...catalog[0]!, repositoryKey: undefined },
      ]),
    ).toBeUndefined();
  });
});
