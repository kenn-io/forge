import { expect, test, type Page } from "@playwright/test";

import { mockApi, mockSettings } from "./support/mockApi";

const repo = {
  provider: "github",
  platform_host: "github.com",
  repo_path: "acme/widgets",
  owner: "acme",
  name: "widgets",
  platform_repo_id: 1001,
  capabilities: {},
};

function pull(number: number, title: string, activityAt: string, overrides: Record<string, unknown> = {}) {
  return {
    ID: number,
    RepoID: 1,
    GitHubID: 1000 + number,
    Number: number,
    URL: `https://github.com/acme/widgets/pull/${number}`,
    Title: title,
    Author: "octo",
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
    UpdatedAt: activityAt,
    LastActivityAt: activityAt,
    MergedAt: null,
    ClosedAt: null,
    MergeableState: "clean",
    KanbanStatus: "new",
    Starred: false,
    repo_owner: "acme",
    repo_name: "widgets",
    platform_host: "github.com",
    platform_head_sha: `${number}`.padStart(40, "a"),
    repo,
    worktree_links: [],
    ...overrides,
  };
}

const fixCI = { label: "Fix CI", agent: "codex", prompt: "Make the failing checks pass." };

interface Recorded {
  creates: Record<string, unknown>[];
  handoffs: { workspaceId: string; body: Record<string, unknown> }[];
}

async function mockQuickActions(page: Page): Promise<Recorded> {
  const recorded: Recorded = { creates: [], handoffs: [] };
  await mockApi(page);
  await page.route("**/api/v1/settings", async (route) => {
    if (route.request().method() !== "GET") {
      await route.fallback();
      return;
    }
    await route.fulfill({
      status: 200,
      contentType: "application/json",
      body: JSON.stringify({
        ...mockSettings,
        quick_actions: [fixCI],
        launch_targets: [
          {
            key: "codex",
            label: "Codex",
            kind: "agent",
            source: "builtin",
            command: ["codex"],
            available: true,
            disabled_reason: "",
          },
        ],
      }),
    });
  });
  await page.route("**/api/v1/pulls**", async (route) => {
    const url = new URL(route.request().url());
    if (route.request().method() !== "GET" || url.pathname !== "/api/v1/pulls") {
      await route.fallback();
      return;
    }
    await route.fulfill({
      status: 200,
      contentType: "application/json",
      body: JSON.stringify([
        pull(21, "Retry flaky upload", "2026-03-30T16:00:00Z"),
        pull(22, "Pin parser version", "2026-03-30T15:00:00Z", {
          workspace: { id: "ws-22", status: "ready" },
        }),
        pull(23, "Rename config loader", "2026-03-30T14:00:00Z"),
        pull(24, "Untouched cleanup", "2026-03-30T13:00:00Z"),
      ]),
    });
  });
  await page.route("**/api/v1/workspaces", async (route) => {
    if (route.request().method() !== "POST") {
      await route.fallback();
      return;
    }
    const body = route.request().postDataJSON() as Record<string, unknown>;
    recorded.creates.push(body);
    await route.fulfill({
      status: 202,
      contentType: "application/json",
      body: JSON.stringify({ id: `ws-${String(body["mr_number"])}`, status: "creating", created: true }),
    });
  });
  await page.route("**/api/v1/issues/github/acme/widgets/7/workspace", async (route) => {
    recorded.creates.push(route.request().postDataJSON() as Record<string, unknown>);
    await route.fulfill({
      status: 202,
      contentType: "application/json",
      body: JSON.stringify({ id: "ws-issue-7", status: "creating", created: true }),
    });
  });
  await page.route("**/api/v1/workspaces/*/runtime/agent-handoffs", async (route) => {
    const workspaceId = new URL(route.request().url()).pathname.split("/")[4] ?? "";
    recorded.handoffs.push({ workspaceId, body: route.request().postDataJSON() as Record<string, unknown> });
    await route.fulfill({
      status: 201,
      contentType: "application/json",
      body: JSON.stringify({
        initial_message: { state: "delivered" },
        session: {
          key: `session-${workspaceId}`,
          target_key: "codex",
          label: "Codex",
          kind: "agent",
          status: "running",
        },
      }),
    });
  });
  return recorded;
}

test("runs a quick action on a shift-selected range of pull requests", async ({ page }) => {
  const recorded = await mockQuickActions(page);
  await page.goto("/pulls");

  const rows = page.locator(".pr-list-row");
  await expect(rows).toHaveCount(4);

  await rows.filter({ hasText: "Retry flaky upload" }).click({ modifiers: ["Shift"] });
  await rows.filter({ hasText: "Rename config loader" }).click({ modifiers: ["Shift"] });

  await expect(page.locator(".pr-list-row.multi-selected .title-text")).toHaveText([
    "Retry flaky upload",
    "Pin parser version",
    "Rename config loader",
  ]);
  // Selecting never opens a PR or selects the row text.
  await expect(page).toHaveURL(/\/pulls$/);
  expect(await page.evaluate(() => window.getSelection()?.toString() ?? "")).toBe("");

  await rows.filter({ hasText: "Pin parser version" }).click({ button: "right" });
  await page.getByRole("menuitem", { name: /Fix CI on 3 PRs/ }).click();

  const dialog = page.getByRole("dialog", { name: 'Run "Fix CI" on 3 PRs?' });
  await expect(dialog).toContainText("acme/widgets#21, acme/widgets#22, acme/widgets#23");
  await dialog.getByRole("button", { name: "Run on 3 PRs" }).click();

  await expect(page.locator(".kit-flash-stack")).toContainText('Started "Fix CI" in 3 workspaces.');
  await expect(dialog).toHaveCount(0);
  await expect(page.locator(".pr-list-row.multi-selected")).toHaveCount(0);

  // PRs without a workspace get one; the existing workspace is reused.
  expect(recorded.creates.map((body) => body["mr_number"]).sort()).toEqual([21, 23]);
  expect(recorded.creates[0]).toMatchObject({
    provider: "github",
    owner: "acme",
    name: "widgets",
    platform_repo_id: 1001,
  });
  expect(recorded.handoffs.map((handoff) => handoff.workspaceId).sort()).toEqual(["ws-21", "ws-22", "ws-23"]);
  expect(recorded.handoffs[0]?.body).toEqual({ target_key: "codex", message: fixCI.prompt });
});

test("Cmd/Ctrl-click toggles pull requests and Escape clears the selection", async ({ page }) => {
  await mockQuickActions(page);
  await page.goto("/pulls");

  const rows = page.locator(".pr-list-row");
  await rows.filter({ hasText: "Retry flaky upload" }).click({ modifiers: ["ControlOrMeta"] });
  await rows.filter({ hasText: "Untouched cleanup" }).click({ modifiers: ["ControlOrMeta"] });
  await expect(page.locator(".pr-list-row.multi-selected .title-text")).toHaveText([
    "Retry flaky upload",
    "Untouched cleanup",
  ]);
  await expect(page).toHaveURL(/\/pulls$/);

  await page.keyboard.press("Escape");
  await expect(page.locator(".pr-list-row.multi-selected")).toHaveCount(0);
});

test("right-clicking a single issue runs a quick action without confirmation", async ({ page }) => {
  const recorded = await mockQuickActions(page);
  await page.goto("/issues");

  const row = page.locator(".issue-item").filter({ hasText: "Theme toggle does not stick" });
  await row.click({ button: "right" });
  await page.getByRole("menuitem", { name: /^Fix CI/ }).click();

  await expect(page.locator(".kit-flash-stack")).toContainText('Started "Fix CI" in 1 workspace.');
  expect(recorded.creates).toHaveLength(1);
  expect(recorded.handoffs).toEqual([
    { workspaceId: "ws-issue-7", body: { target_key: "codex", message: fixCI.prompt } },
  ]);
});
