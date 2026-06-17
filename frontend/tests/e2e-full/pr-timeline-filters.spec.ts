import { expect, test, type Locator, type Page } from "@playwright/test";
import { startIsolatedE2EServer } from "./support/e2eServer";

const storageKey = "middleman-pr-timeline-filter";
const activityViewStorageKey = "middleman-detail-activity-view";

async function gotoWithWebKitRetry(page: Page, url: string): Promise<void> {
  let lastError: unknown;
  for (let attempt = 0; attempt < 3; attempt += 1) {
    try {
      await page.goto(url);
      return;
    } catch (error) {
      const message = error instanceof Error ? error.message : String(error);
      if (!message.includes("WebKit encountered an internal error")) {
        throw error;
      }
      lastError = error;
      await page.waitForTimeout(250);
    }
  }
  throw lastError;
}

async function openPRTimeline(page: Page): Promise<void> {
  await gotoWithWebKitRetry(page, "/pulls/github/acme/widgets/1");
  await page.locator(".pull-detail").waitFor({ state: "visible", timeout: 10_000 });
  await expect(page.getByText("feat: add cache store")).toBeVisible();
  await expect(page.getByText("Cache entries now expire")).toBeVisible();
  await expect(page.getByText("Widget rendering broken on Safari")).toBeVisible();
}

async function openPRTimelinePath(page: Page, path: string): Promise<void> {
  await gotoWithWebKitRetry(page, path);
  await page.locator(".pull-detail").waitFor({ state: "visible", timeout: 10_000 });
}

async function openIssueTimeline(page: Page): Promise<void> {
  await gotoWithWebKitRetry(page, "/issues/github/acme/widgets/10");
  await page.locator(".issue-detail").waitFor({ state: "visible", timeout: 10_000 });
  await expect(page.locator(".issue-detail .detail-title")).toContainText("Widget rendering broken on Safari");
}

async function openActivityViewMenu(
  page: Page,
  surface: ".pull-detail" | ".issue-detail" = ".pull-detail",
): Promise<Locator> {
  await page.locator(surface).getByRole("button", { name: "View", exact: true }).click();
  const menu = page.locator(".filter-dropdown");
  await expect(menu).toBeVisible();
  return menu;
}

function cacheCommitRow(page: Page) {
  return page.locator(".event--compact", { hasText: "abc1111" }).first();
}

async function expectTimelineTextOrder(page: Page, labels: string[]): Promise<void> {
  const timeline = page.locator(".timeline");
  await expect(timeline).toBeVisible();
  for (const label of labels) {
    await expect(timeline).toContainText(label);
  }

  const positions = await timeline.evaluate((element, expectedLabels) => {
    const text = element.textContent ?? "";
    return expectedLabels.map((label) => text.indexOf(label));
  }, labels);

  expect(positions.every((position) => position >= 0)).toBe(true);
  expect(positions).toEqual([...positions].sort((a, b) => a - b));
}

test.describe("PR timeline filters", () => {
  test.beforeEach(async ({ page }) => {
    await gotoWithWebKitRetry(page, "/");
    await page.evaluate(
      ({ filterKey, viewKey }) => {
        localStorage.removeItem(filterKey);
        localStorage.removeItem(viewKey);
      },
      { filterKey: storageKey, viewKey: activityViewStorageKey },
    );
  });

  test("renders seeded commit and system timeline events", async ({ page }) => {
    await openPRTimeline(page);

    await expect(page.locator(".event-type", { hasText: "Force-pushed" })).toHaveCount(2);
    await expect(page.getByText("abc4444 -> def5555")).toBeVisible();
    await expect(page.getByText("abc9999 -> def7777")).toBeVisible();
    await expect(page.locator(".event-type", { hasText: "Referenced" })).toHaveCount(3);
    await expect(page.getByText("Widget rendering broken on Safari")).toBeVisible();
    await expect(page.getByText("Title changed")).toBeVisible();
    await expect(page.getByText('"Add widget cache" -> "Add widget caching layer"')).toBeVisible();
    await expect(page.getByText("Base changed")).toBeVisible();
    await expect(page.getByText("develop -> main")).toBeVisible();
  });

  test("renders merged lifecycle transitions as one purple row", async ({ page }) => {
    await openPRTimelinePath(page, "/pulls/github/acme/tools/2");

    const mergedRows = page.locator(".event--compact", { hasText: "Merged" });
    await expect(mergedRows).toHaveCount(1);
    await expect(mergedRows.first()).toContainText("alice");
    await expect(mergedRows.first()).toContainText("merged this");
    await expect(mergedRows.first().locator(".event-type", { hasText: "Merged" })).toHaveAttribute(
      "style",
      /var\(--accent-purple\)/,
    );
    await expect(mergedRows.first().locator(".dot")).toHaveAttribute("style", /var\(--accent-purple\)/);
    await expect(page.locator(".event--compact", { hasText: "Closed" })).toHaveCount(0);
  });

  test("orders force-push commit generations through the seeded timeline", async ({ page }) => {
    await openPRTimeline(page);

    await expectTimelineTextOrder(page, [
      "Base changed",
      "chore: tune cache eviction metrics",
      "Title changed",
      "fix: finish cache rebase after follow-up force push",
      "abc9999 -> def7777",
      "Same timestamp reviewer note between force-push IDs.",
      "fix: guard nil cache after rebase",
      "abc4444 -> def5555",
      "fix: guard nil cache before rebase",
    ]);
  });

  test("orders fresh-import force-push commits without the old anchor commit", async ({ page }) => {
    await openPRTimelinePath(page, "/pulls/github/acme/widgets/2");

    await expectTimelineTextOrder(page, [
      "fix: guard widget race after import",
      "test: reproduce widget race after import",
      "2222aaa -> 2222ccc",
    ]);
  });

  test("keeps commit rows while hiding and restoring system event buckets", async ({ page }) => {
    await openPRTimeline(page);
    await openActivityViewMenu(page);
    const commitRow = cacheCommitRow(page);

    await page.getByRole("button", { name: "Commit details" }).click();
    await expect(commitRow.locator(".commit-title")).toHaveText("feat: add cache store");
    await expect(commitRow.locator(".commit-body-details")).toHaveCount(0);
    await page.getByRole("button", { name: "Commit details" }).click();
    await expect(commitRow.locator(".commit-title")).toHaveCount(0);
    await expect(commitRow.locator(".commit-body-details")).toContainText("Cache entries now expire");

    await page.getByRole("button", { name: "Events" }).click();
    await expect(page.getByText("Widget rendering broken on Safari")).not.toBeVisible();
    await expect(page.getByText('"Add widget cache" -> "Add widget caching layer"')).not.toBeVisible();
    await expect(page.getByText("develop -> main")).not.toBeVisible();
    await page.getByRole("button", { name: "Events" }).click();
    await expect(page.getByText("Widget rendering broken on Safari")).toBeVisible();

    await page.getByRole("button", { name: "Force pushes" }).click();
    await expect(page.getByText("abc4444 -> def5555")).not.toBeVisible();
    await expect(page.getByText("abc9999 -> def7777")).not.toBeVisible();
    await expectTimelineTextOrder(page, [
      "fix: finish cache rebase after follow-up force push",
      "fix: guard nil cache after rebase",
      "fix: guard nil cache before rebase",
    ]);
    await page.getByRole("button", { name: "Force pushes" }).click();
    await expect(page.getByText("abc4444 -> def5555")).toBeVisible();
  });

  test("persists timeline filter preferences in localStorage", async ({ page }) => {
    await openPRTimeline(page);
    await openActivityViewMenu(page);

    await page.getByRole("button", { name: "Events" }).click();
    await expect(page.getByText("Widget rendering broken on Safari")).not.toBeVisible();
    await expect(page.locator('button[title="View and filter activity"]')).toContainText("1");

    await expect
      .poll(async () => await page.evaluate((key) => localStorage.getItem(key), storageKey))
      .toContain('"showEvents":false');

    await page.reload();
    await page.locator(".pull-detail").waitFor({ state: "visible", timeout: 10_000 });
    await expect(page.getByText("Widget rendering broken on Safari")).not.toBeVisible();
    await expect(page.locator('button[title="View and filter activity"]')).toContainText("1");
  });

  test("keeps commit rows when other event buckets are hidden", async ({ page }) => {
    await openPRTimeline(page);
    const filters = await openActivityViewMenu(page);

    await filters.getByRole("button", { name: "Messages" }).click();
    await filters.getByRole("button", { name: "Commit details" }).click();
    await filters.getByRole("button", { name: "Events" }).click();
    await filters.getByRole("button", { name: "Force pushes" }).click();
    const commitRow = cacheCommitRow(page);

    await expect(commitRow.locator(".commit-title")).toHaveText("feat: add cache store");
    await expect(commitRow.locator(".commit-body-details")).toHaveCount(0);
    await expect(page.getByText("No activity matches the current filters")).not.toBeVisible();
  });

  test("persists compact activity layout across PR and issue detail views", async ({ page }) => {
    await openPRTimeline(page);
    const menu = await openActivityViewMenu(page);

    await menu.getByRole("button", { name: "Commit details" }).click();
    await menu.getByRole("button", { name: "Compact" }).click();

    await expect(page.locator(".pull-detail .event-card--compact-row").first()).toBeVisible();
    const compactCrossReference = page.locator(".pull-detail .event-card--compact-row", {
      hasText: "Add CLI flag parser",
    });
    const compactCrossReferenceLink = compactCrossReference.getByRole("link", { name: "Add CLI flag parser" });
    await expect(compactCrossReferenceLink).toBeVisible();
    await expect(compactCrossReferenceLink).toHaveAttribute("href", "/pulls/github/acme/tools/1");
    await expect(compactCrossReferenceLink).toHaveClass(/item-ref/);
    await expect(compactCrossReferenceLink).toHaveAttribute("data-provider", "github");
    await expect(compactCrossReferenceLink).toHaveAttribute("data-platform-host", "github.com");
    await expect(compactCrossReferenceLink).toHaveAttribute("data-owner", "acme");
    await expect(compactCrossReferenceLink).toHaveAttribute("data-name", "tools");
    await expect(compactCrossReferenceLink).toHaveAttribute("data-repo-path", "acme/tools");
    await expect(compactCrossReferenceLink).toHaveAttribute("data-number", "1");
    const compactCommitRow = page.locator(".pull-detail .event-card--compact-row", {
      hasText: "feat: add cache store",
    });
    await expect(compactCommitRow).toBeVisible();
    await expect(compactCommitRow).not.toContainText("Cache entries now expire");
    await compactCommitRow.click();
    await expect(compactCommitRow).not.toContainText("Cache entries now expire");
    await openActivityViewMenu(page);
    await page.getByRole("button", { name: "Commit details" }).click();
    await compactCommitRow.click();
    await expect(compactCommitRow).toContainText("Cache entries now expire");
    await expect(page.locator(".pull-detail .event-card--compact-row", { hasText: "COMMENTED" })).toBeVisible();
    const reviewCommentRow = page.locator(".pull-detail .event-card--compact-row", {
      hasText: "Guard the cache fallback before returning",
    });
    const reviewCommentFollowUpRow = page.locator(".pull-detail .event-card--compact-row", {
      hasText: "Follow-up compact review context for the same thread.",
    });
    await expect(reviewCommentRow).toBeVisible();
    await expect(reviewCommentFollowUpRow).toBeVisible();
    await expect(reviewCommentRow).toContainText("Guard the cache fallback before returning");
    await expect(reviewCommentRow.getByRole("button", { name: "Copy comment" })).toBeVisible();
    await expect(page.getByText("Expanded context explains stale data handling.")).toHaveCount(0);
    await reviewCommentRow.click();
    await reviewCommentFollowUpRow.click();
    await expect(page.getByText("Expanded context explains stale data handling.")).toBeVisible();
    await expect(reviewCommentRow.getByRole("button", { name: "Reply" })).toBeVisible();
    await expect(reviewCommentFollowUpRow.getByRole("button", { name: "Reply" })).toBeVisible();
    await expect(page.locator(".pull-detail .thread-reply-panel")).toHaveCount(0);
    await reviewCommentFollowUpRow.getByRole("button", { name: "Reply" }).click();
    await expect(page.locator(".pull-detail .thread-reply-panel")).toHaveCount(1);
    await expect(reviewCommentRow.locator(".thread-reply-panel")).toHaveCount(0);
    await expect(reviewCommentFollowUpRow.locator(".thread-reply-panel")).toHaveCount(1);
    await expect
      .poll(async () => await page.evaluate((key) => localStorage.getItem(key), activityViewStorageKey))
      .toBe("compact");

    await openPRTimelinePath(page, "/pulls/github/acme/tools/2");
    await expect(page.locator(".pull-detail .event-card--compact-row").first()).toBeVisible();

    await openIssueTimeline(page);
    await expect(page.locator(".issue-detail").getByRole("button", { name: "View", exact: true })).toContainText(
      "Compact",
    );
    await openActivityViewMenu(page, ".issue-detail");
    await expect(page.locator(".filter-dropdown").getByRole("button", { name: "Messages" })).toHaveCount(0);
  });

  test("keeps normal reply composer open when refreshed detail regroups a review thread", async ({ page }) => {
    const server = await startIsolatedE2EServer();
    try {
      await gotoWithWebKitRetry(page, `${server.info.base_url}/pulls/github/acme/widgets/1`);
      await page.locator(".pull-detail").waitFor({ state: "visible", timeout: 10_000 });

      const threadCard = page.locator(".pull-detail .event-card--reply-inline", {
        hasText: "Regroup root review thread comment.",
      });
      await expect(threadCard).toBeVisible();
      await expect(threadCard).not.toContainText("Regroup reply added during detail refresh.");

      await threadCard.hover();
      const replyButton = threadCard.locator(".thread-reply-action--inline");
      await expect(replyButton).toBeAttached();
      await replyButton.click();
      const replyEditor = page.locator(".pull-detail .thread-reply-panel .comment-editor-input");
      await expect(replyEditor).toBeVisible();
      await replyEditor.fill("Draft survives regroup");
      await expect(replyEditor).toHaveText("Draft survives regroup");

      const response = await page.request.post(`${server.info.base_url}/__e2e/pr-review-thread-regroup/add-reply`);
      expect(response.ok()).toBe(true);

      const regroupedThreadCard = page.locator(".pull-detail .event-card", {
        hasText: "Regroup root review thread comment.",
      });
      await expect(regroupedThreadCard).toContainText("Regroup reply added during detail refresh.");
      await expect(page.locator(".pull-detail .thread-reply-panel")).toHaveCount(1);
      await expect(replyEditor).toHaveText("Draft survives regroup");
    } finally {
      await server.stop();
    }
  });
});
