import { expect, test, type Page } from "@playwright/test";

import { mockApi } from "./support/mockApi";

const REPO = {
  provider: "github",
  platform_host: "github.com",
  owner: "acme",
  name: "widgets",
  repo_path: "acme/widgets",
  capabilities: {},
};

function itemEvent(args: {
  id: string;
  number: number;
  type: "comment" | "review" | "new_pr";
  author: string;
  authorName: string;
  createdAt: string;
  title: string;
  itemAuthor?: string;
}) {
  return {
    id: args.id,
    cursor: args.id,
    activity_type: args.type,
    author: args.author,
    author_name: args.authorName,
    // The backend carries the parent PR/issue author on every row.
    item_author: args.itemAuthor ?? args.author,
    body_preview: "",
    created_at: args.createdAt,
    item_number: args.number,
    item_state: "open",
    item_title: args.title,
    item_type: "pr",
    item_url: `https://github.com/acme/widgets/pull/${args.number}`,
    platform_host: "github.com",
    repo_owner: "acme",
    repo_name: "widgets",
    repo: REPO,
  };
}

function branchCommit(args: {
  id: string;
  author: string;
  authorName: string;
  createdAt: string;
  bodyPreview: string;
  sha: string;
}) {
  return {
    id: args.id,
    cursor: args.id,
    activity_type: "default_branch_commit",
    author: args.author,
    author_name: args.authorName,
    body_preview: args.bodyPreview,
    created_at: args.createdAt,
    committed_at: args.createdAt,
    item_number: 0,
    item_state: "",
    item_title: "",
    item_type: "",
    item_url: "",
    activity_url: `https://github.com/acme/widgets/commit/${args.sha}`,
    platform_host: "github.com",
    repo_owner: "acme",
    repo_name: "widgets",
    repo: REPO,
    branch_name: "main",
    commit_sha: args.sha,
  };
}

async function mockSettings(page: Page): Promise<void> {
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
          view_mode: "threaded",
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
}

async function mockActivity(page: Page, items: unknown[]): Promise<void> {
  await page.route("**/api/v1/activity**", async (route) => {
    await route.fulfill({
      status: 200,
      contentType: "application/json",
      body: JSON.stringify({ capped: false, items }),
    });
  });
}

test.describe("threaded activity columns", () => {
  test("item row author column shows the PR author, not the latest actor", async ({ page }) => {
    await mockSettings(page);
    // Two events on the same PR opened by "alice": her opening event and a
    // newer review by "bob". The threaded row attributes the item to its
    // author (alice), not the most recent actor (bob).
    await mockActivity(page, [
      itemEvent({
        id: "evt-new",
        number: 42,
        type: "review",
        author: "bob",
        authorName: "Bob Reviewer",
        itemAuthor: "alice",
        createdAt: "2026-04-27T13:00:00Z",
        title: "Add browser regression coverage",
      }),
      itemEvent({
        id: "evt-old",
        number: 42,
        type: "new_pr",
        author: "alice",
        authorName: "Alice Author",
        itemAuthor: "alice",
        createdAt: "2026-04-27T12:00:00Z",
        title: "Add browser regression coverage",
      }),
    ]);
    await page.goto("/?view=threaded");

    const row = page.locator(".item-row:not(.branch-activity-row)").first();
    await expect(row).toBeVisible();
    const authorCell = row.locator(".cell--author");
    await expect(authorCell).toHaveText("alice");
  });

  test("branch commit row shows the commit author in the author column", async ({ page }) => {
    await mockSettings(page);
    await mockActivity(page, [
      branchCommit({
        id: "cmt-1",
        author: "carol",
        authorName: "Carol Committer",
        createdAt: "2026-04-27T12:00:00Z",
        bodyPreview: "Refresh cache warmer",
        sha: "abcdef0123456789abcdef0123456789abcdef01",
      }),
    ]);
    await page.goto("/?view=threaded");

    const row = page.locator(".branch-activity-row").first();
    await expect(row).toBeVisible();
    await expect(row.locator(".cell--author")).toHaveText("Carol Committer");
    await expect(row.locator(".cell--type")).toHaveText("Commit");
  });

  test("hide-org toggle swaps the repo chip between owner/name and bare name", async ({ page }) => {
    await mockSettings(page);
    await mockActivity(page, [
      itemEvent({
        id: "evt-only",
        number: 7,
        type: "comment",
        author: "alice",
        authorName: "Alice",
        createdAt: "2026-04-27T12:00:00Z",
        title: "Comment-only PR",
      }),
    ]);
    // Start in ungrouped mode so the repo chip is rendered next to each row.
    await page.goto("/?view=threaded");
    await page.evaluate(() => {
      localStorage.setItem("middleman:groupingMode", "flat");
      localStorage.removeItem("middleman:hideOrgName");
    });
    await page.reload();

    const repoLabel = page.locator(".item-row .repo-chip__label").first();
    await expect(repoLabel).toHaveText("acme/widgets");

    // Toggle "Hide org name" via the View dropdown.
    await page.getByRole("button", { name: "View", exact: true }).click();
    await page.locator(".filter-item", { hasText: "Hide org name" }).click();
    await page.keyboard.press("Escape");

    await expect(repoLabel).toHaveText("widgets");

    // Reload to verify the toggle persists via localStorage.
    await page.reload();
    await expect(page.locator(".item-row .repo-chip__label").first()).toHaveText("widgets");
  });
});
