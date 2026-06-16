import { expect, test, type Page } from "@playwright/test";
import { startIsolatedE2EServer, startIsolatedE2EServerWithOptions } from "./support/e2eServer";

// Notifications no longer have a dedicated inbox; they are merged into
// the Activity feed as rows labelled by their reason, with a
// Notifications filter toggle. The e2e server seeds two notifications:
// "review_requested" on acme/widgets#1 and "mention" on acme/tools#5.

async function waitForTable(page: Page): Promise<void> {
  await page.locator(".activity-table .activity-row").first().waitFor({ state: "visible", timeout: 10_000 });
}

test.describe("notifications in the activity feed", () => {
  test("shows seeded notifications as activity rows and toggles them off", async ({ page }) => {
    const server = await startIsolatedE2EServer();
    try {
      await page.goto(`${server.info.base_url}/`);
      await waitForTable(page);

      const reviewRow = page.locator(".activity-row", { hasText: "Review requested" });
      const mentionRow = page.locator(".activity-row", { hasText: "Mentioned" });
      await expect(reviewRow.first()).toBeVisible();
      await expect(mentionRow.first()).toBeVisible();

      // The Notifications filter removes them while the underlying
      // PR/issue activity rows remain.
      await page.locator(".filter-btn").click();
      await page.locator(".filter-dropdown").waitFor({ state: "visible" });
      await page.locator(".filter-item", { hasText: "Notifications" }).click();

      await expect(reviewRow).toHaveCount(0);
      await expect(mentionRow).toHaveCount(0);
      // A non-notification row for the same PR still renders.
      await expect(page.locator(".activity-row", { hasText: "Add widget caching layer" }).first()).toBeVisible();
    } finally {
      await server.stop();
    }
  });

  test("marks a notification seen and queues the upstream read", async ({ page }) => {
    const server = await startIsolatedE2EServer();
    try {
      await page.goto(`${server.info.base_url}/`);
      await waitForTable(page);

      const reviewRow = page.locator(".activity-row", { hasText: "Review requested" });
      await expect(reviewRow).toHaveCount(1);
      const seen = reviewRow.getByRole("button", { name: "Mark notification seen" });
      await expect(seen).toBeVisible();

      // Clicking queues the GitHub read propagation and flips the row
      // to read, which removes the seen control.
      const readResponse = page.waitForResponse(
        (r) => r.request().method() === "POST" && r.url().endsWith("/api/v1/notifications/read"),
      );
      await seen.click();
      expect((await readResponse).status()).toBe(200);

      await expect(reviewRow.getByRole("button", { name: "Mark notification seen" })).toHaveCount(0);
      // The row itself stays in the feed as read history.
      await expect(reviewRow).toHaveCount(1);
    } finally {
      await server.stop();
    }
  });

  test("omits notifications from the feed when the feature is disabled", async ({ page }) => {
    const server = await startIsolatedE2EServerWithOptions({ notificationsEnabled: false });
    try {
      await page.goto(`${server.info.base_url}/`);
      await waitForTable(page);

      // Activity still renders; notification reason labels never appear.
      await expect(page.locator(".activity-row", { hasText: "Review requested" })).toHaveCount(0);
      await expect(page.locator(".activity-row", { hasText: "Mentioned" })).toHaveCount(0);

      const list = await page.request.get(`${server.info.base_url}/api/v1/notifications?state=unread`);
      expect(list.status()).toBe(403);
    } finally {
      await server.stop();
    }
  });
});
