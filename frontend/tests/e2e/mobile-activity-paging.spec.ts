import { devices, expect, test } from "@playwright/test";
import { mockApi } from "./support/mockApi";

test.use({ ...devices["iPhone 13"], browserName: "chromium" });

test("keeps a visible preview while loading older items", async ({ page }) => {
  await mockApi(page);
  const repo = {
    provider: "github",
    platform_host: "github.com",
    platform_repo_id: 1007,
    owner: "acme",
    name: "widgets",
    repo_path: "acme/widgets",
  };
  const time = new Date().toISOString();
  const subject = (number: number) => ({
    activity_at: time,
    item_number: number,
    item_state: "open",
    item_title: `Pull request ${number}`,
    item_type: "pr",
    item_url: `https://github.com/acme/widgets/pull/${number}`,
    repo,
  });
  const event = {
    id: "comment:1",
    cursor: "comment:1",
    activity_type: "comment",
    author: "alex",
    body_preview: "Preview stays visible",
    created_at: time,
    item_number: 1,
    item_state: "open",
    item_title: "Pull request 1",
    item_type: "pr",
    item_url: "https://github.com/acme/widgets/pull/1",
    repo,
  };
  let hold = false;
  let release: (() => void) | undefined;
  let limit = 0;
  await page.route("**/api/v1/activity**", async (route) => {
    const url = new URL(route.request().url());
    if (url.pathname.endsWith("/thread-events")) {
      await route.fulfill({ json: { items: [event], capped: false } });
      return;
    }
    if (url.pathname !== "/api/v1/activity") return route.fallback();
    const older = url.searchParams.has("subject_before");
    limit = Number(url.searchParams.get("limit"));
    if (hold && older)
      await new Promise<void>((resolve) => {
        release = resolve;
      });
    await route.fulfill({
      json: {
        items: [],
        capped: false,
        item_activity: [subject(older ? 2 : 1)],
        item_activity_capped: !older,
        next_subject_cursor: older ? "" : "older",
      },
    });
  });
  try {
    await page.goto("/m?range=30d&view=threaded");
    const preview = page.getByRole("button", { name: /Comment alex/ });
    await expect(preview).toBeVisible();
    const viewport = page.locator(".kit-scrollbox__viewport");
    while (limit < 500) {
      const previous = limit;
      await expect
        .poll(async () => {
          await viewport.dispatchEvent("wheel");
          return limit;
        })
        .toBeGreaterThan(previous);
    }
    const button = page.getByRole("button", { name: "Load older items" });
    await expect(button).toBeVisible();
    hold = true;
    await button.click();
    await expect(page.getByRole("button", { name: "Loading more…" })).toBeDisabled();
    await expect(preview).toBeVisible();
    await expect.poll(() => Boolean(release)).toBe(true);
    hold = false;
    release?.();
    await expect(page.getByText("Pull request 2")).toBeVisible();
    await expect(preview).toBeVisible();
  } finally {
    release?.();
  }
});
