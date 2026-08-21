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

function sixelGraphicsCommand(): string {
  return `printf '\\033Pq"1;1;4;6#0;2;100;0;0#0!2~$#1;2;0;100;0#1??!2~\\033\\\\'`;
}

function iipGraphicsCommand(): string {
  const png = "iVBORw0KGgoAAAANSUhEUgAAAAIAAAABCAYAAAD0In+KAAAADklEQVR4nGP4z8DwHwQBEPgD/U6VwW8AAAAASUVORK5CYII=";
  const iipSequence = `\\033\\033]1337;File=inline=1;size=71;width=2;height=1;preserveAspectRatio=0:${png}\\033\\033\\\\`;
  return `printf '${tmuxPassthroughFormat(iipSequence)}'`;
}

async function expectGraphicsImage(terminal: Locator, checkColors: boolean): Promise<void> {
  await expect
    .poll(() =>
      terminal.evaluate((element) => {
        const canvas = element.querySelector<HTMLCanvasElement>(".xterm-image-layer-top");
        if (!canvas) return false;
        const pixels = canvas
          .getContext("2d", { willReadFrequently: true })
          ?.getImageData(0, 0, canvas.width, canvas.height).data;
        if (!pixels) return false;
        for (let index = 3; index < pixels.length; index += 4) {
          if (pixels[index]! > 0) return true;
        }
        return false;
      }),
    )
    .toBe(true);
  if (!checkColors) return;
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
            red ||= redChannel > 180 && greenChannel < 80 && blueChannel < 80;
            green ||= greenChannel > 180 && redChannel < 80 && blueChannel < 80;
            if (red && green) return true;
          }
          return false;
        }),
      { timeout: 15_000 },
    )
    .toBe(true);
}

async function renderGraphicsThroughTmux(
  page: Page,
  command: string,
  options: { checkColors: boolean; passthrough: boolean },
): Promise<void> {
  test.skip(!hasCommand("git") || !hasCommand("tmux", ["-V"]), "git and tmux are required for the real workspace flow");

  let isolatedServer: IsolatedE2EServer | null = null;
  let api: APIRequestContext | null = null;
  try {
    isolatedServer = await startIsolatedWorkspaceE2EServer();
    api = await playwrightRequest.newContext({ baseURL: isolatedServer.info.base_url });
    const workspace = await createIssueWorkspace(api);

    if (!options.passthrough) {
      runE2ETmuxCommand(isolatedServer, ["set-option", "-q", "-s", "terminal-features[100]", "xterm-256color:sixel"]);
    }

    await page.goto(`${isolatedServer.info.base_url}/terminal/${workspace.id}`);
    const terminal = await openTerminalPanel(page);
    const tmuxSession = await runningRuntimeTmuxSession(api, workspace.id);
    if (options.passthrough) {
      expect(runE2ETmuxCommand(isolatedServer, ["show-options", "-pv", "-t", tmuxSession, "allow-passthrough"])).toBe(
        "on",
      );
    } else {
      const features = runE2ETmuxCommand(isolatedServer, [
        "display-message",
        "-p",
        "-t",
        tmuxSession,
        "#{client_termfeatures}",
      ]);
      expect(features).toContain("sixel");
      await expect
        .poll(() =>
          Number(
            runE2ETmuxCommand(isolatedServer!, ["display-message", "-p", "-t", tmuxSession, "#{client_cell_width}"]),
          ),
        )
        .toBeGreaterThan(0);
      expect(
        Number(
          runE2ETmuxCommand(isolatedServer, ["display-message", "-p", "-t", tmuxSession, "#{client_cell_height}"]),
        ),
      ).toBeGreaterThan(0);
    }
    runE2ETmuxCommand(isolatedServer, ["send-keys", "-t", tmuxSession, "-l", command]);
    runE2ETmuxCommand(isolatedServer, ["send-keys", "-t", tmuxSession, "Enter"]);

    await expectGraphicsImage(terminal, options.checkColors);
  } finally {
    await api?.dispose();
    await isolatedServer?.stop();
  }
}

test("native SIXEL graphics render through tmux", async ({ page }) => {
  await renderGraphicsThroughTmux(page, sixelGraphicsCommand(), { checkColors: false, passthrough: false });
});

test("OSC 1337 inline images render through tmux passthrough", async ({ page }) => {
  await renderGraphicsThroughTmux(page, iipGraphicsCommand(), { checkColors: true, passthrough: true });
});
