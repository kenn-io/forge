import { expect, test, type Page } from "@playwright/test";

async function waitForIssueList(page: Page): Promise<void> {
  await page
    .locator(".issue-item")
    .first()
    .waitFor({ state: "visible", timeout: 10_000 });
}

async function selectRepo(page: Page, name: string): Promise<void> {
  const option = page.getByRole("option", { name });
  await expect(option).toBeVisible();
  await option.click();
  await expect(option.locator("input[type='checkbox']")).toBeChecked();
}

test("repository selector filters dashboard lists by multiple selected repos", async ({ page }) => {
  await page.goto("/issues");
  await waitForIssueList(page);

  const selector = page.getByTitle("Select repository");
  await selector.click();

  await selectRepo(page, "github.com/acme/widgets");
  await selectRepo(page, "github.com/acme/tools");

  await page.keyboard.press("Escape");

  await expect(selector.locator(".typeahead-value")).toHaveText("2 repos");
  await expect(page.locator(".repo-header__name")).toHaveText([
    "acme/widgets",
    "acme/tools",
  ]);

  await expect(page.getByText("Widget rendering broken on Safari")).toBeVisible();
  await expect(page.getByText("Add dark mode support")).toBeVisible();
  await expect(page.getByText("Support config file loading")).toBeVisible();
  await expect(page.getByText("GitLab read-only issue")).toHaveCount(0);

  await expect(
    page.evaluate(() => localStorage.getItem("middleman-filter-repo")),
  ).resolves.toBe("github.com/acme/widgets,github.com/acme/tools");
});
