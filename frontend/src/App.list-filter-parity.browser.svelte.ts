// The phone lists must offer exactly the desktop sidebar's filters and narrow,
// group, and stack rows the same way. Each surface is driven through its own UI
// against the same fixture: the menus must list the same sections and items,
// and every item must leave both surfaces showing the same rows and sending the
// same list query. A filter added to one surface only, or one a surface offers
// but ignores, fails here.
import { afterEach, beforeEach, describe, expect, it, vi } from "vite-plus/test";
import { page } from "vite-plus/test/browser";

import type { Issue, PullRequest } from "./lib/api/types.js";
import { mountBrowserApp, resetKeyboardModuleState, type MountedBrowserApp } from "./test/browserAppHarness.js";
import { jsonResponse, mockIssueFixture, mockPullFixture, type MockRequest } from "./test/mockApiFetch.js";

const WAIT = 10_000;

type MenuSection = { title: string; items: string[] };

interface Surface {
  /** Root element of the list on this surface. */
  list(): HTMLElement;
  open(): Promise<void>;
  menu(): MenuSection[];
  item(label: string): HTMLElement;
  /** Opens the Labels row and returns its first label choice. */
  firstLabel(): Promise<HTMLElement>;
  reset(): Promise<void>;
}

const desktop = (container: string): Surface => {
  const list = () => document.querySelector<HTMLElement>(container)!;
  const panel = () => document.querySelector<HTMLElement>(".kit-filter-dropdown__panel");
  async function open(): Promise<void> {
    if (panel()) return;
    await page.elementLocator(list().querySelector(".compact-filter-menu button")!).click();
    await vi.waitFor(() => expect(panel()).not.toBeNull());
  }
  return {
    list,
    open,
    menu() {
      const sections: MenuSection[] = [];
      for (const node of panel()!.children) {
        if (node.classList.contains("kit-filter-dropdown__section-title")) {
          sections.push({ title: node.textContent!.trim(), items: [] });
        } else if (node.classList.contains("kit-filter-dropdown__item")) {
          sections.at(-1)!.items.push(node.querySelector(".kit-filter-dropdown__label")!.textContent!.trim());
        }
      }
      return sections;
    },
    item(label) {
      return [...panel()!.querySelectorAll<HTMLElement>(".kit-filter-dropdown__item")].find(
        (node) => node.querySelector(".kit-filter-dropdown__label")!.textContent!.trim() === label,
      )!;
    },
    async firstLabel() {
      const flyout = () => document.querySelector<HTMLElement>(".kit-filter-dropdown__submenu .label-filter__row");
      if (!flyout()) await page.elementLocator(this.item("Labels")).click();
      await vi.waitFor(() => expect(flyout()).not.toBeNull());
      return flyout()!;
    },
    async reset() {
      await open();
      await page.elementLocator(panel()!.querySelector(".kit-filter-dropdown__reset")!).click();
    },
  };
};

const phone = (): Surface => {
  const list = () => document.querySelector<HTMLElement>(".mobile-main .focus-list")!;
  const panel = () => list().querySelector<HTMLElement>(".filter-sections-panel");
  async function open(): Promise<void> {
    if (panel()) return;
    await page.elementLocator(list().querySelector(".mobile-triage-search-bar__filter button")!).click();
    await vi.waitFor(() => expect(panel()).not.toBeNull());
  }
  return {
    list,
    open,
    menu() {
      return [...panel()!.querySelectorAll(".filter-sections-panel__section")].map((section) => ({
        title: section.querySelector(".filter-sections-panel__title")!.textContent!.trim(),
        items: [...section.querySelectorAll(".filter-sections-panel__label")].map((label) => label.textContent!.trim()),
      }));
    },
    item(label) {
      return [...panel()!.querySelectorAll<HTMLElement>(".filter-sections-panel__item")].find(
        (node) => node.querySelector(".filter-sections-panel__label")!.textContent!.trim() === label,
      )!;
    },
    async firstLabel() {
      const choice = () => panel()!.querySelector<HTMLElement>(".filter-sections-panel__submenu .label-filter__row");
      if (!choice()) await page.elementLocator(this.item("Labels")).click();
      await vi.waitFor(() => expect(choice()).not.toBeNull());
      return choice()!;
    },
    async reset() {
      await open();
      await page.elementLocator(panel()!.querySelector(".filter-sections-panel__reset")!).click();
    },
  };
};

/** Repo group headings and item numbers in on-screen order. */
function visibleRows(list: HTMLElement): string[] {
  return [...list.querySelectorAll(".list-body .sidebar-group-header__name, .list-body .item-number")].map((node) =>
    node.textContent!.trim(),
  );
}

/** The last list request's filters; paging differs by surface and is not a filter. */
function lastListQuery(requests: MockRequest[], pathname: string): string {
  const request = requests.findLast((req) => req.method === "GET" && req.url.pathname === pathname);
  if (!request) return "";
  const params = new URLSearchParams(request.url.search);
  params.delete("limit");
  params.delete("offset");
  params.sort();
  return params.toString();
}

async function settled(mounted: MountedBrowserApp, surface: Surface, pathname: string) {
  // Wait until the list stops requesting and re-rendering.
  let previous = "";
  for (;;) {
    await new Promise((resolve) => requestAnimationFrame(() => requestAnimationFrame(resolve)));
    const current = JSON.stringify([mounted.api.requests.length, visibleRows(surface.list())]);
    if (current === previous) break;
    previous = current;
  }
  return { rows: visibleRows(surface.list()), query: lastListQuery(mounted.api.requests, pathname) };
}

async function click(surface: Surface, label: string): Promise<void> {
  await surface.open();
  const target = label === "Labels" ? await surface.firstLabel() : surface.item(label);
  await page.elementLocator(target).click();
}

/**
 * Applies every menu item twice (on, then its next state), resetting between
 * items. Grouping picks the list's render path, so every item runs under each
 * Group choice.
 */
async function exercise(mounted: MountedBrowserApp, surface: Surface, pathname: string) {
  await vi.waitFor(() => expect(visibleRows(surface.list()).length).toBeGreaterThan(0), WAIT);
  await surface.open();
  const menu = surface.menu();
  const groupings = menu.find((section) => section.title === "Group")!.items;
  const outcomes: Record<string, unknown> = { initial: await settled(mounted, surface, pathname) };
  for (const grouping of groupings) {
    for (const section of menu) {
      for (const label of section.items) {
        await click(surface, grouping);
        const states = [];
        for (let round = 0; round < 2; round++) {
          await click(surface, label);
          states.push(await settled(mounted, surface, pathname));
        }
        await surface.reset();
        outcomes[`${grouping}: ${section.title} / ${label}`] = states;
      }
    }
  }
  return { menu, outcomes };
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

describe("phone and desktop list filters", () => {
  vi.setConfig({ testTimeout: 120_000 });

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

  async function run(path: string, pathname: string, body: unknown, surface: () => Surface, phoneRoute: boolean) {
    localStorage.clear();
    window.__KENN_FORGE_FORCE_MOBILE_ROUTES__ = phoneRoute;
    await page.viewport(phoneRoute ? 390 : 1280, phoneRoute ? 844 : 900);
    mounted = await mountBrowserApp(path, {
      overrides: [(req) => (req.method === "GET" && req.url.pathname === pathname ? jsonResponse(body) : null)],
    });
    const result = await exercise(mounted, surface(), pathname);
    mounted.unmount();
    mounted = null;
    return result;
  }

  it.each([
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
  ])("offers the desktop $kind filters on the phone and applies each one the same way", async (list) => {
    const onDesktop = await run(list.desktopPath, list.pathname, list.body(), () => desktop(list.container), false);
    const onPhone = await run(list.phonePath, list.pathname, list.body(), phone, true);

    expect(onPhone.menu).toEqual(onDesktop.menu);
    expect(onPhone.outcomes).toEqual(onDesktop.outcomes);
  });
});
