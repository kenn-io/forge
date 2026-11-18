import { expect, test, type Page } from "@playwright/test";
import { activitySubject as subject, activityTime } from "./support/activityPaging";
import { mockApi } from "./support/mockApi";

const event = (number: number) => ({
  id: `comment:${number}`,
  cursor: `comment:${number}`,
  activity_type: "comment",
  author: "user-a",
  body_preview: `Comment ${number}`,
  created_at: activityTime,
  ...subject(number),
});

const routeSubjects = (page: Page, first: number, older: number) =>
  page.route("**/api/v1/activity**", async (route) => {
    const url = new URL(route.request().url());
    if (url.pathname.endsWith("/thread-events")) return route.fulfill({ json: { items: [], capped: false } });
    if (url.pathname !== "/api/v1/activity") return route.fallback();
    const next = url.searchParams.has("subject_before");
    await route.fulfill({
      json: {
        items: [],
        capped: false,
        item_activity: Array.from({ length: next ? older : first }, (_, i) => subject(i + 1 + (next ? first : 0))),
        item_activity_capped: !next,
        next_subject_cursor: next ? "" : "older",
      },
    });
  });

test("threaded activity scrolls past 5,000 subjects", async ({ page }) => {
  test.setTimeout(240_000);
  await mockApi(page);
  await routeSubjects(page, 5000, 1);
  await page.goto("/?view=threaded");
  const viewport = page.getByRole("region", { name: "Threaded activity" });
  await expect(page.getByText("Pull request 5000", { exact: true })).toBeAttached({ timeout: 180_000 });
  await expect(async () => {
    await viewport.evaluate((node) => {
      node.scrollTop = node.scrollHeight;
    });
    await expect(page.getByText("Pull request 5001", { exact: true })).toBeVisible();
  }).toPass({ timeout: 100_000 });
});

test("returns to a loaded older activity item", async ({ page }) => {
  await mockApi(page);
  await routeSubjects(page, 200, 100);
  await page.goto("/?view=threaded");
  const viewport = page.getByRole("region", { name: "Threaded activity" });
  await expect(async () => {
    await viewport.dispatchEvent("wheel");
    await viewport.evaluate((node) => {
      node.scrollTop = node.scrollHeight;
    });
    await expect(page.getByText("Pull request 250", { exact: true })).toBeAttached({ timeout: 300 });
  }).toPass();
  const older = page.getByText("Pull request 250", { exact: true });
  await older.scrollIntoViewIfNeeded();
  await page.getByRole("button", { name: "PRs", exact: true }).click();
  await page.goBack();
  await expect(older).toBeInViewport();
});

test("flat activity scrolls to its 5,000 event ceiling", async ({ page }) => {
  test.setTimeout(120_000);
  await mockApi(page);
  await page.route("**/api/v1/activity**", async (route) => {
    const url = new URL(route.request().url());
    if (url.pathname !== "/api/v1/activity") return route.fallback();
    const limit = Math.min(Number(url.searchParams.get("limit")) || 200, 5000);
    await route.fulfill({
      json: {
        items: Array.from({ length: limit }, (_, i) => event(i + 1)),
        capped: true,
        item_activity: [],
        item_activity_capped: false,
      },
    });
  });
  await page.goto("/?view=flat");
  const viewport = page.getByRole("region", { name: "Activity feed" });
  await expect(async () => {
    await viewport.evaluate((node) => {
      node.scrollTop = node.scrollHeight;
    });
    await expect(page.getByText(/Showing most recent 5,000 events/)).toBeVisible({ timeout: 300 });
  }).toPass({ timeout: 100_000 });
});
