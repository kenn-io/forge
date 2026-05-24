import { devices, expect, test, type Page } from "@playwright/test";

import { mockApi } from "./support/mockApi";

declare global {
  interface Window {
    __middlemanOpenedURL: string;
  }
}

const repo = {
  provider: "github",
  platform_host: "github.com",
  owner: "acme",
  name: "widgets",
  repo_path: "acme/widgets",
  capabilities: {},
};

function branchCommit(
  id: string,
  sha: string,
  subject: string,
  createdAt: string,
): unknown {
  return {
    id,
    cursor: id,
    activity_type: "default_branch_commit",
    activity_url: `https://github.com/acme/widgets/commit/${sha}`,
    author: "alice",
    author_name: "Alice",
    author_email: "alice@example.com",
    body_preview: subject,
    branch_name: "main",
    commit_sha: sha,
    committed_at: createdAt,
    created_at: createdAt,
    item_number: 0,
    item_state: "",
    item_title: "",
    item_type: "",
    item_url: "",
    platform_host: "github.com",
    repo_owner: "acme",
    repo_name: "widgets",
    repo,
  };
}

function branchForcePush(createdAt: string): unknown {
  return {
    id: "force-1",
    cursor: "force-1",
    activity_type: "default_branch_force_push",
    activity_url:
      "https://github.com/acme/widgets/compare/aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa...bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
    after_sha: "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
    author: "middleman",
    before_sha: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
    body_preview:
      "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa -> bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
    branch_name: "main",
    commit_sha: "",
    created_at: createdAt,
    item_number: 0,
    item_state: "",
    item_title: "",
    item_type: "",
    item_url: "",
    platform_host: "github.com",
    repo_owner: "acme",
    repo_name: "widgets",
    repo,
  };
}

function prComment(): unknown {
  return {
    id: "pr-1",
    cursor: "pr-1",
    activity_type: "comment",
    author: "marius",
    body_preview: "Looks good",
    created_at: "2026-03-30T14:02:30Z",
    item_number: 42,
    item_state: "open",
    item_title: "Add browser regression coverage",
    item_type: "pr",
    item_url: "https://github.com/acme/widgets/pull/42",
    platform_host: "github.com",
    repo_owner: "acme",
    repo_name: "widgets",
    repo,
  };
}

const activityItems = [
  branchForcePush("2026-03-30T14:06:00Z"),
  branchCommit(
    "commit-5",
    "5555555555555555555555555555555555555555",
    "Ship direct main commit 5",
    "2026-03-30T14:05:00Z",
  ),
  branchCommit(
    "commit-4",
    "4444444444444444444444444444444444444444",
    "Ship direct main commit 4",
    "2026-03-30T14:04:00Z",
  ),
  branchCommit(
    "commit-3",
    "3333333333333333333333333333333333333333",
    "Ship direct main commit 3",
    "2026-03-30T14:03:00Z",
  ),
  prComment(),
  branchCommit(
    "commit-2",
    "2222222222222222222222222222222222222222",
    "Ship direct main commit 2",
    "2026-03-30T14:02:00Z",
  ),
  branchCommit(
    "commit-1",
    "1111111111111111111111111111111111111111",
    "Ship direct main commit 1",
    "2026-03-30T14:01:00Z",
  ),
];

async function mockDefaultBranchActivity(page: Page): Promise<void> {
  await page.addInitScript(() => {
    Object.defineProperty(window, "__middlemanOpenedURL", {
      configurable: true,
      value: "",
      writable: true,
    });
    window.open = (url?: string | URL) => {
      window.__middlemanOpenedURL = String(url ?? "");
      return null;
    };
  });

  await mockApi(page);
  await page.route("**/api/v1/settings", async (route) => {
    await route.fulfill({
      status: 200,
      contentType: "application/json",
      body: JSON.stringify({
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
          view_mode: "flat",
          time_range: "7d",
          hide_closed: false,
          hide_bots: false,
          collapse_threads: true,
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
      }),
    });
  });
  await page.route("**/api/v1/activity**", async (route) => {
    await route.fulfill({
      status: 200,
      contentType: "application/json",
      body: JSON.stringify({
        capped: false,
        items: activityItems,
      }),
    });
  });
  await page.route(
    "**/api/v1/repo/github/acme/widgets/commits/*/diff**",
    async (route) => {
      await route.fulfill({
        status: 200,
        contentType: "application/json",
        body: JSON.stringify({
          stale: false,
          whitespace_only_count: 0,
          files: [
            {
              path: "src/direct-main.ts",
              old_path: "src/direct-main.ts",
              status: "modified",
              is_binary: false,
              is_whitespace_only: false,
              additions: 1,
              deletions: 0,
              hunks: [
                {
                  old_start: 1,
                  old_count: 1,
                  new_start: 1,
                  new_count: 2,
                  lines: [
                    {
                      type: "context",
                      content: "export const existing = true;",
                      old_num: 1,
                      new_num: 1,
                    },
                    {
                      type: "add",
                      content: "export const directMain = true;",
                      new_num: 2,
                    },
                  ],
                },
              ],
            },
          ],
        }),
      });
    },
  );
}

async function selectActivityFilterItem(
  page: Page,
  label: string,
): Promise<void> {
  await page.locator(".activity-feed .filter-btn", { hasText: "View" }).click();
  await page.locator(".activity-feed .filter-dropdown").waitFor({
    state: "visible",
  });
  await page.locator(".activity-feed .filter-item", { hasText: label }).click();
}

test.describe("default branch activity", () => {
  test("renders in the flat feed and can be hidden", async ({ page }) => {
    await mockDefaultBranchActivity(page);
    await page.goto("/?view=flat");

    const branchRows = page.locator(".activity-row", { hasText: "Branch" });
    await expect(branchRows).toHaveCount(6);
    await expect(
      page.locator(".activity-row", {
        hasText: "Ship direct main commit 1",
      }),
    ).toBeVisible();
    await expect(
      page.locator(".activity-row", { hasText: "aaaaaaa -> bbbbbbb" }),
    ).toBeVisible();
    await expect(page.locator(".activity-row", { hasText: "#0" })).toHaveCount(0);

    await selectActivityFilterItem(page, "Hide default-branch activity");

    await expect(branchRows).toHaveCount(0);
    await expect(
      page.locator(".activity-row", {
        hasText: "Add browser regression coverage",
      }),
    ).toBeVisible();
  });

  test("renders branch activity as threaded top-level rows", async ({ page }) => {
    await mockDefaultBranchActivity(page);
    await page.goto("/?view=threaded");

    await expect(
      page.locator(".item-row", {
        hasText: "main updates on acme/widgets",
      }),
    ).toHaveCount(0);
    await expect(
      page.locator(".item-row", {
        hasText: "3 commits",
      }),
    ).toBeVisible();
    await expect(
      page.locator(".item-row", {
        hasText: "Add browser regression coverage",
      }),
    ).toBeVisible();
    await expect(
      page.locator(".item-row", {
        hasText: "Ship direct main commit 2",
      }),
    ).toBeVisible();
    await expect(page.locator(".item-row", { hasText: "#0" })).toHaveCount(0);
    await expect(
      page.locator(".event-row.collapsed-event", { hasText: "3 commits" }),
    ).toHaveCount(0);

    const forcePushRow = page.locator(".branch-activity-row", {
      hasText: "Force-pushed",
    });
    await expect(forcePushRow).toBeVisible();
    await forcePushRow.click();
    await expect(page.locator(".activity-detail")).toHaveCount(0);
    await expect
      .poll(() =>
        page.evaluate(() => window.__middlemanOpenedURL),
      )
      .toContain("github.com/acme/widgets/compare");
  });

  test("commit rows open an in-app diff", async ({ page }) => {
    await mockDefaultBranchActivity(page);
    await page.goto("/?view=threaded");

    const commitRow = page.locator(".branch-activity-row", {
      hasText: "Ship direct main commit 2",
    });
    await expect(commitRow).toBeVisible();

    await commitRow.click();
    await expect(page.locator(".activity-detail")).toBeVisible();
    await expect(commitRow).toHaveClass(/selected/);
    await expect(page.locator(".activity-detail-header")).toContainText(
      "Commit acme/widgets main Ship direct main commit 2",
    );
    await expect(page.locator(".files-sidebar")).toContainText(
      "direct-main.ts",
    );
    await expect(page.locator(".file-header")).toContainText(
      "src/direct-main.ts",
    );
    await expect(page.locator(".diff-line", {
      hasText: "export const directMain = true;",
    })).toBeVisible();
    await expect
      .poll(() =>
        page.evaluate(() => window.__middlemanOpenedURL),
      )
      .toBe("");
  });
});

test.describe("mobile default branch activity", () => {
  test.use({
    deviceScaleFactor: devices["iPhone 13"].deviceScaleFactor,
    hasTouch: devices["iPhone 13"].hasTouch,
    isMobile: devices["iPhone 13"].isMobile,
    userAgent: devices["iPhone 13"].userAgent,
    viewport: devices["iPhone 13"].viewport,
  });

  test("renders branch cards and opens nested branch events externally", async ({
    page,
  }) => {
    await mockDefaultBranchActivity(page);
    await page.goto("/m?range=7d");

    const branchCard = page.locator(".mobile-activity-card", {
      hasText: "main",
    });
    await expect(branchCard).toBeVisible();
    await expect(branchCard).toContainText("Branch");
    await expect(branchCard).toContainText("6 events");
    await expect(page.locator(".mobile-activity-card", { hasText: "#0" }))
      .toHaveCount(0);

    await branchCard.locator(".mobile-activity-event", {
      hasText: "Force-pushed",
    }).click();

    await expect(page).toHaveURL(/\/m/);
    await expect
      .poll(() =>
        page.evaluate(() => window.__middlemanOpenedURL),
      )
      .toContain("github.com/acme/widgets/compare");
  });
});
