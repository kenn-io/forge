import { expect, test, type Page } from "@playwright/test";

import { mockApi } from "./support/mockApi";

const mirrorIssueDetail = {
  issue: {
    ID: 2,
    RepoID: 2,
    GitHubID: 202,
    Number: 7,
    URL: "https://ghe.example.com/acme/widgets/issues/7",
    Title: "Mirror host issue",
    Author: "marius",
    State: "open",
    Body: "",
    CommentCount: 1,
    LabelsJSON: "[]",
    CreatedAt: "2026-03-28T14:00:00Z",
    UpdatedAt: "2026-03-30T14:00:00Z",
    LastActivityAt: "2026-03-30T14:00:00Z",
    ClosedAt: null,
    Starred: false,
  },
  events: [],
  platform_host: "ghe.example.com",
  repo_owner: "acme",
  repo_name: "widgets",
  detail_loaded: true,
  detail_fetched_at: "2026-03-30T14:00:00Z",
};

async function mockIssueDetailAndTrackHosts(page: Page): Promise<string[]> {
  const seenHosts: string[] = [];

  await mockApi(page);
  await page.route("**/api/v1/settings", async (route) => {
    await route.fulfill({
      status: 200,
      contentType: "application/json",
      body: JSON.stringify({
        repos: [
          {
            owner: "acme",
            name: "widgets",
            is_glob: false,
            matched_repo_count: 1,
          },
        ],
        activity: { hidden_authors: [] },
        terminal: {
          font_family: "",
          font_size: 14,
          scrollback: 1000,
          line_height: 1,
          letter_spacing: 0,
          cursor_blink: true,
          font_ligatures: false,
          renderer: "xterm",
        },
      }),
    });
  });
  await page.route(
    /\/api\/v1\/repos\/acme\/widgets\/issues\/7(?:[/?]|$)/,
    async (route) => {
      const url = new URL(route.request().url());
      seenHosts.push(url.searchParams.get("platform_host") ?? "");
      await route.fulfill({
        status: 200,
        contentType: "application/json",
        body: JSON.stringify(mirrorIssueDetail),
      });
    },
  );

  return seenHosts;
}

test.describe("issue route platform host", () => {
  test("direct issue load preserves platform_host in detail requests", async ({
    page,
  }) => {
    const seenHosts = await mockIssueDetailAndTrackHosts(page);

    await page.goto("/host/ghe.example.com/issues/github/acme/widgets/7");

    await expect(page.locator(".issue-detail .detail-title")).toContainText(
      "Mirror host issue",
    );
    await expect.poll(() => seenHosts).toContain("ghe.example.com");
  });

  test("popstate preserves platform_host in detail requests", async ({
    page,
  }) => {
    const seenHosts = await mockIssueDetailAndTrackHosts(page);

    await page.goto("/issues");
    await page.evaluate(() => {
      window.history.pushState(
        null,
        "",
        "/host/ghe.example.com/issues/github/acme/widgets/7",
      );
      window.dispatchEvent(new PopStateEvent("popstate"));
    });

    await expect(page.locator(".issue-detail .detail-title")).toContainText(
      "Mirror host issue",
    );
    await expect.poll(() => seenHosts).toContain("ghe.example.com");
  });
});

const assignedIssueDetail = {
  issue: {
    ID: 3,
    RepoID: 2,
    GitHubID: 303,
    Number: 12,
    URL: "https://ghe.example.com/acme/widgets/issues/12",
    Title: "Issue with assignees",
    Author: "marius",
    State: "open",
    Body: "",
    CommentCount: 0,
    LabelsJSON: "[]",
    CreatedAt: "2026-03-28T14:00:00Z",
    UpdatedAt: "2026-03-30T14:00:00Z",
    LastActivityAt: "2026-03-30T14:00:00Z",
    ClosedAt: null,
    Starred: false,
    assignees: ["alice", "bob"],
  },
  events: [],
  platform_host: "ghe.example.com",
  repo_owner: "acme",
  repo_name: "widgets",
  detail_loaded: true,
  detail_fetched_at: "2026-03-30T14:00:00Z",
};

async function mockAssignedIssueDetail(page: Page): Promise<void> {
  await mockApi(page);
  await page.route("**/api/v1/settings", async (route) => {
    await route.fulfill({
      status: 200,
      contentType: "application/json",
      body: JSON.stringify({
        repos: [
          {
            owner: "acme",
            name: "widgets",
            is_glob: false,
            matched_repo_count: 1,
          },
        ],
        activity: { hidden_authors: [] },
        terminal: {
          font_family: "",
          font_size: 14,
          scrollback: 1000,
          line_height: 1,
          letter_spacing: 0,
          cursor_blink: true,
          font_ligatures: false,
          renderer: "xterm",
        },
      }),
    });
  });
  await page.route(
    "**/api/v1/host/ghe.example.com/issues/github/acme/widgets/12",
    async (route) => {
      await route.fulfill({
        status: 200,
        contentType: "application/json",
        body: JSON.stringify(assignedIssueDetail),
      });
    },
  );
}

test.describe("issue detail assignees", () => {
  test("renders assignees in the meta row when present", async ({ page }) => {
    await mockAssignedIssueDetail(page);

    await page.goto("/host/ghe.example.com/issues/github/acme/widgets/12");

    await expect(page.locator(".issue-detail .detail-title")).toContainText(
      "Issue with assignees",
    );
    const metaRow = page.locator(".issue-detail .meta-row");
    await expect(metaRow).toContainText("Assigned: alice, bob");
  });
});
