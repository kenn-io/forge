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
  mr_title: "Workspace title",
};

let mounted: MountedBrowserApp | null = null;
let backendReady = true;
const routes: MockRouteOverride = (req) => {
  if (req.url.pathname === "/healthz") return backendReady ? null : jsonResponse({ status: "starting" }, 503);
  if (req.method === "GET" && req.url.pathname === "/api/v1/workspaces/ws-9") return jsonResponse(workspace);
  if (req.method === "GET" && req.url.pathname === "/api/v1/workspaces/ws-9/runtime")
    return jsonResponse({ launch_targets: [], sessions: [] });
  if (req.method === "GET" && req.url.pathname === "/api/v1/workspaces")
    return jsonResponse({ workspaces: [workspace] });
  return null;
};

describe("workspace host startup gating (browser)", () => {
  vi.setConfig({ testTimeout: 20_000 });

  beforeEach(async () => {
    mounted = null;
    backendReady = true;
    await page.viewport(1280, 900);
  });

  afterEach(async () => {
    mounted?.unmount();
    mounted = null;
    localStorage.clear();
    window.__KENN_FORGE_FORCE_MOBILE_ROUTES__ = false;
    await page.viewport(1280, 900);
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
      replacement.repo.platform_repo_id = 1103;
      (replacement.merge_request ?? replacement.issue).Title = "Replacement repository detail";
      const pinnedWorkspace = {
        ...workspace,
        repo: { ...workspace.repo, platform_repo_id: 1104 },
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
    backendReady = false;
    mounted = await mountBrowserApp("/terminal/ws-9", { overrides: [routes] });

    const workspaceRequests = () =>
      mounted?.api.requests.filter((req) => req.url.pathname.startsWith("/api/v1/workspaces/ws-9")).length ?? 0;

    // Startup polls /healthz every 750ms; give it more than one full cycle
    // while unready. The workspace host must hold off with the rest of the
    // shell — an early workspace fetch would park a failure in the error
    // state until manual retry.
    await new Promise((resolve) => setTimeout(resolve, 1600));
    expect(workspaceRequests()).toBe(0);
    expect(document.title).toBe("Workspace (ws-9) · Workspaces · Forge");

    backendReady = true;
    await vi.waitFor(() => {
      expect(workspaceRequests()).toBeGreaterThan(0);
    }, 15_000);
    await vi.waitFor(() => {
      expect(document.querySelector(".terminal-view")).not.toBeNull();
    }, 15_000);
    await vi.waitFor(() => expect(document.title).toBe("Workspace title · Workspaces · Forge"), 15_000);
  });

  it("names the linked item while navigating the phone workspace", async () => {
    await page.viewport(390, 844);
    window.__KENN_FORGE_FORCE_MOBILE_ROUTES__ = true;
    mounted = await mountBrowserApp("/m/workspaces/local/ws-9", { overrides: [routes] });
    await expect.element(page.getByRole("button", { name: "Open linked PR #42" }), { timeout: 10_000 }).toBeVisible();
    await vi.waitFor(() => expect(document.title).toBe("Workspace title · Workspaces · Forge"), 10_000);
    await page.getByRole("button", { name: "Open linked PR #42" }).click();
    await expect
      .element(page.getByRole("tab", { name: "Conversation", exact: true }), { timeout: 10_000 })
      .toBeVisible();
    await vi.waitFor(
      () => expect(document.title).toBe("Add browser regression coverage (acme/widgets#42) · Forge"),
      10_000,
    );
    await page.getByRole("tab", { name: "Files", exact: true }).click();
    await vi.waitFor(() => expect(window.location.pathname).toBe("/m/workspaces/local/ws-9/item/files"), 10_000);
    expect(document.title).toBe("Add browser regression coverage (acme/widgets#42) · Forge");
    await page.getByRole("button", { name: "Back to workspace terminal" }).click();
    await expect.element(page.getByRole("button", { name: "Open linked PR #42" }), { timeout: 10_000 }).toBeVisible();
    await vi.waitFor(() => expect(document.title).toBe("Workspace title · Workspaces · Forge"), 10_000);
  }, 30_000);

  it("distinguishes an empty linked-item screen from its terminal", async () => {
    await page.viewport(390, 844);
    window.__KENN_FORGE_FORCE_MOBILE_ROUTES__ = true;
    const empty = { ...workspace, item_type: "adhoc", item_number: 0, source_item_visible: false, mr_title: null };
    const emptyRoute: MockRouteOverride = (req) =>
      req.method === "GET" && req.url.pathname === "/api/v1/workspaces/ws-9" ? jsonResponse(empty) : null;
    mounted = await mountBrowserApp("/m/workspaces/local/ws-9/item", { overrides: [emptyRoute, routes] });
    await expect.element(page.getByText("No linked PR or issue"), { timeout: 10_000 }).toBeVisible();
    await vi.waitFor(() => expect(document.title).toBe("Linked item (feature/auth) · Workspaces · Forge"), 10_000);
  }, 30_000);
});
