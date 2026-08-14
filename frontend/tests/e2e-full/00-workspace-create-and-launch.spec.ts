import { execFileSync } from "node:child_process";
import { writeFileSync } from "node:fs";
import { join } from "node:path";
import {
  devices,
  expect,
  request as playwrightRequest,
  test,
  type APIRequestContext,
  type BrowserContext,
  type Page,
} from "@playwright/test";
import { startIsolatedWorkspaceE2EServer, type IsolatedE2EServer } from "./support/e2eServer";
import { openSettingsPanel } from "./support/settingsPanel";

type WorkspaceResponse = {
  id: string;
  status: string;
  git_head_ref?: string;
  worktree_path?: string;
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

type LaunchDialogProbe = {
  phase?: string;
  firstAppearancePhase?: string | null;
};

async function launchDialogFirstAppearance(page: Page): Promise<string | null> {
  return page.evaluate(
    () =>
      (Reflect.get(window, "__middlemanCreateLaunchDialogProbe") as LaunchDialogProbe | undefined)
        ?.firstAppearancePhase ?? null,
  );
}

async function setLaunchDialogProbePhase(page: Page, phase: string): Promise<void> {
  await page.evaluate((nextPhase) => {
    const probe = Reflect.get(window, "__middlemanCreateLaunchDialogProbe") as LaunchDialogProbe | undefined;
    if (probe) probe.phase = nextPhase;
  }, phase);
}

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

async function setRetainedSessions(api: APIRequestContext, limit: number): Promise<void> {
  const currentResponse = await api.get("/api/v1/settings");
  expect(currentResponse.ok()).toBe(true);
  const current = (await currentResponse.json()) as { terminal: Record<string, unknown> };
  const updateResponse = await api.put("/api/v1/settings", {
    data: { terminal: { ...current.terminal, retained_sessions: limit } },
  });
  expect(updateResponse.ok(), await updateResponse.text()).toBe(true);
}

async function trackSessionWebSockets(page: Page): Promise<void> {
  await page.addInitScript(() => {
    const log: Array<{ id: number; url: string; closed: boolean; sent: string[] }> = [];
    const entries = new WeakMap<WebSocket, (typeof log)[number]>();
    Reflect.set(window, "__mobileWorkspaceWebSockets", log);
    const Native = window.WebSocket;
    class TrackedWebSocket extends Native {
      constructor(url: string | URL, protocols?: string | string[]) {
        super(url, protocols);
        const entry = { id: log.length + 1, url: String(url), closed: false, sent: [] as string[] };
        log.push(entry);
        entries.set(this, entry);
        this.addEventListener("close", () => {
          entry.closed = true;
        });
      }

      override send(data: string | ArrayBufferLike | Blob | ArrayBufferView): void {
        const entry = entries.get(this);
        if (typeof data === "string") entry?.sent.push(data);
        else if (ArrayBuffer.isView(data)) entry?.sent.push(new TextDecoder().decode(data));
        else if (data instanceof ArrayBuffer) entry?.sent.push(new TextDecoder().decode(data));
        super.send(data);
      }
    }
    window.WebSocket = TrackedWebSocket as unknown as typeof WebSocket;
  });
}

function sessionWebSockets(page: Page, workspaceId: string) {
  return page.evaluate((needle) => {
    const log = Reflect.get(window, "__mobileWorkspaceWebSockets") as
      | Array<{ id: number; url: string; closed: boolean; sent: string[] }>
      | undefined;
    return (log ?? []).filter((entry) => entry.url.includes(needle));
  }, `/workspaces/${workspaceId}/runtime/sessions/`);
}

async function liveSessionWebSockets(page: Page, workspaceId: string) {
  return (await sessionWebSockets(page, workspaceId)).filter((entry) => !entry.closed);
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
      const launcher = page.getByRole("dialog", { name: "Launch a session" });
      await expect(launcher).toBeVisible();
      await expect(launcher.getByRole("button", { name: agentLabel })).toBeVisible();
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

  test("explicit PR agent selection never opens a transient dialog and fits the narrow drawer", async ({ page }) => {
    test.skip(
      !hasCommand("git") || !hasCommand("tmux", ["-V"]) || !hasCommand("sh", ["-c", ":"]),
      "git, tmux, and sh are required for the real workspace runtime flow",
    );

    let server: IsolatedE2EServer | null = null;
    let api: APIRequestContext | null = null;
    const createResponseGate: { release: (() => void) | null } = { release: null };
    const launchResponseGate: { release: (() => void) | null } = { release: null };
    try {
      server = await startIsolatedWorkspaceE2EServer();
      api = await playwrightRequest.newContext({
        baseURL: server.info.base_url,
      });
      await configureAgent(page, server.info.base_url);

      const createResponseRelease = new Promise<void>((resolve) => {
        createResponseGate.release = resolve;
      });
      let markCreateResponseHeld!: () => void;
      const createResponseHeld = new Promise<void>((resolve) => {
        markCreateResponseHeld = resolve;
      });
      await page.route("**/api/v1/workspaces", async (route) => {
        if (route.request().method() !== "POST") {
          await route.continue();
          return;
        }
        const response = await route.fetch();
        markCreateResponseHeld();
        await createResponseRelease;
        await route.fulfill({ response });
      });

      const launchResponseRelease = new Promise<void>((resolve) => {
        launchResponseGate.release = resolve;
      });
      let markLaunchRequestHeld!: () => void;
      const launchRequestHeld = new Promise<void>((resolve) => {
        markLaunchRequestHeld = resolve;
      });
      await page.route("**/api/v1/workspaces/*/runtime/sessions", async (route) => {
        if (route.request().method() !== "POST") {
          await route.continue();
          return;
        }
        markLaunchRequestHeld();
        await launchResponseRelease;
        const response = await route.fetch();
        await route.fulfill({ response });
      });

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
      await page.setViewportSize({ width: 1280, height: 800 });
      await page.reload();
      await expect(page.locator(".pull-detail")).toBeVisible();

      // An end-state absence assertion can miss a dialog that mounts for one
      // render and closes when either startup response arrives. Record every
      // added launcher node so the transient startup state remains observable.
      await page.evaluate(() => {
        const selector = '[role="dialog"][aria-label="Launch a session"]';
        const initiallyPresent = document.querySelector(selector) !== null;
        const state = {
          phase: "create-pending",
          firstAppearancePhase: initiallyPresent ? "initial" : null,
        };
        Reflect.set(window, "__middlemanCreateLaunchDialogProbe", state);
        new MutationObserver((records) => {
          for (const record of records) {
            for (const node of record.addedNodes) {
              if (node instanceof Element && (node.matches(selector) || node.querySelector(selector) !== null)) {
                state.firstAppearancePhase ??= state.phase;
              }
            }
          }
        }).observe(document.body, { childList: true, subtree: true });
      });

      const createResponsePromise = page.waitForResponse(
        (response) => response.request().method() === "POST" && response.url().endsWith("/api/v1/workspaces"),
      );
      await options.click();
      await expect(page.getByRole("menuitem", { name: agentLabel })).toBeVisible();
      await page.getByRole("menuitem", { name: agentLabel }).click();

      await createResponseHeld;
      await expect(page.locator(".detail-pane-workspace-slot")).toBeVisible();
      await expect(page.getByRole("button", { name: "Launch session" })).toBeVisible();
      await page.evaluate(
        () =>
          new Promise<void>((resolve) => {
            requestAnimationFrame(() => requestAnimationFrame(() => resolve()));
          }),
      );
      const launcherAppearedBeforeCreateResponse = await launchDialogFirstAppearance(page);
      expect(launcherAppearedBeforeCreateResponse).toBeNull();
      await setLaunchDialogProbePhase(page, "create-response-released");
      createResponseGate.release?.();
      createResponseGate.release = null;

      const createResponse = await createResponsePromise;
      expect(createResponse.status(), await createResponse.text()).toBe(202);
      const created = (await createResponse.json()) as WorkspaceResponse;
      expect(created.created).toBe(true);
      expect(created.mr_head_repo_kind).toBe("same_repo");
      await waitForWorkspaceReady(api, created.id);
      await expect(page.locator(".detail-pane-workspace-slot")).toBeVisible();
      await expect(page.getByRole("button", { name: "Launch session" })).toBeVisible();

      await launchRequestHeld;
      await setLaunchDialogProbePhase(page, "launch-request-held");
      await page.evaluate(
        () =>
          new Promise<void>((resolve) => {
            requestAnimationFrame(() => requestAnimationFrame(() => resolve()));
          }),
      );
      const launcherAppearedWhilePending = await launchDialogFirstAppearance(page);
      expect(launcherAppearedWhilePending).toBeNull();
      await setLaunchDialogProbePhase(page, "launch-response-released");
      launchResponseGate.release?.();
      launchResponseGate.release = null;

      const launchResponse = await launchResponsePromise;
      expect(launchResponse.status(), await launchResponse.text()).toBe(200);
      await expect(page.getByRole("dialog")).toHaveCount(0);
      await expect.poll(() => runtimeTargets(api!, created.id)).toContain(agentKey);
      await expect.poll(() => persistedRuntimeTargets(api!, created.id)).toEqual([agentKey]);
      await expect
        .poll(() => page.evaluate(() => document.activeElement?.closest(".terminal-container") !== null))
        .toBe(true);
      const launcherEverAppeared = await launchDialogFirstAppearance(page);
      expect(launcherEverAppeared).toBeNull();
    } finally {
      createResponseGate.release?.();
      launchResponseGate.release?.();
      await page.unrouteAll({ behavior: "ignoreErrors" });
      await api?.dispose();
      await server?.stop();
    }
  });

  test("phone workspace creation uses touch, stops, and force-deletes an active dirty workspace", async ({
    browser,
    page,
  }) => {
    test.skip(
      !hasCommand("git") || !hasCommand("tmux", ["-V"]) || !hasCommand("sh", ["-c", ":"]),
      "git, tmux, and sh are required for the real workspace runtime flow",
    );

    let server: IsolatedE2EServer | null = null;
    let api: APIRequestContext | null = null;
    let phoneContext: BrowserContext | null = null;
    try {
      server = await startIsolatedWorkspaceE2EServer();
      api = await playwrightRequest.newContext({
        baseURL: server.info.base_url,
      });
      await configureAgent(page, server.info.base_url);
      phoneContext = await browser.newContext({ ...devices["Pixel 7"] });
      const phonePage = await phoneContext.newPage();
      await trackSessionWebSockets(phonePage);

      const createResponsePromise = phonePage.waitForResponse(
        (response) =>
          response.request().method() === "POST" &&
          /\/api\/v1\/repo\/github\/acme\/widgets\/workspaces$/.test(response.url()),
      );
      const launchResponsePromise = phonePage.waitForResponse(
        (response) =>
          response.request().method() === "POST" &&
          /\/api\/v1\/workspaces\/[^/]+\/runtime\/sessions$/.test(response.url()),
      );

      await phonePage.goto(`${server.info.base_url}/m/workspaces`);
      await phonePage.getByRole("button", { name: "New workspace" }).tap();
      const dialog = phonePage.getByRole("dialog", { name: "New workspace" });
      await expect(dialog).toBeVisible();
      await dialog.getByRole("button", { name: "Filter repositories" }).tap();
      await dialog.getByRole("option", { name: /acme\/widgets/ }).tap();
      await expect(dialog.getByRole("button", { name: "Filter repositories" })).toContainText("acme/widgets");
      await dialog.getByRole("button", { name: "Create workspace options" }).tap();
      const agent = phonePage.getByRole("menuitem", { name: agentLabel });
      await expect(agent).toBeVisible();
      await agent.tap();

      const createResponse = await createResponsePromise;
      expect(createResponse.status(), await createResponse.text()).toBe(202);
      const created = (await createResponse.json()) as WorkspaceResponse;
      await expect(phonePage).toHaveURL(new RegExp(`/m/workspaces/local/${created.id}$`));
      const ready = await waitForWorkspaceReady(api, created.id);
      const launchResponse = await launchResponsePromise;
      expect(launchResponse.status(), await launchResponse.text()).toBe(200);
      const launchedSession = (await launchResponse.json()) as { key: string };
      await expect.poll(() => runtimeTargets(api!, created.id)).toContain(agentKey);
      await expect.poll(() => persistedRuntimeTargets(api!, created.id)).toEqual([agentKey]);

      const input = phonePage.getByRole("textbox", { name: "Terminal command" });
      await expect(input).not.toBeVisible();
      const composerToggle = phonePage.getByRole("button", { name: "Open terminal composer" });
      await expect(composerToggle).toBeVisible();
      await composerToggle.tap();
      await expect(input).toBeVisible();
      const initialInputHeight = (await input.boundingBox())?.height ?? 0;
      await input.fill("printf 'mobile-input-ok\\n'\nprintf 'second-line\\n'");
      await expect(input).toHaveValue("printf 'mobile-input-ok\\n'\nprintf 'second-line\\n'");
      await expect.poll(async () => (await input.boundingBox())?.height ?? 0).toBeGreaterThan(initialInputHeight);
      await phonePage.getByRole("button", { name: "Send terminal input" }).tap();
      await expect(input).toHaveValue("");
      await expect.poll(async () => (await input.boundingBox())?.height ?? 0).toBeLessThanOrEqual(initialInputHeight);
      await expect
        .poll(async () => (await sessionWebSockets(phonePage, created.id)).flatMap((socket) => socket.sent))
        .toContain("\x1b[200~printf 'mobile-input-ok\\n'\rprintf 'second-line\\n'\x1b[201~\r");

      expect(ready.worktree_path).toBeTruthy();
      const hookResponse = await api.post("/api/v1/agent-hooks/claude", {
        headers: { "X-Kenn-Forge-Runtime-Session-Key": launchedSession.key },
        data: {
          session_id: "mobile-agent",
          cwd: ready.worktree_path,
          hook_event_name: "UserPromptSubmit",
        },
      });
      expect(hookResponse.ok(), await hookResponse.text()).toBe(true);
      await phonePage.goto(`${server.info.base_url}/m/workspaces`);
      await expect(phonePage.getByRole("button", { name: /agent working/ })).toContainText("Working");
      await phonePage.goto(`${server.info.base_url}/m/workspaces/local/${created.id}`);

      await expect(phonePage.getByRole("button", { name: "Launch session" })).toHaveCount(0);
      await expect(phonePage.getByRole("button", { name: `Stop terminal ${agentLabel}` })).toHaveCount(0);
      const terminalOptionsButton = phonePage.getByRole("button", { name: "Terminal options" });
      await terminalOptionsButton.tap();
      const terminalOptions = phonePage.getByRole("dialog", { name: "Terminal options" });
      await expect(terminalOptions.getByRole("spinbutton", { name: "Font size", exact: true })).toBeVisible();
      await terminalOptions.getByRole("button", { name: "Choose" }).tap();
      const fontDialog = phonePage.getByRole("dialog", { name: "Choose monospace font" });
      await expect(fontDialog).toBeVisible();
      await phonePage.keyboard.press("Escape");
      await expect(fontDialog).not.toBeVisible();
      await expect(terminalOptions).toBeVisible();
      await terminalOptions.getByRole("button", { name: "New terminal" }).tap();
      const launchSheet = phonePage.getByRole("dialog", { name: "Launch workspace session" });
      await expect(launchSheet).toBeVisible();
      await launchSheet.getByRole("button", { name: "Close launch session" }).tap();

      await terminalOptionsButton.tap();
      await terminalOptions.getByRole("button", { name: `Stop terminal ${agentLabel}` }).tap();
      const confirmation = phonePage.getByRole("dialog", { name: "Stop terminal?" });
      await expect(confirmation).toBeVisible();
      expect(await runtimeTargets(api, created.id)).toContain(agentKey);
      await confirmation.getByRole("button", { name: "Stop terminal" }).tap();
      await expect(confirmation).not.toBeVisible();
      await expect.poll(() => runtimeTargets(api!, created.id)).not.toContain(agentKey);

      const relaunchResponsePromise = phonePage.waitForResponse(
        (response) =>
          response.request().method() === "POST" &&
          response.url().endsWith(`/api/v1/workspaces/${created.id}/runtime/sessions`),
      );
      await phonePage.getByRole("button", { name: agentLabel, exact: true }).tap();
      const relaunchResponse = await relaunchResponsePromise;
      expect(relaunchResponse.status(), await relaunchResponse.text()).toBe(200);
      await expect.poll(() => runtimeTargets(api!, created.id)).toContain(agentKey);

      if (!ready.worktree_path) throw new Error(`workspace ${created.id} has no worktree path`);
      writeFileSync(join(ready.worktree_path, "mobile-force-delete.txt"), "dirty workspace\n");
      const rejectedDelete = await api.delete(`/api/v1/workspaces/${created.id}`, { data: {} });
      expect(rejectedDelete.status()).toBe(409);
      expect(await rejectedDelete.json()).toMatchObject({ code: "worktreeDirty" });
      await expect(phonePage).toHaveURL(new RegExp(`/m/workspaces/local/${created.id}$`));
      expect(await runtimeTargets(api, created.id)).toContain(agentKey);
      await expect.poll(() => liveSessionWebSockets(phonePage, created.id)).toHaveLength(1);

      const forcedDelete = await api.delete(`/api/v1/workspaces/${created.id}?force=true`, { data: {} });
      expect(forcedDelete.status(), await forcedDelete.text()).toBe(204);
      await expect(phonePage).toHaveURL(/\/m\/workspaces$/);
      await expect.poll(() => liveSessionWebSockets(phonePage, created.id)).toHaveLength(0);
      await expect.poll(async () => (await api!.get(`/api/v1/workspaces/${created.id}`)).status()).toBe(404);
    } finally {
      await phoneContext?.close();
      await api?.dispose();
      await server?.stop();
    }
  });

  test("phone accepted launch expiry reports the missing session and permits relaunch", async ({ page }) => {
    test.skip(
      !hasCommand("git") || !hasCommand("tmux", ["-V"]) || !hasCommand("sh", ["-c", ":"]),
      "git, tmux, and sh are required for the real workspace runtime flow",
    );

    let server: IsolatedE2EServer | null = null;
    let api: APIRequestContext | null = null;
    let launchRequests = 0;
    try {
      server = await startIsolatedWorkspaceE2EServer();
      api = await playwrightRequest.newContext({
        baseURL: server.info.base_url,
      });
      await configureAgent(page, server.info.base_url);
      await page.route("**/api/v1/workspaces/*/runtime/sessions", async (route) => {
        if (route.request().method() !== "POST") {
          await route.continue();
          return;
        }
        launchRequests += 1;
        const pathname = new URL(route.request().url()).pathname;
        const workspaceID = decodeURIComponent(pathname.split("/").at(-3) ?? "");
        expect(workspaceID).not.toBe("");
        await route.fulfill({
          status: 200,
          contentType: "application/json",
          body: JSON.stringify({
            key: `${workspaceID}:missing-agent`,
            workspace_id: workspaceID,
            target_key: agentKey,
            label: agentLabel,
            kind: "agent",
            status: "starting",
            created_at: "2026-08-13T00:00:00Z",
            display_region: "workflow",
          }),
        });
      });

      const createResponsePromise = page.waitForResponse(
        (response) =>
          response.request().method() === "POST" &&
          /\/api\/v1\/repo\/github\/acme\/widgets\/workspaces$/.test(response.url()),
      );
      await page.setViewportSize({ width: 390, height: 844 });
      await page.goto(`${server.info.base_url}/m/workspaces`);
      await page.getByRole("button", { name: "New workspace" }).click();
      const dialog = page.getByRole("dialog", { name: "New workspace" });
      await dialog.getByRole("button", { name: "Filter repositories" }).click();
      await dialog.getByRole("option", { name: /acme\/widgets/ }).click();
      await dialog.getByRole("button", { name: "Create workspace options" }).click();
      await dialog.getByRole("menuitem", { name: agentLabel }).click();

      const createResponse = await createResponsePromise;
      expect(createResponse.status(), await createResponse.text()).toBe(202);
      const created = (await createResponse.json()) as WorkspaceResponse;
      await expect(page).toHaveURL(new RegExp(`/m/workspaces/local/${created.id}$`));
      await waitForWorkspaceReady(api, created.id);
      await expect.poll(() => launchRequests).toBe(1);
      await expect.poll(() => runtimeTargets(api!, created.id)).toEqual([]);

      const expiryMessage = `${agentLabel} launched, but its session did not become available`;
      await expect(page.getByText(expiryMessage)).toBeVisible({ timeout: 20_000 });
      const relaunch = page.getByRole("button", { name: agentLabel, exact: true });
      await expect(relaunch).toBeEnabled();
      await relaunch.click();
      await expect.poll(() => launchRequests).toBe(2);
    } finally {
      await page.unrouteAll({ behavior: "ignoreErrors" });
      await api?.dispose();
      await server?.stop();
    }
  });

  test("phone linked-item navigation preserves a zero-retention terminal session", async ({ page }) => {
    test.skip(
      !hasCommand("git") || !hasCommand("tmux", ["-V"]) || !hasCommand("sh", ["-c", ":"]),
      "git, tmux, and sh are required for the real workspace runtime flow",
    );

    let server: IsolatedE2EServer | null = null;
    let api: APIRequestContext | null = null;
    try {
      server = await startIsolatedWorkspaceE2EServer();
      api = await playwrightRequest.newContext({ baseURL: server.info.base_url });
      await configureAgent(page, server.info.base_url);
      const launchResponsePromise = page.waitForResponse(
        (response) =>
          response.request().method() === "POST" &&
          /\/api\/v1\/workspaces\/[^/]+\/runtime\/sessions$/.test(response.url()),
      );
      const created = await createPullRequestWorkspaceWithAgent(page, server.info.base_url);
      await waitForWorkspaceReady(api, created.id);
      expect((await launchResponsePromise).status()).toBe(200);
      await expect.poll(() => runtimeTargets(api!, created.id)).toContain(agentKey);
      await setRetainedSessions(api, 0);
      await trackSessionWebSockets(page);

      await page.goto(`${server.info.base_url}/m/workspaces/local/${created.id}`);
      await expect(page.getByRole("button", { name: "Terminal options" })).toBeVisible();
      await expect.poll(() => liveSessionWebSockets(page, created.id)).toHaveLength(1);

      await page.getByRole("button", { name: "Open linked PR #1" }).click();
      await expect(page).toHaveURL(new RegExp(`/m/workspaces/local/${created.id}/item$`));
      await expect(page.locator(".pull-detail .detail-title")).toBeVisible();
      await expect(page.getByRole("region", { name: "Workspace terminal" })).not.toBeVisible();
      await expect.poll(() => liveSessionWebSockets(page, created.id)).toHaveLength(1);

      await page.getByRole("button", { name: "Back to workspace terminal" }).click();
      await expect(page).toHaveURL(new RegExp(`/m/workspaces/local/${created.id}$`));
      await expect.poll(() => liveSessionWebSockets(page, created.id)).toHaveLength(1);

      await page.goForward();
      await expect(page).toHaveURL(new RegExp(`/m/workspaces/local/${created.id}/item$`));
      await page.goBack();
      await expect(page).toHaveURL(new RegExp(`/m/workspaces/local/${created.id}$`));
      await expect.poll(() => liveSessionWebSockets(page, created.id)).toHaveLength(1);
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
