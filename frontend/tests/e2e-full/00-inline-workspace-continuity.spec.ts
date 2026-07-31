// The 00- filename prefix schedules this long-running spec first: Playwright
// dispatches files in path order, and multi-second tests that start near the
// end of the run stretch the suite tail.
//
// These specs prove the inline workspace pane's core claim: the single
// hosted WorkspaceHost/WorkspaceTerminalView instance reparents between the
// Workspaces tab slot and a per-surface WorkspaceDockPanel slot without
// tearing down the terminal underneath it. A `data-continuity` tag applied
// via evaluate is the proof a reparent preserves the exact DOM node — a
// destroy+recreate (the reconnecting switch a differing remembered key
// takes) cannot carry the tag forward. After every reparent a command is
// typed into the terminal that creates a marker file in the workspace's
// worktree, and the test asserts the file appears on disk: keystrokes must
// travel the WebSocket to the real tmux shell and execute. This is durable
// evidence the session is live — unlike a screenshot-hash diff, which
// cursor blinking or the tmux status clock can change even when terminal
// input is broken (and canvas readback is unavailable anyway: xterm.js's
// WebGL renderer owns the canvas GL context).

import { closeSync, constants, existsSync, openSync, readFileSync, writeFileSync, writeSync } from "node:fs";
import path from "node:path";
import { execFileSync } from "node:child_process";
import { load as loadToml } from "js-toml";
import {
  expect,
  request as playwrightRequest,
  test,
  type APIRequestContext,
  type Locator,
  type Page,
} from "@playwright/test";
import { startIsolatedWorkspaceE2EServer, type IsolatedE2EServer } from "./support/e2eServer";
import { runPaletteCommand } from "./support/paletteCommands";

type WorkspaceStatusResponse = {
  id: string;
  status: string;
  worktree_path: string;
  error_message?: string | null;
};

type E2ETmuxConfig = {
  tmux?: {
    command?: string[];
  };
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

type TerminalControlFrame = {
  active: boolean | undefined;
  cols: number | undefined;
  rows: number | undefined;
  type: "refresh" | "resize" | "resize_active";
};

type TerminalOutputObserver = {
  cursorReports: () => string[];
  inputIncludes: (text: string) => boolean;
  inputOccurrences: (text: string) => number;
  reconnectedSessionModes: (workspaceId: string) => string[];
  sessionOutputIncludes: (workspaceId: string, text: string) => boolean;
};

const lockedWorkspaceTestTimeoutMs = 120_000;
const ZOOMED_TERMINAL_FONT_SIZE = 16;
const SAFARI_ISSUE_TITLE = "Widget rendering broken on Safari";
const DARK_MODE_ISSUE_TITLE = "Add dark mode support";
const ESCAPE = "\u001b";
const cursorReportPattern = new RegExp(`${ESCAPE}\\[\\??[0-9]+;[0-9]+R`, "g");
const privateModePattern = new RegExp(`${ESCAPE}\\[\\?[0-9;]*[hl]`, "g");

function hasCommand(command: string, args: string[] = ["--version"]): boolean {
  try {
    execFileSync(command, args, { stdio: "ignore" });
    return true;
  } catch {
    return false;
  }
}

function observeTerminalControlFrames(page: Page): TerminalControlFrame[] {
  const frames: TerminalControlFrame[] = [];
  page.on("websocket", (socket) => {
    socket.on("framesent", ({ payload }) => {
      if (typeof payload !== "string") return;
      try {
        const message = JSON.parse(payload) as {
          type?: string;
          active?: boolean;
          cols?: number;
          rows?: number;
        };
        if (message.type !== "refresh" && message.type !== "resize" && message.type !== "resize_active") return;
        frames.push({
          active: message.active,
          cols: message.cols,
          rows: message.rows,
          type: message.type,
        });
      } catch {
        // Terminal input uses binary frames; ignore non-control payloads.
      }
    });
  });
  return frames;
}

function observeTerminalOutput(page: Page): TerminalOutputObserver {
  const streams: Array<{ url: string; output: string; input: string }> = [];
  const isWorkspaceSessionStream = (url: string, workspaceId: string) => {
    const pathname = new URL(url).pathname;
    return (
      pathname.includes(`/workspaces/${encodeURIComponent(workspaceId)}/runtime/sessions/`) &&
      pathname.endsWith("/terminal")
    );
  };
  page.on("websocket", (socket) => {
    const stream = { url: socket.url(), output: "", input: "" };
    streams.push(stream);
    const decoder = new TextDecoder();
    socket.on("framereceived", ({ payload }) => {
      const chunk = typeof payload === "string" ? payload : decoder.decode(payload, { stream: true });
      stream.output = (stream.output + chunk).slice(-128 * 1024);
    });
    socket.on("framesent", ({ payload }) => {
      if (typeof payload === "string") {
        try {
          const control = JSON.parse(payload) as { type?: string };
          if (control.type) return;
        } catch {
          // Raw string terminal input is not a JSON control.
        }
      }
      const chunk = typeof payload === "string" ? payload : new TextDecoder().decode(payload);
      stream.input = (stream.input + chunk).slice(-128 * 1024);
    });
  });
  return {
    cursorReports: () =>
      streams.flatMap((stream) => Array.from(stream.input.matchAll(cursorReportPattern), ([report]) => report)),
    inputIncludes: (text: string) => streams.some((stream) => stream.input.includes(text)),
    inputOccurrences: (text: string) =>
      streams.reduce((total, stream) => total + stream.input.split(text).length - 1, 0),
    reconnectedSessionModes: (workspaceId: string) =>
      streams
        .filter((stream) => isWorkspaceSessionStream(stream.url, workspaceId))
        .slice(1)
        .flatMap((stream) => Array.from(stream.output.matchAll(privateModePattern), ([mode]) => mode)),
    sessionOutputIncludes: (workspaceId: string, text: string) =>
      streams.some((stream) => isWorkspaceSessionStream(stream.url, workspaceId) && stream.output.includes(text)),
  };
}

async function readWebSocketUntil(url: string, expectedText: string): Promise<string> {
  return new Promise((resolve, reject) => {
    const socket = new WebSocket(url);
    socket.binaryType = "arraybuffer";
    let output = "";
    const timeout = setTimeout(() => {
      socket.close();
      reject(new Error(`timed out waiting for replay from ${new URL(url).pathname}`));
    }, 15_000);
    socket.addEventListener("message", async (event) => {
      let chunk: string;
      if (typeof event.data === "string") {
        chunk = event.data;
      } else if (event.data instanceof ArrayBuffer) {
        chunk = new TextDecoder().decode(event.data);
      } else {
        chunk = new TextDecoder().decode(await event.data.arrayBuffer());
      }
      output += chunk;
      if (!output.includes(expectedText)) return;
      clearTimeout(timeout);
      socket.close();
      resolve(output);
    });
    socket.addEventListener("error", () => {
      clearTimeout(timeout);
      reject(new Error(`failed to read replay from ${new URL(url).pathname}`));
    });
  });
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

async function createIssueWorkspace(api: APIRequestContext, issueNumber: number): Promise<WorkspaceStatusResponse> {
  const response = await api.post(`/api/v1/issues/github/acme/widgets/${issueNumber}/workspace`, {
    data: {},
  });
  expect(response.status()).toBe(202);
  const workspace = (await response.json()) as WorkspaceStatusResponse;
  // Return the ready-state GET body: the 202 payload predates worktree
  // provisioning, so only the polled response carries worktree_path.
  return waitForWorkspaceReady(api, workspace.id);
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

/**
 * A workspace running nothing opens its launcher overlay as soon as it lands in a
 * detail pane, and nothing behind a modal is clickable. Dismiss it the way a user
 * who wants the dock instead would.
 */
async function dismissWorkspaceLauncher(page: Page): Promise<void> {
  const launcher = page.getByRole("dialog", { name: "Launch a session" });
  await expect(launcher).toBeVisible();
  await page.keyboard.press("Escape");
  await expect(launcher).toBeHidden();
}

/**
 * Open the dock and return its terminal.
 *
 * In a detail pane a workspace running exactly one session drops its chrome, dock
 * included, and renders that session on its own - so the container to wait for
 * depends on how many sessions the workspace has, not on which control opened it.
 */
async function openTerminalPanel(page: Page): Promise<Locator> {
  await page.getByRole("button", { name: "Open terminal panel" }).click();
  const container = page
    .locator(".terminal-panel.open .terminal-container, .sole-embedded-session .terminal-container")
    .first();
  await expect(container).toBeVisible();
  // xterm.js only paints a canvas when its WebGL addon activates. Without WebGL
  // (headless Firefox) it silently falls back to the DOM renderer, which
  // renders .xterm-screen rows and never creates a canvas.
  await expect(container.locator("canvas, .xterm-screen").first()).toBeVisible();
  return container;
}

async function ensureTerminalPanelOpen(page: Page): Promise<Locator> {
  const container = page
    .locator(".terminal-panel.open .terminal-container, .sole-embedded-session .terminal-container")
    .first();
  const openButton = page.getByRole("button", { name: "Open terminal panel" });
  const closeButton = page.getByRole("button", { name: "Close terminal panel" }).first();
  await expect.poll(async () => (await openButton.isVisible()) || (await closeButton.isVisible())).toBe(true);
  if (await openButton.isVisible()) {
    await openButton.click();
  }
  await expect(container).toBeVisible();
  await expect(container.locator("canvas, .xterm-screen").first()).toBeVisible();
  return container;
}

/**
 * Put the whole workspace away.
 *
 * A detail pane hides the workspace's own header bar, so collapse now lives in the
 * pane's controls popover. The pane's close button is not a substitute: it hides one
 * pane, while collapse reaches the container and every promoted session with it.
 */
async function collapseTerminal(page: Page): Promise<void> {
  await page.getByRole("button", { name: "Workspace controls" }).first().click();
  await page.getByRole("button", { name: "Collapse Terminal" }).click();
}

async function typeIntoTerminal(page: Page, container: Locator, command: string): Promise<void> {
  await container.click({ position: { x: 10, y: 10 } });
  await page.keyboard.type(command);
  await page.keyboard.press("Enter");
}

async function runAttachedTmuxCommand(page: Page, command: string): Promise<void> {
  await page.keyboard.press("Control+b");
  await page.keyboard.press(":");
  await page.keyboard.type(command);
  await page.keyboard.press("Enter");
}

function runE2ETmuxCommand(server: IsolatedE2EServer, args: string[]): string {
  const config = loadToml(readFileSync(server.info.config_path, "utf8")) as E2ETmuxConfig;
  const [command, ...prefix] = config.tmux?.command ?? [];
  if (!command) throw new Error("e2e tmux command is unavailable");
  return execFileSync(command, [...prefix, ...args], { encoding: "utf8" }).trim();
}

function writeTmuxClientTTY(server: IsolatedE2EServer, tmuxSession: string, data: Uint8Array): void {
  const clientTTY = runE2ETmuxCommand(server, ["list-clients", "-t", tmuxSession, "-F", "#{client_tty}"]);
  const fd = openSync(clientTTY, constants.O_WRONLY | constants.O_NOCTTY);
  try {
    writeSync(fd, data);
  } finally {
    closeSync(fd);
  }
}

function tmuxPassthroughFormat(payloadFormat: string): string {
  return `\\033Ptmux;${payloadFormat}\\033\\\\`;
}

async function expectWheelScroll(
  page: Page,
  container: Locator,
  server: IsolatedE2EServer,
  tmuxSession: string,
): Promise<void> {
  await expect(container.locator(".xterm.enable-mouse-events")).toBeVisible();
  const screen = container.locator(".xterm-screen");
  const box = await screen.boundingBox();
  expect(box).not.toBeNull();
  await page.mouse.move(box!.x + box!.width / 2, box!.y + box!.height / 2);
  await page.mouse.wheel(0, -120);
  await expect
    .poll(
      () =>
        runE2ETmuxCommand(server, [
          "display-message",
          "-p",
          "-t",
          tmuxSession,
          "#{pane_in_mode}:#{scroll_position}:#{alternate_on}:#{mouse_any_flag}",
        ]),
      { timeout: 15_000 },
    )
    .toMatch(/^1:0:0:0$/);
  await page.mouse.wheel(0, -120);
  await expect
    .poll(
      () =>
        runE2ETmuxCommand(server, [
          "display-message",
          "-p",
          "-t",
          tmuxSession,
          "#{pane_in_mode}:#{scroll_position}:#{alternate_on}:#{mouse_any_flag}",
        ]),
      { timeout: 15_000 },
    )
    .toMatch(/^1:[1-9]\d*:0:0$/);
}

function exitTmuxCopyMode(server: IsolatedE2EServer, tmuxSession: string): void {
  runE2ETmuxCommand(server, ["send-keys", "-t", tmuxSession, "-X", "cancel"]);
}

async function dispatchTerminalPaste(container: Locator, text: string): Promise<void> {
  await container.locator(".xterm-helper-textarea").evaluate((textarea, pastedText) => {
    const paste = new Event("paste", { bubbles: true, cancelable: true });
    Object.defineProperty(paste, "clipboardData", {
      value: {
        getData: (format: string) => (format === "text/plain" ? pastedText : ""),
      },
    });
    textarea.dispatchEvent(paste);
  }, text);
}

// Types a command that creates a marker file in the workspace worktree and
// waits for the file to exist on disk. Durable proof the terminal's input
// path (DOM focus -> WebSocket -> tmux -> shell) is live at this moment;
// a rendering-level signal cannot fake it and a broken input path cannot
// pass it.
async function typeMarkerCommand(page: Page, container: Locator, worktreePath: string, marker: string): Promise<void> {
  const markerPath = path.join(worktreePath, marker);
  await typeIntoTerminal(page, container, `touch '${markerPath}'`);
  await expect.poll(() => existsSync(markerPath), { timeout: 15_000 }).toBe(true);
}

async function readPtyGeometry(
  page: Page,
  container: Locator,
  worktreePath: string,
  name: string,
): Promise<{ rows: number; cols: number }> {
  const outputPath = path.join(worktreePath, name);
  await typeIntoTerminal(page, container, `stty size > '${outputPath}'`);
  await expect
    .poll(
      () => {
        if (!existsSync(outputPath)) return "";
        return readFileSync(outputPath, "utf8").trim();
      },
      { timeout: 15_000 },
    )
    .toMatch(/^[1-9]\d*\s+[1-9]\d*$/);
  const [rows, cols] = readFileSync(outputPath, "utf8").trim().split(/\s+/).map(Number);
  expect(rows).toBeGreaterThan(0);
  expect(cols).toBeGreaterThan(0);
  return { rows: rows!, cols: cols! };
}

async function waitForPtyColumnsBelow(
  page: Page,
  container: Locator,
  worktreePath: string,
  name: string,
  maximumCols: number,
): Promise<{ rows: number; cols: number }> {
  for (let attempt = 0; attempt < 20; attempt += 1) {
    const geometry = await readPtyGeometry(page, container, worktreePath, `${name}-${attempt}`);
    if (geometry.cols < maximumCols) return geometry;
    await page.waitForTimeout(100);
  }
  throw new Error(`tmux PTY columns did not fall below ${maximumCols}`);
}

async function selectTopBarTab(page: Page, label: string): Promise<void> {
  await page.locator(".kit-top-bar__tabs .kit-top-bar__tab", { hasText: label }).click();
}

async function selectIssueByTitle(page: Page, title: string): Promise<void> {
  await selectTopBarTab(page, "Issues");
  await page.locator(".issue-item").filter({ hasText: title }).first().click();
}

async function expectPersistedTerminalFontSize(api: APIRequestContext, fontSize: number): Promise<void> {
  await expect
    .poll(async () => {
      const response = await api.get("/api/v1/settings");
      const settings = (await response.json()) as {
        terminal: { font_size: number };
      };
      return settings.terminal.font_size;
    })
    .toBe(fontSize);
}

test.describe("inline workspace pane continuity", () => {
  test.describe.configure({ mode: "serial", timeout: lockedWorkspaceTestTimeoutMs });

  // These tests share one page (serial mode) and the pane layout persists, so a
  // test that closes or maximizes the workspace pane would hand that arrangement
  // to the next one. Reset it on every document load.
  test.beforeEach(async ({ page }) => {
    await page.addInitScript(() => {
      for (const surface of ["prs", "issues", "activity"]) {
        localStorage.removeItem(`kenn-forge-pane-layout-v1:${surface}`);
      }
    });
  });

  test("a dock resize grows both split terminals and their PTY rows", async ({ page }) => {
    test.skip(
      !hasCommand("git") || !hasCommand("tmux", ["-V"]),
      "git and tmux are required for the real workspace flow",
    );
    await page.setViewportSize({ width: 1440, height: 900 });

    let isolatedServer: IsolatedE2EServer | null = null;
    let api: APIRequestContext | null = null;
    try {
      isolatedServer = await startIsolatedWorkspaceE2EServer();
      api = await playwrightRequest.newContext({ baseURL: isolatedServer.info.base_url });
      const workspace = await createIssueWorkspace(api, 10);
      for (let index = 0; index < 2; index += 1) {
        const launch = await api.post(`/api/v1/workspaces/${workspace.id}/runtime/sessions`, {
          data: { target_key: "plain_shell", display_region: "terminal" },
        });
        expect(launch.status(), await launch.text()).toBe(200);
      }

      await page.goto(`${isolatedServer.info.base_url}/issues/github/acme/widgets/10`);
      await openTerminalPanel(page);
      const panel = page.locator(".terminal-panel.open");
      const handle = panel.getByRole("separator", { name: "Resize terminal panel" });
      await panel.getByRole("button", { name: "Split terminal right" }).click();
      const terminals = panel.locator(".terminal-container");
      await expect(terminals).toHaveCount(2);

      const beforeHeights = await terminals.evaluateAll((elements) =>
        elements.map((element) => element.getBoundingClientRect().height),
      );
      const beforePtys: Array<{ rows: number; cols: number }> = [];
      for (const index of [0, 1]) {
        beforePtys.push(
          await readPtyGeometry(page, terminals.nth(index), workspace.worktree_path, `dock-resize-before-${index}`),
        );
      }

      await handle.press("ArrowUp");
      await handle.press("ArrowUp");

      for (const index of [0, 1]) {
        await expect
          .poll(() => terminals.nth(index).evaluate((element) => element.getBoundingClientRect().height))
          .toBeGreaterThan(beforeHeights[index]! + 20);
        const afterGeometry = await terminals.nth(index).evaluate((element) => {
          const leafBody = element.closest(".terminal-leaf-body");
          if (!(leafBody instanceof HTMLElement)) return null;
          return {
            leafHeight: leafBody.getBoundingClientRect().height,
            terminalHeight: element.getBoundingClientRect().height,
          };
        });
        expect(afterGeometry).not.toBeNull();
        expect(Math.abs(afterGeometry!.leafHeight - afterGeometry!.terminalHeight)).toBeLessThan(2);

        const afterPty = await readPtyGeometry(
          page,
          terminals.nth(index),
          workspace.worktree_path,
          `dock-resize-after-${index}`,
        );
        expect(afterPty.rows).toBeGreaterThan(beforePtys[index]!.rows);
      }
    } finally {
      await api?.dispose();
      await isolatedServer?.stop();
    }
  });

  test("a hidden workflow terminal revokes authority and sends no geometry frames", async ({ page }) => {
    test.skip(
      !hasCommand("git") || !hasCommand("tmux", ["-V"]),
      "git and tmux are required for the real workspace flow",
    );
    await page.setViewportSize({ width: 1440, height: 900 });

    let isolatedServer: IsolatedE2EServer | null = null;
    let api: APIRequestContext | null = null;
    try {
      const controlFrames = observeTerminalControlFrames(page);
      isolatedServer = await startIsolatedWorkspaceE2EServer();
      api = await playwrightRequest.newContext({ baseURL: isolatedServer.info.base_url });
      const workspace = await createIssueWorkspace(api, 10);

      await page.goto(`${isolatedServer.info.base_url}/terminal/${workspace.id}`);
      const terminalPanel = page.getByRole("region", { name: "Terminal panel" });
      await terminalPanel.getByRole("button", { name: "New terminal" }).click();
      const moveToWorkflow = terminalPanel.getByRole("button", { name: "Move terminal panel to workflow" });
      await expect(moveToWorkflow).toBeVisible();
      await moveToWorkflow.click();

      const workflow = page.getByRole("region", { name: "Workflow panes" });
      const homeTab = workflow.getByRole("tab", { name: "Home" });
      const terminalTab = workflow.getByRole("tab", { name: "Terminal" });
      await expect(terminalTab).toHaveAttribute("aria-selected", "true");
      const revocationsBeforeHide = controlFrames.filter(
        (frame) => frame.type === "resize_active" && frame.active === false,
      ).length;

      await homeTab.click();
      await expect(homeTab).toHaveAttribute("aria-selected", "true");
      await expect
        .poll(() => controlFrames.filter((frame) => frame.type === "resize_active" && frame.active === false).length)
        .toBe(revocationsBeforeHide + 1);
      const geometryFramesAfterHide = controlFrames.filter(
        (frame) => frame.type === "resize" || frame.type === "refresh",
      ).length;
      const widthBeforeResize = await workflow.evaluate((element) => element.getBoundingClientRect().width);

      await page.setViewportSize({ width: 1200, height: 760 });
      await expect
        .poll(() => workflow.evaluate((element) => element.getBoundingClientRect().width))
        .toBeLessThan(widthBeforeResize - 100);
      await page.evaluate(
        () =>
          new Promise<void>((resolve) => {
            requestAnimationFrame(() => requestAnimationFrame(() => resolve()));
          }),
      );

      expect(controlFrames.filter((frame) => frame.type === "resize" || frame.type === "refresh")).toHaveLength(
        geometryFramesAfterHide,
      );
    } finally {
      await api?.dispose();
      await isolatedServer?.stop();
    }
  });

  test("maximizing an inline workspace keeps the app frame fixed and its controls reachable", async ({ page }) => {
    test.skip(
      !hasCommand("git") || !hasCommand("tmux", ["-V"]),
      "git and tmux are required for the real workspace flow",
    );

    let isolatedServer: IsolatedE2EServer | null = null;
    let api: APIRequestContext | null = null;
    try {
      isolatedServer = await startIsolatedWorkspaceE2EServer();
      api = await playwrightRequest.newContext({ baseURL: isolatedServer.info.base_url });
      await createIssueWorkspace(api, 10);

      await page.goto(`${isolatedServer.info.base_url}/issues/github/acme/widgets/10`);
      await dismissWorkspaceLauncher(page);
      const appMain = page.locator(".app-main");
      const itemRail = page.locator(".issue-list");
      // The pane's own controls, in its tab strip: a detail pane never shows the
      // workspace's header bar, so maximize and close are what expand and collapse
      // the inline dock now.
      const workspaceLeaf = page.locator(".tabbed-panel-leaf").filter({
        has: page.locator(".detail-pane-workspace-slot"),
      });
      const zoom = workspaceLeaf.locator('[data-testid="pane-toggle-zoom"]');
      const close = workspaceLeaf.locator('[data-testid="pane-hide-workspace"]');
      await expect(zoom).toBeVisible();
      const overflowMetrics = await appMain.evaluate((element) => {
        const overflowProbe = document.createElement("div");
        overflowProbe.dataset.testOverflowProbe = "true";
        overflowProbe.style.cssText = "flex: 0 0 2000px; width: 1px;";
        element.appendChild(overflowProbe);
        element.scrollTop = 40;
        return { clientHeight: element.clientHeight, scrollHeight: element.scrollHeight };
      });
      expect(overflowMetrics.scrollHeight).toBeGreaterThan(overflowMetrics.clientHeight);
      await expect(appMain).toHaveJSProperty("scrollTop", 0);
      await appMain.locator("[data-test-overflow-probe]").evaluate((element) => element.remove());

      await zoom.click();

      // Maximized: the control flips to Restore, and the pane's close is still there
      // to put the workspace away entirely.
      const restore = workspaceLeaf.getByRole("button", { name: "Restore pane size" });
      await expect(restore).toBeVisible();
      await expect(close).toBeVisible();
      await restore.click();
      await expect(workspaceLeaf.getByRole("button", { name: "Maximize pane" })).toBeVisible();
      await zoom.click();
      await expect(restore).toBeVisible();
      await expect(appMain).toHaveJSProperty("scrollTop", 0);
      await expect
        .poll(async () => {
          const [mainBox, railBox, panelBox] = await Promise.all([
            appMain.boundingBox(),
            itemRail.boundingBox(),
            page.locator(".tabbed-panel-split-child.zoomed").boundingBox(),
          ]);
          return {
            railTopDelta: railBox && mainBox ? railBox.y - mainBox.y : undefined,
            railBottomDelta: railBox && mainBox ? railBox.y + railBox.height - (mainBox.y + mainBox.height) : undefined,
            panelTopDelta: panelBox && mainBox ? panelBox.y - mainBox.y : undefined,
            panelBottomDelta:
              panelBox && mainBox ? panelBox.y + panelBox.height - (mainBox.y + mainBox.height) : undefined,
          };
        })
        .toEqual({
          railTopDelta: 0,
          railBottomDelta: 0,
          panelTopDelta: 0,
          panelBottomDelta: 0,
        });

      await close.click();
      await expect(page.locator(".detail-pane-workspace-slot")).toHaveCount(0);
    } finally {
      await api?.dispose();
      await isolatedServer?.stop();
    }
  });

  test("the popover's dock modes expand and restore, and its Delete destroys the workspace", async ({ page }) => {
    // Both halves of this only exist against a real surface. Expand Terminal drives
    // a zoom the pane layout can silently undo (route authority once re-asserted on
    // every tab-list re-derive, which the zoom itself triggers), and the strip
    // Delete is the destructive path a jsdom placement assertion cannot prove
    // reaches the API.
    test.skip(
      !hasCommand("git") || !hasCommand("tmux", ["-V"]),
      "git and tmux are required for the real workspace flow",
    );

    let isolatedServer: IsolatedE2EServer | null = null;
    let api: APIRequestContext | null = null;
    try {
      isolatedServer = await startIsolatedWorkspaceE2EServer();
      api = await playwrightRequest.newContext({ baseURL: isolatedServer.info.base_url });
      const created = await createIssueWorkspace(api, 10);

      await page.goto(`${isolatedServer.info.base_url}/issues/github/acme/widgets/10`);
      await dismissWorkspaceLauncher(page);

      const detail = page.locator(".detail-pane-conversation, [data-testid='pane-conversation']").first();
      const controls = page.getByRole("button", { name: "Workspace controls" }).first();
      await expect(controls).toBeVisible();

      await controls.click();
      const popover = page.getByRole("dialog", { name: "Workspace controls" });
      await popover.getByRole("button", { name: "Expand Terminal" }).click();

      // Expanded: the workspace covers the surface, so the detail pane it shared it
      // with is gone rather than merely narrower.
      await expect(page.locator(".tabbed-panel-split-child.zoomed")).toHaveCount(1);
      await expect(detail).toHaveCount(0);

      // The popover stays open across its own actions on purpose, and the same
      // button flips to the inverse mode.
      await popover.getByRole("button", { name: "Show Details" }).click();
      await expect(page.locator(".tabbed-panel-split-child.zoomed")).toHaveCount(0);
      await controls.click();
      await expect(popover).toBeHidden();

      // The strip Delete: one click, then the confirmation every entry point shows.
      await page.getByRole("button", { name: /^Delete workspace / }).click();
      await page
        .getByRole("dialog", { name: "Delete workspace?" })
        .getByRole("button", { name: "Delete workspace" })
        .click();

      await expect(page.locator(".detail-pane-workspace-slot")).toHaveCount(0);
      await expect.poll(async () => (await api!.get(`/api/v1/workspaces/${created.id}`)).status()).toBe(404);
    } finally {
      await api?.dispose();
      await isolatedServer?.stop();
    }
  });

  test("deleting a running inline workspace never reveals the launcher during teardown", async ({ page }) => {
    test.skip(
      !hasCommand("git") || !hasCommand("tmux", ["-V"]),
      "git and tmux are required for the real workspace flow",
    );

    let isolatedServer: IsolatedE2EServer | null = null;
    let api: APIRequestContext | null = null;
    try {
      isolatedServer = await startIsolatedWorkspaceE2EServer();
      api = await playwrightRequest.newContext({ baseURL: isolatedServer.info.base_url });
      const workspace = await createIssueWorkspace(api, 10);
      const launch = await api.post(`/api/v1/workspaces/${workspace.id}/runtime/sessions`, {
        data: { target_key: "plain_shell", display_region: "workflow" },
      });
      expect(launch.status(), await launch.text()).toBe(200);

      await page.goto(`${isolatedServer.info.base_url}/issues/github/acme/widgets/10`);
      await expect(page.locator(".detail-pane-workspace-slot .terminal-container")).toBeVisible();
      const launcher = page.getByRole("dialog", { name: "Launch a session" });
      await expect(launcher).toHaveCount(0);

      let resolveDeleteProxy!: () => void;
      const deleteProxyCompleted = new Promise<void>((resolve) => {
        resolveDeleteProxy = resolve;
      });
      // Let the real DELETE stop tmux and remove the workspace, but hold its
      // response briefly so runtime teardown can render while the inline host is
      // still present. A mocked runtime response would miss the server ordering
      // this regression is meant to protect.
      await page.route(`**/api/v1/workspaces/${workspace.id}`, async (route) => {
        if (route.request().method() !== "DELETE") {
          await route.continue();
          return;
        }
        try {
          const response = await route.fetch();
          await new Promise((resolve) => setTimeout(resolve, 1_000));
          await route.fulfill({ response });
        } finally {
          resolveDeleteProxy();
        }
      });

      // An end-state absence assertion can miss a dialog that mounts for one
      // render and disappears with the host. Record every added dialog node so
      // even that transient launch surface fails the test.
      await page.evaluate(() => {
        const selector = '[role="dialog"][aria-label="Launch a session"]';
        const state = { appeared: document.querySelector(selector) !== null };
        Reflect.set(window, "__kenn_forgeDeleteLauncherProbe", state);
        new MutationObserver((records) => {
          for (const record of records) {
            for (const node of record.addedNodes) {
              if (node instanceof Element && (node.matches(selector) || node.querySelector(selector) !== null)) {
                state.appeared = true;
              }
            }
          }
        }).observe(document.body, { childList: true, subtree: true });
      });

      await page.getByRole("button", { name: /^Delete workspace / }).click();
      await page
        .getByRole("dialog", { name: "Delete workspace?" })
        .getByRole("button", { name: "Delete workspace" })
        .click();

      await expect(page.locator(".detail-pane-workspace-slot")).toHaveCount(0, { timeout: 10_000 });
      await deleteProxyCompleted;
      const launcherAppeared = await page.evaluate(
        () =>
          (Reflect.get(window, "__kenn_forgeDeleteLauncherProbe") as { appeared?: boolean } | undefined)?.appeared ??
          false,
      );
      expect(launcherAppeared).toBe(false);
      await expect.poll(async () => (await api!.get(`/api/v1/workspaces/${workspace.id}`)).status()).toBe(404);
    } finally {
      await page.unrouteAll({ behavior: "ignoreErrors" });
      await api?.dispose();
      await isolatedServer?.stop();
    }
  });

  test("the pane's own maximize and close controls keep the live terminal", async ({ page }) => {
    // The frame-geometry side of maximizing is covered above. This is the same pair
    // of pane-native controls driven for a different question: that the single hosted
    // terminal is reparented across the layout change rather than rebuilt.
    test.skip(
      !hasCommand("git") || !hasCommand("tmux", ["-V"]),
      "git and tmux are required for the real workspace flow",
    );

    let isolatedServer: IsolatedE2EServer | null = null;
    let api: APIRequestContext | null = null;
    try {
      isolatedServer = await startIsolatedWorkspaceE2EServer();
      api = await playwrightRequest.newContext({ baseURL: isolatedServer.info.base_url });
      const workspace = await createIssueWorkspace(api, 10);

      await page.goto(`${isolatedServer.info.base_url}/issues/github/acme/widgets/10`);
      await expect(page.locator(".detail-pane-workspace-slot .workspace-host-wrapper")).toBeVisible();
      await dismissWorkspaceLauncher(page);
      // The hosted shell opens on its workflow panel; the terminal container only
      // exists once that panel is open.
      const paneContainer = await openTerminalPanel(page);
      await paneContainer.evaluate((el) => el.setAttribute("data-continuity", "witness"));
      const witness = page.locator('[data-continuity="witness"]');

      const workspaceLeaf = page.locator(".tabbed-panel-leaf").filter({
        has: page.locator(".detail-pane-workspace-slot"),
      });
      const zoom = workspaceLeaf.locator('[data-testid="pane-toggle-zoom"]');
      await zoom.click();
      await expect(page.locator(".tabbed-panel-split-child.zoomed")).toBeVisible();
      // Maximizing must not rebuild the shell: the tagged node is the live one.
      await expect(paneContainer).toHaveAttribute("data-continuity", "witness");
      await typeMarkerCommand(page, paneContainer, workspace.worktree_path, "pane-marker-zoomed");

      await zoom.click();
      await expect(page.locator(".tabbed-panel-split-child.zoomed")).toHaveCount(0);
      await expect(witness).toBeVisible();

      // Closing unmounts the slot, so the host parks; the terminal is not torn
      // down, and reopening must reparent the same node back in.
      await workspaceLeaf.locator('[data-testid="pane-hide-workspace"]').click();
      await expect(page.locator(".detail-pane-workspace-slot")).toHaveCount(0);

      // Named for the session, not the container: a pane holding one terminal takes
      // that terminal's name, and the reopen strip has to agree with the tab.
      await page.getByRole("button", { name: "Show Shell" }).click();
      await expect(paneContainer).toHaveAttribute("data-continuity", "witness");
      // A torn-down or wedged session cannot run this.
      await typeMarkerCommand(page, paneContainer, workspace.worktree_path, "pane-marker-reopened");
    } finally {
      await api?.dispose();
      await isolatedServer?.stop();
    }
  });

  test("promoting a session to a pane of its own and back keeps the live shell", async ({ page }) => {
    // Promotion moves a session out of the workspace pane and into a top-level
    // pane of the detail surface. Nothing but a real backend shows whether the
    // pooled terminal was reparented or rebuilt: the component lanes mount
    // exited sessions, so a torn-down shell looks identical there.
    //
    // Driven through the palette commands rather than a drag, which is both the
    // keyboard path and the only one that works without layout geometry.
    test.skip(
      !hasCommand("git") || !hasCommand("tmux", ["-V"]),
      "git and tmux are required for the real workspace flow",
    );

    let isolatedServer: IsolatedE2EServer | null = null;
    let api: APIRequestContext | null = null;
    try {
      isolatedServer = await startIsolatedWorkspaceE2EServer();
      api = await playwrightRequest.newContext({ baseURL: isolatedServer.info.base_url });
      const workspace = await createIssueWorkspace(api, 10);

      await page.goto(`${isolatedServer.info.base_url}/issues/github/acme/widgets/10`);
      await expect(page.locator(".detail-pane-workspace-slot .workspace-host-wrapper")).toBeVisible();
      await dismissWorkspaceLauncher(page);
      // The dock, so this also covers promoting out of the container promotion
      // was not designed around.
      const dockContainer = await openTerminalPanel(page);
      // Stamped on the live node: the registry key is derived from the workspace,
      // session, and generation, so a rebuilt terminal would still carry it.
      await dockContainer.evaluate((el) => el.setAttribute("data-continuity", "promoted-shell"));
      const witness = page.locator('[data-continuity="promoted-shell"]');
      await typeMarkerCommand(page, dockContainer, workspace.worktree_path, "promote-marker-docked");

      await runPaletteCommand(page, "Move terminal session to a pane");

      // Out of the workspace pane entirely and into its own, carrying the very
      // node the dock was showing.
      await expect(page.locator('.detail-pane-workspace-slot [data-continuity="promoted-shell"]')).toHaveCount(0);
      await expect(page.locator('.session-terminal-slot [data-continuity="promoted-shell"]')).toBeVisible();
      // Same shell, still attached to the same tmux session. Focusing it here is
      // also what makes it the pane commands' target, so the demotion below acts
      // on the pane the user is working in.
      await typeMarkerCommand(page, witness, workspace.worktree_path, "promote-marker-in-pane");

      // Away and back while promoted. The pane is per surface and the session list
      // is per hosted workspace, so the other issue must not inherit this pane -
      // and returning must bring it back rather than leaving the terminal
      // unreachable in a pane nothing renders.
      // Away and back. The other issue has no workspace at all, so the surface
      // stops hosting one; what this proves is that the promoted PLACEMENT is not
      // lost by the round trip, which is the whole point of storing it in the
      // surface's tree rather than in the view.
      await selectIssueByTitle(page, DARK_MODE_ISSUE_TITLE);
      await selectIssueByTitle(page, SAFARI_ISSUE_TITLE);

      // A different DOM node this time: switching workspaces hands the previous
      // one's pooled terminals back, so this asserts the PLACEMENT survived and the
      // reattached shell is live, not that the node did.
      const returnedPane = page.locator(".session-terminal-slot .terminal-container");
      await expect(returnedPane).toBeVisible();
      await typeMarkerCommand(page, returnedPane, workspace.worktree_path, "promote-marker-returned");

      await runPaletteCommand(page, "Return terminal session to the workspace pane");

      // Home again, back inside the workspace pane rather than left in a pane
      // nothing renders. It is the workspace's only session, so the pane hands the
      // whole box to the terminal and there is no dock bar to come back to - which
      // is why this asserts on where the session landed, not on dock chrome.
      const redocked = page.locator(".detail-pane-workspace-slot .sole-embedded-session .terminal-container");
      await expect(redocked).toBeVisible();
      await typeMarkerCommand(page, redocked, workspace.worktree_path, "promote-marker-redocked");
    } finally {
      await api?.dispose();
      await isolatedServer?.stop();
    }
  });

  test("a row-only workspace retires behind its dock and returns when its pane is dropped into the terminal", async ({
    page,
  }) => {
    // This is the production-stack regression for the layout that exists when a
    // workflow session is promoted but a different shell remains in the bottom
    // dock. The container must leave the recursive pane tree without taking the
    // dock with it, then return to its stored branch when the session comes home.
    test.skip(
      !hasCommand("git") || !hasCommand("tmux", ["-V"]),
      "git and tmux are required for the real workspace flow",
    );

    let isolatedServer: IsolatedE2EServer | null = null;
    let api: APIRequestContext | null = null;
    try {
      isolatedServer = await startIsolatedWorkspaceE2EServer();
      api = await playwrightRequest.newContext({ baseURL: isolatedServer.info.base_url });
      const workspace = await createIssueWorkspace(api, 10);

      await page.goto(`${isolatedServer.info.base_url}/issues/github/acme/widgets/10`);
      await expect(page.locator(".detail-pane-workspace-slot .workspace-host-wrapper")).toBeVisible();
      await dismissWorkspaceLauncher(page);
      await openTerminalPanel(page);

      // A sole docked session fills the pane without dock chrome. Launch its
      // sibling through the pane controls; new launcher sessions belong to the
      // workflow stage, leaving the original shell in the bottom dock.
      const workspaceControlsTrigger = page.getByRole("button", { name: "Workspace controls" }).first();
      await workspaceControlsTrigger.click();
      const workspaceControls = page.getByRole("dialog", { name: "Workspace controls" });
      await workspaceControls.getByRole("button", { name: /Launch/ }).click();
      const launcher = page.getByRole("dialog", { name: "Launch a session" });
      await expect(launcher).toBeVisible();
      await launcher.getByRole("button", { name: "Shell", exact: true }).click();
      await expect(launcher).toBeHidden();
      await page.locator(".detail-host").dispatchEvent("pointerdown");
      await expect(workspaceControls).toBeHidden();

      const internalDock = page.locator(".detail-pane-workspace-slot .terminal-panel");
      await internalDock.getByRole("button", { name: "Close terminal panel", exact: true }).last().click();
      const workflowTerminal = page.locator(
        ".detail-pane-workspace-slot .workspace-stage .session-terminal-slot .terminal-container",
      );
      await expect(workflowTerminal).toBeVisible();
      await typeMarkerCommand(page, workflowTerminal, workspace.worktree_path, "row-only-before-promote");

      await runPaletteCommand(page, "Move terminal session to a pane");

      // The promoted pane fills the stored branch. The container is gone, while
      // the same dock row remains reachable at the surface edge.
      const promotedTerminal = page.locator(".tabbed-panel-leaf .session-terminal-slot .terminal-container");
      await expect(promotedTerminal).toBeVisible();
      await promotedTerminal.evaluate((element) => element.setAttribute("data-continuity", "row-only-promoted"));
      await expect(page.locator(".detail-pane-workspace-slot")).toHaveCount(0);
      const surfaceDock = page.locator(".detail-host > .terminal-panel");
      await expect(surfaceDock).toBeVisible();
      await typeMarkerCommand(page, promotedTerminal, workspace.worktree_path, "row-only-promoted");
      await surfaceDock.getByRole("button", { name: "Open terminal panel", exact: true }).click();
      const dockedTerminal = surfaceDock.locator(".terminal-container");
      await expect(dockedTerminal).toBeVisible();
      await typeMarkerCommand(page, dockedTerminal, workspace.worktree_path, "row-only-external-dock");
      // This is a detail-surface drag, not a terminal-session drag. The terminal
      // must translate it back to the owning runtime session and consume the drop;
      // otherwise it bubbles to the outer pane tree, which splits the detail
      // surface and leaves a short dock above a large dead area.
      const promotedLeaf = page.locator(".tabbed-panel-leaf").filter({
        has: page.locator('[data-continuity="row-only-promoted"]'),
      });
      const paneTab = promotedLeaf.getByRole("tab");
      const dockDropTarget = surfaceDock.getByRole("group", {
        name: /split drop targets$/,
      });
      await paneTab.dragTo(dockDropTarget, {
        targetPosition: { x: 8, y: 80 },
      });

      // The accepted drop demotes the pane into the terminal tree. With no
      // workflow content the wrapper correctly stays retired; the single external
      // dock now owns both live sessions and remains flush with the surface edge.
      await expect(page.locator(".detail-pane-workspace-slot")).toHaveCount(0);
      await expect(page.locator('[data-testid^="pane-hide-session:"]')).toHaveCount(0);
      await expect(surfaceDock).toBeVisible();
      await expect(surfaceDock.locator(".terminal-leaf")).toHaveCount(2);
      const surfaceGeometry = await page.locator(".detail-host").evaluate((host) => {
        const detail = host.querySelector(":scope > .detail-pane-layout");
        const dock = host.querySelector(":scope > .terminal-panel");
        if (!(detail instanceof HTMLElement) || !(dock instanceof HTMLElement)) return null;
        const hostRect = host.getBoundingClientRect();
        const detailRect = detail.getBoundingClientRect();
        const dockRect = dock.getBoundingClientRect();
        return {
          detailTop: detailRect.top - hostRect.top,
          gap: dockRect.top - detailRect.bottom,
          dockBottom: hostRect.bottom - dockRect.bottom,
          dockLeft: dockRect.left - hostRect.left,
          dockRight: hostRect.right - dockRect.right,
        };
      });
      expect(surfaceGeometry).not.toBeNull();
      for (const delta of Object.values(surfaceGeometry!)) {
        expect(Math.abs(delta)).toBeLessThanOrEqual(1);
      }
      const restoredPromotedTerminal = surfaceDock.locator('[data-continuity="row-only-promoted"]');
      await expect(restoredPromotedTerminal).toBeVisible();
      await typeMarkerCommand(
        page,
        restoredPromotedTerminal,
        workspace.worktree_path,
        "row-only-dropped-into-terminal",
      );
    } finally {
      await api?.dispose();
      await isolatedServer?.stop();
    }
  });

  test("a pooled workflow session keeps its live tmux shell while its host is reparented", async ({ page }) => {
    // Session terminals no longer live in the workspace view's own subtree:
    // they are rendered once by the app-level pool and reparented into
    // whichever slot shows them.
    //
    // Two distinct moves are under test. First a transfer between two DIFFERENT
    // slots — the dock leaf hands the shell to a workflow tab — which is the
    // pool's own source-to-destination path. Then a reparent of the slot itself,
    // when the workspace host travels into a detail pane: there the pool sees
    // the same slot at a new place in the document and must not park it.
    test.skip(
      !hasCommand("git") || !hasCommand("tmux", ["-V"]),
      "git and tmux are required for the real workspace flow",
    );

    let isolatedServer: IsolatedE2EServer | null = null;
    let api: APIRequestContext | null = null;
    try {
      isolatedServer = await startIsolatedWorkspaceE2EServer();
      api = await playwrightRequest.newContext({ baseURL: isolatedServer.info.base_url });
      const workspace = await createIssueWorkspace(api, 10);

      await page.goto(`${isolatedServer.info.base_url}/terminal/${workspace.id}`);
      const dockContainer = await openTerminalPanel(page);
      // Readiness, not continuity: the session moved below may be either shell.
      await typeMarkerCommand(page, dockContainer, workspace.worktree_path, "pooled-marker-in-dock");

      // A second shell, because the per-session header carrying the move
      // control only renders once the dock has more than one session.
      await page.getByRole("button", { name: "New terminal" }).click();

      // Move a session out of the terminal dock into the workflow region. Both
      // render pooled slots, so this is a transfer between two different slots.
      // The control is the SESSION's own, not the dock's "Move terminal panel
      // to workflow" — that one only redocks the panel.
      const dockLeaf = page.locator(".terminal-leaf").first();
      const moveSession = dockLeaf.getByRole("button", { name: /^Move (?!terminal panel).+ to workflow$/ });
      await expect(moveSession).toBeVisible();
      const dockedHost = dockLeaf.locator("[data-session-host]");
      await expect(dockedHost).toBeVisible();
      // Stamped BEFORE the move, and unique to this node. The registry key alone
      // would not do: it is derived from the workspace, session, and generation,
      // so a terminal destroyed and rebuilt carries the same one.
      await dockedHost.evaluate((el) => el.setAttribute("data-continuity", "moved-from-dock"));
      await moveSession.click();

      // The bottom dock still holds the other shell and takes the whole height,
      // leaving the workflow area a 1px sliver. Close it so the moved session
      // has somewhere to render.
      await page.getByRole("button", { name: "Close terminal panel", exact: true }).nth(1).click();
      // The very node the dock was showing, now inside a workflow tab. A rebuilt
      // terminal loses the stamp, and a stranded one never arrives.
      const movedHost = page.locator('.session-terminal-slot [data-continuity="moved-from-dock"]');
      await expect(movedHost).toBeVisible();
      const workflowContainer = movedHost.locator(".terminal-container");
      await expect(workflowContainer).toBeVisible();
      await workflowContainer.evaluate((el) => el.setAttribute("data-continuity", "pooled"));
      const witness = page.locator('[data-continuity="pooled"]');
      await typeMarkerCommand(page, workflowContainer, workspace.worktree_path, "pooled-marker-in-workflow");

      // Selecting the issue reparents the whole workspace host into the detail
      // pane. The pooled terminal has to travel with its slot: the pool sits
      // OUTSIDE the reparented wrapper, so a wrong placement here parks it.
      await selectIssueByTitle(page, SAFARI_ISSUE_TITLE);
      await expect(page.locator(".detail-pane-workspace-slot .workspace-host-wrapper")).toBeVisible();
      await expect(witness).toBeVisible();
      await expect(workflowContainer).toHaveAttribute("data-continuity", "pooled");
      // The same node AND a working input path: a parked or rebuilt terminal
      // cannot create this file.
      await typeMarkerCommand(page, workflowContainer, workspace.worktree_path, "pooled-marker-in-pane");

      await selectTopBarTab(page, "Workspaces");
      await expect(page).toHaveURL(new RegExp(`/terminal/${workspace.id}$`));
      await expect(witness).toBeVisible();
      await typeMarkerCommand(page, workflowContainer, workspace.worktree_path, "pooled-marker-back-in-tab");
    } finally {
      await api?.dispose();
      await isolatedServer?.stop();
    }
  });

  test("an embedded terminal route renders a live pooled session", async ({ page }) => {
    // The embed routes replace the whole app shell, so they never mount
    // WorkspaceHost — and therefore never got the pool that now owns every
    // session terminal. Every session pane on this route rendered an empty
    // portal slot until the embed shell mounted its own.
    test.skip(
      !hasCommand("git") || !hasCommand("tmux", ["-V"]),
      "git and tmux are required for the real workspace flow",
    );

    let isolatedServer: IsolatedE2EServer | null = null;
    let api: APIRequestContext | null = null;
    try {
      isolatedServer = await startIsolatedWorkspaceE2EServer();
      api = await playwrightRequest.newContext({ baseURL: isolatedServer.info.base_url });
      const workspace = await createIssueWorkspace(api, 10);

      await page.goto(`${isolatedServer.info.base_url}/workspaces/embed/terminal/${workspace.id}`);
      await openTerminalPanel(page);
      await page.getByRole("button", { name: "New terminal" }).click();
      const moveSession = page.getByRole("button", { name: /^Move (?!terminal panel).+ to workflow$/ }).first();
      await expect(moveSession).toBeVisible();
      await moveSession.click();
      await page.getByRole("button", { name: "Close terminal panel", exact: true }).nth(1).click();

      const workflowContainer = page.locator(".session-terminal-slot .terminal-container");
      await expect(workflowContainer).toBeVisible();
      // A slot with no pool behind it is an empty div: this cannot pass without
      // a terminal attached to the real tmux session.
      await typeMarkerCommand(page, workflowContainer, workspace.worktree_path, "embed-pooled-marker");
    } finally {
      await api?.dispose();
      await isolatedServer?.stop();
    }
  });

  test("a promoted terminal is part of its workspace's dock", async ({ page }) => {
    // The dock is a view of every pane of the workspace once a session is promoted.
    // Only the real app proves it: the store tests drive controllers directly, so
    // they cannot show the collapse control acting on both panes, or focus landing
    // inside a pooled terminal that mounts a frame after its pane reveals.
    test.skip(
      !hasCommand("git") || !hasCommand("tmux", ["-V"]),
      "git and tmux are required for the real workspace flow",
    );

    let isolatedServer: IsolatedE2EServer | null = null;
    let api: APIRequestContext | null = null;
    try {
      isolatedServer = await startIsolatedWorkspaceE2EServer();
      api = await playwrightRequest.newContext({ baseURL: isolatedServer.info.base_url });
      const workspace = await createIssueWorkspace(api, 10);

      await page.goto(`${isolatedServer.info.base_url}/issues/github/acme/widgets/10`);
      await expect(page.locator(".detail-pane-workspace-slot .workspace-host-wrapper")).toBeVisible();
      await dismissWorkspaceLauncher(page);
      const dockContainer = await openTerminalPanel(page);
      await typeMarkerCommand(page, dockContainer, workspace.worktree_path, "dock-marker-before-promote");

      await runPaletteCommand(page, "Move terminal session to a pane");
      const promoted = page.locator(".session-terminal-slot .terminal-container");
      await expect(promoted).toBeVisible();
      // Working in it is what makes it this workspace's last-focused pane, which is
      // what Focus Terminal and an expand act on from here on.
      await typeMarkerCommand(page, promoted, workspace.worktree_path, "promoted-marker-before-hide");

      // Closed, so the reveal has to mount the slot and the pool has to reparent the
      // wrapper before focus can land - the race a same-flush focus attempt loses.
      const promotedLeaf = page.locator(".tabbed-panel-leaf").filter({ has: page.locator(".session-terminal-slot") });
      await promotedLeaf.locator('[data-testid^="pane-hide-session:"]').click();
      await expect(page.locator(".session-terminal-slot")).toHaveCount(0);

      await page.getByRole("button", { name: "Focus Terminal" }).click();

      const restored = page.locator(".session-terminal-slot .terminal-container");
      await expect(restored).toBeVisible();
      await expect
        .poll(async () =>
          restored.evaluate((el) => el.closest("[data-session-host]")?.contains(document.activeElement) ?? false),
        )
        .toBe(true);
      await typeMarkerCommand(page, restored, workspace.worktree_path, "promoted-marker-after-focus");

      // Collapse reaches both panes. A promoted terminal left on screen is the whole
      // workspace still sitting there while the button claims it is away.
      await collapseTerminal(page);
      await expect(page.locator(".detail-pane-workspace-slot")).toHaveCount(0);
      await expect(page.locator(".session-terminal-slot")).toHaveCount(0);

      // Collapsing both panes at once drops a whole split node out of the pane tree,
      // and the way back has to survive that. Focus restores the promoted terminal;
      // the empty workspace container stays retired behind its external dock.
      await page.getByRole("button", { name: "Focus Terminal" }).click();
      await expect(page.locator(".detail-pane-workspace-slot")).toHaveCount(0);
      await expect(page.locator(".detail-host > .terminal-panel")).toBeVisible();
      await expect(restored).toBeVisible();
      await typeMarkerCommand(page, restored, workspace.worktree_path, "marker-after-collapse");
    } finally {
      await api?.dispose();
      await isolatedServer?.stop();
    }
  });

  test("a workspace put away keeps its panes through another workspace's dock", async ({ page }) => {
    // The container tab is shared by every workspace on a surface, so the collapse
    // record is the only thing that knows what a given workspace put away. Only the
    // real app runs the sequence that breaks it: collapse A, let B unhide the shared
    // container, then come back to A - where the dock reports "split" while A's
    // promoted terminal is still hidden.
    test.skip(
      !hasCommand("git") || !hasCommand("tmux", ["-V"]),
      "git and tmux are required for the real workspace flow",
    );

    let isolatedServer: IsolatedE2EServer | null = null;
    let api: APIRequestContext | null = null;
    try {
      isolatedServer = await startIsolatedWorkspaceE2EServer();
      api = await playwrightRequest.newContext({ baseURL: isolatedServer.info.base_url });
      const workspace = await createIssueWorkspace(api, 10);
      await createIssueWorkspace(api, 11);

      await page.goto(`${isolatedServer.info.base_url}/issues/github/acme/widgets/10`);
      await expect(page.locator(".detail-pane-workspace-slot .workspace-host-wrapper")).toBeVisible();
      await dismissWorkspaceLauncher(page);
      await openTerminalPanel(page);
      await runPaletteCommand(page, "Move terminal session to a pane");
      const promoted = page.locator(".session-terminal-slot .terminal-container");
      await expect(promoted).toBeVisible();
      await typeMarkerCommand(page, promoted, workspace.worktree_path, "cross-marker-before");

      await collapseTerminal(page);
      await expect(page.locator(".session-terminal-slot")).toHaveCount(0);

      // The other issue's workspace, brought back on screen. It has no promoted pane
      // of its own, so this unhides the container both workspaces share.
      await selectIssueByTitle(page, DARK_MODE_ISSUE_TITLE);
      await page.getByRole("button", { name: "Focus Terminal" }).click();
      await expect(page.locator(".detail-pane-workspace-slot .workspace-host-wrapper")).toBeVisible();
      await dismissWorkspaceLauncher(page);

      // Back to the first issue: the other workspace must not unhide this one's
      // retired container or promoted terminal. Its own collapse record remains the
      // only route back to that pane.
      await selectIssueByTitle(page, SAFARI_ISSUE_TITLE);
      await expect(page.locator(".detail-pane-workspace-slot")).toHaveCount(0);
      await expect(page.locator(".session-terminal-slot")).toHaveCount(0);

      await page.getByRole("button", { name: "Focus Terminal" }).click();
      await expect(page.locator(".detail-pane-workspace-slot")).toHaveCount(0);
      await expect(page.locator(".detail-host > .terminal-panel")).toBeVisible();
      await expect(promoted).toBeVisible();
      await typeMarkerCommand(page, promoted, workspace.worktree_path, "cross-marker-after");
    } finally {
      await api?.dispose();
      await isolatedServer?.stop();
    }
  });

  test("Focus Terminal reveals the workspace from behind a tab, a zoom, and a close", async ({ page }) => {
    // Three ways to be invisible without being "collapsed", each of which left
    // the terminal parked while the store reported it visible. Only a real
    // backend proves the reveal actually lands on a live session rather than a
    // rebuilt one.
    test.skip(
      !hasCommand("git") || !hasCommand("tmux", ["-V"]),
      "git and tmux are required for the real workspace flow",
    );

    let isolatedServer: IsolatedE2EServer | null = null;
    let api: APIRequestContext | null = null;
    try {
      isolatedServer = await startIsolatedWorkspaceE2EServer();
      api = await playwrightRequest.newContext({ baseURL: isolatedServer.info.base_url });
      const workspace = await createIssueWorkspace(api, 10);

      await page.goto(`${isolatedServer.info.base_url}/issues/github/acme/widgets/10`);
      await expect(page.locator(".detail-pane-workspace-slot .workspace-host-wrapper")).toBeVisible();
      await dismissWorkspaceLauncher(page);
      const paneContainer = await openTerminalPanel(page);
      await paneContainer.evaluate((el) => el.setAttribute("data-continuity", "reveal"));

      const workspaceLeaf = page.locator(".tabbed-panel-leaf").filter({
        has: page.locator(".detail-pane-workspace-slot"),
      });
      const conversationLeaf = page.locator(".tabbed-panel-leaf").filter({
        has: page.getByRole("tab", { name: "Conversation" }),
      });
      const focusTerminal = page.getByRole("button", { name: "Focus Terminal" });

      // 1. Tabbed behind a sibling: move the ordinary Conversation tab into the
      //    strip-less workspace leaf, then switch away from the workspace. This
      //    leaves it neither hidden nor maximized without restoring a solo grip.
      await conversationLeaf
        .getByRole("tab", { name: "Conversation" })
        .dragTo(workspaceLeaf.getByRole("group", { name: "Detail pane drop targets" }));
      await page.getByRole("tab", { name: "Conversation" }).click();
      await expect(page.locator(".detail-pane-workspace-slot")).toHaveCount(0);

      await focusTerminal.click();
      await expect(paneContainer).toHaveAttribute("data-continuity", "reveal");
      await typeMarkerCommand(page, paneContainer, workspace.worktree_path, "reveal-from-tab");

      // 2. Buried under another leaf's zoom.
      await page.getByRole("tab", { name: "Conversation" }).click();
      await conversationLeaf.locator('[data-testid="pane-toggle-zoom"]').click();
      await expect(page.locator(".detail-pane-workspace-slot")).toHaveCount(0);

      await focusTerminal.click();
      await expect(paneContainer).toHaveAttribute("data-continuity", "reveal");
      await typeMarkerCommand(page, paneContainer, workspace.worktree_path, "reveal-from-zoom");

      // 3. Closed outright.
      await page.locator('[data-testid="pane-hide-workspace"]').click();
      await expect(page.locator(".detail-pane-workspace-slot")).toHaveCount(0);

      await focusTerminal.click();
      await expect(paneContainer).toHaveAttribute("data-continuity", "reveal");
      await typeMarkerCommand(page, paneContainer, workspace.worktree_path, "reveal-from-closed");
    } finally {
      await api?.dispose();
      await isolatedServer?.stop();
    }
  });

  test("tab flip preserves the live terminal (xterm)", async ({ browserName, page }) => {
    test.skip(
      !hasCommand("git") || !hasCommand("tmux", ["-V"]),
      "git and tmux are required for the real workspace flow",
    );

    let isolatedServer: IsolatedE2EServer | null = null;
    let api: APIRequestContext | null = null;
    try {
      const controlFrames = observeTerminalControlFrames(page);
      const refreshFrames = () => controlFrames.filter((frame) => frame.type === "refresh");
      isolatedServer = await startIsolatedWorkspaceE2EServer();
      api = await playwrightRequest.newContext({ baseURL: isolatedServer.info.base_url });

      const workspace = await createIssueWorkspace(api, 10);

      if (browserName === "chromium") {
        await page.addInitScript(() => {
          const trackedWindow = window as Window & {
            __kenn_forgeGenerateMipmapCalls?: number;
          };
          trackedWindow.__kenn_forgeGenerateMipmapCalls = 0;
          if (typeof WebGL2RenderingContext === "undefined") return;
          const original = Object.getOwnPropertyDescriptor(WebGL2RenderingContext.prototype, "generateMipmap")
            ?.value as WebGL2RenderingContext["generateMipmap"];
          WebGL2RenderingContext.prototype.generateMipmap = function (target: number): void {
            trackedWindow.__kenn_forgeGenerateMipmapCalls = (trackedWindow.__kenn_forgeGenerateMipmapCalls ?? 0) + 1;
            original.call(this, target);
          };
        });
      }
      await page.goto(`${isolatedServer.info.base_url}/terminal/${workspace.id}`);
      const tabContainer = await openTerminalPanel(page);
      await typeMarkerCommand(page, tabContainer, workspace.worktree_path, "continuity-marker-one");
      const beforeZoom = await readPtyGeometry(
        page,
        tabContainer,
        workspace.worktree_path,
        "xterm-geometry-before-zoom",
      );
      const resetZoom = page.getByRole("button", {
        name: "Reset terminal font size",
      });
      await expect(resetZoom).toHaveText("12px");
      const refreshCountBeforeZoom = refreshFrames().length;
      for (let fontSize = 13; fontSize <= ZOOMED_TERMINAL_FONT_SIZE; fontSize += 1) {
        await page.getByRole("button", { name: "Increase terminal font size" }).click();
        await expect(resetZoom).toHaveText(`${fontSize}px`);
      }
      await expectPersistedTerminalFontSize(api, ZOOMED_TERMINAL_FONT_SIZE);
      await expect.poll(() => refreshFrames().length).toBeGreaterThan(refreshCountBeforeZoom);
      await expect.poll(() => refreshFrames().at(-1)?.cols).toBeLessThan(beforeZoom.cols);
      await waitForPtyColumnsBelow(
        page,
        tabContainer,
        workspace.worktree_path,
        "xterm-geometry-after-zoom",
        beforeZoom.cols,
      );
      if (browserName === "chromium") {
        await expect(tabContainer.locator("canvas").first()).toBeVisible();
        await expect
          .poll(() =>
            page.evaluate(
              () =>
                (
                  window as Window & {
                    __kenn_forgeGenerateMipmapCalls?: number;
                  }
                ).__kenn_forgeGenerateMipmapCalls ?? 0,
            ),
          )
          .toBe(0);
      }

      await tabContainer.evaluate((el) => {
        el.setAttribute("data-continuity", "witness");
      });

      // Select the issue that owns this workspace: its detail carries a
      // ready workspace ref, so the inline claim fires and the detail pane takes
      // the shared host — same hostedWorkspaceKey, so this must reparent
      // the exact tagged node rather than recreate it.
      await selectIssueByTitle(page, SAFARI_ISSUE_TITLE);

      const witness = page.locator('[data-continuity="witness"]');
      await expect(witness).toBeVisible();
      const paneContainer = page.locator(".detail-pane-workspace-slot .terminal-container");
      await expect(paneContainer).toHaveAttribute("data-continuity", "witness");
      await typeMarkerCommand(page, paneContainer, workspace.worktree_path, "continuity-marker-two");

      // Flip back to the Workspaces tab: route memory returns to
      // /terminal/{id} — the same hostedWorkspaceKey as the pane claim, so
      // this is another reparent, not a reconnect.
      await selectTopBarTab(page, "Workspaces");
      await expect(page).toHaveURL(new RegExp(`/terminal/${workspace.id}$`));

      const backInTab = page.locator(".workspace-tab-slot .terminal-container");
      await expect(witness).toBeVisible();
      await expect(backInTab).toHaveAttribute("data-continuity", "witness");
      // The same session must still accept input after the second
      // reparent — a torn-down or wedged terminal cannot run this.
      await typeMarkerCommand(page, backInTab, workspace.worktree_path, "continuity-marker-three");
    } finally {
      await api?.dispose();
      await isolatedServer?.stop();
    }
  });

  test("a different remembered key takes the reconnecting switch path", async ({ page }) => {
    test.skip(
      !hasCommand("git") || !hasCommand("tmux", ["-V"]),
      "git and tmux are required for the real workspace flow",
    );

    let isolatedServer: IsolatedE2EServer | null = null;
    let api: APIRequestContext | null = null;
    try {
      isolatedServer = await startIsolatedWorkspaceE2EServer();
      api = await playwrightRequest.newContext({ baseURL: isolatedServer.info.base_url });

      await createIssueWorkspace(api, 10);
      const workspaceB = await createIssueWorkspace(api, 11);

      // Land on B first: the router seeds its "last workspace route" cache
      // from the initial location, so the Workspaces tab will remember
      // /terminal/{B.id} for the rest of the test.
      await page.goto(`${isolatedServer.info.base_url}/terminal/${workspaceB.id}`);
      await expect(page.locator(".workspace-list-sidebar .ws-row.selected")).toContainText(DARK_MODE_ISSUE_TITLE);

      // Select issue A: its ready workspace claims the shared host inline,
      // a different hostedWorkspaceKey than the remembered B route.
      await selectIssueByTitle(page, SAFARI_ISSUE_TITLE);
      await dismissWorkspaceLauncher(page);
      const paneContainer = await openTerminalPanel(page);
      await paneContainer.evaluate((el) => {
        el.setAttribute("data-continuity", "witness-a");
      });
      await expect(page.locator('[data-continuity="witness-a"]')).toBeVisible();

      // Flip to the Workspaces tab: route memory points at B, not A, so WTV
      // must take its normal reconnecting switch instead of a reparent.
      await selectTopBarTab(page, "Workspaces");
      await expect(page).toHaveURL(new RegExp(`/terminal/${workspaceB.id}$`));

      await expect(page.locator('[data-continuity="witness-a"]')).toHaveCount(0);
      await expect(page.locator(".workspace-list-sidebar .ws-row.selected")).toContainText(DARK_MODE_ISSUE_TITLE);
    } finally {
      await api?.dispose();
      await isolatedServer?.stop();
    }
  });

  test("tmux wheel scrolling survives local workspace renderer replacement", async ({ page }) => {
    test.skip(
      !hasCommand("git") || !hasCommand("tmux", ["-V"]),
      "git and tmux are required for the real workspace flow",
    );

    let isolatedServer: IsolatedE2EServer | null = null;
    let api: APIRequestContext | null = null;
    try {
      const output = observeTerminalOutput(page);
      isolatedServer = await startIsolatedWorkspaceE2EServer();
      api = await playwrightRequest.newContext({ baseURL: isolatedServer.info.base_url });
      const workspaceA = await createIssueWorkspace(api, 10);
      const workspaceB = await createIssueWorkspace(api, 11);

      await page.goto(`${isolatedServer.info.base_url}/terminal/${workspaceA.id}`);
      const initialA = await openTerminalPanel(page);
      const runtimeTmuxSessionA = await runningRuntimeTmuxSession(api, workspaceA.id);
      await initialA.click({ position: { x: 10, y: 10 } });
      await runAttachedTmuxCommand(page, "set-option -g mouse off");
      await runAttachedTmuxCommand(page, "set-option -g mouse on");
      await expect(initialA.locator(".xterm.enable-mouse-events")).toBeVisible();

      const replaySentinel = "mode-replay-output-complete";
      await typeIntoTerminal(
        page,
        initialA,
        `printf '\\033[?2004h'; yes '0123456789abcdef0123456789abcdef' | head -n 5000; printf '\\nmode-replay-output-%s\\n' complete; while IFS= read -r _; do :; done`,
      );
      await expect
        .poll(() => output.sessionOutputIncludes(workspaceA.id, replaySentinel), { timeout: 15_000 })
        .toBe(true);

      await expectWheelScroll(page, initialA, isolatedServer, runtimeTmuxSessionA);
      exitTmuxCopyMode(isolatedServer, runtimeTmuxSessionA);
      await initialA.evaluate((element) => {
        element.setAttribute("data-continuity", "mouse-before-standalone-switch");
      });

      await page.locator(".workspace-list-sidebar .ws-row", { hasText: DARK_MODE_ISSUE_TITLE }).click();
      await expect(page).toHaveURL(new RegExp(`/terminal/${workspaceB.id}$`));
      const containerB = await openTerminalPanel(page);
      await typeMarkerCommand(page, containerB, workspaceB.worktree_path, "mouse-replay-switch-b");

      await page.locator(".workspace-list-sidebar .ws-row", { hasText: SAFARI_ISSUE_TITLE }).click();
      await expect(page).toHaveURL(new RegExp(`/terminal/${workspaceA.id}$`));
      const returnedA = page.locator(".terminal-panel.open .terminal-container").first();
      await expect(returnedA).toBeVisible();
      await expect(page.locator('[data-continuity="mouse-before-standalone-switch"]')).toHaveCount(0);
      await expectWheelScroll(page, returnedA, isolatedServer, runtimeTmuxSessionA);
      exitTmuxCopyMode(isolatedServer, runtimeTmuxSessionA);
      const standalonePaste = "standalone-bracketed-paste";
      await dispatchTerminalPaste(returnedA, standalonePaste);
      await expect
        .poll(() => output.inputIncludes(`\x1b[200~${standalonePaste}\x1b[201~`), { timeout: 15_000 })
        .toBe(true);

      await returnedA.evaluate((element) => {
        element.setAttribute("data-continuity", "mouse-before-inline-switch");
      });
      await selectIssueByTitle(page, DARK_MODE_ISSUE_TITLE);
      await expect(page.locator(".detail-pane-workspace-slot .terminal-container")).toBeVisible();
      await selectIssueByTitle(page, SAFARI_ISSUE_TITLE);

      const inlineA = page.locator(".detail-pane-workspace-slot .terminal-container").first();
      await expect(inlineA).toBeVisible();
      await expect(page.locator('[data-continuity="mouse-before-inline-switch"]')).toHaveCount(0);
      await expectWheelScroll(page, inlineA, isolatedServer, runtimeTmuxSessionA);
      exitTmuxCopyMode(isolatedServer, runtimeTmuxSessionA);
      const inlinePaste = "inline-bracketed-paste";
      await dispatchTerminalPaste(inlineA, inlinePaste);
      await expect.poll(() => output.inputIncludes(`\x1b[200~${inlinePaste}\x1b[201~`), { timeout: 15_000 }).toBe(true);
      await page.keyboard.press("Control+c");
    } finally {
      await api?.dispose();
      await isolatedServer?.stop();
    }
  });

  test("terminal replay preserves split data through a real tmux websocket boundary", async ({ page }) => {
    test.skip(
      !hasCommand("git") || !hasCommand("tmux", ["-V"]),
      "git and tmux are required for the real workspace flow",
    );

    const cases = [
      {
        name: "partial-utf8",
        candidateFormat: "replay-partial-utf8:\\033\\033[He\\314",
        continuationFormat: "\\201\\033\\033[6n",
        pasteText: null,
        entersAlternateScreen: false,
        evictsCandidateFromReplay: false,
        expectsBracketedPaste: false,
        expectsCursorReport: true,
      },
      {
        name: "incomplete-csi",
        candidateFormat: "replay-incomplete-csi:\\033\\033[?2004",
        continuationFormat: "h",
        pasteText: "incomplete-csi-bracketed-paste",
        entersAlternateScreen: false,
        evictsCandidateFromReplay: false,
        expectsBracketedPaste: true,
        expectsCursorReport: false,
      },
      {
        name: "unicode-interrupted-csi",
        candidateFormat: "replay-unicode-interrupted-csi:\\033\\033[?2004\\342\\230\\203h",
        continuationFormat: "\\033\\033[6n",
        pasteText: "unicode-interrupted-csi-paste",
        entersAlternateScreen: true,
        evictsCandidateFromReplay: true,
        expectsBracketedPaste: false,
        expectsCursorReport: true,
      },
      {
        name: "alternate-screen-incomplete-csi",
        candidateFormat: "",
        continuationFormat: "",
        directPrecondition: "\x1b[?1000l;1002l;1003l",
        directCandidate: "\x1b[?1049h\rsame-renderer-alt-csi:\x1b[?1003h\x1b[5",
        directContinuation: "C\x1b[6n",
        pasteText: null,
        entersAlternateScreen: false,
        evictsCandidateFromReplay: false,
        expectsBracketedPaste: false,
        expectsCursorReport: true,
        expectedCursorColumn: "28",
      },
      {
        name: "osc-split-st",
        candidateFormat: "\\033\\033]0;replay-osc-split-st\\033\\033",
        continuationFormat: "\\\\\\033\\033[6n",
        pasteText: null,
        entersAlternateScreen: false,
        evictsCandidateFromReplay: false,
        expectsBracketedPaste: false,
        expectsCursorReport: true,
      },
      {
        name: "dcs-split-st",
        candidateFormat: "replay-dcs-split-st:\\033\\033P$qm\\033\\033",
        continuationFormat: "\\\\\\033\\033[6n",
        pasteText: null,
        entersAlternateScreen: false,
        evictsCandidateFromReplay: false,
        expectsBracketedPaste: false,
        expectsCursorReport: true,
      },
    ] as const;

    await page.addInitScript(() => {
      const refreshSuppressedFor = new Set<string>();
      const runtimeSockets: Array<{ url: string; socket: WebSocket; receivedFrames: number }> = [];
      (
        window as unknown as {
          __suppressRuntimeRefresh: (workspaceId: string) => void;
        }
      ).__suppressRuntimeRefresh = (workspaceId: string) => {
        refreshSuppressedFor.add(workspaceId);
      };
      (
        window as unknown as {
          __disconnectRuntime: (workspaceId: string) => number;
          __runtimeReconnectReady: (workspaceId: string, previousCount: number) => boolean;
        }
      ).__disconnectRuntime = (workspaceId: string) => {
        const matching = runtimeSockets.filter(({ url }) => url.includes(`/workspaces/${workspaceId}/`));
        matching.at(-1)?.socket.close();
        return matching.length;
      };
      (
        window as unknown as {
          __runtimeReconnectReady: (workspaceId: string, previousCount: number) => boolean;
        }
      ).__runtimeReconnectReady = (workspaceId: string, previousCount: number) => {
        const matching = runtimeSockets.filter(({ url }) => url.includes(`/workspaces/${workspaceId}/`));
        const latest = matching.at(-1);
        return (
          matching.length > previousCount && latest?.socket.readyState === WebSocket.OPEN && latest.receivedFrames > 0
        );
      };
      const Native = window.WebSocket;
      class RefreshSuppressingWebSocket extends Native {
        readonly suppressRefresh: boolean;

        constructor(url: string | URL, protocols?: string | string[]) {
          const rewritten = new URL(String(url));
          const suppressRefresh = [...refreshSuppressedFor].some((id) =>
            rewritten.pathname.includes(`/workspaces/${id}/`),
          );
          if (suppressRefresh) {
            rewritten.searchParams.delete("cols");
            rewritten.searchParams.delete("rows");
          }
          super(rewritten, protocols);
          this.suppressRefresh = suppressRefresh;
          if (rewritten.pathname.includes("/runtime/sessions/")) {
            const entry = { url: rewritten.toString(), socket: this as WebSocket, receivedFrames: 0 };
            runtimeSockets.push(entry);
            this.addEventListener("message", () => {
              entry.receivedFrames += 1;
            });
          }
        }

        override send(data: string | ArrayBufferLike | Blob | ArrayBufferView): void {
          if (this.suppressRefresh && typeof data === "string") {
            try {
              if ((JSON.parse(data) as { type?: string }).type === "refresh") return;
            } catch {
              // Forward non-JSON terminal input unchanged.
            }
          }
          Native.prototype.send.call(this, data);
        }
      }
      window.WebSocket = RefreshSuppressingWebSocket as unknown as typeof WebSocket;
    });

    for (const boundary of cases) {
      let isolatedServer: IsolatedE2EServer | null = null;
      let api: APIRequestContext | null = null;
      let helperFinishPath: string | null = null;
      try {
        const output = observeTerminalOutput(page);
        isolatedServer = await startIsolatedWorkspaceE2EServer();
        api = await playwrightRequest.newContext({ baseURL: isolatedServer.info.base_url });
        const workspaceA = await createIssueWorkspace(api, 10);
        const workspaceB = await createIssueWorkspace(api, 11);

        await page.goto(`${isolatedServer.info.base_url}/terminal/${workspaceA.id}`);
        let containerA = await openTerminalPanel(page);
        const tmuxSessionA = await runningRuntimeTmuxSession(api, workspaceA.id);
        runE2ETmuxCommand(isolatedServer, ["set-option", "-p", "-t", tmuxSessionA, "allow-passthrough", "on"]);
        runE2ETmuxCommand(isolatedServer, ["set-option", "-t", tmuxSessionA, "status", "off"]);
        await page.evaluate((workspaceId) => {
          (
            window as unknown as {
              __suppressRuntimeRefresh?: (id: string) => void;
            }
          ).__suppressRuntimeRefresh?.(workspaceId);
        }, workspaceA.id);

        runE2ETmuxCommand(isolatedServer, ["set-option", "-g", "-t", tmuxSessionA, "mouse", "off"]);
        runE2ETmuxCommand(isolatedServer, ["set-option", "-g", "-t", tmuxSessionA, "mouse", "on"]);
        await expect(containerA.locator(".xterm.enable-mouse-events")).toBeVisible();

        const readyPath = path.join(workspaceA.worktree_path, `${boundary.name}-ready`);
        const continuePath = path.join(workspaceA.worktree_path, `${boundary.name}-continue`);
        const donePath = path.join(workspaceA.worktree_path, `${boundary.name}-done`);
        helperFinishPath = path.join(workspaceA.worktree_path, `${boundary.name}-finish`);
        const helperPath = path.join(workspaceA.worktree_path, `${boundary.name}-helper.sh`);
        const completionMarker = `${boundary.name}-complete`;
        const command = [
          "i=0",
          `while [ "$i" -lt 2500 ]; do printf '${tmuxPassthroughFormat("0123456789abcdef0123456789abcdef\\r\\n")}'; i=$((i+1)); done`,
          ...(boundary.entersAlternateScreen ? [`printf '${tmuxPassthroughFormat("\\033\\033[?1049h")}'`] : []),
          ...(boundary.evictsCandidateFromReplay ? [`printf '${tmuxPassthroughFormat("\\033\\033[?2004l")}'`] : []),
          ...("directCandidate" in boundary ? [] : [`printf '${tmuxPassthroughFormat(boundary.candidateFormat)}'`]),
          ...(boundary.evictsCandidateFromReplay
            ? [
                "i=0",
                `while [ "$i" -lt 2500 ]; do printf '${tmuxPassthroughFormat("fedcba9876543210fedcba9876543210\\r\\n")}'; i=$((i+1)); done`,
              ]
            : []),
          `touch '${readyPath}'`,
          `while [ ! -f '${continuePath}' ]; do sleep 0.05; done`,
          `printf '${tmuxPassthroughFormat(boundary.continuationFormat)}'`,
          `printf '\\r\\n${completionMarker}\\r\\n'`,
          `touch '${donePath}'`,
          `while [ ! -f '${helperFinishPath}' ]; do sleep 0.05; done`,
        ].join("\n");
        writeFileSync(helperPath, `${command}\n`, "utf8");

        await typeIntoTerminal(page, containerA, `sh '${helperPath}'`);
        await expect.poll(() => existsSync(readyPath), { timeout: 15_000 }).toBe(true);
        if ("directCandidate" in boundary) {
          writeTmuxClientTTY(isolatedServer, tmuxSessionA, new TextEncoder().encode(boundary.directPrecondition));
          await expect(containerA.locator(".xterm.enable-mouse-events")).toHaveCount(0);
          writeTmuxClientTTY(isolatedServer, tmuxSessionA, new TextEncoder().encode(boundary.directCandidate));
          await expect
            .poll(() => output.sessionOutputIncludes(workspaceA.id, "same-renderer-alt-csi:"), { timeout: 15_000 })
            .toBe(true);
          await expect(containerA.locator(".xterm.enable-mouse-events")).toBeVisible();
        } else if (!boundary.evictsCandidateFromReplay) {
          await expect
            .poll(() => output.sessionOutputIncludes(workspaceA.id, `replay-${boundary.name}`), { timeout: 15_000 })
            .toBe(true);
        }
        await containerA.evaluate((element, name) => {
          element.setAttribute("data-replay-boundary", name);
        }, boundary.name);

        if ("directCandidate" in boundary) {
          const previousConnectionCount = await page.evaluate((workspaceId) => {
            return (
              window as unknown as {
                __disconnectRuntime: (id: string) => number;
              }
            ).__disconnectRuntime(workspaceId);
          }, workspaceA.id);
          await expect
            .poll(() =>
              page.evaluate(
                ({ workspaceId, previousConnectionCount }) =>
                  (
                    window as unknown as {
                      __runtimeReconnectReady: (id: string, count: number) => boolean;
                    }
                  ).__runtimeReconnectReady(workspaceId, previousConnectionCount),
                { workspaceId: workspaceA.id, previousConnectionCount },
              ),
            )
            .toBe(true);
        } else {
          await page.locator(".workspace-list-sidebar .ws-row", { hasText: DARK_MODE_ISSUE_TITLE }).click();
          await expect(page).toHaveURL(new RegExp(`/terminal/${workspaceB.id}$`));
          await ensureTerminalPanelOpen(page);
          await page.locator(".workspace-list-sidebar .ws-row", { hasText: SAFARI_ISSUE_TITLE }).click();
          await expect(page).toHaveURL(new RegExp(`/terminal/${workspaceA.id}$`));
        }

        containerA = await ensureTerminalPanelOpen(page);
        await expect(page.locator(`[data-replay-boundary="${boundary.name}"]`)).toHaveCount(
          "directCandidate" in boundary ? 1 : 0,
        );
        await expect(containerA.locator(".xterm.enable-mouse-events")).toBeVisible();
        await containerA.locator(".xterm-helper-textarea").focus();
        const bracketedPaste = boundary.pasteText === null ? null : `\x1b[200~${boundary.pasteText}\x1b[201~`;
        const bracketedPasteOccurrencesBeforeContinuation =
          bracketedPaste === null ? 0 : output.inputOccurrences(bracketedPaste);
        const pasteTextOccurrencesBeforeContinuation =
          boundary.pasteText === null ? 0 : output.inputOccurrences(boundary.pasteText);
        const cursorReportsBeforeContinuation = output.cursorReports().length;

        if ("directContinuation" in boundary) {
          writeTmuxClientTTY(isolatedServer, tmuxSessionA, new TextEncoder().encode(boundary.directContinuation));
        }
        writeFileSync(continuePath, "continue\n", "utf8");
        await expect.poll(() => existsSync(donePath), { timeout: 15_000 }).toBe(true);
        await expect
          .poll(() => output.sessionOutputIncludes(workspaceA.id, completionMarker), { timeout: 15_000 })
          .toBe(true);
        if (boundary.pasteText !== null && bracketedPaste !== null) {
          await dispatchTerminalPaste(containerA, boundary.pasteText);
          await expect
            .poll(() => output.inputOccurrences(boundary.pasteText))
            .toBeGreaterThan(pasteTextOccurrencesBeforeContinuation);
          if (boundary.expectsBracketedPaste) {
            await expect
              .poll(() => output.inputOccurrences(bracketedPaste))
              .toBeGreaterThan(bracketedPasteOccurrencesBeforeContinuation);
          } else {
            expect(output.inputOccurrences(bracketedPaste)).toBe(bracketedPasteOccurrencesBeforeContinuation);
          }
        }
        if (boundary.expectsCursorReport) {
          await expect.poll(() => output.cursorReports().length).toBeGreaterThan(cursorReportsBeforeContinuation);
          if ("expectedCursorColumn" in boundary) {
            const cursorColumn = /^\x1b\[\??\d+;(\d+)R$/.exec(output.cursorReports().at(-1) ?? "")?.[1];
            expect(cursorColumn).toBe(boundary.expectedCursorColumn);
          }
        }
        writeFileSync(helperFinishPath, "finish\n", "utf8");
        helperFinishPath = null;
      } finally {
        if (helperFinishPath !== null) {
          writeFileSync(helperFinishPath, "finish\n", "utf8");
        }
        await api?.dispose();
        await isolatedServer?.stop();
      }
    }
  });

  test("same renderer reconnect applies mouse mode disabled while offline", async ({ page }) => {
    test.skip(
      !hasCommand("git") || !hasCommand("tmux", ["-V"]),
      "git and tmux are required for the real workspace flow",
    );

    let isolatedServer: IsolatedE2EServer | null = null;
    let api: APIRequestContext | null = null;
    let helperFinishPath: string | null = null;
    try {
      await page.addInitScript(() => {
        const log: Array<{ url: string; closed: boolean; socket: WebSocket; sent: string[] }> = [];
        (window as unknown as { __reconnectWsLog: typeof log }).__reconnectWsLog = log;
        const Native = window.WebSocket;
        class TrackedWebSocket extends Native {
          constructor(url: string | URL, protocols?: string | string[]) {
            super(url, protocols);
            const entry = { url: String(url), closed: false, socket: this, sent: [] };
            log.push(entry);
            this.addEventListener("close", () => {
              entry.closed = true;
            });
          }

          override send(data: string | ArrayBufferLike | Blob | ArrayBufferView): void {
            const entry = log.find((candidate) => candidate.socket === this);
            if (entry && typeof data === "string") entry.sent.push(data);
            super.send(data);
          }
        }
        window.WebSocket = TrackedWebSocket as unknown as typeof WebSocket;
        (
          window as unknown as {
            __closeRuntimeSockets: (workspaceId: string) => void;
          }
        ).__closeRuntimeSockets = (workspaceId: string) => {
          for (const entry of log) {
            if (entry.url.includes(`/workspaces/${workspaceId}/runtime/sessions/`)) {
              entry.socket.close();
            }
          }
        };
      });

      isolatedServer = await startIsolatedWorkspaceE2EServer();
      api = await playwrightRequest.newContext({ baseURL: isolatedServer.info.base_url });
      const workspace = await createIssueWorkspace(api, 10);
      const output = observeTerminalOutput(page);

      const runtimeSockets = () =>
        page.evaluate((workspaceId) => {
          const log =
            (
              window as unknown as {
                __reconnectWsLog?: Array<{ url: string; closed: boolean; sent: string[] }>;
              }
            ).__reconnectWsLog ?? [];
          return log
            .filter((entry) => entry.url.includes(`/workspaces/${workspaceId}/runtime/sessions/`))
            .map(({ url, closed, sent }) => ({ url, closed, sent }));
        }, workspace.id);

      await page.goto(`${isolatedServer.info.base_url}/terminal/${workspace.id}`);
      const container = await openTerminalPanel(page);
      const disableSignal = path.join(workspace.worktree_path, "same-renderer-disable-signal");
      const replayReady = path.join(workspace.worktree_path, "same-renderer-replay-ready");
      const helperDone = path.join(workspace.worktree_path, "same-renderer-helper-done");
      helperFinishPath = path.join(workspace.worktree_path, "same-renderer-helper-finish");
      const helperPath = path.join(workspace.worktree_path, "same-renderer-helper.sh");
      const offlineOutputMarker = "same-renderer-offline-output-complete";
      const canonicalReconnectReset = new RegExp(`${ESCAPE}\\[\\?1;1000;(?:[0-9]+;)*1006l`);
      writeFileSync(
        helperPath,
        `${[
          `printf '${tmuxPassthroughFormat("\\033\\033[?1h")}'`,
          `while [ ! -f '${disableSignal}' ]; do sleep 0.05; done`,
          `printf '${tmuxPassthroughFormat("\\033\\033[?1l")}'`,
          "yes '0123456789abcdef0123456789abcdef' | head -n 2500",
          `printf '\\r\\n${offlineOutputMarker}\\r\\n'`,
          `touch '${replayReady}'`,
          `while [ ! -f '${helperFinishPath}' ]; do sleep 0.05; done`,
          `touch '${helperDone}'`,
        ].join("\n")}\n`,
        "utf8",
      );
      const tmuxSession = await runningRuntimeTmuxSession(api, workspace.id);
      runE2ETmuxCommand(isolatedServer, ["set-option", "-p", "-t", tmuxSession, "allow-passthrough", "on"]);
      await typeIntoTerminal(page, container, `sh '${helperPath}'`);
      runE2ETmuxCommand(isolatedServer, ["set-option", "-g", "-t", tmuxSession, "mouse", "on"]);
      await expect(container.locator(".xterm.enable-mouse-events")).toBeVisible();
      const applicationCursorUp = "\x1bOA";
      const normalCursorUp = "\x1b[A";
      const applicationCursorUpBefore = output.inputOccurrences(applicationCursorUp);
      await container.locator(".xterm-helper-textarea").focus();
      await page.keyboard.press("ArrowUp");
      await expect.poll(() => output.inputOccurrences(applicationCursorUp)).toBeGreaterThan(applicationCursorUpBefore);
      await container.evaluate((element) => {
        element.setAttribute("data-same-renderer-reconnect", "witness");
      });
      await expect.poll(async () => (await runtimeSockets()).length).toBe(1);
      const runtimeSocketURL = (await runtimeSockets())[0]!.url;

      await page.context().setOffline(true);
      await page.evaluate((workspaceId) => {
        (
          window as unknown as {
            __closeRuntimeSockets?: (id: string) => void;
          }
        ).__closeRuntimeSockets?.(workspaceId);
      }, workspace.id);
      await expect.poll(async () => (await runtimeSockets())[0]?.closed).toBe(true);

      runE2ETmuxCommand(isolatedServer, ["set-option", "-g", "-t", tmuxSession, "mouse", "off"]);
      writeFileSync(disableSignal, "disable\n", "utf8");
      await expect.poll(() => existsSync(replayReady), { timeout: 15_000 }).toBe(true);
      const replay = await readWebSocketUntil(runtimeSocketURL, offlineOutputMarker);
      expect(replay).toContain(offlineOutputMarker);
      expect(replay).toMatch(canonicalReconnectReset);

      await page.context().setOffline(false);
      await expect.poll(async () => (await runtimeSockets()).length, { timeout: 15_000 }).toBeGreaterThan(1);
      await expect
        .poll(() => output.reconnectedSessionModes(workspace.id).some((mode) => canonicalReconnectReset.test(mode)), {
          timeout: 15_000,
        })
        .toBe(true);
      const reconnectSocket = (await runtimeSockets()).at(-1);
      expect(reconnectSocket).toBeDefined();
      expect(new URL(reconnectSocket!.url).searchParams.has("cols")).toBe(false);
      expect(new URL(reconnectSocket!.url).searchParams.has("rows")).toBe(false);
      expect(reconnectSocket!.sent.filter((frame) => frame.includes('"type":"refresh"'))).toEqual([]);
      await expect(page.locator('[data-same-renderer-reconnect="witness"]')).toBeVisible();
      await expect(container.locator(".xterm.enable-mouse-events")).toHaveCount(0);
      const normalCursorUpBefore = output.inputOccurrences(normalCursorUp);
      await container.locator(".xterm-helper-textarea").focus();
      await page.keyboard.press("ArrowUp");
      await expect.poll(() => output.inputOccurrences(normalCursorUp)).toBeGreaterThan(normalCursorUpBefore);
      writeFileSync(helperFinishPath, "finish\n", "utf8");
      await expect.poll(() => existsSync(helperDone), { timeout: 15_000 }).toBe(true);
      await page.keyboard.press("Control+c");
      await typeMarkerCommand(page, container, workspace.worktree_path, "same-renderer-reconnect-input");
    } finally {
      await page.context().setOffline(false);
      if (helperFinishPath !== null) {
        writeFileSync(helperFinishPath, "finish\n", "utf8");
      }
      await api?.dispose();
      await isolatedServer?.stop();
    }
  });

  test("same renderer reconnect completes replayed UTF-8 before later output", async ({ page }) => {
    test.skip(
      !hasCommand("git") || !hasCommand("tmux", ["-V"]),
      "git and tmux are required for the real workspace flow",
    );

    await page.addInitScript(() => {
      const runtimeConnections = new Map<string, number>();
      const sockets: Array<{ url: string; closed: boolean; socket: WebSocket }> = [];
      (
        window as unknown as {
          __splitReplaySockets: typeof sockets;
        }
      ).__splitReplaySockets = sockets;
      const Native = window.WebSocket;
      class SplitReplayWebSocket extends Native {
        constructor(url: string | URL, protocols?: string | string[]) {
          super(url, protocols);
          const socketURL = String(url);
          if (!socketURL.includes("/runtime/sessions/")) return;

          const connectionNumber = runtimeConnections.get(socketURL.split("?")[0]!) ?? 0;
          runtimeConnections.set(socketURL.split("?")[0]!, connectionNumber + 1);
          const entry = { url: socketURL, closed: false, socket: this };
          sockets.push(entry);
          this.addEventListener("close", () => {
            entry.closed = true;
          });
          if (connectionNumber === 0) return;

          let replayPrefixDelivered = false;
          let continuationDelivered = false;
          let fallbackTimer: number | null = null;
          const deliver = (bytes: Uint8Array) => {
            this.onmessage?.(new MessageEvent("message", { data: bytes.buffer }));
          };
          const deliverContinuation = () => {
            if (continuationDelivered) return;
            continuationDelivered = true;
            if (fallbackTimer !== null) window.clearTimeout(fallbackTimer);
            deliver(new Uint8Array([0x98, 0x83, 0x41, 0x1b, 0x5b, 0x36, 0x6e]));
          };
          this.addEventListener("message", (event) => {
            if (!(event.data instanceof ArrayBuffer)) return;
            if (!replayPrefixDelivered) {
              event.stopImmediatePropagation();
              replayPrefixDelivered = true;
              const text = new TextEncoder().encode("\x1b[?1049h\r\x1b[2Ksame-renderer-split-utf8:");
              const prefix = new Uint8Array(text.length + 1);
              prefix.set(text);
              prefix[text.length] = 0xe2;
              deliver(prefix);
              fallbackTimer = window.setTimeout(deliverContinuation, 500);
              return;
            }
            if (!continuationDelivered) queueMicrotask(deliverContinuation);
          });
        }
      }
      window.WebSocket = SplitReplayWebSocket as unknown as typeof WebSocket;
      (
        window as unknown as {
          __closeSplitReplaySockets: (workspaceId: string) => void;
        }
      ).__closeSplitReplaySockets = (workspaceId: string) => {
        for (const entry of sockets) {
          if (entry.url.includes(`/workspaces/${workspaceId}/runtime/sessions/`)) {
            entry.socket.close();
          }
        }
      };
    });

    let isolatedServer: IsolatedE2EServer | null = null;
    let api: APIRequestContext | null = null;
    try {
      isolatedServer = await startIsolatedWorkspaceE2EServer();
      api = await playwrightRequest.newContext({ baseURL: isolatedServer.info.base_url });
      const workspace = await createIssueWorkspace(api, 10);
      const output = observeTerminalOutput(page);
      const runtimeSockets = () =>
        page.evaluate((workspaceId) => {
          const sockets =
            (
              window as unknown as {
                __splitReplaySockets?: Array<{ url: string; closed: boolean; socket: WebSocket }>;
              }
            ).__splitReplaySockets ?? [];
          return sockets
            .filter((entry) => entry.url.includes(`/workspaces/${workspaceId}/runtime/sessions/`))
            .map(({ url, closed, socket }) => ({ url, closed, readyState: socket.readyState }));
        }, workspace.id);

      await page.goto(`${isolatedServer.info.base_url}/terminal/${workspace.id}`);
      const container = await openTerminalPanel(page);
      await expect.poll(async () => (await runtimeSockets()).length).toBe(1);
      await container.evaluate((element) => {
        element.setAttribute("data-split-replay-renderer", "witness");
      });
      const cursorReportsBefore = output.cursorReports().length;

      await page.context().setOffline(true);
      await page.evaluate((workspaceId) => {
        (
          window as unknown as {
            __closeSplitReplaySockets?: (id: string) => void;
          }
        ).__closeSplitReplaySockets?.(workspaceId);
      }, workspace.id);
      await expect.poll(async () => (await runtimeSockets())[0]?.closed).toBe(true);

      await page.context().setOffline(false);
      await expect
        .poll(async () => (await runtimeSockets()).filter((socket) => socket.readyState === WebSocket.OPEN).length, {
          timeout: 15_000,
        })
        .toBe(1);
      await expect(page.locator('[data-split-replay-renderer="witness"]')).toBeVisible();
      const reconnectedSocket = (await runtimeSockets()).at(-1);
      expect(reconnectedSocket).toBeDefined();

      await expect.poll(() => output.cursorReports().length, { timeout: 15_000 }).toBeGreaterThan(cursorReportsBefore);
      const cursorColumn = /^\x1b\[\??\d+;(\d+)R$/.exec(output.cursorReports().at(-1) ?? "")?.[1];
      expect(cursorColumn).toBe("28");
      expect(new URL(reconnectedSocket!.url).searchParams.has("cols")).toBe(false);
      expect(new URL(reconnectedSocket!.url).searchParams.has("rows")).toBe(false);
    } finally {
      await page.context().setOffline(false);
      await api?.dispose();
      await isolatedServer?.stop();
    }
  });

  test("switching workspaces closes the previous workspace's live attachment", async ({ page }) => {
    // The pool outlives the view, and a parked terminal keeps its websocket, so
    // nothing hands one back on its own: browsing workspaces would leave a live
    // tmux attachment per workspace visited. The view's release is the only
    // thing that closes this one.
    //
    // Registry-level unit coverage cannot see a real socket, and an earlier
    // attempt at this test navigated with page.goto — which closes every socket
    // on its own and passed with the release removed. This switch is
    // client-side, so the socket's fate is the behavior under test.
    test.skip(
      !hasCommand("git") || !hasCommand("tmux", ["-V"]),
      "git and tmux are required for the real workspace flow",
    );

    let isolatedServer: IsolatedE2EServer | null = null;
    let api: APIRequestContext | null = null;
    try {
      isolatedServer = await startIsolatedWorkspaceE2EServer();
      api = await playwrightRequest.newContext({ baseURL: isolatedServer.info.base_url });
      const workspaceA = await createIssueWorkspace(api, 10);
      const workspaceB = await createIssueWorkspace(api, 11);

      // Record every session websocket the app opens and whether it has closed.
      // The real connection is untouched; only its lifecycle is observed.
      await page.addInitScript(() => {
        const log: Array<{ url: string; closed: boolean }> = [];
        (window as unknown as { __wsLog: typeof log }).__wsLog = log;
        const Native = window.WebSocket;
        class TrackedWebSocket extends Native {
          constructor(url: string | URL, protocols?: string | string[]) {
            super(url, protocols);
            const entry = { url: String(url), closed: false };
            log.push(entry);
            this.addEventListener("close", () => {
              entry.closed = true;
            });
          }
        }
        window.WebSocket = TrackedWebSocket as unknown as typeof WebSocket;
      });

      const sessionSockets = (workspaceId: string) =>
        page.evaluate((needle) => {
          const log = (window as unknown as { __wsLog?: Array<{ url: string; closed: boolean }> }).__wsLog ?? [];
          return log.filter((entry) => entry.url.includes(needle));
        }, `/workspaces/${workspaceId}/runtime/sessions/`);

      await page.goto(`${isolatedServer.info.base_url}/terminal/${workspaceA.id}`);
      const containerA = await openTerminalPanel(page);
      // Attached, not merely constructed: only a live socket can create this.
      await typeMarkerCommand(page, containerA, workspaceA.worktree_path, "release-marker-a");
      expect(await sessionSockets(workspaceA.id)).toHaveLength(1);

      // Client-side switch through the workspace list, so nothing but the view's
      // own release can close A's socket.
      await page.locator(".workspace-list-sidebar .ws-row", { hasText: DARK_MODE_ISSUE_TITLE }).click();
      await expect(page).toHaveURL(new RegExp(`/terminal/${workspaceB.id}$`));
      await expect
        .poll(async () => (await sessionSockets(workspaceA.id)).every((entry) => entry.closed), { timeout: 15_000 })
        .toBe(true);

      // And the workspace switched to is fully live, not collateral damage.
      const containerB = await openTerminalPanel(page);
      await typeMarkerCommand(page, containerB, workspaceB.worktree_path, "release-marker-b");
    } finally {
      await api?.dispose();
      await isolatedServer?.stop();
    }
  });
});
