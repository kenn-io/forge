import { expect, test } from "@playwright/test";
import type { PRDiffSizePoint } from "../../src/lib/api/generated/models/pRDiffSizePoint";
import { createMockApiHandler } from "../../src/test/mockApiFetch";
import { worklistResponse, worklistRoutes, type WorklistServer } from "../../src/test/worklistFixtures";
import { mockApi } from "./support/mockApi";

const history: PRDiffSizePoint[] = [
  { head_sha: "first-head", base_sha: "base", additions: 80, deletions: 20, observed_at: "2026-10-27T09:00:00Z" },
  { head_sha: "second-head", base_sha: "base", additions: 280, deletions: 40, observed_at: "2026-10-27T10:00:00Z" },
  { head_sha: "third-head", base_sha: "base", additions: 430, deletions: 50, observed_at: "2026-10-27T11:00:00Z" },
  {
    head_sha: "42aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa42",
    base_sha: "base",
    additions: 120,
    deletions: 12,
    observed_at: "2026-10-27T12:00:00Z",
  },
];

async function setup(page: import("@playwright/test").Page, points = history) {
  await mockApi(page);
  const api = createMockApiHandler();
  await page.route("**/api/v1/**/pulls/**/42", async (route) => {
    const request = route.request();
    const response = api.handle({ method: request.method(), url: new URL(request.url()), bodyText: "" });
    const detail = await response.json();
    detail.merge_request = {
      ...detail.merge_request,
      Author: "user-a",
      platform_base_sha: "base",
      diff_size_history: points,
    };
    await route.fulfill({ json: detail });
  });
  const list = worklistResponse(5, "PR size prototype · synthetic data");
  list.items = list.items.map((item) => ({
    ...item,
    pull: item.pull
      ? {
          ...item.pull,
          Author: "user-a",
          platform_base_sha: "base",
          diff_size_history: item.number === 42 ? points : [],
        }
      : null,
  }));
  const server: WorklistServer = { lists: [list], requests: [] };
  const handle = worklistRoutes(server);
  await page.route("**/api/v1/worklists**", async (route) => {
    const request = route.request();
    const response = handle({
      method: request.method(),
      url: new URL(request.url()),
      bodyText: request.postData() ?? "",
    });
    if (!response) {
      await route.fallback();
      return;
    }
    await route.fulfill({ status: response.status, contentType: "application/json", body: await response.text() });
  });
}

test("shows total PR size by observed revision in the header and worklist", async ({ page }) => {
  await page.setViewportSize({ width: 1440, height: 900 });
  await setup(page);
  await page.goto("/worklists/5");
  const charts = page.getByRole("img", { name: /^PR size: 132 lines · \+32 since first observation/ });
  await expect(charts).toHaveCount(2);
  const headerChart = page.locator(".chips-row").getByRole("img", { name: /^PR size:/ });
  const diffStats = page.locator(".chips-row").getByText("+120", { exact: false }).first();
  const labelsButton = page.locator(".chips-row").getByRole("button", { name: "Labels" });
  const [stats, chartBox, labels] = await Promise.all([
    diffStats.boundingBox(),
    headerChart.boundingBox(),
    labelsButton.boundingBox(),
  ]);
  expect(chartBox!.x).toBeGreaterThan(stats!.x + stats!.width - 1);
  expect(chartBox!.x).toBeLessThan(labels!.x);
  for (const chart of await charts.all()) {
    await expect(chart.locator("circle")).toHaveCount(history.length);
    await chart.hover();
    await page.waitForTimeout(750);
    await expect(page.getByRole("tooltip")).toHaveCount(0);
  }
  await page.setViewportSize({ width: 900, height: 650 });
  const row = page.getByRole("button", { name: "Add browser regression coverage", exact: true });
  await expect(row).toBeInViewport();
  expect(await row.evaluate((element) => element.scrollWidth <= element.clientWidth)).toBe(true);
});

test("announces a single revision without drawing a trend", async ({ page }) => {
  await setup(page, history.slice(-1));
  await page.goto("/worklists/5");
  const chart = page.locator(".chips-row").getByRole("img", { name: /^PR size: 132 lines · 1 revision/ });
  await expect(chart).toBeVisible();
  await expect(chart.locator("circle")).toHaveCount(1);
  await expect(chart.locator("polyline")).toHaveCount(0);
});
