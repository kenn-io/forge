import { devices, expect, test } from "@playwright/test";

import { mockApi } from "./support/mockApi";

test.skip(({ browserName }) => browserName === "firefox", "Firefox does not support Playwright mobile emulation");
test.use({ ...devices["Pixel 5"] });

test("phone PR detail renders the primary actions as one kit action grid", async ({ page }, testInfo) => {
  await mockApi(page);

  await page.goto("/pulls/github/acme/widgets/42");

  const grid = page.getByRole("group", { name: "Pull request actions" });
  await expect(grid).toBeVisible();
  await expect(grid.getByRole("button", { name: "Approve" })).toBeVisible();
  await expect(grid.getByRole("button", { name: "Close" })).toBeVisible();
  await expect(page.locator(".actions-menu-trigger")).toHaveCount(0);
  await expect(page.locator(".actions-row--measure")).toHaveCount(0);

  // Every action stays inside the viewport: no horizontal overflow from the grid.
  const box = await grid.boundingBox();
  expect(box).not.toBeNull();
  expect(box!.x).toBeGreaterThanOrEqual(0);
  expect(box!.x + box!.width).toBeLessThanOrEqual(page.viewportSize()!.width);

  await page.screenshot({ path: testInfo.outputPath("phone-pr-actions.png"), fullPage: false });
});
