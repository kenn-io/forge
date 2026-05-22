import {
  cleanup,
  fireEvent,
  render,
  screen,
  waitFor,
} from "@testing-library/svelte";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

const mocks = vi.hoisted(() => ({
  ensureWorkspaceShell: vi.fn(),
  getWorkspaceRuntime: vi.fn(),
  launchWorkspaceSession: vi.fn(),
  mockDispose: vi.fn(),
  mockFit: vi.fn(),
  mockLoadAddon: vi.fn(),
  mockOnData: vi.fn(),
  mockOpen: vi.fn(),
  stopWorkspaceSession: vi.fn(),
  terminalWrite: vi.fn(),
}));

let sockets: MockWebSocket[] = [];

class MockWebSocket {
  static OPEN = 1;
  readyState = 1;
  binaryType = "arraybuffer";
  onopen: (() => void) | null = null;
  onmessage: ((event: MessageEvent) => void) | null = null;
  onclose: (() => void) | null = null;
  onerror: (() => void) | null = null;

  constructor(public url: string) {
    sockets.push(this);
  }

  send(): void {}
  close(): void {}
}

vi.mock("@xterm/xterm", () => ({
  Terminal: vi.fn().mockImplementation((options) => ({
    cols: 80,
    rows: 24,
    clearTextureAtlas: vi.fn(),
    dispose: mocks.mockDispose,
    loadAddon: mocks.mockLoadAddon,
    onBinary: vi.fn(),
    onData: mocks.mockOnData,
    open: mocks.mockOpen,
    refresh: vi.fn(),
    write: mocks.terminalWrite,
    options: { ...options },
  })),
}));

vi.mock("@xterm/addon-fit", () => ({
  FitAddon: vi.fn().mockImplementation(() => ({
    fit: mocks.mockFit,
  })),
}));

vi.mock("@xterm/addon-webgl", () => ({
  WebglAddon: vi.fn().mockImplementation(() => ({
    dispose: vi.fn(),
    onContextLoss: vi.fn(),
  })),
}));

vi.mock("@xterm/xterm/css/xterm.css", () => ({}));

vi.mock("ghostty-web", () => ({
  init: vi.fn().mockResolvedValue(undefined),
  FitAddon: vi.fn().mockImplementation(() => ({
    fit: mocks.mockFit,
  })),
  Terminal: vi.fn().mockImplementation((options) => ({
    cols: 80,
    rows: 24,
    open: mocks.mockOpen,
    loadAddon: mocks.mockLoadAddon,
    onData: mocks.mockOnData,
    dispose: mocks.mockDispose,
    write: mocks.terminalWrite,
    options: { ...options },
  })),
}));

vi.mock("@middleman/ui", async (importOriginal) => {
  const actual = await importOriginal<typeof import("@middleman/ui")>();
  return {
    ...actual,
    getStores: () => ({
      settings: {
        getTerminalSettings: () => ({
          font_family: "",
          font_size: 14,
          scrollback: 1000,
          line_height: 1,
          letter_spacing: 0,
          cursor_blink: true,
          font_ligatures: false,
          renderer: "ghostty-web",
        }),
        setTerminalSettings: vi.fn(),
        getTerminalFontFamily: () => "",
        getTerminalFontSize: () => 14,
        getTerminalScrollback: () => 1000,
        getTerminalLineHeight: () => 1,
        getTerminalLetterSpacing: () => 0,
        getTerminalCursorBlink: () => true,
        getTerminalFontLigatures: () => false,
        getTerminalRenderer: () => "ghostty-web",
      },
    }),
  };
});

vi.mock("../../api/workspace-runtime.js", () => ({
  ensureWorkspaceShell: mocks.ensureWorkspaceShell,
  getWorkspaceRuntime: mocks.getWorkspaceRuntime,
  launchWorkspaceSession: mocks.launchWorkspaceSession,
  stopWorkspaceSession: mocks.stopWorkspaceSession,
  workspaceSessionWebSocketPath: (workspaceId: string, sessionKey: string) =>
    `/ws/v1/workspaces/${workspaceId}/runtime/sessions/${sessionKey}/terminal`,
  workspaceShellWebSocketPath: (workspaceId: string) =>
    `/ws/v1/workspaces/${workspaceId}/runtime/shell/terminal`,
  workspaceTmuxWebSocketPath: (workspaceId: string) =>
    `/ws/v1/workspaces/${workspaceId}/terminal`,
}));

import WorkspaceTerminalView from "./WorkspaceTerminalView.svelte";

const runningSession = {
  key: "ws-1:helper",
  workspace_id: "ws-1",
  target_key: "helper",
  label: "Helper",
  kind: "agent",
  status: "running",
  created_at: "2026-04-29T00:00:00Z",
};

const workspaceResponse = {
  id: "ws-1",
  platform_host: "github.com",
  repo_owner: "acme",
  repo_name: "widget",
  item_type: "pull_request",
  item_number: 7,
  git_head_ref: "feature/session-exit",
  worktree_path: "/tmp/worktree",
  tmux_session: "middleman-ws-1",
  status: "ready",
  created_at: "2026-04-29T00:00:00Z",
};

function runtimeWithSession(createdAt: string) {
  return {
    launch_targets: [],
    sessions: [
      {
        ...runningSession,
        created_at: createdAt,
      },
    ],
  };
}

function runtimeWithStaleSession() {
  return {
    launch_targets: [],
    sessions: [runningSession],
  };
}

function runtimeWithShellSession() {
  return {
    launch_targets: [],
    sessions: [],
    shell_session: {
      key: "ws-1:shell",
      workspace_id: "ws-1",
      target_key: "plain_shell",
      label: "Shell",
      kind: "plain_shell",
      status: "running",
      created_at: "2026-04-29T00:00:00Z",
    },
  };
}

function runtimeWithoutShellSession() {
  return {
    launch_targets: [],
    sessions: [],
    shell_session: null,
  };
}

function deferred<T>() {
  let resolve!: (value: T) => void;
  const promise = new Promise<T>((r) => {
    resolve = r;
  });
  return { promise, resolve };
}

describe("WorkspaceTerminalView", () => {
  beforeEach(() => {
    delete window.__BASE_PATH__;
    localStorage.clear();
    localStorage.setItem(
      "middleman-workspace-active-tab:ws-1",
      "session:ws-1:helper",
    );
    sockets = [];
    mocks.getWorkspaceRuntime.mockReset();
    mocks.getWorkspaceRuntime.mockResolvedValue(runtimeWithStaleSession());
    mocks.launchWorkspaceSession.mockReset();
    mocks.stopWorkspaceSession.mockReset();
    mocks.ensureWorkspaceShell.mockReset();
    mocks.terminalWrite.mockReset();

    vi.stubGlobal(
      "fetch",
      vi.fn().mockImplementation((input: Request | URL | string) => {
        const url = input instanceof Request ? input.url : String(input);
        const { pathname } = new URL(url);
        if (pathname.endsWith("/api/v1/workspaces/ws-1")) {
          return Promise.resolve(Response.json(workspaceResponse));
        }
        if (pathname.endsWith("/api/v1/workspaces")) {
          return Promise.resolve(
            Response.json({
              workspaces: [workspaceResponse],
            }),
          );
        }
        return Promise.resolve(Response.json({}));
      }),
    );
    vi.stubGlobal(
      "EventSource",
      class {
        addEventListener(): void {}
        close(): void {}
      },
    );
    vi.stubGlobal(
      "ResizeObserver",
      class {
        observe(): void {}
        disconnect(): void {}
      },
    );
    vi.stubGlobal("WebSocket", MockWebSocket);
    vi.stubGlobal("requestAnimationFrame", (callback: FrameRequestCallback) => {
      callback(0);
      return 1;
    });
    vi.stubGlobal("cancelAnimationFrame", () => undefined);
  });

  afterEach(() => {
    cleanup();
    vi.unstubAllGlobals();
  });

  it("closes an agent tab immediately when its terminal exits", async () => {
    render(WorkspaceTerminalView, {
      props: {
        workspaceId: "ws-1",
      },
    });

    await screen.findByRole("tab", { name: /Helper/ });
    await waitFor(() => expect(sockets).toHaveLength(1));

    sockets[0]!.onmessage?.(
      new MessageEvent("message", {
        data: JSON.stringify({ type: "exited", code: 0 }),
      }),
    );

    await waitFor(() =>
      expect(screen.queryByRole("tab", { name: /Helper/ })).toBeNull(),
    );
    expect(
      screen.getByRole("tab", { name: /Home/ }).getAttribute("aria-selected"),
    ).toBe("true");
    expect(localStorage.getItem("middleman-workspace-active-tab:ws-1")).toBe(
      "home",
    );
  });

  it("shows a relaunched agent with the same key and a new generation", async () => {
    const relaunchedAt = "2026-04-29T00:01:00Z";
    mocks.getWorkspaceRuntime
      .mockResolvedValueOnce(runtimeWithStaleSession())
      .mockResolvedValueOnce(runtimeWithSession(relaunchedAt));

    render(WorkspaceTerminalView, {
      props: {
        workspaceId: "ws-1",
      },
    });

    await screen.findByRole("tab", { name: /Helper/ });
    await waitFor(() => expect(sockets).toHaveLength(1));

    sockets[0]!.onmessage?.(
      new MessageEvent("message", {
        data: JSON.stringify({ type: "exited", code: 0 }),
      }),
    );

    await waitFor(() =>
      expect(mocks.getWorkspaceRuntime).toHaveBeenCalledTimes(2),
    );
    await waitFor(() =>
      expect(screen.getByRole("tab", { name: /Helper/ })).toBeTruthy(),
    );
  });

  it("closes the shell drawer when its terminal exits", async () => {
    localStorage.setItem("middleman-workspace-active-tab:ws-1", "home");
    mocks.getWorkspaceRuntime.mockResolvedValue(runtimeWithShellSession());

    render(WorkspaceTerminalView, {
      props: {
        workspaceId: "ws-1",
      },
    });

    const shellButton = await screen.findByRole("button", {
      name: "Open shell drawer",
    });
    await fireEvent.click(shellButton);
    await waitFor(() => expect(sockets).toHaveLength(1));

    sockets[0]!.onmessage?.(
      new MessageEvent("message", {
        data: JSON.stringify({ type: "exited", code: 0 }),
      }),
    );

    await waitFor(() =>
      expect(
        screen.getByRole("button", {
          name: "Open shell drawer",
        }),
      ).toBeTruthy(),
    );
  });

  it("does not reopen the just-exited shell from stale runtime data", async () => {
    localStorage.setItem("middleman-workspace-active-tab:ws-1", "home");
    mocks.getWorkspaceRuntime.mockResolvedValue(runtimeWithShellSession());
    mocks.ensureWorkspaceShell.mockResolvedValue(undefined);

    render(WorkspaceTerminalView, {
      props: {
        workspaceId: "ws-1",
      },
    });

    const shellButton = await screen.findByRole("button", {
      name: "Open shell drawer",
    });
    await fireEvent.click(shellButton);
    await waitFor(() => expect(sockets).toHaveLength(1));

    sockets[0]!.onmessage?.(
      new MessageEvent("message", {
        data: JSON.stringify({ type: "exited", code: 0 }),
      }),
    );
    await waitFor(() =>
      expect(
        screen.getByRole("button", {
          name: "Open shell drawer",
        }),
      ).toBeTruthy(),
    );

    await fireEvent.click(shellButton);

    await waitFor(() =>
      expect(mocks.ensureWorkspaceShell).toHaveBeenCalledTimes(2),
    );
    expect(sockets).toHaveLength(1);
  });

  it("ignores older runtime responses after shell cleanup refreshes", async () => {
    localStorage.setItem("middleman-workspace-active-tab:ws-1", "home");
    const staleRefresh = deferred<ReturnType<typeof runtimeWithShellSession>>();
    const freshRefresh =
      deferred<ReturnType<typeof runtimeWithoutShellSession>>();
    mocks.getWorkspaceRuntime
      .mockResolvedValueOnce(runtimeWithShellSession())
      .mockResolvedValueOnce(runtimeWithShellSession())
      .mockReturnValueOnce(staleRefresh.promise)
      .mockReturnValueOnce(freshRefresh.promise);
    mocks.ensureWorkspaceShell.mockResolvedValue(undefined);

    render(WorkspaceTerminalView, {
      props: {
        workspaceId: "ws-1",
      },
    });

    const shellButton = await screen.findByRole("button", {
      name: "Open shell drawer",
    });
    await fireEvent.click(shellButton);
    await waitFor(() => expect(sockets).toHaveLength(1));

    sockets[0]!.onmessage?.(
      new MessageEvent("message", {
        data: JSON.stringify({ type: "exited", code: 0 }),
      }),
    );
    await waitFor(() =>
      expect(
        screen.getByRole("button", {
          name: "Open shell drawer",
        }),
      ).toBeTruthy(),
    );

    await fireEvent.click(shellButton);
    freshRefresh.resolve(runtimeWithoutShellSession());
    await waitFor(() =>
      expect(screen.getByText("Shell unavailable")).toBeTruthy(),
    );

    staleRefresh.resolve(runtimeWithShellSession());
    await new Promise((resolve) => setTimeout(resolve, 0));

    expect(sockets).toHaveLength(1);
  });

  it("shows a workspace sidebar collapse button", async () => {
    const onToggleSidebar = vi.fn();

    render(WorkspaceTerminalView, {
      props: {
        workspaceId: "ws-1",
        isSidebarToggleEnabled: true,
        onToggleSidebar,
      },
    });

    const collapseButton = await screen.findByRole("button", {
      name: "Collapse Workspaces sidebar",
    });

    await fireEvent.click(collapseButton);

    expect(onToggleSidebar).toHaveBeenCalledTimes(1);
  });
});
