import { expect, test, type Page } from "@playwright/test";

// Seed data repos: acme/widgets, acme/tools, and a GitLab group/project issue.
// Open PRs (8): widgets#1, #2, #6, #7, tools#1, #10, #11, #12 (last three form a stack)
// Open issues (5): widgets#10, #11, #13, tools#5, group/project#11

async function waitForPullList(page: Page): Promise<void> {
  await page.locator(".pull-item").first().waitFor({ state: "visible", timeout: 10_000 });
}

async function waitForIssueList(page: Page): Promise<void> {
  await page.locator(".issue-item").first().waitFor({ state: "visible", timeout: 10_000 });
}

async function openActivityViewDropdown(page: Page) {
  const dropdown = page.locator(".activity-feed .filter-dropdown");
  if (await dropdown.isVisible()) {
    return dropdown;
  }
  await page.locator(".activity-feed .filter-btn", { hasText: "View" }).click();
  await expect(dropdown).toBeVisible();
  return dropdown;
}

async function selectActivityViewItem(page: Page, label: string | RegExp): Promise<void> {
  const dropdown = await openActivityViewDropdown(page);
  await dropdown.locator(".filter-item", { hasText: label }).click();
}

async function selectPullGrouping(page: Page, label: string | RegExp): Promise<void> {
  const groupButton = page.locator(".group-btn", { hasText: label });
  if (await groupButton.isVisible()) {
    await groupButton.click();
    return;
  }

  await page.getByRole("button", { name: "Filters" }).click();
  await page.locator(".filter-dropdown .filter-item", { hasText: label }).last().click();
}

test.describe("grouping toggle", () => {
  test.beforeEach(async ({ page }) => {
    // Clear persisted grouping once before first app bootstrap so tests start
    // in default grouped mode without relying on WebKit reload stability.
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

  test("PR list defaults to grouped with repo headers", async ({ page }) => {
    await expect(page.locator(".repo-header").first()).toBeVisible();
    // No repo badges visible in grouped mode.
    await expect(page.locator(".repo-chip")).toHaveCount(0);
  });

  test("PR list ungrouped shows repo badges and no headers", async ({ page }) => {
    // Click "All" in group toggle.
    await selectPullGrouping(page, "All");

    // Repo headers should disappear.
    await expect(page.locator(".repo-header")).toHaveCount(0, {
      timeout: 5_000,
    });

    // Repo badges should appear on each item.
    const badges = page.locator(".repo-chip");
    await expect(badges.first()).toBeVisible();

    // Should have a badge for each PR.
    const items = page.locator(".pull-item");
    const itemCount = await items.count();
    await expect(badges).toHaveCount(itemCount);
  });

  test("toggle persists across page reload", async ({ page }) => {
    // Switch to ungrouped.
    await selectPullGrouping(page, "All");
    await expect(page.locator(".repo-header")).toHaveCount(0, {
      timeout: 5_000,
    });

    // Verify persisted state in a fresh page in same browser context.
    const refreshedPage = await page.context().newPage();
    await refreshedPage.goto("/pulls");
    await waitForPullList(refreshedPage);

    // Should still be ungrouped.
    await expect(refreshedPage.locator(".repo-header")).toHaveCount(0);
    await expect(refreshedPage.locator(".repo-chip").first()).toBeVisible();

    await refreshedPage.close();
  });

  test("toggle syncs from PRs to issues", async ({ page }) => {
    // Switch to ungrouped in PR list.
    await selectPullGrouping(page, "All");
    await expect(page.locator(".repo-header")).toHaveCount(0, {
      timeout: 5_000,
    });

    // Navigate to issues.
    await page.goto("/issues");
    await waitForIssueList(page);

    // Issues should also be ungrouped.
    await expect(page.locator(".repo-header")).toHaveCount(0);
    await expect(page.locator(".repo-chip").first()).toBeVisible();
  });

  test("toggle syncs to activity threaded view", async ({ page }) => {
    // Switch to ungrouped in PR list.
    await selectPullGrouping(page, "All");
    await expect(page.locator(".repo-header")).toHaveCount(0, {
      timeout: 5_000,
    });

    // Navigate to activity.
    await page.goto("/");

    // Switch to threaded mode.
    await selectActivityViewItem(page, "Threaded");

    // Wait for threaded view to render.
    await page.locator(".threaded-view .item-row").first().waitFor({ state: "visible", timeout: 10_000 });

    // Repo headers should not be visible (ungrouped).
    await expect(page.locator(".threaded-view .repo-header")).toHaveCount(0);

    // Repo tags should appear on item rows.
    await expect(page.locator(".repo-tag").first()).toBeVisible();
  });

  test("activity threaded ungrouped keeps cross-repo items separate", async ({ page }) => {
    // Seed data has both widgets#1 and tools#1 as PRs.
    // In ungrouped threaded mode, they must remain separate threads.
    await page.goto("/");

    // Switch to threaded + ungrouped.
    await selectActivityViewItem(page, "Threaded");
    await page.locator(".threaded-view .item-row").first().waitFor({ state: "visible", timeout: 10_000 });
    await selectActivityViewItem(page, "All");

    // Wait for repo tags to appear (ungrouped).
    await page.locator(".repo-tag").first().waitFor({ state: "visible", timeout: 5_000 });

    // Find all item rows whose ref is exactly "#1" (not #10, #11, etc.).
    const refOnes = page.locator(".item-row .item-ref").filter({ hasText: /^#1$/ });
    // There should be at least 2 (one for widgets, one for tools).
    const count = await refOnes.count();
    expect(count).toBeGreaterThanOrEqual(2);
  });

  test("activity toggle hidden in flat mode, visible in threaded", async ({ page }) => {
    await page.goto("/");

    // In flat mode (default), the view dropdown should not offer grouping.
    let dropdown = await openActivityViewDropdown(page);
    await expect(dropdown.locator(".filter-item", { hasText: /By repo/i })).toHaveCount(0);
    await page.keyboard.press("Escape");

    // Switch to threaded mode.
    await selectActivityViewItem(page, "Threaded");
    await page.locator(".threaded-view").waitFor({ state: "visible", timeout: 10_000 });

    // Now grouping controls should be available from the view dropdown.
    dropdown = await openActivityViewDropdown(page);
    await expect(dropdown.locator(".filter-item", { hasText: /By repo/i })).toBeVisible();
  });

  test("activity flat table rows respect hide org name", async ({ page }) => {
    await page.goto("/?view=flat");

    const row = page
      .locator(".activity-table .activity-row", {
        has: page.locator(".item-title", {
          hasText: "Add widget caching layer",
        }),
      })
      .first();
    await expect(row).toBeVisible({ timeout: 10_000 });

    const repoLabel = row.locator(".col-repo");
    await expect(repoLabel).toHaveText("acme/widgets");

    await selectActivityViewItem(page, "Hide org name");

    await expect(repoLabel).toHaveText("widgets");
    await expect(repoLabel).not.toHaveText("acme/widgets");
  });

  test("activity threaded grouped headers respect hide org name", async ({ page }) => {
    await page.goto("/");

    await selectActivityViewItem(page, "Threaded");
    await page.locator(".threaded-view .repo-header").first().waitFor({ state: "visible", timeout: 10_000 });

    await expect(
      page.locator(".threaded-view .repo-header .repo-name", {
        hasText: "acme/widgets",
      }),
    ).toBeVisible();

    await selectActivityViewItem(page, "Hide org name");

    await expect(
      page.locator(".threaded-view .repo-header .repo-name", {
        hasText: /^widgets$/,
      }),
    ).toBeVisible();
    await expect(
      page.locator(".threaded-view .repo-header .repo-name", {
        hasText: "acme/widgets",
      }),
    ).toHaveCount(0);
  });

  test("activity threaded ungrouped rows show the item author and hide org repo chips", async ({ page }) => {
    await page.goto("/");

    await selectActivityViewItem(page, "Threaded");
    await page.locator(".threaded-view .item-row").first().waitFor({ state: "visible", timeout: 10_000 });
    await selectActivityViewItem(page, "All");

    const row = page
      .locator(".threaded-view .item-row", {
        has: page.locator(".item-title", {
          hasText: "Add widget caching layer",
        }),
      })
      .first();
    await expect(row).toBeVisible();
    // PR #1 was opened by alice; bob posted the latest review. The item row
    // attributes the thread to its author (alice), not the latest actor.
    await expect(row.locator(".cell--author")).toHaveText("alice");

    const repoLabel = row.locator(".repo-chip__label");
    await expect(repoLabel).toHaveText("acme/widgets");

    await selectActivityViewItem(page, "Hide org name");

    await expect(repoLabel).toHaveText("widgets");
    await expect(repoLabel).not.toHaveText("acme/widgets");
  });

  test("threaded ungrouped empty state shows message", async ({ page }) => {
    await page.goto("/");

    // Search for a string that matches nothing to empty the result set.
    // Do this before switching to threaded so the debounced API call
    // completes while we're still in flat mode.
    const input = page.locator(".search-input");
    await input.fill("zzz_no_match_zzz");
    // Wait for the flat view to show the zero-results message
    // (not the loading placeholder) to prove the API returned 0 items.
    await expect(page.locator(".activity-feed .empty-state")).toHaveText("No activity found", {
      timeout: 10_000,
    });

    // Now switch to threaded + ungrouped. ActivityThreaded receives [].
    await selectActivityViewItem(page, "Threaded");
    await selectActivityViewItem(page, "All");

    // The threaded view's own empty state should render.
    await expect(page.locator(".threaded-view .empty-state")).toBeVisible({
      timeout: 5_000,
    });
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
