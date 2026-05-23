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

async function openTimelineFilters(page: Page): Promise<void> {
  await page.locator('button[title="Filter PR activity"]').click();
  await expect(page.locator(".filter-dropdown")).toBeVisible();
}

function cacheCommitRow(page: Page) {
  return page.locator(".event--compact", { hasText: "abc1111" }).first();
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

    await expect(page.getByText("Force-pushed")).toBeVisible();
    await expect(page.getByText("abc4444 -> def5555")).toBeVisible();
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
