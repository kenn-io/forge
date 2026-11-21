import { expect, test, type Page } from "@playwright/test";

import { mockApi, mockSettings as defaultSettings } from "./support/mockApi";

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
        ...defaultSettings,
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
          ...defaultSettings.activity,
          view_mode: "threaded",
          collapse_threads: true,
        },
        terminal: {
          ...defaultSettings.terminal,
          font_size: 14,
        },
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
    await page.addInitScript(() => {
      localStorage.setItem("kenn-forge:groupingMode", "flat");
      localStorage.removeItem("kenn-forge:hideOrgName");
    });
    await page.goto("/?view=threaded");

    const repoLabel = page.locator(".item-row .repo-chip__label").first();
    await expect(repoLabel).toHaveText("acme/widgets");

    // Toggle "Hide org name" via Filters.
    await page.getByRole("button", { name: "Filters", exact: true }).click();
    await page.locator(".kit-filter-dropdown__item", { hasText: "Hide org name" }).click();
    await page.keyboard.press("Escape");

    await expect(repoLabel).toHaveText("widgets");
  });

  test("the rail beside an open item keeps each row on one line under column headers", async ({ page }) => {
    await page.setViewportSize({ width: 1440, height: 900 });
    await mockSettings(page);
    const title = "Improve cache refresh across repositories and background tasks";
    await mockActivity(page, [
      itemEvent({
        id: "evt-long",
        number: 1200,
        type: "new_pr",
        author: "user-a",
        authorName: "User A",
        createdAt: "2026-04-27T12:00:00Z",
        title,
      }),
    ]);
    await page.addInitScript(() => {
      localStorage.setItem("kenn-forge:groupingMode", "flat");
      localStorage.setItem("kenn-forge-activity-pane-width", "615");
    });
    await page.goto("/?view=threaded");

    // Opening an item turns the list into a rail beside it.
    const row = page.locator(".item-row").filter({ hasText: title });
    await row.click();
    const view = page.locator(".threaded-view");
    await expect.poll(async () => Math.round((await view.boundingBox())!.width)).toBe(615);
    await page.evaluate(() => document.fonts.ready);

    await expect(page.locator(".item-table-headers")).toBeVisible();
    const titleBox = (await row.locator(".cell--title").boundingBox())!;
    const titleMid = titleBox.y + titleBox.height / 2;
    for (const cell of [".cell--type", ".cell--repo", ".cell--author", ".cell--time", ".cell--link"]) {
      const box = (await row.locator(cell).boundingBox())!;
      expect(box.y, cell).toBeLessThan(titleMid);
      expect(box.y + box.height, cell).toBeGreaterThan(titleMid);
    }
    // Repo and Author keep their full text; the title is the cell that gives way.
    await expect(row.locator(".repo-chip__label")).toHaveText("acme/widgets");
    expect(await row.locator(".repo-chip__label").evaluate((el) => el.scrollWidth > el.clientWidth)).toBe(false);
    expect(await row.locator(".cell--author").evaluate((el) => el.scrollWidth > el.clientWidth)).toBe(false);
  });

  for (const width of [320, 360]) {
    test(`flat Activity at ${width}px keeps the time and link inside the window`, async ({ page }) => {
      await page.setViewportSize({ width, height: 800 });
      await mockApi(page);
      await page.addInitScript(() => localStorage.setItem("kenn-forge:groupingMode", "flat"));
      await page.goto("/?desktop=1&view=flat");
      const table = page.locator(".activity-table");
      await expect(table.locator(".cell--time").first()).toBeVisible();
      for (const cell of [".cell--time", ".cell--link"]) {
        const box = (await table.locator(cell).first().boundingBox())!;
        expect(box.x + box.width, cell).toBeLessThanOrEqual(width);
      }
      expect(await table.evaluate((el) => el.scrollWidth <= el.clientWidth)).toBe(true);
    });
  }
});
