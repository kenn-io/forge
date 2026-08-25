// The 00- filename prefix schedules this long-running spec first:
// Playwright dispatches files in path order, and multi-second tests
// that start near the end of the run stretch the suite tail.

import { expect, request as playwrightRequest, test, type APIRequestContext } from "@playwright/test";
import { createServer, request as httpRequest, type Server } from "node:http";
import { networkInterfaces } from "node:os";
import {
  startIsolatedWorkspaceE2EServer,
  startIsolatedWorkspaceE2EServerWithOptions,
  type IsolatedE2EServer,
} from "./support/e2eServer";

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

  test("refreshes the real workspace list after a backend-created workspace appears", async ({ page }) => {
    let isolatedServer: IsolatedE2EServer | null = null;
    let api: APIRequestContext | null = null;
    try {
      isolatedServer = await startIsolatedWorkspaceE2EServer();
      api = await playwrightRequest.newContext({
        baseURL: isolatedServer.info.base_url,
      });

      await page.goto(`${isolatedServer.info.base_url}/workspaces`);
      await expect(page.getByText("No workspaces yet.")).toBeVisible();

      await createIssueWorkspace(api, 10);

      await expect(
        page.locator(".workspace-list-sidebar .ws-row").filter({
          hasText: "Widget rendering broken on Safari",
        }),
      ).toHaveCount(1, { timeout: 7_000 });
    } finally {
      await api?.dispose();
      await isolatedServer?.stop();
    }
  });

  test("loads a real workspace from an insecure HTTP origin", async ({ page }) => {
    test.skip(process.env.KENN_FORGE_E2E_INSECURE_ORIGIN !== "1", "Runs only in the isolated Playwright CI container");

    let isolatedServer: IsolatedE2EServer | null = null;
    let api: APIRequestContext | null = null;
    let proxyServer: Server | null = null;
    try {
      isolatedServer = await startIsolatedWorkspaceE2EServer();
      api = await playwrightRequest.newContext({
        baseURL: isolatedServer.info.base_url,
      });
      await createIssueWorkspace(api, 10);

      const upstreamOrigin = new URL(isolatedServer.info.base_url);
      const proxyHost = Object.values(networkInterfaces())
        .flat()
        .find((address) => address?.family === "IPv4" && !address.internal)?.address;
      if (!proxyHost) {
        throw new Error("no non-loopback IPv4 address is available for the insecure-origin proxy");
      }

      proxyServer = createServer((request, response) => {
        const upstreamRequest = httpRequest(
          new URL(request.url ?? "/", upstreamOrigin),
          {
            method: request.method,
            headers: {
              ...request.headers,
              host: upstreamOrigin.host,
            },
          },
          (upstreamResponse) => {
            response.writeHead(upstreamResponse.statusCode ?? 502, upstreamResponse.headers);
            upstreamResponse.pipe(response);
          },
        );
        upstreamRequest.on("error", (error) => {
          if (!response.headersSent) {
            response.writeHead(502);
          }
          response.end(error.message);
        });
        request.pipe(upstreamRequest);
      });
      await new Promise<void>((resolve, reject) => {
        proxyServer?.once("error", reject);
        proxyServer?.listen(0, proxyHost, resolve);
      });
      const proxyAddress = proxyServer.address();
      if (!proxyAddress || typeof proxyAddress === "string") {
        throw new Error("insecure-origin proxy did not publish a TCP address");
      }

      const insecureOrigin = `http://${proxyHost}:${proxyAddress.port}`;
      await page.goto(`${insecureOrigin}/workspaces`);

      expect(await page.evaluate(() => window.isSecureContext)).toBe(false);
      expect(await page.evaluate(() => typeof crypto.randomUUID)).toBe("undefined");
      await expect(
        page.locator(".workspace-list-sidebar .ws-row").filter({
          hasText: "Widget rendering broken on Safari",
        }),
      ).toHaveCount(1);
    } finally {
      if (proxyServer) {
        proxyServer.closeAllConnections();
        await new Promise<void>((resolve, reject) => {
          proxyServer?.close((error) => {
            if (error) {
              reject(error);
              return;
            }
            resolve();
          });
        });
      }
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

      const requestOrder: string[] = [];
      page.on("request", (request) => {
        const pathname = new URL(request.url()).pathname;
        if (pathname === "/api/v1/workspaces" || pathname === `/api/v1/workspaces/${deletedWorkspace.id}`) {
          requestOrder.push(`${request.method()} ${pathname}`);
        }
      });
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

      const deleteRequestIndex = requestOrder.indexOf(`DELETE /api/v1/workspaces/${deletedWorkspace.id}`);
      const listRefreshIndex = requestOrder.indexOf("GET /api/v1/workspaces", deleteRequestIndex + 1);
      expect(deleteRequestIndex).toBeGreaterThanOrEqual(0);
      expect(listRefreshIndex).toBeGreaterThan(deleteRequestIndex);

      const workspacesResponse = await api.get("/api/v1/workspaces");
      expect(workspacesResponse.ok()).toBe(true);
      const workspacesPayload = (await workspacesResponse.json()) as WorkspaceListResponse;
      expect(workspacesPayload.workspaces.map((workspace) => workspace.id)).not.toContain(deletedWorkspace.id);
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
      await page
        .getByRole("dialog", { name: "Delete workspace?" })
        .getByRole("button", { name: "Delete workspace" })
        .click();
      await deleteStarted;

      const deleteButton = page.locator(".header-bar").getByRole("button", { name: "Delete" });
      await expect(deleteButton).toBeDisabled();
      await page.keyboard.press("Escape");

      await page.locator(".workspace-list-sidebar .ws-row:not(.selected)").click();
      await expect(page).not.toHaveURL(new RegExp(`/terminal/${deletingWorkspace.id}$`));
      await expect(page).toHaveURL(new RegExp(`/terminal/${otherWorkspace.id}$`));

      await page.locator(".header-bar").getByRole("button", { name: "Delete" }).click();
      await page
        .getByRole("dialog", { name: "Delete workspace?" })
        .getByRole("button", { name: "Delete workspace" })
        .click();
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

  test("completed workspace deletion remains observable after its terminal presenter is gone", async ({ page }) => {
    let isolatedServer: IsolatedE2EServer | null = null;
    let api: APIRequestContext | null = null;
    let releaseDelete = (): void => {};
    try {
      isolatedServer = await startIsolatedWorkspaceE2EServer();
      api = await playwrightRequest.newContext({ baseURL: isolatedServer.info.base_url });
      const deletingWorkspace = await createIssueWorkspace(api, 10);
      const remainingWorkspace = await createIssueWorkspace(api, 13);
      const deleteStarted = Promise.withResolvers<void>();
      const deleteMayContinue = Promise.withResolvers<void>();
      releaseDelete = () => deleteMayContinue.resolve();

      await page.route(`**/api/v1/workspaces/${deletingWorkspace.id}`, async (route) => {
        if (route.request().method() !== "DELETE") {
          await route.continue();
          return;
        }
        deleteStarted.resolve();
        await deleteMayContinue.promise;
        await route.continue();
      });

      await page.goto(`${isolatedServer.info.base_url}/terminal/${deletingWorkspace.id}`);
      await expect(page.locator(".workspace-list-sidebar .ws-row")).toHaveCount(2);
      await page.locator(".header-bar").getByRole("button", { name: "Delete" }).click();
      await page
        .getByRole("dialog", { name: "Delete workspace?" })
        .getByRole("button", { name: "Delete workspace" })
        .click();
      await deleteStarted.promise;

      await page.locator(".workspace-list-sidebar .ws-row:not(.selected)").click();
      await expect(page).toHaveURL(new RegExp(`/terminal/${remainingWorkspace.id}$`));

      releaseDelete();
      await expect.poll(async () => (await api!.get(`/api/v1/workspaces/${deletingWorkspace.id}`)).status()).toBe(404);
      await expect(page).toHaveURL(new RegExp(`/terminal/${remainingWorkspace.id}$`));
      const rows = page.locator(".workspace-list-sidebar .ws-row");
      await expect(rows).toHaveCount(1);
      await expect(rows).not.toContainText("Widget rendering broken on Safari");
    } finally {
      releaseDelete();
      await page.unrouteAll({ behavior: "ignoreErrors" });
      await api?.dispose();
      await isolatedServer?.stop();
    }
  });

  test("uncertain workspace delete feedback survives a real route round-trip", async ({ page }) => {
    let isolatedServer: IsolatedE2EServer | null = null;
    let api: APIRequestContext | null = null;
    try {
      isolatedServer = await startIsolatedWorkspaceE2EServer();
      api = await playwrightRequest.newContext({ baseURL: isolatedServer.info.base_url });
      const deletingWorkspace = await createIssueWorkspace(api, 10);
      const otherWorkspace = await createIssueWorkspace(api, 13);

      let releaseDelete!: () => void;
      const deleteMayFail = new Promise<void>((resolve) => {
        releaseDelete = resolve;
      });
      let markDeleteStarted!: () => void;
      const deleteStarted = new Promise<void>((resolve) => {
        markDeleteStarted = resolve;
      });
      let markAuthorityReadStarted!: () => void;
      const authorityReadStarted = new Promise<void>((resolve) => {
        markAuthorityReadStarted = resolve;
      });
      let authorityUnavailable = false;
      await page.route(`**/api/v1/workspaces/${deletingWorkspace.id}`, async (route) => {
        if (route.request().method() === "DELETE") {
          markDeleteStarted();
          await deleteMayFail;
          await route.abort("connectionfailed");
          return;
        }
        if (route.request().method() === "GET" && authorityUnavailable) {
          markAuthorityReadStarted();
          await route.abort("connectionfailed");
          return;
        }
        await route.continue();
      });

      await page.goto(`${isolatedServer.info.base_url}/terminal/${deletingWorkspace.id}`);
      await page.locator(".header-bar").getByRole("button", { name: "Delete" }).click();
      await page
        .getByRole("dialog", { name: "Delete workspace?" })
        .getByRole("button", { name: "Delete workspace" })
        .click();
      await deleteStarted;

      await page.locator(".workspace-list-sidebar .ws-row:not(.selected)").click();
      await expect(page).toHaveURL(new RegExp(`/terminal/${otherWorkspace.id}$`));
      authorityUnavailable = true;
      releaseDelete();
      await authorityReadStarted;
      const uncertainty = "Could not confirm whether the delete completed";
      await expect(page.getByText(uncertainty, { exact: false })).toHaveCount(0);

      authorityUnavailable = false;
      await page.locator(".workspace-list-sidebar .ws-row:not(.selected)").click();
      await expect(page).toHaveURL(new RegExp(`/terminal/${deletingWorkspace.id}$`));
      await expect(page.getByText(uncertainty, { exact: false })).toBeVisible();
      expect((await api.get(`/api/v1/workspaces/${deletingWorkspace.id}`)).status()).toBe(200);
      await expect(page.locator(".header-bar").getByRole("button", { name: "Delete" })).toBeEnabled();
    } finally {
      await api?.dispose();
      await isolatedServer?.stop();
    }
  });
});
