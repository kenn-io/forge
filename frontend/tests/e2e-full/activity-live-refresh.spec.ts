import { expect, test, type Page } from "@playwright/test";

import { startIsolatedE2EServer } from "./support/e2eServer";

const detailPath = "/api/v1/pulls/github/acme/widgets/1";
const selectedActivityRoute = "/?selected=pr:1&provider=github&platform_host=github.com&repo_path=acme%2Fwidgets";

async function persistActivityComment(
  page: Page,
  baseURL: string,
  body: string,
  requireSubscriber = true,
): Promise<number> {
  const query = new URLSearchParams({ body });
  if (!requireSubscriber) query.set("require_subscriber", "false");
  const response = await page.request.post(`${baseURL}/__e2e/activity/pr-comment?${query}`);
  expect(response.status(), await response.text()).toBe(204);
  const eventID = Number(response.headers()["x-kenn-e2e-event-id"]);
  expect(eventID).toBeGreaterThan(0);
  return eventID;
}

test("persisted Activity events appear in the open PR timeline after SSE invalidation", async ({ page }) => {
  const server = await startIsolatedE2EServer();
  try {
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
    await page.goto(`${server.info.base_url}${selectedActivityRoute}`);
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

test("viewed-hot PR fast sync refreshes Activity without a notification", async ({ page }) => {
  const server = await startIsolatedE2EServer();
  try {
    const detailPath = "/api/v1/pulls/github/acme/widgets/1";
    const newComment = "Viewed hot fast-sync comment";
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
    const fastSync = await page.request.post(`${server.info.base_url}/__e2e/activity/viewed-hot-fast-sync`);
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

test("replays missed Activity changes after provider-store handoff", async ({ page }) => {
  const server = await startIsolatedE2EServer();
  try {
    const eventURLs: string[] = [];
    let detailGetCount = 0;
    page.on("request", (request) => {
      const url = new URL(request.url());
      if (url.pathname.endsWith("/api/v1/events")) eventURLs.push(url.toString());
      if (request.method() === "GET" && url.pathname === detailPath) detailGetCount++;
    });

    await page.goto(`${server.info.base_url}${selectedActivityRoute}`);
    const detail = page.locator(".activity-detail");
    await expect(detail.locator(".pull-detail")).toBeVisible();
    await expect.poll(() => eventURLs.length).toBeGreaterThanOrEqual(1);
    await expect(detail.locator(".sync-indicator")).toHaveCount(0, { timeout: 15_000 });

    const acceptedComment = "Accepted before provider handoff";
    const acceptedEventID = await persistActivityComment(page, server.info.base_url, acceptedComment);
    await expect(detail.getByText(acceptedComment, { exact: true })).toBeVisible();
    const preHandoffDetailResponse = await page.request.get(`${server.info.base_url}${detailPath}`);
    expect(preHandoffDetailResponse.ok()).toBe(true);
    const preHandoffDetail = await preHandoffDetailResponse.json();

    await page.evaluate(() => window.dispatchEvent(new Event("beforeunload")));
    await page.evaluate(() => {
      history.pushState(null, "", "/workspaces/embed/empty/noSelection");
      window.dispatchEvent(new PopStateEvent("popstate"));
    });
    await expect(page.getByText("Select a workspace from the sidebar", { exact: true })).toBeVisible();

    const missedComment = "Persisted while provider stores were replaced";
    const missedEventID = await persistActivityComment(page, server.info.base_url, missedComment, false);
    expect(missedEventID).toBeGreaterThan(acceptedEventID);
    const detailReadsBeforeReturn = detailGetCount;
    let returnDetailReads = 0;
    await page.route(`**${detailPath}`, async (route) => {
      if (route.request().method() !== "GET" || returnDetailReads > 0) {
        await route.continue();
        return;
      }
      returnDetailReads++;
      await route.fulfill({
        status: 200,
        contentType: "application/json",
        body: JSON.stringify(preHandoffDetail),
      });
    });
    await page.route("**/sync/async", async (route) => {
      await route.fulfill({ status: 202 });
    });

    await page.evaluate((path) => {
      history.pushState(null, "", path);
      window.dispatchEvent(new PopStateEvent("popstate"));
    }, selectedActivityRoute);

    await expect.poll(() => eventURLs.length).toBeGreaterThanOrEqual(2);
    const resumedEventURL = eventURLs.at(-1);
    expect(resumedEventURL).toBeDefined();
    if (resumedEventURL === undefined) throw new Error("provider event stream did not reconnect");
    const resumedURL = new URL(resumedEventURL);
    expect(resumedURL.searchParams.get("since")).toBe(String(acceptedEventID));
    await expect.poll(() => detailGetCount).toBeGreaterThan(detailReadsBeforeReturn + 1);
    await expect(page.locator(".activity-detail").getByText(missedComment, { exact: true })).toBeVisible();
  } finally {
    await server.stop();
  }
});

test("notification-warm PR fast sync refreshes the selected Activity detail", async ({ page }) => {
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

test("replays an event whose detail consequence failed in transit", async ({ page }) => {
  const server = await startIsolatedE2EServer();
  try {
    let eventRequestCount = 0;
    const eventURLs: string[] = [];
    let failDetailReads = false;
    let failedDetailReadCount = 0;
    page.on("request", (request) => {
      if (new URL(request.url()).pathname.endsWith("/api/v1/events")) {
        eventRequestCount++;
        eventURLs.push(request.url());
      }
    });
    await page.route(`**${detailPath}`, async (route) => {
      if (failDetailReads) {
        failedDetailReadCount++;
        await route.abort("connectionfailed");
        return;
      }
      await route.continue();
    });

    const initialSync = page.waitForResponse((response) => {
      const url = new URL(response.url());
      return response.request().method() === "POST" && url.pathname === `${detailPath}/sync/async`;
    });
    await page.goto(`${server.info.base_url}${selectedActivityRoute}`);
    const detail = page.locator(".activity-detail");
    await expect(detail.locator(".pull-detail")).toBeVisible();
    await expect.poll(() => eventRequestCount).toBeGreaterThanOrEqual(1);
    await initialSync;
    await expect(detail.locator(".sync-indicator")).toHaveCount(0, { timeout: 15_000 });

    const checkpointComment = "Accepted checkpoint before retry";
    const checkpointEventID = await persistActivityComment(page, server.info.base_url, checkpointComment);
    await expect(detail.getByText(checkpointComment, { exact: true })).toBeVisible();

    failDetailReads = true;
    const replayedComment = "Replayed after transient detail failure";
    const replayedEventID = await persistActivityComment(page, server.info.base_url, replayedComment);
    expect(replayedEventID).toBeGreaterThan(checkpointEventID);
    await expect.poll(() => failedDetailReadCount).toBeGreaterThan(0);
    await expect.poll(() => eventRequestCount, { timeout: 15_000 }).toBeGreaterThanOrEqual(2);
    expect(new URL(eventURLs.at(-1) ?? server.info.base_url).searchParams.get("since")).toBe(String(checkpointEventID));
    failDetailReads = false;

    await expect(detail.getByText(replayedComment, { exact: true })).toBeVisible({ timeout: 15_000 });
  } finally {
    await server.stop();
  }
});
test("keeps provider events active when a foreground pull query supersedes event reconciliation", async ({ page }) => {
  const server = await startIsolatedE2EServer();
  let releaseHeldPullRead = () => {};
  try {
    let eventRequestCount = 0;
    let holdNextPullRead = false;
    const pullReadHeld = new Promise<void>((resolve) => {
      releaseHeldPullRead = resolve;
    });
    let markPullReadHeld = () => {};
    const heldPullReadStarted = new Promise<void>((resolve) => {
      markPullReadHeld = resolve;
    });
    let holding = false;
    let heldRequestURL = "";

    page.on("request", (request) => {
      if (new URL(request.url()).pathname.endsWith("/api/v1/events")) eventRequestCount++;
    });
    await page.route("**/api/v1/pulls?**", async (route) => {
      const requestURL = new URL(route.request().url());
      if (holdNextPullRead && !holding && requestURL.searchParams.get("starred") !== "true") {
        holding = true;
        heldRequestURL = route.request().url();
        markPullReadHeld();
        await pullReadHeld;
      }
      await route.continue();
    });

    await page.goto(`${server.info.base_url}/pulls`);
    await expect(page.locator(".pull-item").first()).toBeVisible();
    await expect.poll(() => eventRequestCount).toBeGreaterThanOrEqual(1);

    holdNextPullRead = true;
    await persistActivityComment(page, server.info.base_url, "Event refresh held behind foreground query");
    await heldPullReadStarted;

    const foregroundResponse = page.waitForResponse((response) => {
      const url = new URL(response.url());
      return (
        response.request().method() === "GET" &&
        url.pathname === "/api/v1/pulls" &&
        url.searchParams.get("starred") === "true"
      );
    });
    await page.locator(".star-filter-btn").click();
    await foregroundResponse;
    const releasedEventRead = page.waitForResponse(
      (response) => response.request().method() === "GET" && response.url() === heldRequestURL,
    );
    releaseHeldPullRead();
    await releasedEventRead;

    const nextEventRefresh = page.waitForResponse((response) => {
      const url = new URL(response.url());
      return (
        response.request().method() === "GET" &&
        url.pathname === "/api/v1/pulls" &&
        url.searchParams.get("starred") === "true"
      );
    });
    await persistActivityComment(page, server.info.base_url, "Event refresh after foreground query");
    await nextEventRefresh;
    expect(eventRequestCount).toBe(1);
  } finally {
    releaseHeldPullRead();
    await server.stop();
  }
});
