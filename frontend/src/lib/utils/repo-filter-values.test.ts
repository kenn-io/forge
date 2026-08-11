import { describe, expect, it } from "vite-plus/test";

import {
  canonicalRepoFilterValue,
  displayRepoFilterValue,
  interactiveRepoFilterIdentities,
  normalizeGlobalRepoSelection,
  normalizeInteractiveRepoFilterSelection,
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

  it("uses provider-qualified canonical values when provider identities do not collide", () => {
    expect(canonicalRepoFilterValue(widgets, [widgets])).toBe("github|github.com/acme/widgets");
  });

  it("drops slash-qualified provider values while a collision exists", () => {
    const repos: RepoFilterIdentity[] = [widgets, { ...widgets, provider: "gitea" }];

    expect(normalizeRepoFilterValue("gitea/github.com/acme/widgets", repos)).toBe("");
  });

  it("drops slash-qualified provider values without a collision", () => {
    expect(normalizeRepoFilterValue("github/github.com/acme/widgets", [widgets])).toBe("");
  });

  it("keeps pipe-qualified provider values after a collision is removed", () => {
    expect(normalizeRepoFilterValue("github|github.com/acme/widgets", [widgets])).toBe(
      "github|github.com/acme/widgets",
    );
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
        "gitea|github.com/acme/widgets,github|github.com/acme/widgets,github.com/acme/api",
        repos,
      ),
    ).toBe("gitea|github.com/acme/widgets,github|github.com/acme/widgets");
  });

  it("drops slash-qualified selections when they match a current host-qualified option", () => {
    const repos = [
      {
        provider: "github",
        platformHost: "gitea",
        repoPath: "github.com/acme/widgets",
      },
    ];

    expect(normalizeRepoFilterValue("gitea/github.com/acme/widgets", repos)).toBe("");
  });

  it("displays pipe-qualified values as slash-qualified labels", () => {
    expect(displayRepoFilterValue("gitea|github.com/acme/widgets")).toBe("gitea/github.com/acme/widgets");
  });

  it("clears selections that point at server-hidden repositories", () => {
    const repos = [
      {
        provider: "github",
        platform_host: "github.com",
        owner: "acme",
        name: "widgets",
        repo_path: "acme/widgets",
        is_glob: false,
        matched_repo_count: 1,
        hidden_from_ui: false,
      },
      {
        provider: "github",
        platform_host: "github.com",
        owner: "acme",
        name: "archive",
        repo_path: "acme/archive",
        is_glob: false,
        matched_repo_count: 1,
        hidden_from_ui: true,
      },
    ];

    expect(normalizeInteractiveRepoFilterSelection("github|github.com/acme/archive", repos)).toBeUndefined();
    expect(
      normalizeInteractiveRepoFilterSelection("github|github.com/acme/archive,github|github.com/acme/widgets", repos),
    ).toBe("github|github.com/acme/widgets");
  });

  it("clears selections keyed to a hidden repository's renamed current route", () => {
    const repos = [
      {
        provider: "github",
        platform_host: "github.com",
        owner: "acme",
        name: "archive",
        repo_path: "acme/archive",
        tracked_repo_path: "acme-renamed/archive-renamed",
        is_glob: false,
        matched_repo_count: 1,
        hidden_from_ui: true,
      },
    ];

    expect(
      normalizeInteractiveRepoFilterSelection("github|github.com/acme-renamed/archive-renamed", repos),
    ).toBeUndefined();
    expect(normalizeInteractiveRepoFilterSelection("github|github.com/acme/archive", repos)).toBeUndefined();
  });

  it("preserves a host-pinned scope even when the pinned repository is hidden", () => {
    // With ui.hideRepoSelector there is no picker to rescope with; dropping
    // the pinned selection would silently unscope every request.
    const repos = [
      {
        provider: "github",
        platform_host: "github.com",
        owner: "acme",
        name: "archive",
        repo_path: "acme/archive",
        is_glob: false,
        matched_repo_count: 1,
        hidden_from_ui: true,
      },
    ];

    expect(normalizeGlobalRepoSelection("github|github.com/acme/archive", repos, true)).toBe(
      "github|github.com/acme/archive",
    );
    expect(normalizeGlobalRepoSelection("github|github.com/acme/archive", repos, false)).toBeUndefined();
  });

  it("keeps glob-resolved selections that have no configured entry", () => {
    const repos = [
      {
        provider: "github",
        platform_host: "github.com",
        owner: "acme",
        name: "archive",
        repo_path: "acme/archive",
        is_glob: false,
        matched_repo_count: 1,
        hidden_from_ui: true,
      },
    ];

    expect(normalizeInteractiveRepoFilterSelection("github|github.com/acme/glob-child", repos)).toBe(
      "github|github.com/acme/glob-child",
    );
  });

  it("builds interactive filter identities without server-hidden entries", () => {
    const identities = interactiveRepoFilterIdentities([
      {
        provider: "github",
        platform_host: "github.com",
        owner: "acme",
        name: "widgets",
        repo_path: "acme/widgets",
        is_glob: false,
        matched_repo_count: 1,
        hidden_from_ui: false,
      },
      {
        provider: "github",
        platform_host: "github.com",
        owner: "acme",
        name: "archive",
        repo_path: "acme/archive",
        is_glob: false,
        matched_repo_count: 1,
        hidden_from_ui: true,
      },
    ]);

    expect(identities).toEqual([
      {
        provider: "github",
        platformHost: "github.com",
        repoPath: "acme/widgets",
        isGlob: false,
      },
    ]);
  });
});
