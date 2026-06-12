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
  add_label: unavailable,
  remove_label: unavailable,
  close_issue: unavailable,
  reopen_issue: unavailable,
  approve_workflow: unavailable,
};

const providerCapabilities = {
  comment_mutation: true,
  issue_mutation: true,
  merge_mutation: true,
  read_ci: true,
  read_comments: true,
  read_issues: true,
  read_merge_requests: true,
  read_releases: true,
  read_repositories: true,
  ready_for_review: true,
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

function detailEnvelopePR(pr: typeof draftPR): unknown {
  return {
    merge_request: pr,
    repo: repoEnvelope(pr),
    repo_owner: pr.repo_owner,
    repo_name: pr.repo_name,
    detail_loaded: true,
    detail_fetched_at: "2026-04-01T12:00:00Z",
    worktree_links: pr.worktree_links,
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
  test("ready-for-review and close are disabled with the reason and never hit the API", async ({ page }) => {
    const mutationCalls: string[] = [];
    page.on("request", (request) => {
      if (request.method() === "GET") return;
      const path = new URL(request.url()).pathname;
      if (/\/pulls\/\d+\/(ready-for-review|github-state)$/.test(path)) {
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

    expect(mutationCalls).toEqual([]);
  });
});
