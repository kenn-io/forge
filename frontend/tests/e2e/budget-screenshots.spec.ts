import { expect, test } from "@playwright/test";
import { mockApi } from "./support/mockApi";

test("capture budget display dark mode screenshots (mocked)", async ({ page }) => {
  await mockApi(page);
  await page.emulateMedia({ colorScheme: "dark" });
  await page.goto("/pulls");

  await page.evaluate(() => {
    document.documentElement.classList.add("dark");
  });

  const bars = page.locator(".budget-bars");
  await expect(bars).toBeVisible();
  await expect(bars.getByText("REST")).toBeVisible();

  await bars.scrollIntoViewIfNeeded();
  await bars.screenshot({
    path: "../.superpowers/screenshots/budget-display-dark-compact.png",
  });

  await bars.click();
  const popover = page.locator(".budget-popover");
  await expect(popover).toBeVisible();
  await popover.screenshot({
    path: "../.superpowers/screenshots/budget-display-dark-popover.png",
  });

  await page.screenshot({
    path: "../.superpowers/screenshots/budget-display-dark-fullpage.png",
    fullPage: false,
  });
});
