import { expect, test } from "@playwright/test";

import { mockApi } from "./support/mockApi";

test.beforeEach(async ({ page }) => {
  await mockApi(page);
});

test.describe("palette command dispatch", () => {
  test("'>' filters to commands; running Open settings navigates", async ({ page }) => {
    await page.goto("/pulls");
    await page.keyboard.press("Meta+K");
    await page.locator(".palette-input").fill(">settings");
    await page.keyboard.press("Enter");
    await expect(page).toHaveURL(/\/settings/);
  });

  test("typing a single character in the search input does not fire global j", async ({ page }) => {
    await page.goto("/pulls");
    // Wait for the PR list to render so .pr-list-row.selected has a
    // chance to appear if the j shortcut leaks through.
    await page.waitForSelector("[data-test='pr-list']");
    await page.keyboard.press("Meta+K");
    const before = await page.locator(".pr-list-row.selected").count();
    await page.locator(".palette-input").fill("j");
    const after = await page.locator(".pr-list-row.selected").count();
    expect(after).toBe(before);
  });

  test("Cmd+P inside the palette closes it instead of opening browser print", async ({ page }) => {
    await page.goto("/pulls");
    await page.keyboard.press("Meta+K");
    const dialog = page.getByRole("dialog", {
      name: "Command palette",
    });
    await expect(dialog).toBeVisible();
    await page.keyboard.press("Meta+P");
    await expect(dialog).toBeHidden();
  });
});
