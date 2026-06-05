import { expect, test, type Page } from "@playwright/test";

async function waitForPRList(page: Page): Promise<void> {
  await page.locator(".pull-item").first().waitFor({ state: "visible", timeout: 10_000 });
}

async function waitForIssueList(page: Page): Promise<void> {
  await page.locator(".issue-item").first().waitFor({ state: "visible", timeout: 10_000 });
}

test.describe("view navigation", () => {
  test("header tabs switch between views", async ({ page }) => {
    await page.goto("/");

    // Wait for the app to be ready (activity feed visible).
    await page.locator(".activity-feed").waitFor({ state: "visible", timeout: 10_000 });

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

  test("clicking a PR row opens the detail pane", async ({ page }) => {
    await page.goto("/pulls");
    await waitForPRList(page);

    // Detail pane should not be showing a PR detail initially.
    await expect(page.locator(".pull-detail")).not.toBeVisible();

    // Click the first PR item.
    await page.locator(".pull-item").first().click();

    // Detail pane should now show the PR detail.
    await page.locator(".pull-detail").waitFor({ state: "visible", timeout: 10_000 });
  });

  test("clicking an issue row opens the detail pane", async ({ page }) => {
    await page.goto("/issues");
    await waitForIssueList(page);

    // Detail pane should not be showing an issue detail initially.
    await expect(page.locator(".issue-detail")).not.toBeVisible();

    // Click the first issue item.
    await page.locator(".issue-item").first().click();

    // Detail pane should now show the issue detail.
    await page.locator(".issue-detail").waitFor({ state: "visible", timeout: 10_000 });
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
