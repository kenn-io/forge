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

test("works through a worklist, marks an item handled, and undoes its dismissal", async ({ page }) => {
  await page.setViewportSize({ width: 1400, height: 900 });
  const server: WorklistServer = { lists: [worklistResponse(5, "Review queue")], requests: [] };
  await mockApi(page);
  await routeWorklists(page, server);

  await page.goto("/");
  await page.getByRole("button", { name: "Worklists", exact: true }).click();

  await expect(page).toHaveURL(/\/worklists\/5\?selected=pr%3A42/);
  const rail = page.getByRole("region", { name: "Review queue items" });
  await expect(rail.getByRole("button", { name: "Theme toggle does not stick" })).toBeVisible();
  const strip = page.getByRole("region", { name: "Worklist item context" });
  await expect(strip.getByText("Approved and green; only a docs nit left.")).toBeVisible();

  await rail.getByRole("button", { name: "Theme toggle does not stick" }).click();
  await expect(page).toHaveURL(/selected=issue%3A7/);
  await expect(strip.getByText("Reporter confirmed on two browsers.")).toBeVisible();

  await strip.getByRole("button", { name: "Mark handled" }).click();
  await expect(strip.getByRole("button", { name: "Handled" })).toHaveAttribute("aria-pressed", "true");
  await expect(page.getByText("1 of 4 handled")).toBeVisible();
  await expect(rail.getByRole("button", { name: "Theme toggle does not stick" }).getByLabel("Handled")).toBeVisible();
  expect(server.requests).toContain("POST /api/v1/worklists/5/handled");

  await page.getByRole("button", { name: "Dismiss" }).click();
  await expect(page.getByText("No worklists")).toBeVisible();
  await expect(page.getByText("Dismissed Review queue.")).toBeVisible();
  expect(server.requests).toContain("POST /api/v1/worklists/5/dismiss");

  await page.getByRole("button", { name: "Undo" }).click();
  await expect(rail.getByRole("button", { name: "Add browser regression coverage" })).toBeVisible();
  expect(server.requests).toContain("POST /api/v1/worklists/5/undismiss");
});

test("keeps the detail pane and rows usable when notes and the recipe are long", async ({ page }) => {
  await page.setViewportSize({ width: 1400, height: 700 });
  const longText = (lead: string) =>
    Array.from({ length: 120 }, (_, index) => `${lead} line ${index + 1} explains what to check.`).join("\n");
  const list = worklistResponse(5, "Review queue");
  list.recipe = longText("Recipe");
  list.items[0] = { ...list.items[0]!, note: longText("Note"), caveat: "Check the final caveat before merging." };
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
