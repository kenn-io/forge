import { expect, test, type Page } from "@playwright/test";

const storageKey = "middleman-pr-timeline-filter";

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
  await page.locator(".pull-detail")
    .waitFor({ state: "visible", timeout: 10_000 });
  await expect(page.getByText("feat: add cache store")).toBeVisible();
  await expect(page.getByText("Cache entries now expire")).toBeVisible();
  await expect(page.getByText("Widget rendering broken on Safari"))
    .toBeVisible();
}

async function openPRTimelinePath(page: Page, path: string): Promise<void> {
  await gotoWithWebKitRetry(page, path);
  await page.locator(".pull-detail")
    .waitFor({ state: "visible", timeout: 10_000 });
}

async function openTimelineFilters(page: Page): Promise<void> {
  await page.locator('button[title="Filter PR activity"]').click();
  await expect(page.locator(".filter-dropdown")).toBeVisible();
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
    await page.evaluate((key) => {
      localStorage.removeItem(key);
    }, storageKey);
  });

  test("renders seeded commit and system timeline events", async ({ page }) => {
    await openPRTimeline(page);

    await expect(page.locator(".event-type", { hasText: "Force-pushed" }))
      .toHaveCount(2);
    await expect(page.getByText("abc4444 -> def5555")).toBeVisible();
    await expect(page.getByText("abc9999 -> def7777")).toBeVisible();
    await expect(page.locator(".event-type", { hasText: "Referenced" }))
      .toHaveCount(3);
    await expect(page.getByText("Widget rendering broken on Safari"))
      .toBeVisible();
    await expect(page.getByText("Title changed")).toBeVisible();
    await expect(page.getByText(
      '"Add widget cache" -> "Add widget caching layer"',
    )).toBeVisible();
    await expect(page.getByText("Base changed")).toBeVisible();
    await expect(page.getByText("develop -> main")).toBeVisible();
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
    await openTimelineFilters(page);
    const commitRow = cacheCommitRow(page);

    await page.getByRole("button", { name: "Commit details" }).click();
    await expect(commitRow.locator(".commit-title")).toHaveText("feat: add cache store");
    await expect(commitRow.locator(".commit-body-details")).toHaveCount(0);
    await page.getByRole("button", { name: "Commit details" }).click();
    await expect(commitRow.locator(".commit-title")).toHaveCount(0);
    await expect(commitRow.locator(".commit-body-details"))
      .toContainText("Cache entries now expire");

    await page.getByRole("button", { name: "Events" }).click();
    await expect(page.getByText("Widget rendering broken on Safari"))
      .not.toBeVisible();
    await expect(page.getByText(
      '"Add widget cache" -> "Add widget caching layer"',
    )).not.toBeVisible();
    await expect(page.getByText("develop -> main")).not.toBeVisible();
    await page.getByRole("button", { name: "Events" }).click();
    await expect(page.getByText("Widget rendering broken on Safari"))
      .toBeVisible();

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
    await openTimelineFilters(page);

    await page.getByRole("button", { name: "Events" }).click();
    await expect(page.getByText("Widget rendering broken on Safari"))
      .not.toBeVisible();
    await expect(page.locator('button[title="Filter PR activity"]'))
      .toContainText("1");

    await expect.poll(async () =>
      await page.evaluate((key) => localStorage.getItem(key), storageKey),
    ).toContain('"showEvents":false');

    await page.reload();
    await page.locator(".pull-detail")
      .waitFor({ state: "visible", timeout: 10_000 });
    await expect(page.getByText("Widget rendering broken on Safari"))
      .not.toBeVisible();
    await expect(page.locator('button[title="Filter PR activity"]'))
      .toContainText("1");
  });

  test("keeps commit rows when other event buckets are hidden", async ({ page }) => {
    await openPRTimeline(page);
    await openTimelineFilters(page);

    await page.getByRole("button", { name: "Messages" }).click();
    await page.getByRole("button", { name: "Commit details" }).click();
    await page.getByRole("button", { name: "Events" }).click();
    await page.getByRole("button", { name: "Force pushes" }).click();
    const commitRow = cacheCommitRow(page);

    await expect(commitRow.locator(".commit-title")).toHaveText("feat: add cache store");
    await expect(commitRow.locator(".commit-body-details")).toHaveCount(0);
    await expect(page.getByText("No activity matches the current filters"))
      .not.toBeVisible();
  });
});
