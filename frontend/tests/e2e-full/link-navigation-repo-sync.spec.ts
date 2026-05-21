import { expect, test, type Page } from "@playwright/test";

// Regression coverage: deep-linking to a PR or issue used to leave the
// repo dropdown and the left sidebar list pinned to whichever repo was
// previously picked, even though the detail pane jumped to the new repo.
// App.svelte now syncs globalRepo to match the route's selected item.
// Seed: acme/widgets and acme/tools on github.com (cmd/e2e-server).

async function waitForPullDetail(page: Page): Promise<void> {
  await page.locator(".pull-detail")
    .waitFor({ state: "visible", timeout: 10_000 });
}

async function waitForIssueDetail(page: Page): Promise<void> {
  await page.locator(".issue-detail")
    .waitFor({ state: "visible", timeout: 10_000 });
}

test.describe("deep-link repo dropdown + sidebar sync", () => {
  test("navigating to a PR in a different repo updates the dropdown and list", async ({ page }) => {
    await page.addInitScript(() => {
      localStorage.setItem(
        "middleman-filter-repo", "github.com/acme/widgets",
      );
    });

    await page.goto("/pulls/github/acme/tools/1");
    await waitForPullDetail(page);

    await expect(page.locator(".typeahead-value")).toHaveText(
      "github.com/acme/tools",
      { timeout: 5_000 },
    );

    const repoHeaders = page.locator(".repo-header__name");
    await expect(repoHeaders).toHaveCount(1, { timeout: 5_000 });
    await expect(repoHeaders.first()).toHaveText("acme/tools");
  });

  test("navigating to an issue in a different repo updates the dropdown and list", async ({ page }) => {
    await page.addInitScript(() => {
      localStorage.setItem(
        "middleman-filter-repo", "github.com/acme/tools",
      );
    });

    await page.goto("/issues/github/acme/widgets/10");
    await waitForIssueDetail(page);

    await expect(page.locator(".typeahead-value")).toHaveText(
      "github.com/acme/widgets",
      { timeout: 5_000 },
    );
  });

  test("navigating between PRs in different repos updates the dropdown each time", async ({ page }) => {
    await page.addInitScript(() => {
      localStorage.setItem(
        "middleman-filter-repo", "github.com/acme/widgets",
      );
    });

    await page.goto("/pulls/github/acme/widgets/1");
    await waitForPullDetail(page);
    await expect(page.locator(".typeahead-value")).toHaveText(
      "github.com/acme/widgets",
      { timeout: 5_000 },
    );

    await page.goto("/pulls/github/acme/tools/1");
    await waitForPullDetail(page);
    await expect(page.locator(".typeahead-value")).toHaveText(
      "github.com/acme/tools",
      { timeout: 5_000 },
    );
  });

  test("selecting an item from All repos keeps the all-repo filter", async ({ page }) => {
    await page.goto("/pulls");
    await page.locator(".pull-item").first()
      .waitFor({ state: "visible", timeout: 10_000 });

    await expect(page.locator(".typeahead-value")).toHaveText("All repos");
    await page.locator(".pull-item").filter({
      hasText: "Add widget caching layer",
    }).first().click();
    await waitForPullDetail(page);

    await expect(page.locator(".typeahead-value")).toHaveText("All repos");
  });

  test("opening /pulls without a selection preserves the user's chosen repo", async ({ page }) => {
    await page.addInitScript(() => {
      localStorage.setItem(
        "middleman-filter-repo", "github.com/acme/widgets",
      );
    });

    await page.goto("/pulls");
    await page.locator(".pull-item").first()
      .waitFor({ state: "visible", timeout: 10_000 });

    await expect(page.locator(".typeahead-value")).toHaveText(
      "github.com/acme/widgets",
    );
  });
});
