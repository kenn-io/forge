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
  for (const [limit, countText] of [
    [400, "400+ PRs"],
    [600, "450 PRs"],
  ] as const) {
    await expect(async () => {
      await viewport.evaluate((node) => {
        node.scrollTop = node.scrollHeight;
      });
      await expect(more).toBeVisible();
    }).toPass();
    await more.click();
    await expect(count).toHaveText(countText);
    expect(limits.at(-1)).toBe(limit);
    await expect(page.locator(".sidebar-group-header__count")).toHaveText(limit === 400 ? "400+" : "450");
  }
  await expect(more).toHaveCount(0);
});

test("a browser-only filter can load older matches", async ({ page }) => {
  await mockApi(page);
  const template = mockPullFixture(42);
  const rows = Array.from({ length: 450 }, (_, index) => ({
    ...template,
    ID: index + 1,
    Number: index + 1,
    State: "closed",
    IsDraft: false,
  }));
  await page.route("**/api/v1/pulls**", async (route) => {
    const url = new URL(route.request().url());
    if (route.request().method() !== "GET" || url.pathname !== "/api/v1/pulls") return route.fallback();
    await route.fulfill({
      json:
        url.searchParams.get("state") === "open"
          ? []
          : rows.slice(0, Number(url.searchParams.get("limit")) || rows.length),
    });
  });
  await page.goto("/pulls");
  const trigger = page.locator(".compact-filter-menu .kit-filter-dropdown__btn");
  await trigger.click();
  const panel = page.locator(".kit-filter-dropdown__panel");
  await panel.getByRole("button", { name: "Closed", exact: true }).click();
  await trigger.click();
  await expect(page.locator(".list-count-chip")).toHaveText("200+ PRs");
  await trigger.click();
  await panel.getByRole("button", { name: "Draft", exact: true }).click();
  await trigger.click();
  await expect(page.getByText("No pull requests match these filters.")).toBeVisible();
  await expect(page.getByRole("button", { name: "Load more", exact: true })).toBeVisible();
});
