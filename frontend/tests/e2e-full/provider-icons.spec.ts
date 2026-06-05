import { expect, request as playwrightRequest, test } from "@playwright/test";
import { startIsolatedE2EServer } from "./support/e2eServer";

type RepoSummary = {
  repo: {
    provider: string;
    owner: string;
    name: string;
  };
};

test("provider icons disambiguate multi-provider repository views", async ({ page }) => {
  const server = await startIsolatedE2EServer();
  const api = await playwrightRequest.newContext({
    baseURL: server.info.base_url,
  });

  try {
    const summariesResponse = await api.get("/api/v1/repos/summary");
    expect(summariesResponse.ok()).toBe(true);
    const summaries = (await summariesResponse.json()) as RepoSummary[];
    expect(new Set(summaries.map((summary) => summary.repo.provider.toLowerCase())).size).toBeGreaterThan(1);

    await page.goto(`${server.info.base_url}/repos`);
    const repoCards = page.locator(".repo-card");
    await expect(
      repoCards
        .filter({
          has: page.getByRole("button", {
            name: /acme\s*\/\s*widgets/,
          }),
        })
        .first(),
    ).toBeVisible();
    await expect(repoCards.getByRole("img", { name: "GitHub" }).first()).toBeVisible();
    await expect(repoCards.getByRole("img", { name: "GitLab" })).toBeVisible();

    await page.goto(`${server.info.base_url}/settings`);
    await page.locator(".settings-page").waitFor({ state: "visible", timeout: 10_000 });
    await expect(page.locator(".repo-row").getByRole("img")).toHaveCount(0);

    const addResponse = await api.post("/api/v1/repos/bulk", {
      data: {
        repos: [
          {
            provider: "forgejo",
            platform_host: "codeberg.org",
            owner: "forge-lab",
            name: "service",
            repo_path: "forge-lab/service",
          },
        ],
      },
    });
    expect(addResponse.ok()).toBe(true);

    await page.reload();
    await page.locator(".settings-page").waitFor({ state: "visible", timeout: 10_000 });
    const repoRows = page.locator(".repo-row");
    await expect(repoRows.getByRole("img", { name: "GitHub" }).first()).toBeVisible();
    await expect(repoRows.getByRole("img", { name: "Forgejo" })).toBeVisible();
  } finally {
    await api.dispose();
    await server.stop();
  }
});
