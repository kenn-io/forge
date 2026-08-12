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

test("compact activity controls stay in two rows with the author summarized in Filters", async ({ page }) => {
  await page.setViewportSize({ width: 1440, height: 900 });
  await mockActivity(page);
  await page.goto("/?view=threaded&author=long-author-name");
  await page.addStyleTag({
    content: ".activity-pane { flex: 0 0 360px !important; width: 360px !important; }",
  });

  await page.locator(".item-row").first().locator(".item-title").click();
  await expect(page.locator(".activity-feed--compact")).toBeVisible();
  await expect(page.getByRole("button", { name: "Filters · long-author-name" })).toBeVisible();
  await expect(page.getByRole("button", { name: "Clear author filter long-author-name" })).toHaveCount(0);

  const metrics = await page.locator(".activity-feed--compact .controls-bar").evaluate((controls) => {
    const bounds = controls.getBoundingClientRect();
    const searchTop = Math.round(controls.querySelector(".search-wrap")!.getBoundingClientRect().top);
    const togglesTop = Math.round(controls.querySelector(".filter-group")!.getBoundingClientRect().top);
    const filtersTop = Math.round(controls.querySelector(".filters-wrap")!.getBoundingClientRect().top);
    const collapseTop = Math.round(controls.querySelector(".collapse-all-btn")!.getBoundingClientRect().top);
    const childBounds = [...controls.children].map((child) => {
      const rect = child.getBoundingClientRect();
      return { left: rect.left, right: rect.right, top: Math.round(rect.top) };
    });
    return {
      left: bounds.left,
      right: bounds.right,
      scrollWidth: controls.scrollWidth,
      clientWidth: controls.clientWidth,
      rowTops: [...new Set(childBounds.map((rect) => rect.top))],
      childBounds,
      searchTop,
      togglesTop,
      filtersTop,
      collapseTop,
    };
  });

  expect(metrics.rowTops).toHaveLength(2);
  expect(metrics.togglesTop).toBe(metrics.filtersTop);
  expect(metrics.filtersTop).toBe(metrics.collapseTop);
  expect(metrics.searchTop).not.toBe(metrics.filtersTop);
  expect(metrics.scrollWidth).toBeLessThanOrEqual(metrics.clientWidth);
  for (const bounds of metrics.childBounds) {
    expect(bounds.left).toBeGreaterThanOrEqual(metrics.left);
    expect(bounds.right).toBeLessThanOrEqual(metrics.right);
  }
});

test("compact Filters stays content-sized when the Activity pane is wide", async ({ page }) => {
  await page.setViewportSize({ width: 1440, height: 900 });
  await mockActivity(page);
  await page.goto("/?view=threaded&author=fixture-bot");
  await page.addStyleTag({
    content: ".activity-pane { flex: 0 0 1000px !important; width: 1000px !important; }",
  });

  await page.locator(".item-row").first().locator(".item-title").click();
  await expect(page.locator(".activity-feed--compact")).toBeVisible();

  const metrics = await page.locator(".activity-feed--compact .controls-bar").evaluate((controls) => {
    const controlsBounds = controls.getBoundingClientRect();
    const filters = controls.querySelector<HTMLElement>(".filters-wrap")!;
    const filtersBounds = filters.getBoundingClientRect();
    const trigger = filters.querySelector<HTMLElement>(".activity-filters__trigger")!;
    const collapseBounds = controls.querySelector<HTMLElement>(".collapse-all-btn")!.getBoundingClientRect();
    return {
      controlsRight: controlsBounds.right,
      filtersWidth: filtersBounds.width,
      triggerScrollWidth: trigger.scrollWidth,
      collapseRight: collapseBounds.right,
    };
  });

  expect(metrics.filtersWidth).toBeLessThan(320);
  expect(metrics.filtersWidth).toBeGreaterThanOrEqual(metrics.triggerScrollWidth);
  expect(metrics.collapseRight).toBeLessThan(metrics.controlsRight - 200);
});
