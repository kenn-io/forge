// Browser-tier reimplementation of frontend/tests/e2e-full/link-navigation-repo-sync.spec.ts.
// Regression: deep-linking to a PR/issue used to leave the repo dropdown and the
// sidebar list pinned to a previously chosen repo while the detail jumped to the
// new repo. App.svelte now syncs the global repo selector to the route's selected
// item. The app is mounted for real through the browser harness with the list and
// detail responses mocked at the fetch boundary; the pull list honors the `repo`
// query param the pulls store sends (getGlobalRepo -> repo), so the sidebar group
// reflects the same server-side filter the live backend would apply.
//
// A real Chromium page provides matchMedia/ResizeObserver/IntersectionObserver/
// canvas natively, so the jsdom installAppDomGlobals() shim is gone; the browser
// harness stubs only EventSource. The typeahead value and sidebar group name are
// exact-DOM assertions the page locator API does not expose, so they stay as
// querySelector against the real DOM, wrapped in vi.waitFor for the async chain.
//
// Seed parity (cmd/e2e-server, internal/testutil/fixtures.go): acme/widgets and
// acme/tools both on github.com, each with PR #1 ("Add widget caching layer" and
// "Add CLI flag parser"), and acme/widgets issue #10.

import { afterEach, beforeEach, describe, expect, it, vi } from "vite-plus/test";
import { page } from "vite-plus/test/browser";

import {
  firePopstate,
  mountBrowserApp,
  resetKeyboardModuleState,
  type MountedBrowserApp,
} from "./test/browserAppHarness.js";
import { jsonResponse, type MockRouteOverride } from "./test/mockApiFetch.js";
import { setGlobalRepo } from "./lib/stores/filter.svelte.js";

const WAIT = 10_000;

// Both repos must be configured for the global-repo normalization/sync to resolve
// them; the default settings fixture configures only acme/widgets.
const configuredRepos = [
  {
    provider: "github",
    platform_host: "github.com",
    owner: "acme",
    name: "widgets",
    repo_path: "acme/widgets",
    is_glob: false,
    matched_repo_count: 1,
  },
  {
    provider: "github",
    platform_host: "github.com",
    owner: "acme",
    name: "tools",
    repo_path: "acme/tools",
    is_glob: false,
    matched_repo_count: 1,
  },
];

function settingsOverride(): MockRouteOverride {
  return (req) => {
    if (req.method !== "GET" || req.url.pathname !== "/api/v1/settings") return null;
    return jsonResponse({
      repos: configuredRepos,
      activity: { view_mode: "threaded", time_range: "7d", hide_closed: false, hide_bots: false },
      terminal: {
        font_family: "",
        font_size: 14,
        scrollback: 1000,
        line_height: 1,
        letter_spacing: 0,
        cursor_blink: true,
        font_ligatures: false,
        renderer: "xterm",
      },
      agents: [],
      fleet: {
        enabled: false,
        key: "",
        peer_timeout: "2s",
        sessions: { include_unmanaged_details: false },
        peers: [],
        ssh_peers: [],
        restart_required: false,
      },
    });
  };
}

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
      read_releases: true,
      read_ci: true,
      read_labels: true,
      comment_mutation: true,
      state_mutation: true,
      merge_mutation: true,
      label_mutation: true,
      review_mutation: true,
      workflow_approval: true,
      ready_for_review: true,
      issue_mutation: true,
    },
  };
}

function pullSummary(owner: string, name: string, number: number, title: string, author: string) {
  return {
    ID: number * 10 + (name === "tools" ? 2 : 1),
    RepoID: name === "tools" ? 2 : 1,
    GitHubID: 1000 + number,
    Number: number,
    URL: `https://github.com/${owner}/${name}/pull/${number}`,
    Title: title,
    Author: author,
    State: "open",
    IsDraft: false,
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
    provider: "github",
    repo_owner: owner,
    repo_name: name,
    platform_host: "github.com",
    platform_head_sha: `${number}aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa${number}`,
    repo: repoRef(owner, name),
    worktree_links: [],
  };
}

const allPulls = [
  pullSummary("acme", "widgets", 1, "Add widget caching layer", "alice"),
  pullSummary("acme", "tools", 1, "Add CLI flag parser", "dave"),
];

function pullDetail(owner: string, name: string, number: number) {
  const pr =
    allPulls.find((p) => p.repo_owner === owner && p.repo_name === name && p.Number === number) ?? allPulls[0]!;
  return {
    merge_request: pr,
    repo: repoRef(owner, name),
    repo_owner: owner,
    repo_name: name,
    platform_host: "github.com",
    platform_head_sha: pr.platform_head_sha,
    reviewed_head_sha: pr.platform_head_sha,
    detail_loaded: true,
    detail_fetched_at: "2026-03-30T14:00:00Z",
    worktree_links: [],
  };
}

// The pulls store appends `repo=<provider|host/owner/name>` when a global repo is set, so
// the list honors it exactly the way the live backend filters.
function listOverride(): MockRouteOverride {
  return (req) => {
    if (req.method !== "GET" || req.url.pathname !== "/api/v1/pulls") return null;
    const repo = req.url.searchParams.get("repo");
    const filtered = repo
      ? allPulls.filter((p) => `${p.provider}|${p.platform_host}/${p.repo_owner}/${p.repo_name}` === repo)
      : allPulls;
    return jsonResponse(filtered);
  };
}

function detailOverride(): MockRouteOverride {
  return (req) => {
    if (req.method !== "GET") return null;
    const pr = req.url.pathname.match(/^\/api\/v1\/pulls\/github\/acme\/(widgets|tools)\/(\d+)$/);
    if (pr) return jsonResponse(pullDetail("acme", pr[1]!, Number(pr[2])));
    return null;
  };
}

function overrides(): MockRouteOverride[] {
  return [settingsOverride(), listOverride(), detailOverride()];
}

function typeaheadValue(): string {
  return document.querySelector(".typeahead-value")?.textContent?.trim() ?? "";
}

function repoHeaderNames(): string[] {
  return Array.from(document.querySelectorAll(".repo-header__name")).map((el) => el.textContent?.trim() ?? "");
}

function clickPullItem(title: string): Promise<void> {
  const item = Array.from(document.querySelectorAll(".pull-item")).find((el) => (el.textContent ?? "").includes(title));
  return page.elementLocator(item as Element).click();
}

describe("deep-link repo dropdown + sidebar sync", () => {
  vi.setConfig({ testTimeout: 30_000 });

  let mounted: MountedBrowserApp | null = null;

  beforeEach(async () => {
    await page.viewport(1280, 900);
    setGlobalRepo(undefined);
  });

  afterEach(async () => {
    mounted?.unmount();
    mounted = null;
    setGlobalRepo(undefined);
    localStorage.clear();
    await resetKeyboardModuleState();
  });

  it("navigating to a PR in a different repo updates the dropdown and the sidebar list", async () => {
    setGlobalRepo("github|github.com/acme/widgets");
    mounted = await mountBrowserApp("/pulls/github/acme/tools/1", { overrides: overrides() });

    await vi.waitFor(() => expect(document.querySelector(".pull-detail")).not.toBeNull(), WAIT);
    await vi.waitFor(() => expect(typeaheadValue()).toBe("github/github.com/acme/tools"), WAIT);
    await vi.waitFor(() => expect(repoHeaderNames()).toEqual(["acme/tools"]), WAIT);
  });

  it("navigating between PRs in different repos updates the dropdown each time", async () => {
    setGlobalRepo("github|github.com/acme/widgets");
    mounted = await mountBrowserApp("/pulls/github/acme/widgets/1", { overrides: overrides() });
    await vi.waitFor(() => expect(typeaheadValue()).toBe("github/github.com/acme/widgets"), WAIT);

    firePopstate("/pulls/github/acme/tools/1");
    await vi.waitFor(() => expect(typeaheadValue()).toBe("github/github.com/acme/tools"), WAIT);
  });

  it("selecting an item from All repos keeps the all-repo filter", async () => {
    mounted = await mountBrowserApp("/pulls", { overrides: overrides() });

    await expect.element(page.getByText("Add widget caching layer")).toBeVisible();
    await vi.waitFor(() => expect(typeaheadValue()).toBe("All repos"), WAIT);

    await clickPullItem("Add widget caching layer");
    await vi.waitFor(() => expect(document.querySelector(".pull-detail")).not.toBeNull(), WAIT);
    expect(typeaheadValue()).toBe("All repos");
  });

  it("opening /pulls without a selection preserves the user's chosen repo", async () => {
    setGlobalRepo("github|github.com/acme/widgets");
    mounted = await mountBrowserApp("/pulls", { overrides: overrides() });

    await vi.waitFor(() => expect(document.querySelector(".pull-item")).not.toBeNull(), WAIT);
    await vi.waitFor(() => expect(typeaheadValue()).toBe("github/github.com/acme/widgets"), WAIT);
  });
});
