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
