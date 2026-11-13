import { expect, test, type Page } from "@playwright/test";

import { mockApi } from "./support/mockApi";

const repo = {
  provider: "github",
  platform_host: "github.com",
  repo_path: "acme/widgets",
  owner: "acme",
  name: "widgets",
  capabilities: {},
};

function pull(number: number, title: string, overrides: Record<string, unknown> = {}) {
  return {
    ID: number,
    RepoID: 1,
    GitHubID: 1000 + number,
    Number: number,
    URL: `https://github.com/acme/widgets/pull/${number}`,
    Title: title,
    Author: "marius",
    State: "open",
    IsDraft: false,
    Body: "",
    HeadBranch: `feature/${number}`,
    BaseBranch: "main",
    Additions: 10,
    Deletions: 1,
    CommentCount: 0,
    ReviewDecision: "",
    CIStatus: "success",
    CIChecksJSON: "[]",
    CreatedAt: "2026-03-29T14:00:00Z",
    UpdatedAt: "2026-03-30T14:00:00Z",
    LastActivityAt: "2026-03-30T14:00:00Z",
    MergedAt: null,
    ClosedAt: null,
    MergeableState: "clean",
    Starred: false,
    repo_owner: "acme",
    repo_name: "widgets",
    platform_host: "github.com",
    platform_head_sha: `${number}`.padStart(40, "a"),
    head_repo_kind: "same_repo",
    repo,
    worktree_links: [],
    ...overrides,
  };
}

async function mockPulls(page: Page) {
  await mockApi(page);
  await page.route("**/api/v1/pulls**", async (route) => {
    const url = new URL(route.request().url());
    if (route.request().method() !== "GET" || url.pathname !== "/api/v1/pulls") {
      await route.fallback();
      return;
    }
    // The server applies label filters before its limit, ignoring case; the mock does the same.
    const required = url.searchParams.getAll("labels").map((name) => name.toLowerCase());
    const excluded = url.searchParams.getAll("exclude_labels").map((name) => name.toLowerCase());
    const names = (row: { labels?: { name: string }[] }) => (row.labels ?? []).map((label) => label.name.toLowerCase());
    const rows = [
      pull(10, "Approved review queue", {
        labels: [{ name: "bug", color: "d73a4a", is_default: true }],
        ReviewDecision: "APPROVED",
        workspace: { id: "ws-10", status: "ready" },
        merge_queue: {
          position: 1,
          state: "queued",
          enqueued_at: "2026-03-30T14:00:00Z",
          observed_at: "2026-03-30T14:00:00Z",
        },
      }),
      pull(11, "Draft parser cleanup", {
        labels: [
          { name: "bug", color: "d73a4a", is_default: true },
          { name: "docs", color: "0075ca", is_default: true },
        ],
        IsDraft: true,
        head_repo_kind: "unknown",
        CIStatus: "pending",
        CIChecksJSON: JSON.stringify([{ name: "test", status: "in_progress", conclusion: "" }]),
      }),
      pull(12, "Ready failed workflow", {
        labels: [{ name: "docs", color: "0075ca", is_default: true }],
        CIStatus: "failure",
        workspace: { id: "ws-12", status: "ready" },
        deferred_merge_pending: true,
      }),
      pull(13, "Ready conflict resolver", {
        MergeableState: "dirty",
        head_repo_kind: "fork",
      }),
    ];
    await route.fulfill({
      status: 200,
      contentType: "application/json",
      body: JSON.stringify(
        rows.filter(
          (row) =>
            required.every((name) => names(row).includes(name)) && !excluded.some((name) => names(row).includes(name)),
        ),
      ),
    });
  });
}

async function openPrFilters(page: Page): Promise<void> {
  const standaloneTrigger = page.getByTitle("PR filters");
  if (await standaloneTrigger.isVisible()) {
    await standaloneTrigger.click();
  } else {
    await page.locator(".compact-filter-menu .kit-filter-dropdown__btn").click();
  }
  await expect(page.locator(".kit-filter-dropdown__panel")).toBeVisible();
}

test("PR filters all apply and each pair of opposites is pick-one", async ({ page }) => {
  await mockPulls(page);
  await page.goto("/pulls");

  const rows = page.locator(".pr-list-row");
  await expect(rows).toHaveCount(4);

  await openPrFilters(page);
  const filterPanel = page.locator(".kit-filter-dropdown__panel");
  const workspaceGrouping = (
    await filterPanel.locator(".kit-filter-dropdown__section-title, .kit-filter-dropdown__item").allTextContents()
  )
    .map((text) => text.trim())
    .filter((text) => ["Pull requests", "Has workspace", "View"].includes(text));
  expect(workspaceGrouping).toEqual(["Pull requests", "Has workspace", "View"]);

  await filterPanel.getByRole("button", { name: "Has workspace" }).click();
  await expect(rows).toHaveText([/Approved review queue/, /Ready failed workflow/]);
  await filterPanel.getByRole("button", { name: /^(Clear filters|Reset view)$/ }).click();
  await expect(rows).toHaveCount(4);

  const draft = filterPanel.locator(".kit-filter-dropdown__item", { hasText: "Draft" });
  await draft.click();
  await expect(rows).toHaveText([/Draft parser cleanup/]);
  // Excluding drafts from the open list leaves what Ready for review used to show.
  await draft.click();
  await expect(rows).toHaveText([/Approved review queue/, /Ready failed workflow/, /Ready conflict resolver/]);
  await filterPanel.getByRole("button", { name: "Failed CI" }).click();
  await expect(rows).toHaveText([/Ready failed workflow/]);
  await filterPanel.getByRole("button", { name: /^(Clear filters|Reset view)$/ }).click();
  await filterPanel.getByRole("button", { name: "Merge conflicts" }).click();

  await expect(rows).toHaveText([/Ready conflict resolver/]);

  await filterPanel.getByRole("button", { name: /^(Clear filters|Reset view)$/ }).click();
  await filterPanel.getByRole("button", { name: "Passing CI" }).click();

  await expect(rows).toHaveText([/Approved review queue/, /Ready conflict resolver/]);

  await draft.click();
  await draft.click();
  await filterPanel.getByRole("button", { name: "Approved", exact: true }).click();
  await expect(rows).toHaveText([/Approved review queue/]);
});

test("a condition row cycles through required, excluded, and off", async ({ page }) => {
  await mockPulls(page);
  await page.goto("/pulls");

  const rows = page.locator(".pr-list-row");
  await expect(rows).toHaveCount(4);
  await openPrFilters(page);
  const filterPanel = page.locator(".kit-filter-dropdown__panel");

  for (const condition of [
    {
      label: "Draft",
      required: [/Draft parser cleanup/],
      excluded: [/Approved review queue/, /Ready failed workflow/, /Ready conflict resolver/],
    },
    {
      label: "Approved",
      required: [/Approved review queue/],
      excluded: [/Draft parser cleanup/, /Ready failed workflow/, /Ready conflict resolver/],
    },
    {
      label: "Merge conflicts",
      required: [/Ready conflict resolver/],
      excluded: [/Approved review queue/, /Draft parser cleanup/, /Ready failed workflow/],
    },
    {
      label: "Merge queued",
      required: [/Approved review queue/, /Ready failed workflow/],
      excluded: [/Draft parser cleanup/, /Ready conflict resolver/],
    },
    {
      label: "Has workspace",
      required: [/Approved review queue/, /Ready failed workflow/],
      excluded: [/Draft parser cleanup/, /Ready conflict resolver/],
    },
    {
      // A head repository that could not be classified stays visible when forks are hidden.
      label: "From fork",
      required: [/Ready conflict resolver/],
      excluded: [/Approved review queue/, /Draft parser cleanup/, /Ready failed workflow/],
    },
  ]) {
    const row = filterPanel.locator(".kit-filter-dropdown__item", { hasText: condition.label });
    await expect(row).toHaveAttribute("aria-pressed", "false");

    await row.click();
    await expect(row).toHaveAccessibleName(condition.label);
    await expect(row.locator("svg.lucide-check")).toBeVisible();
    await expect(rows).toHaveText(condition.required);

    await row.click();
    await expect(row).toHaveAccessibleName(/, excluded$/);
    await expect(row.locator("svg.lucide-x")).toBeVisible();
    await expect(row).toHaveAttribute("aria-pressed", "true");
    await expect(rows).toHaveText(condition.excluded);

    await row.click();
    await expect(row).toHaveAttribute("aria-pressed", "false");
    await expect(row.locator("svg")).toHaveCount(0);
    await expect(rows).toHaveCount(4);
  }

  // Excluded conditions combine with each other and with a pick-one choice, and reset clears them.
  const approved = filterPanel.locator(".kit-filter-dropdown__item", { hasText: "Approved" });
  const workspace = filterPanel.locator(".kit-filter-dropdown__item", { hasText: "Has workspace" });
  await approved.click();
  await approved.click();
  await workspace.click();
  await workspace.click();
  await expect(rows).toHaveText([/Draft parser cleanup/, /Ready conflict resolver/]);
  await filterPanel.getByRole("button", { name: "Draft", exact: true }).click();
  await expect(rows).toHaveText([/Draft parser cleanup/]);

  await filterPanel.getByRole("button", { name: /^(Clear filters|Reset view)$/ }).click();
  await expect(rows).toHaveCount(4);
  await expect(approved).toHaveAttribute("aria-pressed", "false");
});

test("the Labels flyout requires, excludes, and clears each label", async ({ page }) => {
  await mockPulls(page);
  await page.goto("/pulls");

  const rows = page.locator(".pr-list-row");
  await expect(rows).toHaveCount(4);

  await openPrFilters(page);
  const labelsRow = page.locator(".kit-filter-dropdown__panel").getByRole("button", { name: /^Labels/ });
  await expect(labelsRow).toContainText("Any");
  await labelsRow.click();

  const flyout = page.getByRole("dialog", { name: "Labels" });
  await expect(flyout).toBeVisible();
  await expect(flyout.getByRole("searchbox", { name: "Search labels" })).toBeFocused();
  const bug = flyout.getByRole("button", { name: /^bug/ });
  const docs = flyout.getByRole("button", { name: /^docs/ });

  await bug.click();
  await expect(bug).toHaveAccessibleName(/required/);
  await expect(rows).toHaveText([/Approved review queue/, /Draft parser cleanup/]);
  await expect(labelsRow).toContainText("1 req");

  await bug.click();
  await expect(bug).toHaveAccessibleName(/excluded/);
  await expect(rows).toHaveText([/Ready failed workflow/, /Ready conflict resolver/]);

  await docs.click();
  await expect(rows).toHaveText([/Ready failed workflow/]);
  await expect(labelsRow).toContainText("1 req, 1 excl");

  await page.keyboard.press("Escape");
  await expect(flyout).toBeHidden();
  await page
    .locator(".kit-filter-dropdown__panel")
    .getByRole("button", { name: /^(Clear filters|Reset view)$/ })
    .click();
  await expect(rows).toHaveCount(4);
  await expect(labelsRow).toContainText("Any");
});

test("a changing PR count leaves the open filter menu and its trigger in place", async ({ page }) => {
  await mockApi(page);
  let listSize = 9;
  await page.route("**/api/v1/pulls**", async (route) => {
    const url = new URL(route.request().url());
    if (route.request().method() !== "GET" || url.pathname !== "/api/v1/pulls") {
      await route.fallback();
      return;
    }
    const body = Array.from({ length: listSize }, (_, index) => pull(index + 1, `Change ${index + 1}`));
    listSize = 123;
    await route.fulfill({ status: 200, contentType: "application/json", body: JSON.stringify(body) });
  });
  await page.goto("/pulls");

  const count = page.locator(".filter-bar .list-count-chip");
  await expect(count).toHaveText("9 PRs");
  await openPrFilters(page);
  const trigger = page.locator(".filter-bar .kit-filter-dropdown__btn[aria-expanded='true']");
  const panel = page.locator(".kit-filter-dropdown__panel");
  const triggerBefore = await trigger.boundingBox();
  const panelBefore = await panel.boundingBox();

  // Involves me reloads the list from the server, which now has 123 PRs.
  await panel.getByRole("button", { name: "Involves me" }).click();
  await expect(count).toHaveText("123 PRs");
  await page.evaluate(() => new Promise((resolve) => requestAnimationFrame(() => requestAnimationFrame(resolve))));

  expect(await trigger.boundingBox()).toEqual(triggerBefore);
  expect(await panel.boundingBox()).toEqual(panelBefore);
});
