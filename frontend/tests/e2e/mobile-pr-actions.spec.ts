import { devices, expect, test } from "@playwright/test";

import { mockApi } from "./support/mockApi";

test.skip(({ browserName }) => browserName === "firefox", "Firefox does not support Playwright mobile emulation");
test.use({ ...devices["Pixel 5"] });

test("phone PR detail renders the primary actions as one kit action grid", async ({ page }, testInfo) => {
  await mockApi(page);

  await page.goto("/pulls/github/acme/widgets/42");

  // The detail lives in the phone shell: the shared top bar sits above a
  // detail header that names the PR and offers Back to the list.
  const topbar = page.locator(".mobile-shell .mobile-topbar");
  await expect(topbar).toBeVisible();
  await expect(page.locator(".mobile-detail-header__badge")).toHaveText("PR #42");
  await expect(page.locator(".mobile-detail-header__back")).toHaveText("Pull requests");
  const topbarBox = await topbar.boundingBox();
  expect(topbarBox).not.toBeNull();
  expect(topbarBox!.y).toBeGreaterThanOrEqual(0);
  expect(topbarBox!.x + topbarBox!.width).toBeLessThanOrEqual(page.viewportSize()!.width);

  const grid = page.getByRole("group", { name: "Pull request actions" });
  await expect(grid).toBeVisible();
  await expect(grid.getByRole("button", { name: "Approve" })).toBeVisible();
  await expect(grid.getByRole("button", { name: "Close" })).toBeVisible();
  await expect(page.locator(".actions-menu-trigger")).toHaveCount(0);
  await expect(page.locator(".actions-row--measure")).toHaveCount(0);

  // Every action stays inside the viewport: no horizontal overflow from the grid.
  const box = await grid.boundingBox();
  expect(box).not.toBeNull();
  expect(box!.x).toBeGreaterThanOrEqual(0);
  expect(box!.x + box!.width).toBeLessThanOrEqual(page.viewportSize()!.width);

  await page.screenshot({ path: testInfo.outputPath("phone-pr-actions.png"), fullPage: false });

  // A deep link has no list history, so Back lands on the phone PR list.
  await page.locator(".mobile-detail-header__back").click();
  await expect(page).toHaveURL(/\/m\/pulls$/);
  await expect(page.locator(".mobile-shell .pull-item").first()).toBeVisible();
});

test("phone PR list keyboard focus moves row to row and the search field shows one focus ring", async ({ page }) => {
  await mockApi(page);

  await page.goto("/m/pulls");
  const rows = page.locator(".mobile-shell .pull-item");
  await expect(rows.nth(1)).toBeVisible();

  // Tab from the search field reaches the Filters toggle, the labelled
  // scroll region, the first row, then the second row: the row's hover-only
  // star control must not take a tab stop of its own.
  const wrapper = page.locator(".mobile-triage-search-bar__search .kit-text-input");
  const restingBorder = await wrapper.evaluate((el) => getComputedStyle(el).borderTopColor);

  await page.locator("button[aria-label='Open desktop view']").focus();
  await page.keyboard.press("Tab");
  const search = page.getByRole("searchbox", { name: "Search PRs" });
  await expect(search).toBeFocused();

  // The kit text field signals keyboard focus with its wrapper border alone;
  // a second outline ring around the same wrapper reads as double chrome.
  await expect(wrapper).not.toHaveCSS("border-top-color", restingBorder);
  await expect(wrapper).toHaveCSS("outline-style", "none");

  await page.keyboard.press("Tab");
  await expect(page.getByRole("button", { name: "Filters" })).toBeFocused();
  await page.keyboard.press("Tab");
  await expect(page.getByRole("region", { name: "Focus list" })).toBeFocused();
  await page.keyboard.press("Tab");
  await expect(rows.nth(0)).toBeFocused();
  await page.keyboard.press("Tab");
  await expect(rows.nth(1)).toBeFocused();
});

test("phone PR detail tabs are one tab stop and switch with the arrow keys", async ({ page }) => {
  await mockApi(page);

  await page.goto("/pulls/github/acme/widgets/42");
  const conversation = page.getByRole("tab", { name: "Conversation" });
  const files = page.getByRole("tab", { name: /Files changed/ });
  await expect(conversation).toHaveAttribute("aria-selected", "true");

  // Tab from the detail header's Back control lands on the active tab, and
  // the next Tab leaves the strip for the panel instead of walking the tabs.
  await page.locator(".mobile-detail-header__back").focus();
  await page.keyboard.press("Tab");
  await expect(conversation).toBeFocused();
  await page.keyboard.press("Tab");
  await expect(files).not.toBeFocused();
  await expect(conversation).toHaveAttribute("aria-selected", "true");

  // Arrow keys are what switch tabs.
  await conversation.focus();
  await page.keyboard.press("ArrowRight");
  await expect(files).toBeFocused();
  await expect(files).toHaveAttribute("aria-selected", "true");
  await expect(page.locator(".files-view")).toBeVisible();
});
