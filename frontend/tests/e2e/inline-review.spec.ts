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
  draft_mutation: true,
  issue_mutation: true,
  review_draft_mutation: true,
  review_thread_resolution: true,
  read_review_threads: true,
  native_multiline_ranges: true,
  mutation_head_binding: false,
  supported_review_actions: ["comment", "approve", "request_changes"],
};

function pullDetail(
  reviewThreadResolved = false,
  capabilities = baseCapabilities,
  provider = "github",
  platformHost = "github.com",
  operations?: Record<string, unknown>,
  events?: Array<Record<string, unknown>>,
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
    events: events ?? [
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
      ...(operations !== undefined && { operations }),
    },
    repo_owner: "acme",
    repo_name: "widgets",
    platform_host: platformHost,
    platform_head_sha: "diff-head",
    reviewed_head_sha: "diff-head",
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

function pullListItem(capabilities = baseCapabilities, provider = "github", platformHost = "github.com") {
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
      ...(operations !== undefined && { operations }),
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
            {
              type: "context",
              old_num: 1,
              new_num: 1,
              content: "const a = 1;",
            },
            {
              type: "add",
              old_num: null,
              new_num: 2,
              content: "const b = 2;",
            },
          ],
        },
      ],
    },
  ],
};

const splitComposerDiffResponse = {
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
          new_count: 3,
          section: "",
          lines: [
            {
              type: "context",
              old_num: 1,
              new_num: 1,
              content: "const a = 1;",
            },
            {
              type: "add",
              old_num: null,
              new_num: 2,
              content: "const b = 2;",
            },
            {
              type: "add",
              old_num: null,
              new_num: 3,
              content: "const c = 3;",
            },
          ],
        },
      ],
    },
  ],
};

const trailingAddedFileDiffResponse = {
  stale: false,
  whitespace_only_count: 0,
  files: [
    {
      path: ".github/ISSUE_TEMPLATE/config.yml",
      old_path: ".github/ISSUE_TEMPLATE/config.yml",
      status: "added",
      additions: 4,
      deletions: 0,
      is_binary: false,
      hunks: [
        {
          old_start: 0,
          old_count: 0,
          new_start: 1,
          new_count: 4,
          section: "",
          lines: [
            {
              type: "add",
              old_num: null,
              new_num: 1,
              content: "# Route every new issue through one of the templates above.",
            },
            {
              type: "add",
              old_num: null,
              new_num: 2,
              content: "# Discussions is enabled for this repo, add a contact_links entry pointing",
            },
            {
              type: "add",
              old_num: null,
              new_num: 3,
              content: '# usage/"how do I" questions there instead of the issue tracker.',
            },
            {
              type: "add",
              old_num: null,
              new_num: 4,
              content: "blank_issues_enabled: false",
            },
          ],
        },
      ],
    },
    {
      path: ".github/ISSUE_TEMPLATE/feature_request.yml",
      old_path: ".github/ISSUE_TEMPLATE/feature_request.yml",
      status: "added",
      additions: 2,
      deletions: 0,
      is_binary: false,
      hunks: [
        {
          old_start: 0,
          old_count: 0,
          new_start: 1,
          new_count: 2,
          section: "",
          lines: [
            {
              type: "add",
              old_num: null,
              new_num: 1,
              content: "name: Feature request",
            },
            {
              type: "add",
              old_num: null,
              new_num: 2,
              content: "description: Suggest a new feature or improvement",
            },
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
            {
              type: "add",
              old_num: null,
              new_num: 1,
              content: "const first = 1;",
            },
          ],
        },
        {
          old_start: 20,
          old_count: 1,
          new_start: 20,
          new_count: 1,
          section: "",
          lines: [
            {
              type: "add",
              old_num: null,
              new_num: 20,
              content: "const second = 2;",
            },
          ],
        },
      ],
    },
  ],
};

const longLineDiffResponse = {
  stale: false,
  whitespace_only_count: 0,
  files: [
    {
      path: "internal/github/client.go",
      old_path: "internal/github/client.go",
      status: "modified",
      additions: 1,
      deletions: 0,
      is_binary: false,
      hunks: [
        {
          old_start: 1140,
          old_count: 1,
          new_start: 1140,
          new_count: 2,
          section: "",
          lines: [
            {
              type: "context",
              old_num: 1140,
              new_num: 1140,
              content:
                "func (c *liveClient) CreateReviewWithComments(ctx context.Context, owner, repo string, number int, event string, body string) (*gh.PullRequestReview, error) {",
            },
            {
              type: "add",
              old_num: null,
              new_num: 1141,
              content:
                "\treturn c.CreateReviewWithComments(ctx, owner, repo, number, event, body, comments, pullRequestReviewOptions, requestOptions, validationOptions)",
            },
          ],
        },
      ],
    },
  ],
};

function scrollingDiffResponse() {
  return {
    stale: false,
    whitespace_only_count: 0,
    files: Array.from({ length: 16 }, (_, fileIndex) => ({
      path: `src/file-${fileIndex}.ts`,
      old_path: `src/file-${fileIndex}.ts`,
      status: "modified",
      additions: 40,
      deletions: 0,
      is_binary: false,
      hunks: [
        {
          old_start: 1,
          old_count: 1,
          new_start: 1,
          new_count: 41,
          section: "",
          lines: [
            {
              type: "context",
              old_num: 1,
              new_num: 1,
              content: `const file${fileIndex} = true;`,
            },
            ...Array.from({ length: 40 }, (_, lineIndex) => ({
              type: "add",
              old_num: null,
              new_num: lineIndex + 2,
              content: `const value${fileIndex}_${lineIndex} = ${lineIndex};`,
            })),
          ],
        },
      ],
    })),
  };
}

type MockInlineReviewOptions = {
  detailEvents?: Array<Record<string, unknown>>;
  publishStatus?: "published" | "partially_published";
  detailBody?: string;
  detailFetchedAtSequence?: string[];
  initialDraftComments?: Array<Record<string, unknown>>;
  remainingDraftComments?: Array<Record<string, unknown>>;
  operations?: Record<string, unknown>;
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
  let draftComments: Array<Record<string, unknown>> = [...(options.initialDraftComments ?? [])];
  let detailRequestCount = 0;
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
    const detail = pullDetail(
      reviewThreadResolved,
      capabilities,
      provider,
      platformHost,
      options.operations,
      options.detailEvents,
    );
    if (options.detailBody !== undefined) {
      detail.merge_request.Body = options.detailBody;
    }
    const fetchedAt =
      options.detailFetchedAtSequence?.[Math.min(detailRequestCount, options.detailFetchedAtSequence.length - 1)];
    detailRequestCount += 1;
    if (fetchedAt !== undefined) {
      detail.detail_fetched_at = fetchedAt;
      detail.merge_request.DetailFetchedAt = fetchedAt;
    }
    await fulfillJson(route, detail);
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
    draftComments = [
      {
        id: "1",
        body: body.body,
        path: body.range.path,
        side: body.range.side,
        line: body.range.line,
        new_line: body.range.new_line,
        old_line: body.range.old_line,
        start_line: body.range.start_line,
        start_side: body.range.start_side,
        line_type: body.range.line_type,
        diff_head_sha: body.range.diff_head_sha,
        created_at: "2026-03-30T14:01:00Z",
        updated_at: "2026-03-30T14:01:00Z",
      },
    ];
    await fulfillJson(route, draftComments[0], 201);
  });
  await page.route(`**${path}/review-draft/publish`, async (route) => {
    draftComments = options.remainingDraftComments ?? [];
    await fulfillJson(route, {
      status: options.publishStatus ?? "published",
    });
  });
  await page.route(`**${path}/review-threads/1/resolve`, async (route) => {
    reviewThreadResolved = true;
    await fulfillJson(route, { status: "ok" });
  });
}

async function firstDiffGutterRight(page: Page): Promise<number> {
  return page
    .locator(".pierre-diff")
    .first()
    .evaluate((host) => {
      const gutter = host.shadowRoot?.querySelector("[data-gutter]");
      if (!(gutter instanceof HTMLElement)) {
        throw new Error("diff gutter not found");
      }
      return gutter.getBoundingClientRect().right;
    });
}

test.beforeEach(async ({ page }) => {
  await mockApi(page);
});

test("unavailable operations disable inline review authoring and thread replies", async ({ page }) => {
  // A GitHub App split host with no user write credential reports
  // submit_review and reply_review_thread unavailable; the Files tab
  // must not offer inline review authoring or thread replies that
  // would fail at publish/reply time.
  const unavailableOp = {
    available: false,
    code: "missing_write_credential",
    unavailable_reason: "No user credential for writes on github.com",
  };
  const replyCalls: string[] = [];
  page.on("request", (request) => {
    if (request.method() === "GET") return;
    const path = new URL(request.url()).pathname;
    if (/\/reply$/.test(path) || path.includes("/discussions/")) {
      replyCalls.push(`${request.method()} ${path}`);
    }
  });
  await mockInlineReviewAPI(page, baseCapabilities, "github", "github.com", diffResponse, undefined, {
    operations: { submit_review: unavailableOp, reply_review_thread: unavailableOp },
  });

  await page.goto("/pulls/github/acme/widgets/42/files");
  await expect(page.locator(".file-content").first()).toBeVisible();
  await expect(page.getByRole("button", { name: /Comment on new line/ })).toHaveCount(0);
  await expect(page.getByPlaceholder("Leave a comment")).toHaveCount(0);
  // The existing review thread renders read-only: no reply composer
  // or reply affordance, and no reply request can fire.
  await expect(page.locator(".inline-review-thread").first()).toBeVisible();
  await expect(page.getByPlaceholder("Reply to thread")).toHaveCount(0);
  await expect(page.getByRole("button", { name: /^Reply/ })).toHaveCount(0);
  expect(replyCalls).toEqual([]);
});

test("a loaded draft becomes unpublishable when submit_review flips unavailable", async ({ page }) => {
  // The riskiest Files-tab path: a draft that is already loaded and
  // publishable must lose its publish affordance when submit_review
  // becomes unavailable while the diff stays mounted. Split view
  // keeps the conversation pane (which polls the detail payload) and
  // the files pane mounted together, so the flip arrives through the
  // real in-app poll without any navigation or reload. The local
  // draft view clearing while gated is the store's designed behavior;
  // the draft itself persists server-side, so flipping back must
  // restore it.
  const rateLimitedReason = "github.com rate-limited; retry at 14:35";
  const publishCalls: string[] = [];
  page.on("request", (request) => {
    if (request.method() === "GET") return;
    const path = new URL(request.url()).pathname;
    if (path.endsWith("/review-draft/publish")) {
      publishCalls.push(`${request.method()} ${path}`);
    }
  });
  await mockInlineReviewAPI(page, baseCapabilities, "github", "github.com", diffResponse, undefined, {
    initialDraftComments: [
      {
        id: "draft-1",
        body: "needs a test",
        path: "internal/server/server.go",
        side: "right",
        line: 1,
        new_line: 1,
        line_type: "add",
        diff_head_sha: "diff-head",
        created_at: "2026-03-30T14:01:00Z",
        updated_at: "2026-03-30T14:01:00Z",
      },
    ],
  });
  // Override the detail route (last registered wins) so the test can
  // flip submit_review availability between detail polls.
  let submitReviewOp: Record<string, unknown> | undefined;
  let detailGets = 0;
  await page.route("**/api/v1/pulls/github/acme/widgets/42", async (route) => {
    if (route.request().method() !== "GET") {
      await route.fallback();
      return;
    }
    detailGets += 1;
    const operations = submitReviewOp !== undefined ? { submit_review: submitReviewOp } : undefined;
    await fulfillJson(route, pullDetail(false, baseCapabilities, "github", "github.com", operations));
  });

  await page.clock.install();
  await page.setViewportSize({ width: 2200, height: 1000 });
  await page.goto("/pulls/github/acme/widgets/42");
  await page.getByRole("button", { name: "Split view", exact: true }).click();
  await expect(page.getByText("1 draft comment")).toBeVisible();
  await expect(page.getByRole("button", { name: "Publish review" })).toBeVisible();

  // Flip off: the next detail poll delivers the exhausted budget and
  // the mounted diff swaps the publishable tray for the reason.
  submitReviewOp = {
    available: false,
    code: "rate_limited",
    unavailable_reason: rateLimitedReason,
    retry_at: "2026-03-30T14:35:00Z",
  };
  let getsBefore = detailGets;
  await page.clock.fastForward(61_000);
  await expect.poll(() => detailGets).toBeGreaterThan(getsBefore);
  await expect(page.getByRole("button", { name: "Publish review" })).toHaveCount(0);
  await expect(page.locator(".review-warning")).toContainText(rateLimitedReason);

  // Flip back: the server-side draft survived the gated window and
  // the view restores it, publishable again.
  submitReviewOp = undefined;
  getsBefore = detailGets;
  await page.clock.fastForward(61_000);
  await expect.poll(() => detailGets).toBeGreaterThan(getsBefore);
  await expect(page.getByText("1 draft comment")).toBeVisible();
  await expect(page.getByRole("button", { name: "Publish review" })).toBeVisible();
  expect(publishCalls).toEqual([]);
});

test("adds and publishes an inline draft review comment", async ({ page }) => {
  await mockInlineReviewAPI(page);

  await page.goto("/pulls/github/acme/widgets/42");
  await page.getByRole("button", { name: "Files changed" }).click();
  await page.getByRole("button", { name: "Comment on new line 2" }).click();
  const composer = page.getByPlaceholder("Leave a comment");
  await composer.fill("Please cover this line.");
  await expect(composer).toHaveValue("Please cover this line.");
  const addCommentButton = page.getByRole("button", { name: "Add comment" });
  await expect(addCommentButton).toBeEnabled();
  await addCommentButton.click();

  await expect(page.getByText("1 draft comment")).toBeVisible();
  await expect(page.locator(".inline-draft-comment")).toContainText("Please cover this line.");
  await expect(page.getByRole("button", { name: "Show full comment" })).toHaveCount(0);
  await page.getByRole("button", { name: "Publish review" }).click();
  await expect(page.getByText("1 draft comment")).toBeHidden();
});

test("keeps split-mode inline composers on trailing right-side hunk lines", async ({ page }) => {
  await page.addInitScript(() => {
    localStorage.setItem("diff-view-mode", "split");
  });
  await mockInlineReviewAPI(page, baseCapabilities, "github", "github.com", splitComposerDiffResponse, undefined, {
    detailEvents: [],
  });

  await page.goto("/pulls/github/acme/widgets/42/files");
  async function assertRightSideComposer(line: number): Promise<void> {
    const button = page.getByRole("button", { name: `Comment on new line ${line}` });
    await expect(button).toBeVisible();
    await expect(
      page
        .locator(".pierre-diff")
        .first()
        .evaluate((host, lineNumber) => {
          const root = host.shadowRoot;
          const button = Array.from(
            root?.querySelectorAll<HTMLButtonElement>("[data-middleman-line-comment-button]") ?? [],
          ).find((candidate) => candidate.getAttribute("aria-label") === `Comment on new line ${lineNumber}`);
          return button ? pierreCodeSide(root, button) : null;

          function pierreCodeSide(
            root: ShadowRoot | null | undefined,
            element: Element,
          ): "deletions" | "additions" | null {
            const code = Array.from(root?.querySelectorAll<HTMLElement>("code") ?? []).find((candidate) =>
              candidate.contains(element),
            );
            const pre = code?.parentElement;
            if (!code || !pre) return null;
            if (code.hasAttribute("data-additions")) return "additions";
            if (code.hasAttribute("data-deletions")) return "deletions";
            const index = Array.from(pre.children)
              .filter((child) => child.tagName.toLowerCase() === "code")
              .indexOf(code);
            if (index === 0) return "deletions";
            if (index === 1) return "additions";
            return null;
          }
        }, line),
    ).resolves.toBe("additions");

    await button.click();
    await expect(page.getByPlaceholder("Leave a comment")).toBeVisible();
    await expect(
      page
        .locator(".pierre-diff")
        .first()
        .evaluate((host, lineNumber) => {
          const root = host.shadowRoot;
          const slot = root?.querySelector(`slot[name="annotation-additions-${lineNumber}"]`);
          return slot ? pierreCodeSide(root, slot) : null;

          function pierreCodeSide(
            root: ShadowRoot | null | undefined,
            element: Element,
          ): "deletions" | "additions" | null {
            const code = Array.from(root?.querySelectorAll<HTMLElement>("code") ?? []).find((candidate) =>
              candidate.contains(element),
            );
            const pre = code?.parentElement;
            if (!code || !pre) return null;
            if (code.hasAttribute("data-additions")) return "additions";
            if (code.hasAttribute("data-deletions")) return "deletions";
            const index = Array.from(pre.children)
              .filter((child) => child.tagName.toLowerCase() === "code")
              .indexOf(code);
            if (index === 0) return "deletions";
            if (index === 1) return "additions";
            return null;
          }
        }, line),
    ).resolves.toBe("additions");
    await page.getByRole("button", { name: "Cancel" }).click();
    await expect(page.getByPlaceholder("Leave a comment")).toHaveCount(0);
  }

  await assertRightSideComposer(2);
  await assertRightSideComposer(3);
});

test("keeps final added-file line visible when opening an inline composer", async ({ page }) => {
  await mockInlineReviewAPI(page, baseCapabilities, "github", "github.com", trailingAddedFileDiffResponse, undefined, {
    detailEvents: [],
  });

  await page.goto("/pulls/github/acme/widgets/42/files");
  await expect(page.getByText("blank_issues_enabled: false")).toBeVisible();

  async function assertComposerLayout(lineNumber: number, lineText: string, followingText?: string): Promise<void> {
    await page.getByRole("button", { name: `Comment on new line ${lineNumber}` }).click();
    await expect(page.getByPlaceholder("Leave a comment")).toBeVisible();
    await expect(page.getByText(lineText)).toBeVisible();

    const layout = await page
      .locator(".pierre-diff")
      .first()
      .evaluate(
        (host, { lineNumber, lineText, followingText }) => {
          const root = host.shadowRoot;
          const line = Array.from(
            root?.querySelectorAll<HTMLElement>(`[data-diff-new-line="${lineNumber}"]`) ?? [],
          ).find((node) => node.textContent?.includes(lineText));
          const following = followingText
            ? Array.from(root?.querySelectorAll<HTMLElement>("[data-diff-new-line]") ?? []).find((node) =>
                node.textContent?.includes(followingText),
              )
            : host.closest(".diff-file")?.nextElementSibling?.querySelector(".file-header");
          const slot = root?.querySelector<HTMLSlotElement>(`slot[name="annotation-additions-${lineNumber}"]`);
          const composer = host.querySelector<HTMLElement>(".inline-composer");
          if (!line || !slot || !composer || !(following instanceof HTMLElement)) return null;
          const lineRect = line.getBoundingClientRect();
          const composerRect = composer.getBoundingClientRect();
          const followingRect = following.getBoundingClientRect();
          return {
            assignedCount: slot.assignedElements({ flatten: true }).length,
            composerBottom: Math.round(composerRect.bottom),
            composerTop: Math.round(composerRect.top),
            followingTop: Math.round(followingRect.top),
            lineBottom: Math.round(lineRect.bottom),
            slotInsideLine: slot.closest(`[data-diff-new-line="${lineNumber}"]`) !== null,
          };
        },
        { lineNumber, lineText, followingText },
      );
    expect(layout).not.toBeNull();
    expect(layout!.assignedCount).toBeGreaterThan(0);
    expect(layout!.slotInsideLine).toBe(false);
    expect(layout!.composerTop).toBeGreaterThanOrEqual(layout!.lineBottom);
    expect(layout!.followingTop).toBeGreaterThanOrEqual(layout!.composerBottom);
    await page.getByRole("button", { name: "Cancel" }).click();
    await expect(page.getByPlaceholder("Leave a comment")).toHaveCount(0);
  }

  await assertComposerLayout(
    3,
    '# usage/"how do I" questions there instead of the issue tracker.',
    "blank_issues_enabled: false",
  );
  await assertComposerLayout(4, "blank_issues_enabled: false");
});

test("shows a visible composer focus indicator without focus flicker", async ({ page }) => {
  await mockInlineReviewAPI(page);

  await page.goto("/pulls/github/acme/widgets/42");
  await page.getByRole("button", { name: "Files changed" }).click();

  // Regression guard for issues #445/#446: the composer textarea must never
  // visibly blur while open (focusout count stays zero) and must end up
  // focused without any extra interaction. Firefox silently annuls focus
  // (no focusout) whenever the diff re-render momentarily unslots the
  // composer, and PierreFileDiff restores it on the following slotchange, so
  // the focusin count is browser-dependent; a runaway retry loop would still
  // blow past the small bound asserted below. Count transitions from before
  // the composer exists.
  await page.evaluate(() => {
    const counts = { focusin: 0, focusout: 0 };
    const win = window as typeof window & { __composerFocusCounts?: typeof counts };
    win.__composerFocusCounts = counts;
    const isComposerTextarea = (target: EventTarget | null): boolean =>
      target instanceof HTMLTextAreaElement && target.closest(".inline-composer") !== null;
    document.addEventListener(
      "focusin",
      (event) => {
        if (isComposerTextarea(event.target)) counts.focusin += 1;
      },
      true,
    );
    document.addEventListener(
      "focusout",
      (event) => {
        if (isComposerTextarea(event.target)) counts.focusout += 1;
      },
      true,
    );
  });

  await page.getByRole("button", { name: "Comment on new line 2" }).click();
  const composer = page.getByPlaceholder("Leave a comment");
  await expect(composer).toBeVisible();

  // The composer must be focused without clicking it — this is the actual
  // issue #446 outcome and what the Firefox focus-annulment regression broke.
  await expect(composer).toBeFocused();

  // Let the windows previous retrying implementations used (animation frames
  // plus a 50ms timer) elapse before reading the transition counts.
  await page.waitForTimeout(250);
  const counts = await page.evaluate(
    () =>
      (window as typeof window & { __composerFocusCounts?: { focusin: number; focusout: number } })
        .__composerFocusCounts,
  );
  expect(counts?.focusout).toBe(0);
  expect(counts?.focusin).toBeGreaterThanOrEqual(1);
  expect(counts?.focusin).toBeLessThanOrEqual(3);

  // The textarea must still be focused after the re-render windows above and
  // must show a visible focus ring.
  await expect(composer).toBeFocused();
  const focusedShadow = await composer.evaluate((element) => getComputedStyle(element).boxShadow);
  expect(focusedShadow).not.toBe("none");

  // The ring is focus-driven, not always-on chrome.
  await composer.evaluate((element) => (element as HTMLTextAreaElement).blur());
  const blurredShadow = await composer.evaluate((element) => getComputedStyle(element).boxShadow);
  expect(blurredShadow).toBe("none");
});

test("keeps the draft review footer readable for long comments", async ({ page }) => {
  await page.setViewportSize({ width: 1000, height: 720 });
  const longDraftBody = [
    "so i'd recommend we use huma for this similar to what we do in middleman,",
    "that generally means we can generate a nice typed client which makes more of the frontend safer",
  ].join(" ");
  await mockInlineReviewAPI(page, baseCapabilities, "github", "github.com", diffResponse, undefined, {
    initialDraftComments: [
      {
        id: "draft-1",
        body: longDraftBody,
        path: "internal/server/server.go",
        side: "right",
        line: 1,
        new_line: 1,
        line_type: "add",
        diff_head_sha: "diff-head",
        created_at: "2026-03-30T14:01:00Z",
        updated_at: "2026-03-30T14:01:00Z",
      },
    ],
  });

  await page.goto("/pulls/github/acme/widgets/42/files");
  await expect(page.getByText("1 draft comment")).toBeVisible();

  const draftList = page.locator(".draft-list");
  const draftBody = page.locator(".draft-body").first();
  await expect(draftBody).toContainText("so i'd recommend");
  await expect(page.getByRole("button", { name: "Show full comment" })).toBeVisible();

  await expect
    .poll(async () => draftList.evaluate((element) => element.scrollWidth <= element.clientWidth + 1))
    .toBe(true);
  await expect
    .poll(async () => draftBody.evaluate((element) => getComputedStyle(element).whiteSpace))
    .not.toBe("nowrap");
  const bodyBox = await draftBody.boundingBox();
  expect(bodyBox).not.toBeNull();
  expect(bodyBox!.height).toBeGreaterThan(24);

  await page.getByRole("button", { name: "Show full comment" }).click();
  await expect(page.getByRole("button", { name: "Show less" })).toBeVisible();
  const expandedBodyBox = await draftBody.boundingBox();
  expect(expandedBodyBox).not.toBeNull();
  expect(expandedBodyBox!.height).toBeGreaterThan(bodyBox!.height);
});

test("keeps inline composer inside the visible diff pane on long lines", async ({ page }) => {
  await page.setViewportSize({ width: 900, height: 720 });
  await page.addInitScript(() => {
    localStorage.setItem("diff-word-wrap", "true");
  });
  await mockInlineReviewAPI(page, baseCapabilities, "github", "github.com", longLineDiffResponse);

  await page.goto("/pulls/github/acme/widgets/42/files");
  await page.getByRole("button", { name: "Comment on new line 1141" }).click();

  const scrollPane = page.locator(".file-content").first();
  const composer = page.locator(".inline-composer");
  await expect(composer).toBeVisible();

  const scrollBox = await scrollPane.boundingBox();
  const composerBox = await composer.boundingBox();
  expect(scrollBox).not.toBeNull();
  expect(composerBox).not.toBeNull();
  const gutterRight = await firstDiffGutterRight(page);
  const contentWidth = scrollBox!.x + scrollBox!.width - gutterRight;
  expect(composerBox!.x).toBeGreaterThanOrEqual(gutterRight - 1);
  expect(composerBox!.x + composerBox!.width).toBeLessThanOrEqual(scrollBox!.x + scrollBox!.width + 1);
  expect(composerBox!.width).toBeGreaterThan(contentWidth * 0.85);
  const leftEdgeHitsTextarea = await composer.locator("textarea").evaluate((textarea) => {
    const rect = textarea.getBoundingClientRect();
    const target = document.elementFromPoint(rect.left + 8, rect.top + 16);
    return target === textarea || textarea.contains(target);
  });
  expect(leftEdgeHitsTextarea).toBe(true);
  const textarea = composer.locator("textarea");
  const initialTextareaHeight = await textarea.evaluate((element) => element.clientHeight);
  await textarea.fill(
    [
      "This comment has enough lines to grow.",
      "It should expand the editor instead of adding an internal scrollbar.",
      "That keeps the review text readable while the diff pane scrolls.",
      "One more line makes the regression obvious.",
      "And another line verifies the textarea keeps up.",
    ].join("\n"),
  );
  const textareaMetrics = await textarea.evaluate((element) => ({
    clientHeight: element.clientHeight,
    scrollHeight: element.scrollHeight,
  }));
  expect(textareaMetrics.clientHeight).toBeGreaterThan(initialTextareaHeight);
  expect(textareaMetrics.scrollHeight).toBeLessThanOrEqual(textareaMetrics.clientHeight + 1);
});

test("shows saved draft comments inline and jumps from the tray", async ({ page }) => {
  await page.addInitScript(() => {
    localStorage.setItem("diff-word-wrap", "true");
  });
  await mockInlineReviewAPI(page);

  await page.goto("/pulls/github/acme/widgets/42/files");
  await page.getByRole("button", { name: "Comment on new line 1" }).click();
  await page.getByRole("button", { name: "Comment on new line 2" }).click({
    modifiers: ["Shift"],
  });
  await page.getByPlaceholder("Leave a comment").fill("Please cover both lines.");
  await page.getByRole("button", { name: "Add comment" }).click();

  const inlineDraft = page.locator(".inline-draft-comment");
  const scrollPane = page.locator(".file-content").first();
  await expect(inlineDraft).toBeVisible();
  await expect(inlineDraft).toContainText("Please cover both lines.");
  await expect(page.locator(".gutter-new.gutter--selected")).toHaveCount(2);

  const scrollBox = await scrollPane.boundingBox();
  const inlineBox = await inlineDraft.boundingBox();
  expect(scrollBox).not.toBeNull();
  expect(inlineBox).not.toBeNull();
  const gutterRight = await firstDiffGutterRight(page);
  const contentWidth = scrollBox!.x + scrollBox!.width - gutterRight;
  expect(inlineBox!.x).toBeGreaterThanOrEqual(gutterRight - 1);
  expect(inlineBox!.x + inlineBox!.width).toBeLessThanOrEqual(scrollBox!.x + scrollBox!.width + 1);
  expect(inlineBox!.width).toBeGreaterThan(contentWidth * 0.85);

  await page.getByRole("button", { name: "src/main.ts:1-2" }).click();
  await expect(inlineDraft).toBeFocused();
});

test("keeps remaining GitLab draft state visible after a partial publish", async ({ page }) => {
  await mockInlineReviewAPI(page, baseCapabilities, "gitlab", "gitlab.com", diffResponse, undefined, {
    publishStatus: "partially_published",
    remainingDraftComments: [
      {
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
      },
    ],
  });

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
  await expect(page.locator(".inline-draft-comment")).toContainText("Still needs follow-up.");
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
  await expect(page.getByText("src/main.ts:2")).toBeVisible();
  await page.getByRole("button", { name: "Resolve" }).click();
  await expect(page.getByText("Resolved")).toBeVisible();
});

test("shows published inline review context in conversation and jumps to the diff line", async ({ page }) => {
  await mockInlineReviewAPI(page);

  await page.goto("/pulls/github/acme/widgets/42");

  await expect(page.getByLabel("Commented diff context")).toContainText("const b = 2;");
  await page.getByRole("button", { name: "Jump to diff" }).click();

  await expect(page.getByRole("button", { name: /Files changed/ })).toHaveClass(/detail-tab--active/);
  await expect(
    page.locator('[data-diff-path="src/main.ts"][data-diff-new-line="2"]:not([data-middleman-line-comment-cell])'),
  ).toBeVisible();
});

test("keeps published inline review context loaded after switching back from files", async ({ page }) => {
  await mockInlineReviewAPI(page);

  await page.goto("/pulls/github/acme/widgets/42/files");
  await expect(page.getByRole("button", { name: /Files changed/ })).toHaveClass(/detail-tab--active/);

  await page.getByRole("button", { name: "Conversation" }).click();
  await expect(page).toHaveURL(/\/pulls\/github\/acme\/widgets\/42$/);
  await expect(page.getByLabel("Commented diff context")).toContainText("const b = 2;");
  await expect(page.getByText("Loading diff")).toHaveCount(0);

  await page.getByRole("button", { name: "Files changed" }).click();
  await expect(page.getByRole("button", { name: /Files changed/ })).toHaveClass(/detail-tab--active/);
});

test("preserves PR detail scroll positions while switching tabs", async ({ page }) => {
  await mockInlineReviewAPI(page, baseCapabilities, "github", "github.com", scrollingDiffResponse(), undefined, {
    detailBody: Array.from({ length: 120 }, (_, index) => `Conversation filler line ${index}`).join("\n\n"),
  });

  await page.goto("/pulls/github/acme/widgets/42");
  await page.addStyleTag({
    content: ".pull-detail { min-height: 1800px; }",
  });
  const conversationScroller = page.locator(".pull-detail");
  await expect(conversationScroller).toBeVisible();
  await conversationScroller.evaluate((element) => {
    element.scrollTop = 420;
    element.dispatchEvent(new Event("scroll", { bubbles: true }));
  });
  await expect.poll(async () => conversationScroller.evaluate((element) => element.scrollTop)).toBeGreaterThan(350);

  await page.getByRole("button", { name: /Files changed/ }).click();
  const diffArea = page.locator(".diff-area");
  await expect(diffArea).toBeVisible();
  await diffArea.evaluate((element) => {
    element.scrollTop = 560;
    element.dispatchEvent(new Event("scroll", { bubbles: true }));
  });

  await page.getByRole("button", { name: "Conversation" }).click();
  await expect.poll(async () => conversationScroller.evaluate((element) => element.scrollTop)).toBeGreaterThan(350);

  await page.getByRole("button", { name: /Files changed/ }).click();
  await expect.poll(async () => diffArea.evaluate((element) => element.scrollTop)).toBeGreaterThan(480);
});

test("preserves PR detail scroll position after pushed refresh events", async ({ page }) => {
  await page.addInitScript(() => {
    type Listener = (event: MessageEvent) => void;
    class FakeEventSource {
      static instances: FakeEventSource[] = [];
      listeners = new Map<string, Listener[]>();
      url: string;

      constructor(url: string | URL) {
        this.url = String(url);
        FakeEventSource.instances.push(this);
      }

      addEventListener(type: string, listener: Listener): void {
        this.listeners.set(type, [...(this.listeners.get(type) ?? []), listener]);
      }

      removeEventListener(type: string, listener: Listener): void {
        this.listeners.set(
          type,
          (this.listeners.get(type) ?? []).filter((candidate) => candidate !== listener),
        );
      }

      close(): void {}

      emit(type: string, payload: unknown): void {
        const event = new MessageEvent(type, {
          data: JSON.stringify(payload),
        });
        for (const listener of this.listeners.get(type) ?? []) {
          listener(event);
        }
      }
    }

    (
      window as typeof window & {
        EventSource: typeof EventSource;
        __emitPRDetailRefresh: () => void;
      }
    ).EventSource = FakeEventSource as unknown as typeof EventSource;
    (
      window as typeof window & {
        __emitPRDetailRefresh: () => void;
      }
    ).__emitPRDetailRefresh = () => {
      for (const source of FakeEventSource.instances) {
        source.emit("pr_detail_refreshed", {
          provider: "github",
          platform_host: "github.com",
          repo_path: "acme/widgets",
          owner: "acme",
          name: "widgets",
          number: 42,
          head_sha: "diff-head",
          synced_at: "2026-03-30T14:05:00Z",
          warnings: [],
        });
      }
    };
  });
  await mockInlineReviewAPI(page, baseCapabilities, "github", "github.com", scrollingDiffResponse(), undefined, {
    detailBody: Array.from({ length: 120 }, (_, index) => `Conversation filler line ${index}`).join("\n\n"),
    detailFetchedAtSequence: ["2026-03-30T14:00:00Z", "2026-03-30T14:05:00Z"],
  });

  await page.goto("/pulls/github/acme/widgets/42");
  await page.addStyleTag({
    content: ".pull-detail { min-height: 1800px; }",
  });
  const conversationScroller = page.locator(".pull-detail");
  await expect(conversationScroller).toBeVisible();
  await conversationScroller.evaluate((element) => {
    element.scrollTop = 420;
    element.dispatchEvent(new Event("scroll", { bubbles: true }));
  });
  await expect.poll(async () => conversationScroller.evaluate((element) => element.scrollTop)).toBeGreaterThan(350);

  const refreshResponse = page.waitForResponse(
    (response) =>
      response.url().endsWith("/api/v1/pulls/github/acme/widgets/42") && response.request().method() === "GET",
  );
  await page.evaluate(() => {
    (
      window as typeof window & {
        __emitPRDetailRefresh: () => void;
      }
    ).__emitPRDetailRefresh();
  });
  await refreshResponse;

  await expect.poll(async () => conversationScroller.evaluate((element) => element.scrollTop)).toBeGreaterThan(350);
});

test("opens the sticky draft review action menu upward", async ({ page }) => {
  await mockInlineReviewAPI(page, baseCapabilities, "github", "github.com", scrollingDiffResponse(), undefined, {
    initialDraftComments: [
      {
        id: "draft-1",
        body: "foo",
        path: "src/file-12.ts",
        side: "right",
        line: 30,
        new_line: 30,
        line_type: "add",
        diff_head_sha: "diff-head",
        created_at: "2026-03-30T14:01:00Z",
        updated_at: "2026-03-30T14:01:00Z",
      },
    ],
  });

  await page.goto("/pulls/github/acme/widgets/42/files");
  await expect(page.getByText("1 draft comment")).toBeVisible();
  await page.getByRole("combobox", { name: "Review action: Comment" }).click();

  const triggerBox = await page.locator(".review-action-select .select-dropdown-trigger").boundingBox();
  const listBox = await page.locator(".review-action-select .select-dropdown-list").boundingBox();

  expect(triggerBox).not.toBeNull();
  expect(listBox).not.toBeNull();
  expect(listBox!.y + listBox!.height).toBeLessThanOrEqual(triggerBox!.y + 1);
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
  await mockInlineReviewAPI(page, baseCapabilities, "github", "github.com", multiHunkDiffResponse, (body) => {
    createdRange = body.range;
  });

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
