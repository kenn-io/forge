// The 00- filename prefix schedules this long-running spec first:
// Playwright dispatches files in path order, and multi-second tests
// that start near the end of the run stretch the suite tail.

import { expect, request as playwrightRequest, test, type APIRequestContext, type Locator } from "@playwright/test";
import { startIsolatedWorkspaceE2EServer, type IsolatedE2EServer } from "./support/e2eServer";

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

type TerminalCanvasStats = {
  hash: string;
  paintedPixels: number;
  width: number;
  height: number;
};

async function readTerminalCanvasStats(canvas: Locator): Promise<TerminalCanvasStats> {
  return await canvas.evaluate((node) => {
    const terminalCanvas = node as HTMLCanvasElement;
    const context = terminalCanvas.getContext("2d");
    if (!context) {
      throw new Error("terminal canvas 2d context unavailable");
    }

    const { width, height } = terminalCanvas;
    const data = context.getImageData(0, 0, width, height).data;
    let paintedPixels = 0;
    let hash = 0x811c9dc5;
    for (let i = 0; i < data.length; i += 4) {
      const red = data[i] ?? 0;
      const green = data[i + 1] ?? 0;
      const blue = data[i + 2] ?? 0;
      const alpha = data[i + 3] ?? 0;
      if (alpha > 0 && Math.abs(red - 0x0d) + Math.abs(green - 0x11) + Math.abs(blue - 0x17) > 24) {
        paintedPixels += 1;
      }
      hash ^= red;
      hash = Math.imul(hash, 0x01000193) >>> 0;
      hash ^= green;
      hash = Math.imul(hash, 0x01000193) >>> 0;
      hash ^= blue;
      hash = Math.imul(hash, 0x01000193) >>> 0;
      hash ^= alpha;
      hash = Math.imul(hash, 0x01000193) >>> 0;
    }

    return {
      hash: hash.toString(16),
      paintedPixels,
      width,
      height,
    };
  });
}

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

      const githubGroup = page.locator(".workspace-list-sidebar .group-header").filter({
        has: page.locator(".group-label", {
          hasText: "acme/widgets",
        }),
      });
      await expect(githubGroup.getByRole("img", { name: "GitHub" })).toBeVisible();

      const gitlabGroup = page.locator(".workspace-list-sidebar .group-header").filter({
        has: page.locator(".group-label", {
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
      const headers = page.locator(".workspace-list-sidebar .group-header");
      await expect(rows).toHaveCount(2);
      await expect(headers).toHaveCount(2);

      await page.getByTitle("View workspace options").click();
      await page.locator(".filter-dropdown .filter-item", { hasText: "Created" }).click();

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
      await page.locator(".filter-dropdown .filter-item", { hasText: "Item activity" }).click();

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

      const groupLabel = page.locator(".workspace-list-sidebar .group-header .group-label");
      const diffStats = page.locator(".workspace-list-sidebar .workspace-diff-stats");
      const viewTrigger = page.getByTitle("View workspace options");
      const viewBadge = viewTrigger.locator(".filter-badge");

      // Defaults: org name shown in the repo label, diff stats visible.
      await expect(groupLabel).toHaveText("acme/widgets");
      await expect(diffStats).toBeVisible();
      await expect(page.getByLabel("240 additions, 30 deletions")).toBeVisible();
      await expect(viewBadge).toHaveCount(0);

      // Visibility toggles do not close the menu, so both can be flipped
      // in a single pass before dismissing it.
      await viewTrigger.click();
      await page.locator(".filter-dropdown .filter-item", { hasText: "Show org names" }).click();
      await page.locator(".filter-dropdown .filter-item", { hasText: "Show PR diff stats" }).click();
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
      const groupHeader = page.locator(".workspace-list-sidebar .group-header").filter({
        has: page.locator(".group-label", {
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

      await expect(page.locator(".terminal-view .seg-btn", { hasText: "Issue" })).toBeVisible();
      await expect(page.locator(".terminal-view .seg-btn", { hasText: "PR" })).toHaveCount(0);
      await expect(page.locator(".terminal-view .seg-btn", { hasText: "Reviews" })).toHaveCount(0);

      await page.locator(".terminal-view .seg-btn", { hasText: "Issue" }).click();
      await expect(page.locator(".right-sidebar")).toBeVisible();
      await expect(page.locator(".right-sidebar .detail-title")).toContainText("Widget rendering broken on Safari");
    } finally {
      await api?.dispose();
      await isolatedServer?.stop();
    }
  });

  test("ghostty shell terminal paints output and accepts browser keyboard input", async ({ page }) => {
    let isolatedServer: IsolatedE2EServer | null = null;
    let api: APIRequestContext | null = null;
    try {
      isolatedServer = await startIsolatedWorkspaceE2EServer();
      api = await playwrightRequest.newContext({
        baseURL: isolatedServer.info.base_url,
      });
      const settingsResponse = await api.put("/api/v1/settings", {
        data: {
          terminal: {
            font_family: "",
            font_size: 14,
            scrollback: 1000,
            line_height: 1,
            letter_spacing: 0,
            cursor_blink: true,
            font_ligatures: false,
            hide_tmux_status: false,
            renderer: "ghostty-web",
          },
        },
      });
      expect(settingsResponse.status()).toBe(200);

      const createResponse = await api.post("/api/v1/issues/github/acme/widgets/10/workspace", {
        data: {},
      });
      expect(createResponse.status()).toBe(202);

      const createdWorkspace = (await createResponse.json()) as WorkspaceStatusResponse;
      await waitForWorkspaceReady(api, createdWorkspace.id);

      await page.goto(`${isolatedServer.info.base_url}/terminal/${createdWorkspace.id}`);
      await page.getByRole("button", { name: "Open terminal panel" }).click();

      const canvas = page.locator(".terminal-panel.open .terminal-container canvas");
      await expect(canvas).toBeVisible();
      await expect
        .poll(async () => {
          const stats = await readTerminalCanvasStats(canvas);
          return stats.width > 0 && stats.height > 0 && stats.paintedPixels > 0;
        })
        .toBe(true);

      const beforeInput = await readTerminalCanvasStats(canvas);
      await canvas.click({ position: { x: 10, y: 10 } });
      await page.keyboard.type("printf 'MIDDLEMAN_GHOSTTY_E2E_INPUT_REACHED_1234567890'");
      await page.keyboard.press("Enter");

      await expect
        .poll(
          async () => {
            const stats = await readTerminalCanvasStats(canvas);
            return stats.hash !== beforeInput.hash && Math.abs(stats.paintedPixels - beforeInput.paintedPixels) > 300;
          },
          { timeout: 10_000 },
        )
        .toBe(true);
    } finally {
      await api?.dispose();
      await isolatedServer?.stop();
    }
  });
});
