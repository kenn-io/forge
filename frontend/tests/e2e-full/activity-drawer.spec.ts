import { expect, test, type Locator, type Page } from "@playwright/test";
import type { DiffFile, DiffLine, DiffResult, FilesResult } from "@middleman/ui/api/types";

type DiffFixtureFile = Omit<DiffFile, "patch"> & { patch?: string };
type DiffFixture = Omit<DiffResult, "files"> & {
  files: DiffFixtureFile[];
};

function patchLinePrefix(line: DiffLine): string {
  if (line.type === "add") return "+";
  if (line.type === "delete") return "-";
  return " ";
}

function patchRange(start: number, count: number): string {
  return count === 1 ? `${start}` : `${start},${count}`;
}

function patchForFile(file: DiffFixtureFile): string {
  if (file.is_binary || file.hunks.length === 0) return "";
  const oldPath = file.status === "added" ? "/dev/null" : `a/${file.old_path || file.path}`;
  const newPath = file.status === "deleted" ? "/dev/null" : `b/${file.path}`;
  const lines = [`diff --git a/${file.old_path || file.path} b/${file.path}`, `--- ${oldPath}`, `+++ ${newPath}`];
  for (const hunk of file.hunks) {
    lines.push(
      `@@ -${patchRange(hunk.old_start, hunk.old_count)} +${patchRange(hunk.new_start, hunk.new_count)} @@${hunk.section ? ` ${hunk.section}` : ""}`,
    );
    for (const line of hunk.lines) {
      lines.push(`${patchLinePrefix(line)}${line.content}`);
    }
  }
  return `${lines.join("\n")}\n`;
}

function normalizeFixtureFile(file: DiffFixtureFile): DiffFixtureFile {
  return {
    ...file,
    hunks: file.hunks.map((hunk) => ({
      ...hunk,
      old_count: hunk.lines.filter((line) => line.type !== "add").length,
      new_count: hunk.lines.filter((line) => line.type !== "delete").length,
    })),
  };
}

function withServerDiffData(fixture: DiffFixture): DiffResult {
  const files = fixture.files.map((file) => {
    const normalized = normalizeFixtureFile(file);
    return {
      ...normalized,
      patch: normalized.patch ?? patchForFile(normalized),
    };
  });
  return {
    ...fixture,
    files,
  };
}

function treeFileItems(root: Locator) {
  return root.locator('.diff-file-tree [data-item-type="file"]');
}

function treeFileItem(root: Locator, path: string) {
  return root.locator(`.diff-file-tree [data-item-path="${path}"]`);
}

// Minimal diff fixture: one modified file.
const tinyDiff: DiffResult = withServerDiffData({
  stale: false,
  whitespace_only_count: 0,
  files: [
    {
      path: "src/handler.go",
      old_path: "src/handler.go",
      status: "modified",
      is_binary: false,
      is_whitespace_only: false,
      additions: 2,
      deletions: 1,
      hunks: [
        {
          old_start: 1,
          old_count: 3,
          new_start: 1,
          new_count: 4,
          lines: [
            {
              type: "context",
              content: "package main",
              old_num: 1,
              new_num: 1,
            },
            { type: "delete", content: "// old", old_num: 2 },
            { type: "add", content: "// new", new_num: 2 },
            { type: "add", content: "// added", new_num: 3 },
            { type: "context", content: "", old_num: 3, new_num: 4 },
          ],
        },
      ],
    },
  ],
});

// Multi-file diff fixture: 20 files with 20 lines each to force the
// diff area to overflow in the kanban drawer.
const multiFileDiff: DiffResult = withServerDiffData({
  stale: false,
  whitespace_only_count: 0,
  files: Array.from({ length: 20 }, (_, i) => ({
    path: `src/file_${i}.go`,
    old_path: `src/file_${i}.go`,
    status: "modified" as const,
    is_binary: false,
    is_whitespace_only: false,
    additions: 10,
    deletions: 5,
    hunks: [
      {
        old_start: 1,
        old_count: 10,
        new_start: 1,
        new_count: 15,
        lines: Array.from({ length: 20 }, (_, j) => {
          const type = j % 3 === 0 ? "delete" : j % 3 === 1 ? "add" : "context";
          const line: {
            type: "delete" | "add" | "context";
            content: string;
            old_num?: number;
            new_num?: number;
          } = {
            type,
            content: `line ${j} of file ${i}`,
          };
          if (type !== "add") line.old_num = j + 1;
          if (type !== "delete") line.new_num = j + 1;
          return line;
        }),
      },
    ],
  })),
});

function filesFromDiff(fixture: DiffResult): FilesResult {
  return {
    stale: fixture.stale,
    files: fixture.files.map((f) => ({
      ...f,
      additions: 0,
      deletions: 0,
      hunks: [],
    })),
  };
}

// Broad wildcard mock: any PR in any repo returns the same tiny diff.
// The activity feed test clicks "the first PR row", which could be any
// PR from the seeded fixtures; a wildcard mock keeps the test
// deterministic regardless of which PR is clicked.
async function mockDiffForAllPRs(page: Page, fixture: DiffResult): Promise<void> {
  await page.route("**/api/v1/pulls/github/*/*/*/files", async (route) => {
    await route.fulfill({
      status: 200,
      contentType: "application/json",
      body: JSON.stringify(filesFromDiff(fixture)),
    });
  });
  await page.route("**/api/v1/pulls/github/*/*/*/diff*", async (route) => {
    await route.fulfill({
      status: 200,
      contentType: "application/json",
      body: JSON.stringify(fixture),
    });
  });
}

async function mockDiffForAllPRsWithDelayedDiff(page: Page, fixture: DiffResult): Promise<() => void> {
  let releaseDiff!: () => void;
  const diffGate = new Promise<void>((resolve) => {
    releaseDiff = resolve;
  });
  await page.route("**/api/v1/pulls/github/*/*/*/files", async (route) => {
    await route.fulfill({
      status: 200,
      contentType: "application/json",
      body: JSON.stringify(filesFromDiff(fixture)),
    });
  });
  await page.route("**/api/v1/pulls/github/*/*/*/diff*", async (route) => {
    await diffGate;
    await route.fulfill({
      status: 200,
      contentType: "application/json",
      body: JSON.stringify(fixture),
    });
  });
  return releaseDiff;
}

function issueDetailFixture(platformHost: string, issueNumber = 10, title = "Fix Safari layout issue"): unknown {
  const now = "2026-04-27T12:00:00Z";
  return {
    detail_loaded: true,
    events: [],
    platform_host: platformHost,
    repo_owner: "acme",
    repo_name: "widgets",
    repo: {
      provider: "github",
      platform_host: platformHost,
      owner: "acme",
      name: "widgets",
      repo_path: "acme/widgets",
    },
    issue: {
      ID: issueNumber,
      PlatformID: 10_000 + issueNumber,
      RepoID: 1,
      Number: issueNumber,
      Title: title,
      Body: "The Safari layout needs attention.",
      Author: "alice",
      State: "open",
      URL: `https://ghe.example.com/acme/widgets/issues/${issueNumber}`,
      CreatedAt: now,
      UpdatedAt: now,
      LastActivityAt: now,
      ClosedAt: null,
      CommentCount: 0,
      Starred: false,
      detail_loaded: true,
      platform_host: platformHost,
      repo_owner: "acme",
      repo_name: "widgets",
      labels: [],
    },
  };
}

async function mockIssueDetailForPlatformHost(page: Page, expectedPlatformHost: string): Promise<string[]> {
  const seenHosts: string[] = [];
  await page.route("**/api/v1/**/issues/github/acme/widgets/10**", async (route) => {
    const url = new URL(route.request().url());
    const detailRoute = providerItemRoute(url);
    if (detailRoute?.number !== "10") {
      await route.fallback();
      return;
    }
    const host = detailRoute.platformHost;
    seenHosts.push(host);
    await route.fulfill({
      status: host === expectedPlatformHost ? 200 : 400,
      contentType: "application/json",
      body: JSON.stringify(issueDetailFixture(expectedPlatformHost)),
    });
  });
  return seenHosts;
}

async function mockActivityWithGheIssue(page: Page): Promise<void> {
  await page.route("**/api/v1/activity**", async (route) => {
    await route.fulfill({
      status: 200,
      contentType: "application/json",
      body: JSON.stringify({
        capped: false,
        items: [
          {
            id: "ghe-issue-10-comment",
            cursor: "2026-04-27T12:00:00Z:ghe-issue-10-comment",
            activity_type: "comment",
            platform_host: "ghe.example.com",
            repo_owner: "acme",
            repo_name: "widgets",
            repo: {
              provider: "github",
              platform_host: "ghe.example.com",
              owner: "acme",
              name: "widgets",
              repo_path: "acme/widgets",
            },
            item_type: "issue",
            item_number: 10,
            item_title: "Fix Safari layout issue",
            item_url: "https://ghe.example.com/acme/widgets/issues/10",
            item_state: "open",
            author: "alice",
            created_at: "2026-04-27T12:00:00Z",
            body_preview: "The Safari layout needs attention.",
          },
        ],
      }),
    });
  });
}

async function mockActivityWithTwoGheIssues(page: Page): Promise<void> {
  await page.route("**/api/v1/activity**", async (route) => {
    await route.fulfill({
      status: 200,
      contentType: "application/json",
      body: JSON.stringify({
        capped: false,
        items: [
          {
            id: "ghe-issue-10-comment",
            cursor: "2026-04-27T12:00:00Z:ghe-issue-10-comment",
            activity_type: "comment",
            platform_host: "ghe.example.com",
            repo_owner: "acme",
            repo_name: "widgets",
            repo: {
              provider: "github",
              platform_host: "ghe.example.com",
              owner: "acme",
              name: "widgets",
              repo_path: "acme/widgets",
            },
            item_type: "issue",
            item_number: 10,
            item_title: "Fix Safari layout issue",
            item_url: "https://ghe.example.com/acme/widgets/issues/10",
            item_state: "open",
            author: "alice",
            created_at: "2026-04-27T12:00:00Z",
            body_preview: "The Safari layout needs attention.",
          },
          {
            id: "ghe-issue-11-comment",
            cursor: "2026-04-27T11:00:00Z:ghe-issue-11-comment",
            activity_type: "comment",
            platform_host: "ghe.example.com",
            repo_owner: "acme",
            repo_name: "widgets",
            repo: {
              provider: "github",
              platform_host: "ghe.example.com",
              owner: "acme",
              name: "widgets",
              repo_path: "acme/widgets",
            },
            item_type: "issue",
            item_number: 11,
            item_title: "Fix Firefox layout issue",
            item_url: "https://ghe.example.com/acme/widgets/issues/11",
            item_state: "open",
            author: "bob",
            created_at: "2026-04-27T11:00:00Z",
            body_preview: "The Firefox layout needs attention.",
          },
        ],
      }),
    });
  });
}

function maxCount(counts: Map<string, number>): number {
  return Math.max(0, ...counts.values());
}

function providerItemKey(url: URL): string {
  const match = providerItemRoute(url);
  if (match === null) return "";
  return [match.provider, match.platformHost, `${match.owner}/${match.name}`, match.number].join("|");
}

function providerItemRoute(url: URL): {
  provider: string;
  platformHost: string;
  owner: string;
  name: string;
  number: string;
  suffix: string;
} | null {
  const parts = url.pathname.split("/").filter(Boolean).map(decodeURIComponent);
  let index = parts.findIndex((part, i) => part === "api" && parts[i + 1] === "v1");
  if (index < 0) return null;
  index += 2;

  let platformHost = "github.com";
  if (parts[index] === "host") {
    platformHost = parts[index + 1] ?? "";
    index += 2;
  }

  const kind = parts[index];
  if (kind !== "pulls" && kind !== "issues") return null;

  const provider = parts[index + 1] ?? "";
  const owner = parts[index + 2] ?? "";
  const name = parts[index + 3] ?? "";
  const number = parts[index + 4] ?? "";
  if (!provider || !owner || !name || !number) return null;

  return {
    provider,
    platformHost,
    owner,
    name,
    number,
    suffix: parts.slice(index + 5).join("/"),
  };
}

function isPRRoute(url: URL, suffix = ""): boolean {
  const route = providerItemRoute(url);
  return (
    route !== null &&
    route.provider === "github" &&
    route.owner === "acme" &&
    route.name === "widgets" &&
    route.suffix === suffix
  );
}

function isGheIssueRoute(url: URL, suffix = ""): boolean {
  const route = providerItemRoute(url);
  return (
    route !== null &&
    route.provider === "github" &&
    route.platformHost === "ghe.example.com" &&
    route.owner === "acme" &&
    route.name === "widgets" &&
    route.suffix === suffix
  );
}

async function waitForActivityTable(page: Page): Promise<void> {
  await page.locator(".activity-table .activity-row").first().waitFor({ state: "visible", timeout: 10_000 });
}

async function openActivityPRSplit(page: Page): Promise<Locator> {
  const prRow = page
    .locator(".activity-row")
    .filter({ has: page.locator(".badge", { hasText: "PR" }) })
    .filter({ hasText: "Add widget caching layer" })
    .first();
  await prRow.click();
  const detail = page.locator(".activity-detail");
  await expect(page.locator(".activity-shell.activity-shell--split")).toBeVisible();
  await expect(detail).toBeVisible();
  return detail;
}

async function openActivityIssueSplit(page: Page): Promise<Locator> {
  const issueRow = page
    .locator(".activity-row")
    .filter({ has: page.locator(".badge", { hasText: "Issue" }) })
    .filter({ hasText: "Safari" })
    .first();
  await issueRow.click();
  const detail = page.locator(".activity-detail");
  await expect(page.locator(".activity-shell.activity-shell--split")).toBeVisible();
  await expect(detail).toBeVisible();
  return detail;
}

async function expectDiffFileVisibleInScrollArea(diffArea: Locator, filePath: string): Promise<void> {
  await expect
    .poll(async () => {
      return await diffArea.evaluate((container, path) => {
        const file = Array.from(container.querySelectorAll<HTMLElement>("[data-file-path]")).find(
          (el) => el.dataset.filePath === path,
        );
        if (!file) {
          return false;
        }

        const containerRect = container.getBoundingClientRect();
        const fileRect = file.getBoundingClientRect();
        return fileRect.bottom > containerRect.top && fileRect.top < containerRect.bottom;
      }, filePath);
    })
    .toBe(true);
}

// TODO: Split this file once kanban drawer coverage moves to a dedicated
// spec; it currently covers both the Activity split view and kanban drawer.
test.describe("activity split view and detail drawers", () => {
  test("PR split view shows diff when switching to Files tab", async ({ page }) => {
    // Route-level mocks must be installed before navigation so the
    // diff store never sees a real backend response.
    await mockDiffForAllPRs(page, tinyDiff);

    await page.goto("/");
    await waitForActivityTable(page);

    const detail = await openActivityPRSplit(page);
    await expect(page.locator(".activity-pane")).toBeVisible();

    await detail.locator(".detail-tab", { hasText: "Files changed" }).click();
    await expect(page).toHaveURL(/selected=pr%3A1/);
    await expect(page).toHaveURL(/provider=github/);
    await expect(page).toHaveURL(/repo_path=acme%2Fwidgets/);
    await expect(page).toHaveURL(/selected_tab=files/);

    await expect(detail.locator(".diff-view")).toBeVisible();
    await expect(detail.locator(".diff-toolbar")).toBeVisible();
    await expect(detail.locator(".diff-file")).toHaveCount(1);

    // Switching back to Conversation unmounts the diff and restores
    // the conversation view.
    await detail.locator(".detail-tab", { hasText: "Conversation" }).click();
    await expect(detail.locator(".diff-view")).toHaveCount(0);
    await expect(detail.locator(".pull-detail")).toBeVisible();

    // Escape clears the selection and restores full-width Activity.
    await page.keyboard.press("Escape");
    await expect(page.locator(".activity-shell--split")).toHaveCount(0);
    await expect(page.locator(".activity-table")).toBeVisible();
    await expect(page).not.toHaveURL(/selected=/);
  });

  test("Activity PR selection renders detail when a duplicate load stalls", async ({ page }) => {
    let detailGetCount = 0;

    await page.route(
      (url) => isPRRoute(url) && providerItemRoute(url)?.number === "1",
      async (route) => {
        if (route.request().method() !== "GET") {
          await route.fallback();
          return;
        }

        detailGetCount++;
        if (detailGetCount === 1) {
          const response = await route.fetch();
          await new Promise((resolve) => setTimeout(resolve, 200));
          await route.fulfill({ response });
          return;
        }

        await new Promise(() => {});
      },
    );

    await page.goto("/?view=flat");
    await waitForActivityTable(page);

    const detail = await openActivityPRSplit(page);
    await expect(detail.locator(".pull-detail")).toBeVisible({
      timeout: 3_000,
    });
    expect(detailGetCount).toBeGreaterThanOrEqual(1);

    // The route handler above intentionally hangs on later requests;
    // unroute before teardown so pending route.fetch calls don't leak
    // "Test ended" rejections into the next test.
    await page.unrouteAll({ behavior: "ignoreErrors" });
  });

  test("Activity PR selection hides stale detail while the next item loads", async ({ page }) => {
    await page.route(
      (url) => isPRRoute(url),
      async (route) => {
        if (route.request().method() !== "GET") {
          await route.fallback();
          return;
        }

        const url = new URL(route.request().url());
        if (providerItemRoute(url)?.number !== "1") {
          await new Promise((resolve) => setTimeout(resolve, 1_500));
        }
        const response = await route.fetch();
        await route.fulfill({ response });
      },
    );

    await page.goto("/?view=flat");
    await waitForActivityTable(page);

    const detail = await openActivityPRSplit(page);
    await expect(detail.locator(".detail-title")).toContainText("Add widget caching layer");

    const nextPRRow = page
      .locator(".activity-compact-row")
      .filter({ has: page.locator(".badge", { hasText: "PR" }) })
      .filter({ hasNotText: "Add widget caching layer" })
      .first();
    await nextPRRow.click();

    await expect(
      detail.locator(".detail-title", {
        hasText: "Add widget caching layer",
      }),
    ).toHaveCount(0);
    await expect(detail.locator(".state-center .state-msg")).toContainText("Loading");

    // The 1.5s delay above intentionally outlives the test; unroute
    // before teardown so pending route.fetch calls don't leak "Test
    // ended" rejections into the next test.
    await page.unrouteAll({ behavior: "ignoreErrors" });
  });

  test("compact flat activity rows respect hide org name", async ({ page }) => {
    await page.addInitScript(() => {
      localStorage.removeItem("middleman:hideOrgName");
    });
    await page.goto("/?view=flat");
    await waitForActivityTable(page);

    await openActivityPRSplit(page);

    const row = page
      .locator(".activity-compact-row", {
        has: page.locator(".compact-title", {
          hasText: "Add widget caching layer",
        }),
      })
      .first();
    await expect(row).toBeVisible();

    const repoLabel = row.locator(".compact-meta > span").first();
    await expect(repoLabel).toHaveText("acme/widgets");

    await page.locator(".activity-feed .filter-btn", { hasText: "View" }).click();
    await page
      .locator(".activity-feed .filter-dropdown .filter-item", {
        hasText: "Hide org name",
      })
      .click();

    await expect(repoLabel).toHaveText("widgets");
    await expect(repoLabel).not.toHaveText("acme/widgets");
  });

  test("Activity PR switching uses background sync without foreground fanout", async ({ page }) => {
    const detailBodies = new Map<string, string>();
    const detailGets = new Map<string, number>();
    const syncPosts = new Map<string, number>();
    const asyncSyncPosts = new Map<string, number>();

    await page.route(
      (url) => isPRRoute(url),
      async (route) => {
        if (route.request().method() !== "GET") {
          await route.fallback();
          return;
        }

        const key = providerItemKey(new URL(route.request().url()));
        detailGets.set(key, (detailGets.get(key) ?? 0) + 1);
        const response = await route.fetch();
        const body = await response.text();
        detailBodies.set(key, body);
        await route.fulfill({
          status: response.status(),
          headers: response.headers(),
          body,
        });
      },
    );
    await page.route(
      (url) => isPRRoute(url, "sync"),
      async (route) => {
        if (route.request().method() !== "POST") {
          await route.fallback();
          return;
        }

        const key = providerItemKey(new URL(route.request().url()));
        syncPosts.set(key, (syncPosts.get(key) ?? 0) + 1);
        const body = detailBodies.get(key);
        if (body !== undefined) {
          await route.fulfill({
            status: 200,
            contentType: "application/json",
            body,
          });
          return;
        }
        await route.fallback();
      },
    );
    await page.route(
      (url) => isPRRoute(url, "sync/async"),
      async (route) => {
        if (route.request().method() !== "POST") {
          await route.fallback();
          return;
        }

        const detailUrl = providerItemKey(new URL(route.request().url()));
        asyncSyncPosts.set(detailUrl, (asyncSyncPosts.get(detailUrl) ?? 0) + 1);
        await route.fulfill({ status: 202, body: "" });
      },
    );

    await page.goto("/?view=flat");
    await waitForActivityTable(page);

    const detail = await openActivityPRSplit(page);
    await expect(detail.locator(".pull-detail")).toBeVisible();

    const nextPRRow = page
      .locator(".activity-compact-row")
      .filter({ has: page.locator(".badge", { hasText: "PR" }) })
      .filter({ hasNotText: "Add widget caching layer" })
      .first();
    await nextPRRow.click();

    await expect(detail.locator(".pull-detail")).toBeVisible();
    await expect(
      detail.locator(".detail-title", {
        hasText: "Add widget caching layer",
      }),
    ).toHaveCount(0);

    // Give accidental reactive loops enough time to generate duplicate
    // detail/sync requests without making the test depend on the real
    // backend sync duration.
    await page.waitForTimeout(500);
    expect(maxCount(detailGets)).toBeLessThanOrEqual(2);
    expect(maxCount(syncPosts)).toBe(0);
    expect(maxCount(asyncSyncPosts)).toBeLessThanOrEqual(1);
    expect(maxCount(asyncSyncPosts)).toBeGreaterThan(0);
  });

  test("Activity issue selection renders detail when a duplicate load stalls", async ({ page }) => {
    await mockActivityWithGheIssue(page);

    let detailGetCount = 0;
    await page.route("**/api/v1/**/issues/github/acme/widgets/10**", async (route) => {
      if (route.request().method() !== "GET") {
        await route.fallback();
        return;
      }
      const url = new URL(route.request().url());
      if (!isGheIssueRoute(url) || providerItemRoute(url)?.number !== "10") {
        await route.fallback();
        return;
      }

      detailGetCount++;
      if (detailGetCount === 1) {
        await new Promise((resolve) => setTimeout(resolve, 200));
        await route.fulfill({
          status: 200,
          contentType: "application/json",
          body: JSON.stringify(issueDetailFixture("ghe.example.com")),
        });
        return;
      }

      await new Promise(() => {});
    });

    await page.goto("/?view=flat");
    await waitForActivityTable(page);

    await page.locator(".activity-row", { hasText: "Fix Safari layout issue" }).click();

    const detail = page.locator(".activity-detail");
    await expect(detail.locator(".issue-detail")).toBeVisible({
      timeout: 3_000,
    });
    expect(detailGetCount).toBeGreaterThanOrEqual(1);

    // The route handler above intentionally hangs on later requests;
    // unroute before teardown so pending route handlers don't leak
    // "Test ended" rejections into the next test.
    await page.unrouteAll({ behavior: "ignoreErrors" });
  });

  test("Activity issue switching avoids foreground sync fanout", async ({ page }) => {
    await mockActivityWithTwoGheIssues(page);

    const detailGets = new Map<string, number>();
    const syncPosts = new Map<string, number>();
    const asyncSyncPosts = new Map<string, number>();

    await page.route("**/api/v1/**/issues/github/acme/widgets/**", async (route) => {
      const url = new URL(route.request().url());
      const detailRoute = providerItemRoute(url);
      if (detailRoute === null || !isGheIssueRoute(url, detailRoute.suffix)) {
        await route.fallback();
        return;
      }

      const rawNumber = detailRoute.number;
      const issueNumber = Number(rawNumber);
      const key = rawNumber;
      if (route.request().method() === "GET" && detailRoute.suffix === "") {
        detailGets.set(key, (detailGets.get(key) ?? 0) + 1);
      } else if (route.request().method() === "POST" && detailRoute.suffix === "sync/async") {
        asyncSyncPosts.set(key, (asyncSyncPosts.get(key) ?? 0) + 1);
        await route.fulfill({ status: 202, body: "" });
        return;
      } else if (route.request().method() === "POST" && detailRoute.suffix === "sync") {
        syncPosts.set(key, (syncPosts.get(key) ?? 0) + 1);
      } else {
        await route.fallback();
        return;
      }

      await route.fulfill({
        status: 200,
        contentType: "application/json",
        body: JSON.stringify(
          issueDetailFixture(
            "ghe.example.com",
            issueNumber,
            issueNumber === 10 ? "Fix Safari layout issue" : "Fix Firefox layout issue",
          ),
        ),
      });
    });

    await page.goto("/?view=flat");
    await waitForActivityTable(page);

    await page.locator(".activity-row", { hasText: "Fix Safari layout issue" }).click();
    const detail = page.locator(".activity-detail");
    await expect(detail.locator(".issue-detail")).toBeVisible();

    await page
      .locator(".activity-compact-row", {
        hasText: "Fix Firefox layout issue",
      })
      .click();

    await expect(detail.locator(".issue-detail")).toBeVisible();
    await expect(detail.locator(".detail-title")).toHaveText("Fix Firefox layout issue");

    await page.waitForTimeout(500);
    expect(maxCount(detailGets)).toBeLessThanOrEqual(2);
    expect(maxCount(syncPosts)).toBe(0);
    expect(maxCount(asyncSyncPosts)).toBeLessThanOrEqual(1);
  });

  test("direct Activity PR files URL restores split view", async ({ page }) => {
    await mockDiffForAllPRs(page, tinyDiff);

    await page.goto(
      "/?selected=pr:1&provider=github&platform_host=github.com&repo_path=acme%2Fwidgets&selected_tab=files",
    );

    const detail = page.locator(".activity-detail");
    await expect(page.locator(".activity-shell.activity-shell--split")).toBeVisible();
    await expect(detail.locator(".diff-view")).toBeVisible();
    await expect(detail.locator(".diff-file")).toHaveCount(1);
  });

  test("direct Activity issue URL restores split view with platform host", async ({ page }) => {
    const seenHosts = await mockIssueDetailForPlatformHost(page, "ghe.example.com");
    await page.goto("/?selected=issue:10&provider=github&platform_host=ghe.example.com&repo_path=acme%2Fwidgets");

    const detail = page.locator(".activity-detail");
    await expect(page.locator(".activity-shell.activity-shell--split")).toBeVisible();
    await expect(detail.locator(".issue-detail")).toBeVisible();
    expect(seenHosts).toContain("ghe.example.com");
    expect(seenHosts).not.toContain("github.com");
    await expect(detail.locator(".list-layout > .sidebar")).toHaveCount(0);
    await expect(detail.locator(".list-layout > .resize-handle")).toHaveCount(0);
  });

  test("Activity issue row selection preserves platform host", async ({ page }) => {
    await mockActivityWithGheIssue(page);
    const seenHosts = await mockIssueDetailForPlatformHost(page, "ghe.example.com");

    await page.goto("/");
    await waitForActivityTable(page);

    await page.locator(".activity-row", { hasText: "Fix Safari layout issue" }).click();

    await expect(page).toHaveURL(/selected=issue%3A10/);
    await expect(page).toHaveURL(/provider=github/);
    await expect(page).toHaveURL(/platform_host=ghe\.example\.com/);
    await expect(page).toHaveURL(/repo_path=acme%2Fwidgets/);
    await expect(page.locator(".activity-detail .issue-detail")).toBeVisible();
    expect(seenHosts).toContain("ghe.example.com");
    expect(seenHosts).not.toContain("github.com");
  });

  test("PR tab handoff preserves selected Activity PR files tab", async ({ page }) => {
    const pageErrors: string[] = [];
    page.on("pageerror", (error) => {
      pageErrors.push(error.message);
    });
    await mockDiffForAllPRs(page, tinyDiff);
    await page.goto(
      "/?selected=pr:1&provider=github&platform_host=github.com&repo_path=acme%2Fwidgets&selected_tab=files",
    );
    await expect(page.locator(".activity-detail .diff-view")).toBeVisible();
    await expect(page.locator(".activity-detail .diff-file")).toHaveCount(1);

    await page.locator(".view-tab", { hasText: "PRs" }).click();

    await expect(page).toHaveURL(/\/pulls\/github\/acme\/widgets\/1\/files$/);
    await expect(page.locator(".diff-file")).toHaveCount(1);
    expect(pageErrors).toEqual([]);
  });

  test("Issues tab handoff preserves selected Activity issue platform host", async ({ page }) => {
    await mockIssueDetailForPlatformHost(page, "ghe.example.com");
    await page.goto("/?selected=issue:10&provider=github&platform_host=ghe.example.com&repo_path=acme%2Fwidgets");
    await expect(page.locator(".activity-detail .issue-detail")).toBeVisible();

    await page.locator(".view-tab", { hasText: "Issues" }).click();

    await expect(page).toHaveURL(/\/host\/ghe\.example\.com\/issues\/github\/acme\/widgets\/10$/);
  });

  test("kanban drawer shows diff when switching to Files tab", async ({ page }) => {
    await mockDiffForAllPRs(page, tinyDiff);

    await page.goto("/pulls/board");
    await page.locator(".kanban-card").first().waitFor({ state: "visible", timeout: 10_000 });

    // Click the kanban card for widgets#1 specifically so the drawer
    // title assertion is deterministic.
    const card = page.locator(".kanban-card").filter({ hasText: "Add widget caching layer" }).first();
    await card.click();

    const drawer = page.locator(".drawer-panel");
    await expect(drawer).toBeVisible();
    await expect(drawer.locator(".drawer-title")).toHaveText("acme/widgets#1");

    await drawer.locator(".detail-tab", { hasText: "Files changed" }).click();

    await expect(drawer.locator(".diff-view")).toBeVisible();
    await expect(drawer.locator(".diff-file")).toHaveCount(1);

    // Switching back to Conversation unmounts the diff and restores
    // the conversation view.
    await drawer.locator(".detail-tab", { hasText: "Conversation" }).click();
    await expect(drawer.locator(".diff-view")).toHaveCount(0);
    await expect(drawer.locator(".pull-detail")).toBeVisible();

    // Escape closes the drawer and the kanban board is preserved
    // underneath.
    await page.keyboard.press("Escape");
    await expect(drawer).toHaveCount(0);
    await expect(page.locator(".kanban-board")).toBeVisible();
  });

  test("activity split view Files tab renders diff inside detail pane", async ({ page }) => {
    // Regression guard: when the split view is on the Files tab, the
    // PR diff must stay inside the embedded detail pane, with the
    // stack sidebar suppressed so the Activity split does not become
    // a third-column layout.
    await mockDiffForAllPRs(page, tinyDiff);

    await page.goto("/");
    await waitForActivityTable(page);

    const detail = await openActivityPRSplit(page);
    await detail.locator(".detail-tab", { hasText: "Files changed" }).click();

    await expect(detail.locator(".diff-view")).toBeVisible();
    await expect(detail.locator(".diff-toolbar")).toBeVisible();
    await expect(detail.locator(".diff-file")).toHaveCount(1);
    const fileSidebar = detail.locator(".files-layout > .files-sidebar");
    await expect(fileSidebar).toBeVisible();
    await expect(detail.locator(".diff-scope-picker")).toBeVisible();
    await expect(treeFileItems(fileSidebar)).toHaveCount(1);
    await expect(treeFileItem(fileSidebar, "src/handler.go")).toBeVisible();
    await expect(detail.locator(".stack-sidebar")).toHaveCount(0);
    await expect(detail.locator(".list-layout > .sidebar")).toHaveCount(0);
    await expect(detail.locator(".list-layout > .resize-handle")).toHaveCount(0);
  });

  test("Escape from merge modal keeps activity split detail open", async ({ page }) => {
    await page.goto("/");
    await waitForActivityTable(page);

    const detail = await openActivityPRSplit(page);
    await detail.locator(".btn--merge").first().click();

    const modal = page.locator(".modal", { hasText: "Merge Pull Request" });
    await expect(modal).toBeVisible();

    await page.keyboard.press("Escape");

    await expect(modal).toHaveCount(0);
    await expect(page.locator(".activity-shell.activity-shell--split")).toBeVisible();
    await expect(detail).toBeVisible();
    await expect(detail.locator(".pull-detail")).toBeVisible();
  });

  test("kanban drawer Files tab renders the file/commit sidebar", async ({ page }) => {
    await mockDiffForAllPRs(page, tinyDiff);

    await page.goto("/pulls/board");
    await page.locator(".kanban-card").first().waitFor({ state: "visible", timeout: 10_000 });

    const card = page.locator(".kanban-card").filter({ hasText: "Add widget caching layer" }).first();
    await card.click();

    const drawer = page.locator(".drawer-panel");
    await expect(drawer).toBeVisible();
    await drawer.locator(".detail-tab", { hasText: "Files changed" }).click();

    const sidebar = drawer.locator(".files-layout > .files-sidebar");
    await expect(sidebar).toBeVisible();
    await expect(drawer.locator(".files-layout > .files-main .diff-view")).toBeVisible();

    await expect(drawer.locator(".diff-scope-picker")).toBeVisible();
    await expect(treeFileItems(sidebar)).toHaveCount(1);
    await expect(treeFileItem(sidebar, "src/handler.go")).toBeVisible();
  });

  test("activity split view Files tab renders every diff file", async ({ page }) => {
    // Regression guard for embedded PR diff rendering: multi-file PRs
    // must still render the full DiffView inside Activity detail even
    // though the drawer-specific file sidebar is not present.
    await mockDiffForAllPRs(page, multiFileDiff);

    await page.goto("/");
    await waitForActivityTable(page);

    const detail = await openActivityPRSplit(page);
    await detail.locator(".detail-tab", { hasText: "Files changed" }).click();

    const diffArea = detail.locator(".diff-area");

    await expect(diffArea).toBeVisible();
    await expect(detail.locator(".diff-file")).toHaveCount(20);
    await expect(detail.locator('[data-file-path="src/file_5.go"]')).toHaveCount(1);
  });

  test("activity split view shows detail close row on narrow viewports", async ({ page }) => {
    // Regression guard: below the container-relative breakpoint the
    // Activity rail is hidden and the detail pane gets its own close
    // affordance.
    await page.setViewportSize({ width: 600, height: 800 });
    await mockDiffForAllPRs(page, tinyDiff);

    await page.goto("/?desktop=1");
    await waitForActivityTable(page);

    const detail = await openActivityPRSplit(page);
    await expect(page.locator(".activity-pane")).toBeHidden();
    await expect(detail.locator(".activity-detail-header")).toBeVisible();
    await detail.locator(".detail-tab", { hasText: "Files changed" }).click();
    await expect(detail.locator(".diff-view")).toBeVisible();
  });

  test("kanban drawer multi-file sidebar clicks navigate DiffView", async ({ page }) => {
    await mockDiffForAllPRs(page, multiFileDiff);

    await page.goto("/pulls/board");
    await page.locator(".kanban-card").first().waitFor({ state: "visible", timeout: 10_000 });

    const card = page.locator(".kanban-card").filter({ hasText: "Add widget caching layer" }).first();
    await card.click();

    const drawer = page.locator(".drawer-panel");
    await expect(drawer).toBeVisible();
    await drawer.locator(".detail-tab", { hasText: "Files changed" }).click();

    const sidebar = drawer.locator(".files-layout > .files-sidebar");
    const diffArea = drawer.locator(".files-layout > .files-main .diff-area");

    await expect(diffArea).toBeVisible();
    await expect(treeFileItems(sidebar)).toHaveCount(20);

    // Click the 12th file (file_11.go) and verify navigation.
    await treeFileItem(sidebar, "src/file_11.go").click();
    await expect(treeFileItem(sidebar, "src/file_11.go")).toHaveAttribute("aria-selected", "true");
    await expectDiffFileVisibleInScrollArea(diffArea, "src/file_11.go");
  });

  test("kanban drawer sidebar click waits for delayed diff file DOM", async ({ page }) => {
    const releaseDiff = await mockDiffForAllPRsWithDelayedDiff(page, multiFileDiff);

    await page.goto("/pulls/board");
    await page.locator(".kanban-card").first().waitFor({ state: "visible", timeout: 10_000 });

    const card = page.locator(".kanban-card").filter({ hasText: "Add widget caching layer" }).first();
    await card.click();

    const drawer = page.locator(".drawer-panel");
    await expect(drawer).toBeVisible();
    await drawer.locator(".detail-tab", { hasText: "Files changed" }).click();

    const sidebar = drawer.locator(".files-layout > .files-sidebar");
    const filesMain = drawer.locator(".files-layout > .files-main");

    await expect(filesMain).toBeVisible();
    await expect(treeFileItems(sidebar)).toHaveCount(20);

    await treeFileItem(sidebar, "src/file_11.go").click();
    await expect(treeFileItem(sidebar, "src/file_11.go")).toHaveAttribute("aria-selected", "true");

    releaseDiff();
    const diffArea = filesMain.locator(".diff-area");
    await expect(diffArea).toBeVisible();
    await expect(drawer.locator(".diff-file")).toHaveCount(20);
    await expectDiffFileVisibleInScrollArea(diffArea, "src/file_11.go");
  });

  test("issue split view scrolls internally to bottom of content", async ({ page }) => {
    await page.goto("/");
    await waitForActivityTable(page);

    const detail = await openActivityIssueSplit(page);

    // The issue-detail element exists inside the detail pane.
    const issueDetail = detail.locator(".issue-detail");
    await expect(issueDetail).toBeVisible();

    // Inject a tall filler so the content guarantees overflow. The
    // seeded issue body is short; this isolates the test from fixture
    // content and verifies the actual scroll container behavior.
    // flex-shrink: 0 is required because .issue-detail is a flex
    // column; without it, the child would be shrunk to fit.
    await issueDetail.evaluate((el) => {
      const filler = document.createElement("div");
      filler.style.height = "3000px";
      filler.style.flexShrink = "0";
      filler.style.background = "transparent";
      filler.setAttribute("data-test-filler", "scroll-test");
      el.appendChild(filler);
    });

    // Verify .issue-detail is the actual scroll container.
    const overflowY = await issueDetail.evaluate((el) => getComputedStyle(el).overflowY);
    expect(["auto", "scroll"]).toContain(overflowY);

    // Content now overflows and scroll starts at top.
    const before = await issueDetail.evaluate((el) => ({
      scrollHeight: el.scrollHeight,
      clientHeight: el.clientHeight,
      scrollTop: el.scrollTop,
    }));
    expect(before.scrollHeight).toBeGreaterThan(before.clientHeight);
    expect(before.scrollTop).toBe(0);

    // Scroll to bottom on the intended container.
    await issueDetail.evaluate((el) => {
      el.scrollTop = el.scrollHeight;
    });

    // Scroll position advanced from 0.
    const finalScroll = await issueDetail.evaluate((el) => el.scrollTop);
    expect(finalScroll).toBeGreaterThan(0);

    // The split detail itself should still be visible after the scroll action.
    await expect(detail).toBeVisible();
  });

  test("activity split view keeps activity in a resizable rail", async ({ page }) => {
    await page.goto("/");
    await waitForActivityTable(page);

    await openActivityPRSplit(page);

    const rail = page.locator(".activity-pane");
    const detail = page.locator(".activity-detail");
    const resizeHandle = page.locator(".activity-split-resize-handle");
    const railBox = await rail.boundingBox();
    const detailBox = await detail.boundingBox();
    expect(railBox).not.toBeNull();
    expect(detailBox).not.toBeNull();
    expect(Math.abs(railBox!.width - 360)).toBeLessThan(2);
    expect(detailBox!.width).toBeGreaterThan(railBox!.width);

    await expect(resizeHandle).toBeVisible();
    await expect(detail.locator(".activity-detail-header .activity-rail-close")).toBeVisible();
    await expect(page.locator(".activity-rail-header .activity-rail-close")).toHaveCount(0);

    const handleBox = await resizeHandle.boundingBox();
    expect(handleBox).not.toBeNull();
    await page.mouse.move(handleBox!.x + handleBox!.width / 2, handleBox!.y + handleBox!.height / 2);
    await page.mouse.down();
    await page.mouse.move(handleBox!.x + 80, handleBox!.y + handleBox!.height / 2);
    await page.mouse.up();

    const resizedRailBox = await rail.boundingBox();
    expect(resizedRailBox).not.toBeNull();
    expect(resizedRailBox!.width).toBeGreaterThan(railBox!.width + 40);
  });

  test("activity split rail keeps its dragged width after a reload", async ({ page }) => {
    // The drawer selection lives in the URL, so a reload restores the
    // split view. The rail width must come back at the width the user
    // dragged to rather than snapping back to the 360px default.
    await page.goto("/");
    await waitForActivityTable(page);

    await openActivityPRSplit(page);

    const rail = page.locator(".activity-pane");
    const resizeHandle = page.locator(".activity-split-resize-handle");

    const initialBox = await rail.boundingBox();
    expect(initialBox).not.toBeNull();
    expect(Math.abs(initialBox!.width - 360)).toBeLessThan(2);

    await expect(resizeHandle).toBeVisible();
    const handleBox = await resizeHandle.boundingBox();
    expect(handleBox).not.toBeNull();
    await page.mouse.move(handleBox!.x + handleBox!.width / 2, handleBox!.y + handleBox!.height / 2);
    await page.mouse.down();
    await page.mouse.move(handleBox!.x + 140, handleBox!.y + handleBox!.height / 2, {
      steps: 10,
    });
    await page.mouse.up();

    const widenedBox = await rail.boundingBox();
    expect(widenedBox).not.toBeNull();
    expect(widenedBox!.width).toBeGreaterThan(initialBox!.width + 100);
    const widenedWidth = widenedBox!.width;

    await page.reload();
    await expect(page.locator(".activity-shell.activity-shell--split")).toBeVisible();

    // Before the fix the rail mounts at the hardcoded 360px default here.
    await expect
      .poll(async () => {
        const box = await page.locator(".activity-pane").boundingBox();
        return box ? Math.round(box.width) : 0;
      })
      .toBeGreaterThan(Math.round(initialBox!.width) + 100);

    const reloadedBox = await page.locator(".activity-pane").boundingBox();
    expect(reloadedBox).not.toBeNull();
    expect(Math.abs(reloadedBox!.width - widenedWidth)).toBeLessThanOrEqual(2);
  });

  test("activity split rail resizes past the old 560px cap on a wide viewport", async ({ page }) => {
    // Regression guard: the rail used to be clamped near 560px. On a
    // wide monitor it must now grow well past that, while still leaving
    // the detail pane a usable width.
    await page.setViewportSize({ width: 1920, height: 1000 });

    await page.goto("/");
    await waitForActivityTable(page);

    await openActivityPRSplit(page);

    const rail = page.locator(".activity-pane");
    const detail = page.locator(".activity-detail");
    const resizeHandle = page.locator(".activity-split-resize-handle");
    await expect(resizeHandle).toBeVisible();

    const handleBox = await resizeHandle.boundingBox();
    expect(handleBox).not.toBeNull();

    // Drag the splitter far to the right, past where the old fixed cap
    // would have stopped it.
    await page.mouse.move(handleBox!.x + handleBox!.width / 2, handleBox!.y + handleBox!.height / 2);
    await page.mouse.down();
    await page.mouse.move(handleBox!.x + 760, handleBox!.y + handleBox!.height / 2);
    await page.mouse.up();

    const railBox = await rail.boundingBox();
    const detailBox = await detail.boundingBox();
    expect(railBox).not.toBeNull();
    expect(detailBox).not.toBeNull();
    // Well past the previous ~560px ceiling.
    expect(railBox!.width).toBeGreaterThan(600);
    // The detail pane keeps a usable minimum and is never squeezed away.
    expect(detailBox!.width).toBeGreaterThan(300);
  });

  test("activity split view lets the View dropdown float past the rail splitter", async ({ page }) => {
    await page.goto("/");
    await waitForActivityTable(page);

    await openActivityPRSplit(page);

    const rail = page.locator(".activity-pane");
    const viewButton = rail.locator(".filter-btn", { hasText: "View" });
    await expect(viewButton).toBeVisible();
    await viewButton.click();

    const dropdown = page.locator(".activity-feed .filter-dropdown");
    await expect(dropdown).toBeVisible();

    const railBox = await rail.boundingBox();
    const dropdownBox = await dropdown.boundingBox();
    expect(railBox).not.toBeNull();
    expect(dropdownBox).not.toBeNull();
    const railRight = railBox!.x + railBox!.width;
    const dropdownRight = dropdownBox!.x + dropdownBox!.width;
    expect(dropdownRight).toBeGreaterThan(railRight + 8);

    const itemBeyondRail = await page.evaluate(
      ({ x, y }) => {
        const element = document.elementFromPoint(x, y);
        return element?.closest(".filter-dropdown") !== null;
      },
      {
        x: railRight + 8,
        y: dropdownBox!.y + 24,
      },
    );
    expect(itemBeyondRail).toBe(true);
  });

  test("activity split view can collapse and expand the activity rail", async ({ page }) => {
    await page.goto("/");
    await waitForActivityTable(page);

    await openActivityPRSplit(page);

    const rail = page.locator(".activity-pane");
    const detail = page.locator(".activity-detail");
    const collapseButton = page.locator("button[title='Collapse Activity sidebar']");
    await expect(collapseButton).toBeVisible();

    await collapseButton.click();

    await expect(detail).toBeVisible();
    await expect(page.locator(".activity-split-resize-handle")).toBeHidden();
    await expect(page.locator(".activity-collapsed-strip")).toBeVisible();
    await expect(page.locator("button[title='Expand Activity sidebar']")).toBeVisible();
    const collapsedBox = await rail.boundingBox();
    expect(collapsedBox).not.toBeNull();
    expect(collapsedBox!.width).toBeLessThan(40);

    await page.locator("button[title='Expand Activity sidebar']").click();

    await expect(page.locator(".activity-collapsed-strip")).toBeHidden();
    await expect(page.locator(".activity-split-resize-handle")).toBeVisible();
    const expandedBox = await rail.boundingBox();
    expect(expandedBox).not.toBeNull();
    expect(Math.abs(expandedBox!.width - 360)).toBeLessThan(2);
  });

  test("closing a collapsed activity split restores the Activity feed", async ({ page }) => {
    await page.goto("/");
    await waitForActivityTable(page);

    const detail = await openActivityPRSplit(page);

    await page.locator("button[title='Collapse Activity sidebar']").click();

    await expect(detail).toBeVisible();
    await expect(page.locator(".activity-collapsed-strip")).toBeVisible();

    await detail.locator(".activity-detail-header .activity-rail-close").click();

    await expect(page.locator(".activity-shell--split")).toHaveCount(0);
    await expect(page.locator(".activity-collapsed-strip")).toHaveCount(0);
    await expect(page.locator(".activity-table")).toBeVisible();
    await expect(
      page
        .locator(".activity-row", {
          hasText: "Add widget caching layer",
        })
        .first(),
    ).toBeVisible();
  });

  test("kanban drawer spans full viewport width", async ({ page }) => {
    await page.goto("/pulls/board");
    await page.locator(".kanban-card").first().waitFor({ state: "visible", timeout: 10_000 });
    await page.locator(".kanban-card").first().click();

    const drawer = page.locator(".drawer-panel");
    await expect(drawer).toBeVisible();

    const viewport = page.viewportSize();
    const drawerBox = await drawer.boundingBox();
    expect(viewport).not.toBeNull();
    expect(drawerBox).not.toBeNull();
    // Drawer spans the full viewport width (Task 8 widened to 100%).
    // Sub-pixel rounding from layout can yield a box width that differs
    // from the viewport by a fraction of a pixel, so allow 1px slack.
    expect(Math.abs(drawerBox!.width - viewport!.width)).toBeLessThan(1);
  });

  test("active tab visual state switches with selection", async ({ page }) => {
    await mockDiffForAllPRs(page, tinyDiff);
    await page.goto("/");
    await waitForActivityTable(page);

    const detail = await openActivityPRSplit(page);

    const conversationTab = detail.locator(".detail-tab", {
      hasText: "Conversation",
    });
    const filesTab = detail.locator(".detail-tab", {
      hasText: "Files changed",
    });

    // Conversation is active by default.
    await expect(conversationTab).toHaveClass(/detail-tab--active/);
    await expect(filesTab).not.toHaveClass(/detail-tab--active/);

    // Clicking Files shifts active state.
    await filesTab.click();
    await expect(filesTab).toHaveClass(/detail-tab--active/);
    await expect(conversationTab).not.toHaveClass(/detail-tab--active/);

    // Clicking back restores conversation as active.
    await conversationTab.click();
    await expect(conversationTab).toHaveClass(/detail-tab--active/);
    await expect(filesTab).not.toHaveClass(/detail-tab--active/);
  });

  test("activity split view highlights matching activity rows", async ({ page }) => {
    await page.goto("/");
    await waitForActivityTable(page);

    await openActivityPRSplit(page);

    const selectedRows = page.locator(".activity-compact-row.selected");
    await expect(selectedRows.first()).toContainText("Add widget caching layer");
    await expect(selectedRows.first()).toContainText("PR");
  });

  test("Escape closes activity split view from Files tab", async ({ page }) => {
    await mockDiffForAllPRs(page, tinyDiff);
    await page.goto("/");
    await waitForActivityTable(page);

    const detail = await openActivityPRSplit(page);

    // Switch to the Files tab and confirm the diff renders.
    await detail.locator(".detail-tab", { hasText: "Files changed" }).click();
    await expect(detail.locator(".diff-view")).toBeVisible();

    // Escape should still clear selection, even from the Files tab state.
    await page.keyboard.press("Escape");
    await expect(page.locator(".activity-shell--split")).toHaveCount(0);
  });

  test("activity split view has no drawer backdrop", async ({ page }) => {
    await page.goto("/");
    await waitForActivityTable(page);

    await openActivityPRSplit(page);
    await expect(page.locator(".drawer-backdrop")).toHaveCount(0);
  });

  test("kanban drawer Files view remains scrollable at full width", async ({ page }) => {
    // Serve a multi-file diff so the diff area is guaranteed to
    // overflow the drawer.
    await mockDiffForAllPRs(page, multiFileDiff);

    await page.goto("/pulls/board");
    await page.locator(".kanban-card").first().waitFor({ state: "visible", timeout: 10_000 });

    const card = page.locator(".kanban-card").filter({ hasText: "Add widget caching layer" }).first();
    await card.click();

    const drawer = page.locator(".drawer-panel");
    await expect(drawer).toBeVisible();

    // Open the Files tab.
    await drawer.locator(".detail-tab", { hasText: "Files changed" }).click();
    await expect(drawer.locator(".diff-view")).toBeVisible();

    // The diff-area inside the drawer is the internal scroll
    // container. Wait for all 20 seeded files to render before
    // measuring overflow.
    const diffArea = drawer.locator(".diff-area");
    await expect(diffArea).toBeVisible();
    await expect(drawer.locator(".diff-file")).toHaveCount(20);

    // Content overflows the viewport.
    const before = await diffArea.evaluate((el) => ({
      scrollHeight: el.scrollHeight,
      clientHeight: el.clientHeight,
      scrollTop: el.scrollTop,
    }));
    expect(before.scrollHeight).toBeGreaterThan(before.clientHeight);
    expect(before.scrollTop).toBe(0);

    // Drive a real scroll to the bottom on the diff area.
    await diffArea.evaluate((el) => {
      el.scrollTop = el.scrollHeight;
    });

    const finalScroll = await diffArea.evaluate((el) => el.scrollTop);
    expect(finalScroll).toBeGreaterThan(0);
  });

  test("kanban drawer Close action refreshes board with open filter", async ({ page }) => {
    // Fully synthetic /pulls?state=open response so the test does not
    // depend on the shared backend's mutable state. We mock ONLY the
    // open-filtered list endpoint — the exact path the kanban refreshes
    // through after the close action — and let every other /pulls
    // request fall through to the real backend. That way the close
    // refresh is the only thing that can change what the board shows.
    let pullsContainsWidgets1 = true;
    const widgetsRepo = {
      provider: "github",
      platform_host: "github.com",
      owner: "acme",
      name: "widgets",
      repo_path: "acme/widgets",
      capabilities: {
        read_repositories: true,
        read_merge_requests: true,
        read_issues: true,
        read_comments: true,
        read_releases: true,
        read_ci: true,
        comment_mutation: true,
        state_mutation: true,
        merge_mutation: true,
        review_mutation: true,
        workflow_approval: true,
        ready_for_review: true,
        draft_mutation: true,
        issue_mutation: true,
      },
    };

    const widgets1Card = {
      ID: 1001,
      Number: 1,
      Title: "Add widget caching layer",
      Body: "",
      Author: "alice",
      AuthorDisplayName: "alice",
      State: "open",
      KanbanStatus: "new",
      IsDraft: false,
      Additions: 240,
      Deletions: 30,
      CreatedAt: new Date(Date.now() - 2 * 3_600_000).toISOString(),
      UpdatedAt: new Date(Date.now() - 2 * 3_600_000).toISOString(),
      URL: "https://github.com/acme/widgets/pull/1",
      CIStatus: "",
      ReviewDecision: "",
      MergeableState: "",
      Starred: false,
      CIChecksJSON: "",
      labels: [],
      repo: widgetsRepo,
      worktree_links: [],
    };

    // Always-present card. Lets the test assert that the close
    // refresh removes only widgets#1, not unrelated open PRs.
    const otherCard = {
      ID: 1002,
      Number: 2,
      Title: "Refactor widget pipeline",
      Body: "",
      Author: "bob",
      AuthorDisplayName: "bob",
      State: "open",
      KanbanStatus: "reviewing",
      IsDraft: false,
      Additions: 80,
      Deletions: 12,
      CreatedAt: new Date(Date.now() - 4 * 3_600_000).toISOString(),
      UpdatedAt: new Date(Date.now() - 4 * 3_600_000).toISOString(),
      URL: "https://github.com/acme/widgets/pull/2",
      CIStatus: "",
      ReviewDecision: "",
      MergeableState: "",
      Starred: false,
      CIChecksJSON: "",
      labels: [],
      repo: widgetsRepo,
      worktree_links: [],
    };

    const buildOpenPullsResponse = (): unknown[] => {
      if (!pullsContainsWidgets1) return [otherCard];
      return [widgets1Card, otherCard];
    };

    // Function predicate: intercept only the top-level
    // /api/v1/pulls?state=open list. Other /pulls* requests
    // (per-PR detail, files, diff) fall through to the real backend.
    await page.route(
      (url) => url.pathname.endsWith("/api/v1/pulls") && url.searchParams.get("state") === "open",
      async (route) => {
        await route.fulfill({
          status: 200,
          contentType: "application/json",
          body: JSON.stringify(buildOpenPullsResponse()),
        });
      },
    );

    // Mock the state-change endpoint and flip the synthetic list on
    // success. The detail load goes through the real backend (which
    // still shows widgets#1 as open), but the kanban board reads only
    // from the mocked /pulls endpoint.
    await page.route("**/api/v1/pulls/github/*/*/*/github-state", async (route) => {
      pullsContainsWidgets1 = false;
      await route.fulfill({
        status: 200,
        contentType: "application/json",
        body: "{}",
      });
    });

    // Track calls to the open-filtered pulls endpoint that occur
    // after the close button is clicked. onPullsRefresh forwarding
    // must trigger at least one such call.
    let openPullsRequestsAfterClose = 0;
    let closeClicked = false;
    page.on("request", (req) => {
      const url = req.url();
      if (closeClicked && url.includes("/api/v1/pulls") && url.includes("state=open")) {
        openPullsRequestsAfterClose++;
      }
    });

    await page.goto("/pulls/board");

    // Open widgets#1 in the kanban drawer.
    const card = page.locator(".kanban-card").filter({ hasText: "Add widget caching layer" }).first();
    await expect(card).toBeVisible({ timeout: 10_000 });
    await card.click();

    const drawer = page.locator(".drawer-panel");
    await expect(drawer).toBeVisible();
    await expect(drawer.locator(".drawer-title")).toHaveText("acme/widgets#1");

    // Wait for the Close button inside the drawer's PullDetail.
    const closeBtn = drawer.locator("button.btn--close");
    await expect(closeBtn).toBeVisible();

    closeClicked = true;
    await closeBtn.click();

    // After the close succeeds, widgets#1 disappears from the kanban
    // board because the refetched synthetic /pulls?state=open list
    // omits it. Other open cards remain visible — proves the refresh
    // dropped only widgets#1, not unrelated entries.
    await expect(page.locator(".kanban-card").filter({ hasText: "Add widget caching layer" })).toHaveCount(0, {
      timeout: 10_000,
    });
    await expect(page.locator(".kanban-card").filter({ hasText: "Refactor widget pipeline" })).toBeVisible();

    // At least one /api/v1/pulls?state=open request must have
    // happened after the close was clicked. This proves the refresh
    // went through the open-filtered path wired via onPullsRefresh.
    expect(openPullsRequestsAfterClose).toBeGreaterThan(0);
  });
});

test.describe("PR list tabs", () => {
  test("outer PR-list tab bar remains singular and router-driven", async ({ page }) => {
    // Mock the diff so navigating to /files does not depend on real data.
    await mockDiffForAllPRs(page, tinyDiff);

    await page.goto("/pulls/github/acme/widgets/1");

    // Wait for the PRListView tab bar (scoped to .main-area) to
    // render.
    await page.locator(".main-area .detail-tabs").first().waitFor({ state: "visible", timeout: 10_000 });

    // Exactly one tab bar is present inside the outer PRListView
    // container. If PullDetail ever stops respecting hideTabs, a
    // second .detail-tabs element would show up inside .main-area.
    await expect(page.locator(".main-area .detail-tabs")).toHaveCount(1);
    await expect(
      page.locator(".main-area .detail-tabs .detail-tab", {
        hasText: "Conversation",
      }),
    ).toHaveCount(1);
    await expect(
      page.locator(".main-area .detail-tabs .detail-tab", {
        hasText: "Files changed",
      }),
    ).toHaveCount(1);

    // Clicking Files changed in the outer tab bar updates the URL to
    // the /files sub-route.
    await page
      .locator(".main-area .detail-tabs .detail-tab", {
        hasText: "Files changed",
      })
      .click();
    await expect(page).toHaveURL(/\/pulls\/github\/acme\/widgets\/1\/files$/);
    await expect(page.locator(".diff-view")).toBeVisible();
    await expect(page.locator(".main-area .detail-tabs")).toHaveCount(1);

    // Clicking Conversation routes back and keeps the tab bar
    // singular.
    await page
      .locator(".main-area .detail-tabs .detail-tab", {
        hasText: "Conversation",
      })
      .click();
    await expect(page).toHaveURL(/\/pulls\/github\/acme\/widgets\/1$/);
    await expect(page.locator(".main-area .detail-tabs")).toHaveCount(1);
  });

  test("direct load of provider PR files route renders the diff with a single tab bar", async ({ page }) => {
    // Regression guard for initialization-only bugs that affect deep
    // links to the provider /files route. Going there directly must render
    // the diff and keep exactly one outer PRListView tab bar — the
    // router-click test above does not exercise this path.
    await mockDiffForAllPRs(page, tinyDiff);

    await page.goto("/pulls/github/acme/widgets/1/files");

    await page.locator(".main-area .detail-tabs").first().waitFor({ state: "visible", timeout: 10_000 });

    await expect(page.locator(".main-area .detail-tabs")).toHaveCount(1);
    await expect(page.locator(".diff-view")).toBeVisible();
    await expect(
      page.locator(".main-area .detail-tabs .detail-tab--active", {
        hasText: "Files changed",
      }),
    ).toHaveCount(1);
  });
});
