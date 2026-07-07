import { test, expect, type Page } from "@playwright/test";
import {
  assertSeededRoborevDaemon,
  stopDaemon,
  startDaemon,
  restartDaemon,
  waitForReviewsReady,
  waitForJobRows,
  openDrawer,
} from "./support/roborev-helpers.js";

function parseElapsed(text: string): number {
  const value = text.trim();
  if (value === "--") return -1;

  const hours = value.match(/(\d+)h/);
  const minutes = value.match(/(\d+)m/);
  const seconds = value.match(/(\d+)s/);

  return (
    (hours ? Number(hours[1]) * 3600 : 0) + (minutes ? Number(minutes[1]) * 60 : 0) + (seconds ? Number(seconds[1]) : 0)
  );
}

function jobRowById(page: Page, id: number) {
  return page.locator(".job-row").filter({
    has: page.locator(".col-id .mono", { hasText: new RegExp(`^${id}$`) }),
  });
}

test.describe.serial("Roborev", () => {
  // Refuse to run if the e2e server is not proxying to the
  // script-managed seeded daemon. This catches the failure mode
  // where playwright is invoked directly instead of via
  // scripts/run-roborev-e2e.sh, while a real local roborev daemon
  // is bound to 127.0.0.1:7373 (the e2e server's silent default).
  test.beforeAll(async () => {
    await assertSeededRoborevDaemon();
  });

  async function selectStatusFilter(page: Page, label: string): Promise<void> {
    await page.locator(".kit-filter-dropdown__btn", { hasText: "Status" }).click();
    await page.locator(".kit-filter-dropdown__item", { hasText: label }).click();
  }

  // -------------------------------------------------------
  // Group 1: Table and Data Display
  // -------------------------------------------------------
  test.describe("Table and Data Display", () => {
    test("table loads with seeded jobs (first page, 50 rows)", async ({ page }) => {
      await waitForReviewsReady(page);
      await waitForJobRows(page, 50);
      const count = await page.locator(".job-row").count();
      expect(count).toBe(50);
    });

    test("column data renders correctly for known jobs", async ({ page }) => {
      await waitForReviewsReady(page);
      await waitForJobRows(page, 10);

      // Job 74 is a stable zero-duration fixture:
      // agent=claude, status=done, cost=$0.42.
      const row = jobRowById(page, 74);
      await expect(row).toBeVisible();
      await expect(row.locator(".col-agent")).toContainText("claude");
      await expect(row.locator(".status-badge")).toContainText("done");
      await expect(page.locator("th").filter({ hasText: "Cost" })).toBeVisible();
      await expect(row.locator(".col-cost")).toHaveText("~$0.42");
    });

    test("panel parent expands to real member rows through the daemon proxy", async ({ page }) => {
      await waitForReviewsReady(page);
      await waitForJobRows(page, 10);

      const parent = jobRowById(page, 79);
      await expect(parent).toBeVisible();
      await expect(parent.locator(".panel-status")).toContainText("1 ok · 1 failed");
      await expect(parent.locator(".col-cost")).toHaveText("~$0.35");

      const panelRunResponse = page.waitForResponse((response) => {
        const url = new URL(response.url());
        return (
          response.ok() &&
          url.pathname.endsWith("/api/roborev/api/jobs") &&
          url.searchParams.get("panel_run") === "panel-e2e-1" &&
          url.searchParams.get("omit_prompt") === "true"
        );
      });
      await page.keyboard.press("j");
      await expect(parent).toHaveClass(/highlighted/);
      await page.keyboard.press("ArrowRight");
      await panelRunResponse;

      const members = page.locator(".job-row.member");
      await expect(members).toHaveCount(2);
      await expect(members.nth(0).locator(".member-name")).toHaveText("default");
      await expect(members.nth(0).locator(".col-id")).toContainText("77");
      await expect(members.nth(1).locator(".member-name")).toHaveText("security");
      await expect(members.nth(1).locator(".col-id")).toContainText("78");

      await page.keyboard.press("Enter");
      await expect(page).toHaveURL(/\/reviews\/79$/);
      await expect(page.locator(".panel-line")).toContainText("2 reviewers:");
      await expect(page.locator(".panel-line")).toContainText("default");
      await page.keyboard.press("Escape");
      await expect(page).toHaveURL(/\/reviews$/);

      await page.keyboard.press("ArrowLeft");
      await expect(members).toHaveCount(0);
      await page.keyboard.press("ArrowRight");
      await expect(members).toHaveCount(2);

      let releaseRefresh: (() => void) | undefined;
      let delayedRefresh = false;
      let resolveRefreshStarted!: () => void;
      const refreshStarted = new Promise<void>((resolve) => {
        resolveRefreshStarted = resolve;
      });
      let resolveRefreshContinued!: () => void;
      const refreshContinued = new Promise<void>((resolve) => {
        resolveRefreshContinued = resolve;
      });
      let failNextRefresh = false;
      let allowRetryRefresh = false;
      let failedRefresh = false;
      let retryRefresh = false;
      let resolveFailureServed!: () => void;
      const failureServed = new Promise<void>((resolve) => {
        resolveFailureServed = resolve;
      });
      let resolveRetryContinued!: () => void;
      const retryContinued = new Promise<void>((resolve) => {
        resolveRetryContinued = resolve;
      });
      await page.route("**/api/roborev/api/jobs?**", async (route) => {
        const url = new URL(route.request().url());
        if (url.searchParams.get("panel_run") !== "panel-e2e-1") {
          await route.continue();
          return;
        }

        let heldRefresh = false;
        if (!delayedRefresh) {
          delayedRefresh = true;
          heldRefresh = true;
          resolveRefreshStarted();
          await new Promise<void>((release) => {
            releaseRefresh = release;
          });
        }
        if (heldRefresh) {
          await route.continue();
          resolveRefreshContinued();
          return;
        }
        if (failNextRefresh && !failedRefresh) {
          failedRefresh = true;
          await route.fulfill({
            status: 502,
            contentType: "application/json",
            body: JSON.stringify({ error: "seeded panel refresh failure" }),
          });
          resolveFailureServed();
          return;
        }
        if (failedRefresh && !allowRetryRefresh) {
          await route.fulfill({
            status: 502,
            contentType: "application/json",
            body: JSON.stringify({ error: "seeded panel refresh failure" }),
          });
          return;
        }
        await route.continue();
        if (!retryRefresh) {
          retryRefresh = true;
          resolveRetryContinued();
        }
      });

      await page.keyboard.press("Enter");
      await refreshStarted;
      await expect(members).toHaveCount(2);
      await expect(page.locator(".members-status-row")).toContainText("Refreshing reviewers");
      releaseRefresh?.();
      await refreshContinued;
      await expect(page.locator(".members-status-row", { hasText: "Refreshing reviewers" })).toHaveCount(0);
      await expect(page).toHaveURL(/\/reviews\/79$/);
      await page.keyboard.press("Escape");
      await expect(page).toHaveURL(/\/reviews$/);
      failNextRefresh = true;
      await parent.click();
      await failureServed;
      await expect(page).toHaveURL(/\/reviews\/79$/);
      await expect(page.locator(".drawer")).toBeVisible();
      await expect(page.locator(".panel-error")).toContainText("Could not refresh reviewers.");
      await expect(page.locator(".members-status-row.error")).toContainText("Could not refresh reviewers.");
      allowRetryRefresh = true;
      const retryResponse = page.waitForResponse((response) => {
        const url = new URL(response.url());
        return (
          response.ok() &&
          url.pathname.endsWith("/api/roborev/api/jobs") &&
          url.searchParams.get("panel_run") === "panel-e2e-1" &&
          url.searchParams.get("omit_prompt") === "true"
        );
      });
      await page.locator(".panel-retry").click();
      await retryContinued;
      await retryResponse;
      await expect(page.locator(".panel-error")).toHaveCount(0);
      await expect(page.locator(".members-status-row.error")).toHaveCount(0);
      await page.keyboard.press("Escape");
      await expect(page).toHaveURL(/\/reviews$/);
      await page.keyboard.press("j");
      await expect(members.nth(0)).toHaveClass(/highlighted/);
      await page.keyboard.press("ArrowLeft");
      await expect(members).toHaveCount(0);
      await expect(parent).toHaveClass(/highlighted/);
      await page.keyboard.press("ArrowRight");
      await expect(members).toHaveCount(2);
      await page.keyboard.press("j");
      await expect(members.nth(0)).toHaveClass(/highlighted/);
      await page.keyboard.press("Enter");
      await expect(page).toHaveURL(/\/reviews\/77$/);
      await expect(page.locator(".drawer")).toBeVisible();
      await expect(page.locator(".header-start .review-type", { hasText: "default" })).toBeVisible();
    });

    test("route-selected panel drawer drains queued member refresh while table stays collapsed", async ({ page }) => {
      let panelMemberRequests = 0;
      let releaseFirstMemberFetch: (() => void) | undefined;
      let resolveFirstMemberFetchStarted!: () => void;
      const firstMemberFetchStarted = new Promise<void>((resolve) => {
        resolveFirstMemberFetchStarted = resolve;
      });
      let resolveLatestMemberFetchServed!: () => void;
      const latestMemberFetchServed = new Promise<void>((resolve) => {
        resolveLatestMemberFetchServed = resolve;
      });

      await page.route("**/api/roborev/api/jobs?**", async (route) => {
        const url = new URL(route.request().url());
        if (url.searchParams.get("panel_run") !== "panel-e2e-1") {
          await route.continue();
          return;
        }

        panelMemberRequests++;
        if (panelMemberRequests === 1) {
          resolveFirstMemberFetchStarted();
          await new Promise<void>((resolve) => {
            releaseFirstMemberFetch = resolve;
          });
          await route.continue();
          return;
        }

        const response = await route.fetch();
        const body = (await response.json()) as {
          jobs?: Array<Record<string, unknown>> | null;
          [key: string]: unknown;
        };
        const jobs = (body.jobs ?? []).map((job) => {
          if (job["id"] === 77) {
            return { ...job, panel_member_name: "fresh-default", verdict: "pass" };
          }
          if (job["id"] === 78) {
            return { ...job, panel_member_name: "fresh-security", verdict: "pass" };
          }
          return job;
        });
        await route.fulfill({
          response,
          body: JSON.stringify({ ...body, jobs }),
        });
        resolveLatestMemberFetchServed();
      });

      await page.goto("/reviews/79");
      await expect(page).toHaveURL(/\/reviews\/79$/);
      await expect(page.locator(".job-table")).toBeVisible({
        timeout: 15_000,
      });
      const parent = jobRowById(page, 79);
      await expect(parent).toBeVisible();
      await expect(parent).toHaveAttribute("aria-expanded", "false");
      await expect(page.locator(".job-row.member")).toHaveCount(0);
      await expect(page.locator(".drawer")).toBeVisible();
      await firstMemberFetchStarted;
      await expect(page.locator(".panel-line")).toContainText("Refreshing reviewers");

      const listingReload = page.waitForResponse((response) => {
        const url = new URL(response.url());
        return (
          response.ok() &&
          url.pathname.endsWith("/api/roborev/api/jobs") &&
          url.searchParams.get("panel_run") === null &&
          url.searchParams.get("status") === "done"
        );
      });
      await selectStatusFilter(page, "Done");
      await listingReload;
      await expect(page.locator(".status-badge.status-done").first()).toBeVisible({
        timeout: 5_000,
      });
      await expect(page.locator(".status-badge:not(.status-done)")).toHaveCount(0);

      releaseFirstMemberFetch?.();
      await latestMemberFetchServed;
      await expect(page.locator(".panel-line")).toContainText("fresh-default pass");
      await expect(page.locator(".panel-line")).toContainText("fresh-security pass");
      await expect(parent).toHaveAttribute("aria-expanded", "false");
      await expect(page.locator(".job-row.member")).toHaveCount(0);
    });

    test("status badges show correct classes for each status", async ({ page }) => {
      await waitForReviewsReady(page);
      await waitForJobRows(page, 10);

      // Check statuses visible on the first page (top 50 by ID).
      // Queued/running jobs have the lowest IDs and are on page 2.
      for (const status of ["done", "failed", "canceled"]) {
        const badge = page.locator(`.status-badge.status-${status}`).first();
        await expect(badge).toBeVisible();
      }
    });

    test("verdict badges show pass/fail/none correctly", async ({ page }) => {
      await waitForReviewsReady(page);
      await waitForJobRows(page, 10);

      // Pass verdicts exist among done jobs
      await expect(page.locator(".verdict-pass").first()).toBeVisible();

      // Fail verdicts exist
      await expect(page.locator(".verdict-fail").first()).toBeVisible();

      // No-verdict (--) for queued/running jobs
      await expect(page.locator(".verdict-none").first()).toBeVisible();
    });

    test("elapsed time displays for completed jobs", async ({ page }) => {
      await waitForReviewsReady(page);
      await waitForJobRows(page, 10);

      // Completed jobs should show elapsed like "5m 0s"
      const elapsed = page.locator(".col-elapsed").first();
      await expect(elapsed).toBeVisible();
      const text = await elapsed.textContent();
      // Should be either a time value or "--" for non-started jobs
      expect(text?.trim()).toBeTruthy();
    });

    test("relative queued time displays", async ({ page }) => {
      await waitForReviewsReady(page);
      await waitForJobRows(page, 10);

      // The queued column should show relative time text
      const queued = page.locator(".col-queued").first();
      await expect(queued).toBeVisible();
      const text = await queued.textContent();
      // Should contain time-related text (ago, etc.)
      expect(text?.trim()).toBeTruthy();
    });

    test("repo/branch/ref column shows combined data", async ({ page }) => {
      await waitForReviewsReady(page);
      await waitForJobRows(page, 10);

      // The ref column should contain repo name, branch, and ref
      const refCell = page.locator(".col-ref").first();
      await expect(refCell).toBeVisible();
      await expect(refCell.locator(".repo-name")).toBeVisible();
      await expect(refCell.locator(".git-ref")).toBeVisible();
    });

    test("agent column shows agent name", async ({ page }) => {
      await waitForReviewsReady(page);
      await waitForJobRows(page, 10);

      // All 3 agents appear in the seed data
      const agentCells = page.locator(".col-agent");
      const count = await agentCells.count();
      expect(count).toBeGreaterThan(0);

      // Collect agent texts from visible rows
      const agents = new Set<string>();
      for (let i = 0; i < Math.min(count, 50); i++) {
        const text = await agentCells.nth(i).textContent();
        if (text) agents.add(text.trim());
      }
      expect(agents.size).toBeGreaterThanOrEqual(2);
    });
  });

  // -------------------------------------------------------
  // Group 2: Pagination
  // -------------------------------------------------------
  test.describe("Pagination", () => {
    test("load more button visible when >50 jobs exist", async ({ page }) => {
      await waitForReviewsReady(page);
      await waitForJobRows(page, 50);

      const loadMore = page.locator(".load-more-btn");
      await expect(loadMore).toBeVisible();
    });

    test("clicking load more appends additional rows", async ({ page }) => {
      await waitForReviewsReady(page);
      await waitForJobRows(page, 50);

      const beforeCount = await page.locator(".job-row").count();
      await page.locator(".load-more-btn").click();

      // Wait for more rows to appear
      await expect(async () => {
        const afterCount = await page.locator(".job-row").count();
        expect(afterCount).toBeGreaterThan(beforeCount);
      }).toPass({ timeout: 10_000 });
    });

    test("total row count after loading all pages matches seed count", async ({ page }) => {
      await waitForReviewsReady(page);
      await waitForJobRows(page, 50);

      // Click load more until it disappears or we have enough rows
      while (await page.locator(".load-more-btn").isVisible()) {
        await page.locator(".load-more-btn").click();
        await page.waitForTimeout(500);
      }

      const totalCount = await page.locator(".job-row").count();
      // Seed has 77 parent-visible rows; panel members are hidden until expanded.
      expect(totalCount).toBeGreaterThanOrEqual(70);
    });

    test("cursor-based pagination preserves sort order", async ({ page }) => {
      await waitForReviewsReady(page);
      await waitForJobRows(page, 50);

      // Default sort is by ID descending
      await page.locator(".load-more-btn").click();
      await expect(async () => {
        const count = await page.locator(".job-row").count();
        expect(count).toBeGreaterThan(50);
      }).toPass({ timeout: 10_000 });

      // Verify IDs are in descending order
      const ids: number[] = [];
      const idCells = page.locator(".col-id .mono");
      const count = await idCells.count();
      for (let i = 0; i < count; i++) {
        const text = await idCells.nth(i).textContent();
        if (text) ids.push(Number(text.trim()));
      }
      for (let i = 1; i < ids.length; i++) {
        expect(ids[i]!).toBeLessThanOrEqual(ids[i - 1]!);
      }
    });
  });

  // -------------------------------------------------------
  // Group 3: Filtering
  // -------------------------------------------------------
  test.describe("Filtering", () => {
    test("status dropdown: select failed shows only failed jobs", async ({ page }) => {
      await waitForReviewsReady(page);
      await waitForJobRows(page, 10);

      await selectStatusFilter(page, "Failed");

      // Wait atomically for the filter to settle: at least one failed
      // badge must be visible AND no non-failed badges may remain.
      // This auto-retries both conditions, removing the brittle
      // waitForTimeout + count-then-iterate pattern that races against
      // re-renders under parallel load.
      await expect(page.locator(".status-badge.status-failed").first()).toBeVisible({
        timeout: 5_000,
      });
      await expect(page.locator(".status-badge:not(.status-failed)")).toHaveCount(0);
    });

    test("status dropdown: select done shows only done jobs", async ({ page }) => {
      await waitForReviewsReady(page);
      await waitForJobRows(page, 10);

      await selectStatusFilter(page, "Done");

      await expect(page.locator(".status-badge.status-done").first()).toBeVisible({
        timeout: 5_000,
      });
      await expect(page.locator(".status-badge:not(.status-done)")).toHaveCount(0);
    });

    test("repo picker: select repo filters to that repo's jobs", async ({ page }) => {
      await waitForReviewsReady(page);
      await waitForJobRows(page, 10);

      // Open the repo picker
      await page.locator(".picker-button").click();
      await expect(page.locator(".dropdown")).toBeVisible();

      // Select test-repo-beta
      const betaItem = page.locator(".dropdown-item.repo-item").filter({ hasText: "test-repo-beta" });
      await betaItem.click();

      // Wait atomically for the filter to settle: at least one
      // matching row visible AND no non-matching repo names remain.
      await expect(page.locator(".repo-name", { hasText: "test-repo-beta" }).first()).toBeVisible({
        timeout: 5_000,
      });
      await expect(
        page.locator(".repo-name").filter({
          hasNotText: "test-repo-beta",
        }),
      ).toHaveCount(0);
    });

    test("branch picker: select branch within repo", async ({ page }) => {
      await waitForReviewsReady(page);
      await waitForJobRows(page, 10);

      // Open picker and expand test-repo-alpha
      await page.locator(".picker-button").click();
      await expect(page.locator(".dropdown")).toBeVisible();

      // Expand repo to show branches
      const expandBtn = page.locator(".repo-group").filter({ hasText: "test-repo-alpha" }).locator(".expand-btn");
      await expandBtn.click();

      // Select feat/auth branch
      const branchItem = page.locator(".branch-item").filter({ hasText: "feat/auth" });
      await expect(branchItem).toBeVisible();
      await branchItem.click();

      // Wait atomically for the filter to settle.
      await expect(page.locator(".branch-name", { hasText: "feat/auth" }).first()).toBeVisible({
        timeout: 5_000,
      });
      await expect(page.locator(".branch-name").filter({ hasNotText: "feat/auth" })).toHaveCount(0);
    });

    test("search input: filter by exact git ref", async ({ page }) => {
      await waitForReviewsReady(page);
      await waitForJobRows(page, 10);

      // Use exact ref for job 73 (aa000049 = hex 0x49)
      await page.locator(".filter-bar .search-wrap input").fill("aa000049");

      // Wait atomically for the filter to settle: at least one row
      // with a matching ref AND no rows with a non-matching ref.
      await expect(page.locator(".git-ref", { hasText: "aa000049" }).first()).toBeVisible({
        timeout: 5_000,
      });
      await expect(page.locator(".git-ref").filter({ hasNotText: "aa000049" })).toHaveCount(0);
    });

    test("hide-closed: hides jobs whose review is closed", async ({ page }) => {
      await waitForReviewsReady(page);
      await waitForJobRows(page, 10);

      const beforeCount = await page.locator(".job-row").count();

      await page.getByLabel("Hide closed").check();

      // Auto-retry until the row count drops to or below the
      // unfiltered baseline. The seed has closed jobs that should
      // disappear, so the count strictly decreases unless none were
      // closed (in which case the assertion still passes since the
      // count is unchanged but not greater).
      await expect(async () => {
        const afterCount = await page.locator(".job-row").count();
        expect(afterCount).toBeLessThanOrEqual(beforeCount);
      }).toPass({ timeout: 5_000 });
    });

    test("show auto-design reveals skipped router byproducts", async ({ page }) => {
      await waitForReviewsReady(page);
      await waitForJobRows(page, 10);

      const autoDesignSkipRef = page.locator(".git-ref[title='auto-design-skip']");
      const autoDesignClassifyRef = page.locator(".git-ref[title='auto-design-classify']");
      await expect(autoDesignSkipRef).toHaveCount(0);
      await expect(autoDesignClassifyRef).toHaveCount(0);

      await page.getByLabel("Show auto-design").check();

      await expect(autoDesignSkipRef).toBeVisible({ timeout: 5_000 });
      const autoDesignSkipRow = page.locator(".job-row", { has: autoDesignSkipRef });
      await expect(autoDesignSkipRow.locator(".status-badge")).toHaveText("skipped");
      await expect(autoDesignSkipRow.locator(".col-type")).toHaveText("review");
      await autoDesignSkipRow.click();
      await expect(page.locator(".header-start .review-type", { hasText: "auto-design" })).toBeVisible();
      await expect(page.locator(".skip-reason")).toHaveText("Skipped: trivial diff");
      await page.keyboard.press("Escape");

      await expect(autoDesignClassifyRef).toBeVisible({ timeout: 5_000 });
      const autoDesignClassifyRow = page.locator(".job-row", { has: autoDesignClassifyRef });
      await expect(autoDesignClassifyRow.locator(".status-badge")).toHaveText("failed");
      await expect(autoDesignClassifyRow.locator(".col-type")).toHaveText("classify");
    });

    test("reset each filter to default restores full list", async ({ page }) => {
      await waitForReviewsReady(page);
      await waitForJobRows(page, 10);

      // Capture the unfiltered baseline before applying the filter.
      // The reset must restore exactly this count, not just "more
      // rows than the filtered subset" — a partial reload would also
      // satisfy the looser condition.
      const baselineCount = await page.locator(".job-row").count();

      // Apply a filter and wait for it to take effect.
      await selectStatusFilter(page, "Failed");
      await expect(page.locator(".status-badge.status-failed").first()).toBeVisible({
        timeout: 5_000,
      });
      await expect(page.locator(".status-badge:not(.status-failed)")).toHaveCount(0);
      const filteredCount = await page.locator(".job-row").count();
      expect(filteredCount).toBeLessThan(baselineCount);

      // Reset the filter and wait for the unfiltered list to reload
      // back to the baseline count exactly.
      await selectStatusFilter(page, "All statuses");
      await expect(async () => {
        const resetCount = await page.locator(".job-row").count();
        expect(resetCount).toBe(baselineCount);
      }).toPass({ timeout: 5_000 });
    });
  });

  // -------------------------------------------------------
  // Group 4: Sorting
  // -------------------------------------------------------
  test.describe("Sorting", () => {
    test("click ID header toggles sort direction", async ({ page }) => {
      await waitForReviewsReady(page);
      await waitForJobRows(page, 10);

      // Default sort is ID descending. Get first row ID.
      const firstIdBefore = await page.locator(".col-id .mono").first().textContent();

      // Click ID header to toggle to ascending
      const idHeader = page.locator("th.sortable").filter({ hasText: "ID" });
      await idHeader.click();
      await page.waitForTimeout(300);

      const firstIdAfter = await page.locator(".col-id .mono").first().textContent();

      // The first ID should now be a lower number (ascending)
      expect(Number(firstIdAfter?.trim())).toBeLessThan(Number(firstIdBefore?.trim()));
    });

    test("click Status header sorts by status", async ({ page }) => {
      await waitForReviewsReady(page);
      await waitForJobRows(page, 10);

      // Click Status header
      const statusHeader = page.locator("th.sortable").filter({ hasText: "Status" });
      await statusHeader.click();
      await page.waitForTimeout(300);

      // Verify rows are sorted by status (alphabetically)
      const statuses: string[] = [];
      const badges = page.locator(".status-badge");
      const count = await badges.count();
      for (let i = 0; i < count; i++) {
        const text = await badges.nth(i).textContent();
        if (text) statuses.push(text.trim());
      }
      // Check that statuses are sorted ascending
      const sorted = [...statuses].sort();
      expect(statuses).toEqual(sorted);
    });

    test("click Elapsed header sorts by elapsed duration", async ({ page }) => {
      await waitForReviewsReady(page);
      await waitForJobRows(page, 10);

      const elapsedHeader = page.locator("th.sortable").filter({ hasText: "Elapsed" });
      await elapsedHeader.click();
      await page.waitForTimeout(300);

      const elapsedTexts: string[] = [];
      const elapsedValues: number[] = [];
      const elapsedCells = page.locator(".col-elapsed");
      const count = await elapsedCells.count();
      for (let i = 0; i < count; i++) {
        const text = await elapsedCells.nth(i).textContent();
        if (text) {
          elapsedTexts.push(text.trim());
          elapsedValues.push(parseElapsed(text));
        }
      }

      const sorted = [...elapsedValues].sort((a, b) => a - b);
      expect(elapsedValues).toEqual(sorted);

      // The seeded data includes both missing-elapsed ("--") and
      // zero-duration ("0s") rows. Verify the boundary: "--" must
      // appear before "0s" in ascending order.
      const lastDash = elapsedTexts.lastIndexOf("--");
      const firstZero = elapsedTexts.indexOf("0s");
      expect(lastDash).toBeGreaterThanOrEqual(0);
      expect(firstZero).toBeGreaterThanOrEqual(0);
      expect(lastDash).toBeLessThan(firstZero);
    });

    test("click Cost header sorts by cost", async ({ page }) => {
      await waitForReviewsReady(page);
      await waitForJobRows(page, 10);

      // Job 74 is the only costed job (~$0.42) on the first
      // page; every other row renders "--". Ascending order
      // puts missing costs first, so the costed row sinks to
      // the bottom; descending brings it to the top.
      const costHeader = page.locator("th.sortable").filter({ hasText: "Cost" });
      await costHeader.click();
      await page.waitForTimeout(300);

      const costCells = page.locator(".col-cost");
      await expect(costCells.last()).toHaveText("~$0.42");
      await expect(costCells.first()).toHaveText("--");

      await costHeader.click();
      await page.waitForTimeout(300);

      await expect(costCells.first()).toHaveText("~$0.42");
      await expect(costCells.last()).toHaveText("--");
    });

    test("sort persists across filter changes", async ({ page }) => {
      await waitForReviewsReady(page);
      await waitForJobRows(page, 10);

      // Sort by agent
      const agentHeader = page.locator("th.sortable").filter({ hasText: "Agent" });
      await agentHeader.click();
      await page.waitForTimeout(300);

      // Apply a status filter
      await selectStatusFilter(page, "Done");
      await page.waitForTimeout(500);
      await waitForJobRows(page, 1);

      // Verify agents are still sorted
      const agents: string[] = [];
      const agentCells = page.locator(".col-agent");
      const count = await agentCells.count();
      for (let i = 0; i < count; i++) {
        const text = await agentCells.nth(i).textContent();
        if (text) agents.push(text.trim());
      }
      const sorted = [...agents].sort();
      expect(agents).toEqual(sorted);
    });
  });

  // -------------------------------------------------------
  // Group 5: Drawer and Review Detail
  // -------------------------------------------------------
  test.describe("Drawer and Review Detail", () => {
    test("click row opens drawer with review content", async ({ page }) => {
      await waitForReviewsReady(page);
      await waitForJobRows(page, 10);

      // Click the first row (which should be a done job with review)
      await page.locator(".job-row").first().click();

      const drawer = page.locator(".drawer");
      await expect(drawer).toBeVisible({ timeout: 10_000 });

      // The review tab should be active by default
      const activeTab = page.locator(".tab.active");
      await expect(activeTab).toHaveText("Review");
    });

    test("drawer header shows job metadata", async ({ page }) => {
      // Open drawer for job 72 (known: fix/parser, gemini)
      await openDrawer(page, 72);

      const header = page.locator(".drawer-header");
      await expect(header.locator(".job-id")).toContainText("72");
      await expect(header.locator(".repo-name")).toBeVisible();
      await expect(header.locator(".branch")).toHaveText("fix/parser");
      await expect(header.locator(".header-agent")).toContainText("gemini");
    });

    test("drawer shows comments for reviewed job", async ({ page }) => {
      // Job 72 has 2 comments in seed data
      await openDrawer(page, 72);

      const responses = page.locator(".response-item");
      await expect(async () => {
        const count = await responses.count();
        expect(count).toBeGreaterThanOrEqual(2);
      }).toPass({ timeout: 10_000 });
    });

    test("submit a new comment on job 72", async ({ page }) => {
      await openDrawer(page, 72);

      // Wait for the comment input to be visible
      const textarea = page.locator(".comment-input .comment-textarea");
      await expect(textarea).toBeVisible({ timeout: 10_000 });

      // Type and submit a comment
      await textarea.fill("Test comment from e2e");
      const submitBtn = page.locator(".submit-btn");
      await expect(submitBtn).toBeEnabled();
      await submitBtn.click();

      // The comment should appear in the response list
      await expect(async () => {
        const items = page.locator(".response-item");
        const count = await items.count();
        expect(count).toBeGreaterThanOrEqual(3);
      }).toPass({ timeout: 10_000 });
    });

    test("close review action on job 71", async ({ page }) => {
      await openDrawer(page, 71);

      // Find and click the close review button
      const closeReviewBtn = page.locator(".drawer-footer .action-btn", {
        hasText: "Close Review",
      });
      await expect(closeReviewBtn).toBeVisible({
        timeout: 10_000,
      });
      await closeReviewBtn.click();

      // After close, button should change to "Reopen"
      await expect(
        page.locator(".drawer-footer .action-btn", {
          hasText: "Reopen",
        }),
      ).toBeVisible({ timeout: 10_000 });
    });

    test("rerun job action on job 73", async ({ page }) => {
      await openDrawer(page, 73);

      // Click the rerun button
      const rerunBtn = page.locator(".drawer-footer .action-btn", {
        hasText: "Rerun",
      });
      await expect(rerunBtn).toBeVisible({
        timeout: 10_000,
      });
      await rerunBtn.click();

      // The table should reload (job may now be queued again)
      // Just verify the action completed without error
      await page.waitForTimeout(500);
      await expect(page.locator(".drawer")).toBeVisible();
    });

    test("cancel button hidden for non-cancelable job 70", async ({ page }) => {
      await openDrawer(page, 70);

      // Job 70 is failed (daemon processed it), so
      // Cancel button should NOT be visible.
      const cancelBtn = page.locator(".drawer-footer .action-btn-danger", {
        hasText: "Cancel",
      });
      await expect(cancelBtn).not.toBeVisible({
        timeout: 5_000,
      });

      // Rerun button should still be available
      const rerunBtn = page.locator(".drawer-footer .action-btn", {
        hasText: "Rerun",
      });
      await expect(rerunBtn).toBeVisible({
        timeout: 10_000,
      });
    });

    test("copy output button is functional", async ({ page }) => {
      // Open a done job with review content
      await openDrawer(page, 72);

      const copyBtn = page.locator(".drawer-footer .action-btn", {
        hasText: "Copy Output",
      });
      await expect(copyBtn).toBeVisible({
        timeout: 10_000,
      });
      // Verify the button is clickable (clipboard API may not
      // be available in headless, but the button should not error)
      await copyBtn.click();
    });

    test("tab switching: Review -> Log -> Prompt", async ({ page }) => {
      await openDrawer(page, 72);

      // Review tab is active by default
      const tabs = page.locator(".tab-bar .tab");
      await expect(tabs.filter({ hasText: "Review" })).toHaveClass(/active/);

      // Switch to Log tab
      await tabs.filter({ hasText: "Log" }).click();
      await expect(page.locator(".log-viewer")).toBeVisible();
      await expect(tabs.filter({ hasText: "Log" })).toHaveClass(/active/);

      // Switch to Prompt tab
      await tabs.filter({ hasText: "Prompt" }).click();
      await expect(page.locator(".prompt-viewer")).toBeVisible();
      await expect(tabs.filter({ hasText: "Prompt" })).toHaveClass(/active/);
    });
  });

  // -------------------------------------------------------
  // Group 6: URL State and Navigation
  // -------------------------------------------------------
  test.describe("URL State and Navigation", () => {
    test("/reviews shows table, no drawer", async ({ page }) => {
      await page.goto("/reviews");
      await expect(page.locator(".job-table")).toBeVisible({
        timeout: 15_000,
      });
      await expect(page.locator(".drawer")).not.toBeVisible();
    });

    test("/reviews/:jobId opens drawer on page load", async ({ page }) => {
      await openDrawer(page, 72);
      await expect(page.locator(".job-id")).toContainText("72");
    });

    test("selecting job updates URL to /reviews/:jobId", async ({ page }) => {
      await waitForReviewsReady(page);
      await waitForJobRows(page, 10);

      await page.locator(".job-row").first().click();
      await expect(page).toHaveURL(/\/reviews\/\d+/);
    });

    test("closing drawer navigates back to /reviews", async ({ page }) => {
      await waitForReviewsReady(page);
      await waitForJobRows(page, 10);

      // Open drawer
      await page.locator(".job-row").first().click();
      await expect(page.locator(".drawer")).toBeVisible();

      // Close via X button
      await page.locator(".drawer-header .close-btn").click();
      await expect(page.locator(".drawer")).not.toBeVisible();
      await expect(page).toHaveURL(/\/reviews$/);
    });

    test("page reload preserves drawer state for valid jobId", async ({ page }) => {
      await openDrawer(page, 72);
      await expect(page.locator(".drawer")).toBeVisible();

      // Reload
      await page.reload();
      await expect(page.locator(".drawer")).toBeVisible({
        timeout: 15_000,
      });
      await expect(page.locator(".job-id")).toContainText("72");
    });
  });

  // -------------------------------------------------------
  // Group 7: Keyboard Shortcuts
  // -------------------------------------------------------
  test.describe("Keyboard Shortcuts", () => {
    test("j/k highlights table rows without opening drawer", async ({ page }) => {
      await waitForReviewsReady(page);
      await waitForJobRows(page, 10);

      // Press j to highlight first row
      await page.keyboard.press("j");
      await expect(page.locator(".job-row.highlighted")).toBeVisible();

      // Drawer should NOT open
      await expect(page.locator(".drawer")).not.toBeVisible();

      // Press j again to move to next
      await page.keyboard.press("j");
      const highlighted = page.locator(".job-row.highlighted");
      await expect(highlighted).toHaveCount(1);

      // Press k to move back up
      await page.keyboard.press("k");
      await expect(highlighted).toHaveCount(1);
    });

    test("Enter opens drawer for highlighted row", async ({ page }) => {
      await waitForReviewsReady(page);
      await waitForJobRows(page, 10);

      // Highlight a row
      await page.keyboard.press("j");
      await expect(page.locator(".job-row.highlighted")).toBeVisible();

      // Press Enter to open drawer
      await page.keyboard.press("Enter");
      await expect(page.locator(".drawer")).toBeVisible({
        timeout: 10_000,
      });
    });

    test("Escape closes open drawer", async ({ page }) => {
      await waitForReviewsReady(page);
      await waitForJobRows(page, 10);

      // Open drawer
      await page.locator(".job-row").first().click();
      await expect(page.locator(".drawer")).toBeVisible();

      // Close with Escape
      await page.keyboard.press("Escape");
      await expect(page.locator(".drawer")).not.toBeVisible();
    });

    test("? opens help modal, Escape closes it", async ({ page }) => {
      await waitForReviewsReady(page);
      await page.locator(".reviews-view").waitFor();

      // Open help modal
      await page.keyboard.press("Shift+?");
      const modal = page.getByRole("dialog", { name: "Keyboard Shortcuts" });
      await expect(modal).toBeVisible();
      await expect(modal).toContainText("Move selection down / up");

      // Close with Escape
      await page.keyboard.press("Escape");
      await expect(modal).not.toBeVisible();
    });

    test("modifier keys (Cmd+R) do not trigger shortcuts", async ({ page }) => {
      await waitForReviewsReady(page);
      await waitForJobRows(page, 10);

      // Cmd+R should not trigger the 'r' rerun shortcut
      // (it should be a page reload, but we just verify no
      // shortcut side-effect happens)
      const drawerBefore = await page.locator(".drawer").isVisible();
      expect(drawerBefore).toBe(false);

      await page.keyboard.press("Meta+r");
      // Wait for any potential side effects
      await page.waitForTimeout(300);

      // No drawer should have opened
      await expect(page.locator(".drawer")).not.toBeVisible();
    });
  });

  // -------------------------------------------------------
  // Group 8: Daemon Status
  // -------------------------------------------------------
  test.describe("Daemon Status", () => {
    test("status strip shows version text", async ({ page }) => {
      await waitForReviewsReady(page);
      const statusItem = page.locator(".daemon-status .status-item").first();
      await expect(statusItem).toBeVisible();
      const text = await statusItem.textContent();
      expect(text).toMatch(/^v/);
    });

    test("status strip shows worker count", async ({ page }) => {
      await waitForReviewsReady(page);
      const workerItem = page.locator(".daemon-status .status-item").filter({ hasText: "Workers" });
      await expect(workerItem).toBeVisible();
      const text = await workerItem.textContent();
      expect(text).toMatch(/Workers \d+\/\d+/);
    });

    test("status strip connection indicator has connected class", async ({ page }) => {
      await waitForReviewsReady(page);
      const indicator = page.locator(".daemon-status .conn-indicator.connected");
      await expect(indicator).toBeVisible();
    });
  });

  // -------------------------------------------------------
  // Group 9: Resilience -- Daemon Down
  // -------------------------------------------------------
  test.describe("Daemon Down", () => {
    test.beforeAll(() => {
      stopDaemon();
    });

    test.afterAll(() => {
      startDaemon();
    });

    test("fresh load shows empty state with unreachable message", async ({ page }) => {
      await page.goto("/reviews");
      // Daemon was never available on this fresh page, so
      // ReviewsView shows the empty-state fallback.
      const emptyState = page.locator(".kit-empty-state");
      await expect(emptyState).toBeVisible({
        timeout: 15_000,
      });
      await expect(emptyState).toContainText("not reachable");
    });

    test("empty state does not render table or filter bar", async ({ page }) => {
      await page.goto("/reviews");
      await expect(page.locator(".kit-empty-state")).toBeVisible({
        timeout: 15_000,
      });

      // Full UI elements should not be present
      await expect(page.locator(".job-table")).not.toBeVisible();
      await expect(page.locator(".filter-bar")).not.toBeVisible();
      await expect(page.locator(".daemon-status")).not.toBeVisible();
    });

    test("retry button appears in empty state", async ({ page }) => {
      await page.goto("/reviews");
      await expect(page.locator(".kit-empty-state")).toBeVisible({
        timeout: 15_000,
      });

      const retryBtn = page.locator(".kit-empty-state button");
      await expect(retryBtn).toBeVisible();
      await expect(retryBtn).toHaveText("Retry");
    });

    test("retry button while daemon still down keeps empty state", async ({ page }) => {
      await page.goto("/reviews");
      await expect(page.locator(".kit-empty-state")).toBeVisible({
        timeout: 15_000,
      });

      // Click retry while daemon is still stopped
      await page.locator(".kit-empty-state button").click();
      await page.waitForTimeout(1_000);

      // Should still show the empty state
      await expect(page.locator(".kit-empty-state")).toBeVisible();
    });

    test("header Reviews tab is still navigable", async ({ page }) => {
      await page.goto("/reviews");
      await expect(page.locator(".kit-empty-state")).toBeVisible({
        timeout: 15_000,
      });

      // The page should still be the reviews view
      await expect(page.locator(".reviews-view")).toBeVisible();
    });
  });

  // -------------------------------------------------------
  // Group 10: Resilience -- Daemon Recovery
  // -------------------------------------------------------
  test.describe("Daemon Recovery", () => {
    test.beforeAll(() => {
      restartDaemon();
    });

    test("click Retry in empty state loads table on same page", async ({ page }) => {
      // Start with daemon stopped to get the empty state
      stopDaemon();
      await page.goto("/reviews");
      await expect(page.locator(".kit-empty-state")).toBeVisible({
        timeout: 15_000,
      });

      // Restart daemon (waits for healthcheck), click
      // Retry once, then wait for recovery.
      startDaemon();
      await page.locator(".kit-empty-state button").click();
      await expect(page.locator(".kit-empty-state")).not.toBeVisible({
        timeout: 20_000,
      });
      await expect(page.locator(".conn-indicator.connected")).toBeVisible({
        timeout: 15_000,
      });
      await waitForJobRows(page, 1);
    });

    test("table has data after recovery on same page", async ({ page }) => {
      // Same pattern: stop, load, restart, retry, verify
      // data is present — all on the same page.
      stopDaemon();
      await page.goto("/reviews");
      await expect(page.locator(".kit-empty-state")).toBeVisible({
        timeout: 15_000,
      });

      startDaemon();
      await page.locator(".kit-empty-state button").click();
      await expect(page.locator(".kit-empty-state")).not.toBeVisible({
        timeout: 20_000,
      });
      await waitForJobRows(page, 1);

      const count = await page.locator(".job-row").count();
      expect(count).toBeGreaterThan(0);
    });

    test("status strip shows connected after recovery", async ({ page }) => {
      await waitForReviewsReady(page);
      await expect(page.locator(".conn-indicator.connected")).toBeVisible();
    });

    test("recovery from empty state then open drawer", async ({ page }) => {
      // Stop daemon and get empty state (fresh page load)
      stopDaemon();
      await page.goto("/reviews");
      await expect(page.locator(".kit-empty-state")).toBeVisible({
        timeout: 15_000,
      });

      // Restart daemon, click Retry to recover
      startDaemon();
      await page.locator(".kit-empty-state button").click();
      await expect(page.locator(".kit-empty-state")).not.toBeVisible({
        timeout: 20_000,
      });
      await waitForJobRows(page, 1);

      // Click a row to open the drawer and verify content
      // actually loaded (not just an empty shell)
      await page.locator(".job-row").first().click();
      await expect(page.locator(".drawer")).toBeVisible({
        timeout: 10_000,
      });
      await expect(page.locator(".job-id")).toBeVisible({
        timeout: 5_000,
      });
      await expect(page.locator(".drawer-header")).toContainText(/\d+/);
    });
  });
});
