import { expect, test } from "@playwright/test";

// The activity-selection-restore flows, the legacy /mail fallthrough, and the
// list-row -> detail-pane opens moved to the browser tier
// (frontend/src/App.navigation.browser.svelte.ts). What stays here depends on the
// external mode-shell backends (Kata/Docs/Messages) or on diff rendering, which
// are full-stack concerns best exercised against the live backend.

test.describe("view navigation", () => {
  test("header tabs switch between views", async ({ page }) => {
    await page.goto("/");

    // Wait for the app to be ready (activity feed visible).
    await page.locator(".activity-feed").waitFor({ state: "visible", timeout: 10_000 });

    // Click Kata tab -> URL should contain /kata, shell renders.
    await page.locator(".view-tab", { hasText: "Kata" }).click();
    await expect(page).toHaveURL(/\/kata/);
    await expect(page.getByRole("heading", { name: "Kata" })).toBeVisible();

    // Click Docs tab -> URL should contain /docs, docs shell renders.
    await page.locator(".view-tab", { hasText: "Docs" }).click();
    await expect(page).toHaveURL(/\/docs/);
    await page.locator(".docs-workspace").waitFor({ state: "visible", timeout: 10_000 });

    // Click Messages tab -> URL should contain /messages, messages shell renders.
    await page.locator(".view-tab", { hasText: "Messages" }).click();
    await expect(page).toHaveURL(/\/messages/);
    await expect(page.getByRole("heading", { name: "Messages" })).toBeVisible();

    // Click PRs tab -> URL should contain /pulls, list renders.
    await page.locator(".view-tab", { hasText: "PRs" }).click();
    await expect(page).toHaveURL(/\/pulls/);
    await page.locator(".pull-item").first().waitFor({ state: "visible", timeout: 10_000 });

    // Click Issues tab -> URL should contain /issues, list renders.
    await page.locator(".view-tab", { hasText: "Issues" }).click();
    await expect(page).toHaveURL(/\/issues/);
    await page.locator(".issue-item").first().waitFor({ state: "visible", timeout: 10_000 });

    // Click Activity tab -> back to root, feed renders.
    await page.locator(".view-tab", { hasText: "Activity" }).click();
    // Verify pathname is exactly the base path (default "/").
    await expect(page).toHaveURL(/\/(?:\?.*)?$/);
    const basePath = new URL(page.url()).pathname.replace(/\?.*$/, "");
    expect(basePath).toBe("/");
    await page.locator(".activity-feed").waitFor({ state: "visible", timeout: 5_000 });
  });

  test("Kata shell does not expose repo selector or respond to PR number shortcuts", async ({ page }) => {
    await page.goto("/kata");

    await expect(page).toHaveURL(/\/kata$/);
    await expect(page.getByRole("heading", { name: "Kata" })).toBeVisible();
    await expect(page.getByTitle("Select repository")).not.toBeAttached();

    await page.locator("main.app-main").click();
    await page.keyboard.press("1");
    await expect(page).toHaveURL(/\/kata$/);

    await page.keyboard.press("2");
    await expect(page).toHaveURL(/\/kata$/);
  });

  test("Docs and Messages routes load their mode shells directly", async ({ page }) => {
    await page.goto("/docs");
    await expect(page).toHaveURL(/\/docs$/);
    await page.locator(".docs-workspace").waitFor({ state: "visible", timeout: 10_000 });

    await page.goto("/messages");
    await expect(page).toHaveURL(/\/messages$/);
    await expect(page.getByRole("heading", { name: "Messages" })).toBeVisible();
  });

  test("settings button toggles back to the previous route", async ({ page }) => {
    await page.goto("/pulls/github/acme/widgets/1/files");
    await expect(page).toHaveURL(/\/pulls\/github\/acme\/widgets\/1\/files$/);

    await page.getByTitle("Settings").click();
    await expect(page).toHaveURL(/\/settings$/);
    await page.locator(".settings-page").waitFor({ state: "visible", timeout: 10_000 });

    await page.getByTitle("Settings").click();
    await expect(page).toHaveURL(/\/pulls\/github\/acme\/widgets\/1\/files$/);
    await page.locator(".diff-file").first().waitFor({ state: "visible", timeout: 10_000 });
  });
});
