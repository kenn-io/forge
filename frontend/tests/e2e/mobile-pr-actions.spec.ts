import { devices, expect, test } from "@playwright/test";

import { mockApi } from "./support/mockApi";

test.skip(({ browserName }) => browserName === "firefox", "Firefox does not support Playwright mobile emulation");
test.use({ ...devices["Pixel 5"] });

test("phone PR detail renders the primary actions as one kit action grid", async ({ page }, testInfo) => {
  await mockApi(page);

  await page.goto("/pulls/github/acme/widgets/42");

  // The detail lives in the phone shell: the shared top bar sits above a
  // detail header that names the PR and offers Back to the list.
  const topbar = page.locator(".mobile-shell .mobile-topbar");
  await expect(topbar).toBeVisible();
  await expect(page.locator(".mobile-detail-header__badge")).toHaveText("PR #42");
  await expect(page.locator(".mobile-detail-header__back")).toHaveText("Pull requests");
  const topbarBox = await topbar.boundingBox();
  expect(topbarBox).not.toBeNull();
  expect(topbarBox!.y).toBeGreaterThanOrEqual(0);
  expect(topbarBox!.x + topbarBox!.width).toBeLessThanOrEqual(page.viewportSize()!.width);

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

  // A deep link has no list history, so Back lands on the phone PR list.
  await page.locator(".mobile-detail-header__back").click();
  await expect(page).toHaveURL(/\/m\/pulls$/);
  await expect(page.locator(".mobile-shell .pull-item").first()).toBeVisible();
});
