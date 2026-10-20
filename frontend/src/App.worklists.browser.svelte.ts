import { afterEach, expect, it, vi } from "vite-plus/test";
import { page } from "vite-plus/test/browser";
import {
  emitBrowserEventSource,
  mountBrowserApp,
  pressKey,
  resetKeyboardModuleState,
  type MountedBrowserApp,
} from "./test/browserAppHarness.js";
import { WORKLIST_HANDLED_AT, worklistResponse, worklistRoutes, type WorklistServer } from "./test/worklistFixtures.js";

let mounted: MountedBrowserApp | undefined;

afterEach(async () => {
  mounted?.unmount();
  mounted = undefined;
  localStorage.clear();
  sessionStorage.clear();
  await resetKeyboardModuleState();
});

it("adds the Worklists tab when an agent writes a list and opens its first item", async () => {
  await page.viewport(1400, 900);
  const server: WorklistServer = { lists: [], requests: [] };
  mounted = await mountBrowserApp("/", { overrides: [worklistRoutes(server)] });
  await expect.element(page.getByRole("button", { name: "Activity", exact: true })).toBeVisible();
  expect(document.querySelector(".app-top-bar")?.textContent).not.toContain("Worklists");

  server.lists = [worklistResponse(5, "Review queue")];
  emitBrowserEventSource("worklists_changed", { worklist_id: 5 });
  await page.getByRole("button", { name: "Worklists", exact: true }).click();

  await vi.waitFor(() =>
    expect(window.location.pathname + window.location.search).toBe(
      "/worklists/5?selected=pr%3A42&provider=github&platform_host=github.com&repo_path=acme%2Fwidgets",
    ),
  );
  const strip = page.getByRole("region", { name: "Worklist item context" });
  await expect.element(strip.getByText("Approved and green; only a docs nit left.")).toBeVisible();
  await expect.element(page.getByRole("tab", { name: "Conversation" })).toBeVisible();

  pressKey("j");
  await vi.waitFor(() => expect(window.location.search).toContain("selected=pr%3A84"));
  await expect.element(strip.getByText("Review notes may cover an older head")).toBeVisible();
});

it("refreshes the open list when an agent rewrites it", async () => {
  await page.viewport(1400, 900);
  const server: WorklistServer = { lists: [worklistResponse(5, "Review queue")], requests: [] };
  mounted = await mountBrowserApp("/worklists/5", { overrides: [worklistRoutes(server)] });
  await expect.element(page.getByRole("heading", { name: "Review queue" })).toBeVisible();

  server.lists = [worklistResponse(5, "Review queue, rewritten")];
  emitBrowserEventSource("worklists_changed", { worklist_id: 5 });

  await expect.element(page.getByRole("heading", { name: "Review queue, rewritten" })).toBeVisible();
});

it("fits every row in the rail at its narrowest width", async () => {
  await page.viewport(1400, 900);
  localStorage.setItem("kenn-forge-worklist-rail-width", "320");
  const long = "A very long agent note that keeps going well past the width of a narrow rail ".repeat(3);
  const list = worklistResponse(5, "Review queue");
  list.items = list.items.map((item) => ({ ...item, note: long, caveat: long }));
  const server: WorklistServer = { lists: [list], requests: [] };
  mounted = await mountBrowserApp("/worklists/5", { overrides: [worklistRoutes(server)] });
  const rail = page.getByRole("region", { name: "Review queue items" });
  await expect.element(rail.getByRole("button", { name: "Theme toggle does not stick" })).toBeVisible();

  const region = rail.element();
  expect(region.getBoundingClientRect().width).toBeLessThanOrEqual(320);
  expect(region.scrollWidth).toBeLessThanOrEqual(region.clientWidth);
  const railRight = region.getBoundingClientRect().right;
  for (const row of region.querySelectorAll<HTMLElement>("li > button")) {
    expect(row.scrollWidth).toBeLessThanOrEqual(row.clientWidth);
    for (const part of row.querySelectorAll<HTMLElement>("*")) {
      expect(part.getBoundingClientRect().right).toBeLessThanOrEqual(railRight + 0.5);
    }
  }
});

it("marks the open item handled with d and moves on in rank order", async () => {
  await page.viewport(1400, 900);
  const list = worklistResponse(5, "Review queue");
  list.items = list.items.map((item) => (item.number === 84 ? { ...item, handled_at: WORKLIST_HANDLED_AT } : item));
  const server: WorklistServer = { lists: [list], requests: [], marks: [] };
  mounted = await mountBrowserApp("/worklists/5", { overrides: [worklistRoutes(server)] });
  await vi.waitFor(() => expect(window.location.search).toContain("selected=pr%3A42"));
  const rail = page.getByRole("region", { name: "Review queue items" });
  await expect.element(rail.getByRole("button", { name: "Add browser regression coverage" })).toBeVisible();

  pressKey("d");

  await vi.waitFor(() => expect(window.location.search).toContain("selected=pr%3A84"));
  await expect.element(page.getByText("2 of 4 handled")).toBeVisible();
  expect(server.marks?.map((mark) => mark.items.map((item) => item.number))).toEqual([[42]]);

  pressKey("d");

  await vi.waitFor(() => expect(window.location.search).toContain("selected=issue%3A7"));
  expect(server.marks).toHaveLength(1);
  const handledRow = rail.getByRole("button", { name: "Add browser regression coverage" }).element();
  const title = handledRow.querySelector<HTMLElement>(".worklist-row-body");
  expect(Number(getComputedStyle(title!).opacity)).toBeLessThan(1);
  const openRow = rail.getByRole("button", { name: "Theme toggle does not stick" }).element();
  expect(Number(getComputedStyle(openRow.querySelector<HTMLElement>(".worklist-row-body")!).opacity)).toBe(1);
});

it("keeps handled marks on items an agent keeps when it rewrites the list", async () => {
  await page.viewport(1400, 900);
  const server: WorklistServer = { lists: [worklistResponse(5, "Review queue")], requests: [], marks: [] };
  mounted = await mountBrowserApp("/worklists/5", { overrides: [worklistRoutes(server)] });
  const strip = page.getByRole("region", { name: "Worklist item context" });
  await strip.getByRole("button", { name: "Mark handled" }).click();
  await expect.element(strip.getByRole("button", { name: "Handled" })).toBeVisible();

  const kept = server.lists[0]!.items.find((item) => item.number === 42)!;
  const rewritten = worklistResponse(5, "Review queue, rewritten");
  rewritten.items = rewritten.items
    .filter((item) => item.number !== 55)
    .map((item) => (item.number === 42 ? { ...item, handled_at: kept.handled_at } : item));
  server.lists = [rewritten];
  emitBrowserEventSource("worklists_changed", { worklist_id: 5 });

  await expect.element(page.getByRole("heading", { name: "Review queue, rewritten" })).toBeVisible();
  await expect.element(page.getByText("1 of 3 handled")).toBeVisible();
  await expect.element(strip.getByRole("button", { name: "Handled" })).toBeVisible();
  const rail = page.getByRole("region", { name: "Review queue, rewritten items" });
  await expect
    .element(rail.getByRole("button", { name: "Add browser regression coverage" }).getByLabelText("Handled"))
    .toBeVisible();
});
