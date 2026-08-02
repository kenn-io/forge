import { expect, test } from "@playwright/test";

import { startIsolatedE2EServer } from "./support/e2eServer";

test("persisted Activity events appear in the open PR timeline after SSE invalidation", async ({ page }) => {
  const server = await startIsolatedE2EServer();
  try {
    const detailPath = "/api/v1/pulls/github/acme/widgets/1";
    let detailGetCount = 0;
    page.on("request", (request) => {
      if (request.method() === "GET" && new URL(request.url()).pathname === detailPath) {
        detailGetCount++;
      }
    });
    const streamRequested = page.waitForRequest((request) =>
      new URL(request.url()).pathname.endsWith("/api/v1/events"),
    );
    const initialSync = page.waitForResponse((response) => {
      const url = new URL(response.url());
      return response.request().method() === "POST" && url.pathname === `${detailPath}/sync/async`;
    });
    await page.goto(
      `${server.info.base_url}/?selected=pr:1&provider=github&platform_host=github.com&repo_path=acme%2Fwidgets`,
    );
    await streamRequested;

    const detail = page.locator(".activity-detail");
    const newComment = "Persisted live Activity comment";
    await expect(detail.locator(".pull-detail")).toBeVisible();
    await initialSync;
    await expect(detail.locator(".sync-indicator")).toHaveCount(0, { timeout: 15_000 });
    await expect(detail.getByText(newComment, { exact: true })).toHaveCount(0);
    const selectedURL = page.url();
    const settledDetailGetCount = detailGetCount;
    const refreshedDetail = page.waitForResponse(
      (response) => response.request().method() === "GET" && new URL(response.url()).pathname === detailPath,
      { timeout: 5_000 },
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
    expect(detailGetCount).toBeGreaterThan(settledDetailGetCount);

    await expect(detail.getByText(newComment, { exact: true })).toBeVisible();
    expect(page.url()).toBe(selectedURL);
  } finally {
    await server.stop();
  }
});

test("notification-hot PR fast sync refreshes the selected Activity detail", async ({ page }) => {
  const server = await startIsolatedE2EServer();
  try {
    const detailPath = "/api/v1/pulls/github/acme/widgets/1";
    const newComment = "Notification-driven fast-sync comment";
    const streamRequested = page.waitForRequest((request) =>
      new URL(request.url()).pathname.endsWith("/api/v1/events"),
    );
    const initialSync = page.waitForResponse((response) => {
      const url = new URL(response.url());
      return response.request().method() === "POST" && url.pathname === `${detailPath}/sync/async`;
    });
    await page.goto(
      `${server.info.base_url}/?selected=pr:1&provider=github&platform_host=github.com&repo_path=acme%2Fwidgets`,
    );
    await streamRequested;

    const detail = page.locator(".activity-detail");
    await expect(detail.locator(".pull-detail")).toBeVisible();
    await initialSync;
    await expect(detail.locator(".sync-indicator")).toHaveCount(0, { timeout: 15_000 });
    await expect(detail.getByText(newComment, { exact: true })).toHaveCount(0);

    const refreshedDetail = page.waitForResponse(
      (response) => response.request().method() === "GET" && new URL(response.url()).pathname === detailPath,
      { timeout: 10_000 },
    );
    const fastSync = await page.request.post(`${server.info.base_url}/__e2e/activity/notification-fast-sync`);
    expect(fastSync.status()).toBe(204);

    const storedDetail = await page.request.get(`${server.info.base_url}${detailPath}`);
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
  } finally {
    await server.stop();
  }
});
