import { expect, test } from "@playwright/test";
import { mockIssueFixture } from "../../src/test/mockApiFetch";
import { routeClosedPaging } from "./support/closedPaging";
import { mockApi, mockSettings } from "./support/mockApi";

test("Closed issues load bounded pages and reach the end of the history", async ({ page }) => {
  await mockApi(page);
  const template = mockIssueFixture(7);
  const repo = { ...template.repo, owner: "org-a", name: "project-a", repo_path: "org-a/project-a" };
  await page.route("**/api/v1/repos", (route) =>
    route.fulfill({
      json: [{ ID: 1, Owner: "org-a", Name: "project-a", Platform: "github", PlatformHost: "github.com" }],
    }),
  );
  const rows = Array.from({ length: 450 }, (_, index) => ({
    ...template,
    URL: `https://github.com/org-a/project-a/issues/${index + 1}`,
    Author: "user-a",
    repo_owner: "org-a",
    repo_name: "project-a",
    repo,
    ID: index + 1,
    Number: index + 1,
    Title: `Closed issue ${index + 1}`,
    State: "closed",
  }));
  await routeClosedPaging(page, "/api/v1/issues", rows);
  await page.goto("/issues");
  const trigger = page.locator(".compact-filter-menu .kit-filter-dropdown__btn");
  await trigger.click();
  await page.locator(".kit-filter-dropdown__panel").getByRole("button", { name: "Closed", exact: true }).click();
  await trigger.click();
  const count = page.locator(".list-count-chip");
  await expect(count).toHaveText("200+ issues");
  await expect(page.locator(".sidebar-group-header__count")).toHaveText("200+");

  const viewport = page.locator(".issue-list .kit-scrollbox__viewport");
  const scrollUntil = async (text: string) => {
    await expect(async () => {
      await viewport.evaluate((node) => {
        node.scrollTop = node.scrollHeight;
      });
      await expect(count).toHaveText(text);
    }).toPass();
  };
  await scrollUntil("400+ issues");
  const row300 = page.getByText("Closed issue 300", { exact: true });
  await row300.scrollIntoViewIfNeeded();
  await row300.click();
  await page.goBack();
  await expect(row300).toBeInViewport();
  await scrollUntil("450 issues");
  await expect(page.locator(".sidebar-group-header__count")).toHaveText("450");
  await page.getByRole("button", { name: "Select repository: Global" }).click();
  await page
    .getByRole("listbox", { name: "Repositories" })
    .getByRole("option", { name: "github/github.com/org-a/project-a" })
    .click();
  await expect.poll(() => viewport.evaluate((node) => node.scrollTop)).toBe(0);
});

test("Closed issues load past hidden bot rows", async ({ page }) => {
  await mockApi(page);
  await page.route("**/api/v1/settings", async (route) => {
    if (route.request().method() !== "GET") {
      await route.fallback();
      return;
    }
    await route.fulfill({ json: { ...mockSettings, issues: { ...mockSettings.issues, hide_bots: true } } });
  });
  const template = mockIssueFixture(7);
  const repo = { ...template.repo, owner: "org-a", name: "project-a", repo_path: "org-a/project-a" };
  const rows = Array.from({ length: 250 }, (_, index) => ({
    ...template,
    URL: `https://github.com/org-a/project-a/issues/${index + 1}`,
    repo_owner: "org-a",
    repo_name: "project-a",
    repo,
    ID: index + 1,
    Number: index + 1,
    Title: `Closed issue ${index + 1}`,
    State: "closed",
    Author: index < 200 ? "release-bot" : "user-a",
  }));
  await routeClosedPaging(page, "/api/v1/issues", rows);
  const { promise: nextPage, resolve: release } = Promise.withResolvers<void>();
  await page.route("**/api/v1/issues**", async (route) => {
    if (Number(new URL(route.request().url()).searchParams.get("limit")) > 200) await nextPage;
    await route.fallback();
  });
  await page.goto("/issues");
  const trigger = page.locator(".compact-filter-menu .kit-filter-dropdown__btn");
  await trigger.click();
  await page.locator(".kit-filter-dropdown__panel").getByRole("button", { name: "Closed", exact: true }).click();
  await trigger.click();

  await expect(page.locator(".list-count-chip")).toHaveText("0+ issues");
  await expect(page.getByText("No issues found.")).toHaveCount(0);
  release();
  await expect(page.locator(".list-count-chip")).toHaveText("50 issues");
  await expect(page.getByText("Closed issue 201", { exact: true })).toBeVisible();
});
