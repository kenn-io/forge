import { expect, test, type Page } from "@playwright/test";

// The repo grouping toggle behaviors — PR-list grouped/ungrouped rendering,
// persistence across reload, sync into the issue list and threaded activity
// view, cross-repo thread separation, the threaded-only grouping control,
// hide-org-name relabeling in flat and threaded activity, and the threaded
// empty state — moved to the browser tier
// (frontend/src/App.grouping-toggle.browser.svelte.ts). What stays here is the
// j/k keyboard navigation: it asserts selection follows the flat visual order,
// which needs native key handling plus scroll-into-view behavior.
//
// Seed data repos: acme/widgets, acme/tools. Open PRs (8): widgets#1, #2, #6,
// #7, tools#1, #10, #11, #12 (last three form a stack).

async function waitForPullList(page: Page): Promise<void> {
  await page.locator(".pull-item").first().waitFor({ state: "visible", timeout: 10_000 });
}

async function selectPullGrouping(page: Page, label: string | RegExp): Promise<void> {
  const groupButton = page.locator(".group-btn", { hasText: label });
  if (await groupButton.isVisible()) {
    await groupButton.click();
    return;
  }

  const compactLabel = compactPullGroupingLabel(label);
  await page.getByRole("button", { name: "Filters" }).click();
  await page.locator(".filter-dropdown .filter-item", { hasText: compactLabel }).click();
}

function compactPullGroupingLabel(label: string | RegExp): string | RegExp {
  if (typeof label !== "string") return label;
  if (label === "Repo") return "By repo";
  if (label === "Status") return "By status";
  if (label === "All") return "Flat list";
  return label;
}

test.describe("grouping toggle", () => {
  test.beforeEach(async ({ page }) => {
    await page.addInitScript(() => {
      if (sessionStorage.getItem("middleman:test:grouping:init") === "1") {
        return;
      }
      localStorage.removeItem("middleman:groupingMode");
      localStorage.removeItem("middleman:groupByRepo");
      localStorage.removeItem("middleman:hideOrgName");
      sessionStorage.setItem("middleman:test:grouping:init", "1");
    });
    await page.goto("/pulls");
    await waitForPullList(page);
  });

  test("j/k navigation follows flat order in ungrouped mode", async ({ page }) => {
    // Switch to ungrouped.
    await selectPullGrouping(page, "All");
    await expect(page.locator(".repo-header")).toHaveCount(0, {
      timeout: 5_000,
    });

    // Capture the visible flat order of items.
    const allItems = page.locator(".pull-item");
    const firstVisibleMeta = await allItems.nth(0).locator(".meta-left").textContent();
    const secondVisibleMeta = await allItems.nth(1).locator(".meta-left").textContent();

    // Press j to select first item — should match the first visible item.
    await page.keyboard.press("j");
    await expect(page.locator(".pull-item.selected")).toHaveCount(1);
    const firstSelected = await page.locator(".pull-item.selected .meta-left").textContent();
    expect(firstSelected).toEqual(firstVisibleMeta);

    // Press j again — should match the second visible item.
    await page.keyboard.press("j");
    const secondSelected = await page.locator(".pull-item.selected .meta-left").textContent();
    expect(secondSelected).toEqual(secondVisibleMeta);
  });
});
