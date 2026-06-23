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
  mockTerminalInstances: [] as Array<{ options: Record<string, unknown> }>,
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

  send = vi.fn();
  close = vi.fn();
}

vi.mock("@xterm/xterm", () => ({
  Terminal: vi.fn().mockImplementation(function (options) {
    const terminal = {
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
    mocks.mockTerminalInstances.push(terminal);
    return terminal;
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
    const terminal = {
      cols: 80,
      rows: 24,
      open: mocks.mockOpen,
      loadAddon: mocks.mockLoadAddon,
      onData: mocks.mockOnData,
      dispose: mocks.mockDispose,
      write: mocks.terminalWrite,
      options: { ...options },
    };
    mocks.mockTerminalInstances.push(terminal);
    return terminal;
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
        getModeVisibility: () => ({
          activity: true,
          repos: true,
          kata: false,
          docs: false,
          messages: false,
          pulls: true,
          issues: true,
          board: true,
          reviews: true,
          workspaces: true,
        }),
        setModeVisibility: vi.fn(),
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
    launch_targets: [
      {
        key: "helper",
        label: "Helper",
        kind: "agent",
        source: "config",
        available: true,
      },
    ],
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
    mocks.mockTerminalInstances.length = 0;

    vi.stubGlobal(
      "fetch",
      vi.fn().mockImplementation((input: Request | URL | string) => {
        const url = input instanceof Request ? input.url : String(input);
        const { pathname } = new URL(url, "http://localhost");
        if (pathname.endsWith("/workspaces/ws-1")) {
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
    vi.useRealTimers();
    vi.restoreAllMocks();
    vi.unstubAllGlobals();
  });

  it("explains workspace creation in the main pane when no workspaces exist", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn().mockImplementation((input: Request | URL | string) => {
        const url = input instanceof Request ? input.url : String(input);
        const { pathname } = new URL(url, "http://localhost");
        if (pathname.endsWith("/api/v1/workspaces")) {
          return Promise.resolve(Response.json({ workspaces: [] }));
        }
        if (pathname.endsWith("/api/v1/snapshot")) {
          return Promise.resolve(
            Response.json({
              hosts: [
                {
                  configKey: "local",
                  diagnostics: [],
                  id: "local",
                  kind: "self",
                  name: "local",
                  operationAvailability: {},
                  platform: "darwin",
                  preferredTransport: "local",
                  reachable: true,
                  tmuxSessions: [],
                },
              ],
            }),
          );
        }
        if (pathname.endsWith("/api/v1/settings")) {
          return Promise.resolve(
            Response.json({
              launch_targets: [
                {
                  key: "configured-agent",
                  label: "Configured Agent",
                  kind: "agent",
                  source: "config",
                  available: true,
                },
                {
                  key: "plain_shell",
                  label: "Shell",
                  kind: "plain_shell",
                  source: "system",
                  available: true,
                },
              ],
            }),
          );
        }
        return Promise.resolve(Response.json({}));
      }),
    );

    render(WorkspaceTerminalView, {
      props: {
        workspaceId: "",
      },
    });

    expect(await screen.findByText("Create a workspace to run agents from a PR or issue")).toBeTruthy();
    expect(screen.getByText(/Workspaces are git worktrees created from PR or issue heads\./i)).toBeTruthy();
    expect(screen.getByText(/From a PR or issue, use the/i)).toBeTruthy();
    const exampleCard = screen.getByLabelText("Workspace workflow example");
    expect(exampleCard).toBeTruthy();
    expect(screen.queryByText("Example workflow")).toBeNull();
    const createWorkspaceButton = screen.getByRole("button", {
      name: "Create Workspace",
    }) as HTMLButtonElement;
    expect(createWorkspaceButton.disabled).toBe(true);
    expect(createWorkspaceButton.getAttribute("title")).toContain("launch agents");
    const capabilityCopy = screen.getByText(/start agents, local review sessions, or a shell/i);
    expect(screen.getByText("You can then launch configured agents via the buttons provided")).toBeTruthy();
    const exampleHeading = await screen.findByText("Launch");
    expect(capabilityCopy.compareDocumentPosition(exampleHeading) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy();
    expect(screen.queryByText("New session")).toBeNull();
    expect(screen.queryByRole("button", { name: /Codex review agent/i })).toBeNull();
    expect(screen.queryByRole("button", { name: /Claude review agent/i })).toBeNull();
    expect((screen.getByRole("button", { name: /Configured Agent/i }) as HTMLButtonElement).disabled).toBe(true);
    expect((screen.getByRole("button", { name: /Shell/i }) as HTMLButtonElement).disabled).toBe(true);
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

  it("polls local workspace runtime so peer-spawned sessions appear", async () => {
    localStorage.setItem("middleman-workspace-active-tab:ws-1", "home");
    const intervalCallbacks: Array<{ callback: () => void; delay: number | undefined }> = [];
    const setIntervalSpy = vi
      .spyOn(globalThis, "setInterval")
      .mockImplementation((callback: TimerHandler, delay?: number) => {
        intervalCallbacks.push({ callback: callback as () => void, delay });
        return 1 as unknown as ReturnType<typeof setInterval>;
      });
    const clearIntervalSpy = vi.spyOn(globalThis, "clearInterval").mockImplementation(() => undefined);
    mocks.getWorkspaceRuntime
      .mockResolvedValueOnce({ launch_targets: [], sessions: [] })
      .mockResolvedValueOnce(runtimeWithSession("2026-04-29T00:03:00Z"));

    render(WorkspaceTerminalView, {
      props: {
        workspaceId: "ws-1",
      },
    });

    await waitFor(() => expect(mocks.getWorkspaceRuntime).toHaveBeenCalledWith("ws-1", undefined));
    expect(screen.queryByRole("tab", { name: /Helper/ })).toBeNull();
    const runtimePoll = intervalCallbacks.find((interval) => interval.delay === 3000);
    expect(runtimePoll).toBeTruthy();

    runtimePoll!.callback();

    await waitFor(() => expect(screen.getByRole("tab", { name: /Helper/ })).toBeTruthy());
    expect(mocks.getWorkspaceRuntime).toHaveBeenCalledTimes(2);
    setIntervalSpy.mockRestore();
    clearIntervalSpy.mockRestore();
  });

  it("polls remote workspace runtime so peer-spawned sessions appear", async () => {
    localStorage.setItem("middleman-workspace-active-tab:fleet:member:ws-1", "home");
    const intervalCallbacks: Array<{ callback: () => void; delay: number | undefined }> = [];
    const setIntervalSpy = vi
      .spyOn(globalThis, "setInterval")
      .mockImplementation((callback: TimerHandler, delay?: number) => {
        intervalCallbacks.push({ callback: callback as () => void, delay });
        return 1 as unknown as ReturnType<typeof setInterval>;
      });
    const clearIntervalSpy = vi.spyOn(globalThis, "clearInterval").mockImplementation(() => undefined);
    mocks.getWorkspaceRuntime
      .mockResolvedValueOnce({ launch_targets: [], sessions: [] })
      .mockResolvedValueOnce(runtimeWithSession("2026-04-29T00:03:00Z"));

    render(WorkspaceTerminalView, {
      props: {
        workspaceId: "ws-1",
        workspaceHostKey: "member",
      },
    });

    await waitFor(() => expect(mocks.getWorkspaceRuntime).toHaveBeenCalledWith("ws-1", "member"));
    expect(screen.queryByRole("tab", { name: /Helper/ })).toBeNull();
    const runtimePoll = intervalCallbacks.find((interval) => interval.delay === 3000);
    expect(runtimePoll).toBeTruthy();

    runtimePoll!.callback();

    await waitFor(() => expect(screen.getByRole("tab", { name: /Helper/ })).toBeTruthy());
    expect(mocks.getWorkspaceRuntime).toHaveBeenCalledTimes(2);
    setIntervalSpy.mockRestore();
    clearIntervalSpy.mockRestore();
  });

  it("persists remote terminal layout under the fleet-scoped workspace key", async () => {
    localStorage.setItem("middleman-workspace-active-tab:fleet:member:ws-1", "home");
    localStorage.removeItem("middleman-workspace-terminal-layout:ws-1");
    mocks.getWorkspaceRuntime.mockResolvedValue({ launch_targets: [], sessions: [] });

    render(WorkspaceTerminalView, {
      props: {
        workspaceId: "ws-1",
        workspaceHostKey: "member",
      },
    });

    await screen.findByRole("tab", { name: "Home" });
    await waitFor(() =>
      expect(localStorage.getItem("middleman-workspace-terminal-layout:fleet:member:ws-1")).toContain(
        '"workflowMode":"tabs"',
      ),
    );
    expect(localStorage.getItem("middleman-workspace-terminal-layout:ws-1")).toBeNull();
  });

  it("does not show remote runtime while same-id local workspace data is still cached", async () => {
    localStorage.setItem("middleman-workspace-active-tab:ws-1", "session:ws-1:helper");
    localStorage.setItem("middleman-workspace-active-tab:fleet:member:ws-1", "home");
    const remoteWorkspace = deferred<typeof workspaceResponse>();
    const eventListeners: Record<string, () => void> = {};

    vi.stubGlobal(
      "fetch",
      vi.fn().mockImplementation((input: Request | URL | string) => {
        const url = input instanceof Request ? input.url : String(input);
        const { pathname } = new URL(url, "http://localhost");
        if (pathname === "/api/v1/workspaces/ws-1") {
          return Promise.resolve(Response.json(workspaceResponse));
        }
        if (pathname === "/api/v1/fleet/hosts/member/workspaces/ws-1") {
          return remoteWorkspace.promise.then((workspace) => Response.json({ ...workspace, fleet_host_key: "member" }));
        }
        if (pathname === "/api/v1/workspaces") {
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
        addEventListener(type: string, callback: () => void): void {
          eventListeners[type] = callback;
        }
        close(): void {}
      },
    );

    mocks.getWorkspaceRuntime.mockResolvedValue(runtimeWithStaleSession());
    const { rerender } = render(WorkspaceTerminalView, {
      props: {
        workspaceId: "ws-1",
      },
    });

    await screen.findByRole("tab", { name: /Helper/ });

    mocks.getWorkspaceRuntime.mockClear();
    mocks.getWorkspaceRuntime.mockResolvedValue(runtimeWithTwoWorkflowSessions());
    await rerender({
      workspaceId: "ws-1",
      workspaceHostKey: "member",
    });

    eventListeners["reconnect.stale"]?.();
    await waitFor(() => expect(mocks.getWorkspaceRuntime).toHaveBeenCalledWith("ws-1", "member"));
    expect(screen.queryByRole("tab", { name: /Reviewer/ })).toBeNull();

    remoteWorkspace.resolve(workspaceResponse);

    await waitFor(() => expect(screen.getByRole("tab", { name: /Reviewer/ })).toBeTruthy());
  });

  it("does not overlap runtime polling while a slow fetch is in flight", async () => {
    localStorage.setItem("middleman-workspace-active-tab:fleet:member:ws-1", "home");
    const intervalCallbacks: Array<{ callback: () => void; delay: number | undefined }> = [];
    const setIntervalSpy = vi
      .spyOn(globalThis, "setInterval")
      .mockImplementation((callback: TimerHandler, delay?: number) => {
        intervalCallbacks.push({ callback: callback as () => void, delay });
        return 1 as unknown as ReturnType<typeof setInterval>;
      });
    const clearIntervalSpy = vi.spyOn(globalThis, "clearInterval").mockImplementation(() => undefined);
    let resolveFirst: (value: ReturnType<typeof runtimeWithStaleSession>) => void = () => undefined;
    const firstFetch = new Promise<ReturnType<typeof runtimeWithStaleSession>>((resolve) => {
      resolveFirst = resolve;
    });
    mocks.getWorkspaceRuntime
      .mockReturnValueOnce(firstFetch)
      .mockResolvedValueOnce(runtimeWithSession("2026-04-29T00:03:00Z"));

    render(WorkspaceTerminalView, {
      props: {
        workspaceId: "ws-1",
        workspaceHostKey: "member",
      },
    });

    await waitFor(() => expect(mocks.getWorkspaceRuntime).toHaveBeenCalledWith("ws-1", "member"));
    const runtimePoll = intervalCallbacks.find((interval) => interval.delay === 3000);
    expect(runtimePoll).toBeTruthy();

    runtimePoll!.callback();
    await Promise.resolve();
    expect(mocks.getWorkspaceRuntime).toHaveBeenCalledTimes(1);

    resolveFirst({ launch_targets: [], sessions: [] });
    await waitFor(() => expect(screen.getByRole("tab", { name: /Home/ })).toBeTruthy());

    runtimePoll!.callback();
    await waitFor(() => expect(mocks.getWorkspaceRuntime).toHaveBeenCalledTimes(2));
    setIntervalSpy.mockRestore();
    clearIntervalSpy.mockRestore();
  });

  it("forces post-launch runtime refresh past an older in-flight poll", async () => {
    localStorage.setItem("middleman-workspace-active-tab:ws-1", "home");
    const intervalCallbacks: Array<{ callback: () => void; delay: number | undefined }> = [];
    const setIntervalSpy = vi
      .spyOn(globalThis, "setInterval")
      .mockImplementation((callback: TimerHandler, delay?: number) => {
        intervalCallbacks.push({ callback: callback as () => void, delay });
        return 1 as unknown as ReturnType<typeof setInterval>;
      });
    const clearIntervalSpy = vi.spyOn(globalThis, "clearInterval").mockImplementation(() => undefined);
    const stalePoll = deferred<ReturnType<typeof runtimeWithTerminalSession>>();
    mocks.getWorkspaceRuntime
      .mockResolvedValueOnce(runtimeWithTerminalSession())
      .mockReturnValueOnce(stalePoll.promise)
      .mockResolvedValueOnce(runtimeWithTerminalSession(relaunchedShellSession));
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
    await waitFor(() => expect(sockets.some((socket) => socket.url.includes("ws-1_shell_a"))).toBe(true));
    const runtimePoll = intervalCallbacks.find((interval) => interval.delay === 3000);
    expect(runtimePoll).toBeTruthy();

    runtimePoll!.callback();
    await Promise.resolve();
    expect(mocks.getWorkspaceRuntime).toHaveBeenCalledTimes(2);

    await fireEvent.click(screen.getAllByRole("button", { name: "New terminal" })[0]!);

    await waitFor(() => expect(mocks.getWorkspaceRuntime).toHaveBeenCalledTimes(3));
    await waitFor(() => expect(sockets.some((socket) => socket.url.includes("ws-1_shell_b"))).toBe(true));

    stalePoll.resolve(runtimeWithTerminalSession());
    await new Promise((resolve) => setTimeout(resolve, 0));

    expect(sockets.filter((socket) => socket.url.includes("ws-1_shell_a"))).toHaveLength(1);
    expect(sockets.filter((socket) => socket.url.includes("ws-1_shell_b"))).toHaveLength(1);
    setIntervalSpy.mockRestore();
    clearIntervalSpy.mockRestore();
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

  it("drops a restored legacy Shell tab after runtime tabs are normalized", async () => {
    localStorage.setItem("middleman-workspace-active-tab:ws-1", "shell");

    render(WorkspaceTerminalView, {
      props: {
        workspaceId: "ws-1",
      },
    });

    const homeTab = await screen.findByRole("tab", { name: "Home" });

    expect(homeTab.getAttribute("aria-selected")).toBe("true");
    expect(screen.queryByRole("tab", { name: /Shell/ })).toBeNull();
    expect(sockets).toHaveLength(0);
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

    await waitFor(() => expect(mocks.stopWorkspaceSession).toHaveBeenCalledWith("ws-1", "ws-1_shell_a", undefined));
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
    expect(mocks.renameWorkspaceSession).toHaveBeenCalledWith("ws-1", "ws-1:helper", "Review helper", undefined);
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
    expect(mocks.renameWorkspaceSession).toHaveBeenCalledWith("ws-1", "ws-1:helper", "Plan review", undefined);
  });

  it("shows a moving insertion slot while sorting workflow tabs", async () => {
    mocks.getWorkspaceRuntime.mockResolvedValue(runtimeWithTwoWorkflowSessions());

    render(WorkspaceTerminalView, {
      props: {
        workspaceId: "ws-1",
      },
    });

    const helperTab = await screen.findByRole("tab", { name: /Helper/ });
    const reviewerTab = await screen.findByRole("tab", { name: /Reviewer/ });
    const helperTabHost = helperTab.closest(".tabbed-panel-tab");
    expect(helperTabHost).toBeTruthy();
    const dataTransfer = fakeDataTransfer();

    await fireEvent.dragStart(reviewerTab, { dataTransfer });
    await fireEvent.dragOver(helperTabHost!, {
      clientX: -1,
      dataTransfer,
    });

    expect(screen.getByTestId("tabbed-panel-tab-drop-placeholder")).toBeTruthy();
    expect(reviewerTab.closest(".tabbed-panel-tab")?.classList.contains("dragging")).toBe(true);

    await fireEvent.dragEnd(reviewerTab);

    expect(screen.queryByTestId("tabbed-panel-tab-drop-placeholder")).toBeNull();
    expect(reviewerTab.closest(".tabbed-panel-tab")?.classList.contains("dragging")).toBe(false);
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

  it("shows newly discovered terminal sessions without auto-splitting them", async () => {
    localStorage.setItem("middleman-workspace-active-tab:ws-1", "home");
    const intervalCallbacks: Array<{ callback: () => void; delay: number | undefined }> = [];
    const setIntervalSpy = vi
      .spyOn(globalThis, "setInterval")
      .mockImplementation((callback: TimerHandler, delay?: number) => {
        intervalCallbacks.push({ callback: callback as () => void, delay });
        return 1 as unknown as ReturnType<typeof setInterval>;
      });
    const clearIntervalSpy = vi.spyOn(globalThis, "clearInterval").mockImplementation(() => undefined);
    mocks.getWorkspaceRuntime
      .mockResolvedValueOnce(runtimeWithTerminalSession())
      .mockResolvedValueOnce(runtimeWithTwoTerminalSessions());

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
    const runtimePoll = intervalCallbacks.find((interval) => interval.delay === 3000);
    expect(runtimePoll).toBeTruthy();

    runtimePoll!.callback();
    await waitFor(() => expect(screen.getByRole("button", { name: "Shell 2" })).toBeTruthy());
    expect(sockets.some((socket) => socket.url.includes("ws-1_shell_b"))).toBe(false);

    await fireEvent.click(screen.getByRole("button", { name: "Shell 2" }));

    await waitFor(() => expect(sockets.some((socket) => socket.url.includes("ws-1_shell_b"))).toBe(true));
    setIntervalSpy.mockRestore();
    clearIntervalSpy.mockRestore();
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

  it("disables middle-pane workspace controls while the selected workspace is deleting", async () => {
    localStorage.setItem("middleman-workspace-active-tab:ws-1", "home");
    mocks.getWorkspaceRuntime.mockResolvedValue(runtimeWithTwoTerminalSessions());
    const deleteRequest = deferred<Response>();
    const otherDeleteRequest = deferred<Response>();
    const otherWorkspaceResponse = {
      ...workspaceResponse,
      id: "ws-2",
      item_number: 8,
      worktree_path: "/tmp/worktree-2",
    };
    const fetchMock = vi.fn().mockImplementation((input: Request | URL | string, init?: RequestInit) => {
      const url = input instanceof Request ? input.url : String(input);
      const method = init?.method ?? (input instanceof Request ? input.method : "GET");
      const { pathname } = new URL(url, "http://localhost");
      if (method === "DELETE" && pathname.endsWith("/workspaces/ws-1")) {
        return deleteRequest.promise;
      }
      if (method === "DELETE" && pathname.endsWith("/workspaces/ws-2")) {
        return otherDeleteRequest.promise;
      }
      if (pathname.endsWith("/workspaces/ws-1")) {
        return Promise.resolve(Response.json(workspaceResponse));
      }
      if (pathname.endsWith("/workspaces/ws-2")) {
        return Promise.resolve(Response.json(otherWorkspaceResponse));
      }
      if (pathname.endsWith("/api/v1/workspaces")) {
        return Promise.resolve(Response.json({ workspaces: [workspaceResponse, otherWorkspaceResponse] }));
      }
      if (pathname.endsWith("/workspaces/ws-1/files") || pathname.endsWith("/workspaces/ws-2/files")) {
        return Promise.resolve(Response.json({ stale: false, whitespace_only_count: 0, files: [] }));
      }
      if (pathname.endsWith("/workspaces/ws-1/diff") || pathname.endsWith("/workspaces/ws-2/diff")) {
        return Promise.resolve(Response.json({ stale: false, whitespace_only_count: 0, files: [] }));
      }
      return Promise.resolve(Response.json({}));
    });
    vi.stubGlobal("fetch", fetchMock);
    vi.stubGlobal(
      "confirm",
      vi.fn(() => true),
    );
    window.history.pushState({}, "", "/terminal/ws-1");

    const view = render(WorkspaceTerminalView, {
      props: {
        workspaceId: "ws-1",
      },
    });

    await screen.findByRole("button", { name: "Launch" });
    await fireEvent.click(screen.getByRole("button", { name: "Open terminal panel" }));
    const shellPaneButton = await screen.findByRole("button", { name: "Focus Shell" });
    expect(shellPaneButton.getAttribute("draggable")).toBe("true");

    await fireEvent.click(screen.getByRole("button", { name: "Delete" }));
    await waitFor(() => {
      expect(
        fetchMock.mock.calls.some(([input]) => {
          if (!(input instanceof Request)) return false;
          const { pathname } = new URL(input.url);
          return input.method === "DELETE" && pathname === "/api/v1/workspaces/ws-1";
        }),
      ).toBe(true);
    });

    expect(screen.getAllByRole("button", { name: "Launch" }).every((button) => button.hasAttribute("disabled"))).toBe(
      true,
    );
    expect(screen.getByRole("button", { name: "Diff" }).hasAttribute("disabled")).toBe(true);
    expect(screen.getByRole("button", { name: "PR" }).hasAttribute("disabled")).toBe(true);
    expect(screen.getByRole("button", { name: "Reviews" }).hasAttribute("disabled")).toBe(true);
    expect(screen.getByRole("button", { name: "Delete" }).hasAttribute("disabled")).toBe(true);
    expect(screen.getByRole("button", { name: "Terminal options" }).hasAttribute("disabled")).toBe(true);
    expect(screen.getByRole("button", { name: "Focus Shell" }).hasAttribute("disabled")).toBe(true);
    expect(screen.getByRole("button", { name: "Focus Shell" }).getAttribute("draggable")).toBe("false");
    expect(screen.getByRole("button", { name: "Rename Shell" }).hasAttribute("disabled")).toBe(true);
    expect(screen.getByRole("button", { name: "Move Shell to workflow" }).hasAttribute("disabled")).toBe(true);
    expect(screen.getByRole("button", { name: "Close Shell" }).hasAttribute("disabled")).toBe(true);
    expect(screen.getAllByRole("button", { name: "Shell" }).every((button) => button.hasAttribute("disabled"))).toBe(
      true,
    );
    expect(
      screen.getAllByRole("button", { name: "Shell" }).every((button) => button.getAttribute("draggable") === "false"),
    ).toBe(true);

    window.history.pushState({}, "", "/terminal/ws-2");
    await view.rerender({ workspaceId: "ws-2" });
    await waitFor(() => {
      expect(
        fetchMock.mock.calls.some(([input]) => {
          if (!(input instanceof Request)) return false;
          const { pathname } = new URL(input.url);
          return pathname === "/api/v1/workspaces/ws-2";
        }),
      ).toBe(true);
    });
    await fireEvent.click(screen.getByRole("button", { name: "Delete" }));
    await waitFor(() => {
      expect(
        fetchMock.mock.calls.some(([input]) => {
          if (!(input instanceof Request)) return false;
          const { pathname } = new URL(input.url);
          return input.method === "DELETE" && pathname === "/api/v1/workspaces/ws-2";
        }),
      ).toBe(true);
    });
    expect(screen.getByRole("button", { name: "Delete" }).hasAttribute("disabled")).toBe(true);

    window.history.pushState({}, "", "/terminal/ws-1");
    await view.rerender({ workspaceId: "ws-1" });
    await waitFor(() => {
      expect(screen.getByRole("button", { name: "Focus Shell" }).hasAttribute("disabled")).toBe(true);
    });
    expect(screen.getByRole("button", { name: "Focus Shell" }).getAttribute("draggable")).toBe("false");
    expect(screen.getByRole("button", { name: "Rename Shell" }).hasAttribute("disabled")).toBe(true);
    expect(screen.getByRole("button", { name: "Move Shell to workflow" }).hasAttribute("disabled")).toBe(true);
    expect(screen.getByRole("button", { name: "Close Shell" }).hasAttribute("disabled")).toBe(true);

    otherDeleteRequest.resolve(new Response(null, { status: 204 }));
    deleteRequest.resolve(new Response(null, { status: 204 }));
    await waitFor(() => expect(window.location.pathname).toBe("/workspaces"));
  });

  it("disables active workflow terminal input while the selected workspace is deleting", async () => {
    const deleteRequest = deferred<Response>();
    const fetchMock = vi.fn().mockImplementation((input: Request | URL | string, init?: RequestInit) => {
      const url = input instanceof Request ? input.url : String(input);
      const method = init?.method ?? (input instanceof Request ? input.method : "GET");
      const { pathname } = new URL(url, "http://localhost");
      if (method === "DELETE" && pathname.endsWith("/workspaces/ws-1")) {
        return deleteRequest.promise;
      }
      if (pathname.endsWith("/workspaces/ws-1")) {
        return Promise.resolve(Response.json(workspaceResponse));
      }
      if (pathname.endsWith("/api/v1/workspaces")) {
        return Promise.resolve(Response.json({ workspaces: [workspaceResponse] }));
      }
      return Promise.resolve(Response.json({}));
    });
    vi.stubGlobal("fetch", fetchMock);
    vi.stubGlobal(
      "confirm",
      vi.fn(() => true),
    );
    window.history.pushState({}, "", "/terminal/ws-1");

    render(WorkspaceTerminalView, {
      props: {
        workspaceId: "ws-1",
      },
    });

    await screen.findByRole("tab", { name: /Helper/ });
    await waitFor(() => expect(mocks.mockTerminalInstances.length).toBeGreaterThanOrEqual(1));
    expect(mocks.mockTerminalInstances.some((terminal) => terminal.options.disableStdin === true)).toBe(false);
    const terminalDataHandler = mocks.mockOnData.mock.calls.at(-1)?.[0] as ((data: string) => void) | undefined;
    expect(terminalDataHandler).toBeTypeOf("function");

    await fireEvent.click(screen.getByRole("button", { name: "Delete" }));
    await waitFor(() => {
      expect(
        fetchMock.mock.calls.some(([input]) => {
          if (!(input instanceof Request)) return false;
          const { pathname } = new URL(input.url);
          return input.method === "DELETE" && pathname === "/api/v1/workspaces/ws-1";
        }),
      ).toBe(true);
    });
    await waitFor(() => {
      expect(screen.getByRole("button", { name: "Delete" }).hasAttribute("disabled")).toBe(true);
    });
    sockets.forEach((socket) => socket.send.mockClear());
    terminalDataHandler?.("echo blocked");
    expect(sockets.every((socket) => socket.send.mock.calls.length === 0)).toBe(true);

    deleteRequest.resolve(new Response(null, { status: 204 }));
    await waitFor(() => expect(window.location.pathname).toBe("/workspaces"));
  });
});
