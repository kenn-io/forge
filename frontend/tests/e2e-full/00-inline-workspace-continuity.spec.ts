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
import { runPaletteCommand } from "./support/paletteCommands";

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

async function openTerminalPanel(page: Page): Promise<Locator> {
  await page.getByRole("button", { name: "Open terminal panel" }).click();
  const container = page.locator(".terminal-panel.open .terminal-container");
  await expect(container).toBeVisible();
  // xterm.js only paints a canvas when its WebGL addon activates. Without WebGL
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
        localStorage.removeItem(`middleman-pane-layout-v1:${surface}`);
      }
    });
  });

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
      await dismissWorkspaceLauncher(page);
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

      await collapse.click();
      await expect(page.locator(".detail-pane-workspace-slot")).toHaveCount(0);
    } finally {
      await api?.dispose();
      await isolatedServer?.stop();
    }
  });

  test("the pane's own maximize and close controls keep the live terminal", async ({ page }) => {
    // The terminal toolbar's Expand/Collapse are covered above. These are the
    // pane-native controls that replaced the dock's chrome, and they drive the
    // same layout state through a different path, so they need their own proof
    // that the single hosted terminal is reparented rather than rebuilt.
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

      await page.getByRole("button", { name: "Show Workspace" }).click();
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

      // Home again, in the dock it came from rather than wherever normalization
      // would have put a session the layout had never seen.
      await expect(page.locator(".terminal-panel.open .terminal-container")).toBeVisible();
      await typeMarkerCommand(page, dockContainer, workspace.worktree_path, "promote-marker-redocked");
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
      const dockContainer = await openTerminalPanel(page);
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
      await page.getByRole("button", { name: "Collapse Terminal" }).click();
      await expect(page.locator(".detail-pane-workspace-slot")).toHaveCount(0);
      await expect(page.locator(".session-terminal-slot")).toHaveCount(0);

      // Collapsing both panes at once drops a whole split node out of the pane tree,
      // and the way back has to survive that. It restores what the collapse hid, so
      // the promoted terminal comes back with the container rather than being
      // stranded off screen behind a container that masks it - and it is still live.
      await page.getByRole("button", { name: "Focus Terminal" }).click();
      await expect(page.locator(".detail-pane-workspace-slot")).toBeVisible();
      await expect(restored).toBeVisible();
      await typeMarkerCommand(page, restored, workspace.worktree_path, "marker-after-collapse");
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

      // 1. Tabbed behind a sibling: drag the workspace into the conversation's
      //    leaf, then switch away from it. Neither hidden nor maximized.
      await workspaceLeaf
        .getByRole("tab", { name: "Workspace" })
        .dragTo(conversationLeaf.getByRole("tab", { name: "Conversation" }));
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
