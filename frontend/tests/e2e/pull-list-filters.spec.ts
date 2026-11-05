import { expect, test, type Page } from "@playwright/test";

import { mockApi } from "./support/mockApi";

const repo = {
  provider: "github",
  platform_host: "github.com",
  repo_path: "acme/widgets",
  owner: "acme",
  name: "widgets",
  capabilities: {},
};

function pull(number: number, title: string, overrides: Record<string, unknown> = {}) {
  return {
    ID: number,
    RepoID: 1,
    GitHubID: 1000 + number,
    Number: number,
    URL: `https://github.com/acme/widgets/pull/${number}`,
    Title: title,
    Author: "marius",
    State: "open",
    IsDraft: false,
    Body: "",
    HeadBranch: `feature/${number}`,
    BaseBranch: "main",
    Additions: 10,
    Deletions: 1,
    CommentCount: 0,
    ReviewDecision: "",
    CIStatus: "success",
    CIChecksJSON: "[]",
    CreatedAt: "2026-03-29T14:00:00Z",
    UpdatedAt: "2026-03-30T14:00:00Z",
    LastActivityAt: "2026-03-30T14:00:00Z",
    MergedAt: null,
    ClosedAt: null,
    MergeableState: "clean",
    Starred: false,
    repo_owner: "acme",
    repo_name: "widgets",
    platform_host: "github.com",
    platform_head_sha: `${number}`.padStart(40, "a"),
    repo,
    worktree_links: [],
    ...overrides,
  };
}

async function mockPulls(page: Page) {
  await mockApi(page);
  await page.route("**/api/v1/pulls**", async (route) => {
    const url = new URL(route.request().url());
    if (route.request().method() !== "GET" || url.pathname !== "/api/v1/pulls") {
      await route.fallback();
      return;
    }
    await route.fulfill({
      status: 200,
      contentType: "application/json",
      body: JSON.stringify([
        pull(10, "Approved review queue", {
          labels: [{ name: "bug", color: "d73a4a", is_default: true }],
          ReviewDecision: "APPROVED",
          workspace: { id: "ws-10", status: "ready" },
        }),
        pull(11, "Draft parser cleanup", {
          labels: [
            { name: "bug", color: "d73a4a", is_default: true },
            { name: "docs", color: "0075ca", is_default: true },
          ],
          IsDraft: true,
          CIStatus: "pending",
          CIChecksJSON: JSON.stringify([{ name: "test", status: "in_progress", conclusion: "" }]),
        }),
        pull(12, "Ready failed workflow", {
          labels: [{ name: "docs", color: "0075ca", is_default: true }],
          CIStatus: "failure",
          workspace: { id: "ws-12", status: "ready" },
        }),
        pull(13, "Ready conflict resolver", {
          MergeableState: "dirty",
        }),
      ]),
    });
  });
}

async function openPrFilters(page: Page): Promise<void> {
  const standaloneTrigger = page.getByTitle("PR filters");
  if (await standaloneTrigger.isVisible()) {
    await standaloneTrigger.click();
  } else {
    await page.locator(".compact-filter-menu .kit-filter-dropdown__btn").click();
  }
  await expect(page.locator(".kit-filter-dropdown__panel")).toBeVisible();
}

test("PR filters match any option in a group and every selected group", async ({ page }) => {
  await mockPulls(page);
  await page.goto("/pulls");

  const rows = page.locator(".pr-list-row");
  await expect(rows).toHaveCount(4);

  await openPrFilters(page);
  const filterPanel = page.locator(".kit-filter-dropdown__panel");
  const workspaceGrouping = (
    await filterPanel.locator(".kit-filter-dropdown__section-title, .kit-filter-dropdown__item").allTextContents()
  )
    .map((text) => text.trim())
    .filter((text) => ["Has workspace", "View"].includes(text));
  expect(workspaceGrouping).toEqual(["Has workspace", "View"]);

  await filterPanel.getByRole("button", { name: "Has workspace" }).click();
  await expect(rows).toHaveText([/Approved review queue/, /Ready failed workflow/]);
  await filterPanel.getByRole("button", { name: "Has workspace" }).click();

  await filterPanel.getByRole("button", { name: "Passing CI" }).click();
  await filterPanel.getByRole("button", { name: "Failed CI" }).click();
  await expect(rows).toHaveText([/Approved review queue/, /Ready failed workflow/, /Ready conflict resolver/]);
  await filterPanel.getByRole("button", { name: "Failed CI" }).click();
  await filterPanel.getByRole("button", { name: "Ready for review" }).click();
  await expect(rows).toHaveText([/Approved review queue/, /Ready conflict resolver/]);
  await filterPanel.getByRole("button", { name: "Draft", exact: true }).click();
  await expect(rows).toHaveText([/Approved review queue/, /Ready conflict resolver/]);
  await filterPanel.getByRole("button", { name: /^(Clear filters|Reset view)$/ }).click();

  await filterPanel.getByRole("button", { name: "Ready for review" }).click();

  await expect(rows).toHaveText([/Approved review queue/, /Ready failed workflow/, /Ready conflict resolver/]);

  await filterPanel.getByRole("button", { name: "Failed CI" }).click();
  await expect(rows).toHaveText([/Ready failed workflow/]);

  await filterPanel.getByRole("button", { name: /^(Clear filters|Reset view)$/ }).click();
  await filterPanel.getByRole("button", { name: "Merge conflicts" }).click();

  await expect(rows).toHaveText([/Ready conflict resolver/]);

  await filterPanel.getByRole("button", { name: /^(Clear filters|Reset view)$/ }).click();
  await filterPanel.getByRole("button", { name: "Passing CI" }).click();

  await expect(rows).toHaveText([/Approved review queue/, /Ready conflict resolver/]);
});

test("the Labels flyout includes, excludes, and clears each label", async ({ page }) => {
  await mockPulls(page);
  await page.goto("/pulls");

  const rows = page.locator(".pr-list-row");
  await expect(rows).toHaveCount(4);

  await openPrFilters(page);
  const labelsRow = page.locator(".kit-filter-dropdown__panel").getByRole("button", { name: /^Labels/ });
  await expect(labelsRow).toContainText("Any");
  await labelsRow.click();

  const flyout = page.getByRole("dialog", { name: "Labels" });
  await expect(flyout).toBeVisible();
  await expect(flyout.getByRole("searchbox", { name: "Search labels" })).toBeFocused();
  const bug = flyout.getByRole("button", { name: /^bug/ });
  const docs = flyout.getByRole("button", { name: /^docs/ });

  await bug.click();
  await expect(bug).toHaveAccessibleName(/included/);
  await expect(rows).toHaveText([/Approved review queue/, /Draft parser cleanup/]);
  await expect(labelsRow).toContainText("1 included");

  await bug.click();
  await expect(bug).toHaveAccessibleName(/excluded/);
  await expect(rows).toHaveText([/Ready failed workflow/, /Ready conflict resolver/]);

  await docs.click();
  await expect(rows).toHaveText([/Ready failed workflow/]);
  await expect(labelsRow).toContainText("1 in, 1 out");

  await page.keyboard.press("Escape");
  await expect(flyout).toBeHidden();
  await page
    .locator(".kit-filter-dropdown__panel")
    .getByRole("button", { name: /^(Clear filters|Reset view)$/ })
    .click();
  await expect(rows).toHaveCount(4);
  await expect(labelsRow).toContainText("Any");
});
