import { devices, expect, test, type Page } from "@playwright/test";

import { mockIssueFixture } from "../../src/test/mockApiFetch";
import { worklistItem, worklistResponse, worklistRoutes, type WorklistServer } from "../../src/test/worklistFixtures";
import { mockApi } from "./support/mockApi";

test.skip(({ browserName }) => browserName === "firefox", "Firefox does not support Playwright mobile emulation");
test.use({ ...devices["Pixel 5"] });

async function routeWorklists(page: Page, server: WorklistServer): Promise<void> {
  const handle = worklistRoutes(server);
  await page.route("**/api/v1/worklists**", async (route) => {
    const request = route.request();
    const response = handle({
      method: request.method().toUpperCase(),
      url: new URL(request.url()),
      bodyText: request.postData() ?? "",
    });
    if (!response) {
      await route.fallback();
      return;
    }
    await route.fulfill({
      status: response.status,
      contentType: response.headers.get("Content-Type") ?? "application/json",
      body: await response.text(),
    });
  });
}

test("phone menu opens a worklist whose items round-trip through phone detail", async ({ page }, testInfo) => {
  const server: WorklistServer = {
    lists: [worklistResponse(5, "Review queue"), worklistResponse(4, "Stale issues", [])],
    requests: [],
  };
  await mockApi(page);
  await routeWorklists(page, server);

  await page.goto("/m");
  await page.getByRole("button", { name: "Menu" }).click();
  await page.getByRole("navigation", { name: "Phone mode" }).getByRole("button", { name: "Worklists" }).click();

  await expect(page).toHaveURL(/\/m\/worklists\/5$/);
  const shell = page.locator(".mobile-shell");
  await expect(shell.locator(".app-header")).toHaveCount(0);
  const items = page.getByRole("region", { name: "Review queue items" });
  const row = items.getByRole("button", { name: "Add browser regression coverage" });
  await expect(row).toBeVisible();
  await expect(items.getByRole("button", { name: "Theme toggle does not stick" })).toBeVisible();

  // The first row ends with the shell menu, and nothing overflows the phone width.
  const width = page.viewportSize()!.width;
  const menuBox = await page.locator(".mobile-worklist__bar").getByRole("button", { name: "Menu" }).boundingBox();
  expect(menuBox).not.toBeNull();
  expect(menuBox!.x + menuBox!.width).toBeLessThanOrEqual(width);
  expect(await page.evaluate(() => document.documentElement.scrollWidth)).toBeLessThanOrEqual(width);
  const rowBox = await row.boundingBox();
  expect(rowBox).not.toBeNull();
  expect(rowBox!.x + rowBox!.width).toBeLessThanOrEqual(width);
  await page.screenshot({ path: testInfo.outputPath("phone-worklist.png"), fullPage: false });

  await row.click();
  await expect(page).toHaveURL(/\/focus\/pulls\/github\/acme\/widgets\/42\?platform_repo_id=1001$/);
  await expect(page.locator(".mobile-detail-header__badge")).toHaveText("PR #42");
  await expect(page.locator(".mobile-detail-header__back")).toHaveText("Worklist");

  await page.locator(".mobile-detail-header__back").click();
  await expect(page).toHaveURL(/\/m\/worklists\/5$/);
  await expect(row).toBeVisible();
});

test("Back from a phone worklist item lands on the same rows", async ({ page }) => {
  const list = worklistResponse(5, "Review queue");
  const template = list.items[0]!;
  list.items = Array.from({ length: 40 }, (_, index) => ({
    ...template,
    position: index,
    // Item 30 is the PR the mock API serves; the rest only need distinct keys.
    number: index === 29 ? 42 : 1000 + index,
    title: `Queued change ${index + 1}`,
    section: index < 20 ? "Ready to merge" : "Needs a decision",
  }));
  const server: WorklistServer = { lists: [list], requests: [] };
  await mockApi(page);
  await routeWorklists(page, server);

  await page.goto("/m/worklists/5");
  const viewport = page.locator(".mobile-worklist .kit-scrollbox__viewport");
  const target = page.getByRole("button", { name: "Queued change 30" });
  await target.scrollIntoViewIfNeeded();
  const parked = await viewport.evaluate((element) => element.scrollTop);
  expect(parked).toBeGreaterThan(0);

  await target.click();
  await expect(page.locator(".mobile-detail-header__badge")).toHaveText("PR #42");
  await page.locator(".mobile-detail-header__back").click();

  await expect(page).toHaveURL(/\/m\/worklists\/5$/);
  await expect(target).toBeVisible();
  await expect.poll(() => viewport.evaluate((element) => element.scrollTop)).toBe(parked);

  // A fresh visit from the menu starts at the top.
  await page.locator(".mobile-worklist__bar").getByRole("button", { name: "Menu" }).click();
  await page.getByRole("navigation", { name: "Phone mode" }).getByRole("button", { name: "Worklists" }).click();
  await expect(page).toHaveURL(/\/m\/worklists\/5$/);
  await expect(page.getByRole("button", { name: "Queued change 1", exact: true })).toBeVisible();
  await expect.poll(() => viewport.evaluate((element) => element.scrollTop)).toBe(0);
});

test("a gone item whose route another repository took over does not open that repository's PR", async ({ page }) => {
  const list = worklistResponse(5, "Review queue");
  // The item's repository left Forge; a different repository now holds its route and number.
  const gone = list.items.find((item) => item.number === 55)!;
  gone.number = 42;
  gone.title = "Retired change on the old repository";
  gone.repo = { ...gone.repo, platform_repo_id: 3003 };
  list.items = [gone];
  const server: WorklistServer = { lists: [list], requests: [] };
  await page.addInitScript(() => localStorage.setItem("kenn-forge:worklists:state", "all"));
  await mockApi(page);
  await routeWorklists(page, server);

  await page.goto("/m/worklists/5");
  await page.getByRole("button", { name: "Retired change on the old repository" }).click();

  await expect(page).toHaveURL(/\/focus\/pulls\/github\/acme\/widgets\/42\?platform_repo_id=3003$/);
  await expect(page.locator(".mobile-detail-header__badge")).toHaveText("PR #42");
  await expect(page.getByRole("tab", { name: "Conversation" })).toBeVisible();
  // The replacement repository's PR #42 must neither show nor offer actions.
  await expect(page.locator(".mobile-main").getByText("Add browser regression coverage")).toHaveCount(0);
  await expect(page.getByRole("group", { name: "Pull request actions" })).toHaveCount(0);
});

test("a long agent note and caveat start truncated and expand in place on tap", async ({ page }) => {
  const list = worklistResponse(5, "Review queue");
  const sentences = (lead: string, count: number) =>
    Array.from({ length: count }, (_, index) => `${lead} sentence ${index + 1} explains what to check next.`).join(" ");
  list.items = [
    {
      ...list.items[0]!,
      note: `${sentences("Note", 12)} Final note sentence.`,
      caveat: `${sentences("Caveat", 6)} Final caveat sentence.`,
      caveat_level: "warning",
    },
  ];
  const server: WorklistServer = { lists: [list], requests: [] };
  await mockApi(page);
  await routeWorklists(page, server);

  await page.goto("/m/worklists/5");
  const notes = page.locator(".mobile-worklist__notes");
  const note = notes.locator(".mobile-worklist__note");
  const caveat = notes.locator(".mobile-worklist__caveat-text");
  const truncated = (element: HTMLElement) => element.scrollHeight > element.clientHeight + 1;
  await expect(notes).toHaveAttribute("aria-expanded", "false");
  expect(await note.evaluate(truncated)).toBe(true);
  expect(await caveat.evaluate(truncated)).toBe(true);
  const collapsedHeight = (await notes.boundingBox())!.height;

  await notes.click();

  await expect(page).toHaveURL(/\/m\/worklists\/5$/);
  await expect(notes).toHaveAttribute("aria-expanded", "true");
  expect(await note.evaluate(truncated)).toBe(false);
  expect(await caveat.evaluate(truncated)).toBe(false);
  expect((await notes.boundingBox())!.height).toBeGreaterThan(collapsedHeight * 2);
  await notes.getByText("Final caveat sentence.", { exact: false }).scrollIntoViewIfNeeded();
  await expect(notes.getByText("Final caveat sentence.", { exact: false })).toBeInViewport();
  const width = page.viewportSize()!.width;
  expect(await page.evaluate(() => document.documentElement.scrollWidth)).toBeLessThanOrEqual(width);
  expect((await notes.boundingBox())!.x + (await notes.boundingBox())!.width).toBeLessThanOrEqual(width);

  await notes.click();
  await expect(notes).toHaveAttribute("aria-expanded", "false");
  expect(await note.evaluate(truncated)).toBe(true);
});

test("phone issue detail never shows the previously opened issue while the next one loads", async ({ page }) => {
  const titles: Record<number, string> = { 7: "Theme toggle does not stick", 8: "Search drops the last character" };
  const list = worklistResponse(5, "Review queue", [
    worklistItem({ position: 0, item_type: "issue", number: 7, title: titles[7] }),
    worklistItem({ position: 1, item_type: "issue", number: 8, title: titles[8] }),
  ]);
  const server: WorklistServer = { lists: [list], requests: [] };
  await mockApi(page);
  await routeWorklists(page, server);
  let release: () => void = () => {};
  const held = new Promise<void>((resolve) => {
    release = resolve;
  });
  await page.route(/\/api\/v1\/issues\/github\/acme\/widgets\/(7|8)(?:\/sync(?:\/async)?)?(?:\?|$)/, async (route) => {
    const number = Number(new URL(route.request().url()).pathname.match(/\/(7|8)(?:\/|$)/)![1]);
    if (number === 8) await held;
    const issue = { ...mockIssueFixture(7), Number: number, Title: titles[number] };
    await route.fulfill({
      json: {
        issue,
        repo: { ...issue.repo, platform_repo_id: 1001 },
        events: [],
        platform_host: issue.platform_host,
        repo_owner: issue.repo_owner,
        repo_name: issue.repo_name,
        detail_loaded: true,
        detail_fetched_at: "2026-03-30T14:00:00Z",
      },
    });
  });

  await page.goto("/m/worklists/5");
  const main = page.locator(".mobile-main");
  await page.getByRole("button", { name: titles[7] }).click();
  await expect(page.locator(".mobile-detail-header__badge")).toHaveText("Issue #7");
  await expect(main.getByText(titles[7]).first()).toBeVisible();
  await page.locator(".mobile-detail-header__back").click();

  await page.getByRole("button", { name: titles[8] }).click();
  await expect(page.locator(".mobile-detail-header__badge")).toHaveText("Issue #8");
  await expect(main.getByText(titles[7])).toHaveCount(0);

  release();
  await expect(main.getByText(titles[8]).first()).toBeVisible();
});
