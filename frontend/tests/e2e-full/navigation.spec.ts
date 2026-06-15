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

  test("returning to Activity from PRs restores the selected item", async ({ page }) => {
    await page.goto("/");
    await page.locator(".activity-feed").waitFor({ state: "visible", timeout: 10_000 });
    await page.locator(".activity-table .activity-row").first().waitFor({ state: "visible", timeout: 10_000 });

    // Select a PR in the feed: the split view opens and the selection is
    // written to the URL query string.
    const prRow = page
      .locator(".activity-row")
      .filter({ has: page.locator(".badge", { hasText: "PR" }) })
      .first();
    await prRow.click();
    await expect(page.locator(".activity-shell.activity-shell--split")).toBeVisible();
    await expect(page).toHaveURL(/\/\?.*selected=pr%3A/);

    // Leave for the PRs tab, then return to Activity via the top bar.
    await page.locator(".view-tab", { hasText: "PRs" }).click();
    await expect(page).toHaveURL(/\/pulls\//);

    await page.locator(".view-tab", { hasText: "Activity" }).click();

    // The previous Activity view comes back: the selection survives in the
    // URL and the split view reopens, instead of resetting to a bare "/".
    await expect(page).toHaveURL(/\/\?.*selected=pr%3A/);
    await expect(page.locator(".activity-shell.activity-shell--split")).toBeVisible();
  });

  test("returning to Activity from the settings gear restores the selected item", async ({ page }) => {
    await page.goto("/");
    await page.locator(".activity-feed").waitFor({ state: "visible", timeout: 10_000 });
    await page.locator(".activity-table .activity-row").first().waitFor({ state: "visible", timeout: 10_000 });

    const prRow = page
      .locator(".activity-row")
      .filter({ has: page.locator(".badge", { hasText: "PR" }) })
      .first();
    await prRow.click();
    await expect(page.locator(".activity-shell.activity-shell--split")).toBeVisible();
    await expect(page).toHaveURL(/\/\?.*selected=pr%3A/);

    // The settings gear leaves Activity without going through the tab bar.
    await page.getByTitle("Settings").click();
    await expect(page).toHaveURL(/\/settings$/);

    await page.locator(".view-tab", { hasText: "Activity" }).click();

    await expect(page).toHaveURL(/\/\?.*selected=pr%3A/);
    await expect(page.locator(".activity-shell.activity-shell--split")).toBeVisible();
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

  test("old mail route does not open Messages", async ({ page }) => {
    await page.goto("/mail?q=label%3AInbox");

    await page.locator(".activity-feed").waitFor({ state: "visible", timeout: 10_000 });
    await expect(page.getByRole("heading", { name: "Messages" })).toHaveCount(0);
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
