import { expect, test, type Page } from "@playwright/test";
import { mockApi } from "./support/mockApi";

// A GitHub App split host can sync with only the app token configured,
// but mutations ride the user's credential. When the server reports
// missing_write_credential for an operation, the detail views must
// disable that action with the reason instead of letting the click
// fail at request time. The merge button gained this gating earlier;
// these tests pin the non-merge mutations the review called out.

const MISSING_CREDENTIAL_REASON =
  "No user credential for writes on github.com: the GitHub App token only covers sync reads. Configure a PAT or gh CLI auth.";

const unavailable = {
  available: false,
  code: "missing_write_credential",
  unavailable_reason: MISSING_CREDENTIAL_REASON,
};

const operations = {
  merge_pr: unavailable,
  close_pr: unavailable,
  reopen_pr: unavailable,
  mark_ready_for_review: unavailable,
  submit_review: unavailable,
  add_comment: unavailable,
  edit_comment: unavailable,
  create_issue: unavailable,
  add_label: unavailable,
  remove_label: unavailable,
  close_issue: unavailable,
  reopen_issue: unavailable,
  approve_workflow: unavailable,
  update_content: unavailable,
  reply_review_thread: unavailable,
  resolve_review_thread: unavailable,
  set_assignees: unavailable,
  set_reviewers: unavailable,
};

const providerCapabilities = {
  assignee_mutation: true,
  reviewer_mutation: true,
  comment_mutation: true,
  issue_mutation: true,
  label_mutation: true,
  merge_mutation: true,
  read_ci: true,
  read_comments: true,
  read_issues: true,
  read_labels: true,
  read_merge_requests: true,
  read_releases: true,
  read_repositories: true,
  ready_for_review: true,
  draft_mutation: true,
  review_mutation: true,
  state_mutation: true,
  workflow_approval: true,
};

const draftPR = {
  ID: 11,
  RepoID: 1,
  GitHubID: 1101,
  Number: 100,
  URL: "https://github.com/acme/widgets/pull/100",
  Title: "Draft PR on an app-only host",
  Author: "marius",
  State: "open",
  IsDraft: true,
  Body: "Body",
  HeadBranch: "feature/a",
  BaseBranch: "main",
  Additions: 10,
  Deletions: 1,
  CommentCount: 0,
  ReviewDecision: "",
  CIStatus: "success",
  CIChecksJSON: "[]",
  CreatedAt: "2026-04-01T12:00:00Z",
  UpdatedAt: "2026-04-01T12:00:00Z",
  LastActivityAt: "2026-04-01T12:00:00Z",
  MergedAt: null,
  ClosedAt: null,
  KanbanStatus: "new",
  Starred: false,
  repo_owner: "acme",
  repo_name: "widgets",
  platform_host: "github.com",
  worktree_links: [],
  MergeableState: "clean",
};

function repoEnvelope(item: { repo_owner: string; repo_name: string; platform_host: string }) {
  return {
    provider: "github",
    platform_host: item.platform_host,
    owner: item.repo_owner,
    name: item.repo_name,
    repo_path: `${item.repo_owner}/${item.repo_name}`,
    capabilities: providerCapabilities,
    operations,
  };
}

const commentEvent = {
  ID: 11,
  MergeRequestID: 1,
  PlatformID: 9101,
  EventType: "issue_comment",
  Author: "marius",
  Summary: "",
  Body: "An existing comment",
  MetadataJSON: "",
  CreatedAt: "2026-03-30T14:00:00Z",
  DedupeKey: "comment-9101",
};

function detailEnvelopePR(pr: typeof draftPR): unknown {
  return {
    merge_request: pr,
    repo: repoEnvelope(pr),
    repo_owner: pr.repo_owner,
    repo_name: pr.repo_name,
    detail_loaded: true,
    detail_fetched_at: "2026-04-01T12:00:00Z",
    worktree_links: pr.worktree_links,
    events: [commentEvent],
  };
}

const openIssue = {
  ID: 31,
  RepoID: 1,
  GitHubID: 1201,
  Number: 300,
  URL: "https://github.com/acme/widgets/issues/300",
  Title: "Issue on an app-only host",
  Author: "marius",
  State: "open",
  Body: "Body",
  CommentCount: 0,
  LabelsJSON: "[]",
  labels: [],
  CreatedAt: "2026-04-01T12:00:00Z",
  UpdatedAt: "2026-04-01T12:00:00Z",
  LastActivityAt: "2026-04-01T12:00:00Z",
  ClosedAt: null,
  Starred: false,
  platform_host: "github.com",
  repo_owner: "acme",
  repo_name: "widgets",
};

function detailEnvelopeIssue(issue: typeof openIssue): unknown {
  return {
    issue,
    events: [],
    repo: repoEnvelope(issue),
    platform_host: issue.platform_host,
    repo_owner: issue.repo_owner,
    repo_name: issue.repo_name,
    detail_loaded: true,
    detail_fetched_at: "2026-04-01T12:00:00Z",
  };
}

async function mockRoutes(page: Page): Promise<void> {
  await mockApi(page);
  await page.route(`**/api/v1/pulls/github/acme/widgets/${draftPR.Number}`, async (route) => {
    if (route.request().method() === "GET") {
      await route.fulfill({
        status: 200,
        contentType: "application/json",
        body: JSON.stringify(detailEnvelopePR(draftPR)),
      });
      return;
    }
    await route.fallback();
  });
  await page.route("**/api/v1/repo/github/acme/widgets", async (route) => {
    if (route.request().method() !== "GET") {
      await route.fallback();
      return;
    }
    await route.fulfill({
      status: 200,
      contentType: "application/json",
      body: JSON.stringify({
        ID: draftPR.RepoID,
        Owner: "acme",
        Name: "widgets",
        AllowSquashMerge: true,
        AllowMergeCommit: true,
        AllowRebaseMerge: true,
        ViewerCanMerge: true,
        LastSyncStartedAt: "2026-04-01T12:00:00Z",
        LastSyncCompletedAt: "2026-04-01T12:00:30Z",
        LastSyncError: "",
        CreatedAt: "2026-03-01T00:00:00Z",
        operations,
      }),
    });
  });
}

test.describe("missing write credential gates non-merge mutations", () => {
  test("ready-for-review, close, comments, and labels are disabled with the reason and never hit the API", async ({
    page,
  }) => {
    // Record every non-GET request under the provider-aware PR routes
    // (e.g. /api/v1/pulls/github/acme/widgets/100/ready-for-review) so
    // a regression that fires any mutation fails the test.
    const mutationCalls: string[] = [];
    page.on("request", (request) => {
      if (request.method() === "GET") return;
      const path = new URL(request.url()).pathname;
      // The detail store fires automatic background syncs
      // (/sync, /sync/async); those are refresh triggers, not user
      // mutations, so they are excluded from the recording.
      if (/^\/api\/v1\/(pulls|issues|repo)\//.test(path) && !/\/sync(\/async)?$/.test(path)) {
        mutationCalls.push(`${request.method()} ${path}`);
      }
    });

    await mockRoutes(page);
    await page.goto(`/pulls/github/acme/widgets/${draftPR.Number}`);
    await expect(page.locator(".detail-title")).toContainText(draftPR.Title);

    const readyButton = page.locator(".btn--ready").first();
    await expect(readyButton).toBeDisabled();
    await expect(readyButton).toHaveAttribute("title", MISSING_CREDENTIAL_REASON);
    await readyButton.click({ force: true });

    const closeButton = page.locator(".btn--close").first();
    await expect(closeButton).toBeDisabled();
    await expect(closeButton).toHaveAttribute("title", MISSING_CREDENTIAL_REASON);
    await closeButton.click({ force: true });

    // The comment composer surfaces the reason and refuses to submit.
    const commentSubmit = page.locator(".comment-box .submit-btn");
    await expect(commentSubmit).toBeDisabled();
    await expect(page.locator(".comment-box .error-msg")).toContainText(MISSING_CREDENTIAL_REASON);

    // The label editor cannot open.
    const labelButton = page.locator(".btn--labels").first();
    await expect(labelButton).toBeDisabled();
    await expect(labelButton).toHaveAttribute("title", MISSING_CREDENTIAL_REASON);
    await labelButton.click({ force: true });
    await expect(page.locator(".label-editor-popover")).toHaveCount(0);

    // Content edits (title, description) gate on update_content: the
    // affordances are visibly disabled with the reason, not silent
    // no-ops.
    const editTitle = page.locator(".edit-title-btn");
    await expect(editTitle).toBeDisabled();
    await expect(editTitle).toHaveAttribute("title", MISSING_CREDENTIAL_REASON);
    await editTitle.click({ force: true });
    await expect(page.locator(".title-edit-input")).toHaveCount(0);
    const editBody = page.locator(".edit-body-btn");
    await expect(editBody).toBeDisabled();
    await expect(editBody).toHaveAttribute("title", MISSING_CREDENTIAL_REASON);

    // Timeline comment edits gate on edit_comment: the existing
    // comment renders without an edit affordance.
    await expect(page.getByText("An existing comment")).toBeVisible();
    await expect(page.getByRole("button", { name: "Edit comment" })).toHaveCount(0);

    // Assignee and reviewer editors gate on set_assignees and
    // set_reviewers: the chips disable with the reason and the picker
    // cannot open.
    const assigneeChip = page.locator('[data-user-list-editor="assignees"] button').first();
    await expect(assigneeChip).toBeDisabled();
    await expect(assigneeChip).toHaveAttribute("title", MISSING_CREDENTIAL_REASON);
    await assigneeChip.click({ force: true });
    await expect(page.locator(".user-list-editor__popover")).toHaveCount(0);
    const reviewerChip = page.locator('[data-user-list-editor="reviewers"] button').first();
    await expect(reviewerChip).toBeDisabled();
    await expect(reviewerChip).toHaveAttribute("title", MISSING_CREDENTIAL_REASON);

    expect(mutationCalls).toEqual([]);
  });

  test("a REST rate limit on update_content disables content edits with the retry reason", async ({ page }) => {
    // First-class content operations inherit per-operation rate-limit
    // gating: when only update_content is rate-limited, content edits
    // disable with the retry reason while unrelated mutations (close)
    // stay available.
    const rateLimitedReason = "github.com rate-limited; retry at 14:35";
    await mockApi(page);
    const rateLimitedOps = {
      ...Object.fromEntries(Object.keys(operations).map((k) => [k, { available: true }])),
      update_content: {
        available: false,
        code: "rate_limited",
        unavailable_reason: rateLimitedReason,
        retry_at: "2026-04-01T14:35:00Z",
      },
    };
    await page.route(`**/api/v1/pulls/github/acme/widgets/${draftPR.Number}`, async (route) => {
      if (route.request().method() === "GET") {
        const envelope = detailEnvelopePR(draftPR) as { repo: Record<string, unknown> };
        envelope.repo = { ...envelope.repo, operations: rateLimitedOps };
        await route.fulfill({
          status: 200,
          contentType: "application/json",
          body: JSON.stringify(envelope),
        });
        return;
      }
      await route.fallback();
    });

    await page.goto(`/pulls/github/acme/widgets/${draftPR.Number}`);
    await expect(page.locator(".detail-title")).toContainText(draftPR.Title);

    const editTitle = page.locator(".edit-title-btn");
    await expect(editTitle).toBeDisabled();
    await expect(editTitle).toHaveAttribute("title", rateLimitedReason);

    const closeButton = page.locator(".btn--close").first();
    await expect(closeButton).toBeEnabled();
  });

  test("repo summary New issue is disabled when create_issue is unavailable", async ({ page }) => {
    await mockApi(page);
    await page.route("**/api/v1/repos/summary", async (route) => {
      await route.fulfill({
        status: 200,
        contentType: "application/json",
        body: JSON.stringify([
          {
            owner: "acme",
            name: "widgets",
            platform_host: "github.com",
            repo: repoEnvelope({
              repo_owner: "acme",
              repo_name: "widgets",
              platform_host: "github.com",
            }),
            operations,
            cached_pr_count: 0,
            open_pr_count: 0,
            draft_pr_count: 0,
            cached_issue_count: 0,
            open_issue_count: 0,
            most_recent_activity_at: "2026-03-30T12:00:00Z",
            last_sync_completed_at: "2026-03-30T14:00:30Z",
            last_sync_started_at: "2026-03-30T14:00:00Z",
            last_sync_error: "",
            active_authors: [],
            recent_issues: [],
          },
        ]),
      });
    });

    await page.goto("/repos");
    const newIssue = page.getByRole("button", { name: "New issue" }).first();
    await expect(newIssue).toBeDisabled();
    await expect(newIssue).toHaveAttribute("title", MISSING_CREDENTIAL_REASON);
  });

  test("issue detail close, comment, and labels are disabled with the reason", async ({ page }) => {
    const mutationCalls: string[] = [];
    page.on("request", (request) => {
      if (request.method() === "GET") return;
      const path = new URL(request.url()).pathname;
      if (/^\/api\/v1\/issues\//.test(path) && !/\/sync(\/async)?$/.test(path)) {
        mutationCalls.push(`${request.method()} ${path}`);
      }
    });

    await mockApi(page);
    await page.route(`**/api/v1/issues/github/acme/widgets/${openIssue.Number}`, async (route) => {
      if (route.request().method() === "GET") {
        await route.fulfill({
          status: 200,
          contentType: "application/json",
          body: JSON.stringify(detailEnvelopeIssue(openIssue)),
        });
        return;
      }
      await route.fallback();
    });

    await page.goto(`/issues/github/acme/widgets/${openIssue.Number}`);
    await expect(page.locator(".detail-title")).toContainText(openIssue.Title);

    const closeButton = page.locator(".btn--close").first();
    await expect(closeButton).toBeDisabled();
    await expect(closeButton).toHaveAttribute("title", MISSING_CREDENTIAL_REASON);
    await closeButton.click({ force: true });

    const commentSubmit = page.locator(".comment-box .submit-btn");
    await expect(commentSubmit).toBeDisabled();
    await expect(page.locator(".comment-box .error-msg")).toContainText(MISSING_CREDENTIAL_REASON);

    const labelButton = page.locator(".btn--labels").first();
    await expect(labelButton).toBeDisabled();
    await expect(labelButton).toHaveAttribute("title", MISSING_CREDENTIAL_REASON);

    expect(mutationCalls).toEqual([]);
  });
});
