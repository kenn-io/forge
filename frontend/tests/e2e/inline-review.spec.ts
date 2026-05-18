import { expect, test, type Page, type Route } from "@playwright/test";

import { mockApi } from "./support/mockApi";

async function fulfillJson(route: Route, body: unknown, status = 200): Promise<void> {
  await route.fulfill({
    status,
    contentType: "application/json",
    body: JSON.stringify(body),
  });
}

const baseCapabilities = {
  read_repositories: true,
  read_merge_requests: true,
  read_issues: true,
  read_comments: true,
  read_releases: true,
  read_ci: true,
  comment_mutation: true,
  state_mutation: true,
  merge_mutation: true,
  review_mutation: true,
  workflow_approval: true,
  ready_for_review: true,
  issue_mutation: true,
  review_draft_mutation: true,
  review_thread_resolution: true,
  read_review_threads: true,
  native_multiline_ranges: true,
  supported_review_actions: ["comment", "approve", "request_changes"],
};

function pullDetail(
  reviewThreadResolved = false,
  capabilities = baseCapabilities,
  provider = "github",
  platformHost = "github.com",
) {
  return {
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
      Body: "Adds Playwright smoke tests.",
      HeadBranch: "feature/playwright",
      BaseBranch: "main",
      Additions: 2,
      Deletions: 0,
      CommentCount: 1,
      ReviewDecision: "",
      CIStatus: "success",
      CIChecksJSON: "[]",
      CreatedAt: "2026-03-29T14:00:00Z",
      UpdatedAt: "2026-03-30T14:00:00Z",
      LastActivityAt: "2026-03-30T14:00:00Z",
      MergedAt: null,
      ClosedAt: null,
      KanbanStatus: "reviewing",
      Starred: false,
    },
    events: [
      {
        ID: 51,
        MergeRequestID: 1,
        PlatformID: null,
        PlatformExternalID: "thread-1",
        EventType: "review_comment",
        Author: "ada",
        Summary: "",
        Body: "Existing inline comment",
        MetadataJSON: "",
        CreatedAt: "2026-03-30T14:00:00Z",
        DedupeKey: "review-thread-1",
        ThreadID: null,
        Resolvable: false,
        Resolved: reviewThreadResolved,
        diff_thread: {
          id: "1",
          path: "src/main.ts",
          side: "right",
          line: 2,
          new_line: 2,
          line_type: "add",
          diff_head_sha: "diff-head",
          body: "Existing inline comment",
          author_login: "ada",
          resolved: reviewThreadResolved,
          can_resolve: true,
          created_at: "2026-03-30T14:00:00Z",
          updated_at: "2026-03-30T14:00:00Z",
        },
      },
    ],
    repo: {
      provider,
      platform_host: platformHost,
      repo_path: "acme/widgets",
      owner: "acme",
      name: "widgets",
      capabilities,
    },
    repo_owner: "acme",
    repo_name: "widgets",
    platform_host: platformHost,
    platform_head_sha: "diff-head",
    platform_base_sha: "base",
    diff_head_sha: "diff-head",
    merge_base_sha: "merge-base",
    workflow_approval: { checked: true, required: false, count: 0 },
    warnings: [],
    detail_loaded: true,
    detail_fetched_at: "2026-03-30T14:00:00Z",
    worktree_links: [],
  };
}

function pullListItem(
  capabilities = baseCapabilities,
  provider = "github",
  platformHost = "github.com",
) {
  return {
    ID: 1,
    RepoID: 1,
    GitHubID: 101,
    Number: 42,
    URL: "https://github.com/acme/widgets/pull/42",
    Title: "Add browser regression coverage",
    Author: "marius",
    State: "open",
    IsDraft: false,
    Body: "Adds Playwright smoke tests.",
    HeadBranch: "feature/playwright",
    BaseBranch: "main",
    Additions: 2,
    Deletions: 0,
    CommentCount: 1,
    ReviewDecision: "",
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
    platform_host: platformHost,
    worktree_links: [],
    detail_loaded: true,
    repo: {
      provider,
      platform_host: platformHost,
      repo_path: "acme/widgets",
      owner: "acme",
      name: "widgets",
      capabilities,
    },
  };
}

const diffResponse = {
  stale: false,
  whitespace_only_count: 0,
  files: [
    {
      path: "src/main.ts",
      old_path: "src/main.ts",
      status: "modified",
      additions: 2,
      deletions: 0,
      is_binary: false,
      hunks: [
        {
          old_start: 1,
          old_count: 1,
          new_start: 1,
          new_count: 2,
          section: "",
          lines: [
            { type: "context", old_num: 1, new_num: 1, content: "const a = 1;" },
            { type: "add", old_num: null, new_num: 2, content: "const b = 2;" },
          ],
        },
      ],
    },
  ],
};

const multiHunkDiffResponse = {
  stale: false,
  whitespace_only_count: 0,
  files: [
    {
      path: "src/main.ts",
      old_path: "src/main.ts",
      status: "modified",
      additions: 2,
      deletions: 0,
      is_binary: false,
      hunks: [
        {
          old_start: 1,
          old_count: 1,
          new_start: 1,
          new_count: 1,
          section: "",
          lines: [
            { type: "add", old_num: null, new_num: 1, content: "const first = 1;" },
          ],
        },
        {
          old_start: 20,
          old_count: 1,
          new_start: 20,
          new_count: 1,
          section: "",
          lines: [
            { type: "add", old_num: null, new_num: 20, content: "const second = 2;" },
          ],
        },
      ],
    },
  ],
};

type MockInlineReviewOptions = {
  publishStatus?: "published" | "partially_published";
  remainingDraftComments?: Array<Record<string, unknown>>;
};

async function mockInlineReviewAPI(
  page: Page,
  capabilities = baseCapabilities,
  provider = "github",
  platformHost = "github.com",
  filesResponse: typeof diffResponse = diffResponse,
  onCreateDraft?: (body: { body: string; range: Record<string, unknown> }) => void,
  options: MockInlineReviewOptions = {},
): Promise<void> {
  let draftComments: Array<Record<string, unknown>> = [];
  let reviewThreadResolved = false;
  const path = `/api/v1/pulls/${provider}/acme/widgets/42`;

  await page.route("**/api/v1/pulls", async (route) => {
    await fulfillJson(route, [pullListItem(capabilities, provider, platformHost)]);
  });
  await page.route(`**${path}`, async (route) => {
    if (route.request().method() !== "GET") {
      await route.fallback();
      return;
    }
    await fulfillJson(
      route,
      pullDetail(reviewThreadResolved, capabilities, provider, platformHost),
    );
  });
  await page.route(`**${path}/files`, async (route) => {
    await fulfillJson(route, filesResponse);
  });
  await page.route(`**${path}/diff`, async (route) => {
    await fulfillJson(route, filesResponse);
  });
  await page.route(`**${path}/review-draft`, async (route) => {
    if (route.request().method() === "DELETE") {
      draftComments = [];
      await fulfillJson(route, { status: "ok" });
      return;
    }
    await fulfillJson(route, {
      draft_id: draftComments.length > 0 ? "1" : undefined,
      comments: draftComments,
      supported_actions: capabilities.supported_review_actions,
      native_multiline_ranges: capabilities.native_multiline_ranges,
    });
  });
  await page.route(`**${path}/review-draft/comments`, async (route) => {
    const body = JSON.parse(route.request().postData() ?? "{}") as {
      body: string;
      range: Record<string, unknown>;
    };
    onCreateDraft?.(body);
    draftComments = [{
      id: "1",
      body: body.body,
      path: body.range.path,
      side: body.range.side,
      line: body.range.line,
      new_line: body.range.new_line,
      line_type: body.range.line_type,
      diff_head_sha: body.range.diff_head_sha,
      created_at: "2026-03-30T14:01:00Z",
      updated_at: "2026-03-30T14:01:00Z",
    }];
    await fulfillJson(route, draftComments[0], 201);
  });
  await page.route(`**${path}/review-draft/publish`, async (route) => {
    draftComments = options.remainingDraftComments ?? [];
    await fulfillJson(route, { status: options.publishStatus ?? "published" });
  });
  await page.route(`**${path}/review-threads/1/resolve`, async (route) => {
    reviewThreadResolved = true;
    await fulfillJson(route, { status: "ok" });
  });
}

test.beforeEach(async ({ page }) => {
  await mockApi(page);
});

test("adds and publishes an inline draft review comment", async ({ page }) => {
  await mockInlineReviewAPI(page);

  await page.goto("/pulls/github/acme/widgets/42");
  await page.getByRole("button", { name: "Files changed" }).click();
  await page.getByRole("button", { name: "Comment on new line 2" }).click();
  await page.getByPlaceholder("Leave a comment").fill("Please cover this line.");
  await page.getByRole("button", { name: "Add comment" }).click();

  await expect(page.getByText("1 draft comment")).toBeVisible();
  await expect(page.getByText("Please cover this line.")).toBeVisible();
  await page.getByRole("button", { name: "Publish review" }).click();
  await expect(page.getByText("1 draft comment")).toBeHidden();
});

test("keeps remaining GitLab draft state visible after a partial publish", async ({ page }) => {
  await mockInlineReviewAPI(
    page,
    baseCapabilities,
    "gitlab",
    "gitlab.com",
    diffResponse,
    undefined,
    {
      publishStatus: "partially_published",
      remainingDraftComments: [{
        id: "remaining-1",
        body: "Still needs follow-up.",
        path: "src/main.ts",
        side: "right",
        line: 2,
        new_line: 2,
        line_type: "add",
        diff_head_sha: "diff-head",
        created_at: "2026-03-30T14:02:00Z",
        updated_at: "2026-03-30T14:02:00Z",
      }],
    },
  );

  await page.goto("/pulls/gitlab/acme/widgets/42/files");
  await page.getByRole("button", { name: "Comment on new line 2" }).click();
  await page.getByPlaceholder("Leave a comment").fill("Please cover this line.");
  await page.getByRole("button", { name: "Add comment" }).click();

  const summary = page.getByPlaceholder("Review summary");
  await summary.fill("Summary should not stay in the composer.");
  await page.getByRole("button", { name: "Publish review" }).click();

  await expect(summary).toHaveValue("");
  await expect(page.locator(".review-warning")).toContainText("Review was partially published");
  await expect(page.getByText("1 draft comment")).toBeVisible();
  await expect(page.getByText("Still needs follow-up.")).toBeVisible();
});

test("hides inline review controls when provider draft review is unsupported", async ({ page }) => {
  await mockInlineReviewAPI(page, {
    ...baseCapabilities,
    review_draft_mutation: false,
    supported_review_actions: [],
  });

  await page.goto("/pulls/github/acme/widgets/42");
  await page.getByRole("button", { name: "Files changed" }).click();
  await expect(page.getByRole("button", { name: "Comment on new line 2" })).toHaveCount(0);
});

test("resolves a published inline review thread from the timeline", async ({ page }) => {
  await mockInlineReviewAPI(page);

  await page.goto("/pulls/github/acme/widgets/42");
  await expect(page.getByText("Existing inline comment")).toBeVisible();
  await page.getByRole("button", { name: "Resolve" }).click();
  await expect(page.getByText("Resolved")).toBeVisible();
});

test("enables inline review on public Forgejo and Gitea files routes", async ({ page }) => {
  await mockInlineReviewAPI(page, baseCapabilities, "forgejo", "codeberg.org");
  await page.goto("/pulls/forgejo/acme/widgets/42/files");
  await expect(page.getByRole("button", { name: "Comment on new line 2" })).toBeVisible();

  await mockInlineReviewAPI(page, baseCapabilities, "gitea", "gitea.com");
  await page.goto("/pulls/gitea/acme/widgets/42/files");
  await expect(page.getByRole("button", { name: "Comment on new line 2" })).toBeVisible();
});

test("does not create multiline draft ranges across separate PR diff hunks", async ({ page }) => {
  let createdRange: Record<string, unknown> | undefined;
  await mockInlineReviewAPI(
    page,
    baseCapabilities,
    "github",
    "github.com",
    multiHunkDiffResponse,
    (body) => { createdRange = body.range; },
  );

  await page.goto("/pulls/github/acme/widgets/42/files");
  await page.getByRole("button", { name: "Comment on new line 1" }).click();
  await page.getByRole("button", { name: "Comment on new line 20" }).click({
    modifiers: ["Shift"],
  });

  const selected = page.locator(".gutter-new.gutter--selected");
  await expect(selected).toHaveCount(1);
  await expect(selected).toHaveText("20");

  await page.getByPlaceholder("Leave a comment").fill("Only the second hunk.");
  await page.getByRole("button", { name: "Add comment" }).click();

  expect(createdRange).toMatchObject({
    path: "src/main.ts",
    side: "right",
    line: 20,
    new_line: 20,
  });
  expect(createdRange).not.toHaveProperty("start_line");
  expect(createdRange).not.toHaveProperty("start_side");
});
