import { Effect, Exit } from "effect";
import { describe, expect, it, vi } from "vite-plus/test";
import { GeneratedProblemResponse } from "../api/runtime.js";
import { makeAppRuntime } from "../app/runtime.js";
import { makeGeneratedClient } from "../testing/generated-client.js";
import type { ConfigRepo, Issue, PullRequest, RepoCatalog } from "../api/types.js";
import { ItemPreviewCache, loadItemPreviewEffect, type ItemPreviewOutcome } from "./item-preview.js";

function trackedRepo(overrides: Partial<RepoCatalog> = {}): RepoCatalog {
  return {
    ID: 1,
    PlatformRepoID: 1001,
    Owner: "acme",
    Name: "widgets",
    Platform: "github",
    PlatformHost: "github.com",
    AllowSquashMerge: true,
    AllowMergeCommit: true,
    AllowRebaseMerge: true,
    ViewerCanMerge: true,
    LastSyncStartedAt: null,
    LastSyncCompletedAt: null,
    LastSyncError: "",
    CreatedAt: "2026-03-01T00:00:00Z",
    ...overrides,
  };
}

function samplePull(overrides: Partial<PullRequest> = {}): PullRequest {
  return {
    ID: 101,
    RepoID: 1,
    PlatformID: 2001,
    PlatformExternalID: "",
    Number: 42,
    URL: "https://github.com/acme/widgets/pull/42",
    Title: "Add retry jitter",
    Author: "alice",
    AuthorDisplayName: "Alice",
    State: "open",
    IsDraft: false,
    IsLocked: false,
    Body: "Adds bounded jitter to retries.",
    HeadBranch: "feat/retry-jitter",
    BaseBranch: "main",
    HeadRepoCloneURL: "",
    Additions: 48,
    Deletions: 12,
    FilesChanged: 3,
    MergeCommitSHA: "",
    CommentCount: 2,
    ReviewDecision: "APPROVED",
    CIStatus: "success",
    CIChecksJSON: "[]",
    CIHadPending: false,
    MergeableState: "clean",
    MergeRules: "",
    CreatedAt: "2026-03-29T14:00:00Z",
    UpdatedAt: "2026-03-30T14:00:00Z",
    LastActivityAt: "2026-03-30T14:00:00Z",
    MergedAt: null,
    ClosedAt: null,
    Starred: false,
    detail_loaded: true,
    head_repo_kind: "same_repo",
    platform_host: "github.com",
    repo_owner: "acme",
    repo_name: "widgets",
    repo: {
      provider: "github",
      platform_host: "github.com",
      repo_path: "acme/widgets",
      owner: "acme",
      name: "widgets",
      capabilities: {
        read_repositories: true,
        read_merge_requests: true,
        read_issues: true,
        read_comments: true,
      },
    },
    worktree_links: [],
    ...overrides,
  } as PullRequest;
}

function sampleIssue(overrides: Partial<Issue> = {}): Issue {
  return {
    ID: 201,
    RepoID: 1,
    PlatformID: 3001,
    PlatformExternalID: "",
    Number: 17,
    URL: "https://github.com/acme/widgets/issues/17",
    Title: "Retry loop spins on 429",
    Author: "bob",
    State: "open",
    Body: "Observed tight retries during rate limiting.",
    CommentCount: 4,
    CreatedAt: "2026-03-28T10:00:00Z",
    UpdatedAt: "2026-03-29T10:00:00Z",
    LastActivityAt: "2026-03-29T10:00:00Z",
    ClosedAt: null,
    Starred: false,
    detail_loaded: true,
    platform_host: "github.com",
    repo_owner: "acme",
    repo_name: "widgets",
    repo: {
      provider: "github",
      platform_host: "github.com",
      repo_path: "acme/widgets",
      owner: "acme",
      name: "widgets",
      capabilities: {
        read_repositories: true,
        read_merge_requests: true,
        read_issues: true,
        read_comments: true,
      },
    },
    ...overrides,
  } as Issue;
}

async function runPreview(
  client: ReturnType<typeof makeGeneratedClient>,
  ref: Parameters<typeof loadItemPreviewEffect>[0],
  cache?: ItemPreviewCache,
  configuredRepos?: ReadonlyArray<ConfigRepo>,
): Promise<ItemPreviewOutcome> {
  const runtime = makeAppRuntime(client);
  try {
    const exit = await runtime.runCommand(loadItemPreviewEffect(ref, cache, configuredRepos), {
      operation: "test item preview",
      safeContext: {},
      onFailure: () => {},
    }).exit;
    if (!Exit.isSuccess(exit)) throw new Error("expected preview effect to succeed with typed outcome");
    return exit.value;
  } finally {
    await Effect.runPromise(runtime.disposeEffect);
  }
}

describe("loadItemPreviewEffect", () => {
  it("loads a tracked pull request from local listPulls without calling resolveRepoItem", async () => {
    const pull = samplePull();
    const listPulls = vi.fn().mockResolvedValue([pull]);
    const resolveRepoItem = vi.fn();
    const client = makeGeneratedClient({
      RepositoriesService: { listRepos: vi.fn().mockResolvedValue([trackedRepo()]), resolveRepoItem },
      PullRequestsService: { listPulls },
    });

    const outcome = await runPreview(client, {
      provider: "github",
      platformHost: "github.com",
      owner: "acme",
      name: "widgets",
      repoPath: "acme/widgets",
      number: 42,
      itemType: "pr",
    });

    expect(outcome).toEqual({ status: "found", preview: { kind: "pr", pull } });
    expect(listPulls).toHaveBeenCalledWith(
      { repo: "github|github.com/acme/widgets", numbers: [42], state: "all", limit: 1 },
      expect.objectContaining({ signal: expect.any(AbortSignal) }),
    );
    expect(resolveRepoItem).not.toHaveBeenCalled();
  });

  it("skips untracked repositories with zero item or resolve requests", async () => {
    const listPulls = vi.fn();
    const listIssues = vi.fn();
    const resolveRepoItem = vi.fn();
    const client = makeGeneratedClient({
      RepositoriesService: { listRepos: vi.fn().mockResolvedValue([trackedRepo()]), resolveRepoItem },
      PullRequestsService: { listPulls },
      IssuesService: { listIssues },
    });

    const outcome = await runPreview(client, {
      provider: "github",
      platformHost: "github.com",
      owner: "other",
      name: "repo",
      repoPath: "other/repo",
      number: 99,
      itemType: "pr",
    });

    expect(outcome).toEqual({ status: "untracked" });
    expect(listPulls).not.toHaveBeenCalled();
    expect(listIssues).not.toHaveBeenCalled();
    expect(resolveRepoItem).not.toHaveBeenCalled();
  });

  it("falls back to resolveRepoItem when a tracked item is not yet synced locally", async () => {
    const pull = samplePull({ Number: 104, Title: "Configure cache pool", State: "merged" });
    const listPulls = vi.fn().mockResolvedValueOnce([]).mockResolvedValueOnce([pull]);
    const resolveRepoItem = vi.fn().mockResolvedValue({ item_type: "pr", number: 104, repo_tracked: true });
    const client = makeGeneratedClient({
      RepositoriesService: { listRepos: vi.fn().mockResolvedValue([trackedRepo()]), resolveRepoItem },
      PullRequestsService: { listPulls },
    });

    const outcome = await runPreview(client, {
      provider: "github",
      platformHost: "github.com",
      owner: "acme",
      name: "widgets",
      repoPath: "acme/widgets",
      number: 104,
      itemType: "pr",
    });

    expect(outcome).toEqual({ status: "found", preview: { kind: "pr", pull } });
    expect(resolveRepoItem).toHaveBeenCalledTimes(1);
    expect(listPulls).toHaveBeenCalledTimes(2);
  });

  it("resolves ambiguous #N references by falling through from pulls to issues", async () => {
    const issue = sampleIssue({ Number: 17 });
    const client = makeGeneratedClient({
      RepositoriesService: { listRepos: vi.fn().mockResolvedValue([trackedRepo()]) },
      PullRequestsService: { listPulls: vi.fn().mockResolvedValue([]) },
      IssuesService: { listIssues: vi.fn().mockResolvedValue([issue]) },
    });

    const outcome = await runPreview(client, {
      provider: "github",
      platformHost: "github.com",
      owner: "acme",
      name: "widgets",
      repoPath: "acme/widgets",
      number: 17,
    });

    expect(outcome).toEqual({ status: "found", preview: { kind: "issue", issue } });
  });

  it("caches found previews until TTL expiry and isolates GitLab PR vs Issue namespaces", async () => {
    let now = 1_000;
    const cache = new ItemPreviewCache({ ttlMs: 5_000, now: () => now });
    const ghPull = samplePull({ Number: 42 });
    const listPulls = vi.fn().mockResolvedValue([ghPull]);
    const client = makeGeneratedClient({
      RepositoriesService: { listRepos: vi.fn().mockResolvedValue([trackedRepo()]) },
      PullRequestsService: { listPulls },
    });

    const prRef = {
      provider: "github",
      platformHost: "github.com",
      owner: "acme",
      name: "widgets",
      repoPath: "acme/widgets",
      number: 42,
      itemType: "pr" as const,
    };
    const first = await runPreview(client, prRef, cache);
    expect(first).toEqual({ status: "found", preview: { kind: "pr", pull: ghPull } });
    expect(listPulls).toHaveBeenCalledTimes(1);

    // Same GitHub item referenced via bare #42 hits cache without re-fetching.
    const second = await runPreview(client, { ...prRef, itemType: undefined }, cache);
    expect(second).toEqual({ status: "found", preview: { kind: "pr", pull: ghPull } });
    expect(listPulls).toHaveBeenCalledTimes(1);

    // After TTL expires, it fetches fresh data.
    now += 6_000;
    await runPreview(client, prRef, cache);
    expect(listPulls).toHaveBeenCalledTimes(2);

    // GitLab PR #10 and Issue #10 do not collide in cache.
    const glPull = samplePull({
      Number: 10,
      platform_host: "gitlab.example.com",
      repo_owner: "group",
      repo_name: "app",
      repo: {
        ...samplePull().repo,
        provider: "gitlab",
        platform_host: "gitlab.example.com",
        owner: "group",
        name: "app",
        repo_path: "group/app",
      },
    });
    const glIssue = sampleIssue({
      Number: 10,
      platform_host: "gitlab.example.com",
      repo_owner: "group",
      repo_name: "app",
      repo: {
        ...sampleIssue().repo,
        provider: "gitlab",
        platform_host: "gitlab.example.com",
        owner: "group",
        name: "app",
        repo_path: "group/app",
      },
    });
    cache.set(
      {
        provider: "gitlab",
        platformHost: "gitlab.example.com",
        owner: "group",
        name: "app",
        repoPath: "group/app",
        number: 10,
        itemType: "pr",
      },
      { status: "found", preview: { kind: "pr", pull: glPull } },
    );
    cache.set(
      {
        provider: "gitlab",
        platformHost: "gitlab.example.com",
        owner: "group",
        name: "app",
        repoPath: "group/app",
        number: 10,
        itemType: "issue",
      },
      { status: "found", preview: { kind: "issue", issue: glIssue } },
    );

    expect(
      cache.get({
        provider: "gitlab",
        platformHost: "gitlab.example.com",
        owner: "group",
        name: "app",
        repoPath: "group/app",
        number: 10,
        itemType: "pr",
      }),
    ).toEqual({ status: "found", preview: { kind: "pr", pull: glPull } });
    expect(
      cache.get({
        provider: "gitlab",
        platformHost: "gitlab.example.com",
        owner: "group",
        name: "app",
        repoPath: "group/app",
        number: 10,
        itemType: "issue",
      }),
    ).toEqual({ status: "found", preview: { kind: "issue", issue: glIssue } });
  });

  it("caches not-found 404 responses, leaves providerPage fallbacks and transport errors uncached", async () => {
    const cache = new ItemPreviewCache();
    const listPulls = vi.fn().mockResolvedValue([]);
    const problem404 = new GeneratedProblemResponse(
      { status: 404, title: "Not Found", detail: "item not found" },
      new Response(null, { status: 404 }),
    );
    const resolveNotFound = vi.fn().mockRejectedValue(problem404);
    const notFoundClient = makeGeneratedClient({
      RepositoriesService: { listRepos: vi.fn().mockResolvedValue([trackedRepo()]), resolveRepoItem: resolveNotFound },
      PullRequestsService: { listPulls },
    });

    const missingRef = {
      provider: "github",
      platformHost: "github.com",
      owner: "acme",
      name: "widgets",
      repoPath: "acme/widgets",
      number: 9998,
      itemType: "pr" as const,
    };
    const notFoundOutcome = await runPreview(notFoundClient, missingRef, cache);
    expect(notFoundOutcome).toEqual({ status: "not-found" });
    expect(cache.get(missingRef)).toEqual({ status: "not-found" });

    // When a 404 still has a provider page URL, clicking opens the provider page,
    // so report not-synced without caching it as missing.
    const unsyncedRef = {
      ...missingRef,
      number: 9997,
      externalUrl: "https://github.com/acme/widgets/pull/9997",
    };
    const notSyncedOutcome = await runPreview(notFoundClient, unsyncedRef, cache);
    expect(notSyncedOutcome).toEqual({ status: "not-synced" });
    expect(cache.get(unsyncedRef)).toBeUndefined();

    const resolveError = vi.fn().mockRejectedValue(new TypeError("network down"));
    const errorClient = makeGeneratedClient({
      RepositoriesService: { listRepos: vi.fn().mockResolvedValue([trackedRepo()]), resolveRepoItem: resolveError },
      PullRequestsService: { listPulls },
    });
    const errorRef = { ...missingRef, number: 9999 };
    const errorOutcome = await runPreview(errorClient, errorRef, cache);
    expect(errorOutcome).toMatchObject({ status: "error" });
    expect(cache.get(errorRef)).toBeUndefined();
  });

  it("loads previews for hidden configured repositories omitted from listRepos", async () => {
    const hiddenPull = samplePull({
      Number: 55,
      repo_owner: "acme",
      repo_name: "hidden-lib",
      URL: "https://github.com/acme/hidden-lib/pull/55",
      repo: {
        ...samplePull().repo,
        owner: "acme",
        name: "hidden-lib",
        repo_path: "acme/hidden-lib",
      },
    });
    const listPulls = vi.fn().mockResolvedValue([hiddenPull]);
    const listRepos = vi.fn().mockResolvedValue([trackedRepo()]);
    const client = makeGeneratedClient({
      RepositoriesService: { listRepos },
      PullRequestsService: { listPulls },
    });
    const configuredRepos: ConfigRepo[] = [
      {
        provider: "github",
        platform_host: "github.com",
        owner: "acme",
        name: "hidden-lib",
        repo_path: "acme/hidden-lib",
        hidden_from_ui: true,
        is_glob: false,
        issue_pr_references: true,
        matched_repo_count: 1,
      },
    ];

    const outcome = await runPreview(
      client,
      {
        provider: "github",
        platformHost: "github.com",
        owner: "acme",
        name: "hidden-lib",
        repoPath: "acme/hidden-lib",
        number: 55,
        itemType: "pr",
      },
      undefined,
      configuredRepos,
    );

    expect(outcome).toEqual({ status: "found", preview: { kind: "pr", pull: hiddenPull } });
    expect(listPulls).toHaveBeenCalledTimes(1);
    expect(listRepos).not.toHaveBeenCalled();
  });
});
