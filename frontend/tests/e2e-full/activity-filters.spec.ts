import { expect, test, type Page } from "@playwright/test";
import { startIsolatedE2EServer, type IsolatedE2EServer } from "./support/e2eServer";

// The e2e server seeds the Activity config with view_mode: "flat"
// and time_range: "7d", so the flat table renders by default.
//
// 7d seeded data summary (14 items):
//   item_type: 8 PR, 6 issue
//   activity_type: 3 new_pr, 2 new_issue, 7 comment, 2 review, 0 commit
//   item_state: 13 open, 1 closed (issue#12)
//   bot authors: 2 (dependabot[bot] on PR#7 and issue#13)

async function waitForTable(page: Page): Promise<void> {
  await page.locator(".activity-table .activity-row").first().waitFor({ state: "visible", timeout: 10_000 });
}

async function selectActivityFilterItem(page: Page, label: string): Promise<void> {
  await page.locator(".filter-btn").click();
  await page.locator(".filter-dropdown").waitFor({ state: "visible" });
  await page.locator(".filter-item", { hasText: label }).click();
}

// Verify every badge in the activity table matches the expected text.
// Uses auto-retrying assertions so it waits for the DOM to settle.
async function expectAllBadges(page: Page, expected: string): Promise<void> {
  const badges = page.locator(".activity-row .badge");
  // First wait for at least one badge with the expected text to appear,
  // proving the filtered response has rendered.
  await expect(badges.filter({ hasText: expected }).first()).toBeVisible({
    timeout: 10_000,
  });
  // Then verify no badges with the wrong text remain.
  const wrong = expected === "PR" ? "Issue" : "PR";
  await expect(badges.filter({ hasText: wrong })).toHaveCount(0);
}

test.describe("activity feed filters", () => {
  test.beforeEach(async ({ page }) => {
    await page.goto("/");
    await waitForTable(page);
  });

  test("PR filter shows only PR items", async ({ page }) => {
    await page.locator(".seg-btn", { hasText: "PRs" }).click();
    await expectAllBadges(page, "PR");
  });

  test("Issues filter shows only issue items", async ({ page }) => {
    await page.locator(".seg-btn", { hasText: "Issues" }).click();
    await expectAllBadges(page, "Issue");
  });

  test("All filter shows both PR and issue items", async ({ page }) => {
    // Start on PRs to change state, then switch to All.
    await page.locator(".seg-btn", { hasText: "PRs" }).click();
    await expectAllBadges(page, "PR");

    await page.locator(".seg-btn", { hasText: "All" }).click();

    // Wait for both badge types to appear, proving the unfiltered
    // response has rendered.
    const badges = page.locator(".activity-row .badge");
    await expect(badges.filter({ hasText: "PR" }).first()).toBeVisible({
      timeout: 10_000,
    });
    await expect(badges.filter({ hasText: "Issue" }).first()).toBeVisible({
      timeout: 10_000,
    });
  });

  test("disabling Comments hides comment rows", async ({ page }) => {
    // Verify comments exist initially.
    await expect(page.locator(".evt-label.evt-comment").first()).toBeVisible();

    // Open filter dropdown and disable Comments.
    await selectActivityFilterItem(page, "Comments");

    await expect(page.locator(".evt-label.evt-comment")).toHaveCount(0, {
      timeout: 5_000,
    });
  });

  test("hide closed/merged removes those items", async ({ page }) => {
    // Verify closed/merged items exist initially.
    await expect(page.locator(".state-badge.state-closed, .state-badge.state-merged").first()).toBeVisible();

    // Open filter dropdown and enable "Hide closed/merged".
    await selectActivityFilterItem(page, "Hide closed/merged");

    await expect(page.locator(".state-badge.state-closed")).toHaveCount(0, {
      timeout: 5_000,
    });
    await expect(page.locator(".state-badge.state-merged")).toHaveCount(0);
  });

  test("hide bots removes bot-authored items", async ({ page }) => {
    const botCells = page.locator(".activity-row .col-author", {
      hasText: "dependabot[bot]",
    });
    await expect(botCells.first()).toBeVisible();

    // Open filter dropdown and enable "Hide bots".
    await selectActivityFilterItem(page, "Hide bots");

    await expect(botCells).toHaveCount(0, { timeout: 5_000 });
  });

  test("24h range shows fewer items than 7d", async ({ page }) => {
    const rows7d = page.locator(".activity-row");
    const count7d = await rows7d.count();
    expect(count7d).toBeGreaterThan(0);

    // Switch to 24h. The 7d range has 14 items; 24h has fewer.
    // Use a web-first assertion that retries until the row count
    // drops below the 7d count, proving the filtered response
    // has rendered.
    await selectActivityFilterItem(page, "24h");
    await expect(page.locator(".activity-row")).not.toHaveCount(count7d, {
      timeout: 10_000,
    });
    const count24h = await page.locator(".activity-row").count();
    expect(count24h).toBeLessThan(count7d);
  });

  test("search filters by title", async ({ page }) => {
    const input = page.locator(".search-input");
    await input.fill("caching layer");

    // Wait for the filtered result to render. All rows should
    // reference "Add widget caching layer" (the only match).
    // Use a web-first assertion on the row count: the seeded 7d
    // feed has 14 items, so waiting for exactly the expected
    // match count proves the search completed.
    const rows = page.locator(".activity-row");
    await expect(rows.first().locator(".item-title")).toContainText("caching layer", {
      timeout: 10_000,
    });

    const count = await rows.count();
    expect(count).toBeGreaterThan(0);
    for (let i = 0; i < count; i++) {
      const title = await rows.nth(i).locator(".item-title").textContent();
      expect(title).toContain("Add widget caching layer");
    }
  });

  test("combined: PRs + hide closed/merged shows only open PRs", async ({ page }) => {
    // Click PRs filter and wait for filtered DOM.
    await page.locator(".seg-btn", { hasText: "PRs" }).click();
    await expectAllBadges(page, "PR");

    // Enable hide closed/merged (client-side filter).
    await selectActivityFilterItem(page, "Hide closed/merged");
    await page.locator(".controls-bar").click({ position: { x: 5, y: 5 } });

    // Wait for merged/closed badges to disappear.
    await expect(page.locator(".state-badge.state-merged")).toHaveCount(0, {
      timeout: 5_000,
    });
    await expect(page.locator(".state-badge.state-closed")).toHaveCount(0);

    // All remaining badges should still be PR.
    await expectAllBadges(page, "PR");
  });
});

test.describe("activity UTC timestamp presentation", () => {
  let isolatedServer: IsolatedE2EServer | undefined;

  test.beforeAll(async () => {
    test.setTimeout(60_000);
    isolatedServer = await startIsolatedE2EServer();
  });

  test.afterAll(async () => {
    await isolatedServer?.stop();
  });

  test.beforeEach(async ({ page }) => {
    await page.addInitScript(
      (offsetMs) => {
        const originalNow = Date.now.bind(Date);
        Date.now = () => originalNow() + offsetMs;
      },
      2 * 24 * 60 * 60 * 1000,
    );
    await page.goto(isolatedServer!.info.base_url);
    await waitForTable(page);
  });

  test("activity API timestamps stay UTC and render as local dates", async ({ page }) => {
    await selectActivityFilterItem(page, "30d");
    await expect(page.locator(".activity-row").first()).toBeVisible();

    const payload = await page.evaluate(async () => {
      const response = await fetch("/api/v1/activity?view_mode=flat&time_range=30d");
      return response.json();
    });
    const prComment = payload.items.find(
      (item: { item_title: string; author: string; created_at: string; activity_type: string }) =>
        item.item_title === "Add widget caching layer" && item.author === "carol" && item.activity_type === "comment",
    );

    expect(prComment).toBeTruthy();
    expect(prComment.created_at).toMatch(/Z$/);

    const expectedLabel = await page.evaluate(
      (iso: string) => new Date(iso).toLocaleDateString(),
      prComment.created_at,
    );

    const row = page
      .locator(".activity-row", {
        has: page.locator(".item-title", {
          hasText: "Add widget caching layer",
        }),
      })
      .filter({
        has: page.locator(".col-author", { hasText: "carol" }),
      })
      .filter({
        has: page.locator(".evt-label.evt-comment"),
      })
      .first();

    await expect(row.locator(".col-when")).toHaveText(expectedLabel);
    expect(expectedLabel).not.toContain("T");
    expect(expectedLabel).not.toContain("Z");
  });
});
