import { describe, expect, it } from "vite-plus/test";

import type { ConfigRepo } from "../api/types.js";
import { unresolvedInteractiveConfigRepos } from "./repo-visibility.js";

const repo = (name: string, matchedRepoCount: number): ConfigRepo => ({
  provider: "github",
  platform_host: "github.com",
  owner: "acme",
  name,
  repo_path: `acme/${name}`,
  is_glob: false,
  matched_repo_count: matchedRepoCount,
});

describe("unresolvedInteractiveConfigRepos", () => {
  it("does not restore a resolved stale path that the catalog filtered out", () => {
    const staleExact = repo("legacy-service", 1);
    const unresolvedExact = repo("not-imported-yet", 0);

    expect(
      unresolvedInteractiveConfigRepos([
        staleExact,
        unresolvedExact,
        {
          ...repo("archive-*", 1),
          is_glob: true,
          hide_from_ui: true,
        },
      ]),
    ).toEqual([unresolvedExact]);
  });
});
