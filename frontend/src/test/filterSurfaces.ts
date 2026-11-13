// Drivers for the filter parity tests. Each surface reads and operates its own
// filter UI the way a user does: the desktop kit FilterDropdown, or the phone's
// inline FilterSectionsPanel of toggles and selects. Actions are expressed by
// intent so the two stay comparable: clicking a pick-one choice that is already
// on clears an optional section on both, and a three-way filter advances
// Any -> Yes -> No on both.
import { expect, vi } from "vite-plus/test";
import { page, userEvent } from "vite-plus/test/browser";

import type { MountedBrowserApp } from "./browserAppHarness.js";
import type { MockRequest } from "./mockApiFetch.js";

const WAIT = 10_000;

export type MenuSection = { title: string; items: string[] };

/** What a menu row shows: its tick or cross, and any count or value summary. */
export interface ItemState {
  state: "on" | "excluded" | "off";
  count: string;
  detail: string;
}

export interface Surface {
  /** Root element of the list on this surface. */
  list(): HTMLElement;
  open(): Promise<void>;
  menu(): Promise<MenuSection[]>;
  /** What each row shows, by label. */
  itemStates(): Promise<Record<string, ItemState>>;
  /** Turns the row on, or advances it, as one desktop click does. */
  click(label: string): Promise<void>;
  /** The open submenu of a row that has one, or null for a plain row. */
  submenu(label: string): Promise<HTMLElement | null>;
  reset(): Promise<void>;
}

function text(node: Element | null | undefined): string {
  return node?.textContent?.trim() ?? "";
}

export function desktopSurface(listSelector: string, triggerSelector: string): Surface {
  const list = () => document.querySelector<HTMLElement>(listSelector)!;
  const panel = () => document.querySelector<HTMLElement>(".kit-filter-dropdown__panel");
  const rows = () => [...panel()!.querySelectorAll<HTMLElement>(".kit-filter-dropdown__item")];
  const row = (label: string) =>
    rows().find((node) => text(node.querySelector(".kit-filter-dropdown__label")) === label)!;
  async function open(): Promise<void> {
    if (panel()) return;
    await page.elementLocator(list().querySelector(triggerSelector)!).click();
    await vi.waitFor(() => expect(panel()).not.toBeNull());
  }
  return {
    list,
    open,
    async menu() {
      await open();
      const sections: MenuSection[] = [];
      for (const node of panel()!.children) {
        if (node.classList.contains("kit-filter-dropdown__section-title")) {
          sections.push({ title: text(node), items: [] });
        } else if (node.classList.contains("kit-filter-dropdown__item")) {
          sections.at(-1)!.items.push(text(node.querySelector(".kit-filter-dropdown__label")));
        }
      }
      return sections;
    },
    async itemStates() {
      await open();
      return Object.fromEntries(
        rows().map((node) => [
          text(node.querySelector(".kit-filter-dropdown__label")),
          {
            state: node.classList.contains("excluded") ? "excluded" : node.classList.contains("active") ? "on" : "off",
            count: text(node.querySelector(".kit-filter-dropdown__count")),
            detail: text(node.querySelector(".kit-filter-dropdown__detail")),
          },
        ]),
      );
    },
    async click(label) {
      await open();
      // Click the menu row itself: a page-wide name such as the top bar's PRs tab can share its label.
      row(label).click();
    },
    async submenu(label) {
      await open();
      const node = row(label);
      if (!node.classList.contains("submenu")) return null;
      const flyout = () => document.querySelector<HTMLElement>(".kit-filter-dropdown__submenu");
      if (node.getAttribute("aria-expanded") !== "true") node.click();
      await vi.waitFor(() => expect(flyout()).not.toBeNull());
      return flyout()!;
    },
    async reset() {
      await open();
      await page.elementLocator(panel()!.querySelector(".kit-filter-dropdown__reset")!).click();
    },
  };
}

export function phoneSurface(listSelector: string, triggerSelector: string): Surface {
  const list = () => document.querySelector<HTMLElement>(listSelector)!;
  const panel = () => list().querySelector<HTMLElement>(".filter-sections-panel");
  const sections = () => [...panel()!.querySelectorAll<HTMLElement>(".filter-sections-panel__section")];
  const pickCell = (section: HTMLElement) => section.querySelector<HTMLElement>("[data-pick]");
  const rows = () => [...panel()!.querySelectorAll<HTMLElement>(".filter-sections-panel__item")];
  const row = (label: string) =>
    rows().find((node) => text(node.querySelector(".filter-sections-panel__label")) === label);
  // A view can keep the panel mounted while hiding it, so open means rendered and not hidden.
  const isOpen = () => {
    const node = panel();
    return node !== null && node.closest("[hidden]") === null;
  };
  async function open(): Promise<void> {
    if (isOpen()) return;
    await page.elementLocator(list().querySelector(triggerSelector)!).click();
    await vi.waitFor(() => expect(isOpen()).toBe(true));
  }
  /** Opens a select, reads its option labels, and picks `choose` if given. */
  async function select(cell: HTMLElement, choose?: string): Promise<string[]> {
    await page.elementLocator(cell.querySelector(".kit-select-dropdown__trigger")!).click();
    const options = () => [...document.querySelectorAll<HTMLElement>(".kit-select-dropdown__option")];
    await vi.waitFor(() => expect(options().length).toBeGreaterThan(0));
    const labels = options().map((node) => text(node.querySelector(".kit-select-dropdown__option-label") ?? node));
    if (choose === undefined) await userEvent.keyboard("{Escape}");
    else
      await page
        .elementLocator(
          options().find((node) => text(node.querySelector(".kit-select-dropdown__option-label") ?? node) === choose)!,
        )
        .click();
    await vi.waitFor(() => expect(document.querySelector(".kit-select-dropdown__list")).toBeNull());
    return labels;
  }
  const value = (cell: HTMLElement) => text(cell.querySelector(".kit-select-dropdown__value"));
  // A pick-one section's options do not change, so each select is opened once to read them.
  const optionCache = new Map<string, string[]>();
  async function pickOptions(cell: HTMLElement): Promise<string[]> {
    const title = text(cell.querySelector(".filter-sections-panel__title"));
    if (!optionCache.has(title)) optionCache.set(title, await select(cell));
    return optionCache.get(title)!;
  }
  /** The pick-one section offering `label`, with its options. */
  async function pickSectionFor(label: string) {
    for (const section of sections()) {
      const cell = pickCell(section);
      if (!cell) continue;
      const options = await pickOptions(cell);
      if (options.includes(label)) return { cell, options };
    }
    return null;
  }
  return {
    list,
    open,
    async menu() {
      await open();
      const result: MenuSection[] = [];
      for (const section of sections()) {
        const cell = pickCell(section);
        if (cell) {
          const options = (await pickOptions(cell)).filter((option) => option !== "Any");
          result.push({ title: text(cell.querySelector(".filter-sections-panel__title")), items: options });
        } else {
          result.push({
            title: text(section.querySelector(".filter-sections-panel__heading")),
            items: [...section.querySelectorAll(".filter-sections-panel__item .filter-sections-panel__label")].map(
              text,
            ),
          });
        }
      }
      return result;
    },
    async itemStates() {
      await open();
      const states: Record<string, ItemState> = {};
      for (const section of sections()) {
        const cell = pickCell(section);
        if (cell) {
          const current = value(cell);
          for (const option of (await pickOptions(cell)).filter((label) => label !== "Any")) {
            states[option] = { state: option === current ? "on" : "off", count: "", detail: "" };
          }
          continue;
        }
        for (const node of section.querySelectorAll<HTMLElement>(".filter-sections-panel__item")) {
          const tri = node.dataset.tri;
          const toggle = node.querySelector<HTMLInputElement>("input[type=checkbox]");
          const state: ItemState["state"] =
            tri !== undefined
              ? tri === "require"
                ? "on"
                : tri === "exclude"
                  ? "excluded"
                  : "off"
              : toggle
                ? toggle.checked
                  ? "on"
                  : "off"
                : node.classList.contains("filter-sections-panel__item--active")
                  ? "on"
                  : "off";
          states[text(node.querySelector(".filter-sections-panel__label"))] = {
            state,
            count: text(node.querySelector(".filter-sections-panel__count")),
            detail: text(node.querySelector(".filter-sections-panel__detail")),
          };
        }
      }
      return states;
    },
    async click(label) {
      await open();
      const node = row(label);
      if (node?.dataset.tri !== undefined) {
        const next = { any: "Yes", require: "No", exclude: "Any" }[node.dataset.tri as "any" | "require" | "exclude"];
        await select(node, next);
        return;
      }
      const toggle = node?.querySelector<HTMLInputElement>("input[type=checkbox]");
      if (toggle) {
        await page.elementLocator(toggle).click();
        return;
      }
      const pick = await pickSectionFor(label);
      if (!pick) throw new Error(`no phone control for "${label}"`);
      // Choosing an optional section's active option again clears it, like a desktop click.
      await select(pick.cell, value(pick.cell) === label && pick.options.includes("Any") ? "Any" : label);
    },
    async submenu(label) {
      await open();
      const node = row(label);
      if (!node?.hasAttribute("aria-expanded")) return null;
      const content = () => panel()!.querySelector<HTMLElement>(".filter-sections-panel__submenu");
      if (node.getAttribute("aria-expanded") !== "true") await page.elementLocator(node).click();
      await vi.waitFor(() => expect(content()).not.toBeNull());
      return content()!;
    },
    async reset() {
      await open();
      await page.elementLocator(panel()!.querySelector(".filter-sections-panel__reset")!).click();
    },
  };
}

/** The last list request's filters; paging and timing differ by surface and are not filters. */
export function lastListQuery(requests: MockRequest[], pathname: string): string {
  const request = requests.findLast((req) => req.method === "GET" && req.url.pathname === pathname);
  if (!request) return "";
  const params = new URLSearchParams(request.url.search);
  for (const name of ["limit", "offset", "since", "before", "after", "at_or_before", "projection"]) params.delete(name);
  params.sort();
  return params.toString();
}

export interface Outcome {
  rows: string[];
  query: string;
  items: Record<string, ItemState>;
}

export async function settled(
  mounted: MountedBrowserApp,
  surface: Surface,
  pathname: string,
  visibleRows: (list: HTMLElement) => string[],
  labels?: ReadonlySet<string>,
): Promise<Outcome> {
  // Wait until the list stops requesting and re-rendering.
  let previous = "";
  for (let frame = 0; ; frame++) {
    if (frame === 50) throw new Error(`list never settled: ${previous}`);
    await new Promise((resolve) => requestAnimationFrame(() => requestAnimationFrame(resolve)));
    const current = JSON.stringify([mounted.api.requests.length, visibleRows(surface.list())]);
    if (current === previous) break;
    previous = current;
  }
  const items = await surface.itemStates();
  return {
    rows: visibleRows(surface.list()),
    query: lastListQuery(mounted.api.requests, pathname),
    // Only the compared sections; a surface's declared extras are not compared.
    items: labels ? Object.fromEntries(Object.entries(items).filter(([label]) => labels.has(label))) : items,
  };
}

/**
 * Puts back choices Reset deliberately keeps, such as Activity's time range,
 * so every item starts from the same view: a pick-one section returns to the
 * option that was on, and any other row is clicked until it matches.
 */
async function restoreInitial(surface: Surface, menu: MenuSection[], initial: Outcome): Promise<void> {
  for (const section of menu) {
    const initiallyOn = section.items.filter((label) => initial.items[label]?.state === "on");
    for (let attempt = 0; attempt < 3; attempt++) {
      const now = await surface.itemStates();
      const differing = section.items.filter((label) => now[label]?.state !== initial.items[label]?.state);
      if (differing.length === 0) break;
      const pickOne =
        initiallyOn.length === 1 &&
        section.items.every((label) => label === initiallyOn[0] || initial.items[label]?.state === "off");
      if (pickOne) await surface.click(initiallyOn[0]!);
      else for (const label of differing) await surface.click(label);
    }
  }
}

// Submenus this test can drive. A row whose submenu holds anything else fails
// until the test learns to exercise its choices on both surfaces.
const SUBMENU_CHOICE = ".label-filter__row, .author-filter__row";
const SUBMENU_CHOICE_NAME = ".label-filter__name, .author-filter__name";

async function submenuChoices(surface: Surface, label: string): Promise<string[] | null> {
  const submenu = await surface.submenu(label);
  if (!submenu) return null;
  // A choice already in force, such as Author's "Anyone", cannot narrow anything.
  const names = () =>
    [...submenu.querySelectorAll(SUBMENU_CHOICE)]
      .filter((node) => node.getAttribute("aria-pressed") !== "true")
      .map((node) => text(node.querySelector(SUBMENU_CHOICE_NAME) ?? node));
  // Choices such as authors load after the submenu opens.
  await vi
    .waitFor(() => expect(names().length).toBeGreaterThan(0), WAIT)
    .catch(() => {
      throw new Error(`no choices this test can drive in the "${label}" submenu`);
    });
  return names();
}

async function clickChoice(surface: Surface, label: string, name: string): Promise<void> {
  const submenu = (await surface.submenu(label))!;
  const choice = [...submenu.querySelectorAll<HTMLElement>(SUBMENU_CHOICE)].find(
    (node) => text(node.querySelector(SUBMENU_CHOICE_NAME) ?? node) === name,
  )!;
  // A choice's name, such as an author, can recur elsewhere on the page.
  choice.click();
}

export interface ExerciseOptions {
  pathname: string;
  visibleRows: (list: HTMLElement) => string[];
  /** Base choices each item runs under; null runs every item once from the default view. */
  bases: (menu: MenuSection[]) => (string | null)[];
  /** Sections only this surface has, for a declared reason; neither compared nor exercised. */
  skipSections?: string[];
}

/**
 * Applies every menu item twice (on, then its next state), resetting between
 * items, under each base choice. Each Reset must restore the starting view,
 * and every row or submenu choice that starts off must change the rows or the
 * query under some base, so a filter the fixture cannot tell apart fails
 * instead of passing vacuously.
 */
export async function exercise(mounted: MountedBrowserApp, surface: Surface, options: ExerciseOptions) {
  const { pathname, visibleRows } = options;
  await vi.waitFor(() => expect(visibleRows(surface.list()).length).toBeGreaterThan(0), WAIT);
  const menu = (await surface.menu()).filter((section) => !options.skipSections?.includes(section.title));
  const labels = new Set(menu.flatMap((section) => section.items));
  const settle = () => settled(mounted, surface, pathname, visibleRows, labels);
  const initial = await settle();
  const outcomes: Record<string, unknown> = { initial };
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
          apply: () => (name === null ? surface.click(label) : clickChoice(surface, label, name)),
        });
      }
    }
  }
  await surface.reset();
  const narrowed = new Set<string>();
  for (const base of options.bases(menu)) {
    for (const { key, apply } of targets) {
      if (base !== null) await surface.click(base);
      const start = await settle();
      const states = [];
      for (let round = 0; round < 2; round++) {
        await surface.open();
        await apply();
        states.push(await settle());
      }
      if (
        states.some((state) => state.query !== start.query || JSON.stringify(state.rows) !== JSON.stringify(start.rows))
      ) {
        narrowed.add(key);
      }
      await surface.reset();
      await restoreInitial(surface, menu, initial);
      expect(await settle(), `Reset after ${base ?? "default"}: ${key}`).toEqual(initial);
      outcomes[`${base ?? "default"}: ${key}`] = states;
    }
  }
  // Each plain row and each submenu choice that starts off must narrow on its own.
  const unexercised = targets
    .filter(({ key, label }) => initial.items[label]?.state === "off" && !narrowed.has(key))
    .map(({ key }) => key);
  expect(unexercised, "menu items the fixture does not tell apart").toEqual([]);
  return { menu, submenus, outcomes };
}
