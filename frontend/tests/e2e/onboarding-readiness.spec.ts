import { expect, test } from "@playwright/test";

import { mockApi, mockSettings } from "./support/mockApi";

test.use({ viewport: { width: 390, height: 844 } });

test("missing gh allows restart instructions to wrap within the provider card", async ({ page }) => {
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
  await expect(command).toHaveCSS("display", "inline");
  await expect(command).toHaveCSS("white-space", "normal");
  const overflow = await command.evaluate((element) => {
    const bounds = element.parentElement!.getBoundingClientRect();
    const fragments = Array.from(element.getClientRects());
    return Math.max(...fragments.map((rect) => rect.right - bounds.right));
  });
  expect(overflow, "restart instructions must fit inside the provider card").toBeLessThanOrEqual(1);
});
