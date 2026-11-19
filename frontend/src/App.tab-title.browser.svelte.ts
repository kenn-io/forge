import { afterEach, describe, expect, it, vi } from "vite-plus/test";
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
    capabilities: { read_repositories: true, read_merge_requests: true, read_issues: true, read_comments: true },
  };
}

function pull(owner: string, name: string, number: number, title: string) {
  return {
    ID: 4000 + number,
    RepoID: 1,
    GitHubID: 4100 + number,
    Number: number,
    URL: `https://github.com/${owner}/${name}/pull/${number}`,
    Title: title,
    Author: "alice",
    State: "open",
    IsDraft: false,
    Body: "Related issue #7.",
    HeadBranch: "feature/x",
    BaseBranch: "main",
    Additions: 1,
    Deletions: 0,
    CommentCount: 0,
    ReviewDecision: "",
    CIStatus: "success",
    CIChecksJSON: "[]",
    CreatedAt: "2026-03-29T14:00:00Z",
    UpdatedAt: "2026-03-30T14:00:00Z",
    LastActivityAt: "2026-03-30T14:00:00Z",
    MergedAt: null,
    ClosedAt: null,
    Starred: false,
    repo_owner: owner,
    repo_name: name,
    platform_host: "github.com",
    platform_head_sha: `${number}aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa${number}`,
    repo: repoRef(owner, name),
    worktree_links: [],
  };
}

const pulls = [pull("acme", "widgets", 42, "Add browser regression coverage")];
function overrides(): MockRouteOverride[] {
  return [
    (req) => (req.method === "GET" && req.url.pathname === "/api/v1/pulls" ? jsonResponse(pulls) : null),
    (req) => {
      const pr = req.url.pathname.match(/^\/api\/v1\/pulls\/github\/acme\/widgets\/(\d+)$/);
      if (req.method === "GET" && pr) {
        const found = pulls.find((p) => p.Number === Number(pr[1])) ?? pulls[0]!;
        return jsonResponse({
          merge_request: found,
          repo: repoRef("acme", "widgets"),
          repo_owner: "acme",
          repo_name: "widgets",
          platform_host: "github.com",
          platform_head_sha: found.platform_head_sha,
          reviewed_head_sha: found.platform_head_sha,
          detail_loaded: true,
          detail_fetched_at: "2026-03-30T14:00:00Z",
          worktree_links: [],
        });
      }
      return null;
    },
  ];
}

describe("tab title navigation", () => {
  let mounted: MountedBrowserApp | null = null;
  afterEach(async () => {
    mounted?.unmount();
    localStorage.clear();
    await resetKeyboardModuleState();
  });

  it("updates the title as a pull opens and the user visits issues", async () => {
    await page.viewport(1280, 900);
    mounted = await mountBrowserApp("/pulls", { overrides: overrides() });
    await vi.waitFor(() => expect(document.title).toBe("Pulls · Forge"), WAIT);
    await vi.waitFor(() => expect(document.querySelector(".pull-item")).not.toBeNull(), WAIT);
    await page.elementLocator(document.querySelector(".pull-item")!).click();
    await vi.waitFor(
      () => expect(document.title).toBe("Add browser regression coverage (acme/widgets#42) · Forge"),
      WAIT,
    );
    await page
      .getByRole("navigation", { name: "Page", exact: true })
      .getByRole("button", { name: "Issues", exact: true })
      .click();
    await vi.waitFor(() => expect(document.title).toBe("Issues · Forge"), WAIT);
  });
});
