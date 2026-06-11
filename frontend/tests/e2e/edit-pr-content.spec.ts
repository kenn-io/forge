import { expect, test } from "@playwright/test";

import { mockApi } from "./support/mockApi";

const mockCapabilities = {
  read_repositories: true,
  read_merge_requests: true,
  read_issues: true,
  read_comments: true,
  read_releases: true,
  read_ci: true,
  read_labels: true,
  comment_mutation: true,
  state_mutation: true,
  merge_mutation: true,
  label_mutation: true,
  review_mutation: true,
  workflow_approval: true,
  ready_for_review: true,
  issue_mutation: true,
  review_draft_mutation: false,
  review_thread_resolution: false,
  read_review_threads: false,
  native_multiline_ranges: false,
  thread_reply: false,
  thread_resolve: false,
  supported_review_actions: [],
};

const mockRepo = {
  provider: "github",
  platform_host: "github.com",
  repo_path: "acme/widgets",
  owner: "acme",
  name: "widgets",
  capabilities: mockCapabilities,
};

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
  await page.route("**/api/v1/pulls/github/acme/widgets/42", async (route) => {
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
          repo: mockRepo,
          worktree_links: [],
        },
        repo_owner: "acme",
        repo_name: "widgets",
        platform_host: "github.com",
        repo: mockRepo,
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

  // Auto table layout gives the compact columns their content width, so
  // the commit hash renders on a single line without nowrap overrides.
  const commitLines = await commitCell.evaluate((el) => {
    const range = document.createRange();
    range.selectNodeContents(el);
    return Array.from(range.getClientRects()).filter((rect) => rect.width > 0).length;
  });
  expect(commitLines).toBe(1);

  // The long description column absorbs the remaining width; the compact
  // columns stay narrow instead of splitting the table evenly.
  const taskBox = await taskHeader.boundingBox();
  const tableBox = await page.locator(".markdown-body table").boundingBox();
  const bodyBox = await page.locator(".markdown-body").first().boundingBox();
  expect(taskBox).not.toBeNull();
  expect(tableBox).not.toBeNull();
  expect(bodyBox).not.toBeNull();
  expect(taskBox!.width).toBeLessThan(tableBox!.width * 0.2);
  expect(tableBox!.width).toBeLessThanOrEqual(bodyBox!.width + 1);

  await expect(page.locator(".markdown-body table")).toHaveCSS("border-collapse", "collapse");
});

test("markdown tables keep long unbroken values on one line and scroll", async ({ page }) => {
  const longToken = "deadbeef".repeat(30);
  await page.route("**/api/v1/pulls/github/acme/widgets/42", async (route) => {
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
          Body: ["| Name | Digest |", "| --- | --- |", `| image | ${longToken} |`].join("\n"),
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
          repo: mockRepo,
          worktree_links: [],
        },
        repo_owner: "acme",
        repo_name: "widgets",
        platform_host: "github.com",
        repo: mockRepo,
        detail_loaded: true,
        detail_fetched_at: "2026-03-30T14:00:00Z",
        worktree_links: [],
      }),
    });
  });

  await page.goto("/pulls/github/acme/widgets/42");

  // The PR body container sets word-break: break-word; cells must reset it
  // so an unbroken token keeps its intrinsic width instead of splitting.
  const digestCell = page.locator(".markdown-body td").filter({ hasText: longToken.slice(0, 24) });
  await expect(digestCell).toBeVisible();
  const digestLines = await digestCell.evaluate((el) => {
    const range = document.createRange();
    range.selectNodeContents(el);
    return Array.from(range.getClientRects()).filter((rect) => rect.width > 0).length;
  });
  expect(digestLines).toBe(1);

  // The too-wide table scrolls horizontally within itself like GitHub.
  const overflow = await page
    .locator(".markdown-body table")
    .evaluate((el) => ({ scrollWidth: el.scrollWidth, clientWidth: el.clientWidth }));
  expect(overflow.scrollWidth).toBeGreaterThan(overflow.clientWidth);
});

test("markdown tables with images share width evenly like GitHub", async ({ page }) => {
  // Serve a fixed-size image for every screenshot cell so auto table layout
  // has real intrinsic widths to distribute.
  await page.route("https://images.test/**", async (route) => {
    await route.fulfill({
      status: 200,
      contentType: "image/svg+xml",
      body: '<svg xmlns="http://www.w3.org/2000/svg" width="600" height="300"><rect width="600" height="300" fill="#7a7"/></svg>',
    });
  });

  await page.route("**/api/v1/pulls/github/acme/widgets/42", async (route) => {
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
            "| Default | Sort dropdown | Activity sort |",
            "| --- | --- | --- |",
            "| ![default](https://images.test/shot-1.svg) | ![dropdown](https://images.test/shot-2.svg) | ![activity](https://images.test/shot-3.svg) |",
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
          repo: mockRepo,
          worktree_links: [],
        },
        repo_owner: "acme",
        repo_name: "widgets",
        platform_host: "github.com",
        repo: mockRepo,
        detail_loaded: true,
        detail_fetched_at: "2026-03-30T14:00:00Z",
        worktree_links: [],
      }),
    });
  });

  await page.goto("/pulls/github/acme/widgets/42");

  const images = page.locator(".markdown-body table img");
  await expect(images).toHaveCount(3);

  const widths: number[] = [];
  for (let i = 0; i < 3; i++) {
    const box = await images.nth(i).boundingBox();
    expect(box).not.toBeNull();
    widths.push(box!.width);
  }

  // Every screenshot gets a real share of the row instead of the first
  // columns collapsing to slivers while the last column takes everything.
  for (const width of widths) {
    expect(width).toBeGreaterThan(100);
  }
  expect(Math.max(...widths) / Math.min(...widths)).toBeLessThan(1.25);

  // The table stays within the description container instead of overflowing.
  const tableBox = await page.locator(".markdown-body table").boundingBox();
  const bodyBox = await page.locator(".markdown-body").first().boundingBox();
  expect(tableBox).not.toBeNull();
  expect(bodyBox).not.toBeNull();
  expect(tableBox!.width).toBeLessThanOrEqual(bodyBox!.width + 1);
});

test("add description to empty-body PR shows add-description-btn", async ({ page }) => {
  // Override the GET route to return a PR with empty body.
  await page.route("**/api/v1/pulls/github/acme/widgets/42", async (route) => {
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
          platform_host: "github.com",
          repo: mockRepo,
          worktree_links: [],
        },
        repo_owner: "acme",
        repo_name: "widgets",
        platform_host: "github.com",
        repo: mockRepo,
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
