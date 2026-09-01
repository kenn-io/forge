import { devices, expect, test, type Page } from "@playwright/test";

import { mockApi, mockSettings as defaultSettings } from "./support/mockApi";

async function mockMobileRepoSettings(page: Page): Promise<string[]> {
  const activityRepos: string[] = [];

  await mockApi(page);
  await page.route("**/api/v1/settings", async (route) => {
    await route.fulfill({
      status: 200,
      contentType: "application/json",
      body: JSON.stringify({
        ...defaultSettings,
        repos: [
          {
            provider: "github",
            platform_host: "github.com",
            owner: "acme",
            name: "widgets",
            repo_path: "acme/widgets",
            is_glob: false,
            matched_repo_count: 1,
          },
          {
            provider: "github",
            platform_host: "ghe.example.com",
            owner: "acme",
            name: "widgets",
            repo_path: "acme/widgets",
            is_glob: false,
            matched_repo_count: 1,
          },
          {
            provider: "gitea",
            platform_host: "github.com",
            owner: "acme",
            name: "widgets",
            repo_path: "acme/widgets",
            is_glob: false,
            matched_repo_count: 1,
          },
          {
            provider: "github",
            platform_host: "github.com",
            owner: "acme",
            name: "*",
            repo_path: "acme/*",
            is_glob: true,
            matched_repo_count: 4,
          },
        ],
        activity: {
          ...defaultSettings.activity,
          view_mode: "threaded",
          time_range: "30d",
        },
        terminal: {
          ...defaultSettings.terminal,
          font_size: 14,
        },
      }),
    });
  });
  await page.route("**/api/v1/activity**", async (route) => {
    const url = new URL(route.request().url());
    const repo = url.searchParams.get("repo");
    if (repo) activityRepos.push(repo);
    await route.fulfill({
      status: 200,
      contentType: "application/json",
      body: JSON.stringify({ capped: false, items: [] }),
    });
  });

  return activityRepos;
}

test.use({ ...devices["iPhone 13"] });

test.describe("mobile activity repository selector", () => {
  test("uses host-qualified concrete repos and excludes glob rows", async ({ page }) => {
    const activityRepos = await mockMobileRepoSettings(page);

    await page.goto("/m?range=30d&view=threaded");
    await page.getByRole("button", { name: /Filters/ }).click();
    const repoSelect = page.getByRole("button", { name: "Select repository: Global" });
    await expect(repoSelect).toBeVisible();
    await expect(repoSelect).toContainText("All repositories");

    await repoSelect.click();
    await expect(page.getByRole("option", { name: "Global" })).toBeVisible();
    await expect(page.getByRole("option", { name: "github/github.com/acme/widgets" })).toBeVisible();
    await expect(page.getByRole("option", { name: "gitea/github.com/acme/widgets" })).toBeVisible();
    await expect(
      page.getByRole("option", {
        name: "ghe.example.com/acme/widgets",
      }),
    ).toBeVisible();
    await expect(page.getByRole("option", { name: "acme/*" })).toHaveCount(0);

    await page.getByRole("option", { name: "gitea/github.com/acme/widgets" }).click();
    const selectedRepo = page.getByRole("button", { name: "Select repository: gitea/github.com/acme/widgets" });
    await expect(selectedRepo.locator(".typeahead-value")).toContainText("acme/widgets · gitea/github.com");
    await expect.poll(() => activityRepos).toContain("gitea|github.com/acme/widgets");
  });

  test("groups and labels activity from nested repo identity", async ({ page }) => {
    await mockMobileRepoSettings(page);
    await page.route("**/api/v1/activity**", async (route) => {
      await route.fulfill({
        status: 200,
        contentType: "application/json",
        body: JSON.stringify({
          capped: false,
          items: [
            {
              id: "a1",
              cursor: "a1",
              activity_type: "comment",
              author: "marius",
              body_preview: "Looks good",
              created_at: "2026-03-30T14:00:00Z",
              item_number: 42,
              item_state: "open",
              item_title: "Add browser regression coverage",
              item_type: "pr",
              item_url: "https://github.com/acme/widgets/pull/42",
              repo: {
                provider: "github",
                platform_host: "github.com",
                owner: "acme",
                name: "widgets",
                repo_path: "acme/widgets",
                capabilities: {},
              },
            },
            {
              id: "b1",
              cursor: "b1",
              activity_type: "review",
              author: "luisa",
              body_preview: "Requested tweaks",
              created_at: "2026-03-30T13:00:00Z",
              item_number: 42,
              item_state: "open",
              item_title: "Add browser regression coverage",
              item_type: "pr",
              item_url: "https://ghe.example.com/acme/widgets/pull/42",
              repo: {
                provider: "github",
                platform_host: "ghe.example.com",
                owner: "acme",
                name: "widgets",
                repo_path: "acme/widgets",
                capabilities: {},
              },
            },
            {
              id: "c1",
              cursor: "c1",
              activity_type: "comment",
              author: "sam",
              body_preview: "Same host, different provider",
              created_at: "2026-03-30T12:00:00Z",
              item_number: 42,
              item_state: "open",
              item_title: "Add browser regression coverage",
              item_type: "pr",
              item_url: "https://github.com/acme/widgets/pull/42",
              repo: {
                provider: "gitea",
                platform_host: "github.com",
                owner: "acme",
                name: "widgets",
                repo_path: "acme/widgets",
                capabilities: {},
              },
            },
          ],
        }),
      });
    });

    await page.goto("/m?range=30d&view=threaded");

    await expect(page.locator(".mobile-activity-card")).toHaveCount(3);
    await expect(
      page.locator(".mobile-activity-card__meta", {
        hasText: "github/github.com/acme/widgets",
      }),
    ).toBeVisible();
    await expect(
      page.locator(".mobile-activity-card__meta", {
        hasText: "gitea/github.com/acme/widgets",
      }),
    ).toBeVisible();
    await expect(
      page.locator(".mobile-activity-card__meta", {
        hasText: "ghe.example.com/acme/widgets",
      }),
    ).toBeVisible();
    await expect(page.getByText("undefined/undefined")).toHaveCount(0);
    await expect(
      page.locator(".mobile-activity-card__event-count", {
        hasText: "2",
      }),
    ).toHaveCount(0);
  });
});

test.describe("mobile PR status grouping", () => {
  test("uses kanban status instead of worktree buckets", async ({ page }) => {
    await mockApi(page);

    await page.goto("/m/pulls");
    await expect(page.locator(".focus-list")).toBeVisible();

    await page.getByRole("button", { name: "Filters" }).click();
    await page.getByRole("button", { name: "Status" }).click();

    await expect(page.locator(".workflow-group .group-header")).toHaveText(["New", "Reviewing"]);
    await expect(page.getByText("Needs Worktree")).toHaveCount(0);
  });
});
