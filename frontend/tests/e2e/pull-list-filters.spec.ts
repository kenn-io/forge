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
    KanbanStatus: "new",
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
          ReviewDecision: "APPROVED",
          KanbanStatus: "reviewing",
        }),
        pull(11, "Draft parser cleanup", {
          IsDraft: true,
          KanbanStatus: "waiting",
        }),
        pull(12, "Ready failed workflow", {
          CIStatus: "failure",
          KanbanStatus: "awaiting_merge",
        }),
        pull(13, "Ready conflict resolver", {
          MergeableState: "dirty",
          KanbanStatus: "new",
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
    await page.locator(".compact-filter-menu .filter-btn").click();
  }
  await expect(page.locator(".filter-dropdown")).toBeVisible();
}

test("PR filters stack attributes and allow multiple kanban statuses", async ({ page }) => {
  await mockPulls(page);
  await page.goto("/pulls");

  const rows = page.locator(".pr-list-row");
  await expect(rows).toHaveCount(4);

  await openPrFilters(page);
  await page.locator(".filter-dropdown").getByRole("button", { name: "Ready for review" }).click();
  await page.locator(".filter-dropdown").getByRole("button", { name: "Reviewing" }).click();
  await page.locator(".filter-dropdown").getByRole("button", { name: "Awaiting merge" }).click();

  await expect(rows).toHaveText([/Approved review queue/, /Ready failed workflow/]);

  await page.locator(".filter-dropdown").getByRole("button", { name: "Failed CI" }).click();
  await expect(rows).toHaveText([/Ready failed workflow/]);

  await page
    .locator(".filter-dropdown")
    .getByRole("button", { name: /^(Clear filters|Reset view)$/ })
    .click();
  await page.locator(".filter-dropdown").getByRole("button", { name: "Merge conflicts" }).click();

  await expect(rows).toHaveText([/Ready conflict resolver/]);
});
