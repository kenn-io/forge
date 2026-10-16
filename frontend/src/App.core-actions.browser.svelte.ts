import { afterEach, beforeEach, describe, expect, it, vi } from "vite-plus/test";
import { page } from "vite-plus/test/browser";
import {
  mountBrowserApp,
  resetKeyboardModuleState,
  type MountedBrowserApp,
} from "./test/browserAppHarness.js";
import { jsonResponse, type MockRouteOverride } from "./test/mockApiFetch.js";

const WAIT = 10_000;
let mounted: MountedBrowserApp | null = null;
let captures: unknown[] = [];
const capture: MockRouteOverride = (request) => {
  if (request.method === "POST" && request.url.pathname === "/api/v1/telemetry/events") {
    captures.push(JSON.parse(request.bodyText));
    return jsonResponse({ status: "queued" });
  }
  return null;
};
const prVisits = () => captures.filter((body) => (body as { event: string }).event === "pr_viewed");

describe("core action navigation", () => {
  beforeEach(async () => {
    captures = [];
    await page.viewport(1280, 900);
  });
  afterEach(async () => {
    mounted?.unmount();
    mounted = null;
    window.__KENN_FORGE_FORCE_MOBILE_ROUTES__ = false;
    localStorage.clear();
    await resetKeyboardModuleState();
  });

  it("counts a phone workspace-list linked PR without opening its terminal", async () => {
    window.__KENN_FORGE_FORCE_MOBILE_ROUTES__ = true;
    const workspace = {
      id: "ws-9",
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
      tmux_activity_source: "none",
      tmux_last_output_at: null,
      tmux_working: false,
      item_type: "pull_request",
      item_number: 42,
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
    mounted = await mountBrowserApp("/m/workspaces", { overrides: [capture, routes] });
    await page.getByRole("button", { name: "Open linked item #42" }).click();
    await vi.waitFor(() => expect(window.location.pathname).toBe("/m/workspaces/local/ws-9/item"), WAIT);
    await vi.waitFor(
      () => expect(prVisits()).toEqual([{ event: "pr_viewed", properties: { provider: "github", surface: "web" } }]),
      WAIT,
    );
    expect(captures.filter((body) => (body as { event: string }).event === "workspace_opened")).toEqual([]);
  });
});
