// The 00- filename prefix schedules this long-running spec first: Playwright
// dispatches files in path order, and multi-second tests that start near the
// end of the run stretch the suite tail.
//
// An embedded workspace has no Home tab, so this overlay is the only route to a
// maintainer's first session. Only a real backend can prove the round trip: the
// mock lane can render the overlay and click a target, but a launch that never
// reaches tmux looks exactly the same there. The existing full-stack launch
// coverage (00-workspace-sidebar.spec.ts) only asserts that the empty-state
// example's buttons are disabled, which proves nothing about launching.

import { existsSync } from "node:fs";
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

type WorkspaceStatusResponse = {
  id: string;
  status: string;
  worktree_path: string;
  error_message?: string | null;
};

const lockedWorkspaceTestTimeoutMs = 120_000;

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

// Durable proof that the launched session's input path (DOM focus -> WebSocket ->
// tmux -> shell) is live: a rendered prompt can appear for a shell that never
// attached, and a marker file cannot.
async function typeMarkerCommand(page: Page, container: Locator, worktreePath: string, marker: string): Promise<void> {
  const markerPath = path.join(worktreePath, marker);
  await container.click({ position: { x: 10, y: 10 } });
  await page.keyboard.type(`touch '${markerPath}'`);
  await page.keyboard.press("Enter");
  await expect.poll(() => existsSync(markerPath), { timeout: 15_000 }).toBe(true);
}

test.describe("embedded workspace launcher", () => {
  test.describe.configure({ mode: "serial", timeout: lockedWorkspaceTestTimeoutMs });

  // The pane layout persists, so an arrangement left by another spec would decide
  // whether the workspace pane is even on screen here.
  test.beforeEach(async ({ page }) => {
    await page.addInitScript(() => {
      for (const surface of ["prs", "issues", "activity"]) {
        localStorage.removeItem(`middleman-pane-layout-v1:${surface}`);
      }
    });
  });

  test("launches a first session from the overlay and reopens it from the pane controls", async ({ page }) => {
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

      // A workspace running nothing would otherwise show an empty tab strip, and a
      // pane has no Home tab to fall back to.
      const launcher = page.getByRole("dialog", { name: "Launch a session" });
      await expect(launcher).toBeVisible();
      await launcher.getByRole("button", { name: "Shell" }).click();

      // The overlay stands down only once the refreshed runtime actually reports
      // the session, so its absence here is proof the launch landed.
      await expect(launcher).toBeHidden();
      const container = page.locator(".detail-pane-workspace-slot .terminal-container");
      await expect(container).toBeVisible();
      await typeMarkerCommand(page, container, workspace.worktree_path, "launcher-marker");

      // And it is reachable again afterwards: the workspace's controls moved into
      // the pane's own popover, which is the only launch affordance left in a pane.
      await page.getByRole("button", { name: "Workspace controls" }).first().click();
      const controls = page.getByRole("dialog", { name: "Workspace controls" });
      await expect(controls).toBeVisible();
      await controls.getByRole("button", { name: "Launch" }).click();
      await expect(launcher).toBeVisible();

      // Dismissed by hand this time, which must leave the live terminal behind it
      // untouched rather than reopening over it.
      await page.keyboard.press("Escape");
      await expect(launcher).toBeHidden();
      await typeMarkerCommand(page, container, workspace.worktree_path, "launcher-marker-after-dismiss");
    } finally {
      await api?.dispose();
      await isolatedServer?.stop();
    }
  });
});
