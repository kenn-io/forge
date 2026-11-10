// The phone workspace item toolbar shares one row between the Back button,
// the Conversation and Files tabs, the item badge, and the shell menu. On the
// narrowest phones the tabs must shrink rather than scroll Files out of view.

import { afterEach, describe, expect, it, vi } from "vite-plus/test";
import { page } from "vite-plus/test/browser";

import { mountBrowserApp, resetKeyboardModuleState, type MountedBrowserApp } from "./test/browserAppHarness.js";
import { jsonResponse, type MockRouteOverride } from "./test/mockApiFetch.js";

const workspace = {
  id: "ws-9",
  platform_host: "github.com",
  repo_owner: "acme",
  repo_name: "widgets",
  repo: { provider: "github", platform_host: "github.com", owner: "acme", name: "widgets", repo_path: "acme/widgets" },
  tmux_activity_source: "none",
  tmux_last_output_at: null,
  tmux_working: false,
  item_type: "pull_request",
  // A five-digit number widens the toolbar badge.
  item_number: 12345,
  source_item_visible: true,
  git_head_ref: "feature/auth",
  worktree_path: "/tmp/worktrees/ws-9",
  tmux_session: "forge-test",
  status: "ready",
  enrichment_status: "fresh",
  created_at: "2026-04-10T12:00:00Z",
};

const routes: MockRouteOverride = (request) => {
  if (request.url.pathname === "/api/v1/snapshot") return jsonResponse({ hosts: [], workspaces: [workspace] });
  if (request.url.pathname === "/api/v1/workspaces/ws-9") return jsonResponse(workspace);
  return null;
};

describe("phone workspace item tabs", () => {
  let mounted: MountedBrowserApp | null = null;

  afterEach(async () => {
    mounted?.unmount();
    mounted = null;
    window.__KENN_FORGE_FORCE_MOBILE_ROUTES__ = false;
    localStorage.clear();
    await resetKeyboardModuleState();
  });

  it.each(["item", "item/files"])("keeps both tabs inside the toolbar at 320px on /%s", async (route) => {
    await page.viewport(320, 700);
    window.__KENN_FORGE_FORCE_MOBILE_ROUTES__ = true;
    mounted = await mountBrowserApp(`/m/workspaces/local/ws-9/${route}`, { overrides: [routes] });

    await vi.waitFor(() => expect(document.querySelectorAll(".mobile-workspace-item [role='tab']")).toHaveLength(2), {
      timeout: 10_000,
    });
    const strip = document
      .querySelector<HTMLElement>(".mobile-workspace-item [role='tablist']")!
      .getBoundingClientRect();
    for (const name of ["Conversation", "Files"]) {
      const tab = page.getByRole("tab", { name, exact: true }).element().getBoundingClientRect();
      expect(tab.left, name).toBeGreaterThanOrEqual(strip.left - 0.5);
      expect(tab.right, name).toBeLessThanOrEqual(strip.right + 0.5);
    }
  });
});
