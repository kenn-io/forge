import { execFileSync } from "node:child_process";
import { expect, test } from "@playwright/test";

import { startIsolatedE2EServer, type IsolatedE2EServer } from "./support/e2eServer";

type ConfiguredRepo = {
  provider: string;
  platform_host: string;
  owner: string;
  name: string;
  repo_path: string;
};

function hasCommand(command: string, args: string[] = ["--version"]): boolean {
  try {
    execFileSync(command, args, { stdio: "ignore" });
    return true;
  } catch {
    return false;
  }
}

test("first run persists a host-aware repository and reaches a ready PR workspace", async ({ page }) => {
  test.skip(!hasCommand("git") || !hasCommand("tmux", ["-V"]), "git and tmux are required for workspace setup");
  test.setTimeout(120_000);

  let server: IsolatedE2EServer | null = null;
  try {
    server = await startIsolatedE2EServer();
    const settingsResponse = await page.request.get(`${server.info.base_url}/api/v1/settings`);
    expect(settingsResponse.ok()).toBe(true);
    const settings = (await settingsResponse.json()) as { repos: ConfiguredRepo[] };
    for (const repo of settings.repos) {
      const remove = await page.request.delete(
        `${server.info.base_url}/api/v1/host/${encodeURIComponent(repo.platform_host)}` +
          `/repo/${encodeURIComponent(repo.provider)}/${encodeURIComponent(repo.owner)}/${encodeURIComponent(repo.name)}`,
        { headers: { "content-type": "application/json" } },
      );
      expect(remove.ok(), `failed to remove ${repo.provider}|${repo.platform_host}/${repo.repo_path}`).toBe(true);
    }

    await page.addInitScript(() => {
      if (sessionStorage.getItem("kenn-forge:e2e-onboarding-initialized") !== "1") {
        localStorage.removeItem("kenn-forge:first-run-onboarding");
        sessionStorage.removeItem("kenn-forge:first-run-onboarding");
        sessionStorage.setItem("kenn-forge:e2e-onboarding-initialized", "1");
      }
    });
    await page.route("**/api/v1/tooling-status", async (route) => {
      await route.fulfill({
        contentType: "application/json",
        body: JSON.stringify({
          git: { available: true, version: "2.50" },
          gh: { available: true, authenticated: true, host: "github.com", user: "maintainer" },
          glab: { available: false, authenticated: false, host: "gitlab.com" },
        }),
      });
    });
    await page.route("**/api/v1/platform/user-repositories**", async (route) => {
      const url = new URL(route.request().url());
      expect(url.searchParams.get("provider")).toBe("github");
      expect(url.searchParams.get("platform_host")).toBe("github.com");
      await route.fulfill({
        contentType: "application/json",
        body: JSON.stringify({
          repositories: [
            {
              name_with_owner: "acme/widgets",
              ssh_url: "git@github.com:acme/widgets.git",
              default_branch: "main",
            },
          ],
        }),
      });
    });

    await page.setViewportSize({ width: 700, height: 900 });
    await page.goto(`${server.info.base_url}/`);
    await expect(page.getByRole("heading", { name: "Choose the repositories you maintain" })).toBeVisible();
    await expect(page.getByRole("listitem", { name: "Choose repos: current" })).toBeVisible();
    expect(
      await page.evaluate(() => document.documentElement.scrollWidth <= document.documentElement.clientWidth),
    ).toBe(true);
    await page.getByRole("checkbox", { name: /acme\/widgets/ }).click();
    await page.getByRole("button", { name: "Configure 1 repository" }).click();

    await expect(page.getByRole("heading", { name: "Open a pull request" })).toBeVisible({ timeout: 30_000 });
    const persistedResponse = await page.request.get(`${server.info.base_url}/api/v1/settings`);
    expect(persistedResponse.ok()).toBe(true);
    const persisted = (await persistedResponse.json()) as { repos: ConfiguredRepo[] };
    expect(persisted.repos).toContainEqual(
      expect.objectContaining({
        provider: "github",
        platform_host: "github.com",
        repo_path: "acme/widgets",
      }),
    );

    await page.reload();
    await expect(page.getByRole("heading", { name: "Open a pull request" })).toBeVisible({ timeout: 30_000 });
    await expect(page.getByText("Add widget caching layer", { exact: true })).toBeVisible();
    await page.getByRole("button", { name: /Continue with PR/ }).click();
    await page.getByRole("button", { name: "Create workspace" }).click();

    await expect(page).toHaveURL(/\/terminal\/[^/]+$/, { timeout: 30_000 });
    const workspaceID = decodeURIComponent(new URL(page.url()).pathname.split("/").at(-1) ?? "");
    await expect
      .poll(
        async () => {
          const response = await page.request.get(`${server!.info.base_url}/api/v1/workspaces/${workspaceID}`);
          expect(response.ok()).toBe(true);
          const workspace = (await response.json()) as { status: string; error_message?: string | null };
          if (workspace.status === "error") {
            throw new Error(workspace.error_message ?? `workspace ${workspaceID} failed to become ready`);
          }
          return workspace.status;
        },
        { timeout: 30_000 },
      )
      .toBe("ready");
    await expect
      .poll(async () => {
        const response = await page.request.get(`${server!.info.base_url}/api/v1/workspaces`);
        expect(response.ok()).toBe(true);
        const body = (await response.json()) as { workspaces: Array<{ item_number?: number }> };
        return body.workspaces.some((workspace) => workspace.item_number === 1);
      })
      .toBe(true);
  } finally {
    await server?.stop();
  }
});
