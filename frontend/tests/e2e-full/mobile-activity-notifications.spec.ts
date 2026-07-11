import { devices, expect, test, type Page } from "@playwright/test";
import { startIsolatedE2EServer } from "./support/e2eServer";

// The mobile activity feed (/m) shares the seeded notifications with the
// desktop feed: "review_requested" on acme/widgets#1 and "mention" on
// acme/tools#5. These tests exercise the phone workflow against the real
// Go backend so the notification reason labels, the hide-notifications
// reload, and the mark-seen request are covered end to end rather than
// through a mocked store.

const iPhone13 = devices["iPhone 13"];
test.use({
  viewport: iPhone13.viewport,
  deviceScaleFactor: iPhone13.deviceScaleFactor,
  userAgent: iPhone13.userAgent,
});

async function waitForMobileCards(page: Page): Promise<void> {
  await page.locator(".mobile-activity-card").first().waitFor({ state: "visible", timeout: 10_000 });
}

test.describe("mobile activity notifications", () => {
  test("labels notification reasons and hides them through a real reload", async ({ page }) => {
    const server = await startIsolatedE2EServer();
    try {
      await page.goto(`${server.info.base_url}/m`);
      await waitForMobileCards(page);

      // Reason labels render instead of the raw "notification" type.
      const reviewLabel = page.locator(".mobile-activity-event__body strong", { hasText: "Review requested" });
      const mentionLabel = page.locator(".mobile-activity-event__body strong", { hasText: "Mentioned" });
      await expect(reviewLabel.first()).toBeVisible();
      await expect(mentionLabel.first()).toBeVisible();

      // Hiding notifications drops the notification type from the activity
      // query and reloads, so the rows disappear via the real backend feed
      // rather than a client-side filter.
      const reload = page.waitForResponse(
        (r) => r.request().method() === "GET" && r.url().includes("/api/v1/activity"),
      );
      await page.getByRole("button", { name: "Hide notifications" }).click();
      expect((await reload).status()).toBe(200);

      await expect(page.locator(".mobile-activity-event__body strong", { hasText: "Review requested" })).toHaveCount(0);
      await expect(page.locator(".mobile-activity-event__body strong", { hasText: "Mentioned" })).toHaveCount(0);
      // Non-notification activity still renders, so the feed is filtered, not emptied.
      await expect(page.locator(".mobile-activity-card").first()).toBeVisible();
    } finally {
      await server.stop();
    }
  });

  test("surfaces a notification synced after the feed is already loaded", async ({ page }) => {
    const server = await startIsolatedE2EServer();
    try {
      await page.goto(`${server.info.base_url}/m`);
      await waitForMobileCards(page);

      // Not present until a sync pulls it in.
      await expect(page.getByText("Synced tools notification")).toHaveCount(0);

      // Stage the new upstream notification, then run a notification sync.
      // The sync inserts the row and broadcasts data_changed; the feed's
      // incremental poll only fetches rows newer than its top cursor, so
      // without that broadcast the row would stay invisible until reload.
      const staged = await page.request.post(`${server.info.base_url}/__e2e/notifications/add-synced`);
      expect(staged.ok()).toBe(true);
      const synced = await page.request.post(`${server.info.base_url}/api/v1/notifications/sync`, {
        headers: { "content-type": "application/json" },
        data: {},
      });
      expect(synced.status()).toBe(202);

      await expect(page.getByText("Synced tools notification").first()).toBeVisible({ timeout: 10_000 });
    } finally {
      await server.stop();
    }
  });

  test("marks an unread notification seen from the mobile feed", async ({ page }) => {
    const server = await startIsolatedE2EServer();
    try {
      await page.goto(`${server.info.base_url}/m`);
      await waitForMobileCards(page);

      const reviewSlot = page.locator(".mobile-activity-event-slot", { hasText: "Review requested" });
      const seen = reviewSlot.getByRole("button", { name: "Mark notification seen" });
      await expect(seen.first()).toBeVisible();

      const readResponse = page.waitForResponse(
        (r) => r.request().method() === "POST" && r.url().endsWith("/api/v1/notifications/read"),
      );
      await seen.first().click();
      expect((await readResponse).status()).toBe(200);

      // The control clears once the row reads as seen, but the event stays
      // in the feed as history.
      await expect(reviewSlot.getByRole("button", { name: "Mark notification seen" })).toHaveCount(0);
      await expect(
        page.locator(".mobile-activity-event__body strong", { hasText: "Review requested" }).first(),
      ).toBeVisible();
    } finally {
      await server.stop();
    }
  });

  test("keeps a failed notification action below the mobile header", async ({ page }) => {
    const server = await startIsolatedE2EServer();
    try {
      await page.goto(`${server.info.base_url}/m`);
      await waitForMobileCards(page);

      const reviewSlot = page.locator(".mobile-activity-event-slot", { hasText: "Review requested" });
      const seen = reviewSlot.getByRole("button", { name: "Mark notification seen" });
      await expect(seen.first()).toBeVisible();

      const removed = await page.request.delete(`${server.info.base_url}/api/v1/repo/github/acme/widgets`, {
        data: {},
      });
      expect(removed.ok()).toBe(true);

      const readResponse = page.waitForResponse(
        (response) => response.request().method() === "POST" && response.url().endsWith("/api/v1/notifications/read"),
      );
      await seen.first().click();
      expect((await readResponse).status()).toBe(200);

      const flash = page.locator(".kit-flash-stack").getByRole("status");
      await expect(flash).toContainText("Failed to mark notification as read.");
      await expect(seen.first()).toBeVisible();

      const [headerBottom, flashTop] = await Promise.all([
        page.locator(".mobile-topbar").evaluate((node) => node.getBoundingClientRect().bottom),
        page.locator(".kit-flash-stack").evaluate((node) => node.getBoundingClientRect().top),
      ]);
      expect(Math.abs(flashTop - headerBottom)).toBeLessThan(1);
    } finally {
      await server.stop();
    }
  });
});
