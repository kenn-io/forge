import { expect, test } from "@playwright/test";
import { mockApi } from "./support/mockApi";

const repo = {
  provider: "github",
  platform_host: "github.com",
  platform_repo_id: 1007,
  owner: "org-a",
  name: "project-a",
  repo_path: "org-a/project-a",
};
const time = new Date().toISOString();
const subject = (number: number) => ({
  activity_at: time,
  item_number: number,
  item_state: "open",
  item_title: `Pull request ${number}`,
  item_type: "pr",
  item_url: `https://github.com/org-a/project-a/pull/${number}`,
  repo,
});
const event = (number: number) => ({
  id: `comment:${number}`,
  cursor: `comment:${number}`,
  activity_type: "comment",
  author: "user-a",
  body_preview: `Comment ${number}`,
  created_at: time,
  ...subject(number),
});

test("threaded activity scrolls past 5,000 subjects", async ({ page }) => {
  test.setTimeout(240_000);
  await mockApi(page);
  await page.route("**/api/v1/activity**", async (route) => {
    const url = new URL(route.request().url());
    if (url.pathname !== "/api/v1/activity") return route.fallback();
    const older = url.searchParams.has("subject_before");
    await route.fulfill({
      json: {
        items: [],
        capped: false,
        item_activity: older ? [subject(5001)] : Array.from({ length: 5000 }, (_, i) => subject(i + 1)),
        item_activity_capped: !older,
        next_subject_cursor: older ? "" : "older",
      },
    });
  });
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
  await page.route("**/api/v1/activity**", async (route) => {
    const url = new URL(route.request().url());
    if (url.pathname.endsWith("/thread-events")) return route.fulfill({ json: { items: [], capped: false } });
    if (url.pathname !== "/api/v1/activity") return route.fallback();
    const older = url.searchParams.has("subject_before");
    await route.fulfill({
      json: {
        items: [],
        capped: false,
        item_activity: Array.from({ length: older ? 100 : 200 }, (_, i) => subject(i + (older ? 201 : 1))),
        item_activity_capped: !older,
        next_subject_cursor: older ? "" : "older",
      },
    });
  });
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
