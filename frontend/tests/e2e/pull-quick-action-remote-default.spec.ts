import { expect, test } from "@playwright/test";

import { mockApi, mockSettings } from "./support/mockApi";

// A quick action on another machine opens its workspace beside the pull
// request like a local one, with the machine named on the pane's tab.
test("a quick action on a remote default machine opens its workspace beside the pull request", async ({ page }) => {
  const handoffs: unknown[] = [];
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
        workspaces: { ...mockSettings.workspaces, default_execution_target: "devbox:compute-a" },
        quick_actions: [{ label: "Fix CI", agent: "codex", prompt: "Make the failing checks pass." }],
      }),
    });
  });
  await page.route("**/api/v1/snapshot**", async (route) => {
    await route.fulfill({
      status: 200,
      contentType: "application/json",
      body: JSON.stringify({
        hosts: [
          {
            configKey: "devbox:compute-a",
            kind: "devbox",
            name: "compute-a",
            reachable: true,
            operationAvailability: { workspaceWrite: { available: true } },
          },
        ],
        workspaces: [],
      }),
    });
  });
  await page.route("**/api/v1/devboxes/compute-a/workspaces", async (route) => {
    await route.fulfill({
      status: 202,
      contentType: "application/json",
      body: JSON.stringify({ id: "ws-remote", status: "creating", created: true }),
    });
  });
  await page.route("**/api/v1/devboxes/compute-a/workspaces/ws-remote/runtime/agent-handoffs", async (route) => {
    handoffs.push(route.request().postDataJSON());
    await route.fulfill({
      status: 201,
      contentType: "application/json",
      body: JSON.stringify({
        initial_message: { state: "delivered" },
        session: { key: "session-1", target_key: "codex", label: "Codex", kind: "agent", status: "running" },
      }),
    });
  });

  // The pane beside the pull request loads the workspace from the devbox.
  await page.route("**/api/v1/devboxes/compute-a/workspaces/ws-remote", async (route) => {
    await route.fulfill({
      status: 200,
      contentType: "application/json",
      body: JSON.stringify({
        id: "ws-remote",
        platform_host: "github.com",
        repo_owner: "acme",
        repo_name: "widgets",
        repo: {
          provider: "github",
          platform_host: "github.com",
          owner: "acme",
          name: "widgets",
          repo_path: "acme/widgets",
        },
        item_type: "pull_request",
        item_number: 42,
        source_item_visible: true,
        git_head_ref: "feature/remote",
        worktree_path: "/tmp/worktrees/ws-remote",
        tmux_session: "kenn-forge-ws-remote",
        tmux_pane_title: null,
        tmux_working: false,
        tmux_activity_source: "unknown",
        tmux_last_output_at: null,
        status: "ready",
        created_at: "2026-04-10T12:00:00Z",
        enrichment_status: "fresh",
        mr_title: "Remote widget fix",
        mr_state: "open",
        mr_is_draft: false,
      }),
    });
  });
  await page.route(
    (url) => url.pathname === "/api/v1/devboxes/compute-a/workspaces/ws-remote/runtime",
    async (route) => {
      await route.fulfill({
        status: 200,
        contentType: "application/json",
        body: JSON.stringify({ launch_targets: [], sessions: [] }),
      });
    },
  );

  await page.goto("/pulls/github/acme/widgets/42");
  const create = page.getByRole("button", { name: "Create Workspace", exact: true }).first();
  await expect(create).toBeEnabled();
  await page.getByRole("button", { name: "Quick actions" }).first().click();
  await page.getByRole("menuitem", { name: /Fix CI/ }).click();

  await expect(page.getByRole("button", { name: "Hide Workspace on compute-a (devbox)" })).toBeVisible();
  await expect(page.getByRole("img", { name: "Workspace load failed" })).toHaveCount(0);
  await expect
    .poll(() => handoffs)
    .toEqual([expect.objectContaining({ target_key: "codex", message: "Make the failing checks pass." })]);
  await expect(page).toHaveURL(/\/pulls\/github\/acme\/widgets\/42$/);
});
