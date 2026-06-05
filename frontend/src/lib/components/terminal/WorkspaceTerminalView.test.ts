import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/svelte";
import { afterEach, beforeEach, describe, expect, it, vi } from "vite-plus/test";

const mocks = vi.hoisted(() => ({
  getWorkspaceRuntime: vi.fn(),
  launchWorkspaceSession: vi.fn(),
  mockDispose: vi.fn(),
  mockFit: vi.fn(),
  mockLoadAddon: vi.fn(),
  mockOnData: vi.fn(),
  mockOpen: vi.fn(),
  renameWorkspaceSession: vi.fn(),
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
  Terminal: vi.fn().mockImplementation(function (options) {
    return {
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
    };
  }),
}));

vi.mock("@xterm/addon-fit", () => ({
  FitAddon: vi.fn().mockImplementation(function () {
    return {
      fit: mocks.mockFit,
    };
  }),
}));

vi.mock("@xterm/addon-webgl", () => ({
  WebglAddon: vi.fn().mockImplementation(function () {
    return {
      dispose: vi.fn(),
      onContextLoss: vi.fn(),
    };
  }),
}));

vi.mock("@xterm/xterm/css/xterm.css", () => ({}));

vi.mock("ghostty-web", () => ({
  init: vi.fn().mockResolvedValue(undefined),
  FitAddon: vi.fn().mockImplementation(function () {
    return {
      fit: mocks.mockFit,
    };
  }),
  Terminal: vi.fn().mockImplementation(function (options) {
    return {
      cols: 80,
      rows: 24,
      open: mocks.mockOpen,
      loadAddon: mocks.mockLoadAddon,
      onData: mocks.mockOnData,
      dispose: mocks.mockDispose,
      write: mocks.terminalWrite,
      options: { ...options },
    };
  }),
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
  getWorkspaceRuntime: mocks.getWorkspaceRuntime,
  launchWorkspaceSession: mocks.launchWorkspaceSession,
  renameWorkspaceSession: mocks.renameWorkspaceSession,
  stopWorkspaceSession: mocks.stopWorkspaceSession,
  workspaceSessionWebSocketPath: (workspaceId: string, sessionKey: string) =>
    `/ws/v1/workspaces/${workspaceId}/runtime/sessions/${sessionKey}/terminal`,
  workspaceTmuxWebSocketPath: (workspaceId: string) => `/ws/v1/workspaces/${workspaceId}/terminal`,
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

const reviewerSession = {
  ...runningSession,
  key: "ws-1:reviewer",
  target_key: "reviewer",
  label: "Reviewer",
  created_at: "2026-04-29T00:01:00Z",
};

const duplicateAgentSession = {
  ...runningSession,
  key: "ws-1:helper-b",
  target_key: "helper",
  label: "Helper 2",
  created_at: "2026-04-29T00:02:00Z",
};

const runningShellSession = {
  key: "ws-1_shell_a",
  workspace_id: "ws-1",
  target_key: "plain_shell",
  label: "Shell",
  kind: "plain_shell",
  status: "running",
  created_at: "2026-04-29T00:00:00Z",
};

const relaunchedShellSession = {
  ...runningShellSession,
  key: "ws-1_shell_b",
  created_at: "2026-04-29T00:01:00Z",
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

function runtimeWithTwoWorkflowSessions() {
  return {
    launch_targets: [],
    sessions: [runningSession, reviewerSession],
  };
}

function runtimeWithDuplicateWorkflowSessions() {
  return {
    launch_targets: [],
    sessions: [runningSession, duplicateAgentSession],
  };
}

function runtimeWithTerminalSession(session = runningShellSession) {
  return {
    launch_targets: [],
    sessions: [session],
  };
}

function runtimeWithTwoTerminalSessions() {
  return {
    launch_targets: [],
    sessions: [
      runningShellSession,
      {
        ...relaunchedShellSession,
        label: "Shell 2",
      },
    ],
  };
}

function persistedTerminalLayout(workflowMode: "tabs" | "grid") {
  return JSON.stringify({
    version: 1,
    open: false,
    dock: "bottom",
    height: 300,
    activeSessionKey: null,
    tree: null,
    sessionRegions: {},
    workflowMode,
    workflowTree: null,
    customSessionLabels: {},
  });
}

function deferred<T>() {
  let resolve!: (value: T) => void;
  const promise = new Promise<T>((r) => {
    resolve = r;
  });
  return { promise, resolve };
}

function fakeDataTransfer(): DataTransfer {
  const data = new Map<string, string>();
  return {
    dropEffect: "none",
    effectAllowed: "none",
    getData: (type: string) => data.get(type) ?? "",
    setData: (type: string, value: string) => {
      data.set(type, value);
    },
    setDragImage: vi.fn(),
  } as unknown as DataTransfer;
}

describe("WorkspaceTerminalView", () => {
  beforeEach(() => {
    delete window.__BASE_PATH__;
    localStorage.clear();
    localStorage.setItem("middleman-workspace-active-tab:ws-1", "session:ws-1:helper");
    sockets = [];
    mocks.getWorkspaceRuntime.mockReset();
    mocks.getWorkspaceRuntime.mockResolvedValue(runtimeWithStaleSession());
    mocks.launchWorkspaceSession.mockReset();
    mocks.renameWorkspaceSession.mockReset();
    mocks.renameWorkspaceSession.mockImplementation(
      async (_workspaceId: string, sessionKey: string, label: string) => ({
        ...(sessionKey === duplicateAgentSession.key ? duplicateAgentSession : runningSession),
        key: sessionKey,
        label,
      }),
    );
    mocks.stopWorkspaceSession.mockReset();
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

    await waitFor(() => expect(screen.queryByRole("tab", { name: /Helper/ })).toBeNull());
    expect(screen.getByRole("tab", { name: /Home/ }).getAttribute("aria-selected")).toBe("true");
    expect(localStorage.getItem("middleman-workspace-active-tab:ws-1")).toBe("home");
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

    await waitFor(() => expect(mocks.getWorkspaceRuntime).toHaveBeenCalledTimes(2));
    await waitFor(() => expect(screen.getByRole("tab", { name: /Helper/ })).toBeTruthy());
  });

  it("restores a selected workflow tab without keeping the tiled grid view", async () => {
    localStorage.setItem("middleman-workspace-terminal-layout:ws-1", persistedTerminalLayout("grid"));

    const { container } = render(WorkspaceTerminalView, {
      props: {
        workspaceId: "ws-1",
      },
    });

    const helperTab = await screen.findByRole("tab", {
      name: /Helper/,
    });

    expect(helperTab.getAttribute("aria-selected")).toBe("true");
    expect(container.querySelector(".workspace-stage.grid")).toBeNull();
  });

  it("activates a restored Shell tab after runtime tabs are normalized", async () => {
    localStorage.setItem("middleman-workspace-active-tab:ws-1", "shell");

    const { container } = render(WorkspaceTerminalView, {
      props: {
        workspaceId: "ws-1",
      },
    });

    const shellTab = await screen.findByRole("tab", { name: /Shell/ });

    expect(shellTab.getAttribute("aria-selected")).toBe("true");
    await waitFor(() => expect(container.querySelector(".group-tab-panel.active .terminal-container")).toBeTruthy());
  });

  it("closes a terminal-panel shell when its terminal exits", async () => {
    localStorage.setItem("middleman-workspace-active-tab:ws-1", "home");
    mocks.getWorkspaceRuntime.mockResolvedValue(runtimeWithTerminalSession());

    render(WorkspaceTerminalView, {
      props: {
        workspaceId: "ws-1",
      },
    });

    const terminalButton = await screen.findByRole("button", {
      name: "Open terminal panel",
    });
    await fireEvent.click(terminalButton);
    await waitFor(() => expect(sockets).toHaveLength(1));
    expect(screen.queryByLabelText("Terminal selector")).toBeNull();

    sockets[0]!.onmessage?.(
      new MessageEvent("message", {
        data: JSON.stringify({ type: "exited", code: 0 }),
      }),
    );

    await waitFor(() => expect(screen.getByText("No terminals")).toBeTruthy());
  });

  it("uses an in-app modal when stopping a running shell", async () => {
    localStorage.setItem("middleman-workspace-active-tab:ws-1", "home");
    mocks.getWorkspaceRuntime.mockResolvedValue(runtimeWithTwoTerminalSessions());
    const confirm = vi.fn();
    vi.stubGlobal("confirm", confirm);

    render(WorkspaceTerminalView, {
      props: {
        workspaceId: "ws-1",
      },
    });

    await fireEvent.click(
      await screen.findByRole("button", {
        name: "Open terminal panel",
      }),
    );
    await waitFor(() => expect(sockets).toHaveLength(1));

    await fireEvent.click(screen.getByRole("button", { name: "Close Shell" }));

    expect(confirm).not.toHaveBeenCalled();
    expect(mocks.stopWorkspaceSession).not.toHaveBeenCalled();
    expect(await screen.findByRole("dialog", { name: "Stop Shell?" })).toBeTruthy();

    await fireEvent.click(screen.getByRole("button", { name: "Cancel" }));
    await waitFor(() => expect(screen.queryByRole("dialog", { name: "Stop Shell?" })).toBeNull());

    await fireEvent.click(screen.getByRole("button", { name: "Close Shell" }));
    await fireEvent.click(await screen.findByRole("button", { name: "Stop session" }));

    await waitFor(() => expect(mocks.stopWorkspaceSession).toHaveBeenCalledWith("ws-1", "ws-1_shell_a"));
  });

  it("uses an in-app modal when renaming a tab", async () => {
    const prompt = vi.fn();
    vi.stubGlobal("prompt", prompt);

    render(WorkspaceTerminalView, {
      props: {
        workspaceId: "ws-1",
      },
    });

    await screen.findByRole("tab", { name: /Helper/ });
    await fireEvent.click(screen.getByRole("button", { name: "Rename Helper" }));

    expect(prompt).not.toHaveBeenCalled();
    expect(await screen.findByRole("dialog", { name: "Rename tab" })).toBeTruthy();
    const input = screen.getByRole("textbox", { name: "Name" });
    expect((input as HTMLInputElement).value).toBe("Helper");

    await fireEvent.click(screen.getByRole("button", { name: "Cancel" }));
    await waitFor(() => expect(screen.queryByRole("dialog", { name: "Rename tab" })).toBeNull());
    expect(screen.getByRole("tab", { name: /Helper/ })).toBeTruthy();

    await fireEvent.click(screen.getByRole("button", { name: "Rename Helper" }));
    const reopenedInput = await screen.findByRole("textbox", {
      name: "Name",
    });
    await fireEvent.input(reopenedInput, {
      target: { value: "Review helper" },
    });
    await fireEvent.click(screen.getByRole("button", { name: "Save" }));

    await waitFor(() => expect(screen.getByRole("tab", { name: /Review helper/ })).toBeTruthy());
    expect(mocks.renameWorkspaceSession).toHaveBeenCalledWith("ws-1", "ws-1:helper", "Review helper");
  });

  it("renders duplicate runtime labels literally instead of synthesizing names", async () => {
    mocks.getWorkspaceRuntime.mockResolvedValue({
      launch_targets: [],
      sessions: [
        runningSession,
        {
          ...duplicateAgentSession,
          label: runningSession.label,
        },
      ],
    });

    render(WorkspaceTerminalView, {
      props: {
        workspaceId: "ws-1",
      },
    });

    await waitFor(() => expect(screen.getAllByRole("tab", { name: "Helper" })).toHaveLength(2));
    expect(screen.queryByRole("tab", { name: /Helper 2/ })).toBeNull();
  });

  it("renames a workflow tab by its opaque session key", async () => {
    mocks.getWorkspaceRuntime.mockResolvedValue(runtimeWithDuplicateWorkflowSessions());

    render(WorkspaceTerminalView, {
      props: {
        workspaceId: "ws-1",
      },
    });

    await screen.findByRole("tab", { name: /^Helper$/ });
    await screen.findByRole("tab", { name: /Helper 2/ });

    await fireEvent.click(screen.getByRole("button", { name: "Rename Helper" }));
    const input = await screen.findByRole("textbox", { name: "Name" });
    expect((input as HTMLInputElement).value).toBe("Helper");

    await fireEvent.input(input, {
      target: { value: "Plan review" },
    });
    await fireEvent.click(screen.getByRole("button", { name: "Save" }));

    await waitFor(() => expect(screen.getByRole("tab", { name: /Plan review/ })).toBeTruthy());
    expect(screen.getByRole("tab", { name: /Helper 2/ })).toBeTruthy();
    expect(mocks.renameWorkspaceSession).toHaveBeenCalledWith("ws-1", "ws-1:helper", "Plan review");
  });

  it("shows a moving insertion slot while sorting workflow tabs", async () => {
    mocks.getWorkspaceRuntime.mockResolvedValue(runtimeWithTwoWorkflowSessions());

    render(WorkspaceTerminalView, {
      props: {
        workspaceId: "ws-1",
      },
    });

    const helperTab = await screen.findByRole("tab", {
      name: /Helper/,
    });
    const reviewerTab = await screen.findByRole("tab", {
      name: /Reviewer/,
    });
    const helperTabHost = helperTab.closest(".group-tab");
    expect(helperTabHost).toBeTruthy();
    const dataTransfer = fakeDataTransfer();

    await fireEvent.dragStart(reviewerTab, { dataTransfer });
    await fireEvent.dragOver(helperTabHost!, {
      clientX: -1,
      dataTransfer,
    });

    expect(screen.getByTestId("workflow-tab-drop-placeholder")).toBeTruthy();
    expect(reviewerTab.closest(".group-tab")?.classList.contains("dragging")).toBe(true);

    await fireEvent.dragEnd(reviewerTab);

    expect(screen.queryByTestId("workflow-tab-drop-placeholder")).toBeNull();
    expect(reviewerTab.closest(".group-tab")?.classList.contains("dragging")).toBe(false);
  });

  it("does not reopen the just-exited terminal from stale runtime data", async () => {
    localStorage.setItem("middleman-workspace-active-tab:ws-1", "home");
    mocks.getWorkspaceRuntime.mockResolvedValue(runtimeWithTerminalSession());

    render(WorkspaceTerminalView, {
      props: {
        workspaceId: "ws-1",
      },
    });

    const terminalButton = await screen.findByRole("button", {
      name: "Open terminal panel",
    });
    await fireEvent.click(terminalButton);
    await waitFor(() => expect(sockets).toHaveLength(1));

    sockets[0]!.onmessage?.(
      new MessageEvent("message", {
        data: JSON.stringify({ type: "exited", code: 0 }),
      }),
    );
    await waitFor(() => expect(screen.getByText("No terminals")).toBeTruthy());
    expect(sockets).toHaveLength(1);
  });

  it("reconnects terminal panes when selecting another shell", async () => {
    localStorage.setItem("middleman-workspace-active-tab:ws-1", "home");
    mocks.getWorkspaceRuntime.mockResolvedValue(runtimeWithTwoTerminalSessions());

    render(WorkspaceTerminalView, {
      props: {
        workspaceId: "ws-1",
      },
    });

    const terminalButton = await screen.findByRole("button", {
      name: "Open terminal panel",
    });
    await fireEvent.click(terminalButton);
    await waitFor(() => expect(sockets.some((socket) => socket.url.includes("ws-1_shell_a"))).toBe(true));

    await fireEvent.click(screen.getByRole("button", { name: "Shell 2" }));

    await waitFor(() => expect(sockets.some((socket) => socket.url.includes("ws-1_shell_b"))).toBe(true));
  });

  it("renders a split terminal immediately after launching its session", async () => {
    localStorage.setItem("middleman-workspace-active-tab:ws-1", "home");
    mocks.getWorkspaceRuntime.mockResolvedValue(runtimeWithTerminalSession());
    mocks.launchWorkspaceSession.mockResolvedValue({
      ...relaunchedShellSession,
      label: "Shell 2",
    });

    render(WorkspaceTerminalView, {
      props: {
        workspaceId: "ws-1",
      },
    });

    const terminalButton = await screen.findByRole("button", {
      name: "Open terminal panel",
    });
    await fireEvent.click(terminalButton);
    await waitFor(() => expect(sockets.some((socket) => socket.url.includes("ws-1_shell_a"))).toBe(true));

    await fireEvent.click(screen.getByRole("button", { name: "Split terminal right" }));

    await waitFor(() => expect(sockets.some((socket) => socket.url.includes("ws-1_shell_b"))).toBe(true));
    expect(screen.getByRole("button", { name: "Shell 2" })).toBeTruthy();
  });

  it("ignores older runtime responses after terminal cleanup refreshes", async () => {
    localStorage.setItem("middleman-workspace-active-tab:ws-1", "home");
    const staleRefresh = deferred<ReturnType<typeof runtimeWithTerminalSession>>();
    const freshRefresh = deferred<ReturnType<typeof runtimeWithTerminalSession>>();
    mocks.getWorkspaceRuntime
      .mockResolvedValueOnce(runtimeWithTerminalSession())
      .mockReturnValueOnce(staleRefresh.promise)
      .mockReturnValueOnce(freshRefresh.promise);
    mocks.launchWorkspaceSession.mockResolvedValue(relaunchedShellSession);

    render(WorkspaceTerminalView, {
      props: {
        workspaceId: "ws-1",
      },
    });

    const terminalButton = await screen.findByRole("button", {
      name: "Open terminal panel",
    });
    await fireEvent.click(terminalButton);
    await waitFor(() => expect(sockets).toHaveLength(1));

    sockets[0]!.onmessage?.(
      new MessageEvent("message", {
        data: JSON.stringify({ type: "exited", code: 0 }),
      }),
    );
    await waitFor(() => expect(screen.getByText("No terminals")).toBeTruthy());

    await fireEvent.click(screen.getAllByRole("button", { name: "New terminal" })[0]!);
    freshRefresh.resolve(runtimeWithTerminalSession(relaunchedShellSession));
    await waitFor(() => expect(sockets.some((socket) => socket.url.includes("ws-1_shell_b"))).toBe(true));

    staleRefresh.resolve(runtimeWithTerminalSession());
    await new Promise((resolve) => setTimeout(resolve, 0));

    expect(sockets.filter((socket) => socket.url.includes("ws-1_shell_a"))).toHaveLength(1);
    expect(sockets.filter((socket) => socket.url.includes("ws-1_shell_b"))).toHaveLength(1);
  });

  it("moves a workflow shell back into the terminal panel", async () => {
    localStorage.setItem("middleman-workspace-active-tab:ws-1", "home");
    mocks.getWorkspaceRuntime.mockResolvedValue(runtimeWithTwoTerminalSessions());

    render(WorkspaceTerminalView, {
      props: {
        workspaceId: "ws-1",
      },
    });

    await fireEvent.click(
      await screen.findByRole("button", {
        name: "Open terminal panel",
      }),
    );
    await waitFor(() => expect(sockets).toHaveLength(1));
    expect(screen.getByRole("button", { name: "Focus Shell" }).getAttribute("draggable")).toBe("true");
    await fireEvent.click(screen.getByRole("button", { name: "Move Shell to workflow" }));

    await screen.findByRole("tab", { name: /Shell/ });
    await fireEvent.click(screen.getByRole("button", { name: "Move Shell to terminal" }));

    await waitFor(() => expect(screen.queryByRole("tab", { name: /Shell/ })).toBeNull());
    expect(screen.getByRole("button", { name: "Focus Shell" })).toBeTruthy();
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
