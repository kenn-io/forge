import type { Page, Route } from "@playwright/test";

const defaultProviderCapabilities = {
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
  review_draft_mutation: false,
  review_thread_resolution: false,
  read_review_threads: false,
  native_multiline_ranges: false,
  supported_review_actions: [],
};

function repoRef(owner: string, name: string, platformHost: string) {
  return {
    provider: "github",
    platform_host: platformHost,
    repo_path: `${owner}/${name}`,
    owner,
    name,
    capabilities: defaultProviderCapabilities,
  };
}

const pulls = [
  {
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
    repo: repoRef("acme", "widgets", "github.com"),
    worktree_links: [],
  },
  {
    ID: 2,
    RepoID: 2,
    GitHubID: 201,
    Number: 84,
    URL: "https://example.com/acme/widgets/pull/84",
    Title: "Mirror host stub PR",
    Author: "marius",
    State: "open",
    IsDraft: false,
    Body: "",
    HeadBranch: "mirror/stub",
    BaseBranch: "main",
    Additions: 1,
    Deletions: 0,
    CommentCount: 0,
    ReviewDecision: "",
    CIStatus: "success",
    CIChecksJSON: "[]",
    CreatedAt: "2026-03-29T14:00:00Z",
    UpdatedAt: "2026-03-30T14:00:00Z",
    LastActivityAt: "2026-03-30T14:00:00Z",
    MergedAt: null,
    ClosedAt: null,
    KanbanStatus: "new",
    Starred: false,
    repo_owner: "acme",
    repo_name: "widgets",
    platform_host: "example.com",
    repo: repoRef("acme", "widgets", "example.com"),
    worktree_links: [],
  },
  {
    ID: 3,
    RepoID: 1,
    GitHubID: 301,
    Number: 55,
    URL: "https://github.com/acme/widgets/pull/55",
    Title: "Refactor theme system",
    Author: "luisa",
    State: "open",
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
    ClosedAt: null,
    KanbanStatus: "new",
    Starred: false,
    repo_owner: "acme",
    repo_name: "widgets",
    platform_host: "github.com",
    repo: repoRef("acme", "widgets", "github.com"),
    worktree_links: [
      {
        worktree_key: "projects/theme-rework",
        worktree_branch: "refactor/theme",
      },
    ],
  },
];

const issues = [
  {
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
    repo: repoRef("acme", "widgets", "github.com"),
  },
];

const repos = [
  {
    ID: 1,
    Owner: "acme",
    Name: "widgets",
    AllowSquashMerge: true,
    AllowMergeCommit: true,
    AllowRebaseMerge: true,
    LastSyncStartedAt: "2026-03-30T14:00:00Z",
    LastSyncCompletedAt: "2026-03-30T14:00:30Z",
    LastSyncError: "",
    CreatedAt: "2026-03-01T00:00:00Z",
  },
];

const syncStatus = {
  running: false,
  last_run_at: "2026-03-30T14:00:30Z",
  last_error: "",
};

const settings = {
  repos: [
    {
      provider: "github",
      platform_host: "github.com",
      owner: "acme",
      name: "widgets",
      repo_path: "acme/widgets",
      is_glob: false,
      matched_repo_count: 1,
    },
  ],
  activity: {
    view_mode: "threaded",
    time_range: "7d",
    hide_closed: false,
    hide_bots: false,
  },
  terminal: {
    font_family: "",
    font_size: 14,
    scrollback: 1000,
    line_height: 1,
    letter_spacing: 0,
    cursor_blink: true,
    font_ligatures: false,
    renderer: "xterm",
  },
  agents: [],
};

function makeRateLimits() {
  const now = Date.now();
  return {
    hosts: {
      "github.com": {
        requests_hour: 188,
        rate_remaining: 4812,
        rate_limit: 5000,
        rate_reset_at: new Date(now + 42 * 60_000).toISOString(),
        hour_start: new Date(now - 18 * 60_000).toISOString(),
        sync_throttle_factor: 1,
        sync_paused: false,
        reserve_buffer: 200,
        known: true,
        budget_limit: 500,
        budget_spent: 42,
        budget_remaining: 458,
        gql_remaining: 4950,
        gql_limit: 5000,
        gql_reset_at: new Date(now + 38 * 60_000).toISOString(),
        gql_known: true,
      },
    },
  };
}

async function fulfillJson(
  route: Route,
  body: unknown,
  status = 200,
): Promise<void> {
  await route.fulfill({
    status,
    contentType: "application/json",
    body: JSON.stringify(body),
  });
}

export async function mockApi(page: Page): Promise<void> {
  // Deep-clone so mutations (e.g. PATCH) don't leak between tests.
  const localPulls: typeof pulls = JSON.parse(JSON.stringify(pulls));

  await page.route("**/api/v1/**", async (route) => {
    const url = new URL(route.request().url());
    const { pathname } = url;
    const method = route.request().method();

    if (method === "GET" && pathname === "/api/v1/pulls") {
      await fulfillJson(route, localPulls);
      return;
    }

    const singlePrMatch = pathname.match(
      /^\/api\/v1\/repos\/([^/]+)\/([^/]+)\/pulls\/(\d+)$/,
    );
    if (method === "GET" && singlePrMatch) {
      const prOwner = singlePrMatch[1];
      const prName = singlePrMatch[2];
      const prNumber = parseInt(singlePrMatch[3]!, 10);
      const pr = localPulls.find(
        (p) =>
          p.repo_owner === prOwner &&
          p.repo_name === prName &&
          p.Number === prNumber,
      );
      if (pr) {
        await fulfillJson(route, {
          merge_request: pr,
          repo_owner: pr.repo_owner,
          repo_name: pr.repo_name,
          detail_loaded: true,
          detail_fetched_at: "2026-03-30T14:00:00Z",
          worktree_links: pr.worktree_links,
        });
      } else {
        await fulfillJson(route, { error: "Not found" }, 404);
      }
      return;
    }

    if (method === "GET" && pathname === "/api/v1/issues") {
      await fulfillJson(route, issues);
      return;
    }

    const singleIssueMatch = pathname.match(
      /^\/api\/v1\/repos\/([^/]+)\/([^/]+)\/issues\/(\d+)$/,
    );
    if (method === "GET" && singleIssueMatch) {
      const issueOwner = singleIssueMatch[1];
      const issueName = singleIssueMatch[2];
      const issueNumber = parseInt(singleIssueMatch[3]!, 10);
      const issue = issues.find(
        (candidate) =>
          candidate.repo_owner === issueOwner &&
          candidate.repo_name === issueName &&
          candidate.Number === issueNumber,
      );
      if (!issue) {
        await fulfillJson(route, { title: "Not found" }, 404);
        return;
      }
      await fulfillJson(route, {
        issue,
        events: [],
        platform_host: issue.platform_host,
        repo_owner: issue.repo_owner,
        repo_name: issue.repo_name,
        detail_loaded: true,
        detail_fetched_at: "2026-03-30T14:00:00Z",
      });
      return;
    }

    const syncIssueMatch = pathname.match(
      /^\/api\/v1\/repos\/([^/]+)\/([^/]+)\/issues\/(\d+)\/sync$/,
    );
    if (method === "POST" && syncIssueMatch) {
      const issueOwner = syncIssueMatch[1];
      const issueName = syncIssueMatch[2];
      const issueNumber = parseInt(syncIssueMatch[3]!, 10);
      const issue = issues.find(
        (candidate) =>
          candidate.repo_owner === issueOwner &&
          candidate.repo_name === issueName &&
          candidate.Number === issueNumber,
      );
      if (!issue) {
        await fulfillJson(route, { title: "Not found" }, 404);
        return;
      }
      await fulfillJson(route, {
        issue,
        events: [],
        platform_host: issue.platform_host,
        repo_owner: issue.repo_owner,
        repo_name: issue.repo_name,
        detail_loaded: true,
        detail_fetched_at: "2026-03-30T14:00:00Z",
      });
      return;
    }

    if (method === "GET" && pathname === "/api/v1/repos") {
      await fulfillJson(route, repos);
      return;
    }

    const singleRepoMatch = pathname.match(
      /^\/api\/v1\/repos\/([^/]+)\/([^/]+)$/,
    );
    if (method === "GET" && singleRepoMatch) {
      const repo = repos.find(
        (r) => r.Owner === singleRepoMatch[1] && r.Name === singleRepoMatch[2],
      );
      if (!repo) {
        await fulfillJson(route, { error: "Not found" }, 404);
        return;
      }
      await fulfillJson(route, repo);
      return;
    }

    if (method === "GET" && pathname === "/api/v1/sync/status") {
      await fulfillJson(route, syncStatus);
      return;
    }

    if (method === "GET" && pathname === "/api/v1/settings") {
      await fulfillJson(route, settings);
      return;
    }

    if (method === "GET" && pathname === "/api/v1/events") {
      // Server-Sent Events endpoint. Tests don't need live data over the
      // wire, so reply with an empty SSE stream that keeps the connection
      // open just long enough for the page to settle. EventSource fires an
      // `error` event when the response closes; the events store treats
      // that as "disconnected" and the rest of the app keeps working.
      await route.fulfill({
        status: 200,
        contentType: "text/event-stream",
        body: ":\n\n",
      });
      return;
    }

    if (method === "GET" && pathname === "/api/v1/rate-limits") {
      await fulfillJson(route, makeRateLimits());
      return;
    }

    if (method === "POST" && pathname === "/api/v1/sync") {
      await fulfillJson(route, undefined, 202);
      return;
    }

    const patchPrMatch = pathname.match(
      /^\/api\/v1\/repos\/([^/]+)\/([^/]+)\/pulls\/(\d+)$/,
    );
    if (method === "PATCH" && patchPrMatch) {
      const prOwner = patchPrMatch[1];
      const prName = patchPrMatch[2];
      const prNumber = parseInt(patchPrMatch[3]!, 10);
      const pr = localPulls.find(
        (p) =>
          p.repo_owner === prOwner &&
          p.repo_name === prName &&
          p.Number === prNumber,
      );
      if (!pr) {
        await fulfillJson(route, { title: "Not found" }, 404);
        return;
      }
      const reqBody = JSON.parse((await route.request().postData()) ?? "{}");
      if (reqBody.title !== undefined) pr.Title = reqBody.title;
      if (reqBody.body !== undefined) pr.Body = reqBody.body;
      await fulfillJson(route, {
        merge_request: pr,
        repo_owner: pr.repo_owner,
        repo_name: pr.repo_name,
        detail_loaded: true,
        detail_fetched_at: "2026-03-30T14:00:00Z",
        worktree_links: pr.worktree_links,
      });
      return;
    }

    await fulfillJson(route, { error: `Unhandled ${method} ${pathname}` }, 404);
  });
}
