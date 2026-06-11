// The 00- filename prefix schedules this long-running spec first:
// Playwright dispatches files in path order, and multi-second tests
// that start near the end of the run stretch the suite tail.

import { execFileSync } from "node:child_process";
import { writeFile } from "node:fs/promises";
import { join } from "node:path";
import { expect, request as playwrightRequest, test, type APIRequestContext } from "@playwright/test";
import { startIsolatedWorkspaceE2EServer, type IsolatedE2EServer } from "./support/e2eServer";

type WorkspaceStatusResponse = {
  id: string;
  status: string;
  worktree_path?: string;
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
    if (workspace.status === "ready") {
      return workspace;
    }
    if (workspace.status === "error") {
      throw new Error(`workspace ${workspaceId} failed to become ready`);
    }
    await new Promise((resolve) => setTimeout(resolve, 100));
  }
  throw new Error(`workspace ${workspaceId} did not become ready`);
}

test.describe("workspace force-delete", () => {
  test.describe.configure({
    mode: "serial",
    timeout: lockedWorkspaceTestTimeoutMs,
  });

  test("dirty workspace triggers the 409 prompt and force delete cleans up via the real API", async ({ page }) => {
    test.skip(
      !hasCommand("git") || !hasCommand("tmux", ["-V"]),
      "git and tmux are required for the real workspace flow",
    );

    let isolatedServer: IsolatedE2EServer | null = null;
    let api: APIRequestContext | null = null;
    try {
      isolatedServer = await startIsolatedWorkspaceE2EServer();
      api = await playwrightRequest.newContext({
        baseURL: isolatedServer.info.base_url,
      });

      // Seeded issue 13 has no other coverage and gives us an isolated
      // workspace with a fresh worktree we can dirty.
      const createResponse = await api.post("/api/v1/issues/github/acme/widgets/13/workspace", {
        data: {},
      });
      expect(createResponse.status()).toBe(202);
      const created = (await createResponse.json()) as WorkspaceStatusResponse;
      const ready = await waitForWorkspaceReady(api, created.id);
      expect(ready.worktree_path).toBeTruthy();

      // Drop an uncommitted file so the first DELETE hits the dirty
      // preflight on the workspace manager and returns 409.
      await writeFile(join(ready.worktree_path!, "demo-dirty.txt"), "uncommitted\n");

      await page.goto(`${isolatedServer.info.base_url}/terminal/${created.id}`);

      await page.locator(".header-bar").getByRole("button", { name: "Delete" }).click();

      const dialog = page.getByRole("dialog", {
        name: "Force delete workspace?",
      });
      await expect(dialog).toBeVisible();
      // The server's 409 detail names the dirty file — verifying it
      // reached the UI catches the whole detail-pass-through chain.
      await expect(dialog).toContainText("demo-dirty.txt");

      await dialog.getByRole("button", { name: "Force delete" }).click();

      await expect(page).toHaveURL(/\/workspaces$/);

      // The forced DELETE actually removed the workspace from the DB.
      const after = await api.get(`/api/v1/workspaces/${created.id}`);
      expect(after.status()).toBe(404);
    } finally {
      await api?.dispose();
      await isolatedServer?.stop();
    }
  });
});
