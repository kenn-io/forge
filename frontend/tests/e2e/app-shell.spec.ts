import { expect, test } from "@playwright/test";

import { mockApi } from "./support/mockApi";

// App-shell smoke + live-header theme-toggle wiring.
//
// Both facts used to live in tests/e2e/theme-toggle.spec.ts, which was removed
// when the toggle's detailed render assertions moved to the browser tier
// (src/lib/components/layout/ThemeToggle.browser.svelte.ts). That browser test
// mounts ThemeToggle in isolation, so it cannot prove that the real /pulls route
// renders mocked data and counts, that Issues navigation works, or that
// AppHeader actually mounts the toggle and the click path reaches the theme
// store in the shipped shell. That route-level coverage is restored here; the
// computed-style / icon-fill / icon-swap assertions stay in the browser tier.

test.beforeEach(async ({ page }) => {
  await mockApi(page);
});

test("renders mocked frontend data", async ({ page }) => {
  await page.goto("/pulls");

  await expect(page.getByText("Add browser regression coverage")).toBeVisible();
  await expect(page.getByText("acme/widgets")).toBeVisible();
  await expect(page.getByRole("contentinfo").getByText("3 PRs")).toBeVisible();
  await expect(page.getByRole("contentinfo").getByText("1 repos")).toBeVisible();

  await page.getByRole("button", { name: "Issues" }).click();

  await expect(page.getByText("Theme toggle does not stick")).toBeVisible();
  await expect(page.getByRole("contentinfo").getByText("1 issues")).toBeVisible();
});

test("header theme toggle flips dark mode in the live shell", async ({ page }) => {
  await page.emulateMedia({ colorScheme: "light" });
  await page.goto("/");

  const root = page.locator("html");
  const button = page.getByTitle("Toggle theme");

  // Detailed computed-style / icon-fill / icon-swap assertions live in the
  // browser-tier ThemeToggle test. Here we only verify the integration it
  // cannot: AppHeader mounts the real toggle, and clicking it flips html.dark
  // through the shipped shell.
  await expect(button).toBeVisible();
  await expect(root).not.toHaveClass(/dark/);

  await button.click();
  await expect(root).toHaveClass(/dark/);

  await button.click();
  await expect(root).not.toHaveClass(/dark/);
});
