import { expect, test } from "@playwright/test";
import { mockPullFixture } from "../../src/test/mockApiFetch";
import { routeClosedPaging } from "./support/closedPaging";
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
  const limits = await routeClosedPaging(page, "/api/v1/pulls", rows, (row, params) => {
    const attributes = (params.get("attributes") ?? "").split(",");
    if (attributes.includes("draft")) return row.IsDraft;
    if (attributes.includes("not_draft")) return !row.IsDraft;
    return true;
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
  // Draft runs on the server, so the two drafts past the first page show up at once.
  const draft = panel.locator(".kit-filter-dropdown__item", { hasText: "Draft" });
  await trigger.click();
  await draft.click();
  await trigger.click();
  await expect(page.getByText("Closed change 201", { exact: true })).toBeVisible();
  await expect(page.getByText("Closed change 202", { exact: true })).toBeVisible();
  await expect(count).toHaveText("2 PRs");
  expect(limits.at(-1)).toBe(200);
  await trigger.click();
  // Draft cycles from only drafts through hiding drafts back to off.
  await draft.click();
  await expect(draft).toHaveAccessibleName(/, excluded$/);
  await expect(count).toHaveText("200+ PRs");
  await draft.click();
  await expect(draft).toHaveAttribute("aria-pressed", "false");
  await trigger.click();
  await expect(count).toHaveText("200+ PRs");
  await expect(async () => {
    await viewport.evaluate((node) => {
      node.scrollTop = node.scrollHeight;
    });
    await expect(more).toBeVisible();
  }).toPass();
  await more.click();
  await expect(count).toHaveText("400+ PRs");
  expect(limits.at(-1)).toBe(400);
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
