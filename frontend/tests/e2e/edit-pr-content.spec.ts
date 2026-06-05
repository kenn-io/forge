import { expect, test } from "@playwright/test";

import { mockApi } from "./support/mockApi";

test.beforeEach(async ({ page }) => {
  await mockApi(page);
});

test("edit title: click Edit, type, save", async ({ page }) => {
  await page.goto("/pulls/github/acme/widgets/42");
  await expect(page.locator(".detail-title")).toContainText("Add browser regression coverage");
  await page.locator(".edit-title-btn").click();

  const input = page.locator(".title-edit-input");
  await expect(input).toBeVisible();
  await input.fill("Updated title text");
  await page.locator(".title-edit-save").click();

  await expect(page.locator(".detail-title")).toContainText("Updated title text");
});

test("edit title: cancel with Escape", async ({ page }) => {
  await page.goto("/pulls/github/acme/widgets/42");
  await page.locator(".edit-title-btn").click();

  const input = page.locator(".title-edit-input");
  await input.fill("should not persist");
  await input.press("Escape");

  await expect(page.locator(".detail-title")).toContainText("Add browser regression coverage");
});

test("edit title: save disabled when blank", async ({ page }) => {
  await page.goto("/pulls/github/acme/widgets/42");
  await page.locator(".edit-title-btn").click();

  const input = page.locator(".title-edit-input");
  await input.fill("");
  await expect(page.locator(".title-edit-save")).toBeDisabled();
});

test("edit body: click Edit, type, save", async ({ page }) => {
  await page.goto("/pulls/github/acme/widgets/42");
  await page.locator(".edit-body-btn").click();

  const textarea = page.locator(".body-edit-textarea");
  await expect(textarea).toBeVisible();
  await textarea.fill("New description content");
  await page.locator(".body-edit .title-edit-save").click();

  await expect(page.locator(".markdown-body")).toContainText("New description content");
});

test("edit body: cancel preserves original", async ({ page }) => {
  await page.goto("/pulls/github/acme/widgets/42");
  await page.locator(".edit-body-btn").click();

  await page.locator(".body-edit-textarea").fill("discarded");
  await page.locator(".body-edit .title-edit-cancel").click();

  await expect(page.locator(".markdown-body")).toContainText("Adds Playwright smoke tests");
});

test("markdown tables keep compact columns readable", async ({ page }) => {
  await page.route("**/api/v1/repos/acme/widgets/pulls/42", async (route) => {
    if (route.request().method() !== "GET") {
      await route.fallback();
      return;
    }
    await route.fulfill({
      status: 200,
      contentType: "application/json",
      body: JSON.stringify({
        merge_request: {
          ID: 1,
          RepoID: 1,
          GitHubID: 101,
          Number: 42,
          URL: "https://github.com/acme/widgets/pull/42",
          Title: "Add browser regression coverage",
          Author: "marius",
          State: "open",
          IsDraft: false,
          Body: [
            "| Task | Commit | Description |",
            "| --- | --- | --- |",
            "| 1 | b2af4711 | Add the generated client shape without flattening the response. |",
          ].join("\n"),
          HeadBranch: "feature/playwright",
          BaseBranch: "main",
          Additions: 120,
          Deletions: 12,
          CommentCount: 3,
          ReviewDecision: "APPROVED",
          CIStatus: "success",
          CIChecksJSON: "[]",
          CreatedAt: "2026-03-29T14:00:00Z",
          UpdatedAt: "2026-03-30T14:00:00Z",
          LastActivityAt: "2026-03-30T14:00:00Z",
          MergedAt: null,
          ClosedAt: null,
          KanbanStatus: "reviewing",
          Starred: false,
          repo_owner: "acme",
          repo_name: "widgets",
          platform_host: "github.com",
          worktree_links: [],
        },
        repo_owner: "acme",
        repo_name: "widgets",
        detail_loaded: true,
        detail_fetched_at: "2026-03-30T14:00:00Z",
        worktree_links: [],
      }),
    });
  });

  await page.goto("/pulls/github/acme/widgets/42");

  const taskHeader = page.locator(".markdown-body th").filter({ hasText: "Task" });
  const commitCell = page.locator(".markdown-body td").filter({ hasText: "b2af4711" });
  await expect(taskHeader).toBeVisible();
  await expect(commitCell).toBeVisible();
  await expect(taskHeader).toHaveCSS("white-space", "nowrap");
  await expect(commitCell).toHaveCSS("white-space", "nowrap");
  await expect(page.locator(".markdown-body table")).toHaveCSS("border-collapse", "collapse");
});

test("add description to empty-body PR shows add-description-btn", async ({ page }) => {
  // Override the GET route to return a PR with empty body.
  await page.route("**/api/v1/repos/acme/widgets/pulls/42", async (route) => {
    if (route.request().method() !== "GET") {
      await route.fallback();
      return;
    }
    await route.fulfill({
      status: 200,
      contentType: "application/json",
      body: JSON.stringify({
        merge_request: {
          ID: 1,
          RepoID: 1,
          GitHubID: 101,
          Number: 42,
          URL: "https://github.com/acme/widgets/pull/42",
          Title: "Add browser regression coverage",
          Author: "marius",
          State: "open",
          IsDraft: false,
          Body: "",
          HeadBranch: "feature/playwright",
          BaseBranch: "main",
          Additions: 120,
          Deletions: 12,
          CommentCount: 3,
          ReviewDecision: "APPROVED",
          CIStatus: "success",
          CIChecksJSON: "[]",
          CreatedAt: "2026-03-29T14:00:00Z",
          UpdatedAt: "2026-03-30T14:00:00Z",
          LastActivityAt: "2026-03-30T14:00:00Z",
          MergedAt: null,
          ClosedAt: null,
          KanbanStatus: "reviewing",
          Starred: false,
          repo_owner: "acme",
          repo_name: "widgets",
          worktree_links: [],
        },
        repo_owner: "acme",
        repo_name: "widgets",
        detail_loaded: true,
        detail_fetched_at: "2026-03-30T14:00:00Z",
        worktree_links: [],
      }),
    });
  });

  await page.goto("/pulls/github/acme/widgets/42");

  const addBtn = page.locator(".add-description-btn");
  await expect(addBtn).toBeVisible();
  await addBtn.click();

  const textarea = page.locator(".body-edit-textarea");
  await expect(textarea).toBeVisible();
  await textarea.fill("Added a new description");
  await page.locator(".body-edit .title-edit-save").click();

  await expect(page.locator(".markdown-body")).toContainText("Added a new description");
});
