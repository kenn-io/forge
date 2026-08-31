import { expect, test } from "@playwright/test";

import { mockApi, mockSettings as defaultSettings } from "./support/mockApi";

test.beforeEach(async ({ page }) => {
  await mockApi(page);
  await page.route("**/api/v1/snapshot**", async (route) => {
    await route.fulfill({
      status: 200,
      contentType: "application/json",
      body: JSON.stringify({ hosts: [], workspaces: [] }),
    });
  });
});

const contextMenuWorkspace = {
  id: "ws-context-menu-geometry",
  platform_host: "github.com",
  repo_owner: "kenn-forge",
  repo_name: "kenn-forge",
  repo: {
    provider: "github",
    platform_host: "github.com",
    owner: "kenn-forge",
    name: "kenn-forge",
    repo_path: "kenn-forge/kenn-forge",
  },
  item_type: "pull_request",
  item_number: 555,
  source_item_visible: true,
  git_head_ref: "fix/issue-cross-reference-events",
  worktree_path: "/tmp/kenn-forge-ws-context-menu",
  tmux_session: "kenn-forge-ws-context-menu",
  tmux_pane_title: null,
  tmux_working: false,
  tmux_activity_source: "unknown",
  tmux_last_output_at: null,
  status: "ready",
  created_at: "2026-06-18T12:00:00Z",
  item_last_activity_at: "2026-06-18T14:00:00Z",
  mr_title: "Show GitHub issue cross-reference events",
  mr_state: "open",
  mr_is_draft: false,
  mr_additions: 18,
  mr_deletions: 4,
  commits_ahead: 1,
  commits_behind: 0,
};

async function mockWorkspaceContextMenuRoutes(page: import("@playwright/test").Page): Promise<void> {
  await page.route("**/api/v1/snapshot**", async (route) => {
    await route.fulfill({
      status: 200,
      contentType: "application/json",
      body: JSON.stringify({
        hosts: [
          {
            configKey: "self",
            diagnostics: [],
            id: "self",
            kind: "self",
            name: "This Mac",
            operationAvailability: {},
            platform: "darwin",
            preferredTransport: "local",
            reachable: true,
            tmuxSessions: [],
          },
        ],
        workspaces: [contextMenuWorkspace],
      }),
    });
  });
}

test("workspaces route renders the terminal workspace list shell", async ({ page }) => {
  await mockWorkspaceContextMenuRoutes(page);
  await page.goto("/workspaces");
  await expect(page.getByText("Select a workspace from the sidebar")).toBeVisible();
});

test("repository selector filters Workspaces and keeps preset actions fixed", async ({ page }) => {
  const repoNames = ["api", "web", ...Array.from({ length: 28 }, (_, index) => `service-${index + 1}`)];
  const repos = repoNames.map((name, index) => ({
    ID: index + 1,
    Owner: "acme",
    Name: name,
    Platform: "github",
    PlatformHost: "github.com",
    PlatformRepoID: `R_${name}`,
  }));
  const configuredRepos = repoNames.map((name) => ({
    provider: "github",
    platform_host: "github.com",
    owner: "acme",
    name,
    repo_path: `acme/${name}`,
    platform_repo_id: `R_${name}`,
    is_glob: false,
    matched_repo_count: 1,
    hidden_from_ui: false,
  }));
  const workspace = (name: string, title: string, number: number) => ({
    ...contextMenuWorkspace,
    id: `ws-${name}`,
    repo_name: name,
    repo: {
      ...contextMenuWorkspace.repo,
      name,
      repo_path: `acme/${name}`,
      platform_repo_id: `R_${name}`,
    },
    item_number: number,
    mr_title: title,
    git_head_ref: `feature/${name}`,
  });

  await page.route("**/api/v1/settings", async (route) => {
    await route.fulfill({
      status: 200,
      contentType: "application/json",
      body: JSON.stringify({
        ...defaultSettings,
        repos: configuredRepos,
        repo_presets: [],
      }),
    });
  });
  await page.route("**/api/v1/repos", async (route) => {
    await route.fulfill({
      status: 200,
      contentType: "application/json",
      body: JSON.stringify(repos),
    });
  });
  await page.route("**/api/v1/snapshot**", async (route) => {
    await route.fulfill({
      status: 200,
      contentType: "application/json",
      body: JSON.stringify({
        hosts: [
          {
            configKey: "peer-one",
            diagnostics: [],
            id: "peer-one",
            kind: "peer",
            name: "Peer One",
            operationAvailability: {},
            platform: "linux",
            preferredTransport: "http",
            reachable: true,
            tmuxSessions: [],
          },
        ],
        workspaces: [
          workspace("api", "API workspace", 1),
          workspace("web", "Web workspace", 2),
          workspace("fleet-only", "Fleet workspace", 3),
        ],
      }),
    });
  });

  await page.goto("/workspaces");
  const selector = page.getByRole("button", { name: "Select repository: Global" });
  await expect(selector).toBeVisible();
  await selector.click();

  const repoList = page.getByRole("listbox", { name: "Repositories" });
  const presetList = page.getByRole("listbox", { name: "Repository presets" });
  const footer = page.locator(".typeahead-footer");
  await expect(presetList.getByRole("option", { name: "Global" })).toBeVisible();
  await expect(footer.getByRole("button", { name: "Save preset" })).toBeVisible();
  expect(await repoList.evaluate((node) => getComputedStyle(node).overflowY)).toBe("auto");
  expect(await repoList.evaluate((node) => node.scrollHeight > node.clientHeight)).toBe(true);

  const footerTop = (await footer.boundingBox())?.y;
  await repoList.evaluate((node) => {
    node.scrollTop = node.scrollHeight;
  });
  expect((await footer.boundingBox())?.y).toBe(footerTop);

  const fleetOption = repoList.getByRole("option", { name: "github/github.com/acme/fleet-only" });
  await expect(fleetOption).toBeVisible();
  await fleetOption.click();
  await expect(page.getByText("Fleet workspace")).toBeVisible();
  await expect(page.getByText("API workspace")).toHaveCount(0);
  await fleetOption.click();

  await repoList.getByRole("option", { name: "github/github.com/acme/api" }).click();
  await expect(page.getByText("API workspace")).toBeVisible();
  await expect(page.getByText("Web workspace")).toHaveCount(0);
});

test("workspace row context menu escapes the clipped sidebar", async ({ page }) => {
  await mockWorkspaceContextMenuRoutes(page);
  await page.setViewportSize({ width: 640, height: 480 });
  await page.goto("/workspaces");

  const row = page.getByRole("button", {
    name: /Show GitHub issue cross-reference events/,
  });
  await expect(row).toBeVisible();
  const rowBox = await row.boundingBox();
  expect(rowBox).not.toBeNull();
  await row.click({
    button: "right",
    position: {
      x: Math.max(1, rowBox!.width - 4),
      y: Math.max(1, rowBox!.height / 2),
    },
  });

  const menu = page.getByRole("menu", { name: "Workspace actions" });
  await expect(menu).toBeVisible();
  await expect(page.locator(".workspace-list-sidebar .workspace-context-menu")).toHaveCount(0);

  const menuBox = await menu.boundingBox();
  expect(menuBox).not.toBeNull();
  expect(menuBox!.x).toBeGreaterThanOrEqual(0);
  expect(menuBox!.y).toBeGreaterThanOrEqual(0);
  expect(menuBox!.x + menuBox!.width).toBeLessThanOrEqual(640);
  expect(menuBox!.y + menuBox!.height).toBeLessThanOrEqual(480);
});

test("workspaces sidebar collapses and expands through the shared control", async ({ page }) => {
  await page.goto("/workspaces");

  const sidebar = page.locator(".kit-sidebar-layout__sidebar").first();
  await expect(sidebar).toBeVisible();

  await sidebar.getByRole("button", { name: "Collapse Workspaces sidebar" }).click();
  await expect(sidebar).toHaveClass(/kit-sidebar-layout__sidebar--collapsed/);

  await sidebar.getByRole("button", { name: "Expand sidebar" }).click();
  await expect(sidebar).not.toHaveClass(/kit-sidebar-layout__sidebar--collapsed/);
});

test("AppHeader workspaces tab navigates to /workspaces", async ({ page }) => {
  await page.goto("/pulls");
  await page.getByRole("button", { name: "Workspaces" }).click();
  await expect(page).toHaveURL(/\/workspaces$/);
});

test("Activity filters survive a reload while viewing Workspaces", async ({ page }) => {
  await page.goto("/");

  const issuesToggle = page.getByRole("switch", { name: "Issues" });
  await expect(issuesToggle).toBeChecked();
  await issuesToggle.click();
  await expect(issuesToggle).not.toBeChecked();

  await page.locator(".activity-feed .activity-filters__trigger").click();
  const commitsFilter = page.locator(".activity-filters__panel").getByRole("button", { name: "Commits", exact: true });
  await expect(commitsFilter).toHaveClass(/\bactive\b/);
  await commitsFilter.click();
  await expect(commitsFilter).not.toHaveClass(/\bactive\b/);

  const activitySearch = new URL(page.url()).search;
  expect(activitySearch).toContain("item_types=pr");
  expect(activitySearch).toContain("event_types=comment%2Creview%2Cforce_push");

  await page.getByRole("button", { name: "Workspaces" }).click();
  await expect(page).toHaveURL(/\/workspaces$/);
  await page.reload();

  await page.getByRole("button", { name: "Activity" }).click();
  await expect.poll(() => new URL(page.url()).search).toBe(activitySearch);
  await expect(page.getByRole("switch", { name: "Issues" })).not.toBeChecked();

  await page.locator(".activity-feed .activity-filters__trigger").click();
  await expect(
    page.locator(".activity-filters__panel").getByRole("button", { name: "Commits", exact: true }),
  ).not.toHaveClass(/\bactive\b/);
});

test("Activity filters survive navigation to a URL that only sets the view", async ({ page }) => {
  await page.goto("/");
  await page.getByRole("switch", { name: "Issues" }).click();

  await page.locator(".activity-feed .activity-filters__trigger").click();
  await page.locator(".activity-filters__panel").getByRole("button", { name: "Commits", exact: true }).click();
  const activityURL = new URL(page.url());
  const itemTypes = activityURL.searchParams.get("item_types");
  const eventTypes = activityURL.searchParams.get("event_types");
  expect(itemTypes).toBe("pr");
  expect(eventTypes).toBe("comment,review,force_push");

  await page.goto("/?view=threaded");

  await expect.poll(() => new URL(page.url()).searchParams.get("item_types")).toBe(itemTypes);
  await expect.poll(() => new URL(page.url()).searchParams.get("event_types")).toBe(eventTypes);
  await expect(page.getByRole("switch", { name: "Issues" })).not.toBeChecked();
  await page.locator(".activity-feed .activity-filters__trigger").click();
  await expect(
    page.locator(".activity-filters__panel").getByRole("button", { name: "Commits", exact: true }),
  ).not.toHaveClass(/\bactive\b/);
});

test("Settings Back to app restores Activity filters after a reload", async ({ page }) => {
  await page.goto("/");
  await page.getByRole("switch", { name: "Issues" }).click();
  await page.locator(".activity-feed .activity-filters__trigger").click();
  await page.locator(".activity-filters__panel").getByRole("button", { name: "Commits", exact: true }).click();
  const activityURL = new URL(page.url());
  const itemTypes = activityURL.searchParams.get("item_types");
  const eventTypes = activityURL.searchParams.get("event_types");
  expect(itemTypes).toBe("pr");
  expect(eventTypes).toBe("comment,review,force_push");

  await page.locator('button[title="Settings"]').click();
  await expect(page.getByRole("button", { name: "Back to app" })).toBeVisible();
  await page.reload();
  await page.getByRole("button", { name: "Back to app" }).click();

  await expect.poll(() => new URL(page.url()).searchParams.get("item_types")).toBe(itemTypes);
  await expect.poll(() => new URL(page.url()).searchParams.get("event_types")).toBe(eventTypes);
  await expect(page.getByRole("switch", { name: "Issues" })).not.toBeChecked();
  await page.locator(".activity-feed .activity-filters__trigger").click();
  await expect(
    page.locator(".activity-filters__panel").getByRole("button", { name: "Commits", exact: true }),
  ).not.toHaveClass(/\bactive\b/);
});

test("repo selector renders icon and still filters repos", async ({ page }) => {
  await page.goto("/pulls");

  const selector = page.getByRole("button", { name: /^Select repository:/ });
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
  await expect(page.getByText("Add browser regression coverage")).toBeVisible();
});

test("hideHeader suppresses AppHeader on the workspaces page", async ({ page }) => {
  await page.addInitScript(() => {
    window.__kenn_forge_config = {
      embed: { hideHeader: true },
    };
  });

  await page.goto("/workspaces");
  await expect(page.locator("header.app-top-bar")).toHaveCount(0);
});

test("navigateToRoute bridge method works", async ({ page }) => {
  await page.goto("/pulls");
  await page.evaluate(() => {
    window.__kenn_forge_navigate_to_route?.("/workspaces");
  });
  await expect(page).toHaveURL(/\/workspaces/);
});

test("workspace bridge methods are registered on startup", async ({ page }) => {
  await page.goto("/workspaces");

  await expect(
    page.evaluate(() => ({
      navigateToRoute: typeof window.__kenn_forge_navigate_to_route,
      updateWorkspace: typeof window.__kenn_forge_update_workspace,
      updateSelection: typeof window.__kenn_forge_update_selection,
      updateHostState: typeof window.__kenn_forge_update_host_state,
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
      new URL(request.url()).pathname === "/api/v1/host/git.example.com/issues/gitlab/group/project/7",
  );
  await page.route("**/api/v1/host/git.example.com/issues/gitlab/group/project/7", async (route) => {
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
  });

  await page.goto("/workspaces/embed/detail/gitlab/issue/git.example.com/group/project/7");

  await detailRequest;
  await expect(page.getByText("Provider-explicit GitLab issue")).toBeVisible();
});

test("nested repo_path embed detail route loads matching detail content", async ({ page }) => {
  const detailRequest = page.waitForRequest(
    (request) =>
      request.method() === "GET" &&
      new URL(request.url()).pathname === "/api/v1/host/git.example.com/issues/gitlab/group%2Fsubgroup/project/7",
  );
  await page.route("**/api/v1/host/git.example.com/issues/gitlab/group%2Fsubgroup/project/7", async (route) => {
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
  });

  await page.goto("/workspaces/embed/detail/gitlab/issue/git.example.com/7" + "?repo_path=group%2Fsubgroup%2Fproject");

  await detailRequest;
  await expect(page.getByText("Nested GitLab issue")).toBeVisible();
});

test("embed initialRoute opens detail surface without full app chrome", async ({ page }) => {
  await page.addInitScript(() => {
    window.__kenn_forge_config = {
      embed: {
        initialRoute: "/workspaces/embed/detail/gitlab/issue/git.example.com/7" + "?repo_path=group%2Fproject",
      },
    };
  });

  const detailRequest = page.waitForRequest(
    (request) =>
      request.method() === "GET" &&
      new URL(request.url()).pathname === "/api/v1/host/git.example.com/issues/gitlab/group/project/7",
  );
  await page.route("**/api/v1/host/git.example.com/issues/gitlab/group/project/7", async (route) => {
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
  });

  await page.goto("/");

  await detailRequest;
  await expect(page.locator("header.app-top-bar")).toHaveCount(0);
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
        ...defaultSettings,
        repos: [
          {
            provider: "github",
            platform_host: "github.com",
            owner: "acme",
            name: "widgets",
            repo_path: "acme/widgets",
            is_glob: false,
            matched_repo_count: 1,
          },
        ],
        activity: {
          ...defaultSettings.activity,
          view_mode: "threaded",
        },
        terminal: {
          ...defaultSettings.terminal,
          font_family: '"Fira Code", monospace',
          font_size: 14,
        },
      }),
    });
  });

  await page.addInitScript(() => {
    window.__kenn_forge_config = {
      embed: {
        initialRoute: "/workspaces/embed/list",
      },
    };
  });

  await page.goto("/");
  await expect(page.locator("header.app-top-bar")).toHaveCount(0);

  const pullsResponse = page.waitForResponse((response) => new URL(response.url()).pathname === "/api/v1/pulls");
  await page.evaluate(() => {
    window.__kenn_forge_navigate_to_route?.("/pulls");
  });

  await expect(page).toHaveURL(/\/pulls$/);
  await pullsResponse;
  await expect(page.locator("header.app-top-bar")).toBeVisible();
});

test("full app reinitializes after navigating through an embed route without refetching cached settings", async ({
  page,
}) => {
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
    Object.defineProperty(window, "__kenn_forge_event_source_counts", {
      value: () => ({ created: created.length, closed: closed.length }),
    });
  });
  await page.route("**/api/v1/settings", async (route) => {
    settingsRequests += 1;
    await route.fulfill({
      status: 200,
      contentType: "application/json",
      body: JSON.stringify({
        ...defaultSettings,
        repos: [
          {
            provider: "github",
            platform_host: "github.com",
            owner: "acme",
            name: "widgets",
            repo_path: "acme/widgets",
            is_glob: false,
            matched_repo_count: 1,
          },
        ],
        activity: {
          ...defaultSettings.activity,
          view_mode: "threaded",
        },
        terminal: {
          ...defaultSettings.terminal,
          font_family: '"Fira Code", monospace',
          font_size: 14,
        },
      }),
    });
  });

  await page.goto("/pulls");
  await expect(page.locator("header.app-top-bar")).toBeVisible();
  await expect.poll(() => settingsRequests).toBe(1);
  const initialEventSources = await page.evaluate(() => window.__kenn_forge_event_source_counts?.().created ?? 0);
  expect(initialEventSources).toBeGreaterThan(0);

  await page.evaluate(() => {
    window.__kenn_forge_navigate_to_route?.("/workspaces/embed/list");
  });
  await expect(page).toHaveURL(/\/workspaces\/embed\/list$/);
  await expect(page.locator("header.app-top-bar")).toHaveCount(0);
  await expect.poll(() => settingsRequests).toBe(1);
  await expect
    .poll(async () => page.evaluate(() => window.__kenn_forge_event_source_counts?.().closed ?? 0))
    .toBeGreaterThanOrEqual(initialEventSources);

  await page.evaluate(() => {
    window.__kenn_forge_navigate_to_route?.("/pulls");
  });
  await expect(page).toHaveURL(/\/pulls$/);
  await expect(page.locator("header.app-top-bar")).toBeVisible();
  await expect.poll(() => settingsRequests).toBe(1);
  await expect
    .poll(async () => page.evaluate(() => window.__kenn_forge_event_source_counts?.().created ?? 0))
    .toBeGreaterThan(initialEventSources);
  await expect
    .poll(async () => page.evaluate(() => window.__kenn_forge_event_source_counts?.().closed ?? 0))
    .toBeGreaterThanOrEqual(initialEventSources);
});
