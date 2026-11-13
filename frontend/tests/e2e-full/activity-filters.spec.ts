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

async function selectActivityFilterItem(page: Page, label: string | RegExp): Promise<void> {
  await page.locator(".activity-feed .filters-wrap .kit-filter-dropdown__btn").click();
  await page.locator(".kit-filter-dropdown__panel").waitFor({ state: "visible" });
  await filterItem(page, label).click();
}

async function selectActivityLayoutItem(page: Page, label: string): Promise<void> {
  await page.locator(".activity-feed .layout-wrap .kit-filter-dropdown__btn").click();
  await page.locator(".kit-filter-dropdown__panel").waitFor({ state: "visible" });
  await filterItem(page, label).click();
}

// A string matches part of a row's text; a pattern matches its accessible name, which ends in
// ", excluded" for an excluded condition.
function filterItem(page: Page, label: string | RegExp) {
  return typeof label === "string"
    ? page.locator(".kit-filter-dropdown__item", { hasText: label })
    : page.locator(".kit-filter-dropdown__panel").getByRole("button", { name: label });
}

// State's options match the pull request list's: Open hides closed and merged items.
const OPEN = /^Open$/;
const ALL_STATES = /^All states$/;

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
    await page.getByRole("switch", { name: "Issues" }).click();
    await expectAllBadges(page, "PR");
  });

  test("Issues filter shows only issue items", async ({ page }) => {
    await page.getByRole("switch", { name: "PRs" }).click();
    await expectAllBadges(page, "Issue");
  });

  test("All filter shows both PR and issue items", async ({ page }) => {
    // Hide issues to change state, then restore them.
    await page.getByRole("switch", { name: "Issues" }).click();
    await expectAllBadges(page, "PR");

    await page.getByRole("switch", { name: "Issues" }).click();

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

  test("URLs with no top-level event types retain PR commits and the default item scope", async ({ page }) => {
    const notificationResponse = page.waitForResponse((response) => {
      const url = new URL(response.url());
      const types = (url.searchParams.get("types") ?? "").split(",");
      return url.pathname === "/api/v1/activity" && types.includes("notification");
    });

    await page.goto("/?event_types=none");
    const response = await notificationResponse;
    const requestURL = new URL(response.url());
    const snapshot = (await response.json()) as { items: Array<{ activity_type: string }> };

    expect(requestURL.searchParams.getAll("item_types")).toEqual([]);
    expect(new Set(snapshot.items.map((item) => item.activity_type))).toEqual(new Set(["commit", "notification"]));
    expect(new URL(page.url()).searchParams.get("event_types")).toBe("none");
    await expect(page.getByRole("switch", { name: "PRs" })).toBeChecked();
    await expect(page.getByRole("switch", { name: "Issues" })).toBeChecked();
    await expect(page.locator(".activity-row .evt-label.evt-notification").first()).toBeVisible();
    await expect(page.locator(".activity-row .evt-label.evt-commit").first()).toBeVisible();
    await expect(page.locator(".activity-row .evt-label.evt-comment, .activity-row .evt-label.evt-review")).toHaveCount(
      0,
    );
  });

  test("Threaded item toggles hide the complete matching threads", async ({ page }) => {
    await selectActivityLayoutItem(page, "Threaded");
    await expect(page.locator(".threaded-view .chip--kind-pr").first()).toBeVisible();
    await expect(page.locator(".threaded-view .chip--kind-issue").first()).toBeVisible();

    await page.getByRole("switch", { name: "PRs" }).click();

    await expect(page.locator(".threaded-view .chip--kind-pr")).toHaveCount(0);
    await expect(page.locator(".threaded-view .chip--kind-issue").first()).toBeVisible();
    await expect.poll(() => new URL(page.url()).searchParams.get("item_types")).toBe("issue");
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

  test("State Open removes closed and merged items, and Closed keeps only them", async ({ page }) => {
    // Verify closed/merged items exist initially.
    await expect(page.locator(".state-badge.state-closed, .state-badge.state-merged").first()).toBeVisible();

    await selectActivityFilterItem(page, /^Closed$/);
    await expect(page.locator(".activity-row", { hasText: "WIP: new dashboard layout" })).toHaveCount(0, {
      timeout: 5_000,
    });
    await expect(page.locator(".state-badge.state-closed, .state-badge.state-merged").first()).toBeVisible();

    await filterItem(page, OPEN).click();

    await expect(page.locator(".state-badge.state-closed")).toHaveCount(0, {
      timeout: 5_000,
    });
    await expect(page.locator(".state-badge.state-merged")).toHaveCount(0);
  });

  test("refresh preserves the selected time window and State Open", async ({ page }) => {
    await selectActivityLayoutItem(page, "90d");
    await selectActivityFilterItem(page, OPEN);
    await page.reload();
    await waitForTable(page);

    await expect(page.locator(".activity-feed .layout-wrap .kit-filter-dropdown__btn")).toContainText("90d");
    await expect(page.locator(".state-badge.state-closed, .state-badge.state-merged")).toHaveCount(0);
    await selectActivityFilterItem(page, ALL_STATES);
    await expect(page.locator(".state-badge.state-closed, .state-badge.state-merged").first()).toBeVisible();
  });

  test("returning from Settings preserves the time window and State Open", async ({ page }) => {
    await selectActivityLayoutItem(page, "90d");
    await selectActivityFilterItem(page, OPEN);
    await page.goto("/settings");
    await page.getByRole("button", { name: "Back to app" }).click();
    await waitForTable(page);
    await expect(page.locator(".activity-feed .layout-wrap .kit-filter-dropdown__btn")).toContainText("90d");
    await expect(page.locator(".state-badge.state-closed, .state-badge.state-merged")).toHaveCount(0);
  });

  test("Bots keeps only bot activity, then hides it", async ({ page }) => {
    const botCells = page.locator(".activity-row .col-author", {
      hasText: "dependabot[bot]",
    });
    await expect(botCells.first()).toBeVisible();

    await selectActivityFilterItem(page, /^Bots/);
    await expect
      .poll(async () => (await page.locator(".activity-row .col-author").allTextContents()).map((text) => text.trim()))
      .toEqual(expect.arrayContaining(["dependabot[bot]"]));
    await expect(page.locator(".activity-row .col-author").filter({ hasNotText: /bot/ })).toHaveCount(0);

    await filterItem(page, /^Bots/).click();
    await expect(botCells).toHaveCount(0, { timeout: 5_000 });
  });

  test("Draft keeps or hides draft pull request activity and leaves issues alone", async ({ page }) => {
    const draftRows = page.locator(".activity-row", { hasText: "WIP: new dashboard layout" });
    const issueBadges = page.locator(".activity-row .badge", { hasText: "Issue" });
    await expect(draftRows.first()).toBeVisible();

    const draft = filterItem(page, /^Draft/);
    await selectActivityFilterItem(page, /^Draft/);
    await expect(draft).toHaveAttribute("aria-pressed", "true");
    await expect(draftRows.first()).toBeVisible();
    await expect(issueBadges.first()).toBeVisible();

    await draft.click();
    await expect(draft).toHaveAccessibleName(/, excluded$/);
    await expect(draftRows).toHaveCount(0, { timeout: 5_000 });
    await expect(issueBadges.first()).toBeVisible();

    await draft.click();
    await expect(draftRows.first()).toBeVisible();
  });

  test("merge queued requires, then excludes, and the exclusion survives reload and navigation", async ({ page }) => {
    const row = page.locator(".kit-filter-dropdown__item", { hasText: "Merge queued" });
    const required = page.waitForResponse((response) => {
      const url = new URL(response.url());
      return url.pathname === "/api/v1/activity" && url.searchParams.get("merge_queued") === "true";
    });
    await selectActivityFilterItem(page, "Merge queued");
    expect((await required).status()).toBe(200);
    await expect(row).toHaveAccessibleName("Merge queued");
    await expect(row).toHaveAttribute("aria-pressed", "true");

    const filtered = page.waitForResponse((response) => {
      const url = new URL(response.url());
      return (
        url.pathname === "/api/v1/activity" &&
        url.searchParams.get("hide_merge_queued") === "true" &&
        !url.searchParams.has("merge_queued")
      );
    });
    await row.click();
    expect((await filtered).status()).toBe(200);
    await expect(page).toHaveURL(/hide_merge_queued=1/);
    await expect(row).toHaveAccessibleName(/, excluded$/);

    await page.reload();
    await waitForTable(page);
    await page.locator(".activity-feed .filters-wrap .kit-filter-dropdown__btn").click();
    await expect(row).toHaveAccessibleName(/, excluded$/);
    await page.keyboard.press("Escape");
    await page.getByTitle("Settings", { exact: true }).click();
    await page.getByRole("button", { name: "Back to app" }).click();
    await page.reload();
    await waitForTable(page);
    await page.goto("/?view=flat");
    await waitForTable(page);
    await page.locator(".activity-feed .filters-wrap .kit-filter-dropdown__btn").click();
    await expect(row).toHaveAccessibleName(/, excluded$/);
    await page.getByRole("button", { name: "Reset filters" }).click();
    await expect(row).toHaveAttribute("aria-pressed", "false");
    await expect(page).toHaveURL(/hide_merge_queued=0/);
  });

  test("24h range shows fewer items than 7d", async ({ page }) => {
    const rows7d = page.locator(".activity-row");
    const count7d = await rows7d.count();
    expect(count7d).toBeGreaterThan(0);

    // Switch to 24h. The 7d range has 14 items; 24h has fewer.
    // Use a web-first assertion that retries until the row count
    // drops below the 7d count, proving the filtered response
    // has rendered.
    await selectActivityLayoutItem(page, "24h");
    await expect(page.locator(".activity-row")).not.toHaveCount(count7d, {
      timeout: 10_000,
    });
    const count24h = await page.locator(".activity-row").count();
    expect(count24h).toBeLessThan(count7d);
  });

  test("search filters by title", async ({ page }) => {
    const input = page.locator(".search-wrap input");
    await input.fill("caching layer");

    // Wait for the server-side search to actually apply. Notifications now
    // sort to the top of the unfiltered feed, so a notification titled
    // "Add widget caching layer" satisfies a first-row check before the
    // debounced query returns; gating only on the first row would race the
    // search and observe the still-unfiltered list. Retry until every
    // visible row matches, which only holds once the filtered set renders.
    const rows = page.locator(".activity-row");
    await expect(async () => {
      const titles = await rows.locator(".item-title").allTextContents();
      expect(titles.length).toBeGreaterThan(0);
      for (const title of titles) {
        expect(title).toContain("Add widget caching layer");
      }
    }).toPass({ timeout: 10_000 });
  });

  test("the Author flyout filters by the PR or issue author", async ({ page }) => {
    const filteredResponse = page.waitForResponse((response) => {
      const url = new URL(response.url());
      return url.pathname === "/api/v1/activity" && url.searchParams.get("author") === "alice";
    });

    await page.getByRole("button", { name: /^Filters/ }).click();
    await page.getByRole("button", { name: /^Author/ }).click();
    await page.getByRole("dialog", { name: "Author" }).getByRole("button", { name: "alice", exact: true }).click();
    const response = await filteredResponse;
    expect(response.status()).toBe(200);
    const payload = await response.json();
    expect(payload.items.length).toBeGreaterThan(0);
    expect(payload.items.every((item: { item_author: string }) => item.item_author.toLowerCase() === "alice")).toBe(
      true,
    );

    await expect(page.locator(".activity-row").first()).toBeVisible();
    const eventAuthors = await page.locator(".activity-row .col-author").allTextContents();
    expect(eventAuthors.some((author) => author.trim().toLowerCase() !== "alice")).toBe(true);
    await expect(page).toHaveURL(/author=alice/);

    const authorChip = page.getByRole("button", { name: "Clear author filter alice" });
    await expect(authorChip).toBeVisible();
    const unfilteredResponse = page.waitForResponse((response) => {
      const url = new URL(response.url());
      return url.pathname === "/api/v1/activity" && !url.searchParams.has("author");
    });
    await authorChip.click();
    expect((await unfilteredResponse).status()).toBe(200);
    await expect(page).not.toHaveURL(/author=/);
  });

  test("combined: PRs + State Open shows only open PRs", async ({ page }) => {
    // Hide issues and wait for filtered DOM.
    await page.getByRole("switch", { name: "Issues" }).click();
    await expectAllBadges(page, "PR");

    // Choose Open (applied in the browser before the next load).
    await selectActivityFilterItem(page, OPEN);
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

test.describe("activity repository-only filtering", () => {
  let isolatedServer: IsolatedE2EServer | undefined;

  test.beforeAll(async () => {
    isolatedServer = await startIsolatedE2EServer();
  });

  test.afterAll(async () => {
    await isolatedServer?.stop();
  });

  test("hides all item threads while retaining repository commits", async ({ page }) => {
    const seeded = await page.request.post(`${isolatedServer!.info.base_url}/__e2e/activity/default-branch-commit`);
    expect(seeded.status()).toBe(204);

    await page.goto(isolatedServer!.info.base_url);
    await waitForTable(page);
    await selectActivityLayoutItem(page, "Threaded");
    await expect(page.locator(".threaded-view .item-row:not(.branch-activity-row)").first()).toBeVisible();
    await page.getByRole("switch", { name: "PRs" }).click();
    const repoOnlyResponse = page.waitForResponse((response) => {
      const url = new URL(response.url());
      const itemTypes = url.searchParams.getAll("item_types");
      return url.pathname === "/api/v1/activity" && itemTypes.length === 1 && itemTypes[0] === "repo";
    });
    await page.getByRole("switch", { name: "Issues" }).click();
    await repoOnlyResponse;

    await expect(page.locator(".threaded-view .item-row:not(.branch-activity-row)")).toHaveCount(0);
    await expect(
      page.locator(".threaded-view .branch-activity-row", {
        hasText: "Repository maintenance commit",
      }),
    ).toBeVisible();
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
    await selectActivityLayoutItem(page, "30d");
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
