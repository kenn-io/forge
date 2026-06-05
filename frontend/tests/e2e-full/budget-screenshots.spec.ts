import { expect, test } from "@playwright/test";

// This spec generates human-review artifacts, not a regression
// assertion. Screenshot paths are intentionally stable
// (.superpowers/screenshots/...) so the user can find the rendered
// output after a run. It's expected to run serially; don't enable
// workers for this file.
test.describe.configure({ mode: "serial" });

test("capture budget display dark mode screenshots", async ({ page }) => {
  // Force dark color scheme
  await page.emulateMedia({ colorScheme: "dark" });
  await page.goto("/pulls");

  // Apply the app's own dark class (app toggles :root.dark based on
  // user preference/media; ensure it's on for consistent styling).
  await page.evaluate(() => {
    document.documentElement.classList.add("dark");
  });

  // Verify dark theme actually took effect: the app root carries the
  // .dark class and a dark-mode CSS token resolves to a dark value.
  const html = page.locator("html");
  await expect(html).toHaveClass(/dark/);
  const bgPrimary = await page.evaluate(() =>
    window.getComputedStyle(document.documentElement).getPropertyValue("--bg-primary").trim().toLowerCase(),
  );
  // Token must be present and not light-theme value.
  expect(bgPrimary).toBeTruthy();
  expect(bgPrimary).not.toBe("#f5f6f8");

  // Wait for budget bars to load with real data. The sync store fetches
  // rate-limits on first poll; wait for the "REST" label (not "--") which
  // indicates known data has arrived.
  const bars = page.locator(".budget-bars");
  await expect(bars).toBeVisible({ timeout: 15_000 });
  // The first render may show "--" before the rate-limits poll completes.
  // Wait for the poll to land by checking for GQL label (known data).
  await expect(bars.getByText("GQL")).toBeVisible({ timeout: 15_000 });
  // If GQL arrived but REST still shows --, take the screenshot anyway
  // (the data is real, just the REST tracker may not have reported yet).

  // Screenshot 1: compact budget bars region in status bar
  await bars.scrollIntoViewIfNeeded();
  await bars.screenshot({
    path: "../.superpowers/screenshots/budget-display-dark-compact.png",
  });

  // Screenshot 2: expanded popover
  await bars.click();
  const popover = page.locator(".budget-popover");
  await expect(popover).toBeVisible();
  await popover.screenshot({
    path: "../.superpowers/screenshots/budget-display-dark-popover.png",
  });

  // Bonus: full-page with popover for context
  await page.screenshot({
    path: "../.superpowers/screenshots/budget-display-dark-fullpage.png",
    fullPage: false,
  });
});
