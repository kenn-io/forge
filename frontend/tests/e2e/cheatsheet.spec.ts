import { expect, test, type Page } from "@playwright/test";
import { mockApi } from "./support/mockApi";

test.beforeEach(async ({ page }) => {
  await mockApi(page);
});

async function focusAppBody(page: Page): Promise<void> {
  await page.locator("main").click({ position: { x: 520, y: 260 } });
}

test("? opens the cheatsheet and shows j/k under On this view", async ({ page }) => {
  await page.goto("/pulls");
  await focusAppBody(page);
  await page.keyboard.press("Shift+/");
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
  await focusAppBody(page);
  await page.keyboard.press("Shift+/");
  await expect(page.getByRole("dialog", { name: "Keyboard shortcuts" })).toBeVisible();
  await page.keyboard.press("Escape");
  await expect(page.getByRole("dialog", { name: "Keyboard shortcuts" })).toBeHidden();
});
