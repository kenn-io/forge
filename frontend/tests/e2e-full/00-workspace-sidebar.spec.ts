// The 00- filename prefix schedules this long-running spec first:
// Playwright dispatches files in path order, and multi-second tests
// that start near the end of the run stretch the suite tail.

import { expect, request as playwrightRequest, test, type APIRequestContext } from "@playwright/test";
import {
  startIsolatedWorkspaceE2EServer,
  startIsolatedWorkspaceE2EServerWithOptions,
  type IsolatedE2EServer,
} from "./support/e2eServer";
import {
  configureMiddlemanKataHome,
  createLiveKataHarness,
  type KataIssueSummary,
  type LiveKataHarness,
  type MiddlemanKataHome,
} from "./support/kataLiveHarness";

type WorkspaceStatusResponse = {
  id: string;
  status: string;
};

type WorkspaceListResponse = {
  workspaces: Array<{
    id: string;
    created_at: string;
    item_last_activity_at?: string | null;
    repo: {
      repo_path: string;
      provider: string;
    };
  }>;
};

const lockedWorkspaceTestTimeoutMs = 120_000;

async function waitForWorkspaceReady(api: APIRequestContext, workspaceId: string): Promise<void> {
  for (let attempt = 0; attempt < 100; attempt += 1) {
    const response = await api.get(`/api/v1/workspaces/${workspaceId}`);
    expect(response.ok()).toBe(true);
    const workspace = (await response.json()) as WorkspaceStatusResponse;
    if (workspace.status === "ready") {
      return;
    }
    if (workspace.status === "error") {
      throw new Error(`workspace ${workspaceId} failed to become ready`);
    }
    await new Promise((resolve) => setTimeout(resolve, 100));
  }

  throw new Error(`workspace ${workspaceId} did not become ready`);
}

async function createIssueWorkspace(api: APIRequestContext, issueNumber: number): Promise<WorkspaceStatusResponse> {
  const response = await api.post(`/api/v1/issues/github/acme/widgets/${issueNumber}/workspace`, {
    data: {},
  });
  expect(response.status()).toBe(202);

  const workspace = (await response.json()) as WorkspaceStatusResponse;
  await waitForWorkspaceReady(api, workspace.id);
  return workspace;
}

test.describe("workspace sidebar full-stack", () => {
  test.describe.configure({ timeout: lockedWorkspaceTestTimeoutMs });

  test("shows retrying copy when the workspace list request stalls", async ({ page }) => {
    let isolatedServer: IsolatedE2EServer | null = null;
    try {
      isolatedServer = await startIsolatedWorkspaceE2EServer();
      await page.route("**/api/v1/workspaces", async () => {
        // Keep the first list request pending so the real app shell
        // exercises the workspace rail's hung-request state.
      });

      await page.goto(`${isolatedServer.info.base_url}/workspaces`);

      await expect(page.getByText("Loading workspaces...")).toBeVisible();
      await expect(page.getByText("Still loading workspaces. Retrying...")).toBeVisible({
        timeout: 12_000,
      });
    } finally {
      await isolatedServer?.stop();
    }
  });

  test("empty Workspaces pane explains creation and renders launch targets from settings", async ({ page }) => {
    let isolatedServer: IsolatedE2EServer | null = null;
    let api: APIRequestContext | null = null;
    try {
      isolatedServer = await startIsolatedWorkspaceE2EServer();
      api = await playwrightRequest.newContext({
        baseURL: isolatedServer.info.base_url,
      });

      const seedResponse = await api.put("/api/v1/settings", {
        data: {
          agents: [
            {
              key: "e2e-agent",
              label: "E2E Agent",
              command: ["/bin/sh", "-lc", "true"],
              enabled: true,
            },
          ],
        },
      });
      const seedBody = await seedResponse.text();
      expect(seedResponse.status(), `PUT /api/v1/settings failed: ${seedBody}`).toBe(200);

      await page.goto(`${isolatedServer.info.base_url}/workspaces`);

      await expect(
        page.getByRole("heading", {
          name: "Create a workspace to run agents on a branch",
        }),
      ).toBeVisible();
      // Regex text matching sees the template's line breaks, so the phrase has
      // to sit on one source line of the copy.
      await expect(page.getByText(/issue-backed and unplanned work start from the/)).toBeVisible();
      await expect(page.getByText(/From a PR or issue, use the/)).toBeVisible();
      await expect(page.getByText(/use New workspace in the sidebar/)).toBeVisible();
      await expect(page.getByRole("button", { name: "Create Workspace" })).toBeDisabled();
      await expect(page.getByText("No workspaces yet.")).toBeVisible();

      const launchSurface = page.getByLabel("Launch surface example");
      await expect(
        launchSurface.getByText("You can then launch configured agents via the buttons provided"),
      ).toBeVisible();
      await expect(launchSurface.getByText("Launch", { exact: true })).toBeVisible();
      await expect(launchSurface.getByRole("button", { name: "E2E Agent" })).toBeDisabled();
      await expect(launchSurface.getByRole("button", { name: "Shell" })).toBeDisabled();

      const iconColor = await launchSurface.locator(".section-icon").evaluate((node) => getComputedStyle(node).color);
      const sectionColor = await launchSurface.locator(".section-bar").evaluate((node) => getComputedStyle(node).color);
      expect(iconColor).toBe(sectionColor);
    } finally {
      await api?.dispose();
      await isolatedServer?.stop();
    }
  });

  test("shows provider icons in group headers when workspaces span multiple providers", async ({ page }) => {
    let isolatedServer: IsolatedE2EServer | null = null;
    let api: APIRequestContext | null = null;
    try {
      isolatedServer = await startIsolatedWorkspaceE2EServer();
      api = await playwrightRequest.newContext({
        baseURL: isolatedServer.info.base_url,
      });

      const githubResponse = await api.post("/api/v1/issues/github/acme/widgets/10/workspace", {
        data: {},
      });
      expect(githubResponse.status()).toBe(202);
      const githubWorkspace = (await githubResponse.json()) as WorkspaceStatusResponse;
      await waitForWorkspaceReady(api, githubWorkspace.id);

      const gitlabResponse = await api.post(
        "/api/v1/host/gitlab.example.com/issues/gitlab/group/project/11/workspace",
        {
          data: {},
        },
      );
      expect(gitlabResponse.status()).toBe(202);
      const gitlabWorkspace = (await gitlabResponse.json()) as WorkspaceStatusResponse;
      await waitForWorkspaceReady(api, gitlabWorkspace.id);

      const workspacesResponse = await api.get("/api/v1/workspaces");
      expect(workspacesResponse.ok()).toBe(true);
      const workspacesPayload = (await workspacesResponse.json()) as {
        workspaces: Array<{ repo: { provider: string } }>;
      };
      expect(new Set(workspacesPayload.workspaces.map((workspace) => workspace.repo.provider))).toEqual(
        new Set(["github", "gitlab"]),
      );

      await page.goto(`${isolatedServer.info.base_url}/terminal/${githubWorkspace.id}`);

      const githubGroup = page.locator(".workspace-list-sidebar .sidebar-group-header").filter({
        has: page.locator(".sidebar-group-header__name", {
          hasText: "acme/widgets",
        }),
      });
      await expect(githubGroup.getByRole("img", { name: "GitHub" })).toBeVisible();

      const gitlabGroup = page.locator(".workspace-list-sidebar .sidebar-group-header").filter({
        has: page.locator(".sidebar-group-header__name", {
          hasText: "group/project",
        }),
      });
      await expect(gitlabGroup.getByRole("img", { name: "GitLab" })).toBeVisible();
    } finally {
      await api?.dispose();
      await isolatedServer?.stop();
    }
  });

  test("flat sort modes order real workspaces by creation time and keep provider identity", async ({ page }) => {
    let isolatedServer: IsolatedE2EServer | null = null;
    let api: APIRequestContext | null = null;
    try {
      isolatedServer = await startIsolatedWorkspaceE2EServer();
      api = await playwrightRequest.newContext({
        baseURL: isolatedServer.info.base_url,
      });

      // Created sequentially, so the gitlab workspace has the newer
      // created_at in the real database. created_at is stored with
      // second granularity (datetime('now')), so space the two
      // creations far enough apart that they cannot tie.
      const githubResponse = await api.post("/api/v1/issues/github/acme/widgets/10/workspace", {
        data: {},
      });
      expect(githubResponse.status()).toBe(202);
      const githubWorkspace = (await githubResponse.json()) as WorkspaceStatusResponse;
      await waitForWorkspaceReady(api, githubWorkspace.id);
      await new Promise((resolve) => setTimeout(resolve, 1_100));

      const gitlabResponse = await api.post(
        "/api/v1/host/gitlab.example.com/issues/gitlab/group/project/11/workspace",
        {
          data: {},
        },
      );
      expect(gitlabResponse.status()).toBe(202);
      const gitlabWorkspace = (await gitlabResponse.json()) as WorkspaceStatusResponse;
      await waitForWorkspaceReady(api, gitlabWorkspace.id);

      const workspacesResponse = await api.get("/api/v1/workspaces");
      expect(workspacesResponse.ok()).toBe(true);
      const workspacesPayload = (await workspacesResponse.json()) as WorkspaceListResponse;
      const expectedItemActivityOrder = workspacesPayload.workspaces
        .filter((workspace) => workspace.id === githubWorkspace.id || workspace.id === gitlabWorkspace.id)
        .sort((a, b) => {
          const aTimestamp = Date.parse(a.item_last_activity_at ?? a.created_at);
          const bTimestamp = Date.parse(b.item_last_activity_at ?? b.created_at);
          return bTimestamp - aTimestamp || a.id.localeCompare(b.id);
        })
        .map((workspace) => workspace.repo.repo_path);
      expect(expectedItemActivityOrder).toHaveLength(2);
      const [firstItemActivityRepo, secondItemActivityRepo] = expectedItemActivityOrder;
      expect(firstItemActivityRepo).toBeDefined();
      expect(secondItemActivityRepo).toBeDefined();

      await page.goto(`${isolatedServer.info.base_url}/terminal/${githubWorkspace.id}`);

      const rows = page.locator(".workspace-list-sidebar .ws-row");
      const headers = page.locator(".workspace-list-sidebar .sidebar-group-header");
      await expect(rows).toHaveCount(2);
      await expect(headers).toHaveCount(2);

      await page.getByTitle("View workspace options").click();
      await page.locator(".kit-filter-dropdown__panel .kit-filter-dropdown__item", { hasText: "Created" }).click();

      // Flat list ordered by the real created_at column: the
      // gitlab workspace was created last, so it sorts first.
      await expect(headers).toHaveCount(0);
      await expect(rows).toHaveCount(2);
      await expect(rows.first().locator(".repo-context")).toContainText("group/project");
      await expect(rows.last().locator(".repo-context")).toContainText("acme/widgets");

      // Provider identity survives without group headers.
      await expect(rows.first().getByRole("img", { name: "GitLab" })).toBeVisible();
      await expect(rows.last().getByRole("img", { name: "GitHub" })).toBeVisible();

      // The choice persists against the real backend across reloads.
      await page.reload();
      await expect(rows).toHaveCount(2);
      await expect(headers).toHaveCount(0);
      await expect(rows.first().locator(".repo-context")).toContainText("group/project");

      await page.getByTitle("View workspace options").click();
      await page
        .locator(".kit-filter-dropdown__panel .kit-filter-dropdown__item", { hasText: "Item activity" })
        .click();

      await expect(headers).toHaveCount(0);
      await expect(rows).toHaveCount(2);
      await expect(rows.nth(0).locator(".repo-context")).toContainText(firstItemActivityRepo ?? "");
      await expect(rows.nth(1).locator(".repo-context")).toContainText(secondItemActivityRepo ?? "");

      // Item activity also persists against the real backend across reloads.
      await page.reload();
      await expect(headers).toHaveCount(0);
      await expect(rows).toHaveCount(2);
      await expect(rows.nth(0).locator(".repo-context")).toContainText(firstItemActivityRepo ?? "");
      await expect(rows.nth(1).locator(".repo-context")).toContainText(secondItemActivityRepo ?? "");
    } finally {
      await api?.dispose();
      await isolatedServer?.stop();
    }
  });

  test("view menu hides org names and PR diff stats against the real backend and persists", async ({ page }) => {
    let isolatedServer: IsolatedE2EServer | null = null;
    let api: APIRequestContext | null = null;
    try {
      isolatedServer = await startIsolatedWorkspaceE2EServer();
      api = await playwrightRequest.newContext({
        baseURL: isolatedServer.info.base_url,
      });

      // acme/widgets PR #1 ships real +240/-30 diff stats in the seeded
      // fixture, so the rail renders its diff-stats chip from backend
      // data rather than a route mock.
      const createResponse = await api.post("/api/v1/workspaces", {
        data: {
          provider: "github",
          platform_host: "github.com",
          owner: "acme",
          name: "widgets",
          mr_number: 1,
        },
      });
      expect(createResponse.status()).toBe(202);
      const workspace = (await createResponse.json()) as WorkspaceStatusResponse;
      await waitForWorkspaceReady(api, workspace.id);

      const workspacesResponse = await api.get("/api/v1/workspaces");
      expect(workspacesResponse.ok()).toBe(true);
      const workspacesPayload = (await workspacesResponse.json()) as {
        workspaces: Array<{ id: string; mr_additions?: number | null; mr_deletions?: number | null }>;
      };
      const seeded = workspacesPayload.workspaces.find((entry) => entry.id === workspace.id);
      expect(seeded?.mr_additions).toBe(240);
      expect(seeded?.mr_deletions).toBe(30);

      // The terminal route derives the rail width from the global
      // sidebar width (clamped to 420px). Pin it wide so the 260px
      // container query that hides diff stats can never fire and mask
      // the toggle's effect.
      await page.addInitScript(() => {
        window.localStorage.setItem("middleman-sidebar-width", "420");
      });

      await page.goto(`${isolatedServer.info.base_url}/terminal/${workspace.id}`);

      const groupLabel = page.locator(".workspace-list-sidebar .sidebar-group-header .sidebar-group-header__name");
      const diffStats = page.locator(".workspace-list-sidebar .workspace-diff-stats");
      const viewTrigger = page.getByTitle("View workspace options");
      const viewBadge = viewTrigger.locator(".kit-filter-dropdown__badge");

      // Defaults: org name shown in the repo label, diff stats visible.
      await expect(groupLabel).toHaveText("acme/widgets");
      await expect(diffStats).toBeVisible();
      await expect(page.getByLabel("240 additions, 30 deletions")).toBeVisible();
      await expect(viewBadge).toHaveCount(0);

      // Visibility toggles do not close the menu, so both can be flipped
      // in a single pass before dismissing it.
      await viewTrigger.click();
      await page
        .locator(".kit-filter-dropdown__panel .kit-filter-dropdown__item", { hasText: "Hide org name" })
        .click();
      await page
        .locator(".kit-filter-dropdown__panel .kit-filter-dropdown__item", { hasText: "Show PR diff stats" })
        .click();
      await page.keyboard.press("Escape");

      await expect(groupLabel).toHaveText("widgets");
      await expect(diffStats).toHaveCount(0);
      // Branch metadata survives hiding the diff stats.
      await expect(page.locator(".workspace-list-sidebar .ws-row .branch-chip")).toBeVisible();
      // Both deviations from default register on the trigger badge.
      await expect(viewBadge).toHaveText("2");

      // Both choices persist against the real backend across a reload.
      await page.reload();
      await expect(groupLabel).toHaveText("widgets");
      await expect(diffStats).toHaveCount(0);
      await expect(viewBadge).toHaveText("2");
    } finally {
      await api?.dispose();
      await isolatedServer?.stop();
    }
  });

  test("context menu delete removes the workspace through the real backend", async ({ page }) => {
    let isolatedServer: IsolatedE2EServer | null = null;
    let api: APIRequestContext | null = null;
    try {
      isolatedServer = await startIsolatedWorkspaceE2EServer();
      api = await playwrightRequest.newContext({
        baseURL: isolatedServer.info.base_url,
      });

      const deletedWorkspace = await createIssueWorkspace(api, 10);
      await createIssueWorkspace(api, 11);

      await page.goto(`${isolatedServer.info.base_url}/terminal/${deletedWorkspace.id}`);

      const rows = page.locator(".workspace-list-sidebar .ws-row");
      await expect(rows).toHaveCount(2);

      const deletedRow = rows.filter({ hasText: "Widget rendering broken on Safari" });
      await expect(deletedRow).toHaveCount(1);
      await deletedRow.click({ button: "right" });

      await page
        .getByRole("menu", { name: "Workspace actions" })
        .getByRole("menuitem", { name: "Delete workspace..." })
        .click();

      const dialog = page.getByRole("dialog", { name: "Delete workspace?" });
      await expect(dialog).toBeVisible();
      await expect(dialog).toContainText("Widget rendering broken on Safari");

      const deleteResponse = page.waitForResponse(
        (response) =>
          response.request().method() === "DELETE" &&
          new URL(response.url()).pathname === `/api/v1/workspaces/${deletedWorkspace.id}`,
      );
      await dialog.getByRole("button", { name: "Delete workspace" }).click();
      expect((await deleteResponse).status()).toBe(204);

      await expect(page).toHaveURL(/\/workspaces$/);
      await expect(rows).toHaveCount(1);
      await expect(rows).not.toContainText("Widget rendering broken on Safari");
      await expect(rows).toContainText("Add dark mode support");

      const workspacesResponse = await api.get("/api/v1/workspaces");
      expect(workspacesResponse.ok()).toBe(true);
      const workspacesPayload = (await workspacesResponse.json()) as WorkspaceListResponse;
      expect(workspacesPayload.workspaces.map((workspace) => workspace.id)).not.toContain(deletedWorkspace.id);
    } finally {
      await api?.dispose();
      await isolatedServer?.stop();
    }
  });

  test("filters real workspace API results and expands collapsed matches during search", async ({ page }) => {
    let isolatedServer: IsolatedE2EServer | null = null;
    let api: APIRequestContext | null = null;
    try {
      isolatedServer = await startIsolatedWorkspaceE2EServer();
      api = await playwrightRequest.newContext({
        baseURL: isolatedServer.info.base_url,
      });

      const safariWorkspace = await createIssueWorkspace(api, 10);
      await createIssueWorkspace(api, 11);

      const workspacesResponse = await api.get("/api/v1/workspaces");
      expect(workspacesResponse.ok()).toBe(true);
      const workspacesPayload = (await workspacesResponse.json()) as {
        workspaces: Array<{
          item_number: number;
          mr_title?: string | null;
        }>;
      };
      expect(
        workspacesPayload.workspaces.some(
          (workspace) => workspace.item_number === 11 && workspace.mr_title === "Add dark mode support",
        ),
      ).toBe(true);

      await page.goto(`${isolatedServer.info.base_url}/terminal/${safariWorkspace.id}`);

      const rows = page.locator(".workspace-list-sidebar .ws-row");
      const groupHeader = page.locator(".workspace-list-sidebar .sidebar-group-header").filter({
        has: page.locator(".sidebar-group-header__name", {
          hasText: "acme/widgets",
        }),
      });
      const filter = page.getByLabel("Filter workspaces");

      await expect(rows).toHaveCount(2);
      await groupHeader.click();
      await expect(rows).toHaveCount(0);

      await filter.fill("#11");
      await expect(rows).toHaveCount(1);
      await expect(rows).toContainText("Add dark mode support");

      await filter.fill("");
      await expect(rows).toHaveCount(0);
    } finally {
      await api?.dispose();
      await isolatedServer?.stop();
    }
  });

  test("issue workspaces expose the Issue tab and hide Reviews", async ({ page }) => {
    let isolatedServer: IsolatedE2EServer | null = null;
    let api: APIRequestContext | null = null;
    try {
      isolatedServer = await startIsolatedWorkspaceE2EServer();
      api = await playwrightRequest.newContext({
        baseURL: isolatedServer.info.base_url,
      });

      const createResponse = await api.post("/api/v1/issues/github/acme/widgets/10/workspace", {
        data: {},
      });
      expect(createResponse.status()).toBe(202);

      const createdWorkspace = (await createResponse.json()) as WorkspaceStatusResponse;
      await waitForWorkspaceReady(api, createdWorkspace.id);

      await page.goto(`${isolatedServer.info.base_url}/terminal/${createdWorkspace.id}`);

      await expect(page.locator(".terminal-view .panel-toggle-btn", { hasText: "Issue" })).toBeVisible();
      await expect(page.locator(".terminal-view .panel-toggle-btn", { hasText: "PR" })).toHaveCount(0);
      await expect(page.locator(".terminal-view .panel-toggle-btn", { hasText: "Reviews" })).toHaveCount(0);

      await page.locator(".terminal-view .panel-toggle-btn", { hasText: "Issue" }).click();
      await expect(page.locator(".right-sidebar")).toBeVisible();
      await expect(page.locator(".right-sidebar .detail-title")).toContainText("Widget rendering broken on Safari");

      // The sidebar detail must scroll internally: .pr-scroll is a
      // constrained flex host, so the ScrollBox viewport owns the overflow
      // instead of the pane growing into an outer native scroller.
      const sidebarDetail = page.locator(".right-sidebar .issue-detail");
      await sidebarDetail.evaluate((el) => {
        const filler = document.createElement("div");
        filler.style.height = "3000px";
        filler.style.flexShrink = "0";
        filler.setAttribute("data-test-filler", "sidebar-scroll");
        el.appendChild(filler);
      });
      const sidebarScroller = page.locator(".right-sidebar").getByRole("region", { name: "Issue conversation" });
      const overflow = await sidebarScroller.evaluate((el) => ({
        scrollHeight: el.scrollHeight,
        clientHeight: el.clientHeight,
      }));
      expect(overflow.scrollHeight).toBeGreaterThan(overflow.clientHeight);
      await sidebarScroller.evaluate((el) => {
        el.scrollTop = el.scrollHeight;
      });
      expect(await sidebarScroller.evaluate((el) => el.scrollTop)).toBeGreaterThan(0);
    } finally {
      await api?.dispose();
      await isolatedServer?.stop();
    }
  });

  test("pending workspace delete stays locked after navigating away and back", async ({ page }) => {
    let isolatedServer: IsolatedE2EServer | null = null;
    let api: APIRequestContext | null = null;
    try {
      isolatedServer = await startIsolatedWorkspaceE2EServer();
      api = await playwrightRequest.newContext({
        baseURL: isolatedServer.info.base_url,
      });

      const deletingWorkspace = await createIssueWorkspace(api, 10);
      const otherWorkspace = await createIssueWorkspace(api, 13);

      let releaseDelete!: () => void;
      const deleteMayContinue = new Promise<void>((resolve) => {
        releaseDelete = resolve;
      });
      let markDeleteStarted!: () => void;
      const deleteStarted = new Promise<void>((resolve) => {
        markDeleteStarted = resolve;
      });
      let releaseOtherDelete!: () => void;
      const otherDeleteMayContinue = new Promise<void>((resolve) => {
        releaseOtherDelete = resolve;
      });
      let markOtherDeleteStarted!: () => void;
      const otherDeleteStarted = new Promise<void>((resolve) => {
        markOtherDeleteStarted = resolve;
      });

      await page.route(`**/api/v1/workspaces/${deletingWorkspace.id}`, async (route) => {
        if (route.request().method() !== "DELETE") {
          await route.continue();
          return;
        }
        markDeleteStarted();
        await deleteMayContinue;
        await route.continue();
      });
      await page.route(`**/api/v1/workspaces/${otherWorkspace.id}`, async (route) => {
        if (route.request().method() !== "DELETE") {
          await route.continue();
          return;
        }
        markOtherDeleteStarted();
        await otherDeleteMayContinue;
        await route.continue();
      });

      await page.goto(`${isolatedServer.info.base_url}/terminal/${deletingWorkspace.id}`);
      await expect(page.locator(".workspace-list-sidebar .ws-row")).toHaveCount(2);

      await page.locator(".header-bar").getByRole("button", { name: "Delete" }).click();
      await deleteStarted;

      const deleteButton = page.locator(".header-bar").getByRole("button", { name: "Delete" });
      await expect(deleteButton).toBeDisabled();
      await page.keyboard.press("Escape");

      await page.locator(".workspace-list-sidebar .ws-row:not(.selected)").click();
      await expect(page).not.toHaveURL(new RegExp(`/terminal/${deletingWorkspace.id}$`));
      await expect(page).toHaveURL(new RegExp(`/terminal/${otherWorkspace.id}$`));

      await page.locator(".header-bar").getByRole("button", { name: "Delete" }).click();
      await otherDeleteStarted;
      await expect(page.locator(".header-bar").getByRole("button", { name: "Delete" })).toBeDisabled();

      await page.locator(".workspace-list-sidebar .ws-row.selected").click({ button: "right" });
      await expect(page.getByRole("menuitem", { name: /Delete workspace|Deleting/ })).toBeDisabled();
      await page.keyboard.press("Escape");

      await page.locator(".workspace-list-sidebar .ws-row:not(.selected)").click();
      await expect(page).toHaveURL(new RegExp(`/terminal/${deletingWorkspace.id}$`));
      await expect(deleteButton).toBeDisabled();

      await page.locator(".workspace-list-sidebar .ws-row.selected").click({ button: "right" });
      await expect(page.getByRole("menuitem", { name: /Delete workspace|Deleting/ })).toBeDisabled();
      await page.keyboard.press("Escape");

      releaseOtherDelete();
      releaseDelete();
      await expect(page).toHaveURL(/\/workspaces$/);
    } finally {
      await api?.dispose();
      await isolatedServer?.stop();
    }
  });
});

test.describe("workspace Kata sidebar live integration", () => {
  test.skip(process.env.MIDDLEMAN_LIVE_KATA_TESTS !== "1", "Set MIDDLEMAN_LIVE_KATA_TESTS=1 to run live Kata e2e.");
  test.describe.configure({ timeout: lockedWorkspaceTestTimeoutMs });

  test("preserves the newer task draft while an older mutation acknowledgement is delayed", async ({ page }) => {
    let harness: LiveKataHarness | null = null;
    let kataHome: MiddlemanKataHome | null = null;
    let isolatedServer: IsolatedE2EServer | null = null;
    let api: APIRequestContext | null = null;
    let releaseAcknowledgement = (): void => {};
    let acknowledgementReleased = false;

    try {
      harness = await createLiveKataHarness();
      kataHome = await configureMiddlemanKataHome(harness.baseURL);
      isolatedServer = await startIsolatedWorkspaceE2EServerWithOptions({ freshProcess: true });
      api = await playwrightRequest.newContext({ baseURL: isolatedServer.info.base_url });

      const source = await harness.seedIssue({
        projectName: "widgets",
        issueTitle: "Persist the source comment",
        issueBody: "Source task for a delayed acknowledgement.",
      });
      const target = await harness.post<{ issue: KataIssueSummary; changed: boolean }>(
        `/api/v1/projects/${source.project.id}/issues`,
        {
          actor: "middleman-e2e",
          title: "Preserve the newer draft",
          body: "Target task selected while the source mutation is pending.",
          force_new: true,
        },
        { "Idempotency-Key": "workspace-sidebar-newer-draft" },
      );

      const linkResponse = await fetch(
        `${harness.baseURL}/api/v1/projects/${source.project.id}/issues/${source.issue.uid}`,
        {
          method: "PATCH",
          headers: { "Content-Type": "application/json" },
          body: JSON.stringify({
            actor: "middleman-e2e",
            links_delta: { add_related: [target.issue.short_id] },
          }),
        },
      );
      expect(linkResponse.status, `link source to target failed: ${await linkResponse.text()}`).toBe(200);

      const createResponse = await api.post("/api/v1/kata/workspaces", {
        data: {
          daemon_id: "live",
          project_uid: source.issue.project_uid,
          project_name: source.project.name,
          issue_uid: source.issue.uid,
          short_id: source.issue.short_id,
          qualified_id: source.issue.qualified_id,
          title: source.issue.title,
        },
      });
      const createBody = await createResponse.text();
      expect(createResponse.status(), `POST /api/v1/kata/workspaces failed: ${createBody}`).toBe(202);
      const sourceWorkspace = JSON.parse(createBody) as WorkspaceStatusResponse;
      await waitForWorkspaceReady(api, sourceWorkspace.id);

      let mutationRequests = 0;
      let markMutationPersisted!: () => void;
      const mutationPersisted = new Promise<void>((resolve) => {
        markMutationPersisted = resolve;
      });
      const acknowledgementMayReturn = new Promise<void>((resolve) => {
        releaseAcknowledgement = resolve;
      });
      await page.route(
        `**/api/v1/kata/proxy/api/v1/projects/${source.project.id}/issues/${source.issue.uid}/comments`,
        async (route) => {
          if (route.request().method() !== "POST") {
            await route.continue();
            return;
          }
          mutationRequests += 1;
          const response = await route.fetch();
          markMutationPersisted();
          await acknowledgementMayReturn;
          await route.fulfill({ response });
        },
      );

      await page.goto(`${isolatedServer.info.base_url}/terminal/${sourceWorkspace.id}`);
      await page.getByRole("button", { name: "Kata task" }).click();

      const pane = page.locator(".kata-workspace-sidebar");
      await expect(pane.getByRole("heading", { name: source.issue.title })).toBeVisible();
      await pane.getByRole("textbox", { name: "Comment" }).fill("Persist exactly once on the source task");
      await pane.getByRole("button", { name: "Add comment" }).click();
      await mutationPersisted;

      await pane.getByRole("button", { name: new RegExp(target.issue.title) }).click();
      await expect(pane.getByRole("heading", { name: target.issue.title })).toBeVisible();

      const targetDraft = pane.getByRole("textbox", { name: "Comment" });
      await targetDraft.fill("Keep this newer task draft");
      await expect(pane.getByRole("button", { name: "Add comment" })).toBeDisabled();
      await expect(pane.getByRole("button", { name: "More actions" })).toBeDisabled();

      releaseAcknowledgement();
      acknowledgementReleased = true;

      await expect(pane.getByRole("button", { name: "Add comment" })).toBeEnabled();
      await expect(targetDraft).toHaveValue("Keep this newer task draft");
      expect(mutationRequests).toBe(1);

      const sourceDetail = await harness.getIssue(source.issue.uid);
      expect(
        (sourceDetail.comments as Array<{ body?: string }> | undefined)?.filter(
          (comment) => comment.body === "Persist exactly once on the source task",
        ),
      ).toHaveLength(1);
    } finally {
      if (!acknowledgementReleased) releaseAcknowledgement();
      await api?.dispose();
      await isolatedServer?.stop();
      await kataHome?.stop();
      await harness?.stop();
    }
  });

  test("preserves comment and related-task drafts edited away and back before replacement", async ({ page }) => {
    let harness: LiveKataHarness | null = null;
    let kataHome: MiddlemanKataHome | null = null;
    let isolatedServer: IsolatedE2EServer | null = null;
    let api: APIRequestContext | null = null;
    let releaseCommentAcknowledgement = (): void => {};
    let commentAcknowledgementReleased = false;
    let releaseRelatedAcknowledgement = (): void => {};
    let relatedAcknowledgementReleased = false;

    try {
      harness = await createLiveKataHarness();
      kataHome = await configureMiddlemanKataHome(harness.baseURL);
      isolatedServer = await startIsolatedWorkspaceE2EServerWithOptions({ freshProcess: true });
      api = await playwrightRequest.newContext({ baseURL: isolatedServer.info.base_url });

      const source = await harness.seedIssue({
        projectName: "widgets",
        issueTitle: "Preserve reverted drafts",
        issueBody: "Source task for comment and related-task ABA drafts.",
      });
      const relatedA = await harness.post<{ issue: KataIssueSummary; changed: boolean }>(
        `/api/v1/projects/${source.project.id}/issues`,
        {
          actor: "middleman-e2e",
          title: "Related task A",
          body: "First valid related-task draft.",
          force_new: true,
        },
        { "Idempotency-Key": "workspace-sidebar-related-a" },
      );
      const relatedB = await harness.post<{ issue: KataIssueSummary; changed: boolean }>(
        `/api/v1/projects/${source.project.id}/issues`,
        {
          actor: "middleman-e2e",
          title: "Related task B",
          body: "Interim related-task draft.",
          force_new: true,
        },
        { "Idempotency-Key": "workspace-sidebar-related-b" },
      );

      const createResponse = await api.post("/api/v1/kata/workspaces", {
        data: {
          daemon_id: "live",
          project_uid: source.issue.project_uid,
          project_name: source.project.name,
          issue_uid: source.issue.uid,
          short_id: source.issue.short_id,
          qualified_id: source.issue.qualified_id,
          title: source.issue.title,
        },
      });
      const createBody = await createResponse.text();
      expect(createResponse.status(), `POST /api/v1/kata/workspaces failed: ${createBody}`).toBe(202);
      const sourceWorkspace = JSON.parse(createBody) as WorkspaceStatusResponse;
      await waitForWorkspaceReady(api, sourceWorkspace.id);

      let commentMutationRequests = 0;
      let markCommentPersisted!: () => void;
      const commentPersisted = new Promise<void>((resolve) => {
        markCommentPersisted = resolve;
      });
      const commentAcknowledgementMayReturn = new Promise<void>((resolve) => {
        releaseCommentAcknowledgement = resolve;
      });
      await page.route(
        `**/api/v1/kata/proxy/api/v1/projects/${source.project.id}/issues/${source.issue.uid}/comments`,
        async (route) => {
          if (route.request().method() !== "POST") {
            await route.continue();
            return;
          }
          commentMutationRequests += 1;
          const response = await route.fetch();
          markCommentPersisted();
          await commentAcknowledgementMayReturn;
          await route.fulfill({ response });
        },
      );

      let relatedMutationRequests = 0;
      let markRelatedPersisted!: () => void;
      const relatedPersisted = new Promise<void>((resolve) => {
        markRelatedPersisted = resolve;
      });
      const relatedAcknowledgementMayReturn = new Promise<void>((resolve) => {
        releaseRelatedAcknowledgement = resolve;
      });
      await page.route(
        `**/api/v1/kata/proxy/api/v1/projects/${source.project.id}/issues/${source.issue.uid}`,
        async (route) => {
          if (route.request().method() !== "PATCH") {
            await route.continue();
            return;
          }
          relatedMutationRequests += 1;
          const response = await route.fetch();
          markRelatedPersisted();
          await relatedAcknowledgementMayReturn;
          await route.fulfill({ response });
        },
      );

      await page.goto(`${isolatedServer.info.base_url}/terminal/${sourceWorkspace.id}`);
      await page.getByRole("button", { name: "Kata task" }).click();

      const pane = page.locator(".kata-workspace-sidebar");
      await expect(pane.getByRole("heading", { name: source.issue.title })).toBeVisible();

      const commentDraft = pane.getByRole("textbox", { name: "Comment" });
      const submittedComment = "Comment draft A";
      await commentDraft.fill(submittedComment);
      await pane.getByRole("button", { name: "Add comment" }).click();
      await commentPersisted;
      await commentDraft.fill("Comment draft B");
      await commentDraft.fill(submittedComment);

      releaseCommentAcknowledgement();
      commentAcknowledgementReleased = true;

      await expect(pane.getByRole("button", { name: "Add comment" })).toBeEnabled();
      await expect(commentDraft).toHaveValue(submittedComment);
      expect(commentMutationRequests).toBe(1);

      const relatedDraft = pane.getByRole("textbox", { name: "Related issue" });
      await relatedDraft.fill(relatedA.issue.short_id);
      await pane.getByRole("button", { name: "Link" }).click();
      await relatedPersisted;
      await relatedDraft.fill(relatedB.issue.short_id);
      await relatedDraft.fill(relatedA.issue.short_id);

      releaseRelatedAcknowledgement();
      relatedAcknowledgementReleased = true;

      await expect(pane.getByRole("button", { name: "Link" })).toBeEnabled();
      await expect(relatedDraft).toHaveValue(relatedA.issue.short_id);
      expect(relatedMutationRequests).toBe(1);

      const sourceDetail = await harness.getIssue(source.issue.uid);
      expect(
        (sourceDetail.comments as Array<{ body?: string }> | undefined)?.filter(
          (comment) => comment.body === submittedComment,
        ),
      ).toHaveLength(1);
      expect(
        (sourceDetail.links as Array<{ from?: { uid?: string }; to?: { uid?: string } }> | undefined)?.filter(
          (link) => link.from?.uid === relatedA.issue.uid || link.to?.uid === relatedA.issue.uid,
        ),
      ).toHaveLength(1);
    } finally {
      if (!commentAcknowledgementReleased) releaseCommentAcknowledgement();
      if (!relatedAcknowledgementReleased) releaseRelatedAcknowledgement();
      await api?.dispose();
      await isolatedServer?.stop();
      await kataHome?.stop();
      await harness?.stop();
    }
  });
});
