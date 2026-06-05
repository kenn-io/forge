import { expect, test } from "@playwright/test";

import { mockApi } from "./support/mockApi";

test.beforeEach(async ({ page }) => {
  await mockApi(page);
});

test("recents: select PR, close, reopen, see recent at top", async ({ page }) => {
  await page.goto("/pulls");
  await page.keyboard.press("Meta+K");
  // Pick the first PR row from the Pull requests group. The exact PR
  // depends on the mock data render order, so we don't lock to a specific
  // title — only that some PR row exists in that group.
  const dialog = page.getByRole("dialog", { name: "Command palette" });
  const firstPRRow = dialog.locator(".palette-group", { hasText: "Pull requests" }).locator(".palette-row").first();
  await firstPRRow.click();
  // The click navigates to the PR detail. Go back to /pulls and reopen the
  // palette to verify the chosen PR landed in the recents store.
  await page.goto("/pulls");
  await page.keyboard.press("Meta+K");
  const reopenedDialog = page.getByRole("dialog", {
    name: "Command palette",
  });
  const recents = reopenedDialog.locator(".palette-group", {
    hasText: "Recently used",
  });
  await expect(recents).toBeVisible();
  await expect(recents.locator(".palette-row").first()).toContainText(/#/);
});

test("recents: typing a query hides the Recently used section", async ({ page }) => {
  // Seed a recent by selecting a PR first.
  await page.goto("/pulls");
  await page.keyboard.press("Meta+K");
  const dialog = page.getByRole("dialog", { name: "Command palette" });
  const firstPRRow = dialog.locator(".palette-group", { hasText: "Pull requests" }).locator(".palette-row").first();
  await firstPRRow.click();
  await page.goto("/pulls");
  await page.keyboard.press("Meta+K");
  const reopened = page.getByRole("dialog", {
    name: "Command palette",
  });
  await expect(reopened.locator(".palette-group", { hasText: "Recently used" })).toBeVisible();
  // Typing a query should hide the recents section so the search results
  // own the empty/non-empty rendering paths.
  await reopened.locator(".palette-input").fill("a");
  await expect(reopened.locator(".palette-group", { hasText: "Recently used" })).toBeHidden();
});
