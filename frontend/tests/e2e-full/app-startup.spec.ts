import { expect, test } from "@playwright/test";

test.describe("app startup", () => {
  // Bumped above the 8s SETTINGS_STARTUP_TIMEOUT_MS so the timeout
  // path can complete and the app can finish booting.
  test.setTimeout(20_000);

  test("startup recovers and loads data when /api/v1/settings stalls past the timeout", async ({ page }) => {
    let settingsRequests = 0;
    let pullsRequested = false;
    let issuesRequested = false;
    let activityRequested = false;

    // Stall settings indefinitely. The startup helper races the
    // request against an 8s timeout; once the timeout fires the
    // catch block falls back to defaults and the rest of startup
    // proceeds.
    await page.route("**/api/v1/settings", async (route) => {
      settingsRequests++;
      // Never resolve. The page will tear this route down on close.
      await new Promise(() => {});
      await route.fulfill({ status: 200, body: "{}" });
    });

    page.on("request", (req) => {
      const url = req.url();
      if (url.includes("/api/v1/pulls")) pullsRequested = true;
      if (url.includes("/api/v1/issues")) issuesRequested = true;
      if (url.includes("/api/v1/activity")) activityRequested = true;
    });

    // Control the page clock so the test can jump past the 8s
    // settings timeout instead of waiting it out in real time. The
    // timeout path still executes exactly as in production.
    await page.clock.install();

    await page.goto("/");

    // While settings is pending the loading state is shown.
    await expect(page.locator(".loading-state")).toBeVisible({
      timeout: 2_000,
    });

    // Fast-forward past SETTINGS_STARTUP_TIMEOUT_MS: the race's
    // timer fires, onReady runs, the loading state disappears and
    // the activity feed mounts, proving runAppStartup continued
    // past the timeout and the rest of the post-await wiring fired.
    await page.clock.fastForward(8_100);
    await expect(page.locator(".loading-state")).toHaveCount(0, {
      timeout: 12_000,
    });
    await expect(page.locator(".activity-table")).toBeVisible({
      timeout: 5_000,
    });

    expect(settingsRequests).toBeGreaterThanOrEqual(1);
    // pulls/issues/activity loaders fire from runAppStartup's
    // post-onReady block, proving startup got past the timeout.
    expect(pullsRequested || issuesRequested || activityRequested).toBe(true);

    await page.unrouteAll({ behavior: "ignoreErrors" });
  });
});
