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

test("threaded activity scrolls to older subjects", async ({ page }) => {
  await mockApi(page);
  await routeSubjects(page, 51, 1);
  await page.goto("/?view=threaded");
  const viewport = page.getByRole("region", { name: "Threaded activity" });
  await expect(page.getByText("Pull request 51", { exact: true })).toBeAttached();
  const olderRequest = page.waitForRequest(
    (request) => new URL(request.url()).searchParams.get("subject_before") === "older",
  );
  await expect(async () => {
    await viewport.evaluate((node) => {
      node.scrollTop = node.scrollHeight;
    });
    await expect(page.getByText("Pull request 52", { exact: true })).toBeVisible();
  }).toPass();
  await olderRequest;
  await viewport.evaluate((node) => {
    node.scrollTop = 300;
  });
  await expect.poll(() => viewport.evaluate((node) => node.scrollTop)).toBeGreaterThan(0);
  await page.getByRole("searchbox", { name: "Search activity" }).fill("needle");
  await expect.poll(() => viewport.evaluate((node) => node.scrollTop)).toBe(0);
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

test("flat activity scrolls to the next page", async ({ page }) => {
  await mockApi(page);
  await page.route("**/api/v1/activity**", async (route) => {
    const url = new URL(route.request().url());
    if (url.pathname !== "/api/v1/activity") return route.fallback();
    const limit = Math.min(Number(url.searchParams.get("limit")) || 200, 5000);
    await route.fulfill({
      json: {
        items: Array.from({ length: limit }, (_, i) => event(i + 1)),
        capped: limit < 400,
        item_activity: [],
        item_activity_capped: false,
      },
    });
  });
  await page.goto("/?view=flat");
  const viewport = page.getByRole("region", { name: "Activity feed" });
  await expect(page.getByText("Pull request 200", { exact: true })).toBeAttached();
  const nextPage = page.waitForRequest((request) => new URL(request.url()).searchParams.get("limit") === "400");
  await expect(async () => {
    await viewport.evaluate((node) => {
      node.scrollTop = node.scrollHeight;
    });
    await expect(page.getByText("Pull request 400", { exact: true })).toBeVisible({ timeout: 300 });
  }).toPass();
  await nextPage;
  await viewport.evaluate((node) => {
    node.scrollTop = 300;
  });
  await expect.poll(() => viewport.evaluate((node) => node.scrollTop)).toBeGreaterThan(0);
  await page.getByRole("searchbox", { name: "Search activity" }).fill("needle");
  await expect.poll(() => viewport.evaluate((node) => node.scrollTop)).toBe(0);
});
