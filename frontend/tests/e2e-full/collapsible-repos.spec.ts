import { expect, test, type Page } from "@playwright/test";

// Seed data repos: acme/widgets, acme/tools, and a GitLab group/project issue.
// Open PRs (8): widgets#1, #2, #6, #7, tools#1, #10, #11, #12 -> widgets 4, tools 4
// Open issues (5): widgets#10, #11, #13, tools#5, group/project#11

async function waitForPullList(page: Page): Promise<void> {
  await page.locator(".pull-item").first()
    .waitFor({ state: "visible", timeout: 10_000 });
}

async function waitForIssueList(page: Page): Promise<void> {
  await page.locator(".issue-item").first()
    .waitFor({ state: "visible", timeout: 10_000 });
}

function widgetsHeader(page: Page) {
  return page.locator(".repo-header", { hasText: "acme/widgets" });
}

function toolsHeader(page: Page) {
  return page.locator(".repo-header", { hasText: "acme/tools" });
}

function newStatusHeader(page: Page) {
  return page.locator(".repo-header", { hasText: "New" });
}

async function selectPullGrouping(
  page: Page,
  label: string | RegExp,
): Promise<void> {
  const groupButton = page.locator(".group-btn", { hasText: label });
  if (await groupButton.isVisible()) {
    await groupButton.click();
    return;
  }

  await page.getByRole("button", { name: "Filters" }).click();
  await page.locator(".filter-dropdown .filter-item", { hasText: label })
    .last()
    .click();
}

test.describe("collapsible repo groups", () => {
  test.beforeEach(async ({ page }) => {
    // Clear collapse state before app bootstrap so each test starts expanded
    // without relying on WebKit reload stability in CI.
    await page.addInitScript(() => {
      localStorage.removeItem("middleman:collapsedRepos:pulls");
      localStorage.removeItem("middleman:collapsedRepos:issues");
    });
    await page.goto("/pulls");
    await waitForPullList(page);
  });

  test("PR list — default expanded shows every PR and both headers", async ({ page }) => {
    await expect(widgetsHeader(page)).toBeVisible();
    await expect(toolsHeader(page)).toBeVisible();
    await expect(widgetsHeader(page)).toHaveAttribute("aria-expanded", "true");
    await expect(toolsHeader(page)).toHaveAttribute("aria-expanded", "true");
    await expect(page.locator(".pull-item")).toHaveCount(8);
  });

  test("PR list — collapsing acme/widgets hides its items, keeps header and count", async ({ page }) => {
    await widgetsHeader(page).click();

    await expect(widgetsHeader(page)).toHaveAttribute("aria-expanded", "false");
    await expect(widgetsHeader(page)).toBeVisible();
    await expect(
      widgetsHeader(page).locator(".repo-header__count"),
    ).toHaveText("4");

    // Only acme/tools' PRs remain visible (tools#1 + stack #10/#11/#12).
    await expect(page.locator(".pull-item")).toHaveCount(4);
    // acme/tools stays expanded.
    await expect(toolsHeader(page)).toHaveAttribute("aria-expanded", "true");
  });

  test("PR list — expanding acme/widgets again restores its items", async ({ page }) => {
    await widgetsHeader(page).click();
    await expect(page.locator(".pull-item")).toHaveCount(4);

    await widgetsHeader(page).click();
    await expect(widgetsHeader(page)).toHaveAttribute("aria-expanded", "true");
    await expect(page.locator(".pull-item")).toHaveCount(8);
  });

  test("PR list — keyboard activation via Enter and Space toggles collapse", async ({ page }) => {
    // Focus the widgets header directly.
    await widgetsHeader(page).focus();
    await page.keyboard.press("Enter");
    await expect(widgetsHeader(page)).toHaveAttribute("aria-expanded", "false");
    await expect(page.locator(".pull-item")).toHaveCount(4);

    await widgetsHeader(page).focus();
    await page.keyboard.press("Space");
    await expect(widgetsHeader(page)).toHaveAttribute("aria-expanded", "true");
    await expect(page.locator(".pull-item")).toHaveCount(8);
  });

  test("PR list — status groups collapse like repo groups", async ({ page }) => {
    await selectPullGrouping(page, "Status");
    await expect(newStatusHeader(page)).toBeVisible();
    await expect(newStatusHeader(page)).toHaveAttribute("aria-expanded", "true");

    await newStatusHeader(page).click();

    await expect(newStatusHeader(page)).toHaveAttribute("aria-expanded", "false");
    await expect(newStatusHeader(page).locator(".repo-header__count")).toHaveText("8");
    await expect(page.locator(".pull-item")).toHaveCount(0);

    await newStatusHeader(page).click();
    await expect(newStatusHeader(page)).toHaveAttribute("aria-expanded", "true");
    await expect(page.locator(".pull-item")).toHaveCount(8);
  });

  test("PR list — collapse state persists across reload", async ({ page }) => {
    await widgetsHeader(page).click();
    await expect(widgetsHeader(page)).toHaveAttribute("aria-expanded", "false");

    const refreshedPage = await page.context().newPage();
    await refreshedPage.goto("/pulls");
    await waitForPullList(refreshedPage);

    await expect(widgetsHeader(refreshedPage)).toHaveAttribute("aria-expanded", "false");
    await expect(refreshedPage.locator(".pull-item")).toHaveCount(4);

    await refreshedPage.close();
  });

  test("collapse is independent across pulls and issues surfaces", async ({ page }) => {
    // Collapse acme/widgets on /pulls.
    await widgetsHeader(page).click();
    await expect(widgetsHeader(page)).toHaveAttribute("aria-expanded", "false");

    // Navigate to /issues — acme/widgets must still be expanded there.
    await page.goto("/issues");
    await waitForIssueList(page);

    await expect(widgetsHeader(page)).toHaveAttribute("aria-expanded", "true");
    // Seed data: widgets has 3 open issues; tools and GitLab add 1 each.
    await expect(page.locator(".issue-item")).toHaveCount(5);
  });

  test("issue list — collapse, expand, and persist acme/widgets", async ({ page }) => {
    await page.goto("/issues");
    await waitForIssueList(page);

    // Default: 5 issues total (3 widgets + 1 tools + 1 GitLab).
    await expect(page.locator(".issue-item")).toHaveCount(5);

    // Collapse widgets: 2 issues remain (tools#5 and GitLab group/project#11).
    await widgetsHeader(page).click();
    await expect(widgetsHeader(page)).toHaveAttribute("aria-expanded", "false");
    await expect(
      widgetsHeader(page).locator(".repo-header__count"),
    ).toHaveText("3");
    await expect(page.locator(".issue-item")).toHaveCount(2);

    // Expand again: back to 5.
    await widgetsHeader(page).click();
    await expect(widgetsHeader(page)).toHaveAttribute("aria-expanded", "true");
    await expect(page.locator(".issue-item")).toHaveCount(5);

    // Collapse again and verify persisted state in a fresh page.
    await widgetsHeader(page).click();
    const refreshedPage = await page.context().newPage();
    await refreshedPage.goto("/issues");
    await waitForIssueList(refreshedPage);
    await expect(widgetsHeader(refreshedPage)).toHaveAttribute("aria-expanded", "false");
    await expect(refreshedPage.locator(".issue-item")).toHaveCount(2);

    await refreshedPage.close();
  });
});
