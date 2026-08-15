import { expect, test, type Page, type Request } from "@playwright/test";

import { startIsolatedE2EServer } from "./support/e2eServer";

const detailPath = "/api/v1/pulls/github/acme/widgets/1";
const selectedActivityRoute = "/?selected=pr:1&provider=github&platform_host=github.com&repo_path=acme%2Fwidgets";

const olderDetailSyncCases = [
  {
    itemType: "pr",
    number: 1,
    title: "Add widget caching layer",
    body: "Pull request detail activity older than the feed cursor",
    detailPath: "/api/v1/pulls/github/acme/widgets/1",
  },
  {
    itemType: "issue",
    number: 10,
    title: "Widget rendering broken on Safari",
    body: "Issue detail activity older than the feed cursor",
    detailPath: "/api/v1/issues/github/acme/widgets/10",
  },
] as const;

async function persistActivityComment(
  page: Page,
  baseURL: string,
  body: string,
  requireSubscriber = true,
  itemType: "pr" | "issue" = "pr",
): Promise<number> {
  const query = new URLSearchParams({ body, item_type: itemType });
  if (!requireSubscriber) query.set("require_subscriber", "false");
  const response = await page.request.post(`${baseURL}/__e2e/activity/item-comment?${query}`);
  expect(response.status(), await response.text()).toBe(204);
  const eventID = Number(response.headers()["x-kenn-e2e-event-id"]);
  expect(eventID).toBeGreaterThan(0);
  return eventID;
}

for (const item of olderDetailSyncCases) {
  test(`${item.itemType} detail sync immediately reconciles Activity older than its leading cursor`, async ({
    page,
  }, testInfo) => {
    testInfo.setTimeout(60_000);
    const server = await startIsolatedE2EServer();
    try {
      const staged = await page.request.post(
        `${server.info.base_url}/__e2e/activity/stage-older-detail-event?item_type=${item.itemType}`,
      );
      expect(staged.status(), await staged.text()).toBe(204);

      await page.route("**/api/v1/events**", async (route) => {
        await route.abort("connectionfailed");
      });

      let releaseDetailSync: (() => void) | undefined;
      const initialActivityLoaded = new Promise<void>((resolve) => {
        releaseDetailSync = resolve;
      });
      await page.route(`**${item.detailPath}/sync/async`, async (route) => {
        await initialActivityLoaded;
        await route.continue();
      });

      const isFullActivityRead = (request: Request) => {
        const url = new URL(request.url());
        return request.method() === "GET" && url.pathname === "/api/v1/activity" && !url.searchParams.has("after");
      };
      let activityReadsInFlight = 0;
      page.on("request", (request) => {
        if (isFullActivityRead(request)) activityReadsInFlight++;
      });
      page.on("requestfinished", (request) => {
        if (isFullActivityRead(request)) activityReadsInFlight--;
      });
      page.on("requestfailed", (request) => {
        if (isFullActivityRead(request)) activityReadsInFlight--;
      });

      const initialActivity = page.waitForResponse((response) => {
        const url = new URL(response.url());
        return (
          response.request().method() === "GET" && url.pathname === "/api/v1/activity" && !url.searchParams.has("after")
        );
      });
      const detailSyncAccepted = page.waitForResponse((response) => {
        const url = new URL(response.url());
        return response.request().method() === "POST" && url.pathname === `${item.detailPath}/sync/async`;
      });
      await page.goto(
        `${server.info.base_url}/?view=threaded&selected=${item.itemType}:${item.number}&provider=github&platform_host=github.com&repo_path=acme%2Fwidgets`,
      );

      const initialResponse = await initialActivity;
      const initialSnapshot = (await initialResponse.json()) as {
        items: Array<{ activity_type: string; body_preview: string }>;
      };
      expect(initialSnapshot.items[0]?.activity_type).toBe("default_branch_commit");
      expect(initialSnapshot.items.some((event) => event.body_preview === item.body)).toBe(false);

      const feed = page.locator(".activity-feed");
      const itemRow = feed.locator(".threaded-view .item-row", { hasText: item.title });
      await expect(itemRow).toBeVisible();
      await expect.poll(() => activityReadsInFlight).toBe(0);
      await page.waitForTimeout(250);
      await expect.poll(() => activityReadsInFlight).toBe(0);
      const reconciledActivity = page.waitForResponse(async (response) => {
        const url = new URL(response.url());
        if (
          response.request().method() !== "GET" ||
          url.pathname !== "/api/v1/activity" ||
          url.searchParams.has("after")
        ) {
          return false;
        }
        const snapshot = (await response.json()) as {
          items: Array<{ body_preview: string }>;
        };
        return snapshot.items.some((event) => event.body_preview === item.body);
      });
      releaseDetailSync?.();
      await detailSyncAccepted;

      await expect
        .poll(async () => {
          const response = await page.request.get(`${server.info.base_url}/api/v1/activity?since=2026-08-01T00:00:00Z`);
          const snapshot = (await response.json()) as {
            items: Array<{ body_preview: string }>;
          };
          return snapshot.items.some((event) => event.body_preview === item.body);
        })
        .toBe(true);
      await reconciledActivity;

      await expect(feed.getByText("fixture-bot", { exact: true })).toBeVisible({ timeout: 5_000 });
    } finally {
      await server.stop();
    }
  });
}

for (const item of olderDetailSyncCases) {
  test(`initial ${item.itemType} detail read reconciles already-persisted Activity behind the feed cursor`, async ({
    page,
  }, testInfo) => {
    testInfo.setTimeout(60_000);
    const server = await startIsolatedE2EServer();
    const releaseDetailSync = Promise.withResolvers<void>();
    try {
      await page.route("**/api/v1/events**", async (route) => {
        await route.abort("connectionfailed");
      });
      await page.route(`**${item.detailPath}/sync/async`, async (route) => {
        await releaseDetailSync.promise;
        await route.fulfill({ status: 202 });
      });

      const initialActivity = page.waitForResponse((response) => {
        const url = new URL(response.url());
        return (
          response.request().method() === "GET" && url.pathname === "/api/v1/activity" && !url.searchParams.has("after")
        );
      });
      await page.goto(`${server.info.base_url}/?view=threaded`);
      await initialActivity;

      const body = `Already-persisted ${item.itemType} Activity behind the feed cursor`;
      await persistActivityComment(page, server.info.base_url, body, false, item.itemType);

      const reconciledActivity = page.waitForResponse(
        async (response) => {
          const url = new URL(response.url());
          if (
            response.request().method() !== "GET" ||
            url.pathname !== "/api/v1/activity" ||
            url.searchParams.has("after")
          ) {
            return false;
          }
          const snapshot = (await response.json()) as { items: Array<{ body_preview: string }> };
          return snapshot.items.some((event) => event.body_preview === body);
        },
        { timeout: 5_000 },
      );

      const itemRow = page.locator(".activity-feed .threaded-view .item-row", {
        hasText: item.title,
      });
      const initialDetail = page.waitForResponse((response) => {
        const url = new URL(response.url());
        return response.request().method() === "GET" && url.pathname === item.detailPath;
      });
      await itemRow.click();

      const detailResponse = (await (await initialDetail).json()) as { events?: Array<{ Body?: string }> };
      expect(detailResponse.events?.some((event) => event.Body === body)).toBe(true);
      await reconciledActivity;
      await expect(page.locator(".activity-feed").getByText("fixture-bot", { exact: true })).toBeVisible();
    } finally {
      releaseDetailSync.resolve();
      await page.unrouteAll({ behavior: "ignoreErrors" }).catch(() => {});
      await server.stop();
    }
  });
}

test("a filtered newer event updates the parent ordering and timestamp", async ({ page }, testInfo) => {
  testInfo.setTimeout(60_000);
  const server = await startIsolatedE2EServer();
  try {
    const staged = await page.request.post(`${server.info.base_url}/__e2e/activity/stage-filtered-parent-recency`);
    expect(staged.status(), await staged.text()).toBe(204);

    const streamRequested = page.waitForRequest((request) => new URL(request.url()).pathname === "/api/v1/events");
    const initialActivity = page.waitForResponse((response) => {
      const url = new URL(response.url());
      return response.request().method() === "GET" && url.pathname === "/api/v1/activity";
    });
    await page.goto(
      `${server.info.base_url}/?view=threaded&range=30d&item_types=pr&event_types=none&notif=0&hide_branch=1`,
    );
    await streamRequested;
    await initialActivity;

    // Recency comes from the event ledger, not provider updated_at: widgets#1
    // was "updated" two hours ago but its newest event is days old, so the
    // dependabot pull opened six hours ago leads.
    const rows = page.locator(".activity-feed .threaded-view .item-row");
    await expect(rows.first()).toContainText("Bump lodash from 4.17.20 to 4.17.21");
    await expect(rows.first().locator(".cell--time")).toHaveText("6h ago");
    await expect(rows.filter({ hasText: "WIP: new dashboard layout" })).toHaveCount(0);

    const refreshStartedAt = Math.floor(Date.now() / 1000) * 1000;
    const refreshedActivity = page.waitForResponse(async (response) => {
      const url = new URL(response.url());
      if (response.request().method() !== "GET" || url.pathname !== "/api/v1/activity") return false;
      const snapshot = (await response.json()) as {
        item_activity: Array<{ item_number: number; activity_at: string }>;
      };
      return snapshot.item_activity.some(
        (subject) => subject.item_number === 6 && Date.parse(subject.activity_at) >= refreshStartedAt,
      );
    });
    const advanced = await page.request.post(`${server.info.base_url}/__e2e/activity/filtered-parent-recency`);
    expect(advanced.status(), await advanced.text()).toBe(204);
    const expectedActivityAt = advanced.headers()["x-kenn-e2e-parent-activity-at"];
    expect(expectedActivityAt).toBeDefined();

    const refreshedSnapshot = (await (await refreshedActivity).json()) as {
      items: Array<{
        activity_type: string;
        body_preview: string;
        item_number: number;
      }>;
      item_activity: Array<{ activity_at: string; item_number: number; item_title: string }>;
    };
    expect(
      refreshedSnapshot.items.some((activityItem) => activityItem.body_preview === "Filtered parent comment"),
    ).toBe(false);
    expect(refreshedSnapshot.items.some((activityItem) => activityItem.item_number === 6)).toBe(false);
    const updatedParent = refreshedSnapshot.item_activity.find((subject) => subject.item_number === 6);
    expect(updatedParent?.item_title).toBe("WIP: new dashboard layout");
    expect(Date.parse(updatedParent?.activity_at ?? "")).toBe(Date.parse(expectedActivityAt ?? ""));

    await expect(rows.first()).toContainText("WIP: new dashboard layout");
    await expect(rows.first().locator(".cell--time")).toHaveText("just now");
  } finally {
    await server.stop();
  }
});

test("accepted detail sync reconciles Activity after its selection closes", async ({ page }, testInfo) => {
  testInfo.setTimeout(60_000);
  const server = await startIsolatedE2EServer();
  const releaseSync = () => page.request.post(`${server.info.base_url}/__e2e/activity/release-older-detail-event-sync`);
  try {
    const item = olderDetailSyncCases[0];
    const staged = await page.request.post(
      `${server.info.base_url}/__e2e/activity/stage-older-detail-event?item_type=pr&hold_sync=true`,
    );
    expect(staged.status(), await staged.text()).toBe(204);

    await page.route("**/api/v1/events**", async (route) => {
      await route.abort("connectionfailed");
    });

    const initialActivity = page.waitForResponse((response) => {
      const url = new URL(response.url());
      return (
        response.request().method() === "GET" && url.pathname === "/api/v1/activity" && !url.searchParams.has("after")
      );
    });
    const detailSyncAccepted = page.waitForResponse((response) => {
      const url = new URL(response.url());
      return response.request().method() === "POST" && url.pathname === `${item.detailPath}/sync/async`;
    });
    await page.goto(
      `${server.info.base_url}/?view=threaded&selected=pr:1&provider=github&platform_host=github.com&repo_path=acme%2Fwidgets`,
    );
    await initialActivity;
    expect((await detailSyncAccepted).status()).toBe(202);

    await page.getByRole("button", { name: "Close Activity selection" }).click();
    await expect(page.locator(".activity-detail")).toHaveCount(0);

    const reconciledActivity = page.waitForResponse(async (response) => {
      const url = new URL(response.url());
      if (
        response.request().method() !== "GET" ||
        url.pathname !== "/api/v1/activity" ||
        url.searchParams.has("after")
      ) {
        return false;
      }
      const snapshot = (await response.json()) as {
        items: Array<{ body_preview: string }>;
      };
      return snapshot.items.some((event) => event.body_preview === item.body);
    });
    const released = await releaseSync();
    expect(released.status(), await released.text()).toBe(204);
    await reconciledActivity;

    await expect(page.locator(".activity-feed").getByText("fixture-bot", { exact: true })).toBeVisible();
  } finally {
    await releaseSync().catch(() => {});
    await server.stop();
  }
});

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

    await persistActivityComment(page, server.info.base_url, newComment);

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

test("SSE comment refresh does not promote the commenter to an Activity author candidate", async ({ page }) => {
  const server = await startIsolatedE2EServer();
  try {
    const initialAuthors = page.waitForResponse((response) =>
      new URL(response.url()).pathname.endsWith("/api/v1/activity/authors"),
    );
    const streamRequested = page.waitForRequest((request) =>
      new URL(request.url()).pathname.endsWith("/api/v1/events"),
    );
    await page.goto(`${server.info.base_url}/`);
    await streamRequested;
    expect((await initialAuthors).status()).toBe(200);
    await expect(page.locator(".activity-table .activity-row").first()).toBeVisible();

    const filtersTrigger = page.locator(".activity-filters__trigger");
    const filtersPanel = page.locator(".activity-filters__panel");
    await filtersTrigger.click();
    const authorTrigger = filtersPanel.getByRole("button", { name: "Filter authors" });
    await authorTrigger.click();
    await expect(page.getByRole("option", { name: "alice" })).toBeVisible();
    await expect(page.getByRole("option", { name: "fixture-bot" })).toHaveCount(0);
    await page.keyboard.press("Escape");

    const refreshedAuthors = page.waitForResponse((response) =>
      new URL(response.url()).pathname.endsWith("/api/v1/activity/authors"),
    );
    const refreshedActivity = page.waitForResponse((response) =>
      new URL(response.url()).pathname.endsWith("/api/v1/activity"),
    );
    await persistActivityComment(page, server.info.base_url, "Fresh actor Activity comment");
    expect((await refreshedActivity).status()).toBe(200);
    expect((await refreshedAuthors).status()).toBe(200);

    await expect(page.locator(".activity-row .col-author", { hasText: "fixture-bot" }).first()).toBeVisible();
    if (!(await filtersPanel.isVisible())) {
      await filtersTrigger.click();
    }
    await authorTrigger.click();
    await expect(page.getByRole("option", { name: "alice" })).toBeVisible();
    await expect(page.getByRole("option", { name: "fixture-bot" })).toHaveCount(0);
  } finally {
    await server.stop();
  }
});

test("incremental Activity polling keeps renamed routes distinct and reconciles provider links", async ({
  page,
}, testInfo) => {
  testInfo.setTimeout(75_000);
  const server = await startIsolatedE2EServer();
  try {
    await page.goto(`${server.info.base_url}/?view=threaded&range=30d`);

    const feed = page.locator(".activity-feed .threaded-view");
    const original = feed.locator(".item-row", { hasText: "Add widget caching layer" });
    await expect(original).toHaveCount(1);

    const renamedActivity = page.waitForResponse(async (response) => {
      const url = new URL(response.url());
      if (
        response.request().method() !== "GET" ||
        url.pathname !== "/api/v1/activity" ||
        !url.searchParams.has("after")
      ) {
        return false;
      }
      const snapshot = (await response.json()) as {
        item_activity: Array<{ repo: { platform_repo_id?: string; repo_path?: string } }>;
      };
      return snapshot.item_activity.some(
        (subject) => subject.repo.platform_repo_id && subject.repo.repo_path === "acme/widgets-renamed",
      );
    });
    const rename = await page.request.post(`${server.info.base_url}/__e2e/activity/repository-identity?phase=rename`);
    expect(rename.ok(), await rename.text()).toBe(true);
    await renamedActivity;

    await expect(original).toHaveCount(1);

    const replacementActivity = page.waitForResponse(async (response) => {
      const url = new URL(response.url());
      if (
        response.request().method() !== "GET" ||
        url.pathname !== "/api/v1/activity" ||
        !url.searchParams.has("after")
      ) {
        return false;
      }
      const snapshot = (await response.json()) as {
        item_activity: Array<{
          item_title: string;
          repo: { platform_repo_id?: string; repo_path?: string };
        }>;
      };
      return snapshot.item_activity.some(
        (subject) =>
          subject.item_title === "Replacement route pull request" &&
          subject.repo.platform_repo_id === "e2e-replacement-widgets" &&
          subject.repo.repo_path === "acme/widgets",
      );
    });
    const reuse = await page.request.post(`${server.info.base_url}/__e2e/activity/repository-identity?phase=reuse`);
    expect(reuse.ok(), await reuse.text()).toBe(true);
    await replacementActivity;

    await expect(original).toHaveCount(1);
    const replacement = feed.locator(".item-row", { hasText: "Replacement route pull request" });
    await expect(replacement).toHaveCount(1);

    await page.getByRole("button", { name: /^Filters/ }).click();
    await page.getByRole("radiogroup", { name: "View" }).getByRole("radio", { name: "Flat" }).click();
    const notification = page.locator(".activity-row", { hasText: "Review requested" });
    await expect(notification).toHaveCount(1);
    await page.evaluate(() => {
      window.open = ((url?: string | URL) => {
        window.sessionStorage.setItem("e2e-opened-provider-url", String(url ?? ""));
        return null;
      }) as typeof window.open;
    });
    await notification.getByRole("button", { name: "Open activity in provider" }).click();
    await expect
      .poll(() => page.evaluate(() => window.sessionStorage.getItem("e2e-opened-provider-url")))
      .toBe("https://github.com/acme/widgets-renamed/pull/1");

    await page.getByRole("button", { name: /^Filters/ }).click();
    await page.getByRole("radiogroup", { name: "View" }).getByRole("radio", { name: "Threaded" }).click();
    await original.click();
    await expect.poll(() => new URL(page.url()).searchParams.get("repo_path")).toBe("acme/widgets-renamed");

    await replacement.click();
    await expect.poll(() => new URL(page.url()).searchParams.get("repo_path")).toBe("acme/widgets");
  } finally {
    await server.stop();
  }
});

test("legacy commit bookmark normalizes against the real Activity API", async ({ page }) => {
  const server = await startIsolatedE2EServer();
  try {
    const seeded = await page.request.post(`${server.info.base_url}/__e2e/activity/default-branch-commit`);
    expect(seeded.status(), await seeded.text()).toBe(204);

    const activityResponse = page.waitForResponse((response) => {
      const url = new URL(response.url());
      return response.request().method() === "GET" && url.pathname === "/api/v1/activity";
    });
    await page.goto(`${server.info.base_url}/?view=flat&types=commit,notification`);
    const snapshot = (await (await activityResponse).json()) as {
      items: Array<{ activity_type: string; body_preview: string }>;
    };
    expect(
      snapshot.items.some(
        (item) =>
          item.activity_type === "default_branch_commit" && item.body_preview === "Repository maintenance commit",
      ),
    ).toBe(true);

    await expect(page.locator(".activity-row", { hasText: "Repository maintenance commit" })).toBeVisible();
    await expect(page.locator(".activity-row", { hasText: "Add widget caching layer" })).toHaveCount(0);
    await expect(page.locator(".activity-row", { hasText: "Widget rendering broken on Safari" })).toHaveCount(0);

    const normalized = new URL(page.url());
    expect(normalized.searchParams.has("types")).toBe(false);
    expect(normalized.searchParams.get("item_types")).toBe("none");
    expect(normalized.searchParams.get("event_types")).toBe("commit");
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
