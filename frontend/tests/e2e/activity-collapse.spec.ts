import { expect, test, type Page } from "@playwright/test";

import { mockApi } from "./support/mockApi";

function event(
  id: string,
  number: number,
  type: string,
  created: string,
): unknown {
  return {
    id,
    cursor: id,
    activity_type: type,
    author: "marius",
    body_preview: "",
    created_at: created,
    item_number: number,
    item_state: "open",
    item_title:
      number === 42 ? "Add browser regression coverage" : "Refactor theme system",
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

test.describe("threaded activity collapse", () => {
  test("collapses, drills into one item, and persists across reload", async ({
    page,
  }) => {
    await mockActivity(page);
    await page.goto("/?view=threaded");

    const itemRows = page.locator(".item-row");
    const eventRows = page.locator(".event-row");
    await expect(itemRows).toHaveCount(2);
    await expect(eventRows.first()).toBeVisible();

    await page.getByRole("button", { name: "Collapse all" }).click();
    await expect(itemRows).toHaveCount(2);
    await expect(eventRows).toHaveCount(0);

    // Drill into a single item via its caret.
    await itemRows.first().locator(".thread-caret").click();
    await expect(eventRows.first()).toBeVisible();

    // Collapse-all wrote ?collapsed=1; a reload restores the collapsed state
    // and clears the session-only single-item override.
    await page.reload();
    await expect(page.locator(".item-row")).toHaveCount(2);
    await expect(page.locator(".event-row")).toHaveCount(0);
  });

  test("collapse control works while the side detail pane is open", async ({
    page,
  }) => {
    await mockActivity(page);
    await page.goto("/?view=threaded");

    // Open a detail by clicking the item row body (not the caret).
    await page.locator(".item-row").first().locator(".item-title").click();
    await expect(page.locator(".activity-detail")).toBeVisible();
    await expect(page.locator(".activity-pane")).toBeVisible();

    // In the narrow pane the control is icon-only: the button is present by
    // its accessible name, but its text label is hidden to avoid stacking.
    const collapseBtn = page.getByRole("button", { name: "Collapse all" });
    await expect(collapseBtn).toBeVisible();
    await expect(
      page.locator(".collapse-all-btn .collapse-all-label"),
    ).toBeHidden();
    await collapseBtn.click();
    await expect(page.locator(".event-row")).toHaveCount(0);
  });

  test("expand all restores every item's events", async ({ page }) => {
    await mockActivity(page);
    await page.goto("/?view=threaded");

    const eventRows = page.locator(".event-row");
    await expect(eventRows.first()).toBeVisible();
    const initialCount = await eventRows.count();

    await page.getByRole("button", { name: "Collapse all" }).click();
    await expect(eventRows).toHaveCount(0);

    // The control flips to Expand all; clicking it brings every event back.
    await page.getByRole("button", { name: "Expand all" }).click();
    await expect(eventRows).toHaveCount(initialCount);
  });
});
