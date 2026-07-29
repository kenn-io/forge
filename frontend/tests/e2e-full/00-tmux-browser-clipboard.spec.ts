import { execFileSync } from "node:child_process";
import {
  expect,
  request as playwrightRequest,
  test,
  type APIRequestContext,
  type Locator,
  type Page,
} from "@playwright/test";
import { startIsolatedWorkspaceE2EServer, type IsolatedE2EServer } from "./support/e2eServer";

type WorkspaceStatusResponse = {
  id: string;
  status: string;
  error_message?: string | null;
};

const TERMINAL_OUTPUT_TIMEOUT_MS = 15_000;

function hasCommand(command: string, args: string[] = ["--version"]): boolean {
  try {
    execFileSync(command, args, { stdio: "ignore" });
    return true;
  } catch {
    return false;
  }
}

async function waitForWorkspaceReady(api: APIRequestContext, workspaceId: string): Promise<WorkspaceStatusResponse> {
  for (let attempt = 0; attempt < 100; attempt += 1) {
    const response = await api.get(`/api/v1/workspaces/${workspaceId}`);
    expect(response.ok()).toBe(true);
    const workspace = (await response.json()) as WorkspaceStatusResponse;
    if (workspace.status === "ready") return workspace;
    if (workspace.status === "error") {
      throw new Error(workspace.error_message ?? `workspace ${workspaceId} failed to become ready`);
    }
    await new Promise((resolve) => setTimeout(resolve, 100));
  }
  throw new Error(`workspace ${workspaceId} did not become ready`);
}

async function createIssueWorkspace(api: APIRequestContext): Promise<WorkspaceStatusResponse> {
  const response = await api.post("/api/v1/issues/github/acme/widgets/10/workspace", { data: {} });
  expect(response.status()).toBe(202);
  const workspace = (await response.json()) as WorkspaceStatusResponse;
  return waitForWorkspaceReady(api, workspace.id);
}

async function openTerminalPanel(page: Page): Promise<Locator> {
  await page.getByRole("button", { name: "Open terminal panel" }).click();
  const container = page.locator(".terminal-panel.open .terminal-container");
  await expect(container).toBeVisible();
  await expect(container.locator("canvas, .xterm-screen").first()).toBeVisible();
  return container;
}

function observeTerminalOutput(page: Page): { includes(text: string): boolean } {
  let output = "";
  page.on("websocket", (socket) => {
    const decoder = new TextDecoder();
    socket.on("framereceived", ({ payload }) => {
      const chunk = typeof payload === "string" ? payload : decoder.decode(payload, { stream: true });
      output = (output + chunk).slice(-64 * 1024);
    });
  });
  return { includes: (text: string) => output.includes(text) };
}

function shellOctal(text: string): string {
  return Array.from(new TextEncoder().encode(text), (byte) => `\\${byte.toString(8).padStart(3, "0")}`).join("");
}

async function runAttachedTmuxCommand(page: Page, command: string): Promise<void> {
  await page.keyboard.press("Control+b");
  await page.keyboard.press(":");
  await page.keyboard.type(command);
  await page.keyboard.press("Enter");
}

async function copyMarkerWithTmux(
  page: Page,
  container: Locator,
  output: ReturnType<typeof observeTerminalOutput>,
  marker: string,
): Promise<void> {
  await container.click({ position: { x: 10, y: 10 } });
  await page.keyboard.type(`clear; printf '${shellOctal(`${marker}#`)}\\n'`);
  await page.keyboard.press("Enter");
  await expect.poll(() => output.includes(marker), { timeout: TERMINAL_OUTPUT_TIMEOUT_MS }).toBe(true);
  await runAttachedTmuxCommand(page, `set-buffer -w '${marker}'`);
}

test.describe.configure({ timeout: 120_000 });

test("real tmux OSC 52 copy reaches Chromium's clipboard", async ({ page, browserName }) => {
  test.skip(browserName !== "chromium", "Firefox does not expose clipboard permission grants to Playwright");
  test.skip(!hasCommand("git") || !hasCommand("tmux", ["-V"]), "git and tmux are required");
  await page.context().grantPermissions(["clipboard-read", "clipboard-write"]);

  let isolatedServer: IsolatedE2EServer | null = null;
  let api: APIRequestContext | null = null;
  try {
    isolatedServer = await startIsolatedWorkspaceE2EServer();
    api = await playwrightRequest.newContext({ baseURL: isolatedServer.info.base_url });
    const workspace = await createIssueWorkspace(api);
    const output = observeTerminalOutput(page);

    await page.goto(`${isolatedServer.info.base_url}/terminal/${workspace.id}`);
    const terminal = await openTerminalPanel(page);
    await page.evaluate(() => navigator.clipboard.writeText(""));
    await copyMarkerWithTmux(page, terminal, output, "real tmux clipboard marker");

    await expect.poll(() => output.includes("\x1b]52;"), { timeout: TERMINAL_OUTPUT_TIMEOUT_MS }).toBe(true);
    await expect.poll(() => page.evaluate(() => navigator.clipboard.readText())).toBe("real tmux clipboard marker");
  } finally {
    await api?.dispose();
    await isolatedServer?.stop();
  }
});
