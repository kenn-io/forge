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
      Additions: points.at(-1)?.additions ?? detail.merge_request.Additions,
      Deletions: points.at(-1)?.deletions ?? detail.merge_request.Deletions,
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
          Additions: item.number === 42 ? (points.at(-1)?.additions ?? item.pull.Additions) : item.pull.Additions,
          Deletions: item.number === 42 ? (points.at(-1)?.deletions ?? item.pull.Deletions) : item.pull.Deletions,
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

test("embeds only the zero-anchored trace in existing stats without a popover", async ({ page }) => {
  await page.setViewportSize({ width: 1440, height: 900 });
  await setup(page);
  await page.goto("/worklists/5");
  const charts = page.getByRole("img", { name: /^PR diff size: 100 to 132 lines/ });
  await expect(charts).toHaveCount(2);
  const header = page.locator(".chips-row svg.pr-size-sparkline");
  await expect(page.locator(".diff-summary-stats")).toContainText("+120");
  await expect(header).toHaveClass(/neutral/);
  await expect(header.locator(".zero")).toHaveAttribute("d", "M2 18H50");
  await expect(header).toHaveText("");
  await header.hover();
  await expect(page.getByRole("tooltip")).toHaveCount(0);
  await expect(header.locator("title")).toHaveCount(0);
  const row = page.getByRole("button", { name: "Add browser regression coverage", exact: true });
  await row.locator("svg.pr-size-sparkline").hover();
  await expect(page.getByRole("tooltip")).toHaveCount(0);
  await expect(row).toHaveAccessibleDescription(/PR diff size: 100 to 132/);
  await page.setViewportSize({ width: 900, height: 650 });
  await expect(row).toBeInViewport();
  expect(await row.evaluate((element) => element.scrollWidth <= element.clientWidth)).toBe(true);
});

test("has unobtrusive empty and single-revision states", async ({ page }) => {
  await setup(page, []);
  await page.goto("/worklists/5");
  await expect(page.locator(".chips-row svg.pr-size-sparkline")).toHaveCount(0);
  await expect(page.locator(".chips-row").getByRole("img", { name: "PR diff size history unavailable" })).toHaveCount(
    1,
  );
  await setup(page, [{ ...history.at(-1)!, additions: 2509, deletions: 0 }]);
  await page.reload();
  const charts = page.getByRole("img", { name: "PR diff size: 2509 lines, 1 observed revision" });
  await expect(charts).toHaveCount(2);
  for (const chart of await charts.all()) {
    await expect(chart).toBeVisible();
    await expect(chart.locator("circle")).toHaveCount(1);
    await expect(chart.locator("polyline")).toHaveCount(0);
    await expect(chart).not.toHaveAttribute("tabindex");
    await chart.hover();
    await expect(page.getByRole("tooltip")).toHaveCount(0);
  }
  const row = page.getByRole("button", { name: "Add browser regression coverage", exact: true });
  await row.focus();
  await expect(row).toBeFocused();
  await expect(page.getByRole("tooltip")).toHaveCount(0);
  await page.locator(".diff-summary-trigger").focus();
  await expect(page.getByRole("tooltip")).toBeVisible();
  await expect(page.getByRole("tooltip")).not.toContainText("observed head/base revisions");
  await expect(page.locator(".size-tooltip")).toHaveCount(0);
});

for (const [total, tone] of [
  [50, "success"],
  [51, "neutral"],
  [149, "neutral"],
  [150, "warning"],
] as const) {
  test(`uses ${tone} at ${total} lines in both placements`, async ({ page }) => {
    const last = history.at(-1)!;
    await setup(page, [history[0]!, { ...last, additions: total, deletions: 0 }]);
    await page.goto("/worklists/5");
    await expect(page.locator(`svg.pr-size-sparkline.${tone}`)).toHaveCount(2);
  });
}
