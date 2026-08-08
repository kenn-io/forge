import { render } from "vitest-browser-svelte";
import { describe, expect, it, vi } from "vite-plus/test";
import { DEFAULT_TERMINAL_SETTINGS } from "../../api/types.js";

import { STORES_KEY } from "../../context.js";
import { createMockApiFetch, jsonResponse, type MockRouteOverride } from "../../../test/mockApiFetch.js";
import type { WorkspaceRuntimeState } from "../../api/workspace-runtime.ts";
import WorkspaceTerminalView from "./WorkspaceTerminalView.svelte";

const WAIT = 10_000;

const workspace = {
  id: "ws-1",
  platform_host: "github.com",
  repo_owner: "acme",
  repo_name: "widget",
  repo: {
    provider: "github",
    platform_host: "github.com",
    owner: "acme",
    name: "widget",
    repo_path: "acme/widget",
  },
  item_type: "pull_request",
  item_number: 7,
  git_head_ref: "feature/session-exit",
  worktree_path: "/tmp/worktree",
  tmux_session: "kenn-forge-ws-1",
  status: "ready",
  enrichment_status: "fresh",
  created_at: "2026-04-29T00:00:00Z",
};

const emptyRuntime: WorkspaceRuntimeState = { launch_targets: [], sessions: [] };

function workspaceRoutes(runtime: WorkspaceRuntimeState = emptyRuntime): MockRouteOverride {
  return (req) => {
    if (req.method !== "GET") return null;
    if (req.url.pathname === "/api/v1/workspaces/ws-1") return jsonResponse(workspace);
    if (req.url.pathname === "/api/v1/workspaces/ws-1/runtime") return jsonResponse(runtime);
    if (req.url.pathname === "/api/v1/workspaces") return jsonResponse({ workspaces: [workspace] });
    return null;
  };
}

// A no-op EventSource: WTV opens one to watch for workspace/diff invalidation
// events, and a real EventSource would spin retrying against a backend that
// doesn't exist in this tier. Mirrors browserAppHarness.ts's NoopEventSource.
class NoopEventSource {
  static readonly CONNECTING = 0;
  static readonly OPEN = 1;
  static readonly CLOSED = 2;

  url: string;
  readyState = 0;
  withCredentials = false;
  onopen: ((ev: unknown) => void) | null = null;
  onmessage: ((ev: unknown) => void) | null = null;
  onerror: ((ev: unknown) => void) | null = null;

  constructor(url: string | URL) {
    this.url = String(url);
  }

  addEventListener(): void {}
  removeEventListener(): void {}
  dispatchEvent(): boolean {
    return false;
  }
  close(): void {
    this.readyState = 2;
  }
}

describe("WorkspaceTerminalView layout flip", () => {
  it("keeps the main subtree's DOM identity when hideWorkspaceList flips", async () => {
    const api = createMockApiFetch([workspaceRoutes()]);
    const originalFetch = globalThis.fetch;
    const originalEventSource = globalThis.EventSource;
    globalThis.fetch = api.fetch;
    globalThis.EventSource = NoopEventSource as unknown as typeof EventSource;

    // TerminalOptionsMenu (always present in the ready-workspace toolbar)
    // reads settingsStore.getTerminalSettings() at initialization, so the
    // main subtree cannot mount without a settings store on STORES_KEY.
    const settingsStore = {
      getTerminalFontSize: () => DEFAULT_TERMINAL_SETTINGS.font_size,
      getTerminalSettings: () => DEFAULT_TERMINAL_SETTINGS,
    };

    const screen = render(WorkspaceTerminalView, {
      props: { workspaceId: "ws-1", hideWorkspaceList: false, hideRightSidebar: true },
      context: new Map([[STORES_KEY, { settings: settingsStore }]]),
    });

    try {
      const before = await vi.waitFor(() => {
        const el = document.querySelector(".workspace-stage");
        expect(el).not.toBeNull();
        return el as HTMLElement;
      }, WAIT);

      await screen.rerender({ workspaceId: "ws-1", hideWorkspaceList: true, hideRightSidebar: true });

      expect(before.isConnected).toBe(true);
      expect(document.contains(before)).toBe(true);

      await screen.rerender({ workspaceId: "ws-1", hideWorkspaceList: false, hideRightSidebar: true });
      expect(before.isConnected).toBe(true);
    } finally {
      await screen.unmount();
      globalThis.fetch = originalFetch;
      globalThis.EventSource = originalEventSource;
    }
  });
});
