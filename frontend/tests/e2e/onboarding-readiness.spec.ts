import { devices, expect, test } from "@playwright/test";

import { mockApi, mockSettings } from "./support/mockApi";

test.skip(({ browserName }) => browserName === "firefox", "Firefox does not support Playwright mobile emulation");
test.use({ ...devices["Pixel 5"], viewport: { width: 390, height: 844 }, colorScheme: "dark" });

test("missing gh lets restart instructions wrap with the recovery text on phones", async ({ page }, testInfo) => {
  await mockApi(page);
  await page.route("**/api/v1/settings", (route) => route.fulfill({ json: { ...mockSettings, repos: [] } }));
  await page.route("**/api/v1/tooling-status", (route) =>
    route.fulfill({
      json: {
        git: { available: true },
        gh: { available: false, authenticated: false, host: "github.com" },
        glab: { available: false, authenticated: false },
        github_token: { env: "KENN_FORGE_GITHUB_TOKEN", ready: false },
      },
    }),
  );

  await page.goto("/m");
  await expect(page.getByRole("heading", { name: "Connect a code forge" })).toBeVisible();
  const command = page.locator("code").filter({ hasText: /^kenn-forge daemon restart$/ });
  await expect(command).toBeVisible();
  await page.screenshot({ path: testInfo.outputPath("readiness-narrow.png"), fullPage: true });
  const layout = await command.evaluate((element) => {
    const bounds = element.parentElement!.getBoundingClientRect();
    const fragments = Array.from(element.getClientRects());
    return { lines: fragments.length, overflow: Math.max(...fragments.map((rect) => rect.right - bounds.right)) };
  });
  expect(layout.lines, "restart instructions should flow across lines with the surrounding text").toBeGreaterThan(1);
  expect(layout.overflow, "restart instructions must fit inside the provider card").toBeLessThanOrEqual(1);
});
