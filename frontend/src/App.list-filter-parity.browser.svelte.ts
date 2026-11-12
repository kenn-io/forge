// The phone lists must offer exactly the desktop sidebar's filters and narrow,
// group, and stack rows the same way. Each surface is driven through its own UI
// against the same fixture: the menus must list the same sections and items,
// and every item must leave both surfaces showing the same rows and sending the
// same list query. A filter added to one surface only, one a surface offers but
// ignores, one Reset leaves on, or one the fixture does not exercise fails here.
import { afterEach, beforeEach, describe, expect, it, vi } from "vite-plus/test";
import { page } from "vite-plus/test/browser";

import type { Issue, PullRequest } from "./lib/api/types.js";
import { mountBrowserApp, resetKeyboardModuleState, type MountedBrowserApp } from "./test/browserAppHarness.js";
import {
  jsonResponse,
  mockIssueFixture,
  mockPullFixture,
  mockSettings,
  type MockRequest,
  type MockRouteOverride,
} from "./test/mockApiFetch.js";

const WAIT = 10_000;

type MenuSection = { title: string; items: string[] };
/** What a menu row shows: its tick or cross, and any count or value summary. */
interface ItemState {
  state: "on" | "excluded" | "off";
  count: string;
  detail: string;
}

function itemState(node: Element, classPrefix: string, on: string, excluded: string): ItemState {
  const text = (part: string) => node.querySelector(`.${classPrefix}__${part}`)?.textContent?.trim() ?? "";
  const state = node.classList.contains(excluded) ? "excluded" : node.classList.contains(on) ? "on" : "off";
  return { state, count: text("count"), detail: text("detail") };
}

interface Surface {
  /** Root element of the list on this surface. */
  list(): HTMLElement;
  open(): Promise<void>;
  menu(): MenuSection[];
  /** What each row shows, by label. */
  itemStates(): Record<string, ItemState>;
  item(label: string): HTMLElement;
  /** The open submenu of a row that has one, or null for a plain row. */
  submenu(label: string): Promise<HTMLElement | null>;
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
    itemStates() {
      return Object.fromEntries(
        [...panel()!.querySelectorAll(".kit-filter-dropdown__item")].map((node) => [
          node.querySelector(".kit-filter-dropdown__label")!.textContent!.trim(),
          itemState(node, "kit-filter-dropdown", "active", "excluded"),
        ]),
      );
    },
    item(label) {
      return [...panel()!.querySelectorAll<HTMLElement>(".kit-filter-dropdown__item")].find(
        (node) => node.querySelector(".kit-filter-dropdown__label")!.textContent!.trim() === label,
      )!;
    },
    async submenu(label) {
      const row = this.item(label);
      if (!row.classList.contains("submenu")) return null;
      const flyout = () => document.querySelector<HTMLElement>(".kit-filter-dropdown__submenu");
      if (row.getAttribute("aria-expanded") !== "true") await page.elementLocator(row).click();
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
    itemStates() {
      return Object.fromEntries(
        [...panel()!.querySelectorAll(".filter-sections-panel__item")].map((node) => [
          node.querySelector(".filter-sections-panel__label")!.textContent!.trim(),
          itemState(
            node,
            "filter-sections-panel",
            "filter-sections-panel__item--active",
            "filter-sections-panel__item--excluded",
          ),
        ]),
      );
    },
    item(label) {
      return [...panel()!.querySelectorAll<HTMLElement>(".filter-sections-panel__item")].find(
        (node) => node.querySelector(".filter-sections-panel__label")!.textContent!.trim() === label,
      )!;
    },
    async submenu(label) {
      const row = this.item(label);
      if (!row.hasAttribute("aria-expanded")) return null;
      const content = () => panel()!.querySelector<HTMLElement>(".filter-sections-panel__submenu");
      if (row.getAttribute("aria-expanded") !== "true") await page.elementLocator(row).click();
      await vi.waitFor(() => expect(content()).not.toBeNull());
      return content()!;
    },
    async reset() {
      await open();
      await page.elementLocator(panel()!.querySelector(".filter-sections-panel__reset")!).click();
    },
  };
};

/** Repo group headings, item numbers, and row repo names in on-screen order. */
function visibleRows(list: HTMLElement): string[] {
  const selector = [".sidebar-group-header__name", ".item-number", ".repo-name"].map((name) => `.list-body ${name}`);
  return [...list.querySelectorAll(selector.join(", "))].map((node) => node.textContent!.trim());
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

interface Outcome {
  rows: string[];
  query: string;
  items: Record<string, ItemState>;
}

async function settled(mounted: MountedBrowserApp, surface: Surface, pathname: string): Promise<Outcome> {
  // Wait until the list stops requesting and re-rendering.
  let previous = "";
  for (let frame = 0; ; frame++) {
    if (frame === 50) throw new Error(`list never settled: ${previous}`);
    await new Promise((resolve) => requestAnimationFrame(() => requestAnimationFrame(resolve)));
    const current = JSON.stringify([mounted.api.requests.length, visibleRows(surface.list())]);
    if (current === previous) break;
    previous = current;
  }
  await surface.open();
  return {
    rows: visibleRows(surface.list()),
    query: lastListQuery(mounted.api.requests, pathname),
    items: surface.itemStates(),
  };
}

async function click(surface: Surface, label: string): Promise<void> {
  await surface.open();
  await page.elementLocator(surface.item(label)).click();
}

// Submenus this test can drive. A row whose submenu holds anything else fails
// until the test learns to exercise its choices on both surfaces.
const SUBMENU_CHOICE = ".label-filter__row";
const SUBMENU_CHOICE_NAME = ".label-filter__name";

async function submenuChoices(surface: Surface, label: string): Promise<string[] | null> {
  await surface.open();
  const submenu = await surface.submenu(label);
  if (!submenu) return null;
  const names = [...submenu.querySelectorAll(`${SUBMENU_CHOICE} ${SUBMENU_CHOICE_NAME}`)].map((node) =>
    node.textContent!.trim(),
  );
  if (names.length === 0) throw new Error(`no choices this test can drive in the "${label}" submenu`);
  return names;
}

async function clickChoice(surface: Surface, label: string, name: string): Promise<void> {
  const submenu = (await surface.submenu(label))!;
  const choice = [...submenu.querySelectorAll<HTMLElement>(SUBMENU_CHOICE)].find(
    (node) => node.querySelector(SUBMENU_CHOICE_NAME)!.textContent!.trim() === name,
  )!;
  await page.elementLocator(choice).click();
}

/**
 * Applies every menu item twice (on, then its next state), resetting between
 * items. Grouping picks the list's render path, so every item runs under each
 * Group choice. Each Reset must restore the starting view, and every row or
 * submenu choice that starts off must change the rows or the query under some
 * grouping, so a filter the fixture cannot tell apart fails instead of passing
 * vacuously.
 */
async function exercise(mounted: MountedBrowserApp, surface: Surface, pathname: string) {
  await vi.waitFor(() => expect(visibleRows(surface.list()).length).toBeGreaterThan(0), WAIT);
  await surface.open();
  const menu = surface.menu();
  const initial = await settled(mounted, surface, pathname);
  const groupings = menu.find((section) => section.title === "Group")!.items;
  const outcomes: Record<string, unknown> = { initial };
  // Every plain row is one target; a row with a submenu is one target per choice.
  const submenus: Record<string, string[]> = {};
  const targets: { key: string; label: string; apply: () => Promise<void> }[] = [];
  for (const section of menu) {
    for (const label of section.items) {
      const choices = await submenuChoices(surface, label);
      if (choices) submenus[label] = choices;
      for (const name of choices ?? [null]) {
        targets.push({
          key: `${section.title} / ${label}${name === null ? "" : ` / ${name}`}`,
          label,
          apply: () => (name === null ? click(surface, label) : clickChoice(surface, label, name)),
        });
      }
    }
  }
  await surface.reset();
  const narrowed = new Set<string>();
  for (const grouping of groupings) {
    for (const { key, apply } of targets) {
      await click(surface, grouping);
      const base = await settled(mounted, surface, pathname);
      const states = [];
      for (let round = 0; round < 2; round++) {
        await surface.open();
        await apply();
        states.push(await settled(mounted, surface, pathname));
      }
      if (
        states.some((state) => state.query !== base.query || JSON.stringify(state.rows) !== JSON.stringify(base.rows))
      ) {
        narrowed.add(key);
      }
      await surface.reset();
      expect(await settled(mounted, surface, pathname), `Reset after ${grouping}: ${key}`).toEqual(initial);
      outcomes[`${grouping}: ${key}`] = states;
    }
  }
  // Each plain row and each submenu choice must narrow on its own.
  const unexercised = targets
    .filter(({ key, label }) => initial.items[label]?.state === "off" && !narrowed.has(key))
    .map(({ key }) => key);
  expect(unexercised, "menu items the fixture does not tell apart").toEqual([]);
  return { menu, submenus, outcomes };
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
 * Answers each list query with the fixture in a different order, so a surface
 * that sends the right query but keeps showing the previous rows fails. Every
 * row stays in the answer, so each filter still has its rows to narrow.
 */
function answerFor<T>(req: MockRequest, rows: T[]): T[] {
  const params = new URLSearchParams(req.url.search);
  params.delete("limit");
  params.delete("offset");
  params.sort();
  let hash = 7;
  for (const char of params.toString()) hash = (hash * 31 + char.charCodeAt(0)) >>> 0;
  const shift = hash % rows.length;
  return [...rows.slice(shift), ...rows.slice(0, shift)];
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

  async function run(path: string, pathname: string, body: unknown[], surface: () => Surface, phoneRoute: boolean) {
    localStorage.clear();
    window.__KENN_FORGE_FORCE_MOBILE_ROUTES__ = phoneRoute;
    await page.viewport(phoneRoute ? 390 : 1280, phoneRoute ? 844 : 900);
    mounted = await mountBrowserApp(path, {
      overrides: [
        (req) => (req.method === "GET" && req.url.pathname === pathname ? jsonResponse(answerFor(req, body)) : null),
        savedSettings(),
      ],
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
    expect(onPhone.submenus).toEqual(onDesktop.submenus);
    expect(onPhone.outcomes).toEqual(onDesktop.outcomes);
  });

  it("keeps every phone filter reachable and touch-sized in a short landscape viewport", async () => {
    window.__KENN_FORGE_FORCE_MOBILE_ROUTES__ = true;
    await page.viewport(740, 360);
    mounted = await mountBrowserApp("/m/pulls", {
      overrides: [
        (req) => (req.method === "GET" && req.url.pathname === "/api/v1/pulls" ? jsonResponse(pullFixtures()) : null),
      ],
    });
    const surface = phone();
    await vi.waitFor(() => expect(visibleRows(surface.list()).length).toBeGreaterThan(0), WAIT);
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
