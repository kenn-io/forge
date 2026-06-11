import { expect, test } from "@playwright/test";

import { mockApi } from "./support/mockApi";

// E2E coverage for the PR-detail palette commands (`pr.approve`,
// `pr.ready`, `pr.approveWorkflows`). The merge palette command is
// intentionally not registered (the trigger lives in PullDetail.svelte's
// local component state).

test.beforeEach(async ({ page }) => {
  await mockApi(page);
});

test.describe("PR-detail palette commands", () => {
  test("Approve PR runs from the palette and triggers the approve flow", async ({ page }) => {
    await page.goto("/pulls/github/acme/widgets/42");

    // Capture the approve POST so we can assert the action wired
    // through the same closure the existing button uses.
    const approveRequest = page.waitForRequest(
      (req) =>
        req.method() === "POST" && /\/pulls\/github\/acme\/widgets\/42\/approve$/.test(new URL(req.url()).pathname),
    );

    await page.keyboard.press("Meta+K");
    await page.locator(".palette-input").fill("approve pr");
    await page.keyboard.press("Enter");

    // The approve must pin the head the detail view rendered so the
    // server can reject the action when the head moved after review.
    const request = await approveRequest;
    const body = request.postDataJSON() as { expected_head_sha?: string };
    expect(body.expected_head_sha).toBe("42aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa42");
  });

  test("Approve PR is absent from the palette when the PR is closed", async ({ page }) => {
    await page.route("**/api/v1/pulls/github/acme/widgets/55", async (route) => {
      if (route.request().method() !== "GET") {
        await route.fallback();
        return;
      }
      await route.fulfill({
        status: 200,
        contentType: "application/json",
        body: JSON.stringify({
          merge_request: {
            ID: 3,
            RepoID: 1,
            GitHubID: 301,
            Number: 55,
            URL: "https://github.com/acme/widgets/pull/55",
            Title: "Refactor theme system",
            Author: "luisa",
            State: "closed",
            IsDraft: false,
            Body: "Consolidates theme tokens.",
            HeadBranch: "refactor/theme",
            BaseBranch: "main",
            Additions: 80,
            Deletions: 40,
            CommentCount: 0,
            ReviewDecision: "",
            CIStatus: "pending",
            CIChecksJSON: "[]",
            CreatedAt: "2026-03-29T14:00:00Z",
            UpdatedAt: "2026-03-30T14:00:00Z",
            LastActivityAt: "2026-03-30T14:00:00Z",
            MergedAt: null,
            ClosedAt: "2026-03-30T14:00:00Z",
            KanbanStatus: "new",
            Starred: false,
            repo_owner: "acme",
            repo_name: "widgets",
            platform_host: "github.com",
            repo: {
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
            },
            worktree_links: [],
          },
          events: [],
          repo: {
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
          },
          repo_owner: "acme",
          repo_name: "widgets",
          platform_host: "github.com",
          detail_loaded: true,
          detail_fetched_at: "2026-03-30T14:00:00Z",
          worktree_links: [],
        }),
      });
    });
    await page.goto("/pulls/github/acme/widgets/55");

    await page.keyboard.press("Meta+K");
    await page.locator(".palette-input").fill("approve pr");

    // Palette rows render as <button class="palette-row">; query by name
    // against the actual role so a regression that surfaces the command
    // anyway would fail this assertion (the previous role="option" query
    // matched nothing regardless of palette state).
    await expect(page.getByRole("button", { name: /Approve PR/i })).toHaveCount(0);
  });

  test("Mark ready for review appears only when the PR is a draft", async ({ page }) => {
    await page.goto("/pulls/github/acme/widgets/42");
    await page.keyboard.press("Meta+K");
    await page.locator(".palette-input").fill("ready for review");
    // Non-draft PR; the action should be filtered out by `when`. Same
    // role-correctness note as the closed-PR test above.
    await expect(page.getByRole("button", { name: /Mark ready for review/i })).toHaveCount(0);
  });
});
