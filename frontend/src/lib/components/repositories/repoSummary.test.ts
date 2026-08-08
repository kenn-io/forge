import { describe, expect, it } from "vite-plus/test";
import type { RepoSummary } from "../../api/types.js";

import {
  defaultProviderCapabilities,
  normalizeSummaries,
  providerRepoStateKey,
  repoKey,
  shouldShowPlatformHost,
} from "./repoSummary.js";

describe("repo summary labels", () => {
  it("canonicalizes provider aliases while preserving case-sensitive repository paths", () => {
    const stored = {
      provider: "fj",
      platform_host: "CODEBERG.org",
      repo_path: "MixedCase/Service",
    };

    expect(providerRepoStateKey(stored)).toBe("forgejo|codeberg.org/MixedCase/Service");
    expect(
      providerRepoStateKey({
        provider: "forgejo",
        platform_host: "codeberg.org",
        repo_path: "MixedCase/Service",
      }),
    ).toBe(providerRepoStateKey(stored));
    expect(stored.repo_path).toBe("MixedCase/Service");
  });

  it("case-folds GitHub repository paths for identity comparison", () => {
    expect(
      providerRepoStateKey({
        provider: "gh",
        platform_host: "GITHUB.COM",
        repo_path: "Acme/Widgets",
      }),
    ).toBe("github|github.com/acme/widgets");
  });

  it("hides github.com when it is the default platform host", () => {
    const summary = {
      platform_host: "github.com",
      default_platform_host: "github.com",
      owner: "acme",
      name: "widgets",
    };

    expect(shouldShowPlatformHost(summary)).toBe(false);
    expect(repoKey(summary)).toBe("acme/widgets");
  });

  it("hides a configured non-github default platform host", () => {
    const summary = {
      platform_host: "ghe.example.com",
      default_platform_host: "ghe.example.com",
      owner: "acme",
      name: "widgets",
    };

    expect(shouldShowPlatformHost(summary)).toBe(false);
    expect(repoKey(summary)).toBe("acme/widgets");
  });

  it("includes a non-default platform host in repository labels", () => {
    const summary = {
      platform_host: "ghe.example.com",
      default_platform_host: "github.com",
      owner: "acme",
      name: "widgets",
    };

    expect(shouldShowPlatformHost(summary)).toBe(true);
    expect(repoKey(summary)).toBe("ghe.example.com/acme/widgets");
  });

  it("defaults missing repo capabilities when provider identity is present", () => {
    const [summary] = normalizeSummaries([
      {
        owner: "acme",
        name: "widgets",
        platform_host: "github.com",
        repo: {
          provider: "github",
          platform_host: "github.com",
          owner: "acme",
          name: "widgets",
          repo_path: "acme/widgets",
        },
        default_platform_host: "github.com",
        cached_pr_count: 0,
        open_pr_count: 0,
        draft_pr_count: 0,
        cached_issue_count: 0,
        open_issue_count: 0,
        active_authors: null,
        recent_issues: null,
        commit_timeline: null,
        releases: null,
      } as unknown as RepoSummary,
    ]);

    expect(summary).toBeDefined();
    if (!summary) throw new Error("summary missing");
    expect(summary.repo.capabilities).toEqual(defaultProviderCapabilities);
    expect(summary.repo.repo_path).toBe("acme/widgets");
  });

  it("preserves provider capability flags from the API", () => {
    const [summary] = normalizeSummaries([
      {
        owner: "group",
        name: "project",
        platform_host: "gitlab.com",
        default_platform_host: "github.com",
        cached_pr_count: 0,
        open_pr_count: 0,
        draft_pr_count: 0,
        cached_issue_count: 0,
        open_issue_count: 0,
        active_authors: null,
        recent_issues: null,
        commit_timeline: null,
        releases: null,
        repo: {
          provider: "gitlab",
          platform_host: "gitlab.com",
          repo_path: "group/project",
          owner: "group",
          name: "project",
          capabilities: {
            ...defaultProviderCapabilities,
            issue_mutation: false,
          },
        },
      } as unknown as RepoSummary,
    ]);

    expect(summary).toBeDefined();
    if (!summary) throw new Error("summary missing");
    expect(summary.repo.capabilities.issue_mutation).toBe(false);
  });
});
