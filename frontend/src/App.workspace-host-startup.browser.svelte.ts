import { afterEach, beforeEach, describe, expect, it, vi } from "vite-plus/test";
import { page } from "vite-plus/test/browser";

import { mountBrowserApp, type MountedBrowserApp } from "./test/browserAppHarness.js";
import { createMockApiHandler, jsonResponse, type MockRouteOverride } from "./test/mockApiFetch.js";

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
  source_item_visible: true,
  git_head_ref: "feature/auth",
  worktree_path: "/tmp/worktrees/ws-9",
  tmux_session: "kenn-forge-ws-9",
  status: "ready",
  enrichment_status: "fresh",
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
    localStorage.clear();
  });

  it.each([
    ["pull_request", "pr", "pulls", 42],
    ["issue", "issue", "issues", 7],
  ] as const)(
    "rejects another repository's detail when opening a %s workspace",
    async (itemType, tab, kind, number) => {
      const fixtures = createMockApiHandler();
      const detailPath = `/api/v1/${kind}/github/acme/widgets/${number}`;
      const replacement = await fixtures
        .handle({
          method: "GET",
          url: new URL(detailPath, "http://localhost"),
          bodyText: "",
        })
        .json();
      replacement.repo.platform_repo_id = "R_replacement";
      (replacement.merge_request ?? replacement.issue).Title = "Replacement repository detail";
      const pinnedWorkspace = {
        ...workspace,
        repo: { ...workspace.repo, platform_repo_id: "R_original" },
        item_type: itemType,
        item_number: number,
      };
      let completeRead: (() => void) | undefined;
      const routes: MockRouteOverride = (request) => {
        if (request.url.pathname === "/api/v1/workspaces") return jsonResponse({ workspaces: [pinnedWorkspace] });
        if (request.url.pathname === "/api/v1/workspaces/ws-9") return jsonResponse(pinnedWorkspace);
        if (request.url.pathname === "/api/v1/workspaces/ws-9/runtime")
          return jsonResponse({ launch_targets: [], sessions: [] });
        if (request.url.pathname === `${detailPath}/sync`) return jsonResponse(replacement);
        if (request.method === "GET" && request.url.pathname === detailPath) {
          return new Response(
            new ReadableStream<Uint8Array>({
              start(controller) {
                completeRead = () => {
                  controller.enqueue(new TextEncoder().encode(JSON.stringify(replacement)));
                  controller.close();
                };
              },
            }),
            { headers: { "content-type": "application/json" } },
          );
        }
        return null;
      };
      localStorage.setItem("kenn-forge-workspace-sidebar-open", "true");
      localStorage.setItem("kenn-forge-workspace-sidebar-tab:ws-9", tab);
      mounted = await mountBrowserApp("/terminal/ws-9", { overrides: [routes] });
      const sidebar = page.getByRole("region", { name: "Workspace details pane" });
      const loading = sidebar.getByText(/^Loading(?:…|\.\.\.)$/);
      await expect.element(loading).toBeVisible();
      completeRead!();
      await expect.element(loading).not.toBeInTheDocument();
      expect(sidebar.element().textContent).not.toContain("Replacement repository detail");
    },
  );

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
    // state until manual retry.
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
