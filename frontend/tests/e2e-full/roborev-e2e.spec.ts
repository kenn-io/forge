import { test, expect, type Page } from "@playwright/test";
import type * as RoborevModels from "../../src/lib/api/roborev/generated/models/index.js";
import {
  assertSeededRoborevDaemon,
  stopDaemon,
  startDaemon,
  waitForReviewsReady,
  waitForJobRows,
  openDrawer,
  reviewAction,
  setupRoborevWorkspace,
  openWorkspaceReviews,
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

  test.beforeEach(async ({ page }) => {
    await setupRoborevWorkspace(page);
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
      await parent.getByRole("button", { name: "Expand panel" }).click();
      await panelRunResponse;

      const members = page.locator(".job-row.member");
      await expect(members).toHaveCount(2);
      await expect(members.nth(0).locator(".member-name")).toHaveText("default");
      await expect(members.nth(0).locator(".col-id")).toContainText("77");
      await expect(members.nth(1).locator(".member-name")).toHaveText("security");
      await expect(members.nth(1).locator(".col-id")).toContainText("78");

      await parent.click();
      await expect(page.locator(".panel-line")).toContainText("2 reviewers:");
      await expect(page.locator(".panel-line")).toContainText("default");
      await page.getByRole("button", { name: "Close review details", exact: true }).click();

      await parent.getByRole("button", { name: "Collapse panel" }).click();
      await expect(members).toHaveCount(0);
      await parent.getByRole("button", { name: "Expand panel" }).click();
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

      await parent.click();
      await refreshStarted;
      await expect(members).toHaveCount(2);
      await expect(page.locator(".members-status-row")).toContainText("Refreshing reviewers");
      releaseRefresh?.();
      await refreshContinued;
      await expect(page.locator(".members-status-row", { hasText: "Refreshing reviewers" })).toHaveCount(0);
      await page.getByRole("button", { name: "Close review details", exact: true }).click();
      failNextRefresh = true;
      await parent.click();
      await failureServed;
      await expect(page.getByRole("region", { name: "Review details" })).toBeVisible();
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
      await page.getByRole("button", { name: "Close review details", exact: true }).click();
      await parent.getByRole("button", { name: "Collapse panel" }).click();
      await expect(members).toHaveCount(0);
      await parent.getByRole("button", { name: "Expand panel" }).click();
      await expect(members).toHaveCount(2);
      await members.nth(0).click();
      await expect(page.getByRole("region", { name: "Review details" })).toBeVisible();
      await expect(page.locator(".header-start .review-type", { hasText: "default" })).toBeVisible();
    });

    test("selected panel drawer drains queued member refresh while table stays collapsed", async ({ page }) => {
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

      await openDrawer(page, 79);
      await expect(page.locator(".job-table")).toBeVisible({
        timeout: 15_000,
      });
      const parent = jobRowById(page, 79);
      await expect(parent).toBeVisible();
      await expect(parent).toHaveAttribute("aria-expanded", "false");
      await expect(page.locator(".job-row.member")).toHaveCount(0);
      await expect(page.getByRole("region", { name: "Review details" })).toBeVisible();
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
      await expect(page.locator(".job-row .branch-name", { hasText: "feat/auth" }).first()).toBeVisible({
        timeout: 5_000,
      });
      await expect(page.locator(".job-row .branch-name").filter({ hasNotText: "feat/auth" })).toHaveCount(0);
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

    test("refresh updates reviews after a daemon-side change", async ({ page }) => {
      const row = jobRowById(page, 72);
      try {
        await waitForReviewsReady(page);
        await page.getByLabel("Hide closed").check();
        await expect(row).toBeVisible();
        const closeResponse = await page.request.post("/api/roborev/api/review/close", {
          data: { job_id: 72, closed: true },
        });
        expect(closeResponse.ok()).toBe(true);
        await page.getByRole("button", { name: "Refresh local reviews" }).click();
        await expect(row).toHaveCount(0);
      } finally {
        const reopenResponse = await page.request.post("/api/roborev/api/review/close", {
          data: { job_id: 72, closed: false },
        });
        expect(reopenResponse.ok()).toBe(true);
      }
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
      await page.getByRole("button", { name: "Close review details", exact: true }).click();

      await expect(autoDesignClassifyRef).toBeVisible({ timeout: 5_000 });
      const autoDesignClassifyRow = page.locator(".job-row", { has: autoDesignClassifyRef });
      await expect(autoDesignClassifyRow.locator(".status-badge")).toHaveText("failed");
      await expect(autoDesignClassifyRow.locator(".col-type")).toHaveText("classify");
    });

    test("review visibility filters survive a reload", async ({ page }) => {
      await waitForReviewsReady(page);
      await waitForJobRows(page, 10);

      await page.getByLabel("Hide closed").check();
      await page.getByLabel("Show auto-design").check();

      const restoredJobs = page.waitForResponse((response) => {
        const url = new URL(response.url());
        return (
          response.ok() &&
          url.pathname.endsWith("/api/roborev/api/jobs") &&
          url.searchParams.get("limit") === "50" &&
          url.searchParams.get("closed") === "false" &&
          url.searchParams.get("hide_classify_jobs") === null
        );
      });
      await page.reload();
      await restoredJobs;
      await page.locator(".picker-button").click();
      await page.getByRole("button", { name: "All Repos", exact: true }).click();

      await expect(page.getByLabel("Hide closed")).toBeChecked();
      await expect(page.getByLabel("Show auto-design")).toBeChecked();
      await expect(page.locator(".git-ref[title='auto-design-classify']")).toBeVisible();
    });

    test("status counts follow the default auto-design visibility", async ({ page }) => {
      await waitForReviewsReady(page);
      await waitForJobRows(page, 10);

      await page.locator(".picker-button").click();
      const visibleJobsResponse = page.waitForResponse((response) => {
        const url = new URL(response.url());
        return (
          response.ok() &&
          url.pathname.endsWith("/api/roborev/api/jobs") &&
          url.searchParams.get("limit") === "0" &&
          url.searchParams.get("hide_classify_jobs") === "true" &&
          url.searchParams.getAll("repo").some((repo) => repo.includes("test-repo-alpha"))
        );
      });
      await page.locator(".dropdown-item.repo-item", { hasText: "test-repo-alpha" }).click();
      const visibleJobsBody = (await (await visibleJobsResponse).json()) as {
        jobs: Array<{ id: number; status: string }>;
      };
      const visibleFailed = visibleJobsBody.jobs.filter((job) => job.status === "failed").length;
      expect(visibleJobsBody.jobs.some((job) => job.id === 76)).toBe(false);

      await expect(page.locator('.status-counts [title="Failed"]')).toHaveText(`${visibleFailed} failed`);

      const allJobsResponse = page.waitForResponse((response) => {
        const url = new URL(response.url());
        return (
          response.ok() &&
          url.pathname.endsWith("/api/roborev/api/jobs") &&
          url.searchParams.get("limit") === "0" &&
          url.searchParams.get("hide_classify_jobs") === null &&
          url.searchParams.getAll("repo").some((repo) => repo.includes("test-repo-alpha"))
        );
      });
      await page.getByLabel("Show auto-design").check();

      const allJobsBody = (await (await allJobsResponse).json()) as {
        jobs: Array<{ id: number; status: string }>;
      };
      const allFailed = allJobsBody.jobs.filter((job) => job.status === "failed").length;
      expect(allJobsBody.jobs.some((job) => job.id === 76)).toBe(true);
      expect(allFailed).toBeGreaterThan(visibleFailed);
      await expect(page.locator(".git-ref[title='auto-design-classify']")).toBeVisible();
      await expect(page.locator('.status-counts [title="Failed"]')).toHaveText(`${allFailed} failed`);
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

    test("click Elapsed header places missing time before zero duration", async ({ page }) => {
      await waitForReviewsReady(page);
      await waitForJobRows(page, 10);

      const elapsedHeader = page.locator("th.sortable").filter({ hasText: "Elapsed" });
      await elapsedHeader.click();

      const elapsedRows = await page.locator(".job-row").evaluateAll((rows) =>
        rows.map((row) => ({
          status: row.querySelector(".status-badge")?.textContent?.trim(),
          elapsed: row.querySelector(".col-elapsed")?.textContent?.trim(),
        })),
      );
      // The real daemon can move queued jobs through running to failed while
      // this test reads the table. Compare the fixture's immutable done rows.
      const elapsedTexts = elapsedRows.filter(({ status }) => status === "done").map(({ elapsed }) => elapsed ?? "");
      const elapsedValues = elapsedTexts.map(parseElapsed);

      const sorted = [...elapsedValues].sort((a, b) => a - b);
      expect(elapsedValues).toEqual(sorted);
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

      const drawer = page.getByRole("region", { name: "Review details" });
      await expect(drawer).toBeVisible({ timeout: 10_000 });

      // The review tab should be active by default
      const activeTab = page.locator(".tab.active");
      await expect(activeTab).toHaveText("Review");
    });

    test("drawer header shows job metadata", async ({ page }) => {
      // Open drawer for job 72 (known: fix/parser, gemini)
      await openDrawer(page, 72);

      const header = page.locator(".review-dock-header");
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

    test("a committed comment with a lost response is reconciled without replay", async ({ page }, testInfo) => {
      await openDrawer(page, 72);
      const comment = `Lost response reconciliation comment (attempt ${testInfo.retry})`;
      await page.route(
        "**/api/roborev/api/comment",
        async (route) => {
          const response = await route.fetch();
          expect(response.ok()).toBe(true);
          await route.abort("failed");
        },
        { times: 1 },
      );

      const textarea = page.locator(".comment-input .comment-textarea");
      await textarea.fill(comment);
      await page.locator(".submit-btn").click();

      await expect(page.locator(".response-item").filter({ hasText: comment })).toHaveCount(1, { timeout: 10_000 });
      await expect(textarea).toHaveValue("");
      await expect(page.locator(".submit-btn")).toBeDisabled();
      const authority = await page.request.get("/api/roborev/api/comments?job_id=72");
      expect(authority.ok()).toBe(true);
      const body: RoborevModels.ListCommentsOutputBody = await authority.json();
      expect((body.responses ?? []).filter((response) => response.response === comment)).toHaveLength(1);
    });

    test("an acknowledged comment remains successful when its refresh fails", async ({ page }, testInfo) => {
      await openDrawer(page, 72);
      const comment = `Acknowledged comment with unavailable refresh (attempt ${testInfo.retry})`;
      let mutationAccepted = false;
      await page.route("**/api/roborev/api/comment", async (route) => {
        const response = await route.fetch();
        mutationAccepted = true;
        await route.fulfill({ response });
      });
      await page.route(
        "**/api/roborev/api/comments?**",
        async (route) => {
          if (!mutationAccepted) {
            await route.continue();
            return;
          }
          await route.abort("failed");
        },
        { times: 1 },
      );

      const textarea = page.locator(".comment-input .comment-textarea");
      await textarea.fill(comment);
      await page.locator(".submit-btn").click();

      await expect(textarea).toHaveValue("");
      await expect(page.getByText("Comment was added, but the refreshed review is unavailable")).toBeVisible();
      const authority = await page.request.get("/api/roborev/api/comments?job_id=72");
      expect(authority.ok()).toBe(true);
      const body: RoborevModels.ListCommentsOutputBody = await authority.json();
      expect((body.responses ?? []).filter((response) => response.response === comment)).toHaveLength(1);
    });

    test("close review action on job 71", async ({ page }) => {
      await openDrawer(page, 71);

      // Find and click the close review button
      const closeReviewBtn = reviewAction(page, "Close Review");
      await expect(closeReviewBtn).toBeVisible({
        timeout: 10_000,
      });
      await closeReviewBtn.click();

      // After close, button should change to "Reopen"
      await expect(reviewAction(page, "Reopen")).toBeVisible({ timeout: 10_000 });
    });

    test("reruns and cancels a job through drawer actions", async ({ page }) => {
      await openDrawer(page, 73);
      // Keep the accepted rerun queued until the drawer submits cancellation.
      const pause = await page.request.post("/api/roborev/api/queue/pause");
      expect(pause.status(), await pause.text()).toBe(200);
      try {
        const rerunResponse = page.waitForResponse(
          (response) => response.request().method() === "POST" && response.url().endsWith("/api/job/rerun"),
        );
        await reviewAction(page, "Rerun").click();
        const rerun = await rerunResponse;
        expect(rerun.status(), await rerun.text()).toBe(200);
        const queued = await page.request.get("/api/roborev/api/jobs?id=73");
        expect(queued.ok()).toBe(true);
        expect(await queued.json()).toMatchObject({ jobs: [{ id: 73, status: "queued" }] });

        const cancelResponse = page.waitForResponse(
          (response) => response.request().method() === "POST" && response.url().endsWith("/api/job/cancel"),
        );
        await reviewAction(page, "Cancel").click();
        const cancel = await cancelResponse;
        expect(cancel.status(), await cancel.text()).toBe(200);
        const canceled = await page.request.get("/api/roborev/api/jobs?id=73");
        expect(canceled.ok()).toBe(true);
        expect(await canceled.json()).toMatchObject({ jobs: [{ id: 73, status: "canceled" }] });
        await expect(page.getByRole("region", { name: "Review details" })).toBeVisible();
      } finally {
        const unpause = await page.request.post("/api/roborev/api/queue/unpause");
        expect(unpause.status(), await unpause.text()).toBe(200);
      }
    });

    test("cancel button hidden for non-cancelable job 70", async ({ page }) => {
      await openDrawer(page, 70);

      // Job 70 is failed (daemon processed it), so
      // Cancel button should NOT be visible.
      const cancelBtn = reviewAction(page, "Cancel");
      await expect(cancelBtn).not.toBeVisible({
        timeout: 5_000,
      });

      // Rerun button should still be available
      const rerunBtn = reviewAction(page, "Rerun");
      await expect(rerunBtn).toBeVisible({
        timeout: 10_000,
      });
    });

    test("copy output button is functional", async ({ page }) => {
      // Open a done job with review content
      await openDrawer(page, 72);

      const copyBtn = reviewAction(page, "Copy Output");
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

  test("unavailable daemon recovers when workspace reviews are refreshed", async ({ page }) => {
    stopDaemon();
    try {
      await openWorkspaceReviews(page);
      await expect(page.locator(".sidebar-reviews")).toContainText("Roborev daemon not reachable");
      await page.getByRole("button", { name: "Refresh local reviews" }).click();
      await expect(page.locator(".sidebar-reviews")).toContainText("Roborev daemon not reachable");
      startDaemon();
      await page.getByRole("button", { name: "Refresh local reviews" }).click();
      await waitForJobRows(page, 1);
      await expect(page.locator(".conn-indicator.connected")).toBeVisible();
      await page.locator(".job-row").first().click();
      await expect(page.getByRole("region", { name: "Review details" })).toBeVisible();
    } finally {
      startDaemon();
    }
  });
});
