import { expect, test, type Page } from "@playwright/test";

import { mockApi, mockSettings as defaultSettings } from "./support/mockApi";

// Browser-only remainder of the activity-collapse coverage: when the side
// detail pane opens, the feed switches to compact mode and the Collapse all
// control becomes icon-only via `.activity-feed--compact .collapse-all-label
// { display: none }`. Compact-mode activation and the control's behavior are
// covered in jsdom (src/App.activity-collapse.test.ts); the actual label
// hiding is a real computed-CSS effect jsdom cannot see, so it stays here.

function event(id: string, number: number, type: string, created: string): unknown {
  return {
    id,
    cursor: id,
    activity_type: type,
    author: "marius",
    body_preview: "",
    created_at: created,
    item_number: number,
    item_state: "open",
    item_title: number === 42 ? "Add browser regression coverage" : "Refactor theme system",
    item_type: "pr",
    item_url: `https://github.com/acme/widgets/pull/${number}`,
    platform_host: "github.com",
    repo_owner: "acme",
    repo_name: "widgets",
    repo: {
      provider: "github",
      platform_host: "github.com",
      owner: "acme",
      name: "widgets",
      repo_path: "acme/widgets",
      capabilities: {},
    },
  };
}

async function mockActivity(page: Page): Promise<void> {
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
          collapse_threads: false,
        },
        terminal: {
          ...defaultSettings.terminal,
          font_size: 14,
        },
      }),
    });
  });
  await page.route("**/api/v1/activity**", async (route) => {
    await route.fulfill({
      status: 200,
      contentType: "application/json",
      body: JSON.stringify({
        capped: false,
        items: [
          event("a1", 42, "comment", "2026-03-30T14:00:00Z"),
          event("a2", 42, "review", "2026-03-30T13:00:00Z"),
          event("b1", 55, "comment", "2026-03-30T12:00:00Z"),
        ],
      }),
    });
  });
}

test("compact collapse control hides its text label in the side detail pane", async ({ page }) => {
  await mockActivity(page);
  await page.goto("/?view=threaded");

  // Open a detail by clicking the item row body (not the caret).
  await page.locator(".item-row").first().locator(".item-title").click();
  await expect(page.locator(".activity-detail")).toBeVisible();
  await expect(page.locator(".activity-pane")).toBeVisible();

  // The control stays reachable by its accessible name, but its text label
  // is hidden by the compact-feed CSS so it does not stack awkwardly in the
  // narrow pane.
  const collapseBtn = page.getByRole("button", { name: "Collapse all" });
  await expect(collapseBtn).toBeVisible();
  await expect(page.locator(".collapse-all-btn .collapse-all-label")).toBeHidden();
});
