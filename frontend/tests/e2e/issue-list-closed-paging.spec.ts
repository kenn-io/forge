import { expect, test, type Page } from "@playwright/test";
import { mockIssueFixture } from "../../src/test/mockApiFetch";
import { mockApi, mockSettings } from "./support/mockApi";

// Serves the first `limit` rows for Closed and All and an empty Open tab.
// Returns the limit of each Closed request.
async function routeIssues(page: Page, rows: ReturnType<typeof mockIssueFixture>[]): Promise<number[]> {
  const closedLimits: number[] = [];
  await page.route("**/api/v1/issues**", async (route) => {
    const url = new URL(route.request().url());
    if (route.request().method() !== "GET" || url.pathname !== "/api/v1/issues") {
      await route.fallback();
      return;
    }
    const limit = Number(url.searchParams.get("limit")) || rows.length;
    const state = url.searchParams.get("state");
    if (state === "closed") closedLimits.push(limit);
    await route.fulfill({ json: state === "open" ? [] : rows.slice(0, limit) });
  });
  return closedLimits;
}

test("Closed issues load bounded pages and reach the end of the history", async ({ page }) => {
  await mockApi(page);
  const template = mockIssueFixture(7);
  const rows = Array.from({ length: 450 }, (_, index) => ({
    ...template,
    ID: index + 1,
    Number: index + 1,
    Title: `Closed issue ${index + 1}`,
    State: "closed",
  }));
  const limits = await routeIssues(page, rows);
  await page.goto("/issues");
  const trigger = page.locator(".compact-filter-menu .kit-filter-dropdown__btn");
  await trigger.click();
  await page.locator(".kit-filter-dropdown__panel").getByRole("button", { name: "Closed", exact: true }).click();
  await trigger.click();
  const count = page.locator(".list-count-chip");
  await expect(count).toHaveText("200+ issues");
  expect(limits.at(-1)).toBe(200);
  await expect(page.locator(".sidebar-group-header__count")).toHaveText("200+");

  const viewport = page.locator(".issue-list .kit-scrollbox__viewport");
  const more = page.getByRole("button", { name: "Load more", exact: true });
  const scrollToMore = async () => {
    await expect(async () => {
      await viewport.evaluate((node) => {
        node.scrollTop = node.scrollHeight;
      });
      await expect(more).toBeVisible();
    }).toPass();
  };
  await scrollToMore();
  await more.click();
  await expect(count).toHaveText("400+ issues");
  expect(limits.at(-1)).toBe(400);
  await expect(page.locator(".sidebar-group-header__count")).toHaveText("400+");
  await scrollToMore();
  await more.click();
  await expect(count).toHaveText("450 issues");
  expect(limits.at(-1)).toBe(600);
  await expect(page.locator(".sidebar-group-header__count")).toHaveText("450");
  await expect(more).toHaveCount(0);
});

test("Closed issues offer Load more, not an empty state, while Hide bots hides every loaded row", async ({ page }) => {
  await mockApi(page);
  await page.route("**/api/v1/settings", async (route) => {
    if (route.request().method() !== "GET") {
      await route.fallback();
      return;
    }
    await route.fulfill({ json: { ...mockSettings, issues: { ...mockSettings.issues, hide_bots: true } } });
  });
  const template = mockIssueFixture(7);
  const rows = Array.from({ length: 250 }, (_, index) => ({
    ...template,
    ID: index + 1,
    Number: index + 1,
    Title: `Closed issue ${index + 1}`,
    State: "closed",
    Author: index < 200 ? "release-bot" : "human-author",
  }));
  await routeIssues(page, rows);
  await page.goto("/issues");
  const trigger = page.locator(".compact-filter-menu .kit-filter-dropdown__btn");
  await trigger.click();
  await page.locator(".kit-filter-dropdown__panel").getByRole("button", { name: "Closed", exact: true }).click();
  await trigger.click();
  await expect(page.locator(".list-count-chip")).toHaveText("0+ issues");
  await expect(page.getByText("No issues found.")).toHaveCount(0);

  await page.getByRole("button", { name: "Load more", exact: true }).click();
  await expect(page.locator(".list-count-chip")).toHaveText("50 issues");
  await expect(page.getByText("Closed issue 201", { exact: true })).toBeVisible();
});
