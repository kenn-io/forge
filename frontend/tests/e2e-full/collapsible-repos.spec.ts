import { expect, test, type Page } from "@playwright/test";

// The collapse/expand, count, persistence, cross-surface, and status-group
// behaviors moved to the browser tier
// (frontend/src/App.collapsible-repos.browser.svelte.ts). What stays here is the
// native keyboard activation of the header button: Enter/Space firing the
// button's default click is a real key event the browser tier's synthetic
// dispatch cannot reproduce.
//
// Seed (cmd/e2e-server): 8 open PRs (widgets #1/#2/#6/#7, tools #1/#10/#11/#12).

async function waitForPullList(page: Page): Promise<void> {
  await page.locator(".pull-item").first().waitFor({ state: "visible", timeout: 10_000 });
}

function widgetsHeader(page: Page) {
  return page.locator(".repo-header", { hasText: "acme/widgets" });
}

test("PR list — keyboard activation via Enter and Space toggles collapse", async ({ page }) => {
  await page.addInitScript(() => {
    localStorage.removeItem("middleman:collapsedRepos:pulls");
  });
  await page.goto("/pulls");
  await waitForPullList(page);

  await widgetsHeader(page).focus();
  await page.keyboard.press("Enter");
  await expect(widgetsHeader(page)).toHaveAttribute("aria-expanded", "false");
  await expect(page.locator(".pull-item")).toHaveCount(4);

  await widgetsHeader(page).focus();
  await page.keyboard.press("Space");
  await expect(widgetsHeader(page)).toHaveAttribute("aria-expanded", "true");
  await expect(page.locator(".pull-item")).toHaveCount(8);
});
