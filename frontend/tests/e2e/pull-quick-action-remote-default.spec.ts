import { expect, test } from "@playwright/test";

import { mockApi, mockSettings } from "./support/mockApi";

// The workspace pane beside a pull request shows only this machine's
// workspaces. A quick action that runs on another machine keeps the user on
// the pull request and offers that workspace from the message instead.
test("a quick action on a remote default machine keeps the pull request open", async ({ page }) => {
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

  await page.goto("/pulls/github/acme/widgets/42");
  const create = page.getByRole("button", { name: "Create Workspace", exact: true }).first();
  await expect(create).toBeEnabled();
  await page.getByRole("button", { name: "Quick actions" }).first().click();
  await page.getByRole("menuitem", { name: /Fix CI/ }).click();

  await expect(page.getByText('"Fix CI" is starting on compute-a (devbox).')).toBeVisible();
  await expect
    .poll(() => handoffs)
    .toEqual([expect.objectContaining({ target_key: "codex", message: "Make the failing checks pass." })]);
  await expect(page).toHaveURL(/\/pulls\/github\/acme\/widgets\/42$/);

  await page.getByRole("button", { name: "Open workspace" }).click();
  await expect(page).toHaveURL(/\/terminal\/fleet\/devbox%3Acompute-a\/ws-remote$/);
});
