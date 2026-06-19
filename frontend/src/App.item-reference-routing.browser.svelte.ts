// Browser-tier reimplementation of frontend/tests/e2e-full/item-reference-routing.spec.ts.
// A cross-repository timeline reference is an item-ref link: clicking it POSTs to
// the repo resolve endpoint and then either navigates internally (when the target
// repo is tracked) or opens the provider URL via window.open (when it is not).
// The app is mounted for real with the PR detail, the resolve responses, and the
// target detail mocked at the fetch boundary, so the navigation and the
// window.open call are the genuine outputs of the resolve handler.
//
// A real Chromium page provides matchMedia/ResizeObserver/IntersectionObserver/
// canvas natively, so the jsdom installAppDomGlobals() shim is gone; the browser
// harness stubs only EventSource. The untracked case spies window.open with
// vi.spyOn instead of intercepting a Playwright popup.
//
// Seed parity (internal/testutil/fixtures.go): acme/widgets#1 carries
// cross_referenced events to the tracked acme/tools#1 and the untracked
// other/repo#77.

import { afterEach, beforeEach, describe, expect, it, vi } from "vite-plus/test";
import { page } from "vite-plus/test/browser";

import { mountBrowserApp, resetKeyboardModuleState, type MountedBrowserApp } from "./test/browserAppHarness.js";
import { jsonResponse, type MockRouteOverride } from "./test/mockApiFetch.js";

const WAIT = 10_000;

function repoRef(owner: string, name: string) {
  return {
    provider: "github",
    platform_host: "github.com",
    repo_path: `${owner}/${name}`,
    owner,
    name,
    capabilities: {
      read_repositories: true,
      read_merge_requests: true,
      read_issues: true,
      read_comments: true,
      state_mutation: true,
    },
  };
}

function crossRefEvent(id: number, owner: string, name: string, number: number, title: string, author: string) {
  return {
    ID: id,
    MergeRequestID: 1001,
    PlatformID: 8000 + id,
    EventType: "cross_referenced",
    Author: author,
    Body: "",
    Summary: `Referenced from ${owner}/${name}#${number}`,
    MetadataJSON: JSON.stringify({
      source_type: "PullRequest",
      source_owner: owner,
      source_repo: name,
      source_number: number,
      source_title: title,
      source_url: `https://github.com/${owner}/${name}/pull/${number}`,
      is_cross_repository: true,
      will_close_target: false,
    }),
    DedupeKey: `w1-cross-${owner}-${name}-${number}`,
    CreatedAt: "2026-03-30T14:00:00Z",
    ThreadID: null,
    Resolvable: false,
    Resolved: false,
  };
}

function pullDetail(owner: string, name: string, number: number, title: string, events: unknown[]) {
  return {
    merge_request: {
      ID: 1000 + number,
      RepoID: name === "tools" ? 2 : 1,
      GitHubID: 1100 + number,
      Number: number,
      URL: `https://github.com/${owner}/${name}/pull/${number}`,
      Title: title,
      Author: "alice",
      State: "open",
      IsDraft: false,
      MergeableState: "clean",
      Body: "",
      HeadBranch: "feature/x",
      BaseBranch: "main",
      Additions: 10,
      Deletions: 1,
      CommentCount: 0,
      ReviewDecision: "",
      CIStatus: "success",
      CIChecksJSON: "[]",
      CreatedAt: "2026-03-29T14:00:00Z",
      UpdatedAt: "2026-03-30T14:00:00Z",
      LastActivityAt: "2026-03-30T14:00:00Z",
      MergedAt: null,
      ClosedAt: null,
      KanbanStatus: "new",
      Starred: false,
      repo_owner: owner,
      repo_name: name,
      platform_host: "github.com",
      platform_head_sha: `${number}aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa${number}`,
      repo: repoRef(owner, name),
      worktree_links: [],
    },
    repo: repoRef(owner, name),
    events,
    repo_owner: owner,
    repo_name: name,
    platform_host: "github.com",
    platform_head_sha: `${number}aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa${number}`,
    reviewed_head_sha: `${number}aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa${number}`,
    detail_loaded: true,
    detail_fetched_at: "2026-03-30T14:00:00Z",
    worktree_links: [],
  };
}

const widgetsEvents = [
  crossRefEvent(1, "acme", "tools", 1, "Add CLI flag parser", "dave"),
  crossRefEvent(2, "other", "repo", 77, "External follow-up PR", "mallory"),
];

function overrides(): MockRouteOverride[] {
  return [
    // Match the detail GET and the background /sync/async POST so the sync does
    // not 404 against the default fixtures and clobber the loaded detail.
    (req) =>
      /^\/api\/v1\/pulls\/github\/acme\/widgets\/1(\/sync\/async)?$/.test(req.url.pathname)
        ? jsonResponse(pullDetail("acme", "widgets", 1, "Add widget caching layer", widgetsEvents))
        : null,
    (req) =>
      /^\/api\/v1\/pulls\/github\/acme\/tools\/1(\/sync\/async)?$/.test(req.url.pathname)
        ? jsonResponse(pullDetail("acme", "tools", 1, "Add CLI flag parser", []))
        : null,
    (req) =>
      req.method === "POST" && req.url.pathname === "/api/v1/repo/github/acme/tools/resolve/1"
        ? jsonResponse({ number: 1, item_type: "pr", repo_tracked: true })
        : null,
    (req) =>
      req.method === "POST" && req.url.pathname === "/api/v1/repo/github/other/repo/resolve/77"
        ? jsonResponse({ number: 77, item_type: "pr", repo_tracked: false })
        : null,
  ];
}

function detailTitle(): string {
  return document.querySelector(".pull-detail .detail-title")?.textContent ?? "";
}

describe("item references through the timeline", () => {
  vi.setConfig({ testTimeout: 30_000 });

  let mounted: MountedBrowserApp | null = null;

  beforeEach(async () => {
    await page.viewport(1280, 900);
  });

  afterEach(async () => {
    mounted?.unmount();
    mounted = null;
    vi.restoreAllMocks();
    localStorage.clear();
    await resetKeyboardModuleState();
  });

  it("routes a tracked cross-repository reference internally", async () => {
    mounted = await mountBrowserApp("/pulls/github/acme/widgets/1", { overrides: overrides() });
    await vi.waitFor(() => expect(detailTitle()).toContain("Add widget caching layer"), WAIT);

    await page.getByRole("link", { name: "Add CLI flag parser" }).click();

    await vi.waitFor(() => expect(window.location.pathname).toBe("/pulls/github/acme/tools/1"), WAIT);
    await vi.waitFor(() => expect(detailTitle()).toContain("Add CLI flag parser"), WAIT);
    expect(mounted.api.requests.some((r) => r.url.pathname === "/api/v1/repo/github/acme/tools/resolve/1")).toBe(true);
  });

  it("opens the provider URL for an untracked cross-repository reference", async () => {
    mounted = await mountBrowserApp("/pulls/github/acme/widgets/1", { overrides: overrides() });
    await vi.waitFor(() => expect(detailTitle()).toContain("Add widget caching layer"), WAIT);

    const openSpy = vi.spyOn(window, "open").mockReturnValue(null);
    await page.getByRole("link", { name: "External follow-up PR" }).click();

    await vi.waitFor(() => expect(openSpy).toHaveBeenCalled(), WAIT);
    expect(String(openSpy.mock.calls[0]![0])).toBe("https://github.com/other/repo/pull/77");
    expect(window.location.pathname).toBe("/pulls/github/acme/widgets/1");
  });
});
