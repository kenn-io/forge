import { expect, test, type Page, type Route } from "@playwright/test";

import { mockApi } from "./support/mockApi";

type TimelineEvent = {
  ID: number;
  MergeRequestID?: number;
  IssueID?: number;
  PlatformID: number;
  EventType: string;
  Author: string;
  Summary: string;
  Body: string;
  MetadataJSON: string;
  CreatedAt: string;
  DedupeKey: string;
  DirectURL?: string;
};

const mockRepo = {
  provider: "github",
  platform_host: "github.com",
  repo_path: "acme/widgets",
  owner: "acme",
  name: "widgets",
  capabilities: {
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
    mutation_head_binding: false,
    thread_reply: false,
    thread_resolve: false,
    supported_review_actions: [],
  },
};

async function fulfillJson(route: Route, body: unknown, status = 200): Promise<void> {
  await route.fulfill({
    status,
    contentType: "application/json",
    body: JSON.stringify(body),
  });
}

function prDetail(commentBody: string, event: TimelineEvent) {
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
      Body: "Adds Playwright smoke tests for workspace panel.",
      HeadBranch: "feature/playwright",
      BaseBranch: "main",
      Additions: 120,
      Deletions: 12,
      CommentCount: 1,
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
    events: [{ ...event, Body: commentBody }],
    repo: mockRepo,
    repo_owner: "acme",
    repo_name: "widgets",
    platform_host: "github.com",
    detail_loaded: true,
    detail_fetched_at: "2026-03-30T14:00:00Z",
    worktree_links: [],
  };
}

function issueDetail(commentBody: string, event: TimelineEvent) {
  return {
    issue: {
      ID: 2,
      RepoID: 1,
      GitHubID: 202,
      Number: 7,
      URL: "https://github.com/acme/widgets/issues/7",
      Title: "Theme toggle does not stick",
      Author: "marius",
      State: "open",
      Body: "",
      CommentCount: 1,
      LabelsJSON: "[]",
      CreatedAt: "2026-03-28T14:00:00Z",
      UpdatedAt: "2026-03-30T14:00:00Z",
      LastActivityAt: "2026-03-30T14:00:00Z",
      ClosedAt: null,
      Starred: false,
      platform_host: "github.com",
      repo_owner: "acme",
      repo_name: "widgets",
      repo: mockRepo,
    },
    events: [{ ...event, Body: commentBody }],
    repo: mockRepo,
    platform_host: "github.com",
    repo_owner: "acme",
    repo_name: "widgets",
    detail_loaded: true,
    detail_fetched_at: "2026-03-30T14:00:00Z",
  };
}

async function editVisibleTimelineComment(page: Page, body: string): Promise<void> {
  await page.getByText(/^Original /).hover();
  await page.getByRole("button", { name: "Edit comment" }).first().click();
  const editor = page.locator(".edit-panel .comment-editor-input");
  await expect(editor).toBeVisible();
  await editor.click();
  await page.keyboard.press("ControlOrMeta+A");
  await page.keyboard.type(body);
  await page.getByRole("button", { name: "Save" }).click();
}

test.beforeEach(async ({ page }) => {
  await mockApi(page);
});

test("edits a pull request timeline comment", async ({ page }) => {
  let commentBody = "Original PR comment";
  let patchedBody = "";
  const event: TimelineEvent = {
    ID: 11,
    MergeRequestID: 1,
    PlatformID: 9101,
    EventType: "issue_comment",
    Author: "marius",
    Summary: "",
    Body: commentBody,
    MetadataJSON: "",
    CreatedAt: "2026-03-30T14:00:00Z",
    DedupeKey: "comment-9101",
  };

  await page.route(/\/api\/v1\/pulls\/github\/acme\/widgets\/42(?:[/?]|$)/, async (route) => {
    if (route.request().method() !== "GET") {
      await route.fallback();
      return;
    }
    await fulfillJson(route, prDetail(commentBody, event));
  });
  await page.route("**/api/v1/pulls/github/acme/widgets/42/comments/9101", async (route) => {
    const reqBody = JSON.parse(route.request().postData() ?? "{}") as {
      body?: string;
    };
    patchedBody = reqBody.body ?? "";
    commentBody = patchedBody;
    await fulfillJson(route, { ...event, Body: patchedBody });
  });

  await page.goto("/pulls/github/acme/widgets/42");
  await expect(page.getByText("Original PR comment")).toBeVisible();

  await editVisibleTimelineComment(page, "Edited PR comment");

  await expect.poll(() => patchedBody).toBe("Edited PR comment");
  await expect(page.getByText("Edited PR comment")).toBeVisible();
});

test("copies a direct provider link from a pull request timeline comment", async ({ page, context }) => {
  await context.grantPermissions(["clipboard-read", "clipboard-write"]);
  const directURL = "https://github.com/acme/widgets/pull/42#issuecomment-9101";
  const event: TimelineEvent = {
    ID: 11,
    MergeRequestID: 1,
    PlatformID: 9101,
    EventType: "issue_comment",
    Author: "marius",
    Summary: "",
    Body: "Original PR comment",
    MetadataJSON: "",
    CreatedAt: "2026-03-30T14:00:00Z",
    DedupeKey: "comment-9101",
    DirectURL: directURL,
  };

  await page.route(/\/api\/v1\/pulls\/github\/acme\/widgets\/42(?:[/?]|$)/, async (route) => {
    if (route.request().method() !== "GET") {
      await route.fallback();
      return;
    }
    await fulfillJson(route, prDetail("Original PR comment", event));
  });

  await page.goto("/pulls/github/acme/widgets/42");
  await page.getByText("Original PR comment").hover();
  await page.getByRole("button", { name: "Copy direct link" }).click();

  await expect.poll(() => page.evaluate(() => navigator.clipboard.readText())).toBe(directURL);
});

test("edits an issue timeline comment", async ({ page }) => {
  let commentBody = "Original issue comment";
  let patchedBody = "";
  const event: TimelineEvent = {
    ID: 22,
    IssueID: 2,
    PlatformID: 9202,
    EventType: "issue_comment",
    Author: "marius",
    Summary: "",
    Body: commentBody,
    MetadataJSON: "",
    CreatedAt: "2026-03-30T14:00:00Z",
    DedupeKey: "issue-comment-9202",
  };

  await page.route(/\/api\/v1\/issues\/github\/acme\/widgets\/7(?:[/?]|$)/, async (route) => {
    if (route.request().method() !== "GET") {
      await route.fallback();
      return;
    }
    await fulfillJson(route, issueDetail(commentBody, event));
  });
  await page.route("**/api/v1/issues/github/acme/widgets/7/comments/9202", async (route) => {
    const reqBody = JSON.parse(route.request().postData() ?? "{}") as {
      body?: string;
    };
    patchedBody = reqBody.body ?? "";
    commentBody = patchedBody;
    await fulfillJson(route, { ...event, Body: patchedBody });
  });

  await page.goto("/focus/issues/github/acme/widgets/7");
  await expect(page.getByText("Original issue comment")).toBeVisible();

  await editVisibleTimelineComment(page, "Edited issue comment");

  await expect.poll(() => patchedBody).toBe("Edited issue comment");
  await expect(page.getByText("Edited issue comment")).toBeVisible();
});
