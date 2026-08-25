import { expect, test, type Page } from "@playwright/test";

async function waitForPRList(page: Page): Promise<void> {
  await page.locator(".pull-item").first().waitFor({ state: "visible", timeout: 10_000 });
}

test.describe("lucide migration", () => {
  test("startup loading state renders the live spinner icon", async ({ page }) => {
    let releaseSettings: () => void = () => {};
    const settingsGate = new Promise<void>((resolve) => {
      releaseSettings = resolve;
    });

    await page.route("**/api/v1/settings", async (route) => {
      const response = await route.fetch();
      await settingsGate;
      await route.fulfill({ response });
    });

    const gotoPromise = page.goto("/pulls");

    const loadingState = page.locator(".loading-state");
    await expect(loadingState).toBeVisible();
    await expect(loadingState.locator(".kit-spinner")).toBeVisible();

    releaseSettings();
    await gotoPromise;

    await waitForPRList(page);
    await expect(loadingState).toHaveCount(0);
  });
});
