import { execFileSync } from "node:child_process";
import { expect, request as playwrightRequest, test, type APIRequestContext, type Page } from "@playwright/test";
import { startIsolatedWorkspaceE2EServer, type IsolatedE2EServer } from "./support/e2eServer";
import { openSettingsPanel } from "./support/settingsPanel";

type WorkspaceResponse = {
  id: string;
  status: string;
  created?: boolean;
  mr_head_repo_kind?: "same_repo" | "fork" | "unknown";
  error_message?: string | null;
};

type RuntimeResponse = {
  sessions?: Array<{
    target_key: string;
  }>;
};

type PersistedRuntimeResponse = {
  target_keys: string[];
};

const workspaceTestTimeoutMs = 120_000;
const agentKey = "e2e-agent";
const agentLabel = "E2E Agent";

function hasCommand(command: string, args: string[] = ["--version"]): boolean {
  try {
    execFileSync(command, args, { stdio: "ignore" });
    return true;
  } catch {
    return false;
  }
}

async function configureAgent(page: Page, baseURL: string): Promise<void> {
  await page.goto(`${baseURL}/settings`);
  await openSettingsPanel(page, "Workspace agents");
  await page.getByRole("button", { name: "Add custom agent" }).click();
  await page.getByLabel("Custom agent key").fill(agentKey);
  await page.getByLabel("Custom agent label").fill(agentLabel);
  await page.getByLabel(`${agentLabel} binary`).fill("sh");
  const saveResponsePromise = page.waitForResponse(
    (response) => response.request().method() === "PUT" && response.url().endsWith("/api/v1/settings"),
  );
  await page.getByRole("button", { name: "Save workspace agents" }).click();
  const response = await saveResponsePromise;
  expect(response.status(), await response.text()).toBe(200);
}

async function createPullRequestWorkspaceWithAgent(page: Page, baseURL: string): Promise<WorkspaceResponse> {
  await page.goto(`${baseURL}/pulls/github/acme/widgets/1`);
  await expect(page.locator(".pull-detail")).toBeVisible();
  const createResponsePromise = page.waitForResponse(
    (response) => response.request().method() === "POST" && response.url().endsWith("/api/v1/workspaces"),
  );
  await page.getByRole("button", { name: "Create Workspace options" }).click();
  await page.getByRole("menuitem", { name: agentLabel }).click();
  const createResponse = await createResponsePromise;
  expect(createResponse.status(), await createResponse.text()).toBe(202);
  return (await createResponse.json()) as WorkspaceResponse;
}

async function waitForWorkspaceReady(api: APIRequestContext, workspaceID: string): Promise<WorkspaceResponse> {
  for (let attempt = 0; attempt < 100; attempt += 1) {
    const response = await api.get(`/api/v1/workspaces/${workspaceID}`);
    if (!response.ok()) {
      const listResponse = await api.get("/api/v1/workspaces");
      throw new Error(
        `GET workspace ${workspaceID} returned ${response.status()}: ${await response.text()}; ` +
          `workspace list ${listResponse.status()}: ${await listResponse.text()}`,
      );
    }
    const workspace = (await response.json()) as WorkspaceResponse;
    if (workspace.status === "ready") return workspace;
    if (workspace.status === "error") {
      throw new Error(workspace.error_message ?? `workspace ${workspaceID} failed to become ready`);
    }
    await new Promise((resolve) => setTimeout(resolve, 100));
  }
  throw new Error(`workspace ${workspaceID} did not become ready`);
}

async function runtimeTargets(api: APIRequestContext, workspaceID: string): Promise<string[]> {
  const response = await api.get(`/api/v1/workspaces/${workspaceID}/runtime`);
  expect(response.ok()).toBe(true);
  const runtime = (await response.json()) as RuntimeResponse;
  return (runtime.sessions ?? []).map((session) => session.target_key);
}

async function persistedRuntimeTargets(api: APIRequestContext, workspaceID: string): Promise<string[]> {
  const response = await api.get(`/__e2e/workspaces/${workspaceID}/persisted-runtime-sessions`);
  expect(response.ok()).toBe(true);
  const runtime = (await response.json()) as PersistedRuntimeResponse;
  return runtime.target_keys;
}

test.describe("workspace create-and-launch full stack", () => {
  test.describe.configure({ timeout: workspaceTestTimeoutMs });

  test("primary create on a reused issue branch does not launch an agent", async ({ page }) => {
    test.skip(
      !hasCommand("git") || !hasCommand("tmux", ["-V"]) || !hasCommand("sh", ["-c", ":"]),
      "git, tmux, and sh are required for the real workspace runtime flow",
    );

    let server: IsolatedE2EServer | null = null;
    let api: APIRequestContext | null = null;
    try {
      server = await startIsolatedWorkspaceE2EServer();
      api = await playwrightRequest.newContext({
        baseURL: server.info.base_url,
      });
      await configureAgent(page, server.info.base_url);
      const branchResponse = await api.post("/__e2e/issue-workspace/reused-branch");
      expect(branchResponse.status(), await branchResponse.text()).toBe(204);

      let launchRequests = 0;
      page.on("request", (request) => {
        if (request.method() === "POST" && /\/api\/v1\/workspaces\/[^/]+\/runtime\/sessions$/.test(request.url())) {
          launchRequests += 1;
        }
      });

      await page.goto(`${server.info.base_url}/issues/github/acme/widgets/10`);
      await expect(page.locator(".issue-detail")).toBeVisible();
      await page.getByRole("button", { name: "Create Workspace", exact: true }).click();

      const conflict = page.getByRole("dialog", {
        name: "Branch Name Conflict",
      });
      await expect(conflict).toBeVisible();
      const reuseResponsePromise = page.waitForResponse(
        (response) =>
          response.request().method() === "POST" &&
          response.status() === 202 &&
          response.url().endsWith("/api/v1/issues/github/acme/widgets/10/workspace"),
      );
      await conflict.getByRole("button", { name: "Use Existing Branch" }).click();

      const reuseResponse = await reuseResponsePromise;
      const reused = (await reuseResponse.json()) as WorkspaceResponse;
      expect(reused.created).toBeUndefined();
      await waitForWorkspaceReady(api, reused.id);
      await expect(page.locator(".workspace-dock-slot").getByRole("button", { name: agentLabel })).toBeVisible();
      await page.evaluate(
        () =>
          new Promise<void>((resolve) => {
            requestAnimationFrame(() => requestAnimationFrame(() => resolve()));
          }),
      );

      expect(launchRequests).toBe(0);
      expect(await runtimeTargets(api, reused.id)).toEqual([]);
      expect(await persistedRuntimeTargets(api, reused.id)).toEqual([]);
    } finally {
      await api?.dispose();
      await server?.stop();
    }
  });

  test("explicit PR agent selection launches and persists without a dialog in the narrow drawer", async ({ page }) => {
    test.skip(
      !hasCommand("git") || !hasCommand("tmux", ["-V"]) || !hasCommand("sh", ["-c", ":"]),
      "git, tmux, and sh are required for the real workspace runtime flow",
    );

    let server: IsolatedE2EServer | null = null;
    let api: APIRequestContext | null = null;
    try {
      server = await startIsolatedWorkspaceE2EServer();
      api = await playwrightRequest.newContext({
        baseURL: server.info.base_url,
      });
      await configureAgent(page, server.info.base_url);

      const launchResponsePromise = page.waitForResponse(
        (response) =>
          response.request().method() === "POST" &&
          /\/api\/v1\/workspaces\/[^/]+\/runtime\/sessions$/.test(response.url()),
      );
      await page.setViewportSize({ width: 390, height: 844 });
      await page.goto(`${server.info.base_url}/pulls/github/acme/widgets/1`);
      await expect(page.locator(".pull-detail")).toBeVisible();
      const create = page.getByRole("button", {
        name: "Create Workspace",
        exact: true,
      });
      const options = page.getByRole("button", {
        name: "Create Workspace options",
      });
      await expect(create).toBeVisible();
      await expect(options).toBeVisible();
      await expect(options).toBeInViewport();
      await expect(create.locator("..")).toHaveCSS("max-width", "100%");
      await expect(create.locator("span")).toHaveCSS("text-overflow", "ellipsis");
      await expect(options).toHaveCSS("flex-shrink", "0");

      const createResponsePromise = page.waitForResponse(
        (response) => response.request().method() === "POST" && response.url().endsWith("/api/v1/workspaces"),
      );
      await options.click();
      await expect(page.getByRole("menuitem", { name: agentLabel })).toBeVisible();
      await page.getByRole("menuitem", { name: agentLabel }).click();
      const createResponse = await createResponsePromise;
      expect(createResponse.status(), await createResponse.text()).toBe(202);
      const created = (await createResponse.json()) as WorkspaceResponse;
      expect(created.created).toBe(true);
      expect(created.mr_head_repo_kind).toBe("same_repo");
      await waitForWorkspaceReady(api, created.id);

      const launchResponse = await launchResponsePromise;
      expect(launchResponse.status(), await launchResponse.text()).toBe(200);
      await expect(page.getByRole("dialog")).toHaveCount(0);
      await expect.poll(() => runtimeTargets(api!, created.id)).toContain(agentKey);
      await expect.poll(() => persistedRuntimeTargets(api!, created.id)).toEqual([agentKey]);
      await expect
        .poll(() => page.evaluate(() => document.activeElement?.closest(".terminal-container") !== null))
        .toBe(true);
    } finally {
      await api?.dispose();
      await server?.stop();
    }
  });

  test("explicit fork-head selection launches through the ordinary manual Launch-menu trust boundary", async ({
    page,
  }) => {
    test.skip(
      !hasCommand("git") || !hasCommand("tmux", ["-V"]) || !hasCommand("sh", ["-c", ":"]),
      "git, tmux, and sh are required for the real workspace runtime flow",
    );

    let server: IsolatedE2EServer | null = null;
    let api: APIRequestContext | null = null;
    try {
      server = await startIsolatedWorkspaceE2EServer();
      api = await playwrightRequest.newContext({
        baseURL: server.info.base_url,
      });
      const forkResponse = await api.post("/__e2e/pr-head-repo/fork");
      expect(forkResponse.status(), await forkResponse.text()).toBe(204);

      await configureAgent(page, server.info.base_url);
      const launchResponsePromise = page.waitForResponse(
        (response) =>
          response.request().method() === "POST" &&
          /\/api\/v1\/workspaces\/[^/]+\/runtime\/sessions$/.test(response.url()),
      );

      const created = await createPullRequestWorkspaceWithAgent(page, server.info.base_url);
      expect(created.created).toBe(true);
      expect(created.mr_head_repo_kind).toBe("fork");
      await waitForWorkspaceReady(api, created.id);
      const launchResponse = await launchResponsePromise;
      expect(launchResponse.status(), await launchResponse.text()).toBe(200);
      await expect(page.getByRole("dialog")).toHaveCount(0);
      await expect.poll(() => runtimeTargets(api!, created.id)).toContain(agentKey);
      await expect.poll(() => persistedRuntimeTargets(api!, created.id)).toEqual([agentKey]);
    } finally {
      await api?.dispose();
      await server?.stop();
    }
  });
});
