import { expect, test, type Page } from "@playwright/test";

import { mockApi, mockSettings } from "./support/mockApi";

const repo = {
  provider: "github",
  platform_host: "github.com",
  repo_path: "acme/widgets",
  owner: "acme",
  name: "widgets",
  platform_repo_id: 1001,
  capabilities: {
    state_mutation: true,
    draft_mutation: true,
    ready_for_review: true,
    read_labels: true,
    label_mutation: true,
    assignee_mutation: true,
  },
};

const bug = { name: "bug", color: "d73a4a", is_default: true };
const docs = { name: "docs", color: "0075ca", is_default: true };

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
  /** Item mutations as "METHOD /path" with their JSON body. */
  mutations: { request: string; body: unknown }[];
}

async function mockQuickActions(
  page: Page,
  options: {
    operations?: Record<string, unknown>;
    /** Workspaces whose agent handoff is held until the returned release is called, then fails. */
    failingHandoffs?: { workspaceIds: string[]; release: Promise<void> };
  } = {},
): Promise<Recorded> {
  const recorded: Recorded = { creates: [], handoffs: [], mutations: [] };
  await mockApi(page);
  await page.route("**/api/v1/repo/github/acme/widgets", async (route) => {
    await route.fulfill({
      status: 200,
      contentType: "application/json",
      body: JSON.stringify({ capabilities: repo.capabilities, operations: options.operations ?? {} }),
    });
  });
  await page.route("**/api/v1/repo/github/acme/widgets/labels", async (route) => {
    await route.fulfill({
      status: 200,
      contentType: "application/json",
      body: JSON.stringify({ labels: [bug, docs], stale: false, syncing: false }),
    });
  });
  await page.route("**/api/v1/repo/github/acme/widgets/comment-autocomplete**", async (route) => {
    await route.fulfill({
      status: 200,
      contentType: "application/json",
      body: JSON.stringify({ users: ["octo", "hubot"] }),
    });
  });
  await page.route(
    /\/api\/v1\/(pulls|issues)\/github\/acme\/widgets\/\d+\/(github-state|labels|assignees|state)$/,
    async (route) => {
      const request = route.request();
      recorded.mutations.push({
        request: `${request.method()} ${new URL(request.url()).pathname}`,
        body: request.postDataJSON() as unknown,
      });
      await route.fulfill({ status: 200, contentType: "application/json", body: "{}" });
    },
  );
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
        pull(21, "Retry flaky upload", "2026-03-30T16:00:00Z", { labels: [bug], assignees: ["octo"] }),
        pull(22, "Pin parser version", "2026-03-30T15:00:00Z", {
          IsDraft: true,
          workspace: { id: "ws-22", status: "ready" },
        }),
        pull(23, "Rename config loader", "2026-03-30T14:00:00Z", {
          labels: [{ name: "infra", color: "5319e7", is_default: false }],
        }),
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
    const failing = options.failingHandoffs;
    if (failing?.workspaceIds.includes(workspaceId)) {
      await failing.release;
      await route.fulfill({
        status: 409,
        contentType: "application/problem+json",
        body: JSON.stringify({ type: "about:blank", code: "conflict", status: 409, detail: "agent is not installed" }),
      });
      return;
    }
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

test("reports bulk quick action launches once they have all finished", async ({ page }) => {
  const release = Promise.withResolvers<void>();
  await mockQuickActions(page, { failingHandoffs: { workspaceIds: ["ws-23"], release: release.promise } });
  await page.goto("/pulls");

  const rows = page.locator(".pr-list-row");
  await rows.filter({ hasText: "Retry flaky upload" }).click({ modifiers: ["Shift"] });
  await rows.filter({ hasText: "Rename config loader" }).click({ modifiers: ["Shift"] });
  await rows.filter({ hasText: "Pin parser version" }).click({ button: "right" });
  await page.getByRole("menuitem", { name: /Fix CI on 3 PRs/ }).click();
  await page.getByRole("button", { name: "Run on 3 PRs" }).click();

  // Launching continues in the background; nothing reports success early.
  await expect(page.getByRole("dialog", { name: 'Run "Fix CI" on 3 PRs?' })).toHaveCount(0);
  await expect(page.locator(".pr-list-row.multi-selected")).toHaveCount(0);
  await expect(page.locator(".kit-flash-stack", { hasText: "Fix CI" })).toHaveCount(0);
  const flashes = page.locator(".kit-flash-stack");

  release.resolve();
  await expect(flashes).toContainText(
    '"Fix CI" on 3 items: started 2, failed 1. acme/widgets#23: agent is not installed.',
  );
  await expect(flashes).not.toContainText('Started "Fix CI"');
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

function mutation(recorded: Recorded, request: string): unknown[] {
  return recorded.mutations.filter((entry) => entry.request === request).map((entry) => entry.body);
}

async function selectFirstThreePulls(page: Page): Promise<void> {
  const rows = page.locator(".pr-list-row");
  await expect(rows).toHaveCount(4);
  await rows.filter({ hasText: "Retry flaky upload" }).click({ modifiers: ["Shift"] });
  await rows.filter({ hasText: "Rename config loader" }).click({ modifiers: ["Shift"] });
  await expect(page.locator(".pr-list-row.multi-selected")).toHaveCount(3);
}

test("closes a selection of pull requests after one confirmation", async ({ page }) => {
  const recorded = await mockQuickActions(page);
  await page.goto("/pulls");
  await selectFirstThreePulls(page);

  await page.locator(".pr-list-row").filter({ hasText: "Pin parser version" }).click({ button: "right" });
  // Only the draft PR can be marked ready, and only open non-drafts converted.
  await expect(page.getByRole("menuitem", { name: "Mark ready for review 1 of 3 PRs" })).toBeVisible();
  await expect(page.getByRole("menuitem", { name: "Convert to draft 2 of 3 PRs" })).toBeVisible();
  await expect(page.getByRole("menuitem", { name: /^Reopen/ })).toHaveCount(0);
  await page.getByRole("menuitem", { name: "Close 3 PRs" }).click();

  const dialog = page.getByRole("dialog", { name: "Close 3 PRs?" });
  await dialog.getByRole("button", { name: "Close 3 PRs" }).click();
  await expect(page.locator(".kit-flash-stack")).toContainText("Closed 3 PRs.");

  for (const number of [21, 22, 23]) {
    expect(mutation(recorded, `POST /api/v1/pulls/github/acme/widgets/${number}/github-state`)).toEqual([
      { state: "closed" },
    ]);
  }
  expect(mutation(recorded, "POST /api/v1/pulls/github/acme/widgets/24/github-state")).toEqual([]);
});

test("adds and removes labels across a selection, keeping other labels", async ({ page }) => {
  const recorded = await mockQuickActions(page);
  await page.goto("/pulls");
  await selectFirstThreePulls(page);

  await page.locator(".pr-list-row").filter({ hasText: "Retry flaky upload" }).click({ button: "right" });
  await page.getByRole("menuitem", { name: "Edit labels..." }).click();

  const dialog = page.getByRole("dialog", { name: "Labels for 3 PRs" });
  const bugRow = dialog.getByRole("checkbox", { name: /bug/ });
  const docsRow = dialog.getByRole("checkbox", { name: /docs/ });
  await expect(bugRow).toHaveAttribute("aria-checked", "mixed");
  await expect(docsRow).toHaveAttribute("aria-checked", "false");

  await docsRow.click();
  await bugRow.click();
  await bugRow.click();
  await expect(docsRow).toHaveAttribute("aria-checked", "true");
  await expect(bugRow).toHaveAttribute("aria-checked", "false");
  await dialog.getByRole("button", { name: "Apply" }).click();

  await expect(page.locator(".kit-flash-stack")).toContainText("Updated labels on 3 PRs.");
  for (const number of [21, 22]) {
    expect(mutation(recorded, `PUT /api/v1/pulls/github/acme/widgets/${number}/labels`)).toEqual([
      { labels: ["docs"] },
    ]);
  }
  expect(mutation(recorded, "PUT /api/v1/pulls/github/acme/widgets/23/labels")).toEqual([
    { labels: ["infra", "docs"] },
  ]);
});

test("adds an assignee to every selected pull request", async ({ page }) => {
  const recorded = await mockQuickActions(page);
  await page.goto("/pulls");
  await selectFirstThreePulls(page);

  await page.locator(".pr-list-row").filter({ hasText: "Retry flaky upload" }).click({ button: "right" });
  await page.getByRole("menuitem", { name: "Edit assignees..." }).click();

  const dialog = page.getByRole("dialog", { name: "Assignees for 3 PRs" });
  await dialog.getByRole("checkbox", { name: /hubot/ }).click();
  await dialog.getByRole("button", { name: "Apply" }).click();

  await expect(page.locator(".kit-flash-stack")).toContainText("Updated assignees on 3 PRs.");
  expect(mutation(recorded, "PUT /api/v1/pulls/github/acme/widgets/21/assignees")).toEqual([
    { assignees: ["octo", "hubot"] },
  ]);
  expect(mutation(recorded, "PUT /api/v1/pulls/github/acme/widgets/22/assignees")).toEqual([{ assignees: ["hubot"] }]);
});

test("sets the workflow status of a single pull request from its menu", async ({ page }) => {
  const recorded = await mockQuickActions(page);
  await page.goto("/pulls");

  await page.locator(".pr-list-row").filter({ hasText: "Untouched cleanup" }).click({ button: "right" });
  await page.getByRole("menuitemradio", { name: "Reviewing" }).click();

  await expect
    .poll(() => mutation(recorded, "PUT /api/v1/pulls/github/acme/widgets/24/state"))
    .toEqual([{ status: "reviewing" }]);
});

test("disables actions the repository cannot perform and shows why", async ({ page }) => {
  const recorded = await mockQuickActions(page, {
    operations: {
      close_pr: {
        available: false,
        code: "missing_write_credential",
        unavailable_reason: "Sign in to change pull requests on github.com",
      },
    },
  });
  await page.goto("/pulls");

  await page.locator(".pr-list-row").filter({ hasText: "Untouched cleanup" }).click({ button: "right" });
  const close = page.getByRole("menuitem", { name: /^Close PR/ });
  await expect(close).toBeDisabled();
  await expect(close).toContainText("Sign in to change pull requests on github.com");
  expect(recorded.mutations).toEqual([]);
});

test("closes a single issue from its menu without confirmation", async ({ page }) => {
  const recorded = await mockQuickActions(page);
  await page.goto("/issues");

  await page.locator(".issue-item").filter({ hasText: "Theme toggle does not stick" }).click({ button: "right" });
  await expect(page.getByRole("menuitem", { name: /^Convert to draft/ })).toHaveCount(0);
  await page.getByRole("menuitem", { name: "Close issue" }).click();

  await expect(page.locator(".kit-flash-stack")).toContainText("Closed 1 issue.");
  expect(mutation(recorded, "POST /api/v1/issues/github/acme/widgets/7/github-state")).toEqual([{ state: "closed" }]);
});
