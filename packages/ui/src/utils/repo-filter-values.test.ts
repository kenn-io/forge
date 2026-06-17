import { describe, expect, it } from "vite-plus/test";

import {
  canonicalRepoFilterValue,
  displayRepoFilterValue,
  normalizeRepoFilterSelection,
  normalizeRepoFilterValue,
  type RepoFilterIdentity,
} from "./repo-filter-values.js";

const widgets = {
  provider: "github",
  platformHost: "github.com",
  repoPath: "acme/widgets",
  isGlob: false,
};

describe("repo filter values", () => {
  it("uses provider-qualified canonical values when provider identities collide", () => {
    const repos: RepoFilterIdentity[] = [widgets, { ...widgets, provider: "gitea" }];

    expect(canonicalRepoFilterValue(repos[0]!, repos)).toBe("github|github.com/acme/widgets");
    expect(canonicalRepoFilterValue(repos[1]!, repos)).toBe("gitea|github.com/acme/widgets");
  });

  it("uses host-qualified canonical values when provider identities no longer collide", () => {
    expect(canonicalRepoFilterValue(widgets, [widgets])).toBe("github.com/acme/widgets");
  });

  it("normalizes stale slash-qualified provider values to pipe values while a collision exists", () => {
    const repos: RepoFilterIdentity[] = [widgets, { ...widgets, provider: "gitea" }];

    expect(normalizeRepoFilterValue("gitea/github.com/acme/widgets", repos)).toBe("gitea|github.com/acme/widgets");
  });

  it("normalizes stale slash-qualified provider values back to host-qualified values after a collision is removed", () => {
    expect(normalizeRepoFilterValue("github/github.com/acme/widgets", [widgets])).toBe("github.com/acme/widgets");
  });

  it("normalizes stale pipe-qualified provider values back to host-qualified values after a collision is removed", () => {
    expect(normalizeRepoFilterValue("github|github.com/acme/widgets", [widgets])).toBe("github.com/acme/widgets");
  });

  it("normalizes each value in a comma-separated filter independently", () => {
    const repos: RepoFilterIdentity[] = [
      widgets,
      { ...widgets, provider: "gitea" },
      {
        provider: "github",
        platformHost: "github.com",
        repoPath: "acme/api",
      },
    ];

    expect(
      normalizeRepoFilterSelection(
        "gitea/github.com/acme/widgets,github/github.com/acme/widgets,github.com/acme/api",
        repos,
      ),
    ).toBe("gitea|github.com/acme/widgets,github|github.com/acme/widgets,github.com/acme/api");
  });

  it("keeps slash-qualified selections when they match a current host-qualified option", () => {
    const repos = [
      {
        provider: "github",
        platformHost: "gitea",
        repoPath: "github.com/acme/widgets",
      },
    ];

    expect(normalizeRepoFilterValue("gitea/github.com/acme/widgets", repos)).toBe("gitea/github.com/acme/widgets");
  });

  it("displays pipe-qualified values as slash-qualified labels", () => {
    expect(displayRepoFilterValue("gitea|github.com/acme/widgets")).toBe("gitea/github.com/acme/widgets");
  });
});
