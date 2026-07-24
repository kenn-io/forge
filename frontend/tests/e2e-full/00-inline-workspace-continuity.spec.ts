// The 00- filename prefix schedules this long-running spec first: Playwright
// dispatches files in path order, and multi-second tests that start near the
// end of the run stretch the suite tail.
//
// These specs prove the inline workspace dock's core claim: the single
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
// WebGL renderer owns the canvas GL context, and ghostty-web's readback
// proved unreliable under this harness's headless GPU path).

import { existsSync, readFileSync } from "node:fs";
import path from "node:path";
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
import { openSettingsPanel } from "./support/settingsPanel";

type WorkspaceStatusResponse = {
  id: string;
  status: string;
  worktree_path: string;
  error_message?: string | null;
};

type TerminalGeometryFrame = {
  cols: number;
  rows: number;
};

const lockedWorkspaceTestTimeoutMs = 120_000;
const ZOOMED_TERMINAL_FONT_SIZE = 16;
const SAFARI_ISSUE_TITLE = "Widget rendering broken on Safari";
const DARK_MODE_ISSUE_TITLE = "Add dark mode support";

function hasCommand(command: string, args: string[] = ["--version"]): boolean {
  try {
    execFileSync(command, args, { stdio: "ignore" });
    return true;
  } catch {
    return false;
  }
}

function observeTerminalRefreshFrames(page: Page): TerminalGeometryFrame[] {
  const frames: TerminalGeometryFrame[] = [];
  page.on("websocket", (socket) => {
    socket.on("framesent", ({ payload }) => {
      if (typeof payload !== "string") return;
      try {
        const message = JSON.parse(payload) as {
          type?: string;
          cols?: number;
          rows?: number;
        };
        if (message.type !== "refresh" || message.cols === undefined || message.rows === undefined) return;
        frames.push({ cols: message.cols, rows: message.rows });
      } catch {
        // Terminal input uses binary frames; ignore non-control payloads.
      }
    });
  });
  return frames;
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

async function openTerminalPanel(page: Page): Promise<Locator> {
  await page.getByRole("button", { name: "Open terminal panel" }).click();
  const container = page.locator(".terminal-panel.open .terminal-container");
  await expect(container).toBeVisible();
  // Renderer-agnostic readiness: ghostty-web always paints a canvas, but
  // xterm.js only does when its WebGL addon activates — without WebGL
  // (headless Firefox) it silently falls back to the DOM renderer, which
  // renders .xterm-screen rows and never creates a canvas.
  await expect(container.locator("canvas, .xterm-screen").first()).toBeVisible();
  return container;
}

async function typeIntoTerminal(page: Page, container: Locator, command: string): Promise<void> {
  await container.click({ position: { x: 10, y: 10 } });
  await page.keyboard.type(command);
  await page.keyboard.press("Enter");
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

async function switchRendererToGhosttyViaSettings(page: Page, baseURL: string): Promise<void> {
  await page.goto(`${baseURL}/settings`);
  await openSettingsPanel(page, "Terminal");
  const renderer = page.getByRole("combobox", { name: "Terminal renderer: xterm.js" });
  await renderer.click();
  await page.getByRole("option", { name: "ghostty-web" }).click();
  const saveResponsePromise = page.waitForResponse(
    (response) => response.url().endsWith("/api/v1/settings") && response.request().method() === "PUT",
  );
  await page.getByRole("button", { name: "Save", exact: true }).click();
  expect((await saveResponsePromise).ok()).toBe(true);
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

test.describe("inline workspace dock continuity", () => {
  test.describe.configure({ mode: "serial", timeout: lockedWorkspaceTestTimeoutMs });

  test("expanding an inline workspace keeps the app frame fixed and its controls reachable", async ({ page }) => {
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
      const appMain = page.locator(".app-main");
      const itemRail = page.locator(".issue-list");
      const expand = page.getByRole("button", { name: "Expand Terminal" });
      await expect(expand).toBeVisible();
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

      await expand.click();

      const collapse = page.getByRole("button", { name: "Collapse Terminal" });
      const showDetails = page.getByRole("button", { name: "Show Details" });
      await expect(showDetails).toBeVisible();
      await expect(collapse).toBeVisible();
      await showDetails.click();
      await expect(expand).toBeVisible();
      await expand.click();
      await expect(showDetails).toBeVisible();
      await expect(appMain).toHaveJSProperty("scrollTop", 0);
      await expect
        .poll(async () => {
          const [mainBox, railBox, panelBox] = await Promise.all([
            appMain.boundingBox(),
            itemRail.boundingBox(),
            page.locator(".workspace-dock-panel").boundingBox(),
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

      await collapse.click();
      await expect(page.locator(".workspace-dock-slot")).toHaveCount(0);
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
      const refreshFrames = observeTerminalRefreshFrames(page);
      isolatedServer = await startIsolatedWorkspaceE2EServer();
      api = await playwrightRequest.newContext({ baseURL: isolatedServer.info.base_url });

      const workspace = await createIssueWorkspace(api, 10);

      if (browserName === "chromium") {
        await page.addInitScript(() => {
          const trackedWindow = window as Window & {
            __middlemanGenerateMipmapCalls?: number;
          };
          trackedWindow.__middlemanGenerateMipmapCalls = 0;
          if (typeof WebGL2RenderingContext === "undefined") return;
          const original = Object.getOwnPropertyDescriptor(WebGL2RenderingContext.prototype, "generateMipmap")
            ?.value as WebGL2RenderingContext["generateMipmap"];
          WebGL2RenderingContext.prototype.generateMipmap = function (target: number): void {
            trackedWindow.__middlemanGenerateMipmapCalls = (trackedWindow.__middlemanGenerateMipmapCalls ?? 0) + 1;
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
      const refreshCountBeforeZoom = refreshFrames.length;
      for (let fontSize = 13; fontSize <= ZOOMED_TERMINAL_FONT_SIZE; fontSize += 1) {
        await page.getByRole("button", { name: "Increase terminal font size" }).click();
        await expect(resetZoom).toHaveText(`${fontSize}px`);
      }
      await expectPersistedTerminalFontSize(api, ZOOMED_TERMINAL_FONT_SIZE);
      await expect.poll(() => refreshFrames.length).toBeGreaterThan(refreshCountBeforeZoom);
      await expect.poll(() => refreshFrames.at(-1)?.cols).toBeLessThan(beforeZoom.cols);
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
                    __middlemanGenerateMipmapCalls?: number;
                  }
                ).__middlemanGenerateMipmapCalls ?? 0,
            ),
          )
          .toBe(0);
      }

      await tabContainer.evaluate((el) => {
        el.setAttribute("data-continuity", "witness");
      });

      // Select the issue that owns this workspace: its detail carries a
      // ready workspace ref, so the inline claim fires and the dock takes
      // the shared host — same hostedWorkspaceKey, so this must reparent
      // the exact tagged node rather than recreate it.
      await selectIssueByTitle(page, SAFARI_ISSUE_TITLE);

      const witness = page.locator('[data-continuity="witness"]');
      await expect(witness).toBeVisible();
      const dockContainer = page.locator(".workspace-dock-panel .workspace-dock-slot .terminal-container");
      await expect(dockContainer).toHaveAttribute("data-continuity", "witness");
      await typeMarkerCommand(page, dockContainer, workspace.worktree_path, "continuity-marker-two");

      // Flip back to the Workspaces tab: route memory returns to
      // /terminal/{id} — the same hostedWorkspaceKey as the dock claim, so
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

  test("tab flip preserves the live terminal (ghostty)", async ({ page }) => {
    test.skip(
      !hasCommand("git") || !hasCommand("tmux", ["-V"]),
      "git and tmux are required for the real workspace flow",
    );

    let isolatedServer: IsolatedE2EServer | null = null;
    let api: APIRequestContext | null = null;
    try {
      const refreshFrames = observeTerminalRefreshFrames(page);
      isolatedServer = await startIsolatedWorkspaceE2EServer();
      api = await playwrightRequest.newContext({ baseURL: isolatedServer.info.base_url });

      await switchRendererToGhosttyViaSettings(page, isolatedServer.info.base_url);

      const workspace = await createIssueWorkspace(api, 10);

      await page.goto(`${isolatedServer.info.base_url}/terminal/${workspace.id}`);
      const tabContainer = await openTerminalPanel(page);
      await expect(tabContainer.locator("canvas")).toHaveCount(1);
      await typeMarkerCommand(page, tabContainer, workspace.worktree_path, "continuity-marker-one");
      const beforeZoom = await readPtyGeometry(
        page,
        tabContainer,
        workspace.worktree_path,
        "ghostty-geometry-before-zoom",
      );
      const resetZoom = page.getByRole("button", {
        name: "Reset terminal font size",
      });
      await expect(resetZoom).toHaveText("12px");
      const refreshCountBeforeZoom = refreshFrames.length;
      for (let fontSize = 13; fontSize <= ZOOMED_TERMINAL_FONT_SIZE; fontSize += 1) {
        await page.getByRole("button", { name: "Increase terminal font size" }).click();
        await expect(resetZoom).toHaveText(`${fontSize}px`);
      }
      await expectPersistedTerminalFontSize(api, ZOOMED_TERMINAL_FONT_SIZE);
      await expect.poll(() => refreshFrames.length).toBeGreaterThan(refreshCountBeforeZoom);
      await expect.poll(() => refreshFrames.at(-1)?.cols).toBeLessThan(beforeZoom.cols);
      await waitForPtyColumnsBelow(
        page,
        tabContainer,
        workspace.worktree_path,
        "ghostty-geometry-after-zoom",
        beforeZoom.cols,
      );

      await tabContainer.evaluate((el) => {
        el.setAttribute("data-continuity", "witness");
      });

      // Select the issue that owns this workspace: same reparent contract
      // as the xterm case, witnessed on the same canvas-backed container.
      await selectIssueByTitle(page, SAFARI_ISSUE_TITLE);

      const witness = page.locator('[data-continuity="witness"]');
      await expect(witness).toBeVisible();
      const dockContainer = page.locator(".workspace-dock-panel .workspace-dock-slot .terminal-container");
      await expect(dockContainer).toHaveAttribute("data-continuity", "witness");
      await typeMarkerCommand(page, dockContainer, workspace.worktree_path, "continuity-marker-two");
      await dockContainer.click({ position: { x: 10, y: 10 } });
      await page.keyboard.press("Control+=");
      await expect(resetZoom).toHaveText(`${ZOOMED_TERMINAL_FONT_SIZE + 1}px`);
      await expectPersistedTerminalFontSize(api, ZOOMED_TERMINAL_FONT_SIZE + 1);

      // Flip back to the Workspaces tab: same hostedWorkspaceKey, so the
      // tagged container (and the canvas inside it) must still be the one
      // that reappears in the tab slot, with no reconnect repaint.
      await selectTopBarTab(page, "Workspaces");
      await expect(page).toHaveURL(new RegExp(`/terminal/${workspace.id}$`));
      const backInTab = page.locator(".workspace-tab-slot .terminal-container");
      await expect(witness).toBeVisible();
      await expect(backInTab).toHaveAttribute("data-continuity", "witness");
      await expect(resetZoom).toHaveText(`${ZOOMED_TERMINAL_FONT_SIZE + 1}px`);
      // The same session must still accept input after the second
      // reparent — a torn-down or wedged terminal cannot run this.
      await typeMarkerCommand(page, backInTab, workspace.worktree_path, "continuity-marker-three");
      await page.reload();
      await expect(page.getByRole("button", { name: "Reset terminal font size" })).toHaveText(
        `${ZOOMED_TERMINAL_FONT_SIZE + 1}px`,
      );
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
      const dockContainer = await openTerminalPanel(page);
      await dockContainer.evaluate((el) => {
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
});
