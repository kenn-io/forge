import { render } from "vitest-browser-svelte";
import { describe, expect, it, vi } from "vite-plus/test";
import { DEFAULT_TERMINAL_SETTINGS } from "@middleman/ui";
import type { RuntimeSession } from "@middleman/ui/api/types";

import { STORES_KEY } from "../../../../../packages/ui/src/context.js";
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
  tmux_session: "middleman-ws-1",
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

  it("keeps a terminal-only workspace row as wide as its pane", async () => {
    const shell: RuntimeSession = {
      key: "ws-1_shell_a",
      workspace_id: "ws-1",
      target_key: "plain_shell",
      label: "Shell",
      kind: "plain_shell",
      status: "running",
      display_region: "terminal",
      created_at: "2026-04-29T00:00:00Z",
    };
    const secondShell = {
      ...shell,
      key: "ws-1_shell_b",
      label: "Shell 2",
      created_at: "2026-04-29T00:01:00Z",
    };
    const api = createMockApiFetch([workspaceRoutes({ launch_targets: [], sessions: [shell, secondShell] })]);
    const originalFetch = globalThis.fetch;
    const originalEventSource = globalThis.EventSource;
    globalThis.fetch = api.fetch;
    globalThis.EventSource = NoopEventSource as unknown as typeof EventSource;
    localStorage.setItem(
      "middleman-workspace-terminal-layout:ws-1",
      JSON.stringify({
        version: 1,
        open: true,
        dock: "bottom",
        height: 300,
        activeSessionKey: shell.key,
        tree: { type: "leaf", id: "dock-leaf", sessionKey: shell.key },
        sessionRegions: {
          [shell.key]: "terminal",
          [secondShell.key]: "terminal",
        },
        workflowMode: "tabs",
        workflowTree: { type: "leaf", id: "home-leaf", tabs: ["home"], activeTabKey: "home" },
        terminalGroups: [],
        customSessionLabels: {},
      }),
    );

    const settingsStore = {
      getTerminalFontSize: () => DEFAULT_TERMINAL_SETTINGS.font_size,
      getTerminalSettings: () => DEFAULT_TERMINAL_SETTINGS,
    };
    const host = document.createElement("div");
    host.style.cssText = "width: 800px; height: 500px;";
    document.body.append(host);
    const screen = render(WorkspaceTerminalView, {
      target: host,
      props: {
        workspaceId: "ws-1",
        paneSurface: "prs",
        hideWorkspaceList: true,
        hideRightSidebar: true,
      },
      context: new Map([[STORES_KEY, { settings: settingsStore }]]),
    });

    try {
      const terminalArea = await vi.waitFor(() => {
        const row = host.querySelector<HTMLElement>(".terminal-view.workspace-pane-row-only .terminal-and-sidebar");
        const area = host.querySelector<HTMLElement>(".terminal-view.workspace-pane-row-only .terminal-area");
        expect(row).not.toBeNull();
        expect(area).not.toBeNull();
        expect(row!.getBoundingClientRect().width).toBeGreaterThan(0);
        return area!;
      }, WAIT);
      const row = host.querySelector<HTMLElement>(".terminal-and-sidebar")!;

      expect(terminalArea.getBoundingClientRect().width).toBeCloseTo(row.getBoundingClientRect().width, 0);
    } finally {
      await screen.unmount();
      host.remove();
      localStorage.removeItem("middleman-workspace-terminal-layout:ws-1");
      globalThis.fetch = originalFetch;
      globalThis.EventSource = originalEventSource;
    }
  });
});
