import { expect, test } from "@playwright/test";
import { mockIssueFixture } from "../../src/test/mockApiFetch";
import { mockApi } from "./support/mockApi";

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
  const limits: number[] = [];
  await page.route("**/api/v1/issues**", async (route) => {
    const url = new URL(route.request().url());
    if (route.request().method() !== "GET" || url.pathname !== "/api/v1/issues") {
      await route.fallback();
      return;
    }
    const limit = Number(url.searchParams.get("limit")) || rows.length;
    if (url.searchParams.get("state") === "closed") limits.push(limit);
    await route.fulfill({ json: url.searchParams.get("state") === "open" ? [] : rows.slice(0, limit) });
  });
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
