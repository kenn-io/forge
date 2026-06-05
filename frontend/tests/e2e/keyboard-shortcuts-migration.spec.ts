import { expect, test } from "@playwright/test";

import { mockApi } from "./support/mockApi";

test.beforeEach(async ({ page }) => {
  await mockApi(page);
});

test.describe("migrated global shortcuts", () => {
  test("j and k navigate the PR list", async ({ page }) => {
    await page.goto("/pulls");
    await page.waitForSelector("[data-test='pr-list']");
    await page.keyboard.press("j");
    await expect(page.locator(".pr-list-row.selected").first()).toBeVisible();
    await page.keyboard.press("k");
    await expect(page.locator(".pr-list-row.selected").first()).toBeVisible();
  });

  test("Cmd+[ toggles the sidebar", async ({ page }) => {
    await page.goto("/pulls");
    const sidebar = page.locator("[data-test='sidebar']");
    const wasCollapsed = (await sidebar.getAttribute("data-collapsed")) === "true";
    await page.keyboard.press("Meta+BracketLeft");
    await expect(sidebar).toHaveAttribute(
      "data-collapsed",
      (!wasCollapsed).toString(),
    );
  });
});
