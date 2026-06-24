import { expect, test, type Page } from "@playwright/test";

// The repo dropdown + sidebar sync for PR navigation (initial deep-link,
// back-to-back PR switches, all-repos selection, and preserving the chosen repo
// on a bare /pulls) moved to the browser tier
// (frontend/src/App.repo-sync.browser.svelte.ts). The issue deep-link variant
// stays here: the issues-route global-repo sync interacts with the issue detail
// and settings load in a way that is exercised most faithfully end-to-end
// against the live backend.

async function waitForIssueDetail(page: Page): Promise<void> {
  await page.locator(".issue-detail").waitFor({ state: "visible", timeout: 10_000 });
}

test("navigating to an issue in a different repo updates the dropdown", async ({ page }) => {
  await page.addInitScript(() => {
    localStorage.setItem("middleman-filter-repo", "github|github.com/acme/tools");
  });

  await page.goto("/issues/github/acme/widgets/10");
  await waitForIssueDetail(page);

  await expect(page.locator(".typeahead-value")).toHaveText("github/github.com/acme/widgets", {
    timeout: 5_000,
  });
});
