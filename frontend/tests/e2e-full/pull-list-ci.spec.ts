import { expect, test } from "@playwright/test";

import { startIsolatedE2EServer } from "./support/e2eServer";

test.describe("pull list CI cluster", () => {
  test("renders compact tokens from the live list payload (mixed state)", async ({ page }) => {
    const server = await startIsolatedE2EServer();
    try {
      const seed = await page.request.post(`${server.info.base_url}/__e2e/pr-ci-state/mixed`);
      expect(seed.ok()).toBe(true);

      await page.goto(`${server.info.base_url}/pulls`);

      const row = page.locator(".pull-item", { hasText: "#1" });
      await expect(row.locator("[data-testid='ci-token-failed']")).toHaveText(/1/);
      await expect(row.locator("[data-testid='ci-token-pending']")).toHaveText(/1/);
      await expect(row.locator("[data-testid='ci-token-passed']")).toHaveText(/2/);
      await expect(row.locator("[data-testid='ci-token-skipped']")).toHaveText(/1/);
    } finally {
      await server.stop();
    }
  });

  test("renders the unavailable token when CIChecksJSON is malformed", async ({ page }) => {
    const server = await startIsolatedE2EServer();
    try {
      const seed = await page.request.post(`${server.info.base_url}/__e2e/pr-ci-state/malformed`);
      expect(seed.ok()).toBe(true);

      await page.goto(`${server.info.base_url}/pulls`);

      const row = page.locator(".pull-item", { hasText: "#1" });
      await expect(row.locator("[data-testid='ci-token-unavailable']")).toBeVisible();

      // Verify the accessible-name algorithm picks up the "CI unavailable:"
      // diagnostic from the sr-only span inside the .pull-item button. Using
      // getByRole exercises the same path assistive tech uses, not raw DOM
      // scraping of the title attribute.
      const unavailableButton = page.getByRole("button", {
        name: /CI unavailable:/i,
      });
      await expect(unavailableButton).toBeVisible();
    } finally {
      await server.stop();
    }
  });

  test("renders no CI token cluster for rows without CI status", async ({ page }) => {
    const server = await startIsolatedE2EServer();
    try {
      await page.goto(`${server.info.base_url}/pulls`);

      // widgets#2 is seeded by SeedFixtures with no CIStatus and no
      // CIChecksJSON, so the sidebar row should render no ci-token-* element.
      const row = page.locator(".pull-item", { hasText: "#2" });
      await expect(row).toBeVisible();
      await expect(row.locator("[data-testid^='ci-token-']")).toHaveCount(0);
    } finally {
      await server.stop();
    }
  });
});
