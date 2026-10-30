import { devices, expect, test, type Page } from "@playwright/test";

import { worklistResponse, worklistRoutes, type WorklistServer } from "../../src/test/worklistFixtures";
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
  await expect(page).toHaveURL(/\/focus\/pulls\/github\/acme\/widgets\/42$/);
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
});
