import { devices, expect, test, type Page } from "@playwright/test";

import { mockApi } from "./support/mockApi";

async function expectMobileRowRailLayout(page: Page, itemSelector: string): Promise<void> {
  const item = page.locator(itemSelector).first();
  await expect(item).toBeVisible();

  const metrics = await item.evaluate((node) => {
    const title = node.querySelector<HTMLElement>(".title");
    const strip = node.querySelector<HTMLElement>(".sidebar-status-strip");
    const titleText = node.querySelector<HTMLElement>(".title-text");
    const textStyle = titleText ? getComputedStyle(titleText) : null;
    const stripRect = strip?.getBoundingClientRect();
    const titleRect = title?.getBoundingClientRect();
    const itemRect = node.getBoundingClientRect();
    return {
      stripIsDirectChild: strip?.parentElement === node,
      titleTextLineClamp: textStyle?.webkitLineClamp ?? "",
      stripWidth: stripRect?.width ?? 0,
      stripHeight: stripRect?.height ?? 0,
      stripLeft: stripRect?.left ?? 0,
      stripRight: stripRect?.right ?? 0,
      stripCenterY: stripRect ? stripRect.top + stripRect.height / 2 : 0,
      itemLeft: itemRect.left,
      itemCenterY: itemRect.top + itemRect.height / 2,
      titleLeft: titleRect?.left ?? 0,
    };
  });

  expect(metrics.stripIsDirectChild).toBe(true);
  expect(metrics.titleTextLineClamp).toBe("2");
  expect(metrics.stripWidth).toBe(3);
  expect(metrics.stripHeight).toBe(18);
  expect(metrics.stripLeft - metrics.itemLeft).toBe(6);
  expect(metrics.stripRight).toBeLessThanOrEqual(metrics.titleLeft);
  expect(Math.abs(metrics.stripCenterY - metrics.itemCenterY)).toBeLessThanOrEqual(0.51);
}

async function mockMobileRepoSettings(page: Page): Promise<string[]> {
  const activityRepos: string[] = [];

  await mockApi(page);
  await page.route("**/api/v1/settings", async (route) => {
    await route.fulfill({
      status: 200,
      contentType: "application/json",
      body: JSON.stringify({
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
          view_mode: "threaded",
          time_range: "30d",
          hide_closed: false,
          hide_bots: false,
        },
        terminal: {
          font_family: "",
          font_size: 14,
          scrollback: 1000,
          line_height: 1,
          letter_spacing: 0,
          cursor_blink: true,
          font_ligatures: false,
          renderer: "xterm",
        },
        agents: [],
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

const iPhone13 = devices["iPhone 13"];
test.use({
  viewport: iPhone13.viewport,
  deviceScaleFactor: iPhone13.deviceScaleFactor,
  userAgent: iPhone13.userAgent,
  hasTouch: iPhone13.hasTouch,
  isMobile: iPhone13.isMobile,
});

test.describe("mobile activity repository selector", () => {
  test("uses host-qualified concrete repos and excludes glob rows", async ({ page }) => {
    const activityRepos = await mockMobileRepoSettings(page);

    await page.goto("/m?range=30d&view=threaded");
    const repoSelect = page.getByRole("combobox", {
      name: /Repository/,
    });
    await expect(repoSelect).toBeVisible();

    await repoSelect.click();
    await expect(page.getByRole("option", { name: "All repos" })).toBeVisible();
    await expect(page.getByRole("option", { name: "github/github.com/acme/widgets" })).toBeVisible();
    await expect(page.getByRole("option", { name: "gitea/github.com/acme/widgets" })).toBeVisible();
    await expect(
      page.getByRole("option", {
        name: "ghe.example.com/acme/widgets",
      }),
    ).toBeVisible();
    await expect(page.getByRole("option", { name: "acme/*" })).toHaveCount(0);

    await page.getByRole("option", { name: "gitea/github.com/acme/widgets" }).click();
    await expect(page.getByRole("combobox", { name: "Repository: gitea/github.com/acme/widgets" })).toHaveText(
      "gitea/github.com/acme/widgets",
    );
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

    await page.getByRole("button", { name: "Status" }).click();

    await expect(page.locator(".workflow-group .group-header")).toHaveText(["New", "Reviewing"]);
    await expect(page.getByText("Needs Worktree")).toHaveCount(0);
  });

  test("keeps pull and issue state rails compact in the row gutter", async ({ page }) => {
    await mockApi(page);

    await page.goto("/m/pulls");
    await expectMobileRowRailLayout(page, ".pull-item");

    await page.goto("/m/issues");
    await expectMobileRowRailLayout(page, ".issue-item");
  });
});
