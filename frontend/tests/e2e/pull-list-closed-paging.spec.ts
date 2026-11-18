import { expect, test } from "@playwright/test";
import { mockPullFixture } from "../../src/test/mockApiFetch";
import { routeClosedPaging } from "./support/closedPaging";
import { mockApi } from "./support/mockApi";

test("Closed loads bounded pages and reaches the end of the history", async ({ page }) => {
  await mockApi(page);
  const template = mockPullFixture(42);
  const repo = { ...template.repo, owner: "org-a", name: "project-a", repo_path: "org-a/project-a" };
  await page.route("**/api/v1/repos", (route) =>
    route.fulfill({ json: [{ ID: 1, Owner: "org-a", Name: "project-a", Platform: "github", PlatformHost: "github.com" }] }),
  );
  const rows = Array.from({ length: 450 }, (_, index) => ({
    ...template,
    URL: `https://github.com/org-a/project-a/pull/${index + 1}`,
    Author: "user-a",
    repo_owner: "org-a",
    repo_name: "project-a",
    repo,
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
  await expect(draft).toHaveAccessibleName(/, left out of results$/);
  await expect(count).toHaveText("200+ PRs");
  await draft.click();
  await expect(draft).toHaveAttribute("aria-pressed", "false");
  await trigger.click();
  await expect(count).toHaveText("200+ PRs");
  const scrollUntil = async (text: string) => {
    await expect(async () => {
      await viewport.evaluate((node) => {
        node.scrollTop = node.scrollHeight;
      });
      await expect(count).toHaveText(text);
    }).toPass();
  };
  await scrollUntil("400+ PRs");
  expect(limits.at(-1)).toBe(400);
  await expect(count).toHaveText("400+ PRs");
  const row300 = page.getByText("Closed change 300", { exact: true });
  await row300.scrollIntoViewIfNeeded();
  await row300.click();
  await page.goBack();
  await expect(row300).toBeInViewport();
  await trigger.click();
  await draft.click();
  await trigger.click();
  await expect.poll(() => viewport.evaluate((node) => node.scrollTop)).toBe(0);
  await trigger.click();
  await draft.click();
  await draft.click();
  await trigger.click();
  await scrollUntil("400+ PRs");
  await scrollUntil("450 PRs");
  expect(limits.at(-1)).toBe(600);
  await expect(page.locator(".sidebar-group-header__count")).toHaveText("450");
  await page.getByRole("button", { name: "Select repository: Global" }).click();
  await page
    .getByRole("listbox", { name: "Repositories" })
    .getByRole("option", { name: "github/github.com/org-a/project-a" })
    .click();
  await expect.poll(() => viewport.evaluate((node) => node.scrollTop)).toBe(0);
});
