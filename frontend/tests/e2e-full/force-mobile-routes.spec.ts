import { expect, test, type Page } from "@playwright/test";

async function expectPathname(page: Page, pathname: string): Promise<void> {
  await expect.poll(() => new URL(page.url()).pathname).toBe(pathname);
}

test("force-mobile flag renders canonical issue route with focus presentation", async ({ page }) => {
  await page.setViewportSize({ width: 1280, height: 800 });
  await page.addInitScript(() => {
    (
      window as unknown as {
        __MIDDLEMAN_FORCE_MOBILE_ROUTES__?: boolean;
      }
    ).__MIDDLEMAN_FORCE_MOBILE_ROUTES__ = true;
  });

  await page.goto("/issues");

  await expectPathname(page, "/issues");
  await expect(page.locator(".focus-layout .focus-list")).toBeVisible();
  await expect(page.locator(".mobile-shell")).toHaveCount(0);
  await expect(page.locator(".app-header")).toHaveCount(0);
});
