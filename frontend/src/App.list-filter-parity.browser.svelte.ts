// Every list shape must offer the same filters and apply them the same way.
// The PR and issue lists are compared desktop against phone; Activity is
// compared desktop against phone and must contain every PR and issue list
// filter, since it is built from the same elements. Each surface is driven
// through its own UI against one fixture (src/test/filterSurfaces.ts). A filter
// added to one shape only, one a shape offers but ignores, one Reset leaves
// on, or one the fixture does not exercise fails here.
import { afterEach, beforeEach, describe, expect, it, vi } from "vite-plus/test";
import { page } from "vite-plus/test/browser";

import type { ActivityItem, ActivitySubject, Issue, PullRequest } from "./lib/api/types.js";
import { mountBrowserApp, resetKeyboardModuleState, type MountedBrowserApp } from "./test/browserAppHarness.js";
import { desktopSurface, exercise, phoneSurface, type ExerciseOptions, type Surface } from "./test/filterSurfaces.js";
import {
  jsonResponse,
  mockIssueFixture,
  mockPullFixture,
  mockSettings,
  type MockRequest,
  type MockRouteOverride,
} from "./test/mockApiFetch.js";

const WAIT = 10_000;
const DAY = 24 * 60 * 60 * 1000;
const PHONE_FILTERS = ".mobile-triage-search-bar__filter button";

/** Repo group headings, item numbers, titles, and row repo names in on-screen order. */
function listRows(list: HTMLElement): string[] {
  const selector = [".sidebar-group-header__name", ".item-number", ".title-text", ".repo-name"].map(
    (name) => `.list-body ${name}`,
  );
  return [...list.querySelectorAll(selector.join(", "))].map((node) => node.textContent!.trim());
}

/**
 * The PRs and issues an Activity shape shows. Desktop lists events and the
 * phone shows one card per item, so the comparison is the set of items.
 */
function activityItems(list: HTMLElement): string[] {
  const read = (selector: string) =>
    [...new Set([...list.querySelectorAll(selector)].map((node) => node.textContent!.trim()))].sort();
  // Repo labels show Hide org name; desktop puts them on group headers or rows, the phone on each card.
  const repos = read(".repo-name, .repo-tag, .col-repo, .mobile-activity-card__meta > span:first-child");
  return [...read(".item-number, .item-ref, .mobile-activity-number"), ...repos.map((repo) => `repo: ${repo}`)];
}

function label(name: string) {
  return { name, color: "d73a4a", is_default: false };
}

function pullFixtures(): PullRequest[] {
  const base = mockPullFixture(42) as unknown as PullRequest;
  const gadgets = { ...base.repo, name: "gadgets", repo_path: "acme/gadgets" };
  const pull = (number: number, overrides: Partial<PullRequest>): PullRequest => ({
    ...base,
    ID: 1000 + number,
    Number: number,
    Title: `Change ${number}`,
    ReviewDecision: "",
    CIStatus: "pending",
    MergeableState: "clean",
    labels: [],
    worktree_links: [],
    ...overrides,
  });
  return [
    pull(1, { ReviewDecision: "APPROVED", CIStatus: "success", labels: [label("bug")], Starred: true }),
    pull(2, { IsDraft: true, CIStatus: "failure", labels: [label("docs")], MergeableState: "dirty" }),
    pull(3, {
      stack: { stack_id: 7, position: 1, size: 2 },
      merge_queue: {
        branch: "main",
        enqueued_at: "2026-03-30T14:00:00Z",
        observed_at: "2026-03-30T14:00:00Z",
        position: 1,
        state: "queued",
      },
    }),
    pull(4, { stack: { stack_id: 7, position: 2, size: 2 }, workspace: { id: "ws-4", status: "ready" } }),
    pull(5, { RepoID: 2, repo: gadgets, labels: [label("bug")], CIStatus: "success" }),
    pull(6, { RepoID: 2, repo: gadgets, ReviewDecision: "CHANGES_REQUESTED" }),
  ];
}

function issueFixtures(): Issue[] {
  const base = mockIssueFixture(7) as unknown as Issue;
  const gadgets = { ...base.repo, name: "gadgets", repo_path: "acme/gadgets" };
  const issue = (number: number, overrides: Partial<Issue>): Issue => ({
    ...base,
    ID: 2000 + number,
    Number: number,
    Title: `Issue ${number}`,
    labels: [],
    ...overrides,
  });
  return [
    issue(1, { labels: [label("bug")], Starred: true }),
    issue(2, { Author: "renovate[bot]", labels: [label("docs")] }),
    issue(3, { RepoID: 2, repo: gadgets, labels: [label("bug")] }),
    issue(4, { RepoID: 2, repo: gadgets }),
  ];
}

/**
 * Stamps each list query into the row titles, so a surface that sends the right
 * query but keeps showing the previous rows renders different titles and fails.
 */
function answerFor<T extends { Title: string }>(req: MockRequest, rows: T[]): T[] {
  const params = new URLSearchParams(req.url.search);
  params.delete("limit");
  params.delete("offset");
  params.sort();
  return rows.map((row) => ({ ...row, Title: `${row.Title} [${params.toString()}]` }));
}

/** Echoes saved settings, so a filter stored as a setting (Hide bots) stays on. */
function savedSettings(): MockRouteOverride {
  let hideBots: boolean = mockSettings.issues.hide_bots;
  return (req) => {
    if (req.url.pathname !== "/api/v1/settings" || (req.method !== "GET" && req.method !== "PUT")) return null;
    if (req.method === "PUT") {
      hideBots = (JSON.parse(req.bodyText) as { issues?: { hide_bots?: boolean } }).issues?.hide_bots ?? hideBots;
    }
    return jsonResponse({ ...mockSettings, issues: { ...mockSettings.issues, hide_bots: hideBots } });
  };
}

/** One event per item, so each filter's effect shows in which items remain. */
function activityFixture() {
  const now = Date.now();
  const repo = {
    provider: "github",
    platform_host: "github.com",
    platform_repo_id: 1001,
    owner: "acme",
    name: "widgets",
    repo_path: "acme/widgets",
  };
  const subject = (
    itemType: "pr" | "issue",
    number: number,
    overrides: Partial<ActivitySubject> = {},
  ): ActivitySubject => ({
    activity_at: new Date(now - number * 60_000).toISOString(),
    event_ledger_revision: `r${number}`,
    item_number: number,
    item_state: "open",
    item_title: `Item ${number}`,
    item_author: "octocat",
    item_type: itemType,
    item_url: `https://github.com/acme/widgets/${itemType === "pr" ? "pull" : "issues"}/${number}`,
    platform_host: "github.com",
    repo_owner: "acme",
    repo_name: "widgets",
    repo,
    labels: [],
    ...overrides,
  });
  const subjects = [
    subject("pr", 11, { labels: [label("bug")], workspace: { id: "ws-11", status: "ready" } }),
    subject("pr", 12, { labels: [label("docs")] }),
    subject("issue", 21, { item_author: "renovate[bot]", labels: [label("bug")] }),
    subject("issue", 22, { item_state: "closed" }),
    // Older items, so each time range shows a different set.
    subject("pr", 13, { activity_at: new Date(now - 3 * DAY).toISOString() }),
    subject("pr", 14, { activity_at: new Date(now - 20 * DAY).toISOString() }),
    subject("pr", 15, { activity_at: new Date(now - 60 * DAY).toISOString() }),
  ];
  const items = subjects.map(
    (item, index): ActivityItem =>
      ({
        ...item,
        id: `event-${item.item_number}`,
        cursor: `event-${item.item_number}`,
        activity_type: "comment",
        // One event author is a bot, for Activity's own Hide bots filter.
        author: index === 1 ? "dependabot[bot]" : "octocat",
        body_preview: "",
        created_at: item.activity_at,
      }) as unknown as ActivityItem,
  );
  return { items, item_activity: subjects };
}

/** The start of the window a request asks for: a range such as "7d", or a timestamp. */
function sinceOf(req: MockRequest): number {
  const since = req.url.searchParams.get("since") ?? "";
  const range = /^(\d+)([hd])$/.exec(since);
  if (range) return Date.now() - Number(range[1]) * (range[2] === "h" ? 60 * 60 * 1000 : DAY);
  return since ? Date.parse(since) : 0;
}

function answerActivity(): MockRouteOverride {
  const fixture = activityFixture();
  // The server keeps only activity inside the requested time window.
  const within = (req: MockRequest) => (row: { activity_at?: string; created_at?: string }) =>
    Date.parse(row.activity_at ?? row.created_at ?? "") >= sinceOf(req);
  return (req) =>
    req.method === "GET" && req.url.pathname === "/api/v1/activity"
      ? jsonResponse({
          items: fixture.items.filter(within(req)),
          item_activity: fixture.item_activity.filter(within(req)),
          capped: false,
          item_activity_capped: false,
          event_cursor: "cursor",
          use_workspace_activity_for_recency: false,
          workspace_activity: [],
        })
      : req.url.pathname === "/api/v1/activity/authors"
        ? jsonResponse({ authors: ["octocat", "renovate[bot]"] })
        : null;
}

describe("list filters across desktop, phone, and Activity", () => {
  vi.setConfig({ testTimeout: 240_000 });

  let mounted: MountedBrowserApp | null = null;

  beforeEach(() => {
    localStorage.clear();
  });

  afterEach(async () => {
    mounted?.unmount();
    mounted = null;
    window.__KENN_FORGE_FORCE_MOBILE_ROUTES__ = false;
    localStorage.clear();
    await resetKeyboardModuleState();
  });

  async function run(
    path: string,
    overrides: MockRouteOverride[],
    surface: Surface,
    phoneRoute: boolean,
    options: ExerciseOptions,
  ) {
    localStorage.clear();
    window.__KENN_FORGE_FORCE_MOBILE_ROUTES__ = phoneRoute;
    await page.viewport(phoneRoute ? 390 : 1280, phoneRoute ? 844 : 900);
    mounted = await mountBrowserApp(path, { overrides: [...overrides, savedSettings()] });
    const result = await exercise(mounted, surface, options);
    mounted.unmount();
    mounted = null;
    return result;
  }

  const listOptions = (pathname: string): ExerciseOptions => ({
    pathname,
    visibleRows: listRows,
    // Grouping picks the list's render path, so every item runs under each Group choice.
    bases: (menu) => menu.find((section) => section.title === "Group")!.items,
  });
  const answerList =
    (pathname: string, body: () => { Title: string }[]): MockRouteOverride =>
    (req) =>
      req.method === "GET" && req.url.pathname === pathname ? jsonResponse(answerFor(req, body())) : null;

  const lists = [
    {
      kind: "PR",
      desktopPath: "/pulls",
      phonePath: "/m/pulls",
      pathname: "/api/v1/pulls",
      container: ".pull-list",
      body: pullFixtures,
    },
    {
      kind: "issue",
      desktopPath: "/issues",
      phonePath: "/m/issues",
      pathname: "/api/v1/issues",
      container: ".issue-list",
      body: issueFixtures,
    },
  ];

  it.each(lists)("offers the desktop $kind filters on the phone and applies each one the same way", async (list) => {
    const answer = answerList(list.pathname, list.body);
    const onDesktop = await run(
      list.desktopPath,
      [answer],
      desktopSurface(list.container, ".compact-filter-menu button"),
      false,
      listOptions(list.pathname),
    );
    const onPhone = await run(
      list.phonePath,
      [answer],
      phoneSurface(".mobile-main .focus-list", PHONE_FILTERS),
      true,
      listOptions(list.pathname),
    );

    expect(onPhone.menu).toEqual(onDesktop.menu);
    expect(onPhone.submenus).toEqual(onDesktop.submenus);
    expect(onPhone.outcomes).toEqual(onDesktop.outcomes);
  });

  it("offers the same Activity filters on the phone and applies each one the same way", async () => {
    const options: ExerciseOptions = {
      pathname: "/api/v1/activity",
      visibleRows: activityItems,
      bases: () => [null],
      // The phone inbox always shows one card per PR or issue with its newest
      // events, so View, Group, and commit roll-up are desktop feed layouts.
      skipSections: ["Layout", "Group", "Commits"],
      // Reset clears filters but keeps which item types show and the time range.
      keptByReset: ["Items", "Time range"],
    };
    const onDesktop = await run(
      "/",
      [answerActivity()],
      desktopSurface(".activity-feed", ".filters-wrap .kit-filter-dropdown__btn"),
      false,
      options,
    );
    const onPhone = await run(
      "/m",
      [answerActivity()],
      phoneSurface(".mobile-activity-inbox", PHONE_FILTERS),
      true,
      options,
    );

    expect(onPhone.menu).toEqual(onDesktop.menu);
    expect(onPhone.submenus).toEqual(onDesktop.submenus);
    expect(onPhone.outcomes).toEqual(onDesktop.outcomes);
  });

  it("gives Activity every PR list and issue list filter", async () => {
    await page.viewport(1280, 900);
    const labelsOf = async (path: string, overrides: MockRouteOverride[], surface: Surface) => {
      mounted = await mountBrowserApp(path, { overrides: [...overrides, savedSettings()] });
      await vi.waitFor(() => expect(surface.list()).not.toBeNull(), WAIT);
      const labels = (await surface.menu()).flatMap((section) => section.items);
      mounted.unmount();
      mounted = null;
      return labels;
    };
    const activity = await labelsOf(
      "/",
      [answerActivity()],
      desktopSurface(".activity-feed", ".filters-wrap .kit-filter-dropdown__btn"),
    );
    const pulls = await labelsOf(
      "/pulls",
      [answerList("/api/v1/pulls", pullFixtures)],
      desktopSurface(".pull-list", ".compact-filter-menu button"),
    );
    const issues = await labelsOf(
      "/issues",
      [answerList("/api/v1/issues", issueFixtures)],
      desktopSurface(".issue-list", ".compact-filter-menu button"),
    );

    // Stack tree arranges PR list rows; Activity rows are events, so it has no stack layout.
    const listOnly = ["Stack tree"];
    const missing = [...new Set([...pulls, ...issues])].filter(
      (label) => !listOnly.includes(label) && !activity.includes(label),
    );
    expect(missing, "PR or issue list filters Activity lacks").toEqual([]);
  });

  it("keeps every phone filter reachable and touch-sized in a short landscape viewport", async () => {
    window.__KENN_FORGE_FORCE_MOBILE_ROUTES__ = true;
    await page.viewport(740, 360);
    mounted = await mountBrowserApp("/m/pulls", {
      overrides: [
        (req) => (req.method === "GET" && req.url.pathname === "/api/v1/pulls" ? jsonResponse(pullFixtures()) : null),
      ],
    });
    const surface = phoneSurface(".mobile-main .focus-list", PHONE_FILTERS);
    await vi.waitFor(() => expect(listRows(surface.list()).length).toBeGreaterThan(0), WAIT);
    await surface.open();
    await surface.submenu("Labels");

    const reset = surface.list().querySelector<HTMLElement>(".filter-sections-panel__reset")!;
    reset.scrollIntoView({ block: "end" });
    const resetRect = reset.getBoundingClientRect();
    expect(resetRect.bottom).toBeLessThanOrEqual(window.innerHeight);
    expect(resetRect.top).toBeGreaterThanOrEqual(0);

    const targets = surface
      .list()
      .querySelectorAll(".label-filter__row, .sidebar-group-header, .filter-sections-panel__item");
    expect(targets.length).toBeGreaterThan(0);
    for (const target of targets) {
      expect(target.getBoundingClientRect().height, target.textContent!.trim()).toBeGreaterThanOrEqual(40);
    }
  });
});
