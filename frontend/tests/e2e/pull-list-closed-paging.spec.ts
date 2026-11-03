import { expect, test } from "@playwright/test";
import { mockPullFixture } from "../../src/test/mockApiFetch";
import { mockApi } from "./support/mockApi";

test("Closed loads bounded pages and reaches the end of the history", async ({ page }) => {
  await mockApi(page);
  const template = mockPullFixture(42);
  const rows = Array.from({ length: 450 }, (_, index) => ({
    ...template,
    ID: index + 1,
    Number: index + 1,
    Title: `Closed change ${index + 1}`,
    State: "closed",
    IsDraft: index === 200 || index === 201,
  }));
  const limits: number[] = [];
  await page.route("**/api/v1/pulls**", async (route) => {
    const url = new URL(route.request().url());
    if (route.request().method() !== "GET" || url.pathname !== "/api/v1/pulls") {
      await route.fallback();
      return;
    }
    const limit = Number(url.searchParams.get("limit")) || rows.length;
    if (url.searchParams.get("state") === "closed") limits.push(limit);
    await route.fulfill({ json: url.searchParams.get("state") === "open" ? [] : rows.slice(0, limit) });
  });
  await page.goto("/pulls");
  await page.locator(".compact-filter-menu .kit-filter-dropdown__btn").click();
  await page.locator(".kit-filter-dropdown__panel").getByRole("button", { name: "Closed", exact: true }).click();
  await page.locator(".compact-filter-menu .kit-filter-dropdown__btn").click();
  const count = page.locator(".list-count-chip");
  await expect(count).toHaveText("200+ PRs");
  expect(limits.at(-1)).toBe(200);
  await expect(page.locator(".sidebar-group-header__count")).toHaveText("200+");

  const viewport = page.locator('[data-test="pr-list"] .kit-scrollbox__viewport');
  const more = page.getByRole("button", { name: "Load more", exact: true });
  const trigger = page.locator(".compact-filter-menu .kit-filter-dropdown__btn");
  const panel = page.locator(".kit-filter-dropdown__panel");
  await trigger.click();
  await panel.getByRole("button", { name: "Draft", exact: true }).click();
  await trigger.click();
  await expect(page.getByText("No pull requests match these filters.")).toBeVisible();
  await expect(more).toBeVisible();
  await more.click();
  await expect(page.getByText("Closed change 201", { exact: true })).toBeVisible();
  await expect(page.getByText("Closed change 202", { exact: true })).toBeVisible();
  expect(limits.at(-1)).toBe(400);
  await trigger.click();
  await panel.getByRole("button", { name: "Draft", exact: true }).click();
  await trigger.click();
  await expect(count).toHaveText("400+ PRs");
  await expect(page.locator(".sidebar-group-header__count")).toHaveText("400+");
  await expect(async () => {
    await viewport.evaluate((node) => {
      node.scrollTop = node.scrollHeight;
    });
    await expect(more).toBeVisible();
  }).toPass();
  await more.click();
  await expect(count).toHaveText("450 PRs");
  expect(limits.at(-1)).toBe(600);
  await expect(page.locator(".sidebar-group-header__count")).toHaveText("450");
  await expect(more).toHaveCount(0);
});
