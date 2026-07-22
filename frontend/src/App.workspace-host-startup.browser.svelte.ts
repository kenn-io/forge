import { afterEach, beforeEach, describe, expect, it, vi } from "vite-plus/test";
import { page } from "vite-plus/test/browser";

import { mountBrowserApp, type MountedBrowserApp } from "./test/browserAppHarness.js";
import { jsonResponse, type MockRouteOverride } from "./test/mockApiFetch.js";

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
  item_type: "pull_request",
  item_number: 42,
  git_head_ref: "feature/auth",
  worktree_path: "/tmp/worktrees/ws-9",
  tmux_session: "middleman-ws-9",
  status: "ready",
  created_at: "2026-04-10T12:00:00Z",
};

let mounted: MountedBrowserApp | null = null;

describe("workspace host startup gating (browser)", () => {
  vi.setConfig({ testTimeout: 20_000 });

  beforeEach(async () => {
    mounted = null;
    await page.viewport(1280, 900);
  });

  afterEach(() => {
    mounted?.unmount();
    mounted = null;
  });

  it("defers workspace requests on a direct terminal load until the backend is ready", async () => {
    let backendReady = false;
    const routes: MockRouteOverride = (req) => {
      if (req.url.pathname === "/healthz") {
        return backendReady ? null : jsonResponse({ status: "starting" }, 503);
      }
      if (req.method === "GET" && req.url.pathname === "/api/v1/workspaces/ws-9") {
        return jsonResponse(workspace);
      }
      if (req.method === "GET" && req.url.pathname === "/api/v1/workspaces/ws-9/runtime") {
        return jsonResponse({ launch_targets: [], sessions: [] });
      }
      if (req.method === "GET" && req.url.pathname === "/api/v1/workspaces") {
        return jsonResponse({ workspaces: [workspace] });
      }
      return null;
    };
    mounted = await mountBrowserApp("/terminal/ws-9", { overrides: [routes] });

    const workspaceRequests = () =>
      mounted?.api.requests.filter((req) => req.url.pathname.startsWith("/api/v1/workspaces/ws-9")).length ?? 0;

    // Startup polls /healthz every 750ms; give it more than one full cycle
    // while unready. The workspace host must hold off with the rest of the
    // shell — an early workspace fetch would park a failure in the error
    // state until manual retry, and settings arriving later would replace
    // an already-active terminal renderer.
    await new Promise((resolve) => setTimeout(resolve, 1600));
    expect(workspaceRequests()).toBe(0);

    backendReady = true;
    await vi.waitFor(() => {
      expect(workspaceRequests()).toBeGreaterThan(0);
    }, 15_000);
    await vi.waitFor(() => {
      expect(document.querySelector(".terminal-view")).not.toBeNull();
    }, 15_000);
  });
});
