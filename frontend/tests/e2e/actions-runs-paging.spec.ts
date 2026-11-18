import { expect, test } from "@playwright/test";
import { mockApi, mockSettings } from "./support/mockApi";

const capabilities = {
  read_repositories: true,
  read_merge_requests: true,
  read_issues: true,
  read_issue_pr_references: true,
  read_comments: true,
  read_releases: true,
  read_ci: true,
  read_workflows: true,
  read_workflow_runs: true,
  workflow_dispatch: true,
  read_labels: true,
  read_markdown_images: true,
  read_markdown_media: true,
  read_authenticated_user: true,
  comment_mutation: true,
  state_mutation: true,
  merge_mutation: true,
  label_mutation: true,
  assignee_mutation: true,
  reviewer_mutation: true,
  review_mutation: true,
  workflow_approval: true,
  ready_for_review: true,
  draft_mutation: true,
  issue_mutation: true,
  review_draft_mutation: false,
  review_thread_resolution: false,
  review_suggestion_application: false,
  read_review_threads: false,
  native_multiline_ranges: false,
  mutation_head_binding: false,
  thread_reply: false,
  thread_resolve: false,
  supported_review_actions: [],
};

test("scrolls to the oldest workflow run", async ({ page }) => {
  await mockApi(page);
  const repo = {
    provider: "github",
    platform_host: "github.com",
    platform_repo_id: 1014,
    owner: "org-a",
    name: "project-a",
    repo_path: "org-a/project-a",
    default_branch: "main",
    capabilities,
    operations: { dispatch_workflow: { available: true } },
  };
  await page.route("**/api/v1/settings", async (route) =>
    route.fulfill({ json: { ...mockSettings, modes: { ...mockSettings.modes, actions: true } } }),
  );
  await page.route("**/api/v1/repos/summary", async (route) =>
    route.fulfill({
      json: [
        {
          owner: "org-a",
          name: "project-a",
          platform_host: "github.com",
          default_platform_host: "github.com",
          repo,
          operations: repo.operations,
          cached_pr_count: 0,
          open_pr_count: 0,
          draft_pr_count: 0,
          cached_issue_count: 0,
          open_issue_count: 0,
          active_authors: [],
          recent_issues: [],
          commit_timeline: [],
          releases: [],
        },
      ],
    }),
  );
  await page.route("**/api/v1/actions/github/org-a/project-a/workflows", async (route) =>
    route.fulfill({
      json: {
        repo,
        environments: [],
        workflows: [
          {
            id: "deploy.yml",
            name: "Deploy",
            path: ".github/workflows/deploy.yml",
            state: "active",
            available: true,
            definition_sha: "sha",
            inputs: [],
          },
        ],
      },
    }),
  );
  await page.route("**/api/v1/actions/github/org-a/project-a/runs**", async (route) => {
    const older = new URL(route.request().url()).searchParams.has("cursor");
    const items = Array.from({ length: older ? 1 : 50 }, (_, i) => ({
      actor: "user-a",
      conclusion: "success",
      created_at: "2026-08-27T12:30:00Z",
      event: "workflow_dispatch",
      head_sha: "0123456789abcdef",
      id: String(older ? 51 : i + 1),
      name: "Deploy",
      ref: "main",
      run_number: older ? 1 : 51 - i,
      status: "completed",
      workflow_id: "deploy.yml",
    }));
    await route.fulfill({ json: { repo, items, exhausted: older, ...(older ? {} : { next_cursor: "older" }) } });
  });
  await page.goto("/actions");
  await page.getByRole("button", { name: /Deploy/ }).click();
  const viewport = page.getByRole("region", { name: "Recent workflow runs" });
  await expect(async () => {
    await viewport.evaluate((node) => {
      node.scrollTop = node.scrollHeight;
    });
    await expect(page.getByRole("button", { name: "Run 1 Deploy" })).toBeVisible();
  }).toPass();
});
