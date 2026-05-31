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

test("repository selector cascades an owner group to all its repos", async ({ page }) => {
  await page.goto("/issues");
  await waitForIssueList(page);

  const selector = page.getByTitle("Select repository");
  await selector.click();

  // The owner row's checkbox cascades selection to every repo under that
  // owner. The row body would only toggle expand/collapse, so the checkbox
  // is the deliberate target. Selection is wired to mousedown (see
  // RepoTreeNode.checkboxMouseDown), so dispatch that event directly rather
  // than a click, mirroring the component test's fireEvent.mouseDown.
  const ownerCheckbox = page
    .getByRole("option", { name: "github.com/acme", exact: true })
    .locator("input[type='checkbox']");
  await expect(ownerCheckbox).toBeVisible();
  await ownerCheckbox.dispatchEvent("mousedown");
  await expect(ownerCheckbox).toBeChecked();

  await page.keyboard.press("Escape");

  const stored = await page.evaluate(() =>
    localStorage.getItem("middleman-filter-repo"),
  );
  expect(stored).toContain("github.com/acme/widgets");
  expect(stored).toContain("github.com/acme/tools");
  expect(stored).toContain("github.com/acme/archived");

  // The group selection keeps acme's issues visible and excludes repos
  // outside the group, such as the GitLab read-only fixture.
  await expect(page.getByText("Widget rendering broken on Safari")).toBeVisible();
  await expect(page.getByText("Support config file loading")).toBeVisible();
  await expect(page.getByText("GitLab read-only issue")).toHaveCount(0);
});
