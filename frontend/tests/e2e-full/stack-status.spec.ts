import { expect, test } from "@playwright/test";

// The stack-panel rendering and the focus-route member navigation moved to the
// browser tier (frontend/src/App.stack-status.browser.svelte.ts), where the app
// is mounted in real Chromium with the stack detail mocked at the fetch
// boundary. What stays here is the activity-drawer flow: opening the drawer from
// a selected= URL param and navigating between stack members rewrites the route
// query, which is genuine cross-layer drawer/routing glue best exercised against
// the live backend rather than re-mocked.
test("stack member navigation updates the activity drawer with full-stack data", async ({ page }) => {
  await page.goto("/?selected=pr:11&provider=github&platform_host=github.com&repo_path=acme%2Ftools");

  const detail = page.locator(".activity-detail");
  await expect(detail).toBeVisible();
  await expect(detail.locator(".pull-detail")).toBeVisible();

  await detail.getByTestId("stack-chip").click();
  await detail
    .getByRole("button", {
      name: "#10 Auth: extract token refresh helper",
    })
    .click();

  await expect(page).toHaveURL(/selected=pr%3A10/);
  await expect(page).toHaveURL(/repo_path=acme%2Ftools/);
  await expect(detail.locator(".activity-detail-header")).toContainText("acme/tools#10");
  await expect(detail.locator(".stack-panel")).toContainText("3 PRs · current 1/3");
});
