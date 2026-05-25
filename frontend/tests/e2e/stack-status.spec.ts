import { expect, test, type Page, type Route } from "@playwright/test";

const capabilities = {
  comment_mutation: true,
  issue_mutation: true,
  merge_mutation: true,
  read_ci: true,
  read_comments: true,
  read_issues: true,
  read_labels: false,
  read_merge_requests: true,
  read_releases: true,
  read_repositories: true,
  ready_for_review: true,
  review_mutation: true,
  state_mutation: true,
  workflow_approval: true,
};

function repoRef() {
  return {
    provider: "github",
    platform_host: "github.com",
    owner: "acme",
    name: "widgets",
    repo_path: "acme/widgets",
    capabilities,
  };
}

const checks = [
  {
    name: "frontend / svelte-check",
    status: "completed",
    conclusion: "failure",
    app: "GitHub Actions",
    url: "https://example.test/frontend",
  },
  {
    name: "e2e / chromium",
    status: "in_progress",
    conclusion: "",
    app: "GitHub Actions",
    url: "https://example.test/e2e",
  },
];

const pr = {
  ID: 102,
  RepoID: 1,
  GitHubID: 100102,
  PlatformID: 1,
  PlatformExternalID: "PR_kwDO_102",
  Number: 102,
  URL: "https://github.com/acme/widgets/pull/102",
  Title: "Add workspace session recovery",
  Author: "marius",
  AuthorDisplayName: "Marius",
  State: "open",
  IsDraft: false,
  IsLocked: false,
  Body: "Adds recovery behavior for interrupted workspace sessions.",
  HeadBranch: "feat/session-storage",
  BaseBranch: "main",
  HeadRepoCloneURL: "https://github.com/acme/widgets.git",
  Additions: 342,
  Deletions: 91,
  CommentCount: 3,
  ReviewDecision: "APPROVED",
  CIStatus: "pending",
  CIChecksJSON: JSON.stringify(checks),
  CIHadPending: true,
  CreatedAt: "2026-05-24T15:00:00Z",
  UpdatedAt: "2026-05-24T17:00:00Z",
  LastActivityAt: "2026-05-24T17:00:00Z",
  MergedAt: null,
  ClosedAt: null,
  MergeableState: "blocked",
  DetailFetchedAt: "2026-05-24T17:00:00Z",
  KanbanStatus: "reviewing",
  Starred: false,
  LabelsJSON: "[]",
  labels: [],
  repo_owner: "acme",
  repo_name: "widgets",
  platform_host: "github.com",
  repo: repoRef(),
  worktree_links: [],
};

function prForNumber(number: number) {
  const member = stackMembers.find((candidate) => candidate.number === number);
  return {
    ...pr,
    ID: number,
    GitHubID: 100000 + number,
    PlatformExternalID: `PR_kwDO_${number}`,
    Number: number,
    URL: `https://github.com/acme/widgets/pull/${number}`,
    Title: member ? member.title : pr.Title,
    HeadBranch: member?.base_branch === "main"
      ? "feat/base-schema"
      : member?.base_branch.replace("feat/", "feat/child-") ?? pr.HeadBranch,
    CIStatus: member?.ci_status ?? pr.CIStatus,
    ReviewDecision: member?.review_decision ?? pr.ReviewDecision,
  };
}

const stackMembers = [
  {
    number: 101,
    title: "base schema",
    state: "open",
    ci_status: "failure",
    review_decision: "APPROVED",
    position: 1,
    is_draft: false,
    base_branch: "main",
    blocked_by: null,
  },
  {
    number: 102,
    title: "session storage",
    state: "open",
    ci_status: "pending",
    review_decision: "APPROVED",
    position: 2,
    is_draft: false,
    base_branch: "feat/base-schema",
    blocked_by: 101,
  },
  {
    number: 103,
    title: "auth flow",
    state: "open",
    ci_status: "success",
    review_decision: "APPROVED",
    position: 3,
    is_draft: false,
    base_branch: "feat/session-storage",
    blocked_by: 101,
  },
  {
    number: 104,
    title: "cache API",
    state: "open",
    ci_status: "success",
    review_decision: "APPROVED",
    position: 4,
    is_draft: false,
    base_branch: "feat/auth-flow",
    blocked_by: 101,
  },
  {
    number: 105,
    title: "workspace logs",
    state: "open",
    ci_status: "success",
    review_decision: "APPROVED",
    position: 5,
    is_draft: false,
    base_branch: "feat/cache-api",
    blocked_by: 101,
  },
  {
    number: 106,
    title: "agent retries",
    state: "open",
    ci_status: "success",
    review_decision: "",
    position: 6,
    is_draft: false,
    base_branch: "feat/logs",
    blocked_by: 101,
  },
  {
    number: 107,
    title: "UI polish",
    state: "open",
    ci_status: "pending",
    review_decision: "",
    position: 7,
    is_draft: false,
    base_branch: "feat/agent-retries",
    blocked_by: 101,
  },
];

async function fulfillJson(route: Route, body: unknown, status = 200): Promise<void> {
  await route.fulfill({
    status,
    contentType: "application/json",
    body: JSON.stringify(body),
  });
}

async function mockStackedPR(
  page: Page,
  options: { stackResponseDelays?: Map<number, Promise<void>> } = {},
): Promise<void> {
  await page.route("**/api/v1/**", async (route) => {
    const url = new URL(route.request().url());
    const { pathname } = url;
    const method = route.request().method();

    if (method === "GET" && pathname === "/api/v1/pulls") {
      await fulfillJson(route, [pr]);
      return;
    }

    const detailMatch = pathname.match(
      /^\/api\/v1\/pulls\/github\/acme\/widgets\/(\d+)$/,
    );
    if (method === "GET" && detailMatch) {
      const number = Number(detailMatch[1]!);
      const detailPR = prForNumber(number);
      await fulfillJson(route, {
        merge_request: detailPR,
        repo: repoRef(),
        platform_host: "github.com",
        repo_owner: "acme",
        repo_name: "widgets",
        detail_loaded: true,
        detail_fetched_at: "2026-05-24T17:00:00Z",
        diff_head_sha: "head",
        merge_base_sha: "base",
        platform_base_sha: "base",
        platform_head_sha: "head",
        events: [],
        warnings: [],
        worktree_links: [],
        workflow_approval: {
          checked: true,
          required: false,
          count: 0,
          runs: [],
        },
      });
      return;
    }

    const stackMatch = pathname.match(
      /^\/api\/v1\/pulls\/github\/acme\/widgets\/(\d+)\/stack$/,
    );
    if (method === "GET" && stackMatch) {
      const number = Number(stackMatch[1]!);
      const member = stackMembers.find((candidate) => candidate.number === number);
      await options.stackResponseDelays?.get(number);
      await fulfillJson(route, {
        stack_id: 1,
        stack_name: "session-recovery",
        position: member?.position ?? 2,
        size: 7,
        health: "blocked",
        members: stackMembers,
      });
      return;
    }

    if (method === "GET" && pathname === "/api/v1/repo/github/acme/widgets") {
      await fulfillJson(route, {
        AllowSquashMerge: true,
        AllowMergeCommit: true,
        AllowRebaseMerge: true,
        ViewerCanMerge: true,
      });
      return;
    }

    if (method === "GET" && pathname === "/api/v1/workspaces") {
      await fulfillJson(route, []);
      return;
    }

    if (method === "GET" && pathname === "/api/v1/settings") {
      await fulfillJson(route, {
        repos: [{
          provider: "github",
          platform_host: "github.com",
          owner: "acme",
          name: "widgets",
          repo_path: "acme/widgets",
          is_glob: false,
          matched_repo_count: 1,
        }],
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
      });
      return;
    }

    if (method === "GET" && pathname === "/api/v1/sync/status") {
      await fulfillJson(route, {
        running: false,
        last_run_at: "2026-05-24T17:00:00Z",
        last_error: "",
      });
      return;
    }

    if (method === "GET" && pathname === "/api/v1/rate-limits") {
      await fulfillJson(route, { hosts: {} });
      return;
    }

    if (method === "GET" && pathname === "/api/v1/events") {
      await route.fulfill({
        status: 200,
        contentType: "text/event-stream",
        body: ":\n\n",
      });
      return;
    }

    if (method === "GET" && pathname === "/api/v1/activity") {
      await fulfillJson(route, []);
      return;
    }

    await fulfillJson(route, { error: `Unhandled ${method} ${pathname}` }, 404);
  });
}

test("stack status shares the PR detail expandable slot with CI", async ({ page }) => {
  await page.setViewportSize({ width: 892, height: 998 });
  await mockStackedPR(page);

  await page.goto("/pulls/github/acme/widgets/102");

  await page.getByTestId("ci-chip").click();
  await expect(page.getByText("frontend / svelte-check")).toBeVisible();

  await page.getByTestId("stack-chip").click();

  await expect(page.getByText("frontend / svelte-check")).toBeHidden();
  await expect(page.getByText("7 PRs · current 2/7 · downstack CI failure")).toBeVisible();
  await expect(page.getByText("blocked by #101")).toBeVisible();

  const currentRow = page.locator(".stack-row--current");
  const currentBadgesBox = await currentRow.locator(".stack-badges").boundingBox();
  const currentLinkBox = await currentRow.locator(".stack-member-link").boundingBox();
  expect(currentBadgesBox).not.toBeNull();
  expect(currentLinkBox).not.toBeNull();
  const badgeCenterY = currentBadgesBox!.y + currentBadgesBox!.height / 2;
  const linkCenterY = currentLinkBox!.y + currentLinkBox!.height / 2;
  expect(Math.abs(badgeCenterY - linkCenterY)).toBeLessThanOrEqual(4);
  await expect(page.locator(".stack-member-meta")).toHaveCount(0);
  await expect(page.locator(".stack-base-name")).toHaveText("main");
  await expect(page.locator(".stack-row--base .stack-member-link")).toHaveCount(0);

  const stackRows = page.locator(".stack-member-link");
  await expect(stackRows).toHaveText([
    "#107 UI polish",
    "#106 agent retries",
    "#105 workspace logs",
    "#104 cache API",
    "#103 auth flow",
    "#102 session storage",
    "#101 base schema",
  ]);

  await page.getByRole("button", { name: "#101 base schema" }).click();

  await expect(page).toHaveURL(/\/pulls\/github\/acme\/widgets\/101$/);
  await expect(page.getByText("7 PRs · current 1/7")).toBeVisible();
  await expect(page.locator(".stack-base-name")).toHaveText("main");
});

test("stack status stays rendered while navigating to a stack member", async ({ page }) => {
  let releaseStackResponse: () => void = () => {};
  const delayedStackResponse = new Promise<void>((resolve) => {
    releaseStackResponse = resolve;
  });
  await mockStackedPR(page, {
    stackResponseDelays: new Map([[101, delayedStackResponse]]),
  });

  await page.goto("/pulls/github/acme/widgets/102");
  await page.getByTestId("stack-chip").click();
  await expect(page.getByText("7 PRs · current 2/7 · downstack CI failure")).toBeVisible();

  await page.getByRole("button", { name: "#101 base schema" }).click();

  await expect(page).toHaveURL(/\/pulls\/github\/acme\/widgets\/101$/);
  await expect(page.getByTestId("stack-chip")).toBeVisible();
  await expect(page.getByText("7 PRs · current 1/7")).toBeVisible();
  releaseStackResponse();
  await expect(page.getByText("7 PRs · current 1/7")).toBeVisible();
});

test("stack member navigation preserves focus routes", async ({ page }) => {
  await mockStackedPR(page);

  await page.goto("/focus/pulls/github/acme/widgets/102");
  await page.getByTestId("stack-chip").click();
  await page.getByRole("button", { name: "#101 base schema" }).click();

  await expect(page).toHaveURL(/\/focus\/pulls\/github\/acme\/widgets\/101$/);
  await expect(page.getByText("7 PRs · current 1/7")).toBeVisible();
});

test("stack member navigation updates the activity drawer selection", async ({ page }) => {
  await mockStackedPR(page);

  await page.goto(
    "/?selected=pr:102&provider=github&platform_host=github.com&repo_path=acme%2Fwidgets",
  );
  await page.locator(".activity-detail").getByTestId("stack-chip").click();
  await page.getByRole("button", { name: "#101 base schema" }).click();

  await expect(page).toHaveURL(/selected=pr%3A101/);
  await expect(page).toHaveURL(/repo_path=acme%2Fwidgets/);
  await expect(page.locator(".activity-detail")).toContainText("acme/widgets#101");
  await expect(page.getByText("7 PRs · current 1/7")).toBeVisible();
});

test("stack rail spans wrapped CI badges at narrow widths", async ({ page }) => {
  await page.setViewportSize({ width: 319, height: 998 });
  await mockStackedPR(page);

  await page.goto("/pulls/github/acme/widgets/102");
  await page.getByTestId("stack-chip").click();

  const currentRow = page.locator(".stack-row--current");
  const currentRowBox = await currentRow.boundingBox();
  const currentDotBox = await currentRow.locator(".stack-dot--current").boundingBox();
  const currentLineBox = await currentRow.locator(".stack-line").boundingBox();
  const currentBadgesBox = await currentRow.locator(".stack-badges").boundingBox();
  expect(currentRowBox).not.toBeNull();
  expect(currentDotBox).not.toBeNull();
  expect(currentLineBox).not.toBeNull();
  expect(currentBadgesBox).not.toBeNull();
  const dotCenterY = currentDotBox!.y + currentDotBox!.height / 2;
  const rowCenterY = currentRowBox!.y + currentRowBox!.height / 2;
  expect(Math.abs(dotCenterY - rowCenterY)).toBeLessThanOrEqual(4);
  expect(currentLineBox!.y).toBeLessThanOrEqual(currentRowBox!.y + 1);
  expect(currentLineBox!.y + currentLineBox!.height).toBeGreaterThanOrEqual(
    currentBadgesBox!.y + currentBadgesBox!.height - 1,
  );
  const containerQueryEvidence = await page.evaluate(() => {
    function collectRules(ruleList: CSSRuleList): string[] {
      return Array.from(ruleList).flatMap((rule) => {
        const nested = "cssRules" in rule
          ? collectRules((rule as CSSGroupingRule).cssRules)
          : [];
        return [rule.cssText, ...nested];
      });
    }
    const rules = Array.from(document.styleSheets).flatMap((sheet) => {
      try {
        return collectRules(sheet.cssRules);
      } catch {
        return [];
      }
    });
    return {
      hasExpectedContainerRule: rules.some((rule) =>
        rule.includes("@container pull-detail")
          && rule.includes("max-width: 440px")
          && rule.includes(".stack-row")
      ),
      hasMalformedRule: rules.some((rule) =>
        rule.includes("@frontend/src/lib/stores/container.svelte.ts")
      ),
    };
  });
  expect(containerQueryEvidence).toEqual({
    hasExpectedContainerRule: true,
    hasMalformedRule: false,
  });
  const narrowStyles = await currentRow.evaluate((row) => {
    const badges = row.querySelector(".stack-badges");
    if (!badges) return null;
    const rowStyle = getComputedStyle(row);
    const badgeStyle = getComputedStyle(badges);
    return {
      rowGridColumns: rowStyle.gridTemplateColumns,
      badgesGridColumnStart: badgeStyle.gridColumnStart,
      badgesGridRowStart: badgeStyle.gridRowStart,
    };
  });
  expect(narrowStyles).not.toBeNull();
  expect(narrowStyles!.rowGridColumns.trim().split(/\s+/)).toHaveLength(2);
  expect(narrowStyles!.badgesGridColumnStart).toBe("2");
  expect(narrowStyles!.badgesGridRowStart).toBe("2");
  const railColors = await currentRow.evaluate((row) => {
    const line = row.querySelector(".stack-line");
    const panel = row.closest(".stack-panel");
    if (!line || !panel) return null;
    return {
      line: getComputedStyle(line).backgroundColor,
      panel: getComputedStyle(panel).backgroundColor,
    };
  });
  expect(railColors).not.toBeNull();
  expect(railColors!.line).not.toEqual(railColors!.panel);
});
