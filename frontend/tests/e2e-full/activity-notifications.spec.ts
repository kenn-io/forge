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
      await page.locator(".kit-filter-dropdown__btn").click();
      await page.locator(".kit-filter-dropdown__panel").waitFor({ state: "visible" });
      await page.locator(".kit-filter-dropdown__item", { hasText: "Notifications" }).click();

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

  // The feed switches to compact rows whenever a detail pane is open
  // (ActivityFeedView passes compact={phone || hasActiveDetail}). Opening
  // one notification's detail exercises the compact mark-seen control,
  // which lives beside the row rather than inside the wide table.
  test("marks a notification seen from the compact split layout", async ({ page }) => {
    const server = await startIsolatedE2EServer();
    try {
      await page.goto(`${server.info.base_url}/`);
      await waitForTable(page);

      // Open the review notification's detail to collapse the feed into the
      // compact split layout. The unrelated "Mentioned" notification stays
      // unread and now renders as a compact row.
      await page.locator(".activity-row", { hasText: "Review requested" }).first().click();
      const mentionRow = page.locator(".compact-row-slot", { hasText: "Mentioned" });
      await expect(mentionRow.locator(".activity-compact-row")).toBeVisible();

      const seen = mentionRow.getByRole("button", { name: "Mark notification seen" });
      await expect(seen).toBeVisible();

      const readResponse = page.waitForResponse(
        (r) => r.request().method() === "POST" && r.url().endsWith("/api/v1/notifications/read"),
      );
      await seen.click();
      expect((await readResponse).status()).toBe(200);

      // The control clears once the row is read, but the row stays in the feed.
      await expect(mentionRow.getByRole("button", { name: "Mark notification seen" })).toHaveCount(0);
      await expect(mentionRow.locator(".activity-compact-row")).toBeVisible();
    } finally {
      await server.stop();
    }
  });
});
