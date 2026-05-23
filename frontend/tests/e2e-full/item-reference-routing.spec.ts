import { expect, test, type Page, type Response } from "@playwright/test";

async function openPRTimeline(page: Page): Promise<void> {
  await page.goto("/pulls/github/acme/widgets/1");
  await page.locator(".pull-detail")
    .waitFor({ state: "visible", timeout: 10_000 });
  await expect(page.locator(".pull-detail .detail-title"))
    .toContainText("Add widget caching layer");
}

function resolveResponse(page: Page, path: string): Promise<Response> {
  return page.waitForResponse((response) => {
    const url = new URL(response.url());
    return url.pathname === path
      && response.request().method() === "POST";
  });
}

test.describe("item references through the full stack", () => {
  test("routes tracked cross-repository timeline references internally", async ({ page }) => {
    await openPRTimeline(page);

    const response = resolveResponse(
      page,
      "/api/v1/repo/github/acme/tools/resolve/1",
    );
    await page.getByRole("link", { name: "Add CLI flag parser" }).click();

    await expect.poll(() => new URL(page.url()).pathname)
      .toBe("/pulls/github/acme/tools/1");
    await expect((await response).ok()).toBe(true);
    await expect(page.locator(".pull-detail .detail-title"))
      .toContainText("Add CLI flag parser");
  });

  test("opens provider fallback URLs for untracked timeline references", async ({ page, context }) => {
    await openPRTimeline(page);

    const response = resolveResponse(
      page,
      "/api/v1/repo/github/other/repo/resolve/77",
    );
    const popup = context.waitForEvent("page");
    await page.getByRole("link", { name: "External follow-up PR" }).click();

    expect((await popup).url()).toBe("https://github.com/other/repo/pull/77");
    await expect((await response).ok()).toBe(true);
    await expect.poll(() => new URL(page.url()).pathname)
      .toBe("/pulls/github/acme/widgets/1");
  });
});
