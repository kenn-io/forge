import { expect, test } from "@playwright/test";

test.describe("app startup", () => {
  // Bumped above the 8s SETTINGS_STARTUP_TIMEOUT_MS so the timeout
  // path can complete and the app can finish booting.
  test.setTimeout(20_000);

  // The "startup recovers and loads data when /api/v1/settings stalls past the
  // timeout" case was converted to a Vitest component test in
  // frontend/src/App.startup-timeout.test.ts. This backend-readiness gating
  // case stays in Playwright: it exercises the real /healthz warmup sequence
  // (503 retries before the SPA fetches settings), which is integration glue
  // rather than component behavior.
  test("shows app chrome while waiting for backend readiness", async ({ page }) => {
    let healthRequests = 0;
    let settingsRequests = 0;
    let releaseBackendReadiness: () => void = () => {};
    const backendReady = new Promise<void>((resolve) => {
      releaseBackendReadiness = resolve;
    });

    await page.route("**/healthz", async (route) => {
      healthRequests++;
      if (healthRequests < 3) {
        await route.fulfill({
          status: 503,
          contentType: "application/problem+json",
          body: JSON.stringify({ status: 503, code: "serviceUnavailable" }),
        });
        return;
      }
      await backendReady;
      await route.continue();
    });

    await page.route("**/api/v1/settings", async (route) => {
      settingsRequests++;
      await route.continue();
    });

    await page.goto("/");

    await expect(page.locator(".app-header")).toBeVisible();
    await expect(page.locator(".loading-state")).toBeVisible();
    await expect.poll(() => healthRequests).toBeGreaterThanOrEqual(3);
    expect(settingsRequests).toBe(0);

    releaseBackendReadiness();
    await expect.poll(() => settingsRequests).toBeGreaterThan(0);

    await page.unrouteAll({ behavior: "ignoreErrors" });
  });
});
