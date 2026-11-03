import { expect, test, type Page } from "@playwright/test";

import { mockIssueFixture } from "../../src/test/mockApiFetch";
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
    merge_mutation: true,
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
      body: JSON.stringify({
        capabilities: repo.capabilities,
        operations: options.operations ?? {},
        AllowSquashMerge: true,
        AllowMergeCommit: false,
        AllowRebaseMerge: true,
      }),
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
    /\/api\/v1\/(pulls|issues)\/github\/acme\/widgets\/\d+\/(github-state|labels|assignees|merge|merge\/deferred)$/,
    async (route) => {
      const request = route.request();
      const path = new URL(request.url()).pathname;
      recorded.mutations.push({ request: `${request.method()} ${path}`, body: request.postDataJSON() as unknown });
      const body = path.endsWith("/merge") ? { merged: true, sha: "abc", message: "" } : {};
      await route.fulfill({ status: 200, contentType: "application/json", body: JSON.stringify(body) });
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
      body: JSON.stringify(
        [
          pull(21, "Retry flaky upload", "2026-03-30T16:00:00Z", { labels: [bug], assignees: ["octo"] }),
          pull(22, "Pin parser version", "2026-03-30T15:00:00Z", {
            IsDraft: true,
            workspace: { id: "ws-22", status: "ready" },
          }),
          pull(23, "Rename config loader", "2026-03-30T14:00:00Z", {
            labels: [{ name: "infra", color: "5319e7", is_default: false }],
            CIStatus: "pending",
          }),
          pull(24, "Untouched cleanup", "2026-03-30T13:00:00Z"),
        ].filter((pr) => pr.Title.toLowerCase().includes((url.searchParams.get("q") ?? "").toLowerCase())),
      ),
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

test("a repeated quick action reuses the workspace the first run created", async ({ page }) => {
  const recorded = await mockQuickActions(page);
  await page.goto("/pulls");
  const row = page.locator(".pr-list-row").filter({ hasText: "Retry flaky upload" });

  for (const run of [1, 2]) {
    await row.click({ button: "right" });
    await page.getByRole("menuitem", { name: /Fix CI/ }).click();
    await expect.poll(() => recorded.handoffs.length).toBe(run);
  }

  // The list row still shows no workspace, but the second run queues in
  // the one the first run created instead of creating another.
  expect(recorded.creates.map((body) => body["mr_number"])).toEqual([21]);
  expect(recorded.handoffs.map((handoff) => handoff.workspaceId)).toEqual(["ws-21", "ws-21"]);
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

test("Cmd/Ctrl+A selects every pull request the search leaves visible", async ({ page }) => {
  await mockQuickActions(page);
  await page.goto("/pulls");
  const rows = page.locator(".pr-list-row");
  await expect(rows).toHaveCount(4);

  const search = page.getByRole("searchbox", { name: "Search PRs" });
  await search.fill("re");
  await expect(rows).toHaveCount(2);

  // The search field keeps its own select-all.
  await search.press("ControlOrMeta+a");
  await expect(page.locator(".pr-list-row.multi-selected")).toHaveCount(0);
  expect(await search.evaluate((input: HTMLInputElement) => input.selectionEnd! - input.selectionStart!)).toBe(2);

  await rows.first().focus();
  await page.keyboard.press("ControlOrMeta+a");
  await expect(page.locator(".pr-list-row.multi-selected .title-text")).toHaveText([
    "Retry flaky upload",
    "Rename config loader",
  ]);
  await expect(page).toHaveURL(/\/pulls(\?.*)?$/);
  // The browser's own select-all would have selected the page text.
  expect(await page.evaluate(() => window.getSelection()?.toString() ?? "")).not.toContain("Retry flaky upload");

  await rows.first().click({ button: "right" });
  await expect(page.getByRole("menu", { name: "Actions for 2 PRs" })).toBeVisible();
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

test("Cmd/Ctrl+A selects every issue in the list", async ({ page }) => {
  await mockQuickActions(page);
  const second = { ...mockIssueFixture(7), ID: 3, GitHubID: 203, Number: 8, Title: "Export drops empty rows" };
  await page.route(/\/api\/v1\/issues(\?.*)?$/, async (route) => {
    await route.fulfill({ json: [mockIssueFixture(7), second] });
  });
  await page.goto("/issues");
  const rows = page.locator(".issue-item");
  await expect(rows).toHaveCount(2);

  await rows.first().focus();
  await page.keyboard.press("ControlOrMeta+a");
  await expect(page.locator(".issue-item.multi-selected")).toHaveCount(2);
  await expect(page).toHaveURL(/\/issues$/);
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

test("sends only the label changes for a selection", async ({ page }) => {
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
  // Only the change is sent; the server keeps labels it does not mention.
  for (const number of [21, 22, 23]) {
    expect(mutation(recorded, `PATCH /api/v1/pulls/github/acme/widgets/${number}/labels`)).toEqual([
      { add: ["docs"], remove: ["bug"] },
    ]);
  }
});

test("waits for a refreshing label list before applying", async ({ page }) => {
  const recorded = await mockQuickActions(page);
  let refreshed = false;
  await page.route("**/api/v1/repo/github/acme/widgets/labels", async (route) => {
    const done = refreshed;
    await route.fulfill({
      status: 200,
      contentType: "application/json",
      body: JSON.stringify(
        done ? { labels: [bug, docs], stale: false, syncing: false } : { labels: [bug], stale: true, syncing: true },
      ),
    });
  });
  await page.goto("/pulls");
  await selectFirstThreePulls(page);

  await page.locator(".pr-list-row").filter({ hasText: "Retry flaky upload" }).click({ button: "right" });
  await page.getByRole("menuitem", { name: "Edit labels..." }).click();

  const dialog = page.getByRole("dialog", { name: "Labels for 3 PRs" });
  const bugRow = dialog.getByRole("checkbox", { name: /bug/ });
  await bugRow.click();
  await bugRow.click();
  await expect(bugRow).toHaveAttribute("aria-checked", "false");
  await expect(dialog.getByRole("checkbox", { name: /docs/ })).toHaveCount(0);
  await expect(dialog.getByRole("button", { name: "Apply" })).toBeDisabled();

  refreshed = true;
  await dialog.getByRole("checkbox", { name: /docs/ }).click();
  await dialog.getByRole("button", { name: "Apply" }).click();

  await expect(page.locator(".kit-flash-stack")).toContainText("Updated labels on 3 PRs.");
  expect(mutation(recorded, "PATCH /api/v1/pulls/github/acme/widgets/21/labels")).toEqual([
    { add: ["docs"], remove: ["bug"] },
  ]);
});

test("only removes labels when the label list refresh failed", async ({ page }) => {
  const recorded = await mockQuickActions(page);
  // The server marks a failed refresh as checked, so it reports a current
  // list together with the error.
  await page.route("**/api/v1/repo/github/acme/widgets/labels", async (route) => {
    await route.fulfill({
      status: 200,
      contentType: "application/json",
      body: JSON.stringify({ labels: [bug, docs], stale: false, syncing: false, sync_error: "rate limited" }),
    });
  });
  await page.goto("/pulls");
  await selectFirstThreePulls(page);

  await page.locator(".pr-list-row").filter({ hasText: "Retry flaky upload" }).click({ button: "right" });
  await page.getByRole("menuitem", { name: "Edit labels..." }).click();

  const dialog = page.getByRole("dialog", { name: "Labels for 3 PRs" });
  await expect(dialog).toContainText("Could not load repository labels, so labels can only be removed.");
  await expect(dialog.getByRole("checkbox", { name: /docs/ })).toHaveCount(0);
  const bugRow = dialog.getByRole("checkbox", { name: /bug/ });
  await bugRow.click();
  await expect(bugRow).toHaveAttribute("aria-checked", "false");
  await dialog.getByRole("button", { name: "Apply" }).click();

  await expect(page.locator(".kit-flash-stack")).toContainText("Updated labels on 3 PRs.");
  expect(mutation(recorded, "PATCH /api/v1/pulls/github/acme/widgets/21/labels")).toEqual([
    { add: [], remove: ["bug"] },
  ]);
});

test("only removes labels when the repository label list cannot load", async ({ page }) => {
  const recorded = await mockQuickActions(page);
  await page.route("**/api/v1/repo/github/acme/widgets/labels", async (route) => {
    await route.fulfill({
      status: 503,
      contentType: "application/problem+json",
      body: JSON.stringify({ type: "about:blank", code: "service_unavailable", status: 503, detail: "unavailable" }),
    });
  });
  await page.goto("/pulls");
  await selectFirstThreePulls(page);

  await page.locator(".pr-list-row").filter({ hasText: "Retry flaky upload" }).click({ button: "right" });
  await page.getByRole("menuitem", { name: "Edit labels..." }).click();

  const dialog = page.getByRole("dialog", { name: "Labels for 3 PRs" });
  await expect(dialog).toContainText("Could not load repository labels, so labels can only be removed.");
  const bugRow = dialog.getByRole("checkbox", { name: /bug/ });
  await bugRow.click();
  await expect(bugRow).toHaveAttribute("aria-checked", "false");
  await bugRow.click();
  await expect(bugRow).toHaveAttribute("aria-checked", "mixed");
  await bugRow.click();
  await dialog.getByRole("button", { name: "Apply" }).click();

  await expect(page.locator(".kit-flash-stack")).toContainText("Updated labels on 3 PRs.");
  expect(mutation(recorded, "PATCH /api/v1/pulls/github/acme/widgets/21/labels")).toEqual([
    { add: [], remove: ["bug"] },
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
  for (const number of [21, 22, 23]) {
    expect(mutation(recorded, `PATCH /api/v1/pulls/github/acme/widgets/${number}/assignees`)).toEqual([
      { add: ["hubot"], remove: [] },
    ]);
  }
});

test("merges a selection of pull requests in the background, waiting for pending CI where needed", async ({ page }) => {
  const recorded = await mockQuickActions(page);
  // Hold one merge open to see the list while it runs.
  const held = Promise.withResolvers<void>();
  await page.route("**/api/v1/pulls/github/acme/widgets/21/merge", async (route) => {
    await held.promise;
    await route.fallback();
  });
  await page.goto("/pulls");
  await selectFirstThreePulls(page);

  await page.locator(".pr-list-row").filter({ hasText: "Retry flaky upload" }).click({ button: "right" });
  // The draft cannot be merged.
  await page.getByRole("menuitem", { name: "Merge 2 of 3 PRs..." }).click();

  const dialog = page.getByRole("dialog", { name: "Merge 2 PRs?" });
  await expect(dialog).toContainText("1 has pending CI and will merge once the checks pass.");
  await dialog.getByRole("radio", { name: "Rebase and merge" }).click();
  await dialog.getByRole("button", { name: "Merge 2 PRs" }).click();

  // The dialog closes while the merge runs; the row says so and cannot be merged again.
  await expect(dialog).toHaveCount(0);
  const row = page.locator(".pr-list-row").filter({ hasText: "Retry flaky upload" });
  await expect(row).toContainText("Merging");
  await row.click({ button: "right" });
  await expect(page.getByRole("menuitem", { name: /^Merge PR/ })).toContainText("Merging");
  await page.keyboard.press("Escape");
  held.resolve();

  await expect(page.locator(".kit-flash-stack")).toContainText("Merged 1 PR. 1 PR will merge once CI passes.");
  expect(mutation(recorded, "POST /api/v1/pulls/github/acme/widgets/21/merge")).toEqual([
    expect.objectContaining({ method: "rebase", commit_title: "Retry flaky upload (#21)" }),
  ]);
  expect(mutation(recorded, "POST /api/v1/pulls/github/acme/widgets/23/merge/deferred")).toEqual([
    expect.objectContaining({ method: "rebase" }),
  ]);
  expect(mutation(recorded, "POST /api/v1/pulls/github/acme/widgets/22/merge")).toEqual([]);
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

async function mockMergeQueue(page: Page): Promise<void> {
  const queued = (position: number) => ({
    merge_queue: {
      position,
      state: "awaiting_checks",
      enqueued_at: "2026-03-30T16:00:00Z",
      observed_at: "2026-03-30T16:00:00Z",
    },
  });
  await page.route("**/api/v1/pulls**", async (route) => {
    const url = new URL(route.request().url());
    if (url.pathname !== "/api/v1/pulls" || url.searchParams.get("attributes") !== "merge_queue") {
      await route.fallback();
      return;
    }
    await route.fulfill({
      status: 200,
      contentType: "application/json",
      body: JSON.stringify([
        pull(21, "Retry flaky upload", "2026-03-30T16:00:00Z", queued(1)),
        pull(22, "Pin parser version", "2026-03-30T15:00:00Z", queued(2)),
        pull(23, "Rename config loader", "2026-03-30T14:00:00Z", queued(3)),
        pull(24, "Untouched cleanup", "2026-03-30T13:00:00Z", {
          merge_queue_removal: { reason: "failed_checks", removed_at: "2026-03-30T13:30:00Z" },
        }),
      ]),
    });
  });
}

test("merge queue rows multi-select and the row menu acts on the whole selection", async ({ page }) => {
  const recorded = await mockQuickActions(page);
  await mockMergeQueue(page);
  await page.goto("/queues");

  const rows = page.locator(".merge-queue__row");
  await expect(rows).toHaveCount(4);
  const selected = page.locator(".merge-queue__row.multi-selected .merge-queue__pr-title");

  // A shift-click started with focus outside the list still lets Escape clear it.
  await page.getByRole("heading", { name: "Merge queues" }).click();
  await rows.filter({ hasText: "Pin parser version" }).click({ modifiers: ["Shift"] });
  await expect(selected).toHaveCount(1);
  await page.keyboard.press("Escape");
  await expect(selected).toHaveCount(0);

  // Shift-click selects the on-screen range, across queued and removed rows,
  // without opening a pull request.
  await rows.filter({ hasText: "Pin parser version" }).click({ modifiers: ["Shift"] });
  await rows.filter({ hasText: "Untouched cleanup" }).click({ modifiers: ["Shift"] });
  await expect(selected).toHaveText(["Pin parser version", "Rename config loader", "Untouched cleanup"]);
  await expect(page).toHaveURL(/\/queues$/);
  expect(await page.evaluate(() => window.getSelection()?.toString() ?? "")).toBe("");
  await expect(rows.filter({ hasText: "Pin parser version" })).toHaveAccessibleName(/#22 , selected /);
  await expect(rows.filter({ hasText: "Retry flaky upload" })).not.toHaveAccessibleName(/selected/);

  // Cmd/Ctrl-click toggles a row out; Escape clears; Cmd/Ctrl+A selects all.
  await rows.filter({ hasText: "Untouched cleanup" }).click({ modifiers: ["ControlOrMeta"] });
  await expect(selected).toHaveText(["Pin parser version", "Rename config loader"]);
  await page.keyboard.press("Escape");
  await expect(selected).toHaveCount(0);
  await rows.first().focus();
  await page.keyboard.press("ControlOrMeta+a");
  await expect(selected).toHaveCount(4);
  await page.keyboard.press("Escape");

  await rows.filter({ hasText: "Retry flaky upload" }).click({ modifiers: ["Shift"] });
  await rows.filter({ hasText: "Rename config loader" }).click({ modifiers: ["Shift"] });
  await rows.filter({ hasText: "Pin parser version" }).click({ button: "right" });
  await expect(page.getByRole("menu", { name: "Actions for 3 PRs" })).toBeVisible();
  await page.getByRole("menuitem", { name: "Close 3 PRs" }).click();
  await page.getByRole("dialog", { name: "Close 3 PRs?" }).getByRole("button", { name: "Close 3 PRs" }).click();
  await expect(page.locator(".kit-flash-stack")).toContainText("Closed 3 PRs.");
  for (const number of [21, 22, 23]) {
    expect(mutation(recorded, `POST /api/v1/pulls/github/acme/widgets/${number}/github-state`)).toEqual([
      { state: "closed" },
    ]);
  }
  expect(mutation(recorded, "POST /api/v1/pulls/github/acme/widgets/24/github-state")).toEqual([]);

  // Right-clicking a row outside the selection acts on that row alone.
  await rows.filter({ hasText: "Retry flaky upload" }).click({ modifiers: ["Shift"] });
  await rows.filter({ hasText: "Pin parser version" }).click({ modifiers: ["Shift"] });
  await rows.filter({ hasText: "Untouched cleanup" }).click({ button: "right" });
  await expect(page.getByRole("menu", { name: "Pull request actions" })).toBeVisible();
  await expect(selected).toHaveCount(0);
});

test("the focus list multi-selects pull requests and the row menu acts on the selection", async ({ page }) => {
  await mockQuickActions(page);
  await page.goto("/focus/mrs");

  const rows = page.locator(".pr-list-row");
  await expect(rows).toHaveCount(4);
  await rows.filter({ hasText: "Retry flaky upload" }).click({ modifiers: ["Shift"] });
  await rows.filter({ hasText: "Rename config loader" }).click({ modifiers: ["Shift"] });
  await expect(page.locator(".pr-list-row.multi-selected .title-text")).toHaveText([
    "Retry flaky upload",
    "Pin parser version",
    "Rename config loader",
  ]);
  await expect(page).toHaveURL(/\/focus\/mrs$/);

  await rows.filter({ hasText: "Pin parser version" }).click({ button: "right" });
  await expect(page.getByRole("menu", { name: "Actions for 3 PRs" })).toBeVisible();
  await page.keyboard.press("Escape");
  await page.keyboard.press("Escape");
  await expect(page.locator(".pr-list-row.multi-selected")).toHaveCount(0);
});

test("a merge queue row shows a merge in progress from its menu", async ({ page }) => {
  await mockQuickActions(page);
  await mockMergeQueue(page);
  const held = Promise.withResolvers<void>();
  await page.route("**/api/v1/pulls/github/acme/widgets/24/merge", async (route) => {
    await held.promise;
    await route.fallback();
  });
  await page.goto("/queues");

  const row = page.locator(".merge-queue__row").filter({ hasText: "Untouched cleanup" });
  await row.click({ button: "right" });
  await page.getByRole("menuitem", { name: /^Merge PR/ }).click();
  const dialog = page.getByRole("dialog", { name: /^Merge/ });
  await dialog.getByRole("button", { name: /^Merge/ }).click();

  await expect(dialog).toHaveCount(0);
  await expect(row).toContainText("Merging");
  held.resolve();
  await expect(row).not.toContainText("Merging");
});

test("the focus list multi-selects issues and the row menu acts only on the selection", async ({ page }) => {
  const recorded = await mockQuickActions(page);
  const issue = (number: number, title: string) => ({
    ...mockIssueFixture(7),
    ID: number,
    GitHubID: 200 + number,
    Number: number,
    Title: title,
  });
  // Closed issues leave the open list once the server has closed them.
  const closed = (number: number) =>
    mutation(recorded, `POST /api/v1/issues/github/acme/widgets/${number}/github-state`).length > 0;
  await page.route(/\/api\/v1\/issues(\?.*)?$/, async (route) => {
    const all = [
      issue(7, "Theme toggle does not stick"),
      issue(8, "Export drops empty rows"),
      issue(9, "Search ignores accents"),
    ];
    await route.fulfill({ json: all.filter((item) => !closed(item.Number)) });
  });
  await page.goto("/focus/issues");

  const rows = page.locator(".issue-item");
  await expect(rows).toHaveCount(3);
  await rows.filter({ hasText: "Theme toggle does not stick" }).click({ modifiers: ["Shift"] });
  await rows.filter({ hasText: "Export drops empty rows" }).click({ modifiers: ["Shift"] });
  await expect(page.locator(".issue-item.multi-selected")).toHaveCount(2);
  await expect(page).toHaveURL(/\/focus\/issues$/);

  await rows.filter({ hasText: "Export drops empty rows" }).click({ button: "right" });
  await page.getByRole("menuitem", { name: "Close 2 issues" }).click();
  await page.getByRole("dialog", { name: "Close 2 issues?" }).getByRole("button", { name: "Close 2 issues" }).click();
  await expect(page.locator(".kit-flash-stack")).toContainText("Closed 2 issues.");

  for (const number of [7, 8]) {
    expect(mutation(recorded, `POST /api/v1/issues/github/acme/widgets/${number}/github-state`)).toEqual([
      { state: "closed" },
    ]);
  }
  expect(mutation(recorded, "POST /api/v1/issues/github/acme/widgets/9/github-state")).toEqual([]);
  // The list reloads after the action.
  await expect(rows).toHaveText([/Search ignores accents/]);
});

const focusIssue = (number: number, title: string) => ({
  ...mockIssueFixture(7),
  ID: number,
  GitHubID: 200 + number,
  Number: number,
  Title: title,
});

// The same focus list serves pull requests and issues through separate stores.
const focusListKinds = [
  {
    kind: "pull requests",
    api: "/api/v1/pulls",
    listPage: "/pulls",
    focusPage: "/focus/mrs",
    row: ".pr-list-row",
    items: () => [
      pull(21, "Retry flaky upload", "2026-03-30T16:00:00Z"),
      pull(24, "Untouched cleanup", "2026-03-30T13:00:00Z"),
    ],
    target: { number: 24, title: "Untouched cleanup" },
    mutationPath: "POST /api/v1/pulls/github/acme/widgets/24/github-state",
    closedFlash: "Closed 1 PR.",
    empty: "No pull requests found.",
  },
  {
    kind: "issues",
    api: "/api/v1/issues",
    listPage: "/issues",
    focusPage: "/focus/issues",
    row: ".issue-item",
    items: () => [focusIssue(7, "Theme toggle does not stick"), focusIssue(8, "Export drops empty rows")],
    target: { number: 8, title: "Export drops empty rows" },
    mutationPath: "POST /api/v1/issues/github/acme/widgets/8/github-state",
    closedFlash: "Closed 1 issue.",
    empty: "No issues found.",
  },
] as const;

for (const list of focusListKinds) {
  // Serves the list without closed items, filtered by the search, and fails
  // searches while failSearch() is true.
  async function mockFocusList(page: Page, recorded: Recorded, failSearch: () => boolean = () => false) {
    await page.route(`**${list.api}**`, async (route) => {
      const url = new URL(route.request().url());
      if (route.request().method() !== "GET" || url.pathname !== list.api) {
        await route.fallback();
        return;
      }
      const query = (url.searchParams.get("q") ?? "").toLowerCase();
      if (query && failSearch()) {
        await route.fulfill({
          status: 500,
          contentType: "application/problem+json",
          body: JSON.stringify({ type: "about:blank", code: "internalError", status: 500, detail: "search failed" }),
        });
        return;
      }
      const closed = mutation(recorded, list.mutationPath).length > 0;
      const items: { Number: number; Title: string }[] = list.items();
      await route.fulfill({
        json: items.filter(
          (item) => !(closed && item.Number === list.target.number) && item.Title.toLowerCase().includes(query),
        ),
      });
    });
  }

  test(`a focus list action on ${list.kind} that finishes after leaving the list reloads the current list's query`, async ({
    page,
  }) => {
    const recorded = await mockQuickActions(page);
    await mockFocusList(page, recorded);
    const held = Promise.withResolvers<void>();
    await page.route(`**${list.mutationPath.replace("POST ", "")}`, async (route) => {
      await held.promise;
      await route.fallback();
    });
    const listQueries: URLSearchParams[] = [];
    page.on("request", (request) => {
      const url = new URL(request.url());
      if (request.method() === "GET" && url.pathname === list.api) listQueries.push(url.searchParams);
    });

    await page.goto(list.listPage);
    const rows = page.locator(list.row);
    await expect(rows).toHaveCount(2);
    // Open the focus list for one repository in-app, so Back returns to the list page.
    await page.evaluate((path) => {
      history.pushState(null, "", path);
      dispatchEvent(new PopStateEvent("popstate"));
    }, `${list.focusPage}?repo=acme/widgets`);
    await expect.poll(() => listQueries.some((query) => query.get("repo") === "acme/widgets")).toBe(true);

    await rows.filter({ hasText: list.target.title }).click({ button: "right" });
    await page.getByRole("menuitem", { name: /^Close/ }).click();
    await page.goBack();
    await expect(page).toHaveURL(new RegExp(`${list.listPage}$`));
    await expect(rows).toHaveCount(2);

    const before = listQueries.length;
    held.resolve();
    await expect(page.locator(".kit-flash-stack")).toContainText(list.closedFlash);
    await expect.poll(() => listQueries.length).toBeGreaterThan(before);
    // The reload keeps the list page's own query instead of the focus list's repository.
    expect(listQueries.slice(before).map((query) => query.get("repo"))).not.toContain("acme/widgets");
    await expect(rows).toHaveCount(1);
  });

  test(`a focus list action on ${list.kind} clears an earlier load error once the list reloads`, async ({ page }) => {
    const recorded = await mockQuickActions(page);
    let failSearch = false;
    await mockFocusList(page, recorded, () => failSearch);
    await page.goto(list.focusPage);
    const rows = page.locator(list.row);
    await expect(rows).toHaveCount(2);

    // A failed search keeps the rows on screen with the error stored.
    failSearch = true;
    const failed = page.waitForResponse(
      (response) => new URL(response.url()).pathname === list.api && response.status() === 500,
    );
    const search = page.getByRole("searchbox", { name: /^Search/ });
    await search.fill(list.target.title.split(" ")[0]!);
    await search.press("Enter");
    await failed;
    await expect(rows).toHaveCount(2);
    failSearch = false;

    await rows.filter({ hasText: list.target.title }).click({ button: "right" });
    await page.getByRole("menuitem", { name: /^Close/ }).click();
    await expect(page.locator(".kit-flash-stack")).toContainText(list.closedFlash);
    await expect(page.getByText(list.empty)).toBeVisible();
    await expect(page.getByText(/^Error:/)).toHaveCount(0);
  });
}
