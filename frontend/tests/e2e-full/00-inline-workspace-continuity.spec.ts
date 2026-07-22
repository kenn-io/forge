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
// takes) cannot carry the tag forward. A screenshot hash of the tagged
// element taken before and after typing corroborates that the session
// behind it is still live, not just visually frozen — this reads the
// browser's actual composited output rather than the terminal's backing
// canvas, since xterm.js's WebGL renderer keeps its own GL context on that
// canvas (a second, `getContext("2d")`-based readback is not possible on
// the same element) and ghostty-web's canvas readback proved unreliable to
// synchronize with this harness's headless GPU path.

import { createHash } from "node:crypto";
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
  error_message?: string | null;
};

const lockedWorkspaceTestTimeoutMs = 120_000;
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

async function waitForWorkspaceReady(api: APIRequestContext, workspaceId: string): Promise<void> {
  for (let attempt = 0; attempt < 100; attempt += 1) {
    const response = await api.get(`/api/v1/workspaces/${workspaceId}`);
    expect(response.ok()).toBe(true);
    const workspace = (await response.json()) as WorkspaceStatusResponse;
    if (workspace.status === "ready") return;
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
  await waitForWorkspaceReady(api, workspace.id);
  return workspace;
}

async function openTerminalPanel(page: Page): Promise<Locator> {
  await page.getByRole("button", { name: "Open terminal panel" }).click();
  const container = page.locator(".terminal-panel.open .terminal-container");
  await expect(container).toBeVisible();
  await expect(container.locator("canvas").first()).toBeVisible();
  return container;
}

async function typeIntoTerminal(page: Page, container: Locator, command: string): Promise<void> {
  await container.click({ position: { x: 10, y: 10 } });
  await page.keyboard.type(command);
  await page.keyboard.press("Enter");
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

async function screenshotHash(container: Locator): Promise<string> {
  const buffer = await container.screenshot();
  return createHash("sha256").update(buffer).digest("hex");
}

async function expectDistinctPaint(container: Locator, baselineHash: string): Promise<void> {
  await expect.poll(async () => (await screenshotHash(container)) !== baselineHash, { timeout: 10_000 }).toBe(true);
}

test.describe("inline workspace dock continuity", () => {
  test.describe.configure({ mode: "serial", timeout: lockedWorkspaceTestTimeoutMs });

  test("tab flip preserves the live terminal (xterm)", async ({ page }) => {
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
      const tabContainer = await openTerminalPanel(page);
      const blank = await screenshotHash(tabContainer);
      await typeIntoTerminal(page, tabContainer, "printf 'CONTINUITY_MARKER_ONE'");
      await expectDistinctPaint(tabContainer, blank);

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
      const afterReparent = await screenshotHash(dockContainer);

      await typeIntoTerminal(page, dockContainer, "printf 'CONTINUITY_MARKER_TWO'");
      await expectDistinctPaint(dockContainer, afterReparent);

      // Flip back to the Workspaces tab: route memory returns to
      // /terminal/{id} — the same hostedWorkspaceKey as the dock claim, so
      // this is another reparent, not a reconnect.
      await selectTopBarTab(page, "Workspaces");
      await expect(page).toHaveURL(new RegExp(`/terminal/${workspace.id}$`));

      const backInTab = page.locator(".workspace-tab-slot .terminal-container");
      await expect(witness).toBeVisible();
      await expect(backInTab).toHaveAttribute("data-continuity", "witness");
      // Still showing accumulated scrollback, not reset to the pristine
      // blank pane a reconnect would produce (the tmux status bar's clock
      // and the cursor blink mean an exact pixel match across time isn't a
      // safe assertion, so this checks "still has content" rather than
      // "identical frame").
      expect(await screenshotHash(backInTab)).not.toBe(blank);
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
      isolatedServer = await startIsolatedWorkspaceE2EServer();
      api = await playwrightRequest.newContext({ baseURL: isolatedServer.info.base_url });

      await switchRendererToGhosttyViaSettings(page, isolatedServer.info.base_url);

      const workspace = await createIssueWorkspace(api, 10);

      await page.goto(`${isolatedServer.info.base_url}/terminal/${workspace.id}`);
      const tabContainer = await openTerminalPanel(page);
      await expect(tabContainer.locator("canvas")).toHaveCount(1);
      const blank = await screenshotHash(tabContainer);
      await typeIntoTerminal(page, tabContainer, "printf 'CONTINUITY_MARKER_ONE'");
      await expectDistinctPaint(tabContainer, blank);

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
      const afterReparent = await screenshotHash(dockContainer);

      await typeIntoTerminal(page, dockContainer, "printf 'CONTINUITY_MARKER_TWO'");
      await expectDistinctPaint(dockContainer, afterReparent);

      // Flip back to the Workspaces tab: same hostedWorkspaceKey, so the
      // tagged container (and the canvas inside it) must still be the one
      // that reappears in the tab slot, with no reconnect repaint.
      await selectTopBarTab(page, "Workspaces");
      await expect(page).toHaveURL(new RegExp(`/terminal/${workspace.id}$`));
      const backInTab = page.locator(".workspace-tab-slot .terminal-container");
      await expect(witness).toBeVisible();
      await expect(backInTab).toHaveAttribute("data-continuity", "witness");
      // Still showing accumulated scrollback rather than the pristine blank
      // pane a reconnect would produce.
      expect(await screenshotHash(backInTab)).not.toBe(blank);
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
