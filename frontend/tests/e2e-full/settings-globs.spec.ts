import { expect, request as playwrightRequest, test, type APIRequestContext } from "@playwright/test";
import { startIsolatedE2EServer, type IsolatedE2EServer } from "./support/e2eServer";

type RepoSummary = {
  Platform: string;
  PlatformHost: string;
  Owner: string;
  Name: string;
};

let isolatedServer: IsolatedE2EServer | undefined;
let api: APIRequestContext | undefined;

test.beforeEach(async () => {
  isolatedServer = await startIsolatedE2EServer();
  api = await playwrightRequest.newContext({
    baseURL: isolatedServer.info.base_url,
  });
});

test.afterEach(async () => {
  await api?.dispose();
  await isolatedServer?.stop();
  api = undefined;
  isolatedServer = undefined;
});

test("settings shows glob match counts and refresh updates tracked repos", async ({ page }) => {
  await page.goto(`${isolatedServer!.info.base_url}/settings`);

  await page.locator(".settings-page").waitFor({ state: "visible", timeout: 10_000 });

  const row = page.locator(".repo-row", { hasText: "roborev-dev/*" });
  await expect(row).toContainText("roborev-dev/*");
  await expect(row).toContainText("(2)");
  await expect.poll(async () => {
    if (!api) {
      throw new Error("settings-globs API context not initialized");
    }
    const response = await api.get("/api/v1/repos");
    const repos = await response.json() as RepoSummary[];
    return repos
      .filter((repo) => repo.Owner === "roborev-dev")
      .map((repo) => repo.Name)
      .sort()
      .join(",");
  }).toBe("middleman,worker");

  const selector = page.getByTitle("Select repository");
  await expect(selector).toBeVisible();
  await selector.click();
  await expect(page.getByRole("option", { name: /roborev-dev\/middleman/ })).toBeVisible();
  await expect(page.getByRole("option", { name: /roborev-dev\/worker/ })).toBeVisible();
  await page.keyboard.press("Escape");

  await row.getByRole("button", { name: "Refresh" }).click();

  await expect(row).toContainText("(3)");
  await expect.poll(async () => {
    if (!api) {
      throw new Error("settings-globs API context not initialized");
    }
    const response = await api.get("/api/v1/repos");
    const repos = await response.json() as RepoSummary[];
    return repos
      .filter((repo) => repo.Owner === "roborev-dev")
      .map((repo) => repo.Name)
      .sort()
      .join(",");
  }).toBe("middleman,review-bot,worker");

  await page.screenshot({
    path: "test-results/settings-globs-pr.png",
    fullPage: true,
  });
});

test("settings imports a selected subset from a repository glob", async ({ page }) => {
  await page.goto(`${isolatedServer!.info.base_url}/settings`);
  await page.locator(".settings-page").waitFor({ state: "visible", timeout: 10_000 });

  await page.getByRole("button", { name: "Add repositories…" }).click();
  await expect(page.getByRole("dialog", { name: "Add repositories" })).toBeVisible();
  await page.getByLabel("Repository pattern").fill("import-lab/*");
  await page.getByRole("button", { name: "Preview" }).click();

  await expect(page.getByText("import-lab/api")).toBeVisible();
  await expect(page.getByText("import-lab/worker")).toBeVisible();
  await expect(page.getByText("import-lab/archived")).toHaveCount(0);

  await page.getByLabel("Filter repositories").fill("worker");
  await page.getByRole("button", { name: "None" }).click();
  await page.getByLabel("Filter repositories").fill("");
  await page.getByRole("button", { name: "Add selected repositories" }).click();

  await expect(page.getByRole("dialog", { name: "Add repositories" })).toHaveCount(0);
  await expect(page.locator(".repo-row", { hasText: "import-lab/api" })).toBeVisible();
  await expect(page.locator(".repo-row", { hasText: "import-lab/worker" })).toHaveCount(0);

  const selector = page.getByTitle("Select repository");
  await expect(selector).toBeVisible();
  await selector.click();
  const apiOption = page.getByRole("option", { name: /import-lab\/api/ });
  await expect(apiOption).toBeVisible();
  await expect(page.getByRole("option", { name: /import-lab\/worker/ })).toHaveCount(0);
  await apiOption.click();
  await expect(apiOption.locator("input[type='checkbox']")).toBeChecked();
  await page.keyboard.press("Escape");
  await expect(selector).toContainText("github.com/import-lab/api");

  if (!api) throw new Error("settings-globs API context not initialized");
  const settingsResponse = await api.get("/api/v1/settings");
  const settings = await settingsResponse.json() as { repos: Array<{ owner: string; name: string; is_glob: boolean }> };
  const exactNames = settings.repos
    .filter((repo) => repo.owner === "import-lab" && !repo.is_glob)
    .map((repo) => repo.name)
    .sort();
  expect(exactNames).toEqual(["api"]);
  await expect.poll(async () => {
    const response = await api!.get("/api/v1/repos");
    const repos = await response.json() as RepoSummary[];
    return repos
      .filter((repo) => repo.Owner === "import-lab")
      .map((repo) => repo.Name)
      .sort()
      .join(",");
  }).toBe("api");

  const repoRow = page.locator(".repo-row", { hasText: "import-lab/api" });
  await repoRow.getByTitle("Remove github/github.com/import-lab/api").click();
  await repoRow.getByRole("button", { name: "Yes" }).click();

  await expect(repoRow).toHaveCount(0);
  await expect(selector).toContainText("All repos");
  await expect.poll(async () => {
    const response = await api!.get("/api/v1/repos");
    const repos = await response.json() as RepoSummary[];
    return repos
      .filter((repo) => repo.Owner === "import-lab")
      .map((repo) => repo.Name)
      .sort()
      .join(",");
  }).toBe("");
});

test("repository import can hide forks and private repositories before adding", async ({ page }) => {
  let bulkRepos: Array<{ provider: string; host: string; owner: string; name: string; repo_path: string }> | undefined;
  await page.route("**/api/v1/repos/preview", async (route) => {
    await route.fulfill({
      contentType: "application/json",
      body: JSON.stringify({
        owner: "import-lab",
        pattern: "*",
        repos: [
          {
            provider: "github",
            platform_host: "github.com",
            owner: "import-lab",
            name: "public-source",
            repo_path: "import-lab/public-source",
            description: "Source repository",
            private: false,
            fork: false,
            pushed_at: "2026-04-22T10:00:00Z",
            already_configured: false,
          },
          {
            provider: "github",
            platform_host: "github.com",
            owner: "import-lab",
            name: "private-source",
            repo_path: "import-lab/private-source",
            description: "Private repository",
            private: true,
            fork: false,
            pushed_at: "2026-04-23T10:00:00Z",
            already_configured: false,
          },
          {
            provider: "github",
            platform_host: "github.com",
            owner: "import-lab",
            name: "public-fork",
            repo_path: "import-lab/public-fork",
            description: "Forked repository",
            private: false,
            fork: true,
            pushed_at: "2026-04-24T10:00:00Z",
            already_configured: false,
          },
        ],
      }),
    });
  });
  await page.route("**/api/v1/repos/bulk", async (route) => {
    const body = route.request().postDataJSON() as { repos: Array<{ provider: string; host: string; owner: string; name: string; repo_path: string }> };
    bulkRepos = body.repos;
    await route.fulfill({
      contentType: "application/json",
      status: 201,
      body: JSON.stringify({
        repos: [{ owner: "import-lab", name: "public-source", is_glob: false, matched_repo_count: 1 }],
        activity: { view_mode: "threaded", time_range: "7d", hide_closed: false, hide_bots: false },
        terminal: { font_family: "" },
      }),
    });
  });

  await page.goto(`${isolatedServer!.info.base_url}/settings`);
  await page.locator(".settings-page").waitFor({ state: "visible", timeout: 10_000 });

  await page.getByRole("button", { name: "Add repositories…" }).click();
  const dialog = page.getByRole("dialog", { name: "Add repositories" });
  await dialog.getByLabel("Repository pattern").fill("import-lab/*");
  await dialog.getByRole("button", { name: "Preview" }).click();

  await expect(dialog.getByText("import-lab/public-source")).toBeVisible();
  await expect(dialog.getByText("import-lab/private-source")).toBeVisible();
  await expect(dialog.getByText("import-lab/public-fork")).toBeVisible();

  await dialog.getByLabel("Hide private").check();
  await dialog.getByLabel("Hide forks").check();
  await expect(dialog.getByText("import-lab/public-source")).toBeVisible();
  await expect(dialog.getByText("import-lab/private-source")).toHaveCount(0);
  await expect(dialog.getByText("import-lab/public-fork")).toHaveCount(0);
  await expect(dialog.getByText("Selected 1 of 1")).toBeVisible();

  await dialog.getByRole("button", { name: "Add selected repositories" }).click();

  await expect.poll(() => bulkRepos).toEqual([{
    provider: "github",
    host: "github.com",
    owner: "import-lab",
    name: "public-source",
    repo_path: "import-lab/public-source",
  }]);
});

test("repository import previews and adds Forgejo and Gitea repositories through the API", async ({ page }) => {
  await page.goto(`${isolatedServer!.info.base_url}/settings`);
  await page.locator(".settings-page").waitFor({ state: "visible", timeout: 10_000 });
  await page.getByRole("button", { name: "Add repositories…" }).click();
  const dialog = page.getByRole("dialog", { name: "Add repositories" });

  await dialog.getByLabel("Provider").selectOption("forgejo");
  await expect(dialog.getByLabel("Host")).toHaveValue("codeberg.org");
  await dialog.getByLabel("Repository pattern").fill("team/subgroup/service-*");
  await dialog.getByRole("button", { name: "Preview" }).click();
  await expect(dialog.getByRole("alert")).toContainText("Format: owner/pattern");

  await dialog.getByLabel("Repository pattern").fill("forge-lab/*");
  await dialog.getByRole("button", { name: "Preview" }).click();
  await expect(dialog.getByText("forge-lab/service")).toBeVisible();
  await expect(dialog.getByText("forge-lab/archived")).toHaveCount(0);
  await dialog.getByRole("button", { name: "Add selected repositories" }).click();
  await expect(page.getByRole("dialog", { name: "Add repositories" })).toHaveCount(0);
  await expect(page.locator(".repo-row", { hasText: "forge-lab/service" })).toBeVisible();

  if (!api) throw new Error("settings-globs API context not initialized");
  let settingsResponse = await api.get("/api/v1/settings");
  let settings = await settingsResponse.json() as {
    repos: Array<{
      provider: string;
      platform_host: string;
      owner: string;
      name: string;
      repo_path: string;
      is_glob: boolean;
    }>;
  };
  expect(settings.repos).toContainEqual(expect.objectContaining({
    provider: "forgejo",
    platform_host: "codeberg.org",
    owner: "forge-lab",
    name: "service",
    repo_path: "forge-lab/service",
    is_glob: false,
  }));
  await expect.poll(async () => {
    const response = await api!.get("/api/v1/repos");
    const repos = await response.json() as RepoSummary[];
    return repos
      .filter((repo) => repo.Platform === "forgejo" && repo.PlatformHost === "codeberg.org" && repo.Owner === "forge-lab")
      .map((repo) => repo.Name)
      .sort()
      .join(",");
  }).toBe("service");

  await page.getByRole("button", { name: "Add repositories…" }).click();
  const giteaDialog = page.getByRole("dialog", { name: "Add repositories" });
  await giteaDialog.getByLabel("Provider").selectOption("gitea");
  await expect(giteaDialog.getByLabel("Host")).toHaveValue("gitea.com");
  await giteaDialog.getByLabel("Repository pattern").fill("gitea-team/*");
  await giteaDialog.getByRole("button", { name: "Preview" }).click();
  await expect(giteaDialog.getByText("gitea-team/service")).toBeVisible();
  await expect(giteaDialog.getByText("gitea-team/private-service")).toBeVisible();
  await expect(giteaDialog.getByText("gitea-team/archived")).toHaveCount(0);

  await giteaDialog.getByLabel("Hide private").check();
  await expect(giteaDialog.getByText("gitea-team/service")).toBeVisible();
  await expect(giteaDialog.getByText("gitea-team/private-service")).toHaveCount(0);
  await giteaDialog.getByRole("button", { name: "Add selected repositories" }).click();

  await expect(page.getByRole("dialog", { name: "Add repositories" })).toHaveCount(0);
  await expect(page.locator(".repo-row", { hasText: "gitea-team/service" })).toBeVisible();

  settingsResponse = await api.get("/api/v1/settings");
  settings = await settingsResponse.json() as typeof settings;
  expect(settings.repos).toContainEqual(expect.objectContaining({
    provider: "gitea",
    platform_host: "gitea.com",
    owner: "gitea-team",
    name: "service",
    repo_path: "gitea-team/service",
    is_glob: false,
  }));

  await expect.poll(async () => {
    const response = await api!.get("/api/v1/repos");
    const repos = await response.json() as RepoSummary[];
    return repos
      .filter((repo) => repo.Owner === "gitea-team")
      .map((repo) => repo.Name)
      .sort()
      .join(",");
  }).toBe("service");
});

test("repository import traps keyboard focus inside the dialog", async ({ page }) => {
  await page.goto(`${isolatedServer!.info.base_url}/settings`);
  await page.locator(".settings-page").waitFor({ state: "visible", timeout: 10_000 });

  await page.getByRole("button", { name: "Add repositories…" }).click();
  const dialog = page.getByRole("dialog", { name: "Add repositories" });
  await expect(dialog.getByLabel("Repository pattern")).toBeFocused();

  await dialog.getByRole("button", { name: "Close" }).focus();
  await page.keyboard.press("Shift+Tab");
  await expect(dialog.getByRole("button", { name: "Cancel" })).toBeFocused();

  await page.keyboard.press("Tab");
  await expect(dialog.getByRole("button", { name: "Close" })).toBeFocused();
});

test("repository import clears stale preview results after failed preview", async ({ page }) => {
  await page.goto(`${isolatedServer!.info.base_url}/settings`);
  await page.locator(".settings-page").waitFor({ state: "visible", timeout: 10_000 });

  await page.getByRole("button", { name: "Add repositories…" }).click();
  const dialog = page.getByRole("dialog", { name: "Add repositories" });
  await dialog.getByLabel("Repository pattern").fill("roborev-dev/*");
  await dialog.getByRole("button", { name: "Preview" }).click();
  await expect(dialog.getByText("roborev-dev/middleman")).toBeVisible();

  await dialog.getByLabel("Repository pattern").fill("bad-owner/[invalid");
  await dialog.getByRole("button", { name: "Preview" }).click();
  await expect(dialog.getByText(/invalid glob pattern|GitHub API error|glob syntax/)).toBeVisible();
  await expect(dialog.getByText("roborev-dev/middleman")).toHaveCount(0);
});

test("repository import ignores older preview responses", async ({ page }) => {
  let firstPreviewRelease: (() => void) | undefined;
  let previewCalls = 0;
  await page.route("**/api/v1/repos/preview", async (route) => {
    previewCalls += 1;
    const request = route.request().postDataJSON() as { owner: string; pattern: string };
    if (previewCalls === 1) {
      await new Promise<void>((resolve) => { firstPreviewRelease = resolve; });
      await route.fulfill({
        contentType: "application/json",
        body: JSON.stringify({
          owner: request.owner,
          pattern: request.pattern,
          repos: [{
            provider: "github",
            platform_host: "github.com",
            owner: "roborev-dev",
            name: "middleman",
            repo_path: "roborev-dev/middleman",
            description: "Main dashboard",
            private: false,
            fork: false,
            pushed_at: "2026-04-22T10:00:00Z",
            already_configured: false,
          }],
        }),
      });
      return;
    }
    await route.fulfill({
      contentType: "application/json",
      body: JSON.stringify({
        owner: request.owner,
        pattern: request.pattern,
        repos: [{
          provider: "github",
          platform_host: "github.com",
          owner: "roborev-dev",
          name: "review-bot",
          repo_path: "roborev-dev/review-bot",
          description: "Review automation",
          private: false,
          fork: false,
          pushed_at: "2026-04-24T09:15:00Z",
          already_configured: false,
        }],
      }),
    });
  });

  await page.goto(`${isolatedServer!.info.base_url}/settings`);
  await page.locator(".settings-page").waitFor({ state: "visible", timeout: 10_000 });
  await page.getByRole("button", { name: "Add repositories…" }).click();
  const dialog = page.getByRole("dialog", { name: "Add repositories" });

  await dialog.getByLabel("Repository pattern").fill("roborev-dev/*");
  await dialog.getByRole("button", { name: "Preview" }).click();
  await expect.poll(() => previewCalls).toBe(1);

  await dialog.getByLabel("Repository pattern").fill("roborev-dev/review-*");
  await dialog.getByRole("button", { name: "Preview" }).click();
  await expect.poll(() => previewCalls).toBe(2);
  await expect(dialog.getByText("roborev-dev/review-bot")).toBeVisible();

  firstPreviewRelease?.();
  await expect(dialog.getByText("roborev-dev/review-bot")).toBeVisible();
  await expect(dialog.getByText("roborev-dev/middleman")).toHaveCount(0);
});
