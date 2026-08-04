import { execFileSync } from "node:child_process";
import { expect, test, type Page } from "@playwright/test";

import { startIsolatedE2EServerWithOptions, type IsolatedE2EServer } from "./support/e2eServer";

const defaultPlatformHost = "github.com";
const enterprisePlatformHost = "ghe.example.com";

type ConfiguredRepo = {
  provider: string;
  platform_host: string;
  owner: string;
  name: string;
  repo_path: string;
};

type PullRow = {
  repo: {
    platform_host: string;
    repo_path: string;
  };
};

type RepoSummary = {
  Platform: string;
  PlatformHost: string;
  Owner: string;
  Name: string;
};

function hasCommand(command: string, args: string[] = ["--version"]): boolean {
  try {
    execFileSync(command, args, { stdio: "ignore" });
    return true;
  } catch {
    return false;
  }
}

async function removeConfiguredRepositories(page: Page, baseURL: string): Promise<void> {
  const settingsResponse = await page.request.get(`${baseURL}/api/v1/settings`);
  expect(settingsResponse.ok()).toBe(true);
  const settings = (await settingsResponse.json()) as { repos: ConfiguredRepo[] };
  for (const repo of settings.repos) {
    const remove = await page.request.delete(
      `${baseURL}/api/v1/host/${encodeURIComponent(repo.platform_host)}` +
        `/repo/${encodeURIComponent(repo.provider)}/${encodeURIComponent(repo.owner)}/${encodeURIComponent(repo.name)}`,
      { headers: { "content-type": "application/json" } },
    );
    expect(remove.ok(), `failed to remove ${repo.provider}|${repo.platform_host}/${repo.repo_path}`).toBe(true);
  }
}

async function prepareGitHubOnboarding(page: Page, platformHost: string): Promise<void> {
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
        gh: { available: true, authenticated: true, host: platformHost, user: "maintainer" },
        glab: { available: false, authenticated: false, host: "gitlab.com" },
      }),
    });
  });
  await page.route("**/api/v1/platform/user-repositories**", async (route) => {
    const url = new URL(route.request().url());
    expect(url.searchParams.get("provider")).toBe("github");
    expect(url.searchParams.get("platform_host")).toBe(platformHost);
    expect(url.searchParams.get("limit")).toBe("1000");
    await route.fulfill({
      contentType: "application/json",
      body: JSON.stringify({
        repositories: [
          {
            name_with_owner: "acme/widgets",
            ssh_url: `git@${platformHost}:acme/widgets.git`,
            default_branch: "main",
          },
        ],
      }),
    });
  });
}

async function configureWidgetRepository(page: Page, baseURL: string): Promise<void> {
  await page.setViewportSize({ width: 700, height: 900 });
  await page.goto(`${baseURL}/`);
  await expect(page.getByRole("heading", { name: "Connect a code forge" })).toBeVisible();
  await page.getByRole("button", { name: "Continue with GitHub" }).click();
  await expect(page.getByRole("heading", { name: "Choose the repositories you maintain" })).toBeVisible();
  await expect(page.getByRole("listitem", { name: "Choose repos: current" })).toBeVisible();
  expect(await page.evaluate(() => document.documentElement.scrollWidth <= document.documentElement.clientWidth)).toBe(
    true,
  );
  await page.getByRole("checkbox", { name: /acme\/widgets/ }).click();
  await page.getByRole("button", { name: "Configure 1 repository" }).click();
  await expect(page.getByRole("heading", { name: "Open a pull request" })).toBeVisible({ timeout: 30_000 });
}

test("first run reaches a ready PR workspace", async ({ page }) => {
  test.skip(!hasCommand("git") || !hasCommand("tmux", ["-V"]), "git and tmux are required for workspace setup");
  test.setTimeout(120_000);

  let server: IsolatedE2EServer | null = null;
  try {
    server = await startIsolatedE2EServerWithOptions();
    await removeConfiguredRepositories(page, server.info.base_url);
    await prepareGitHubOnboarding(page, defaultPlatformHost);
    await configureWidgetRepository(page, server.info.base_url);

    const persistedResponse = await page.request.get(`${server.info.base_url}/api/v1/settings`);
    expect(persistedResponse.ok()).toBe(true);
    const persisted = (await persistedResponse.json()) as { repos: ConfiguredRepo[] };
    expect(persisted.repos).toContainEqual(
      expect.objectContaining({
        provider: "github",
        platform_host: defaultPlatformHost,
        repo_path: "acme/widgets",
      }),
    );

    await page.reload();
    await expect(page.getByRole("heading", { name: "Open a pull request" })).toBeVisible({ timeout: 30_000 });
    await expect(page.getByText("Add widget caching layer", { exact: true })).toBeVisible();
    await page.getByRole("button", { name: /Continue with PR/ }).click();
    await page.getByRole("button", { name: "Open PR first" }).click();
    await expect(page).toHaveURL(/\/pulls\/github\/acme\/widgets\/1$/);

    await page.evaluate(() => sessionStorage.removeItem("kenn-forge:first-run-onboarding"));
    await page.goto(`${server.info.base_url}/`);
    await expect(page.getByRole("heading", { name: "Open a pull request" })).toBeVisible({ timeout: 30_000 });
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

test("first sync creates a pull under the authenticated enterprise host", async ({ page }) => {
  test.skip(!hasCommand("git"), "git is required for sync setup");
  test.setTimeout(120_000);

  let server: IsolatedE2EServer | null = null;
  try {
    server = await startIsolatedE2EServerWithOptions({ defaultPlatformHost: enterprisePlatformHost });
    await removeConfiguredRepositories(page, server.info.base_url);
    await prepareGitHubOnboarding(page, enterprisePlatformHost);

    const beforeSyncResponse = await page.request.get(`${server.info.base_url}/api/v1/pulls?limit=100`);
    expect(beforeSyncResponse.ok()).toBe(true);
    const beforeSync = (await beforeSyncResponse.json()) as PullRow[];
    expect(
      beforeSync.some(
        (pull) => pull.repo.platform_host === enterprisePlatformHost && pull.repo.repo_path === "acme/widgets",
      ),
    ).toBe(false);

    await configureWidgetRepository(page, server.info.base_url);

    const persistedResponse = await page.request.get(`${server.info.base_url}/api/v1/settings`);
    expect(persistedResponse.ok()).toBe(true);
    const persisted = (await persistedResponse.json()) as { repos: ConfiguredRepo[] };
    expect(persisted.repos).toContainEqual(
      expect.objectContaining({
        provider: "github",
        platform_host: enterprisePlatformHost,
        repo_path: "acme/widgets",
      }),
    );
    const afterSyncResponse = await page.request.get(`${server.info.base_url}/api/v1/pulls?limit=100`);
    expect(afterSyncResponse.ok()).toBe(true);
    const afterSync = (await afterSyncResponse.json()) as PullRow[];
    expect(afterSync).toContainEqual(
      expect.objectContaining({
        repo: expect.objectContaining({
          platform_host: enterprisePlatformHost,
          repo_path: "acme/widgets",
        }),
      }),
    );
  } finally {
    await server?.stop();
  }
});

test("missing gh stays usable on the mobile entry route", async ({ page }) => {
  let server: IsolatedE2EServer | null = null;
  try {
    server = await startIsolatedE2EServerWithOptions();
    await removeConfiguredRepositories(page, server.info.base_url);
    await page.addInitScript(() => {
      localStorage.removeItem("kenn-forge:first-run-onboarding");
      sessionStorage.removeItem("kenn-forge:first-run-onboarding");
    });
    await page.route("**/api/v1/tooling-status", async (route) => {
      await route.fulfill({
        contentType: "application/json",
        body: JSON.stringify({
          git: { available: true, version: "2.50" },
          gh: { available: false, authenticated: false },
          glab: { available: false, authenticated: false },
        }),
      });
    });

    await page.setViewportSize({ width: 390, height: 844 });
    await page.goto(`${server.info.base_url}/m`);
    await expect(page.getByRole("heading", { name: "Connect a code forge" })).toBeVisible();
    await expect(page.getByRole("listitem", { name: "Code forge: current" })).toBeVisible();
    for (const provider of ["GitHub", "GitLab", "Forgejo", "Gitea"]) {
      await expect(page.getByRole("img", { name: provider })).toBeVisible();
    }
    expect(
      await page.evaluate(() => document.documentElement.scrollWidth <= document.documentElement.clientWidth),
    ).toBe(true);

    await page.getByRole("button", { name: "Configure Forgejo" }).click();
    await expect(page).toHaveURL(`${server.info.base_url}/settings`);
  } finally {
    await server?.stop();
  }
});

test("Forgejo setup returns from repository settings and syncs the complete provider identity", async ({ page }) => {
  test.setTimeout(120_000);

  let server: IsolatedE2EServer | null = null;
  try {
    server = await startIsolatedE2EServerWithOptions();
    await removeConfiguredRepositories(page, server.info.base_url);
    await page.addInitScript(() => {
      localStorage.removeItem("kenn-forge:first-run-onboarding");
      sessionStorage.removeItem("kenn-forge:first-run-onboarding");
    });

    await page.goto(`${server.info.base_url}/`);
    await expect(page.getByRole("heading", { name: "Connect a code forge" })).toBeVisible();
    await page.getByRole("button", { name: "Configure Forgejo" }).click();
    await expect(page).toHaveURL(`${server.info.base_url}/settings`);
    expect(await page.evaluate(() => localStorage.getItem("kenn-forge:first-run-onboarding"))).toBe("active");

    await page.getByRole("button", { name: "Add repositories…" }).click();
    const dialog = page.getByRole("dialog", { name: "Add repositories" });
    await dialog.getByRole("combobox", { name: /Provider/ }).click();
    await dialog.getByRole("option", { name: "Forgejo" }).click();
    await expect(dialog.getByLabel("Host")).toHaveValue("codeberg.org");
    await dialog.getByLabel("Repository pattern").fill("forge-lab/*");
    await dialog.getByRole("button", { name: "Preview" }).click();
    await expect(dialog.getByText("forge-lab/service")).toBeVisible();
    await dialog.getByRole("button", { name: "Add selected repositories" }).click();
    await page.getByRole("button", { name: "Back to app" }).click();

    await expect(page.getByRole("heading", { name: "First sync is underway" })).toBeVisible({ timeout: 30_000 });
    const settingsResponse = await page.request.get(`${server.info.base_url}/api/v1/settings`);
    expect(settingsResponse.ok()).toBe(true);
    const settings = (await settingsResponse.json()) as { repos: ConfiguredRepo[] };
    expect(settings.repos).toContainEqual(
      expect.objectContaining({
        provider: "forgejo",
        platform_host: "codeberg.org",
        owner: "forge-lab",
        name: "service",
        repo_path: "forge-lab/service",
      }),
    );
    await expect
      .poll(async () => {
        const response = await page.request.get(`${server!.info.base_url}/api/v1/repos`);
        expect(response.ok()).toBe(true);
        const repos = (await response.json()) as RepoSummary[];
        return repos.some(
          (repo) =>
            repo.Platform === "forgejo" &&
            repo.PlatformHost === "codeberg.org" &&
            repo.Owner === "forge-lab" &&
            repo.Name === "service",
        );
      })
      .toBe(true);
  } finally {
    await server?.stop();
  }
});
