// Browser-tier reimplementation of frontend/tests/e2e-full/grouping-toggle.spec.ts.
// The repo grouping toggle is a single store (middleman:groupingMode) shared
// across the PR list, the issue list, and the threaded activity view, plus a
// hide-org-name preference. This exercises the full app: the PR list defaults to
// grouped repo headers and switches to per-item repo chips, the choice persists
// across a reload and syncs into the issue list and the threaded activity view,
// threaded ungrouped keeps cross-repo items in separate threads, the grouping
// control only appears in threaded mode, hide-org-name rewrites the repo labels
// in both flat and threaded activity, and the threaded ungrouped view shows its
// own empty state. The app is mounted for real with the lists and activity feed
// mocked at the fetch boundary.
//
// A real Chromium page provides matchMedia/ResizeObserver/IntersectionObserver/
// canvas natively, so the jsdom installAppDomGlobals() shim is gone; the browser
// harness stubs only EventSource.
//
// Kept in Playwright (frontend/tests/e2e-full/grouping-toggle.spec.ts): the j/k
// keyboard navigation that asserts selection follows the flat visual order needs
// native key handling plus scroll-into-view behavior.

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

function pull(owner: string, name: string, number: number, title: string, author: string) {
  return {
    ID: 4000 + (name === "tools" ? 100 : 0) + number,
    RepoID: name === "tools" ? 2 : 1,
    GitHubID: 4100 + number,
    Number: number,
    URL: `https://github.com/${owner}/${name}/pull/${number}`,
    Title: title,
    Author: author,
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
    ID: 7000 + (name === "tools" ? 100 : 0) + number,
    RepoID: name === "tools" ? 2 : 1,
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

// Eight open PRs across two repos, mirroring the grouping seed.
const pulls = [
  pull("acme", "widgets", 1, "Add widget caching layer", "alice"),
  pull("acme", "widgets", 2, "Fix race condition in event loop", "bob"),
  pull("acme", "widgets", 6, "WIP: new dashboard layout", "carol"),
  pull("acme", "widgets", 7, "Bump lodash", "dependabot[bot]"),
  pull("acme", "tools", 1, "Add CLI flag parser", "dave"),
  pull("acme", "tools", 10, "Stack base", "dave"),
  pull("acme", "tools", 11, "Stack middle", "dave"),
  pull("acme", "tools", 12, "Stack top", "dave"),
];

const issues = [
  issueRow("acme", "widgets", 10, "Widget rendering broken on Safari"),
  issueRow("acme", "widgets", 11, "Add dark mode support"),
  issueRow("acme", "widgets", 13, "Security advisory"),
  issueRow("acme", "tools", 5, "Support config file loading"),
];

function activityItem(owner: string, name: string, number: number, title: string, author: string) {
  return {
    id: `pr:${owner}/${name}/${number}`,
    cursor: `pr:${owner}/${name}/${number}`,
    activity_type: "comment",
    author,
    body_preview: "",
    created_at: "2026-03-30T14:00:00Z",
    item_number: number,
    item_state: "open",
    item_title: title,
    item_type: "pr",
    item_author: author,
    item_url: `https://github.com/${owner}/${name}/pull/${number}`,
    platform_host: "github.com",
    repo_owner: owner,
    repo_name: name,
    repo: { ...repoRef(owner, name), capabilities: {} },
  };
}

const activityItems = [
  activityItem("acme", "widgets", 1, "Add widget caching layer", "alice"),
  activityItem("acme", "widgets", 2, "Fix race condition in event loop", "bob"),
  activityItem("acme", "tools", 1, "Add CLI flag parser", "dave"),
  activityItem("acme", "tools", 10, "Stack base", "dave"),
];

function settingsResponse(): MockRouteOverride {
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
        {
          provider: "github",
          platform_host: "github.com",
          owner: "acme",
          name: "tools",
          repo_path: "acme/tools",
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
    });
  };
}

function overrides(): MockRouteOverride[] {
  return [
    settingsResponse(),
    (req) => (req.method === "GET" && req.url.pathname === "/api/v1/pulls" ? jsonResponse(pulls) : null),
    (req) => (req.method === "GET" && req.url.pathname === "/api/v1/issues" ? jsonResponse(issues) : null),
    (req) =>
      req.method === "GET" && req.url.pathname === "/api/v1/activity"
        ? jsonResponse({ capped: false, items: activityItems })
        : null,
  ];
}

function count(selector: string): number {
  return document.querySelectorAll(selector).length;
}

async function selectPullGrouping(label: string): Promise<void> {
  // The PR/issue list sidebar is 340px, below the 395px compact threshold, so the
  // inline group buttons are CSS-hidden and grouping lives in the "Filters"
  // compact dropdown. The "Group" section is last, so pick the last item whose
  // label matches (the State section may also expose an "All" entry).
  const filtersBtn = Array.from(document.querySelectorAll(".compact-filter-menu .filter-btn")).find((b) =>
    (b.textContent ?? "").includes("Filters"),
  )!;
  await page.elementLocator(filtersBtn).click();
  await vi.waitFor(() => expect(document.querySelector(".compact-filter-menu .filter-dropdown")).not.toBeNull(), WAIT);
  const items = Array.from(document.querySelectorAll(".compact-filter-menu .filter-dropdown .filter-item")).filter(
    (el) => (el.querySelector(".filter-label")?.textContent ?? el.textContent ?? "").trim() === label,
  );
  await page.elementLocator(items[items.length - 1]!).click();
}

function activityViewBtn(): Element {
  return Array.from(document.querySelectorAll(".activity-feed .filter-btn")).find((b) =>
    (b.textContent ?? "").includes("View"),
  )!;
}

async function selectActivityViewItem(label: string): Promise<void> {
  // Open the activity feed's View dropdown fresh, then click the labelled item.
  if (!document.querySelector(".activity-feed .filter-dropdown")) {
    await page.elementLocator(activityViewBtn()).click();
    await vi.waitFor(() => expect(document.querySelector(".activity-feed .filter-dropdown")).not.toBeNull(), WAIT);
  }
  const item = Array.from(document.querySelectorAll(".activity-feed .filter-dropdown .filter-item")).find((el) =>
    (el.textContent ?? "").includes(label),
  )!;
  await page.elementLocator(item).click();
}

function activityDropdownHas(label: string): boolean {
  return Array.from(document.querySelectorAll(".activity-feed .filter-dropdown .filter-item")).some((el) =>
    (el.textContent ?? "").includes(label),
  );
}

describe("grouping toggle", () => {
  vi.setConfig({ testTimeout: 30_000 });

  let mounted: MountedBrowserApp | null = null;

  beforeEach(async () => {
    await page.viewport(1280, 900);
    localStorage.removeItem("middleman:groupingMode");
    localStorage.removeItem("middleman:groupByRepo");
    localStorage.removeItem("middleman:hideOrgName");
  });

  afterEach(async () => {
    mounted?.unmount();
    mounted = null;
    localStorage.clear();
    await resetKeyboardModuleState();
  });

  async function mountPulls(): Promise<void> {
    mounted = await mountBrowserApp("/pulls", { overrides: overrides() });
    await vi.waitFor(() => expect(document.querySelector(".pull-item")).not.toBeNull(), WAIT);
  }

  it("PR list defaults to grouped with repo headers and no badges", async () => {
    await mountPulls();
    await vi.waitFor(() => expect(document.querySelector(".repo-header")).not.toBeNull(), WAIT);
    expect(count(".repo-chip")).toBe(0);
  });

  it("PR list ungrouped shows a repo badge per item and no headers", async () => {
    await mountPulls();
    await selectPullGrouping("All");

    await vi.waitFor(() => expect(count(".repo-header")).toBe(0), WAIT);
    await vi.waitFor(() => expect(count(".repo-chip")).toBe(count(".pull-item")), WAIT);
    expect(count(".pull-item")).toBe(pulls.length);
  });

  it("toggle persists across a reload", async () => {
    await mountPulls();
    await selectPullGrouping("All");
    await vi.waitFor(() => expect(count(".repo-header")).toBe(0), WAIT);

    // Assert the choice is written to localStorage, not just held in module
    // state: this is what survives a real reload, and proves a regression that
    // kept grouping only in memory would fail here rather than silently pass.
    await vi.waitFor(() => expect(localStorage.getItem("middleman:groupingMode")).toBe("flat"), WAIT);

    // Remount then rereads that persisted value: localStorage survives teardown.
    mounted?.unmount();
    mounted = await mountBrowserApp("/pulls", { overrides: overrides() });
    await vi.waitFor(() => expect(document.querySelector(".pull-item")).not.toBeNull(), WAIT);
    expect(count(".repo-header")).toBe(0);
    expect(document.querySelector(".repo-chip")).not.toBeNull();
  });

  it("toggle syncs from PRs to the issue list", async () => {
    await mountPulls();
    await selectPullGrouping("All");
    await vi.waitFor(() => expect(count(".repo-header")).toBe(0), WAIT);

    mounted?.unmount();
    mounted = await mountBrowserApp("/issues", { overrides: overrides() });
    await vi.waitFor(() => expect(document.querySelector(".issue-item")).not.toBeNull(), WAIT);
    expect(count(".repo-header")).toBe(0);
    expect(document.querySelector(".repo-chip")).not.toBeNull();
  });

  it("toggle syncs into the threaded activity view", async () => {
    await mountPulls();
    await selectPullGrouping("All");
    await vi.waitFor(() => expect(count(".repo-header")).toBe(0), WAIT);

    mounted?.unmount();
    mounted = await mountBrowserApp("/", { overrides: overrides() });
    await vi.waitFor(() => expect(document.querySelector(".activity-feed")).not.toBeNull(), WAIT);
    await selectActivityViewItem("Threaded");
    await vi.waitFor(() => expect(document.querySelector(".threaded-view .item-row")).not.toBeNull(), WAIT);

    expect(count(".threaded-view .repo-header")).toBe(0);
    expect(document.querySelector(".threaded-view .repo-tag")).not.toBeNull();
  });

  it("threaded ungrouped keeps cross-repo items in separate threads", async () => {
    mounted = await mountBrowserApp("/", { overrides: overrides() });
    await vi.waitFor(() => expect(document.querySelector(".activity-feed")).not.toBeNull(), WAIT);
    await selectActivityViewItem("Threaded");
    await vi.waitFor(() => expect(document.querySelector(".threaded-view .item-row")).not.toBeNull(), WAIT);
    await selectActivityViewItem("All");
    await vi.waitFor(() => expect(document.querySelector(".threaded-view .repo-tag")).not.toBeNull(), WAIT);

    // widgets#1 and tools#1 must stay distinct threads, so an exact "#1" ref appears twice.
    const refOnes = Array.from(document.querySelectorAll(".threaded-view .item-row .item-ref")).filter(
      (el) => (el.textContent ?? "").trim() === "#1",
    );
    expect(refOnes.length).toBeGreaterThanOrEqual(2);
  });

  it("grouping control is hidden in flat mode and shown in threaded mode", async () => {
    mounted = await mountBrowserApp("/", { overrides: overrides() });
    await vi.waitFor(() => expect(document.querySelector(".activity-feed")).not.toBeNull(), WAIT);

    await page.elementLocator(activityViewBtn()).click();
    await vi.waitFor(() => expect(document.querySelector(".activity-feed .filter-dropdown")).not.toBeNull(), WAIT);
    expect(activityDropdownHas("By repo")).toBe(false);

    await selectActivityViewItem("Threaded");
    await vi.waitFor(() => expect(document.querySelector(".threaded-view")).not.toBeNull(), WAIT);
    await page.elementLocator(activityViewBtn()).click();
    await vi.waitFor(() => expect(document.querySelector(".activity-feed .filter-dropdown")).not.toBeNull(), WAIT);
    expect(activityDropdownHas("By repo")).toBe(true);
  });

  it("flat activity rows respect hide org name", async () => {
    mounted = await mountBrowserApp("/?view=flat", { overrides: overrides() });
    await vi.waitFor(() => expect(document.querySelector(".activity-table .activity-row")).not.toBeNull(), WAIT);

    const row = (): Element =>
      Array.from(document.querySelectorAll(".activity-table .activity-row")).find((r) =>
        (r.querySelector(".item-title")?.textContent ?? "").includes("Add widget caching layer"),
      )!;
    const repoLabel = (): string => row().querySelector(".col-repo")?.textContent?.trim() ?? "";
    await vi.waitFor(() => expect(repoLabel()).toBe("acme/widgets"), WAIT);

    await selectActivityViewItem("Hide org name");
    await vi.waitFor(() => expect(repoLabel()).toBe("widgets"), WAIT);
  });

  it("threaded grouped repo headers respect hide org name", async () => {
    mounted = await mountBrowserApp("/", { overrides: overrides() });
    await vi.waitFor(() => expect(document.querySelector(".activity-feed")).not.toBeNull(), WAIT);
    await selectActivityViewItem("Threaded");
    await vi.waitFor(() => expect(document.querySelector(".threaded-view .repo-header")).not.toBeNull(), WAIT);

    const widgetsHeader = (): Element | undefined =>
      Array.from(document.querySelectorAll(".threaded-view .repo-header .repo-name")).find((el) =>
        (el.textContent ?? "").includes("widgets"),
      );
    await vi.waitFor(() => expect(widgetsHeader()?.textContent?.trim()).toBe("acme/widgets"), WAIT);

    await selectActivityViewItem("Hide org name");
    await vi.waitFor(() => expect(widgetsHeader()?.textContent?.trim()).toBe("widgets"), WAIT);
  });

  it("threaded ungrouped rows show the item author and respect hide org name", async () => {
    mounted = await mountBrowserApp("/", { overrides: overrides() });
    await vi.waitFor(() => expect(document.querySelector(".activity-feed")).not.toBeNull(), WAIT);
    await selectActivityViewItem("Threaded");
    await vi.waitFor(() => expect(document.querySelector(".threaded-view .item-row")).not.toBeNull(), WAIT);
    await selectActivityViewItem("All");
    await vi.waitFor(() => expect(document.querySelector(".threaded-view .repo-tag")).not.toBeNull(), WAIT);

    const row = (): Element =>
      Array.from(document.querySelectorAll(".threaded-view .item-row")).find((r) =>
        (r.querySelector(".item-title")?.textContent ?? "").includes("Add widget caching layer"),
      )!;
    // The item row attributes the thread to its author (alice), not the latest actor.
    expect(row().querySelector(".cell--author")?.textContent?.trim()).toBe("alice");
    const repoLabel = (): string => row().querySelector(".repo-chip__label")?.textContent?.trim() ?? "";
    expect(repoLabel()).toBe("acme/widgets");

    await selectActivityViewItem("Hide org name");
    await vi.waitFor(() => expect(repoLabel()).toBe("widgets"), WAIT);
  });

  it("threaded ungrouped empty state shows its own message", async () => {
    mounted = await mountBrowserApp("/", {
      overrides: [
        settingsResponse(),
        (req) =>
          req.method === "GET" && req.url.pathname === "/api/v1/activity"
            ? jsonResponse({ capped: false, items: [] })
            : null,
      ],
    });
    await vi.waitFor(() => expect(document.querySelector(".activity-feed .empty-state")).not.toBeNull(), WAIT);
    expect(document.querySelector(".activity-feed .empty-state")?.textContent ?? "").toContain("No activity found");

    await selectActivityViewItem("Threaded");
    await selectActivityViewItem("All");
    await vi.waitFor(() => expect(document.querySelector(".threaded-view .empty-state")).not.toBeNull(), WAIT);
  });
});
