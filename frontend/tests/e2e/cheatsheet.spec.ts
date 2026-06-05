import { expect, test } from "@playwright/test";
import { mockApi } from "./support/mockApi";

test.beforeEach(async ({ page }) => {
  await mockApi(page);
});

test("? opens the cheatsheet and shows j/k under On this view", async ({ page }) => {
  await page.goto("/pulls");
  await page.keyboard.press("?");
  const sheet = page.getByRole("dialog", {
    name: "Keyboard shortcuts",
  });
  await expect(sheet).toBeVisible();
  // j and k navigate PRs on /pulls — they should appear under "On this view".
  const onThisView = sheet.locator(".cheatsheet-section", {
    hasText: "On this view",
  });
  await expect(onThisView).toContainText(/Next pull request|Previous pull request/i);
});

test("Escape closes the cheatsheet", async ({ page }) => {
  await page.goto("/pulls");
  await page.keyboard.press("?");
  await expect(page.getByRole("dialog", { name: "Keyboard shortcuts" })).toBeVisible();
  await page.keyboard.press("Escape");
  await expect(page.getByRole("dialog", { name: "Keyboard shortcuts" })).toBeHidden();
});
