import { execFileSync } from "node:child_process";
import { mkdtempSync, realpathSync, rmSync } from "node:fs";
import os from "node:os";
import path from "node:path";
import { expect, test, type Page } from "@playwright/test";

import { startIsolatedE2EServer, startIsolatedE2EServerWithOptions } from "./support/e2eServer";

type ProjectResponse = {
  id: string;
  display_name: string;
  local_path?: string;
};

type ProjectListResponse = {
  projects: ProjectResponse[];
};

type SnapshotResponse = {
  hosts: Array<{
    configKey: string;
    kind: string;
    name: string;
  }>;
};

async function waitForPRList(page: Page): Promise<void> {
  await page.locator(".pull-item").first().waitFor({ state: "visible", timeout: 10_000 });
}

async function sidebarWidth(page: Page): Promise<number> {
  return Math.round(
    await page
      .locator(".kit-sidebar-layout__sidebar")
      .first()
      .evaluate((node) => node.getBoundingClientRect().width),
  );
}

test.describe("embedded config", () => {
  test("hides sync button when hideSync is true", async ({ page }) => {
    await page.addInitScript(() => {
      window.__middleman_config = { ui: { hideSync: true } };
    });
    await page.goto("/pulls");
    await waitForPRList(page);

    await expect(page.locator(".action-btn", { hasText: "Sync" })).not.toBeVisible();
  });

  test("hides repo selector when hideRepoSelector is true", async ({ page }) => {
    await page.addInitScript(() => {
      window.__middleman_config = { ui: { hideRepoSelector: true } };
    });
    await page.goto("/pulls");
    await waitForPRList(page);

    await expect(page.locator(".typeahead")).not.toBeAttached();
  });

  test("hides star button when hideStar is true", async ({ page }) => {
    await page.addInitScript(() => {
      window.__middleman_config = { ui: { hideStar: true } };
    });
    await page.goto("/pulls");
    await waitForPRList(page);

    // Open a PR detail.
    await page.locator(".pull-item").first().click();
    await page.locator(".pull-detail").waitFor({ state: "visible", timeout: 10_000 });

    await expect(page.locator(".pull-detail .star-btn")).not.toBeAttached();
  });

  test("hides theme toggle when theme.mode is set", async ({ page }) => {
    await page.addInitScript(() => {
      window.__middleman_config = { theme: { mode: "dark" } };
    });
    await page.goto("/pulls");
    await waitForPRList(page);

    await expect(page.locator("button[title='Toggle theme']")).not.toBeAttached();
  });

  test("host sidebarWidth overrides persisted width on pulls", async ({ page }) => {
    await page.addInitScript(() => {
      localStorage.setItem("middleman-sidebar-width", "520");
      window.__middleman_config = { embed: { sidebarWidth: 410 } };
    });
    await page.goto("/pulls");
    await waitForPRList(page);

    await expect.poll(async () => sidebarWidth(page)).toBe(410);

    await page.reload();
    await waitForPRList(page);

    await expect.poll(async () => sidebarWidth(page)).toBe(410);
  });

  test("settings page is blocked in embedded mode", async ({ page }) => {
    await page.addInitScript(() => {
      window.__middleman_config = { embed: {} };
    });
    await page.goto("/settings");

    // When embedded, /settings is not a valid route and falls
    // through to the activity page. The URL may still say /settings
    // but the activity feed should render instead.
    await page.locator(".activity-feed").waitFor({ state: "visible", timeout: 10_000 });
    await expect(page.locator(".settings-page")).not.toBeAttached();
  });

  test("daemon ui-only config does not block standalone settings", async ({ page }) => {
    // The daemon serves window.__middleman_config carrying only its
    // UI focus state (ui.activeWorktreeKey, set via the API). That
    // must not flip the SPA into embedded mode and hide the settings
    // page, which a standalone client needs.
    await page.addInitScript(() => {
      window.__middleman_config = { ui: { activeWorktreeKey: "wt-1" } };
    });
    await page.goto("/settings");

    await page.locator(".settings-page").waitFor({ state: "visible", timeout: 10_000 });
  });

  test("project intake uses snapshot host metadata and host-scoped registration", async ({ page }) => {
    const server = await startIsolatedE2EServerWithOptions({ fleetKey: "hub" });
    const localRepo = realpathSync(mkdtempSync(path.join(os.tmpdir(), "middleman-hosted-intake-")));
    try {
      execFileSync("git", ["init", "-b", "main"], { cwd: localRepo, stdio: "ignore" });
      execFileSync("git", ["config", "user.email", "e2e@example.com"], {
        cwd: localRepo,
        stdio: "ignore",
      });
      execFileSync("git", ["config", "user.name", "E2E Fixture"], {
        cwd: localRepo,
        stdio: "ignore",
      });
      execFileSync("git", ["commit", "--allow-empty", "-m", "fixture: seed project"], {
        cwd: localRepo,
        stdio: "ignore",
      });

      const snapshotResponse = await page.request.get(`${server.info.base_url}/api/v1/snapshot?include_peers=true`);
      expect(snapshotResponse.status(), await snapshotResponse.text()).toBe(200);
      const snapshot = (await snapshotResponse.json()) as SnapshotResponse;
      const hubHost = snapshot.hosts.find((host) => host.configKey === "hub");
      expect(hubHost).toBeDefined();
      expect(hubHost?.kind).toBe("self");

      const snapshotLoaded = page.waitForResponse((response) => {
        const url = new URL(response.url());
        return url.pathname === "/api/v1/snapshot" && url.searchParams.get("include_peers") === "true";
      });
      await page.goto(`${server.info.base_url}/project-intake?host=hub`);
      await snapshotLoaded;
      await expect(page.getByText(`Host: ${hubHost?.name ?? "hub"}`)).toBeVisible();

      await page.getByRole("button", { name: /Add an existing repository/ }).click();
      await page.getByLabel("Repository path").fill(localRepo);

      const registerFinished = page.waitForResponse((response) => {
        const url = new URL(response.url());
        return response.request().method() === "POST" && url.pathname === "/api/v1/fleet/hosts/hub/projects";
      });
      await page.getByRole("button", { name: "Add repository" }).click();
      const registerResponse = await registerFinished;
      expect(registerResponse.status(), await registerResponse.text()).toBe(201);
      const created = (await registerResponse.json()) as ProjectResponse;
      expect(created.id).not.toBe("");

      await expect(page).toHaveURL(/\/workspaces$/);
      const listResponse = await page.request.get(`${server.info.base_url}/api/v1/projects`);
      expect(listResponse.status(), await listResponse.text()).toBe(200);
      const list = (await listResponse.json()) as ProjectListResponse;
      expect(list.projects).toContainEqual(
        expect.objectContaining({
          id: created.id,
          local_path: localRepo,
        }),
      );
    } finally {
      rmSync(localRepo, { recursive: true, force: true });
      await server.stop();
    }
  });

  test("embed project card preserves host key in project actions", async ({ page }) => {
    const server = await startIsolatedE2EServerWithOptions({ fleetKey: "hub" });
    const localRepo = realpathSync(mkdtempSync(path.join(os.tmpdir(), "middleman-hosted-card-")));
    try {
      execFileSync("git", ["init"], { cwd: localRepo, stdio: "ignore" });
      const registerResponse = await page.request.post(`${server.info.base_url}/api/v1/projects`, {
        data: {
          local_path: localRepo,
          display_name: "Fleet Project",
          default_branch: "main",
        },
      });
      expect(registerResponse.status(), await registerResponse.text()).toBe(201);
      const project = (await registerResponse.json()) as ProjectResponse;
      expect(project.id).not.toBe("");

      await page.addInitScript(() => {
        const win = window as unknown as {
          __middleman_config?: MiddlemanConfig;
          __middleman_project_action_context?: unknown;
        };
        win.__middleman_config = {
          actions: {
            project: [
              {
                id: "new-worktree",
                label: "New Worktree",
                handler: (context) => {
                  win.__middleman_project_action_context = context;
                  return { ok: true };
                },
              },
            ],
          },
        };
      });

      await page.goto(`${server.info.base_url}/workspaces/embed/project/${encodeURIComponent(project.id)}?host=hub`);
      await expect(page.locator("header.app-top-bar")).toHaveCount(0);
      await expect(page.getByText("Fleet Project")).toBeVisible();

      await page
        .getByRole("button", {
          name: /Create (your first|another) worktree/i,
        })
        .click();

      await expect
        .poll(() =>
          page.evaluate(() => {
            const win = window as unknown as {
              __middleman_project_action_context?: unknown;
            };
            return win.__middleman_project_action_context;
          }),
        )
        .toEqual({
          surface: "project-card",
          projectId: project.id,
          hostKey: "hub",
        });
    } finally {
      rmSync(localRepo, { recursive: true, force: true });
      await server.stop();
    }
  });
});
