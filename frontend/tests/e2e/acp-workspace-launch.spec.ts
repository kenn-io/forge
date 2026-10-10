import { devices, expect, test } from "@playwright/test";
import { createMockApiHandler } from "../../src/test/mockApiFetch";
import { mockApi, mockSettings } from "./support/mockApi";

const workspace = {
  id: "ws-chat",
  status: "creating",
  created_at: "2026-10-01T00:00:00Z",
  enrichment_status: "fresh",
  git_head_ref: "feature/chat",
  item_number: 42,
  item_type: "pull_request",
  platform_host: "github.com",
  repo_owner: "acme",
  repo_name: "widgets",
  tmux_session: "test-chat",
  worktree_path: "/tmp/test-chat",
  repo: { provider: "github", platform_host: "github.com", owner: "acme", name: "widgets", repo_path: "acme/widgets" },
};
const target = { key: "chat", label: "Chat agent", kind: "acp", available: true, source: "config" };
const session = {
  key: "chat-session",
  workspace_id: workspace.id,
  target_key: target.key,
  label: target.label,
  kind: "acp",
  status: "running",
  display_region: "workflow",
  created_at: workspace.created_at,
};

for (const mobile of [true, false]) {
  test.describe(mobile ? "phone ACP launch" : "desktop ACP launch", () => {
    if (mobile) {
      const { defaultBrowserType: _browser, ...phone } = devices["Pixel 5"];
      test.use(phone);
    }
    test.skip(({ browserName }) => mobile && browserName === "firefox", "Mobile emulation requires Chromium");

    for (const quick of [true, false]) {
      test(
        quick ? "PR quick action opens chat after startup" : "new workspace opens the selected chat after startup",
        async ({ page }, testInfo) => {
          await mockApi(page);
          let created = false;
          let ready = false;
          let launched = false;
          const releaseLaunch = Promise.withResolvers<void>();
          await page.route("**/api/v1/settings", (route) =>
            route.fulfill({
              json: {
                ...mockSettings,
                launch_targets: [target],
                quick_actions: [{ label: "Review change", agent: target.key, prompt: "Review this change" }],
              },
            }),
          );
          const api = createMockApiHandler();
          await page.route("**/api/v1/pulls/github/acme/widgets/42", async (route) => {
            const payload: unknown = await api
              .handle({ method: "GET", url: new URL(route.request().url()), bodyText: "" })
              .json();
            if (typeof payload !== "object" || payload === null) throw new Error("Missing pull detail fixture");
            await route.fulfill({
              json: {
                ...payload,
                workspace: created ? { id: workspace.id, status: ready ? "ready" : "creating" } : null,
              },
            });
          });
          await page.route("**/api/v1/workspaces", (route) => {
            if (route.request().method() === "POST") created = true;
            return route.fulfill({ status: created ? 202 : 200, json: created ? workspace : { workspaces: [] } });
          });
          await page.route("**/api/v1/repo/github/acme/widgets/workspaces", (route) => {
            created = true;
            return route.fulfill({ status: 202, json: workspace });
          });
          await page.route("**/api/v1/workspaces/ws-chat", (route) =>
            route.fulfill({ json: { ...workspace, status: ready ? "ready" : "creating" } }),
          );
          await page.route("**/api/v1/workspaces/ws-chat/runtime", (route) =>
            route.fulfill({
              json: {
                launch_targets: [target],
                sessions: launched ? [session] : [],
              },
            }),
          );
          await page.route("**/api/v1/workspaces/ws-chat/runtime/sessions", async (route) => {
            await releaseLaunch.promise;
            launched = true;
            await route.fulfill({ json: session });
          });
          await page.route("**/api/v1/workspaces/ws-chat/runtime/agent-handoffs", async (route) => {
            await releaseLaunch.promise;
            launched = true;
            await route.fulfill({ json: { session, initial_message: { state: "delivered" } } });
          });

          if (quick) {
            await page.goto("/pulls/github/acme/widgets/42");
            await page.getByRole("button", { name: "Quick actions", exact: true }).click();
            await page.getByRole("menuitem", { name: /Review change/ }).click();
          } else {
            await page.goto(mobile ? "/m/workspaces" : "/workspaces");
            if (mobile) {
              await page.getByRole("button", { name: "New workspace", exact: true }).click();
            } else {
              await page
                .getByRole("button", { name: /New workspace/i })
                .first()
                .click();
            }
            const dialog = page.getByRole("dialog", { name: "New workspace" });
            await dialog.getByRole("button", { name: "Create workspace options" }).click();
            await page.getByRole("menuitem", { name: target.label }).click();
          }
          await expect(page.getByText(/Preparing worktree/)).toBeVisible();
          ready = true;
          await expect(page.getByText(mobile ? "Starting Chat agent…" : "Launching Chat agent...")).toBeVisible({
            timeout: 15_000,
          });
          await expect(page.getByRole("button", { name: "Open terminal composer" })).toHaveCount(0);
          await page.screenshot({ path: testInfo.outputPath("acp-startup.png") });
          releaseLaunch.resolve();
          if (mobile) await expect(page.getByRole("button", { name: "Chat options" })).toBeVisible();
          else await expect(page.locator(".acp-workspace")).toBeVisible();
          await expect(page.getByRole("button", { name: "Open terminal composer" })).toHaveCount(0);
        },
      );
    }
  });
}
