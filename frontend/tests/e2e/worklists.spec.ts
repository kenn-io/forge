import { expect, test, type Page } from "@playwright/test";

import { worklistResponse, worklistRoutes, type WorklistServer } from "../../src/test/worklistFixtures";
import { mockApi } from "./support/mockApi";

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

test("keeps the detail pane and rows usable when notes and the recipe are long", async ({ page }) => {
  await page.setViewportSize({ width: 1400, height: 700 });
  const longText = (lead: string, lines: number) =>
    Array.from({ length: lines }, (_, index) => `${lead} line ${index + 1} explains what to check.`).join("\n");
  const list = worklistResponse(5, "Review queue");
  list.recipe = longText("Recipe", 120);
  // 105 lines is about 3,900 characters, just under the 4,000-character note limit.
  list.items[0] = { ...list.items[0]!, note: longText("Note", 105), caveat: "Check the final caveat before merging." };
  const server: WorklistServer = { lists: [list], requests: [] };
  await mockApi(page);
  await routeWorklists(page, server);

  await page.goto("/worklists/5");
  await expect(page).toHaveURL(/selected=pr%3A42/);
  const strip = page.getByRole("region", { name: "Worklist item context" });
  const notes = strip.getByRole("region", { name: "Item notes" });
  await expect(notes.getByText("Note line 1 explains", { exact: false })).toBeVisible();

  const detailBox = await page.locator(".worklist-detail").boundingBox();
  const stripBox = await strip.boundingBox();
  expect(detailBox).not.toBeNull();
  expect(stripBox).not.toBeNull();
  expect(stripBox!.height).toBeLessThanOrEqual(detailBox!.height * 0.45);

  const caveat = notes.getByText("Check the final caveat before merging.");
  expect(await notes.evaluate((element) => element.scrollHeight > element.clientHeight * 2)).toBe(true);
  await expect(caveat).not.toBeInViewport();
  await notes.evaluate((element) => {
    element.scrollTop = element.scrollHeight;
  });
  await expect(caveat).toBeInViewport({ ratio: 1 });
  await expect(strip.getByRole("button", { name: "Close worklist item" })).toBeInViewport();

  const rail = page.getByRole("region", { name: "Review queue items" });
  await page.getByRole("button", { name: "Recipe" }).click();
  const recipe = page.getByRole("region", { name: "Recipe" });
  await expect(recipe.getByText("Recipe line 1 explains", { exact: false })).toBeVisible();
  const recipeBox = await recipe.boundingBox();
  expect(recipeBox).not.toBeNull();
  expect(recipeBox!.height).toBeLessThanOrEqual(700 * 0.3 + 1);
  await expect(rail.getByRole("button", { name: "Add browser regression coverage" })).toBeInViewport();
  await expect(rail.getByRole("button", { name: "Theme toggle does not stick" })).toBeInViewport();
});

test("collapses the context strip to one line so the detail gets the room", async ({ page }) => {
  await page.setViewportSize({ width: 900, height: 600 });
  const list = worklistResponse(5, "Review queue");
  list.items[0] = {
    ...list.items[0]!,
    note: "Approved and green, but the docs nit needs a reply before anyone merges this change.",
    caveat: "Check the final caveat before merging. ".repeat(12),
  };
  const server: WorklistServer = { lists: [list], requests: [] };
  await mockApi(page);
  await routeWorklists(page, server);

  await page.goto("/worklists/5");
  const strip = page.getByRole("region", { name: "Worklist item context" });
  await expect(strip.getByRole("region", { name: "Item notes" })).toBeVisible();
  const expandedHeight = (await strip.boundingBox())!.height;

  await strip.getByRole("button", { name: "Item notes" }).click();

  await expect(strip.getByRole("region", { name: "Item notes" })).toHaveCount(0);
  const summary = strip.getByText("Approved and green, but the docs nit", { exact: false });
  await expect(summary).toBeVisible();
  expect(await summary.evaluate((element) => element.scrollWidth > element.clientWidth)).toBe(true);
  const collapsedHeight = (await strip.boundingBox())!.height;
  expect(collapsedHeight).toBeLessThan(48);
  expect(collapsedHeight).toBeLessThan(expandedHeight / 2);
  await expect(strip.getByRole("button", { name: "Mark handled" })).toBeInViewport({ ratio: 1 });
  await expect(strip.getByRole("button", { name: "Close worklist item" })).toBeInViewport({ ratio: 1 });
});

async function openWithKennLinks(page: Page, link: string): Promise<void> {
  await page.setViewportSize({ width: 1400, height: 700 });
  await page.addInitScript(() => {
    localStorage.setItem("kenn-forge-sidebar-width", "200");
    // What an app embedding Forge sets.
    window.__KENN_EMBEDDED_KENN_LINKS__ = true;
  });
  const server: WorklistServer = { lists: [worklistResponse(5, "Review queue")], requests: [] };
  await mockApi(page);
  await routeWorklists(page, server);
  await page.route("**/api/v1/worklists/5/link", (route) => route.fulfill({ json: { link } }));
  await page.goto("/worklists/5");
}

test("fits Copy link in the rail header at its narrowest", async ({ page }) => {
  await openWithKennLinks(page, "kenn://kenn-forge/worklists/import#v1.abc");

  await expect(page.getByRole("button", { name: "Copy link" })).toBeInViewport({ ratio: 1 });
  await expect(page.getByRole("button", { name: "Dismiss" })).toBeInViewport({ ratio: 1 });
  const header = page.locator(".worklist-title-row");
  expect(await header.evaluate((element) => element.scrollWidth <= element.clientWidth)).toBe(true);
});
