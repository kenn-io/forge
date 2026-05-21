import { expect, test } from "@playwright/test";

import { mockApi } from "./support/mockApi";

test.beforeEach(async ({ page }) => {
  await mockApi(page);
});

test("workspaces route renders the terminal workspace list shell", async ({ page }) => {
  await page.goto("/workspaces");
  await expect(
    page.getByText("Select a workspace from the sidebar"),
  ).toBeVisible();
});

test("workspaces sidebar collapses and expands through the shared control", async ({ page }) => {
  await page.goto("/workspaces");

  const sidebar = page.locator(".sidebar").first();
  await expect(sidebar).toBeVisible();

  await sidebar
    .getByRole("button", { name: "Collapse Workspaces sidebar" })
    .click();
  await expect(sidebar).toHaveClass(/sidebar--collapsed/);

  await sidebar
    .getByRole("button", { name: "Expand sidebar" })
    .click();
  await expect(sidebar).not.toHaveClass(/sidebar--collapsed/);
});

test("AppHeader workspaces tab navigates to /workspaces", async ({ page }) => {
  await page.goto("/pulls");
  await page
    .getByRole("button", { name: "Workspaces" })
    .click();
  await expect(page).toHaveURL(/\/workspaces$/);
});

test(
  "repo selector renders icon and still filters repos",
  async ({ page }) => {
    await page.goto("/pulls");

    const selector = page.getByTitle(
      "Select repository",
    );
    await expect(selector).toBeVisible();
    await expect(selector.locator("svg")).toBeVisible();

    await selector.click();

    const input = page.getByLabel("Filter repos");
    await expect(input).toBeVisible();
    await input.fill("widg");

    const option = page.getByRole("option", {
      name: "github.com/acme/widgets",
    });
    await expect(option).toBeVisible();
    await option.click();
    await expect(option.locator("input[type='checkbox']")).toBeChecked();

    await page.keyboard.press("Escape");
    await expect(selector).toContainText("acme/widgets");
    await expect(selector.locator("svg")).toBeVisible();
    await expect(
      page.getByText("Add browser regression coverage"),
    ).toBeVisible();
  },
);

test("hideHeader suppresses AppHeader on the workspaces page", async ({ page }) => {
  await page.addInitScript(() => {
    window.__middleman_config = {
      embed: { hideHeader: true },
    };
  });

  await page.goto("/workspaces");
  await expect(
    page.locator("header.app-header"),
  ).toHaveCount(0);
});

test("navigateToRoute bridge method works", async ({ page }) => {
  await page.goto("/pulls");
  await page.evaluate(() => {
    window.__middleman_navigate_to_route?.("/workspaces");
  });
  await expect(page).toHaveURL(/\/workspaces/);
});

test("workspace bridge methods are registered on startup", async ({ page }) => {
  await page.goto("/workspaces");

  await expect(
    page.evaluate(() => ({
      navigateToRoute: typeof window.__middleman_navigate_to_route,
      updateWorkspace: typeof window.__middleman_update_workspace,
      updateSelection: typeof window.__middleman_update_selection,
      updateHostState: typeof window.__middleman_update_host_state,
    })),
  ).resolves.toEqual({
    navigateToRoute: "function",
    updateWorkspace: "function",
    updateSelection: "function",
    updateHostState: "function",
  });
});

test("provider-explicit embed detail route uses provider in detail request", async ({ page }) => {
  const detailRequest = page.waitForRequest(
    (request) =>
      request.method() === "GET" &&
      new URL(request.url()).pathname ===
        "/api/v1/host/git.example.com/issues/gitlab/group/project/7",
  );
  await page.route(
    "**/api/v1/host/git.example.com/issues/gitlab/group/project/7",
    async (route) => {
      await route.fulfill({
        status: 200,
        contentType: "application/json",
        body: JSON.stringify({
          issue: {
            ID: 7,
            RepoID: 7,
            GitHubID: 7007,
            Number: 7,
            URL: "https://git.example.com/group/project/-/issues/7",
            Title: "Provider-explicit GitLab issue",
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
          },
          repo: {
            provider: "gitlab",
            platform_host: "git.example.com",
            owner: "group",
            name: "project",
            repo_path: "group/project",
          },
          events: [],
          platform_host: "git.example.com",
          repo_owner: "group",
          repo_name: "project",
          detail_loaded: true,
          detail_fetched_at: "2026-03-30T14:00:00Z",
        }),
      });
    },
  );

  await page.goto(
    "/workspaces/embed/detail/gitlab/issue/git.example.com/group/project/7",
  );

  await detailRequest;
  await expect(page.getByText("Provider-explicit GitLab issue")).toBeVisible();
});

test("nested repo_path embed detail route loads matching detail content", async ({ page }) => {
  const detailRequest = page.waitForRequest(
    (request) =>
      request.method() === "GET" &&
      new URL(request.url()).pathname ===
        "/api/v1/host/git.example.com/issues/gitlab/group%2Fsubgroup/project/7",
  );
  await page.route(
    "**/api/v1/host/git.example.com/issues/gitlab/group%2Fsubgroup/project/7",
    async (route) => {
      await route.fulfill({
        status: 200,
        contentType: "application/json",
        body: JSON.stringify({
          issue: {
            ID: 7,
            RepoID: 7,
            GitHubID: 7007,
            Number: 7,
            URL: "https://git.example.com/group/subgroup/project/-/issues/7",
            Title: "Nested GitLab issue",
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
          },
          repo: {
            provider: "gitlab",
            platform_host: "git.example.com",
            owner: "group/subgroup",
            name: "project",
            repo_path: "group/subgroup/project",
          },
          events: [],
          platform_host: "git.example.com",
          repo_owner: "group/subgroup",
          repo_name: "project",
          detail_loaded: true,
          detail_fetched_at: "2026-03-30T14:00:00Z",
        }),
      });
    },
  );

  await page.goto(
    "/workspaces/embed/detail/gitlab/issue/git.example.com/7" +
      "?repo_path=group%2Fsubgroup%2Fproject",
  );

  await detailRequest;
  await expect(page.getByText("Nested GitLab issue")).toBeVisible();
});

test("embed initialRoute opens detail surface without full app chrome", async ({ page }) => {
  await page.addInitScript(() => {
    window.__middleman_config = {
      embed: {
        initialRoute:
          "/workspaces/embed/detail/gitlab/issue/git.example.com/7" +
          "?repo_path=group%2Fproject",
      },
    };
  });

  const detailRequest = page.waitForRequest(
    (request) =>
      request.method() === "GET" &&
      new URL(request.url()).pathname ===
        "/api/v1/host/git.example.com/issues/gitlab/group/project/7",
  );
  await page.route(
    "**/api/v1/host/git.example.com/issues/gitlab/group/project/7",
    async (route) => {
      await route.fulfill({
        status: 200,
        contentType: "application/json",
        body: JSON.stringify({
          issue: {
            ID: 7,
            RepoID: 7,
            GitHubID: 7007,
            Number: 7,
            URL: "https://git.example.com/group/project/-/issues/7",
            Title: "Initial route GitLab issue",
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
          },
          repo: {
            provider: "gitlab",
            platform_host: "git.example.com",
            owner: "group",
            name: "project",
            repo_path: "group/project",
          },
          events: [],
          platform_host: "git.example.com",
          repo_owner: "group",
          repo_name: "project",
          detail_loaded: true,
          detail_fetched_at: "2026-03-30T14:00:00Z",
        }),
      });
    },
  );

  await page.goto("/");

  await detailRequest;
  await expect(page.locator("header.app-header")).toHaveCount(0);
  await expect(page).toHaveURL(
    /\/workspaces\/embed\/detail\/gitlab\/issue\/git\.example\.com\/7\?repo_path=group%2Fproject$/,
  );
  await expect(page.getByText("Initial route GitLab issue")).toBeVisible();
});

test("full app initializes after navigating away from an initial embed route", async ({ page }) => {
  await page.route("**/api/v1/settings", async (route) => {
    await route.fulfill({
      status: 200,
      contentType: "application/json",
      body: JSON.stringify({
        repos: [],
        activity: {
          view_mode: "threaded",
          time_range: "7d",
          hide_closed: false,
          hide_bots: false,
        },
        terminal: {
          font_family: '"Fira Code", monospace',
          renderer: "xterm",
        },
        agents: [],
      }),
    });
  });

  await page.addInitScript(() => {
    window.__middleman_config = {
      embed: {
        initialRoute: "/workspaces/embed/list",
      },
    };
  });

  await page.goto("/");
  await expect(page.locator("header.app-header")).toHaveCount(0);

  const pullsResponse = page.waitForResponse(
    (response) => new URL(response.url()).pathname === "/api/v1/pulls",
  );
  await page.evaluate(() => {
    window.__middleman_navigate_to_route?.("/pulls");
  });

  await expect(page).toHaveURL(/\/pulls$/);
  await pullsResponse;
  await expect(page.locator("header.app-header")).toBeVisible();
});

test("full app reinitializes after navigating through an embed route", async ({ page }) => {
  let settingsRequests = 0;
  await page.addInitScript(() => {
    const OriginalEventSource = window.EventSource;
    const created: EventSource[] = [];
    const closed: EventSource[] = [];
    class TrackingEventSource extends OriginalEventSource {
      constructor(url: string | URL, eventSourceInitDict?: EventSourceInit) {
        super(url, eventSourceInitDict);
        created.push(this);
      }

      close(): void {
        closed.push(this);
        super.close();
      }
    }
    window.EventSource = TrackingEventSource;
    Object.defineProperty(window, "__middleman_event_source_counts", {
      value: () => ({ created: created.length, closed: closed.length }),
    });
  });
  await page.route("**/api/v1/settings", async (route) => {
    settingsRequests += 1;
    await route.fulfill({
      status: 200,
      contentType: "application/json",
      body: JSON.stringify({
        repos: [],
        activity: {
          view_mode: "threaded",
          time_range: "7d",
          hide_closed: false,
          hide_bots: false,
        },
        terminal: {
          font_family: '"Fira Code", monospace',
          renderer: "xterm",
        },
        agents: [],
      }),
    });
  });

  await page.goto("/pulls");
  await expect(page.locator("header.app-header")).toBeVisible();
  await expect.poll(() => settingsRequests).toBe(1);
  const initialEventSources = await page.evaluate(() =>
    window.__middleman_event_source_counts?.().created ?? 0,
  );
  expect(initialEventSources).toBeGreaterThan(0);

  await page.evaluate(() => {
    window.__middleman_navigate_to_route?.("/workspaces/embed/list");
  });
  await expect(page).toHaveURL(/\/workspaces\/embed\/list$/);
  await expect(page.locator("header.app-header")).toHaveCount(0);
  await expect.poll(async () => page.evaluate(() =>
    window.__middleman_event_source_counts?.().closed ?? 0,
  )).toBeGreaterThanOrEqual(initialEventSources);

  await page.evaluate(() => {
    window.__middleman_navigate_to_route?.("/pulls");
  });
  await expect(page).toHaveURL(/\/pulls$/);
  await expect(page.locator("header.app-header")).toBeVisible();
  await expect.poll(() => settingsRequests).toBe(2);
  await expect.poll(async () => page.evaluate(() =>
    window.__middleman_event_source_counts?.().created ?? 0,
  )).toBeGreaterThan(initialEventSources);
  await expect.poll(async () => page.evaluate(() =>
    window.__middleman_event_source_counts?.().closed ?? 0,
  )).toBeGreaterThanOrEqual(initialEventSources);
});
