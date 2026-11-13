import { expect, test } from "@playwright/test";

import { mockPullFixture } from "../../src/test/mockApiFetch";
import { mockApi } from "./support/mockApi";

test.use({ viewport: { width: 1440, height: 900 }, deviceScaleFactor: 2 });

for (const [width, grouping] of [
  [340, "flat"],
  [600, "flat"],
  [340, "byRepo"],
] as const) {
  test(`table keeps titles and statuses visible at ${width}px, ${grouping}`, async ({ page }, testInfo) => {
    const errors: string[] = [];
    page.on("pageerror", (error) => errors.push(error.message));
    await mockApi(page);
    const pull = {
      ...mockPullFixture(42),
      Title: "Improve cache refresh",
      Author: "user-a",
      workspace: { id: "workspace-a", status: "ready" },
      ReviewDecision: "APPROVED",
      MergeableState: "dirty",
      stack: { position: 2, size: 3 },
      CIChecksJSON: JSON.stringify([
        { name: "build", status: "completed", conclusion: "success" },
        { name: "test", status: "completed", conclusion: "failure" },
        { name: "lint", status: "in_progress", conclusion: "" },
      ]),
    };
    await page.route("**/api/v1/pulls**", async (route) => {
      if (new URL(route.request().url()).pathname !== "/api/v1/pulls") return route.fallback();
      await route.fulfill({ json: [pull, { ...mockPullFixture(84), Author: "user-b" }] });
    });
    await page.addInitScript(
      ({ width, grouping }) => {
        localStorage.setItem("kenn-forge:pullListLayout", "table");
        localStorage.setItem("kenn-forge:groupingMode", grouping);
        localStorage.setItem("kenn-forge-sidebar-width", String(width));
      },
      { width, grouping },
    );
    await page.goto("/pulls");
    const row = page.locator(".pull-item--table").filter({ hasText: pull.Title });
    await expect(row).toBeVisible();
    await page.evaluate(() => document.fonts.ready);
    if (grouping === "flat") {
      await expect(row.locator(".repo-chip__label")).toHaveText("widgets");
      await expect(row.locator(".repo-chip__label")).toHaveAttribute("title", "github.com/acme/widgets");
    }
    await expect(row.locator(".review-indicator")).toBeVisible();
    await expect(row.locator(".stack-indicator")).toBeVisible();
    await expect(row.locator('[data-testid="ci-token-failed"]')).toBeVisible();
    await expect(row.locator(".conflict-icon")).toBeVisible();
    await expect
      .poll(async () => row.locator(".title-text").evaluate((el) => el.getBoundingClientRect().width))
      .toBeGreaterThanOrEqual(40);
    const titleCell = await row.locator(".cell--title").boundingBox();
    const statuses = await row.locator(".row-status").boundingBox();
    expect(statuses!.x + statuses!.width).toBeLessThanOrEqual(titleCell!.x + titleCell!.width + 1);
    const headers = page.locator(".table-headers");
    const itemHeader = await headers.getByText("Item", { exact: true }).boundingBox();
    expect(Math.abs(titleCell!.x - itemHeader!.x)).toBeLessThan(1);
    if (width === 340) {
      await expect(row.locator(".cell--author")).toBeHidden();
      await expect(row.locator(".cell--time")).toBeHidden();
    } else {
      await expect(row.locator(".cell--author")).toBeVisible();
      await expect(row.locator(".cell--time")).toBeVisible();
    }
    await row.click();
    await expect(row).toHaveClass(/selected/);
    await page.screenshot({ path: testInfo.outputPath("table.png"), clip: { x: 0, y: 0, width, height: 280 } });
    expect(errors).toEqual([]);
  });
}
