import { expect, test, type Page, type Response } from "@playwright/test";

const prTitle = "Add widget caching layer";
const issueTitle = "Widget rendering broken on Safari";

function detailResponse(page: Page, path: string): Promise<Response> {
  return page.waitForResponse((response) => {
    const url = new URL(response.url());
    return url.pathname === path && response.request().method() === "GET";
  });
}

function issueDetailResponse(page: Page, path: string): Promise<Response> {
  return page.waitForResponse((response) => {
    const url = new URL(response.url());
    return url.pathname === path && response.request().method() === "GET";
  });
}

async function expectPathname(page: Page, pathname: string): Promise<void> {
  await expect.poll(() => new URL(page.url()).pathname).toBe(pathname);
}

test.describe("routed item builders through the UI", () => {
  test("selecting a PR row routes to its API-backed detail", async ({ page }) => {
    await page.goto("/pulls");
    await page.locator(".pull-item").first().waitFor({ state: "visible", timeout: 10_000 });

    const detailLoaded = detailResponse(page, "/api/v1/pulls/github/acme/widgets/1");
    await page.locator(".pull-item").filter({ hasText: prTitle }).first().click();

    await expectPathname(page, "/pulls/github/acme/widgets/1");
    await expect(page.locator(".pull-detail .detail-title")).toContainText(prTitle);
    await expect((await detailLoaded).ok()).toBe(true);
  });

  test("selecting an issue row carries platform_host into route and detail API", async ({ page }) => {
    await page.goto("/issues");
    await page.locator(".issue-item").first().waitFor({ state: "visible", timeout: 10_000 });

    const detailLoaded = issueDetailResponse(page, "/api/v1/issues/github/acme/widgets/10");
    await page.locator(".issue-item").filter({ hasText: issueTitle }).first().click();

    await expectPathname(page, "/issues/github/acme/widgets/10");
    await expect(page.locator(".issue-detail .detail-title")).toContainText(issueTitle);
    await expect((await detailLoaded).ok()).toBe(true);
  });

  test("focus PR list routes selected rows to focus detail", async ({ page }) => {
    await page.goto("/focus/mrs?repo=acme%2Fwidgets");
    await page.locator(".focus-list .pull-item").first().waitFor({ state: "visible", timeout: 10_000 });

    const detailLoaded = detailResponse(page, "/api/v1/pulls/github/acme/widgets/1");
    await page.locator(".focus-list .pull-item").filter({ hasText: prTitle }).first().click();

    await expect(page).toHaveURL(/\/focus\/pulls\/github\/acme\/widgets\/1$/);
    await expect(page.locator(".focus-layout .pull-detail .detail-title")).toContainText(prTitle);
    await expect(page.locator(".app-header")).not.toBeAttached();
    await expect((await detailLoaded).ok()).toBe(true);
  });

  test("focus issue list routes selected rows with platform_host", async ({ page }) => {
    await page.goto("/focus/issues?repo=acme%2Fwidgets");
    await page.locator(".focus-list .issue-item").first().waitFor({ state: "visible", timeout: 10_000 });

    const detailLoaded = issueDetailResponse(page, "/api/v1/issues/github/acme/widgets/10");
    await page.locator(".focus-list .issue-item").filter({ hasText: issueTitle }).first().click();

    await expect(page).toHaveURL(/\/focus\/issues\/github\/acme\/widgets\/10$/);
    await expect(page.locator(".focus-layout .issue-detail .detail-title")).toContainText(issueTitle);
    await expect(page.locator(".app-header")).not.toBeAttached();
    await expect((await detailLoaded).ok()).toBe(true);
  });

  test("narrow canonical PR list routes selected rows to canonical detail", async ({ page }) => {
    await page.setViewportSize({ width: 390, height: 844 });
    await page.goto("/pulls");
    await page.locator(".focus-list .pull-item").first().waitFor({ state: "visible", timeout: 10_000 });

    const detailLoaded = detailResponse(page, "/api/v1/pulls/github/acme/widgets/1");
    await page.locator(".focus-list .pull-item").filter({ hasText: prTitle }).first().click();

    await expectPathname(page, "/pulls/github/acme/widgets/1");
    await expect(page.locator(".focus-layout .pull-detail .detail-title")).toContainText(prTitle);
    await expect((await detailLoaded).ok()).toBe(true);
  });

  test("narrow canonical issue list routes selected rows to canonical detail", async ({ page }) => {
    await page.setViewportSize({ width: 390, height: 844 });
    await page.goto("/issues");
    await page.locator(".focus-list .issue-item").first().waitFor({ state: "visible", timeout: 10_000 });

    const detailLoaded = issueDetailResponse(page, "/api/v1/issues/github/acme/widgets/10");
    await page.locator(".focus-list .issue-item").filter({ hasText: issueTitle }).first().click();

    await expectPathname(page, "/issues/github/acme/widgets/10");
    await expect(page.locator(".focus-layout .issue-detail .detail-title")).toContainText(issueTitle);
    await expect((await detailLoaded).ok()).toBe(true);
  });

  test("canonical PR detail swaps focus and desktop presentation as viewport resizes", async ({ page }) => {
    await page.setViewportSize({ width: 390, height: 844 });
    await page.goto("/pulls/github/acme/widgets/1");

    await expectPathname(page, "/pulls/github/acme/widgets/1");
    await expect(page.locator(".focus-layout:not(.focus-layout--phone) .pull-detail .detail-title")).toContainText(
      prTitle,
    );
    await expect(page.locator(".app-header")).toHaveCount(0);

    await page.setViewportSize({ width: 1200, height: 844 });

    await expectPathname(page, "/pulls/github/acme/widgets/1");
    await expect(page.locator(".focus-layout")).toHaveCount(0);
    await expect(page.locator(".app-header")).toBeVisible();
    await expect(page.locator(".pull-detail .detail-title")).toContainText(prTitle);
  });
});
