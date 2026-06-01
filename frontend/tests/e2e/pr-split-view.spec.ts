import { expect, test, type Page, type Route } from "@playwright/test";

import { mockApi } from "./support/mockApi";

const capabilities = {
  read_repositories: true,
  read_merge_requests: true,
  read_issues: true,
  read_comments: true,
  read_releases: true,
  read_ci: true,
  read_labels: true,
  comment_mutation: true,
  thread_reply: false,
  thread_resolve: false,
  label_mutation: true,
  state_mutation: true,
  merge_mutation: true,
  review_mutation: true,
  workflow_approval: true,
  ready_for_review: true,
  issue_mutation: true,
  review_draft_mutation: false,
  review_thread_resolution: false,
  read_review_threads: false,
  native_multiline_ranges: false,
  supported_review_actions: [],
};

const diffResponse = {
  stale: false,
  whitespace_only_count: 0,
  files: [
    {
      path: "src/split-view.ts",
      old_path: "src/split-view.ts",
      status: "modified",
      is_binary: false,
      is_generated: false,
      is_whitespace_only: false,
      additions: 1,
      deletions: 1,
      patch: "@@ -1,1 +1,1 @@\n-old\n+new\n",
      hunks: [
        {
          old_start: 1,
          old_count: 1,
          new_start: 1,
          new_count: 1,
          lines: [
            { type: "removed", content: "old", old_num: 1 },
            { type: "added", content: "new", new_num: 1 },
          ],
        },
      ],
    },
  ],
};

async function fulfillJson(route: Route, body: unknown): Promise<void> {
  await route.fulfill({
    status: 200,
    contentType: "application/json",
    body: JSON.stringify(body),
  });
}

async function mockSplitViewPR(page: Page): Promise<void> {
  const detailPath = "**/api/v1/pulls/github/acme/widgets/42";

  await page.route(detailPath, async (route) => {
    if (route.request().method() !== "GET") {
      await route.fallback();
      return;
    }
    await fulfillJson(route, {
      detail_loaded: true,
      detail_fetched_at: "2026-03-30T14:00:00Z",
      diff_head_sha: "head",
      platform_host: "github.com",
      repo_owner: "acme",
      repo_name: "widgets",
      warnings: [],
      workflow_approval: {
        count: 0,
        required: false,
        runs: [],
      },
      workspace: undefined,
      worktree_links: [],
      repo: {
        provider: "github",
        platform_host: "github.com",
        owner: "acme",
        name: "widgets",
        repo_path: "acme/widgets",
        capabilities,
      },
      merge_request: {
        ID: 1,
        RepoID: 1,
        PlatformID: 101,
        PlatformExternalID: "PR_42",
        Number: 42,
        URL: "https://github.com/acme/widgets/pull/42",
        Title: "Add browser regression coverage",
        Author: "marius",
        AuthorDisplayName: "Marius",
        State: "open",
        IsDraft: false,
        IsLocked: false,
        Body: "Adds Playwright smoke tests for workspace panel.",
        HeadBranch: "feature/playwright",
        BaseBranch: "main",
        HeadRepoCloneURL: "https://github.com/acme/widgets.git",
        Additions: 120,
        Deletions: 12,
        CommentCount: 3,
        ReviewDecision: "APPROVED",
        CIStatus: "success",
        CIChecksJSON: "[]",
        CIHadPending: false,
        CreatedAt: "2026-03-29T14:00:00Z",
        UpdatedAt: "2026-03-30T14:00:00Z",
        LastActivityAt: "2026-03-30T14:00:00Z",
        MergedAt: null,
        ClosedAt: null,
        MergeableState: "clean",
        DetailFetchedAt: "2026-03-30T14:00:00Z",
        KanbanStatus: "reviewing",
        Starred: false,
        labels: [],
      },
      events: [],
    });
  });

  await page.route(`${detailPath}/files`, async (route) => {
    await fulfillJson(route, diffResponse);
  });
  await page.route(`${detailPath}/diff**`, async (route) => {
    await fulfillJson(route, diffResponse);
  });
}

test.beforeEach(async ({ page }) => {
  await mockApi(page);
  await mockSplitViewPR(page);
});

test("lets wide PR detail panes opt into split conversation and files", async ({ page }) => {
  await page.setViewportSize({ width: 2200, height: 1000 });
  await page.goto("/pulls/github/acme/widgets/42");

  await expect(page.locator(".detail-title")).toContainText(
    "Add browser regression coverage",
  );
  await expect(page.locator(".detail-split-layout")).toHaveCount(0);

  const splitToggle = page.getByRole("button", { name: "Split view" });
  await expect(splitToggle).toBeVisible();
  await expect(splitToggle).toHaveAttribute("aria-pressed", "false");

  await splitToggle.click();

  await expect(splitToggle).toHaveAttribute("aria-pressed", "true");
  await expect(page.locator(".detail-split-layout")).toBeVisible();
  await expect(page.locator(".detail-title")).toContainText(
    "Add browser regression coverage",
  );
  await expect(page.locator(".files-view")).toBeVisible();
  await expect(page.getByText("src/split-view.ts")).toBeVisible();
});
