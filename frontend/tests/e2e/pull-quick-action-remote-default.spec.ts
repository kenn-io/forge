import { expect, test } from "@playwright/test";

import { mockApi, mockSettings } from "./support/mockApi";

const peerNode = "b".repeat(32);

// The saved default can be a devbox or another Forge in the fleet (a hub or
// a spoke). Either way the workspace opens beside the pull request like a
// local one, with the machine named on the pane's tab.
const machines = [
  {
    kind: "devbox",
    defaultTarget: "devbox:compute-a",
    host: { configKey: "devbox:compute-a", kind: "devbox", name: "compute-a" },
    api: "/api/v1/devboxes/compute-a",
    tab: "Workspace on compute-a (devbox)",
  },
  {
    kind: "fleet member",
    defaultTarget: "https://build-b.example.test",
    host: { configKey: peerNode, kind: "remote", name: "build-b", baseURL: "https://build-b.example.test" },
    api: `/api/v1/fleet/hosts/${peerNode}`,
    tab: "Workspace on build-b",
  },
];

for (const machine of machines) {
  test(`a quick action on a ${machine.kind} default opens its workspace beside the pull request`, async ({ page }) => {
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
          workspaces: { ...mockSettings.workspaces, default_execution_target: machine.defaultTarget },
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
              ...machine.host,
              reachable: true,
              operationAvailability: { workspaceWrite: { available: true } },
            },
          ],
          workspaces: [],
        }),
      });
    });
    await page.route(`**${machine.api}/workspaces`, async (route) => {
      await route.fulfill({
        status: 202,
        contentType: "application/json",
        body: JSON.stringify({ id: "ws-remote", status: "creating", created: true }),
      });
    });
    await page.route(`**${machine.api}/workspaces/ws-remote/runtime/agent-handoffs`, async (route) => {
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

    // The pane beside the pull request loads the workspace from that machine.
    await page.route(`**${machine.api}/workspaces/ws-remote`, async (route) => {
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
      (url) => url.pathname === `${machine.api}/workspaces/ws-remote/runtime`,
      async (route) => {
        await route.fulfill({
          status: 200,
          contentType: "application/json",
          body: JSON.stringify({
            launch_targets: [
              { key: "devbox-agent", label: "Devbox Agent", kind: "agent", source: "builtin", available: true },
            ],
            sessions: [],
          }),
        });
      },
    );

    await page.goto("/pulls/github/acme/widgets/42");
    const create = page.getByRole("button", { name: "Create Workspace", exact: true }).first();
    await expect(create).toBeEnabled();
    await page.getByRole("button", { name: "Quick actions" }).first().click();
    await page.getByRole("menuitem", { name: /Fix CI/ }).click();

    await expect(page.getByRole("button", { name: `Hide ${machine.tab}` })).toBeVisible();
    // The branch comes from that machine's workspace, so this control appears
    // only once the pane has loaded it from that machine.
    await expect(page.getByRole("button", { name: "Delete workspace feature/remote" })).toBeVisible();
    await expect
      .poll(() => handoffs)
      .toEqual([expect.objectContaining({ target_key: "codex", message: "Make the failing checks pass." })]);
    await expect(page).toHaveURL(/\/pulls\/github\/acme\/widgets\/42$/);

    // The header launches into that machine's workspace, so it offers its agents.
    await page.getByRole("button", { name: "Launch an agent" }).first().click();
    await expect(page.getByRole("menuitem", { name: "Devbox Agent" })).toBeVisible();
  });
}
