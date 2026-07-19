import { expect, test } from "@playwright/test";

import { startIsolatedE2EServer } from "./support/e2eServer";

test("persisted Activity events appear in the open PR timeline after SSE invalidation", async ({ page }) => {
  const server = await startIsolatedE2EServer();
  try {
    const streamRequested = page.waitForRequest((request) =>
      new URL(request.url()).pathname.endsWith("/api/v1/events"),
    );
    await page.goto(
      `${server.info.base_url}/?selected=pr:1&provider=github&platform_host=github.com&repo_path=acme%2Fwidgets`,
    );
    await streamRequested;

    const detail = page.locator(".activity-detail");
    const newComment = "Persisted live Activity comment";
    await expect(detail.locator(".pull-detail")).toBeVisible();
    await expect(detail.getByText(newComment, { exact: true })).toHaveCount(0);
    const selectedURL = page.url();
    const refreshedDetail = page.waitForResponse(
      (response) =>
        response.request().method() === "GET" &&
        new URL(response.url()).pathname.endsWith("/api/v1/pulls/github/acme/widgets/1"),
    );

    const persisted = await page.request.post(`${server.info.base_url}/__e2e/activity/pr-comment`);
    expect(persisted.status()).toBe(204);

    const storedDetail = await page.request.get(`${server.info.base_url}/api/v1/pulls/github/acme/widgets/1`);
    expect(storedDetail.ok()).toBe(true);
    const storedBody = (await storedDetail.json()) as {
      events?: Array<{ Body?: string }>;
    };
    expect(storedBody.events?.some((event) => event.Body === newComment)).toBe(true);

    const refreshedBody = (await (await refreshedDetail).json()) as {
      events?: Array<{ Body?: string }>;
    };
    expect(refreshedBody.events?.some((event) => event.Body === newComment)).toBe(true);

    await expect(detail.getByText(newComment, { exact: true })).toBeVisible();
    expect(page.url()).toBe(selectedURL);
  } finally {
    await server.stop();
  }
});
