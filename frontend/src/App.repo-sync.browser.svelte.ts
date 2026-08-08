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
import { getGlobalRepo, setGlobalRepo } from "./lib/stores/filter.svelte.js";
import type { ConfigRepo } from "@kenn-forge/ui/api/types";

const WAIT = 10_000;

// Both repos must be configured for the global-repo normalization/sync to resolve
// them; the default settings fixture configures only acme/widgets.
const configuredRepos: ConfigRepo[] = [
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

function settingsOverride(repos: ConfigRepo[] = configuredRepos): MockRouteOverride {
  return (req) => {
    if (req.method !== "GET" || req.url.pathname !== "/api/v1/settings") return null;
    return jsonResponse({
      repos,
      activity: { view_mode: "threaded", time_range: "7d", hide_closed: false, hide_bots: false },
      issues: { hide_bots: false },
      terminal: {
        font_family: "",
        font_size: 14,
        scrollback: 1000,
        line_height: 1,
        letter_spacing: 0,
        cursor_blink: true,
        font_ligatures: false,
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

function repoCatalogOverride(...repos: Array<[owner: string, name: string]>): MockRouteOverride {
  return (req) => {
    if (req.method !== "GET" || req.url.pathname !== "/api/v1/repos") return null;
    return jsonResponse(
      repos.map(([owner, name]) => ({
        Platform: "github",
        PlatformHost: "github.com",
        Owner: owner,
        Name: name,
      })),
    );
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
  return [
    settingsOverride(),
    repoCatalogOverride(["acme", "widgets"], ["acme", "tools"]),
    listOverride(),
    detailOverride(),
  ];
}

function typeaheadValue(): string {
  return document.querySelector(".typeahead-value")?.textContent?.trim() ?? "";
}

function repoHeaderNames(): string[] {
  return Array.from(document.querySelectorAll(".sidebar-group-header__name")).map((el) => el.textContent?.trim() ?? "");
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
    delete window.__kenn_forge_config;
    window.__kenn_forge_notify_config_changed?.();
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

  it("preserves a persisted repository discovered through a visible glob", async () => {
    setGlobalRepo("github|github.com/acme/service");
    mounted = await mountBrowserApp("/pulls", {
      overrides: [
        settingsOverride([
          {
            provider: "github",
            platform_host: "github.com",
            owner: "acme",
            name: "*",
            repo_path: "acme/*",
            is_glob: true,
            matched_repo_count: 1,
          },
        ]),
        repoCatalogOverride(["acme", "service"]),
        listOverride(),
        detailOverride(),
      ],
    });

    await vi.waitFor(() => expect(typeaheadValue()).toBe("github/github.com/acme/service"), WAIT);
    expect(getGlobalRepo()).toBe("github|github.com/acme/service");
  });

  it("preserves a newly selected repository whose provider route was renamed", async () => {
    mounted = await mountBrowserApp("/pulls", {
      overrides: [
        settingsOverride([
          {
            provider: "github",
            platform_host: "github.com",
            owner: "acme",
            name: "legacy",
            repo_path: "acme/legacy",
            is_glob: false,
            matched_repo_count: 1,
          },
        ]),
        repoCatalogOverride(["acme", "renamed"]),
        listOverride(),
        detailOverride(),
      ],
    });

    await page.getByRole("button", { name: /all repos/i }).click();
    const renamedOption = page.getByRole("option", { name: /github.com\/acme\/renamed/i });
    await expect.element(renamedOption).toBeVisible();
    await renamedOption.click();

    await vi.waitFor(() => expect(getGlobalRepo()).toBe("github|github.com/acme/renamed"), WAIT);
    (document.querySelector(".typeahead-input") as HTMLInputElement).blur();
    await vi.waitFor(() => expect(typeaheadValue()).toBe("github/github.com/acme/renamed"), WAIT);
  });

  it("clears a stale configured path when the resolved renamed repository is hidden", async () => {
    setGlobalRepo("github|github.com/acme/legacy-service");
    mounted = await mountBrowserApp("/pulls", {
      overrides: [
        settingsOverride([
          {
            provider: "github",
            platform_host: "github.com",
            owner: "acme",
            name: "legacy-service",
            repo_path: "acme/legacy-service",
            is_glob: false,
            matched_repo_count: 1,
          },
          {
            provider: "github",
            platform_host: "github.com",
            owner: "acme",
            name: "archive-*",
            repo_path: "acme/archive-*",
            is_glob: true,
            matched_repo_count: 1,
            hide_from_ui: true,
          },
        ]),
        (req) => {
          if (req.method !== "GET" || req.url.pathname !== "/api/v1/repos") return null;
          return jsonResponse([]);
        },
        listOverride(),
        detailOverride(),
      ],
    });

    await vi.waitFor(() => expect(getGlobalRepo()).toBeUndefined(), WAIT);
    await vi.waitFor(() => expect(typeaheadValue()).toBe("All repos"), WAIT);
  });

  it("preserves a host-pinned repository outside the interactive catalog", async () => {
    window.__kenn_forge_config = {
      ui: {
        hideRepoSelector: true,
        repo: {
          provider: "github",
          host: "github.com",
          owner: "acme",
          name: "archive",
        },
      },
    };
    window.__kenn_forge_notify_config_changed?.();

    mounted = await mountBrowserApp("/pulls", {
      overrides: [
        settingsOverride([
          {
            provider: "github",
            platform_host: "github.com",
            owner: "acme",
            name: "archive",
            repo_path: "acme/archive",
            is_glob: false,
            matched_repo_count: 1,
            hide_from_ui: true,
          },
        ]),
        repoCatalogOverride(),
        listOverride(),
        detailOverride(),
      ],
    });

    await vi.waitFor(
      () => expect(mounted?.api.requests.some((req) => req.url.pathname === "/api/v1/repos")).toBe(true),
      WAIT,
    );
    await new Promise((resolve) => window.setTimeout(resolve, 100));
    expect(getGlobalRepo()).toBe("github|github.com/acme/archive");
  });
});
