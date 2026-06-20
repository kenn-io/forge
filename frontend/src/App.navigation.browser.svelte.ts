// Browser-tier reimplementation of part of frontend/tests/e2e-full/navigation.spec.ts.
// What lives here is the app-shell navigation that needs only the activity feed
// and the PR/issue lists: selecting an activity item and returning to it after a
// detour preserves the selection in the URL and reopens the split view, the
// legacy /mail route falls through to Activity rather than Messages, and clicking
// a list row opens the detail pane. The app is mounted for real with those feeds
// mocked at the fetch boundary.
//
// A real Chromium page provides matchMedia/ResizeObserver/IntersectionObserver/
// canvas natively, so the jsdom installAppDomGlobals() shim is gone; the browser
// harness stubs only EventSource.
//
// The cross-mode tab tour (Kata/Docs/Messages shells), the Kata-shell specifics,
// the direct Docs/Messages loads, and the settings/diff route toggle stay in
// Playwright: they depend on the external mode-shell backends and diff rendering,
// which are full-stack concerns.

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
    Body: "",
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
    KanbanStatus: "new",
    Starred: false,
    repo_owner: owner,
    repo_name: name,
    platform_host: "github.com",
    platform_head_sha: `${number}aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa${number}`,
    repo: repoRef(owner, name),
    worktree_links: [],
  };
}

function issueRow(owner: string, name: string, number: number, title: string) {
  return {
    ID: 7000 + number,
    RepoID: 1,
    GitHubID: 7100 + number,
    Number: number,
    URL: `https://github.com/${owner}/${name}/issues/${number}`,
    Title: title,
    Author: "alice",
    State: "open",
    Body: "",
    CommentCount: 0,
    LabelsJSON: "[]",
    CreatedAt: "2026-03-28T14:00:00Z",
    UpdatedAt: "2026-03-30T14:00:00Z",
    LastActivityAt: "2026-03-30T14:00:00Z",
    ClosedAt: null,
    Starred: false,
    platform_host: "github.com",
    repo_owner: owner,
    repo_name: name,
    repo: repoRef(owner, name),
  };
}

const pulls = [pull("acme", "widgets", 42, "Add browser regression coverage")];
const issues = [issueRow("acme", "widgets", 7, "Theme toggle does not stick")];

function activityEvent(): unknown {
  return {
    id: "pr:42",
    cursor: "pr:42",
    activity_type: "comment",
    author: "marius",
    body_preview: "",
    created_at: "2026-03-30T14:00:00Z",
    item_number: 42,
    item_state: "open",
    item_title: "Add browser regression coverage",
    item_type: "pr",
    item_url: "https://github.com/acme/widgets/pull/42",
    platform_host: "github.com",
    repo_owner: "acme",
    repo_name: "widgets",
    repo: { ...repoRef("acme", "widgets"), capabilities: {} },
  };
}

function flatSettings(): MockRouteOverride {
  return (req) => {
    if (req.method !== "GET" || req.url.pathname !== "/api/v1/settings") return null;
    return jsonResponse({
      repos: [
        {
          provider: "github",
          platform_host: "github.com",
          owner: "acme",
          name: "widgets",
          repo_path: "acme/widgets",
          is_glob: false,
          matched_repo_count: 1,
        },
      ],
      activity: { view_mode: "flat", time_range: "7d", hide_closed: false, hide_bots: false, collapse_threads: false },
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
      // The settings-gear case routes to /settings, which renders FleetSettings;
      // it dereferences fleet.sessions, so the override must carry a fleet block.
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

function overrides(): MockRouteOverride[] {
  return [
    flatSettings(),
    (req) =>
      req.method === "GET" && req.url.pathname === "/api/v1/activity"
        ? jsonResponse({ capped: false, items: [activityEvent()] })
        : null,
    (req) => (req.method === "GET" && req.url.pathname === "/api/v1/pulls" ? jsonResponse(pulls) : null),
    (req) => (req.method === "GET" && req.url.pathname === "/api/v1/issues" ? jsonResponse(issues) : null),
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
      const iss = req.url.pathname.match(/^\/api\/v1\/issues\/github\/acme\/widgets\/(\d+)$/);
      if (req.method === "GET" && iss) {
        const found = issues.find((i) => i.Number === Number(iss[1])) ?? issues[0]!;
        return jsonResponse({
          issue: found,
          repo: repoRef("acme", "widgets"),
          events: [],
          platform_host: "github.com",
          repo_owner: "acme",
          repo_name: "widgets",
          detail_loaded: true,
          detail_fetched_at: "2026-03-30T14:00:00Z",
        });
      }
      return null;
    },
  ];
}

function viewTab(label: string): Element {
  return Array.from(document.querySelectorAll(".view-tab")).find((t) => (t.textContent ?? "").includes(label))!;
}

describe("view navigation", () => {
  vi.setConfig({ testTimeout: 30_000 });

  let mounted: MountedBrowserApp | null = null;

  beforeEach(async () => {
    await page.viewport(1280, 900);
  });

  afterEach(async () => {
    mounted?.unmount();
    mounted = null;
    localStorage.clear();
    await resetKeyboardModuleState();
  });

  it("returning to Activity from PRs restores the selected item", async () => {
    mounted = await mountBrowserApp("/", { overrides: overrides() });
    await vi.waitFor(() => expect(document.querySelector(".activity-table .activity-row")).not.toBeNull(), WAIT);

    await page.elementLocator(document.querySelector(".activity-table .activity-row")!).click();
    await vi.waitFor(() => expect(document.querySelector(".activity-shell--split")).not.toBeNull(), WAIT);
    expect(window.location.search).toContain("selected=pr%3A");

    await page.elementLocator(viewTab("PRs")).click();
    await vi.waitFor(() => expect(window.location.pathname).toMatch(/^\/pulls\//), WAIT);

    await page.elementLocator(viewTab("Activity")).click();
    await vi.waitFor(() => expect(document.querySelector(".activity-shell--split")).not.toBeNull(), WAIT);
    expect(window.location.search).toContain("selected=pr%3A");
  });

  it("returning to Activity from the settings gear restores the selected item", async () => {
    mounted = await mountBrowserApp("/", { overrides: overrides() });
    await vi.waitFor(() => expect(document.querySelector(".activity-table .activity-row")).not.toBeNull(), WAIT);

    await page.elementLocator(document.querySelector(".activity-table .activity-row")!).click();
    await vi.waitFor(() => expect(document.querySelector(".activity-shell--split")).not.toBeNull(), WAIT);
    expect(window.location.search).toContain("selected=pr%3A");

    await page.getByTitle("Settings").click();
    await vi.waitFor(() => expect(window.location.pathname).toBe("/settings"), WAIT);

    await page.elementLocator(viewTab("Activity")).click();
    await vi.waitFor(() => expect(document.querySelector(".activity-shell--split")).not.toBeNull(), WAIT);
    expect(window.location.search).toContain("selected=pr%3A");
  });

  it("legacy /mail route falls through to Activity, not Messages", async () => {
    mounted = await mountBrowserApp("/mail?q=label%3AInbox", { overrides: overrides() });

    await vi.waitFor(() => expect(document.querySelector(".activity-feed")).not.toBeNull(), WAIT);
    expect(page.getByRole("heading", { name: "Messages" }).elements().length).toBe(0);
  });

  it("clicking a PR row opens the detail pane", async () => {
    mounted = await mountBrowserApp("/pulls", { overrides: overrides() });
    await vi.waitFor(() => expect(document.querySelector(".pull-item")).not.toBeNull(), WAIT);
    expect(document.querySelector(".pull-detail")).toBeNull();

    await page.elementLocator(document.querySelector(".pull-item")!).click();
    await vi.waitFor(() => expect(document.querySelector(".pull-detail")).not.toBeNull(), WAIT);
  });

  it("clicking an issue row opens the detail pane", async () => {
    mounted = await mountBrowserApp("/issues", { overrides: overrides() });
    await vi.waitFor(() => expect(document.querySelector(".issue-item")).not.toBeNull(), WAIT);
    expect(document.querySelector(".issue-detail")).toBeNull();

    await page.elementLocator(document.querySelector(".issue-item")!).click();
    await vi.waitFor(() => expect(document.querySelector(".issue-detail")).not.toBeNull(), WAIT);
  });
});
