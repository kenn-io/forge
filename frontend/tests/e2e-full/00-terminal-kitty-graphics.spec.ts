import { execFileSync } from "node:child_process";
import { readFileSync } from "node:fs";
import {
  expect,
  request as playwrightRequest,
  test,
  type APIRequestContext,
  type Locator,
  type Page,
} from "@playwright/test";
import { load as loadToml } from "js-toml";
import { startIsolatedWorkspaceE2EServer, type IsolatedE2EServer } from "./support/e2eServer";

type WorkspaceStatusResponse = {
  id: string;
  status: string;
  error_message?: string | null;
};

type RuntimeSessionResponse = {
  sessions: Array<{
    key: string;
    status: string;
  }> | null;
};

type RuntimeAttachSpecResponse = {
  tmux_session: string;
};

type E2ETmuxConfig = {
  tmux?: {
    command?: string[];
  };
};

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

async function runningRuntimeTmuxSession(api: APIRequestContext, workspaceId: string): Promise<string> {
  const response = await api.get(`/api/v1/workspaces/${workspaceId}/runtime`);
  expect(response.ok()).toBe(true);
  const runtime = (await response.json()) as RuntimeSessionResponse;
  const running = runtime.sessions?.filter((session) => session.status === "running") ?? [];
  expect(running).toHaveLength(1);

  const attachResponse = await api.get(
    `/api/v1/workspaces/${workspaceId}/runtime/sessions/${encodeURIComponent(running[0]!.key)}/attach-spec`,
  );
  expect(attachResponse.ok()).toBe(true);
  return ((await attachResponse.json()) as RuntimeAttachSpecResponse).tmux_session;
}

function runE2ETmuxCommand(server: IsolatedE2EServer, args: string[]): string {
  const config = loadToml(readFileSync(server.info.config_path, "utf8")) as E2ETmuxConfig;
  const [command, ...prefix] = config.tmux?.command ?? [];
  if (!command) throw new Error("e2e tmux command is unavailable");
  return execFileSync(command, [...prefix, ...args], { encoding: "utf8" }).trim();
}

function tmuxPassthroughFormat(payloadFormat: string): string {
  return `\\033Ptmux;${payloadFormat}\\033\\\\`;
}

function kittyGraphicsCommand(): string {
  const pixels = Buffer.from([255, 0, 0, 0, 255, 0]).toString("base64");
  const kittySequence = `\\033\\033_Ga=T,f=24,s=2,v=1;${pixels}\\033\\033\\\\`;
  return `printf '${tmuxPassthroughFormat(kittySequence)}'`;
}

test("Direct Kitty graphics render through the real tmux terminal path", async ({ page }) => {
  test.skip(!hasCommand("git") || !hasCommand("tmux", ["-V"]), "git and tmux are required for the real workspace flow");

  let isolatedServer: IsolatedE2EServer | null = null;
  let api: APIRequestContext | null = null;
  try {
    isolatedServer = await startIsolatedWorkspaceE2EServer();
    api = await playwrightRequest.newContext({ baseURL: isolatedServer.info.base_url });
    const workspace = await createIssueWorkspace(api);

    await page.goto(`${isolatedServer.info.base_url}/terminal/${workspace.id}`);
    const terminal = await openTerminalPanel(page);
    const tmuxSession = await runningRuntimeTmuxSession(api, workspace.id);
    expect(runE2ETmuxCommand(isolatedServer, ["show-options", "-pv", "-t", tmuxSession, "allow-passthrough"])).toBe(
      "on",
    );
    runE2ETmuxCommand(isolatedServer, ["send-keys", "-t", tmuxSession, "-l", kittyGraphicsCommand()]);
    runE2ETmuxCommand(isolatedServer, ["send-keys", "-t", tmuxSession, "Enter"]);

    await expect
      .poll(
        () =>
          terminal.evaluate((element) => {
            const canvas = element.querySelector<HTMLCanvasElement>(".xterm-image-layer-top");
            if (!canvas) return false;
            const context = canvas.getContext("2d", { willReadFrequently: true });
            if (!context) return false;
            const pixels = context.getImageData(0, 0, canvas.width, canvas.height).data;
            let red = false;
            let green = false;
            for (let index = 0; index < pixels.length; index += 4) {
              const alpha = pixels[index + 3]!;
              if (alpha === 0) continue;
              const redChannel = pixels[index]!;
              const greenChannel = pixels[index + 1]!;
              const blueChannel = pixels[index + 2]!;
              red ||= redChannel > 200 && greenChannel < 32 && blueChannel < 32;
              green ||= greenChannel > 200 && redChannel < 32 && blueChannel < 32;
              if (red && green) return true;
            }
            return false;
          }),
        { timeout: 15_000 },
      )
      .toBe(true);
  } finally {
    await api?.dispose();
    await isolatedServer?.stop();
  }
});
