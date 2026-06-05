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

function prEvent(args: {
  id: string;
  type: "comment" | "review" | "commit" | "force_push" | "new_pr";
  author: string;
  createdAt: string;
}): unknown {
  return {
    id: args.id,
    cursor: args.id,
    activity_type: args.type,
    author: args.author,
    body_preview: "",
    created_at: args.createdAt,
    item_number: 42,
    item_state: "open",
    item_title: "Add browser regression coverage",
    item_type: "pr",
    item_url: "https://github.com/acme/widgets/pull/42",
    platform_host: "github.com",
    repo_owner: "acme",
    repo_name: "widgets",
    repo: REPO,
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
          collapse_threads: false,
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

test.describe("threaded activity run collapse", () => {
  test("collapses a run of three or more reviews from the same author", async ({ page }) => {
    await mockSettings(page);
    await mockActivity(page, [
      prEvent({
        id: "r5",
        type: "review",
        author: "alice",
        createdAt: "2026-04-27T15:00:00Z",
      }),
      prEvent({
        id: "r4",
        type: "review",
        author: "alice",
        createdAt: "2026-04-27T14:00:00Z",
      }),
      prEvent({
        id: "r3",
        type: "review",
        author: "alice",
        createdAt: "2026-04-27T13:00:00Z",
      }),
      prEvent({
        id: "r2",
        type: "review",
        author: "alice",
        createdAt: "2026-04-27T12:00:00Z",
      }),
      prEvent({
        id: "r1",
        type: "review",
        author: "alice",
        createdAt: "2026-04-27T11:00:00Z",
      }),
    ]);
    await page.goto("/?view=threaded");

    const collapsed = page.locator(".event-row.collapsed-event", {
      hasText: "5 reviews",
    });
    await expect(collapsed).toHaveCount(1);
    await expect(collapsed.locator(".evt-review")).toBeVisible();
    await expect(page.locator(".event-row:not(.collapsed-event)")).toHaveCount(0);
  });

  test("collapses comments and reviews into separate runs by event type", async ({ page }) => {
    await mockSettings(page);
    await mockActivity(page, [
      prEvent({
        id: "c3",
        type: "comment",
        author: "alice",
        createdAt: "2026-04-27T16:00:00Z",
      }),
      prEvent({
        id: "c2",
        type: "comment",
        author: "alice",
        createdAt: "2026-04-27T15:00:00Z",
      }),
      prEvent({
        id: "c1",
        type: "comment",
        author: "alice",
        createdAt: "2026-04-27T14:00:00Z",
      }),
      prEvent({
        id: "r3",
        type: "review",
        author: "alice",
        createdAt: "2026-04-27T13:00:00Z",
      }),
      prEvent({
        id: "r2",
        type: "review",
        author: "alice",
        createdAt: "2026-04-27T12:00:00Z",
      }),
      prEvent({
        id: "r1",
        type: "review",
        author: "alice",
        createdAt: "2026-04-27T11:00:00Z",
      }),
    ]);
    await page.goto("/?view=threaded");

    await expect(
      page.locator(".event-row.collapsed-event", {
        hasText: "3 comments",
      }),
    ).toHaveCount(1);
    await expect(
      page.locator(".event-row.collapsed-event", {
        hasText: "3 reviews",
      }),
    ).toHaveCount(1);
  });

  test("leaves short runs of comments unrolled", async ({ page }) => {
    await mockSettings(page);
    await mockActivity(page, [
      prEvent({
        id: "c2",
        type: "comment",
        author: "alice",
        createdAt: "2026-04-27T13:00:00Z",
      }),
      prEvent({
        id: "c1",
        type: "comment",
        author: "alice",
        createdAt: "2026-04-27T12:00:00Z",
      }),
    ]);
    await page.goto("/?view=threaded");

    await expect(page.locator(".event-row.collapsed-event")).toHaveCount(0);
    await expect(page.locator(".event-row:not(.collapsed-event)")).toHaveCount(2);
  });
});
