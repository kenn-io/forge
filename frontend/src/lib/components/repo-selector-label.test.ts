import { describe, expect, it } from "vite-plus/test";

import { repoSelectorLabel, type RepoSelectorOption } from "./repo-selector-label.js";

const forgeRepo: RepoSelectorOption = {
  value: "github|github.com/example-labs/atlas",
  provider: "github",
  platformHost: "github.com",
  owner: "example-labs",
  name: "atlas",
  repoPath: "example-labs/atlas",
};

describe("repository selector labels", () => {
  it("shows owner/repository while retaining the full canonical identity", () => {
    expect(repoSelectorLabel(forgeRepo.value, [forgeRepo])).toEqual({
      primary: "example-labs/atlas",
      owner: "example-labs",
      name: "atlas",
      full: "github/github.com/example-labs/atlas",
    });
  });

  it("derives the human label before repository metadata has loaded", () => {
    expect(repoSelectorLabel(forgeRepo.value, [])).toEqual({
      primary: "example-labs/atlas",
      owner: "example-labs",
      name: "atlas",
      full: "github/github.com/example-labs/atlas",
    });
  });

  it("adds a visible qualifier only when owner/repository is ambiguous", () => {
    const mirror = {
      ...forgeRepo,
      value: "github|git.example.test/example-labs/atlas",
      platformHost: "git.example.test",
    };

    expect(repoSelectorLabel(forgeRepo.value, [forgeRepo, mirror])).toEqual({
      primary: "example-labs/atlas",
      owner: "example-labs",
      name: "atlas",
      qualifier: "github.com",
      full: "github/github.com/example-labs/atlas",
    });
  });

  it("leaves ordinary local project labels unchanged", () => {
    expect(repoSelectorLabel("local-projects/field-notes", [])).toEqual({
      primary: "local-projects/field-notes",
      full: "local-projects/field-notes",
    });
  });
});
