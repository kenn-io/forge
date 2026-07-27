import { cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/svelte";
import { afterEach, beforeEach, describe, expect, it, vi } from "vite-plus/test";
import { createDiffStore } from "@middleman/ui/stores/diff";
import {
  clearActiveTabbedPanelDrag,
  getPaneLayoutStore,
  resetPaneLayoutStoresForTest,
  sessionPaneKey,
  startTabbedPanelTabDrag,
} from "@middleman/ui";
import {
  consumeWorkspaceLaunch,
  queueWorkspaceLaunch,
  resetWorkspaceCreatePendingForTest,
} from "@middleman/ui/stores/workspace-create-pending";
import { STORES_KEY } from "../../../../../packages/ui/src/context.js";

const mocks = vi.hoisted(() => ({
  getWorkspaceRuntime: vi.fn(),
  launchWorkspaceSession: vi.fn(),
  mockDispose: vi.fn(),
  mockFit: vi.fn(),
  mockLoadAddon: vi.fn(),
  mockOnData: vi.fn(),
  mockOpen: vi.fn(),
  mockSetTerminalSettings: vi.fn(),
  mockTerminalInstances: [] as Array<{
    focus: ReturnType<typeof vi.fn>;
    options: Record<string, unknown>;
  }>,
  mockUpdateSettings: vi.fn(),
  renameWorkspaceSession: vi.fn(),
  showFlash: vi.fn(),
  stopWorkspaceSession: vi.fn(),
  terminalWrite: vi.fn(),
  diffStore: null as unknown as ReturnType<typeof createDiffStore>,
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
      focus: vi.fn(),
      loadAddon: mocks.mockLoadAddon,
      modes: { bracketedPasteMode: false },
      onBinary: vi.fn(),
      onData: mocks.mockOnData,
      open: mocks.mockOpen,
      parser: {
        registerOscHandler: vi.fn(() => ({ dispose: vi.fn() })),
      },
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
        }),
        setTerminalSettings: mocks.mockSetTerminalSettings,
        getModeVisibility: () => ({
          activity: true,
          repos: true,
          kata: false,
          docs: false,
          pulls: true,
          issues: true,
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
      },
      diff: mocks.diffStore,
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

vi.mock("../../api/settings.js", () => ({
  updateSettings: mocks.mockUpdateSettings,
}));

vi.mock("@middleman/ui/stores/flash", () => ({
  showFlash: mocks.showFlash,
}));

// The harness pairs the view with the session terminal pool, which WorkspaceHost
// mounts in the app. Terminals live in the pool now, so the view on its own
// renders portal slots and no terminal would ever appear.
import WorkspaceTerminalView from "./WorkspaceTerminalViewTestHarness.svelte";
import WorkspacePaneControls from "./WorkspacePaneControls.svelte";
import { mountedSessions, resetSessionHostForTest, sessionHostPrefix } from "../../stores/session-host.svelte.ts";
import {
  activeHostedSession,
  getInlineWorkspaceController,
  hostedWorkspaceLauncher,
  hostedWorkspaceControls,
  workspaceControlsBusy,
  resetWorkspaceHostForTest,
} from "../../stores/workspace-host.svelte.ts";
import { navigate } from "../../stores/router.svelte.ts";

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
  enrichment_status: "fresh",
  created_at: "2026-04-29T00:00:00Z",
  mr_head_repo_kind: "same_repo",
};

/**
 * Serve any workspace id, not just ws-1.
 *
 * The default stub answers for ws-1 alone, so a test that switches the view to
 * another workspace gets a body with no id, the view never reports that workspace
 * live, and nothing renders - which makes "the overlay is gone" pass for the wrong
 * reason.
 */
function serveAnyWorkspace(): void {
  vi.stubGlobal(
    "fetch",
    vi.fn().mockImplementation((input: Request | URL | string) => {
      const url = input instanceof Request ? input.url : String(input);
      const { pathname } = new URL(url, "http://localhost");
      const match = /\/workspaces\/([^/]+)$/.exec(pathname);
      if (match) {
        return Promise.resolve(
          Response.json({ ...workspaceResponse, id: match[1], tmux_session: `middleman-${match[1]}` }),
        );
      }
      if (pathname.endsWith("/api/v1/workspaces")) {
        return Promise.resolve(Response.json({ workspaces: [workspaceResponse] }));
      }
      return Promise.resolve(Response.json({}));
    }),
  );
}

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

/** A workspace that can launch but is running nothing yet. */
function runtimeWithLaunchTargetsOnly() {
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
    sessions: [],
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

function runtimeWithCodexTarget(available = true, sessions: Array<Record<string, unknown>> = []) {
  return {
    launch_targets: [
      {
        key: "codex",
        label: "Codex",
        kind: "agent",
        source: "builtin",
        available,
        ...(available ? {} : { disabled_reason: "Codex is not configured" }),
      },
    ],
    sessions,
  };
}

function runtimeWithTwoWorkflowSessions() {
  return {
    launch_targets: [],
    sessions: [runningSession, reviewerSession],
  };
}

function fetchPath(input: Request | URL | string): string {
  const url = input instanceof Request ? input.url : String(input);
  return new URL(url, "http://localhost").pathname;
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

/** Home and one session tab in leaves of their own, so a demotion has a
 *  placement it could plausibly lose. */
function persistedSplitWorkflowLayout(sessionKey: string, region: "workflow" | "terminal" = "workflow") {
  return JSON.stringify({
    version: 1,
    open: region === "terminal",
    dock: "bottom",
    height: 300,
    activeSessionKey: region === "terminal" ? sessionKey : null,
    tree: region === "terminal" ? { type: "leaf", id: "dock-leaf", sessionKey } : null,
    sessionRegions: { [sessionKey]: region },
    workflowMode: "tabs",
    workflowTree: {
      type: "split",
      id: "wf-split",
      direction: "horizontal",
      ratio: 0.5,
      first: { type: "leaf", id: "wf-home", tabs: ["home"], activeTabKey: "home" },
      second:
        region === "workflow"
          ? { type: "leaf", id: "wf-session", tabs: [`session:${sessionKey}`], activeTabKey: `session:${sessionKey}` }
          : { type: "leaf", id: "wf-session", tabs: ["home"], activeTabKey: "home" },
    },
    customSessionLabels: {},
  });
}

/**
 * Two workflow sessions, each in its own leaf. A detail pane has no Home tab, so a
 * second session is what gives the strip something to render and a demotion
 * somewhere to land.
 */
function persistedTwoSessionWorkflowLayout(firstKey: string, secondKey: string) {
  return JSON.stringify({
    version: 1,
    open: false,
    dock: "bottom",
    height: 300,
    activeSessionKey: null,
    tree: null,
    sessionRegions: { [firstKey]: "workflow", [secondKey]: "workflow" },
    workflowMode: "tabs",
    workflowTree: {
      type: "split",
      id: "wf-split",
      direction: "horizontal",
      ratio: 0.5,
      first: {
        type: "leaf",
        id: "wf-first",
        tabs: [`session:${firstKey}`],
        activeTabKey: `session:${firstKey}`,
      },
      second: {
        type: "leaf",
        id: "wf-second",
        tabs: [`session:${secondKey}`],
        activeTabKey: `session:${secondKey}`,
      },
    },
    customSessionLabels: {},
  });
}

/**
 * Put the workspace on the PRs detail surface, the way the app does before an
 * embedded view exists: the session publication is surface-scoped, so a command
 * cannot reach a terminal rendered on a page the user is not looking at.
 */
/**
 * What the surface's container reports while the workspace pane is on screen.
 *
 * Promotion refuses without it, because a pane can hold a leaf in the stored tree
 * while rendering nothing (closed, tabbed behind a sibling, under another leaf's
 * zoom), and growing a split off screen looks to the user like the control failed.
 * The container notes this from its own render; a view rendered on its own here has
 * to stand in for it.
 */
function noteWorkspacePaneRendered(surface: "prs" | "issues" | "activity"): void {
  getPaneLayoutStore(surface).notePaneRender({
    editableTabs: ["conversation", "workspace"],
    onScreenTabs: ["conversation", "workspace"],
    flattened: false,
  });
}

function claimForPrs(): void {
  navigate("/pulls");
  getInlineWorkspaceController("prs").claim(
    {
      provider: "github",
      platformHost: "github.com",
      owner: "octo",
      name: "repo",
      repoPath: "octo/repo",
      number: 1,
      itemType: "pull",
    },
    { id: "ws-1", status: "ready" },
  );
}

function promoteSession(surface: "prs" | "issues" | "activity", sessionKey: string): string {
  const layout = getPaneLayoutStore(surface);
  const paneKey = sessionPaneKey("ws-1", undefined, sessionKey);
  const leafID = layout.leafIDForTab("conversation");
  if (leafID === null) throw new Error("surface default tree has no conversation leaf");
  if (!layout.promoteTab(paneKey, { kind: "tab", leafID })) throw new Error("promotion refused");
  return paneKey;
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
    resetSessionHostForTest();
    resetPaneLayoutStoresForTest();
    resetWorkspaceHostForTest();
    localStorage.setItem("middleman-workspace-active-tab:ws-1", "session:ws-1:helper");
    sockets = [];
    resetWorkspaceCreatePendingForTest();
    mocks.diffStore = createDiffStore();
    mocks.getWorkspaceRuntime.mockReset();
    mocks.getWorkspaceRuntime.mockResolvedValue(runtimeWithStaleSession());
    mocks.launchWorkspaceSession.mockReset();
    mocks.renameWorkspaceSession.mockReset();
    mocks.showFlash.mockReset();
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
    mocks.mockSetTerminalSettings.mockReset();
    mocks.mockUpdateSettings.mockReset();
    mocks.mockUpdateSettings.mockImplementation(async ({ terminal }) => ({
      terminal,
    }));

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
        unobserve(): void {}
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

    expect(await screen.findByText("Create a workspace to run agents on a branch")).toBeTruthy();
    expect(screen.getByText(/a PR workspace checks out the PR head/i)).toBeTruthy();
    expect(screen.getByText(/From a PR or issue, use the/i)).toBeTruthy();
    expect(screen.getByText(/use New workspace in the sidebar/i)).toBeTruthy();
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

  it("uses an idle status for a live workflow session without changing the tab name", async () => {
    render(WorkspaceTerminalView, {
      props: {
        workspaceId: "ws-1",
      },
    });

    expect(await screen.findByRole("tab", { name: "Helper, Helper running" })).toBeTruthy();
    expect(screen.getByLabelText("Helper running").classList.contains("kit-status-dot--idle")).toBe(true);
  });

  it("persists toolbar and focused-terminal font zoom through shared settings", async () => {
    const { container } = render(WorkspaceTerminalView, {
      props: {
        workspaceId: "ws-1",
      },
    });

    await fireEvent.click(
      await screen.findByRole("button", {
        name: "Increase terminal font size",
      }),
    );
    await waitFor(() => {
      expect(mocks.mockUpdateSettings).toHaveBeenCalledWith({
        terminal: expect.objectContaining({ font_size: 15 }),
      });
    });
    expect(mocks.mockSetTerminalSettings).toHaveBeenCalledWith(expect.objectContaining({ font_size: 15 }));

    mocks.mockUpdateSettings.mockClear();
    const terminalInput = document.createElement("textarea");
    container.querySelector(".terminal-container")?.append(terminalInput);
    terminalInput.focus();
    await fireEvent.keyDown(terminalInput, {
      key: "=",
      metaKey: true,
    });
    await waitFor(() => {
      expect(mocks.mockUpdateSettings).toHaveBeenCalledWith({
        terminal: expect.objectContaining({ font_size: 15 }),
      });
    });
  });

  it.each([
    ["starting", "Helper starting", "kit-status-dot--stale"],
    ["error", "Helper unavailable", "kit-status-dot--unclean"],
  ] as const)("maps a %s workflow session to a semantic tab status", async (status, label, className) => {
    mocks.getWorkspaceRuntime.mockResolvedValue({
      launch_targets: [],
      sessions: [{ ...runningSession, status }],
    });

    render(WorkspaceTerminalView, {
      props: {
        workspaceId: "ws-1",
      },
    });

    expect(await screen.findByRole("tab", { name: `Helper, ${label}` })).toBeTruthy();
    expect(screen.getByLabelText(label).classList.contains(className)).toBe(true);
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

  it("starts the runtime request before workspace metadata resolves without fetching it twice", async () => {
    const workspaceRequest = deferred<Response>();
    const runtimeRequest = deferred<ReturnType<typeof runtimeWithStaleSession>>();
    let workspaceRequestStarted = false;
    vi.stubGlobal(
      "fetch",
      vi.fn().mockImplementation((input: Request | URL | string) => {
        const url = input instanceof Request ? input.url : String(input);
        const { pathname } = new URL(url, "http://localhost");
        if (pathname.endsWith("/workspaces/ws-1")) {
          workspaceRequestStarted = true;
          return workspaceRequest.promise;
        }
        if (pathname.endsWith("/api/v1/workspaces")) {
          return Promise.resolve(Response.json({ workspaces: [workspaceResponse] }));
        }
        return Promise.resolve(Response.json({}));
      }),
    );
    mocks.getWorkspaceRuntime.mockReturnValue(runtimeRequest.promise);

    render(WorkspaceTerminalView, {
      props: {
        workspaceId: "ws-1",
      },
    });

    await waitFor(() => expect(workspaceRequestStarted).toBe(true));
    expect(mocks.getWorkspaceRuntime).toHaveBeenCalledWith("ws-1", undefined);

    runtimeRequest.resolve(runtimeWithStaleSession());
    workspaceRequest.resolve(Response.json(workspaceResponse));

    await screen.findByText("acme/widget");
    await screen.findByRole("tab", { name: /Helper/ });
    expect(mocks.getWorkspaceRuntime).toHaveBeenCalledTimes(1);
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
    const initialRuntime = deferred<{ launch_targets: never[]; sessions: never[] }>();
    mocks.getWorkspaceRuntime
      .mockReturnValueOnce(initialRuntime.promise)
      .mockResolvedValueOnce(runtimeWithSession("2026-04-29T00:03:00Z"));

    render(WorkspaceTerminalView, {
      props: {
        workspaceId: "ws-1",
      },
    });

    await waitFor(() => expect(mocks.getWorkspaceRuntime).toHaveBeenCalledWith("ws-1", undefined));
    await waitFor(() => expect(intervalCallbacks.some((interval) => interval.delay === 3000)).toBe(true));
    initialRuntime.resolve({ launch_targets: [], sessions: [] });
    await new Promise((resolve) => setTimeout(resolve, 0));
    expect(screen.queryByRole("tab", { name: /Helper/ })).toBeNull();
    const runtimePoll = intervalCallbacks.find((interval) => interval.delay === 3000);
    expect(runtimePoll).toBeTruthy();

    runtimePoll!.callback();

    await waitFor(() => expect(screen.getByRole("tab", { name: /Helper/ })).toBeTruthy());
    expect(mocks.getWorkspaceRuntime).toHaveBeenCalledTimes(2);
    setIntervalSpy.mockRestore();
    clearIntervalSpy.mockRestore();
  });

  it("does not reapply identical runtime polls to an active terminal", async () => {
    const intervalCallbacks: Array<{ callback: () => void; delay: number | undefined }> = [];
    vi.spyOn(globalThis, "setInterval").mockImplementation((callback: TimerHandler, delay?: number) => {
      intervalCallbacks.push({ callback: callback as () => void, delay });
      return 1 as unknown as ReturnType<typeof setInterval>;
    });
    vi.spyOn(globalThis, "clearInterval").mockImplementation(() => undefined);
    const requestAnimationFrameSpy = vi.fn((callback: FrameRequestCallback) => {
      callback(0);
      return 1;
    });
    vi.stubGlobal("requestAnimationFrame", requestAnimationFrameSpy);
    const layoutStorageSpy = vi.spyOn(Storage.prototype, "setItem");
    const runtimePayload = runtimeWithStaleSession();
    mocks.getWorkspaceRuntime.mockImplementation(async () => structuredClone(runtimePayload));

    render(WorkspaceTerminalView, {
      props: {
        workspaceId: "ws-1",
      },
    });

    await screen.findByRole("tab", { name: /Helper/ });
    await waitFor(() => expect(mocks.mockTerminalInstances).toHaveLength(1));
    const runtimePoll = intervalCallbacks.find((interval) => interval.delay === 3000);
    expect(runtimePoll).toBeTruthy();
    const runtimeRequestCount = mocks.getWorkspaceRuntime.mock.calls.length;
    const terminalCount = mocks.mockTerminalInstances.length;
    mocks.mockFit.mockClear();
    requestAnimationFrameSpy.mockClear();
    layoutStorageSpy.mockClear();

    runtimePoll!.callback();

    await waitFor(() => expect(mocks.getWorkspaceRuntime).toHaveBeenCalledTimes(runtimeRequestCount + 1));
    await Promise.resolve();
    expect(mocks.mockTerminalInstances).toHaveLength(terminalCount);
    expect(mocks.mockFit).not.toHaveBeenCalled();
    expect(requestAnimationFrameSpy).not.toHaveBeenCalled();
    expect(layoutStorageSpy).not.toHaveBeenCalled();
  });

  it("reapplies an authoritative runtime response after a local rename", async () => {
    const intervalCallbacks: Array<{ callback: () => void; delay: number | undefined }> = [];
    vi.spyOn(globalThis, "setInterval").mockImplementation((callback: TimerHandler, delay?: number) => {
      intervalCallbacks.push({ callback: callback as () => void, delay });
      return 1 as unknown as ReturnType<typeof setInterval>;
    });
    vi.spyOn(globalThis, "clearInterval").mockImplementation(() => undefined);
    const serverRuntime = runtimeWithStaleSession();
    mocks.getWorkspaceRuntime.mockImplementation(async () => structuredClone(serverRuntime));

    render(WorkspaceTerminalView, {
      props: {
        workspaceId: "ws-1",
      },
    });

    await screen.findByRole("tab", { name: /Helper/ });
    await fireEvent.click(screen.getByRole("button", { name: "Rename Helper" }));
    const input = await screen.findByRole("textbox", { name: "Name" });
    await fireEvent.input(input, { target: { value: "Review helper" } });
    await fireEvent.click(screen.getByRole("button", { name: "Save" }));
    await screen.findByRole("tab", { name: /Review helper/ });

    const runtimePoll = intervalCallbacks.find((interval) => interval.delay === 3000);
    expect(runtimePoll).toBeTruthy();
    runtimePoll!.callback();

    await waitFor(() => expect(screen.getByRole("tab", { name: "Helper, Helper running" })).toBeTruthy());
    expect(screen.queryByRole("tab", { name: /Review helper/ })).toBeNull();
  });

  it("ignores a runtime poll that started before a local rename", async () => {
    const intervalCallbacks: Array<{ callback: () => void; delay: number | undefined }> = [];
    vi.spyOn(globalThis, "setInterval").mockImplementation((callback: TimerHandler, delay?: number) => {
      intervalCallbacks.push({ callback: callback as () => void, delay });
      return 1 as unknown as ReturnType<typeof setInterval>;
    });
    vi.spyOn(globalThis, "clearInterval").mockImplementation(() => undefined);
    const stalePoll = deferred<ReturnType<typeof runtimeWithStaleSession>>();
    mocks.getWorkspaceRuntime
      .mockResolvedValueOnce(runtimeWithStaleSession())
      .mockReturnValueOnce(stalePoll.promise)
      .mockResolvedValueOnce({
        ...runtimeWithStaleSession(),
        sessions: [{ ...runningSession, label: "Review helper" }],
      });

    render(WorkspaceTerminalView, {
      props: {
        workspaceId: "ws-1",
      },
    });

    await screen.findByRole("tab", { name: "Helper, Helper running" });
    const runtimePoll = intervalCallbacks.find((interval) => interval.delay === 3000);
    expect(runtimePoll).toBeTruthy();
    runtimePoll!.callback();
    await waitFor(() => expect(mocks.getWorkspaceRuntime).toHaveBeenCalledTimes(2));

    await fireEvent.click(screen.getByRole("button", { name: "Rename Helper" }));
    const input = await screen.findByRole("textbox", { name: "Name" });
    await fireEvent.input(input, { target: { value: "Review helper" } });
    await fireEvent.click(screen.getByRole("button", { name: "Save" }));
    await screen.findByRole("tab", { name: /Review helper/ });

    stalePoll.resolve(runtimeWithStaleSession());
    await new Promise((resolve) => setTimeout(resolve, 0));
    expect(screen.getByRole("tab", { name: /Review helper/ })).toBeTruthy();

    runtimePoll!.callback();
    await waitFor(() => expect(mocks.getWorkspaceRuntime).toHaveBeenCalledTimes(3));
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
    const initialRuntime = deferred<{ launch_targets: never[]; sessions: never[] }>();
    mocks.getWorkspaceRuntime
      .mockReturnValueOnce(initialRuntime.promise)
      .mockResolvedValueOnce(runtimeWithSession("2026-04-29T00:03:00Z"));

    render(WorkspaceTerminalView, {
      props: {
        workspaceId: "ws-1",
        workspaceHostKey: "member",
      },
    });

    await waitFor(() => expect(mocks.getWorkspaceRuntime).toHaveBeenCalledWith("ws-1", "member"));
    await waitFor(() => expect(intervalCallbacks.some((interval) => interval.delay === 3000)).toBe(true));
    initialRuntime.resolve({ launch_targets: [], sessions: [] });
    await new Promise((resolve) => setTimeout(resolve, 0));
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

  it("recovers pending enrichment that completed before the event stream opened", async () => {
    const eventListeners: Record<string, () => void> = {};
    const pendingWorkspace = {
      ...workspaceResponse,
      enrichment_status: "pending",
    };
    const fetchMock = vi.fn().mockImplementation((input: Request | URL | string) => {
      const url = input instanceof Request ? input.url : String(input);
      const { pathname } = new URL(url, "http://localhost");
      if (pathname.endsWith("/workspaces/ws-1")) {
        return Promise.resolve(Response.json(pendingWorkspace));
      }
      if (pathname.endsWith("/api/v1/workspaces")) {
        return Promise.resolve(Response.json({ workspaces: [pendingWorkspace] }));
      }
      return Promise.resolve(Response.json({}));
    });
    vi.stubGlobal("fetch", fetchMock);
    vi.stubGlobal(
      "EventSource",
      class {
        addEventListener(type: string, callback: () => void): void {
          eventListeners[type] = callback;
        }
        close(): void {}
      },
    );

    render(WorkspaceTerminalView, { props: { workspaceId: "ws-1" } });
    await waitFor(() => {
      expect(
        fetchMock.mock.calls.some(([input]) => fetchPath(input as Request | URL | string).endsWith("/workspaces/ws-1")),
      ).toBe(true);
    });
    const beforeOpen = fetchMock.mock.calls.filter(([input]) =>
      fetchPath(input as Request | URL | string).endsWith("/workspaces/ws-1"),
    ).length;

    eventListeners.open?.();

    await waitFor(() => {
      const afterOpen = fetchMock.mock.calls.filter(([input]) =>
        fetchPath(input as Request | URL | string).endsWith("/workspaces/ws-1"),
      ).length;
      expect(afterOpen).toBe(beforeOpen + 1);
    });
  });

  it("scopes only local workspace event streams for diff prewarming", async () => {
    const urls: string[] = [];
    vi.stubGlobal(
      "EventSource",
      class {
        constructor(url: string) {
          urls.push(url);
        }
        addEventListener(): void {}
        close(): void {}
      },
    );

    const { rerender } = render(WorkspaceTerminalView, { props: { workspaceId: "ws-1" } });
    await waitFor(() => expect(urls).toContain("/api/v1/events?workspace_id=ws-1"));

    await rerender({ workspaceId: "ws-1", workspaceHostKey: "member" });
    await waitFor(() => expect(urls.at(-1)).toBe("/api/v1/events"));
  });

  it("prewarms a selected fleet diff and reloads it when the remote watch advances", async () => {
    window.__BASE_PATH__ = window.location.origin;
    localStorage.setItem("middleman-workspace-sidebar-open", "true");
    localStorage.setItem("middleman-workspace-sidebar-tab", "diff");
    const changed = deferred<Response>();
    let watchCalls = 0;
    const fetchMock = vi.fn().mockImplementation((input: Request | URL | string) => {
      const raw = input instanceof Request ? input.url : String(input);
      const url = new URL(raw, "http://localhost");
      if (url.pathname.endsWith("/fleet/hosts/member/workspaces/ws-1/diff/watch")) {
        watchCalls += 1;
        if (watchCalls === 1) {
          return Promise.resolve(Response.json({ changed: true, version: "fleet:1" }));
        }
        if (watchCalls === 2) return changed.promise;
        return new Promise<Response>(() => {});
      }
      if (url.pathname.endsWith("/fleet/hosts/member/workspaces/ws-1")) {
        return Promise.resolve(Response.json({ ...workspaceResponse, fleet_host_key: "member" }));
      }
      if (url.pathname.endsWith("/api/v1/workspaces")) {
        return Promise.resolve(Response.json({ workspaces: [] }));
      }
      return Promise.resolve(Response.json({}));
    });
    vi.stubGlobal("fetch", fetchMock);
    const loadWorkspaceDiff = vi.spyOn(mocks.diffStore, "loadWorkspaceDiff").mockResolvedValue();

    render(WorkspaceTerminalView, {
      props: { workspaceId: "ws-1", workspaceHostKey: "member" },
      context: new Map([[STORES_KEY, { diff: mocks.diffStore }]]),
    });

    await waitFor(() => expect(watchCalls).toBe(2));
    await waitFor(() => expect(loadWorkspaceDiff).toHaveBeenCalled());
    const beforeChange = loadWorkspaceDiff.mock.calls.length;

    changed.resolve(Response.json({ changed: true, version: "fleet:2" }));

    await waitFor(() => expect(loadWorkspaceDiff.mock.calls.length).toBeGreaterThan(beforeChange));
    expect(loadWorkspaceDiff).toHaveBeenLastCalledWith(
      "ws-1",
      "head",
      false,
      expect.objectContaining({ workspaceHostKey: "member", preserveVisible: true }),
    );
  });

  it("retries a fleet diff watch while the workspace transitions from creating to ready", async () => {
    window.__BASE_PATH__ = window.location.origin;
    localStorage.setItem("middleman-workspace-sidebar-open", "true");
    localStorage.setItem("middleman-workspace-sidebar-tab", "diff");
    vi.spyOn(Math, "random").mockReturnValue(0);
    let watchCalls = 0;
    const fetchMock = vi.fn().mockImplementation((input: Request | URL | string) => {
      const raw = input instanceof Request ? input.url : String(input);
      const url = new URL(raw, "http://localhost");
      if (url.pathname.endsWith("/fleet/hosts/member/workspaces/ws-1/diff/watch")) {
        watchCalls += 1;
        if (watchCalls === 1) {
          return Promise.resolve(Response.json({ detail: "workspace is not ready" }, { status: 409 }));
        }
        if (watchCalls === 2) {
          return Promise.resolve(Response.json({ changed: true, version: "fleet:ready" }));
        }
        return new Promise<Response>(() => {});
      }
      if (url.pathname.endsWith("/fleet/hosts/member/workspaces/ws-1")) {
        return Promise.resolve(Response.json({ ...workspaceResponse, fleet_host_key: "member" }));
      }
      if (url.pathname.endsWith("/api/v1/workspaces")) {
        return Promise.resolve(Response.json({ workspaces: [] }));
      }
      return Promise.resolve(Response.json({}));
    });
    vi.stubGlobal("fetch", fetchMock);
    const loadWorkspaceDiff = vi.spyOn(mocks.diffStore, "loadWorkspaceDiff").mockResolvedValue();

    render(WorkspaceTerminalView, {
      props: { workspaceId: "ws-1", workspaceHostKey: "member" },
      context: new Map([[STORES_KEY, { diff: mocks.diffStore }]]),
    });

    await waitFor(() => expect(watchCalls).toBe(3), { timeout: 2_000 });
    expect(
      fetchMock.mock.calls.some(([input]) => {
        const raw = input instanceof Request ? input.url : String(input);
        return new URL(raw, "http://localhost").searchParams.get("version") === "fleet:ready";
      }),
    ).toBe(true);
    await waitFor(() => {
      expect(loadWorkspaceDiff).toHaveBeenLastCalledWith(
        "ws-1",
        "head",
        false,
        expect.objectContaining({ workspaceHostKey: "member", preserveVisible: true }),
      );
    });
  });

  it("removes the old sidebar and waits for matching runtime before loading the new diff", async () => {
    window.__BASE_PATH__ = window.location.origin;
    localStorage.setItem("middleman-workspace-sidebar-open", "true");
    localStorage.setItem("middleman-workspace-sidebar-tab", "diff");
    const workspaceB = { ...workspaceResponse, id: "ws-2", git_head_ref: "feature/two" };
    const workspaceBGate = deferred<typeof workspaceB>();
    const runtimeBGate = deferred<ReturnType<typeof runtimeWithStaleSession>>();
    const eventListeners: Array<Record<string, (event: MessageEvent) => void>> = [];
    const loadWorkspaceDiff = vi.spyOn(mocks.diffStore, "loadWorkspaceDiff").mockResolvedValue();

    vi.stubGlobal(
      "fetch",
      vi.fn().mockImplementation((input: Request | URL | string) => {
        const path = fetchPath(input);
        if (path.endsWith("/workspaces/ws-1")) return Promise.resolve(Response.json(workspaceResponse));
        if (path.endsWith("/workspaces/ws-2")) {
          return workspaceBGate.promise.then((workspace) => Response.json(workspace));
        }
        if (path.endsWith("/api/v1/workspaces")) {
          return Promise.resolve(Response.json({ workspaces: [workspaceResponse, workspaceB] }));
        }
        return Promise.resolve(Response.json({}));
      }),
    );
    vi.stubGlobal(
      "EventSource",
      class {
        private listeners: Record<string, (event: MessageEvent) => void> = {};
        constructor() {
          eventListeners.push(this.listeners);
        }
        addEventListener(type: string, callback: (event: MessageEvent) => void): void {
          this.listeners[type] = callback;
        }
        close(): void {}
      },
    );
    mocks.getWorkspaceRuntime
      .mockResolvedValueOnce(runtimeWithStaleSession())
      .mockReturnValueOnce(runtimeBGate.promise);

    const { rerender } = render(WorkspaceTerminalView, {
      props: { workspaceId: "ws-1" },
      context: new Map([[STORES_KEY, { diff: mocks.diffStore }]]),
    });
    await waitFor(() => expect(loadWorkspaceDiff).toHaveBeenCalledWith("ws-1", "head", false, expect.anything()));

    await rerender({ workspaceId: "ws-2" });

    // Liveness gating unmounts the stale ws-1 view entirely while ws-2
    // loads: the old toolbar and sidebar are gone, not lingering behind
    // action guards.
    expect(await screen.findByText("Setting up workspace...")).toBeTruthy();
    expect(screen.queryByRole("region", { name: "Workspace Diff" })).toBeNull();
    expect(loadWorkspaceDiff).toHaveBeenCalledTimes(1);

    eventListeners.at(-1)?.workspace_diff_ready?.(
      new MessageEvent("workspace_diff_ready", {
        data: JSON.stringify({ workspace_id: "ws-2", version: "generation:2" }),
      }),
    );
    workspaceBGate.resolve(workspaceB);
    // ws-2's payload landed but its runtime is still pending: the ready
    // view mounts with the details-loading sub-state and the diff still
    // waits for the matching runtime.
    expect(await screen.findByText("Loading workspace details...")).toBeTruthy();
    expect(screen.queryByRole("region", { name: "Workspace Diff" })).toBeNull();
    expect(loadWorkspaceDiff).toHaveBeenCalledTimes(1);

    runtimeBGate.resolve(runtimeWithStaleSession());
    await waitFor(() => expect(loadWorkspaceDiff).toHaveBeenCalledWith("ws-2", "head", false, expect.anything()));
    expect(screen.queryByText("Loading workspace details...")).toBeNull();
  });

  it("renders matching workspace details when runtime loading fails", async () => {
    window.__BASE_PATH__ = window.location.origin;
    localStorage.setItem("middleman-workspace-sidebar-open", "true");
    localStorage.setItem("middleman-workspace-sidebar-tab", "diff");
    const loadWorkspaceDiff = vi.spyOn(mocks.diffStore, "loadWorkspaceDiff").mockResolvedValue();

    vi.stubGlobal(
      "fetch",
      vi.fn().mockImplementation((input: Request | URL | string) => {
        const path = fetchPath(input);
        if (path.endsWith("/workspaces/ws-1")) return Promise.resolve(Response.json(workspaceResponse));
        if (path.endsWith("/api/v1/workspaces")) {
          return Promise.resolve(Response.json({ workspaces: [workspaceResponse] }));
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
    mocks.getWorkspaceRuntime.mockRejectedValue(new Error("runtime unavailable"));

    render(WorkspaceTerminalView, {
      props: { workspaceId: "ws-1" },
      context: new Map([[STORES_KEY, { diff: mocks.diffStore }]]),
    });

    expect(await screen.findByText("runtime unavailable")).toBeTruthy();
    await waitFor(() => expect(loadWorkspaceDiff).toHaveBeenCalledWith("ws-1", "head", false, expect.anything()));
    expect(screen.queryByText("Loading workspace details...")).toBeNull();
  });

  it("treats id-less workspace status events as global invalidation", async () => {
    const eventListeners: Record<string, (event: MessageEvent) => void> = {};
    const fetchMock = vi.fn().mockImplementation((input: Request | URL | string) => {
      const url = input instanceof Request ? input.url : String(input);
      const { pathname } = new URL(url, "http://localhost");
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
      "EventSource",
      class {
        addEventListener(type: string, callback: (event: MessageEvent) => void): void {
          eventListeners[type] = callback;
        }
        close(): void {}
      },
    );

    render(WorkspaceTerminalView, { props: { workspaceId: "ws-1" } });
    await waitFor(() => expect(fetchMock).toHaveBeenCalled());
    fetchMock.mockClear();

    eventListeners.workspace_status?.(new MessageEvent("workspace_status", { data: "{}" }));

    await waitFor(() => {
      expect(
        fetchMock.mock.calls.some(([input]) => fetchPath(input as Request | URL | string).endsWith("/workspaces/ws-1")),
      ).toBe(true);
    });
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
    await waitFor(() => expect(intervalCallbacks.some((interval) => interval.delay === 3000)).toBe(true));
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

  it("hands back the previous workspace's pooled terminals when the selection moves on", async () => {
    // The pool outlives this view, so nothing else would take them down. Every
    // parked terminal holds a live websocket; browsing ten workspaces must not
    // leave ten attachments open.
    const { rerender } = render(WorkspaceTerminalView, {
      props: { workspaceId: "ws-1" },
    });

    await screen.findByRole("tab", { name: /Helper/ });
    await waitFor(() => expect(mountedSessions()).toHaveLength(1));
    const firstPrefix = sessionHostPrefix("ws-1", undefined);
    expect(mountedSessions()[0]?.hostKey.startsWith(firstPrefix)).toBe(true);

    await rerender({ workspaceId: "ws-2" });

    await waitFor(() => {
      expect(mountedSessions().some((session) => session.hostKey.startsWith(firstPrefix))).toBe(false);
    });
  });

  it("hands back its pooled terminals when the view itself goes away", async () => {
    render(WorkspaceTerminalView, { props: { workspaceId: "ws-1" } });

    await screen.findByRole("tab", { name: /Helper/ });
    await waitFor(() => expect(mountedSessions()).toHaveLength(1));

    cleanup();

    expect(mountedSessions()).toHaveLength(0);
  });

  it("shows a relaunched agent with the same key and a new generation", async () => {
    const relaunchedAt = "2026-04-29T00:01:00Z";
    const initialRuntime = deferred<ReturnType<typeof runtimeWithStaleSession>>();
    mocks.getWorkspaceRuntime
      .mockReturnValueOnce(initialRuntime.promise)
      .mockResolvedValueOnce(runtimeWithSession(relaunchedAt));

    render(WorkspaceTerminalView, {
      props: {
        workspaceId: "ws-1",
      },
    });

    await screen.findByRole("button", { name: "Refresh workspace details" });
    initialRuntime.resolve(runtimeWithStaleSession());
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

    await waitFor(() => expect(screen.getAllByRole("tab", { name: "Helper, Helper running" })).toHaveLength(2));
    expect(screen.queryByRole("tab", { name: /Helper 2/ })).toBeNull();
  });

  it("renames a workflow tab by its opaque session key", async () => {
    mocks.getWorkspaceRuntime.mockResolvedValue(runtimeWithDuplicateWorkflowSessions());

    render(WorkspaceTerminalView, {
      props: {
        workspaceId: "ws-1",
      },
    });

    await screen.findByRole("tab", { name: "Helper, Helper running" });
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

  it("keeps a split terminal when an older runtime poll resolves", async () => {
    localStorage.setItem("middleman-workspace-active-tab:ws-1", "home");
    const intervalCallbacks: Array<{ callback: () => void; delay: number | undefined }> = [];
    vi.spyOn(globalThis, "setInterval").mockImplementation((callback: TimerHandler, delay?: number) => {
      intervalCallbacks.push({ callback: callback as () => void, delay });
      return 1 as unknown as ReturnType<typeof setInterval>;
    });
    vi.spyOn(globalThis, "clearInterval").mockImplementation(() => undefined);
    const stalePoll = deferred<ReturnType<typeof runtimeWithTerminalSession>>();
    mocks.getWorkspaceRuntime
      .mockResolvedValueOnce(runtimeWithTerminalSession())
      .mockReturnValueOnce(stalePoll.promise)
      .mockResolvedValueOnce(runtimeWithTwoTerminalSessions());
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
    const runtimePoll = intervalCallbacks.find((interval) => interval.delay === 3000);
    expect(runtimePoll).toBeTruthy();
    runtimePoll!.callback();
    await waitFor(() => expect(mocks.getWorkspaceRuntime).toHaveBeenCalledTimes(2));

    await fireEvent.click(screen.getByRole("button", { name: "Split terminal right" }));
    await waitFor(() => expect(screen.getByRole("button", { name: "Shell 2" })).toBeTruthy());

    stalePoll.resolve(runtimeWithTerminalSession());
    await new Promise((resolve) => setTimeout(resolve, 0));
    expect(screen.getByRole("button", { name: "Shell 2" })).toBeTruthy();

    runtimePoll!.callback();
    await waitFor(() => expect(mocks.getWorkspaceRuntime).toHaveBeenCalledTimes(3));
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
    const initialRuntime = deferred<ReturnType<typeof runtimeWithTerminalSession>>();
    mocks.getWorkspaceRuntime
      .mockReturnValueOnce(initialRuntime.promise)
      .mockResolvedValueOnce(runtimeWithTwoTerminalSessions());

    render(WorkspaceTerminalView, {
      props: {
        workspaceId: "ws-1",
      },
    });

    await screen.findByRole("button", { name: "Refresh workspace details" });
    initialRuntime.resolve(runtimeWithTerminalSession());
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
    const initialRuntime = deferred<ReturnType<typeof runtimeWithTerminalSession>>();
    const staleRefresh = deferred<ReturnType<typeof runtimeWithTerminalSession>>();
    const freshRefresh = deferred<ReturnType<typeof runtimeWithTerminalSession>>();
    mocks.getWorkspaceRuntime
      .mockReturnValueOnce(initialRuntime.promise)
      .mockReturnValueOnce(staleRefresh.promise)
      .mockReturnValueOnce(freshRefresh.promise);
    mocks.launchWorkspaceSession.mockResolvedValue(relaunchedShellSession);

    render(WorkspaceTerminalView, {
      props: {
        workspaceId: "ws-1",
      },
    });

    await screen.findByRole("button", { name: "Refresh workspace details" });
    initialRuntime.resolve(runtimeWithTerminalSession());
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

  it("keeps one live terminal while a shell moves between the terminal panel and the workflow area", async () => {
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
    const socket = sockets[0]!;
    expect(socket.url).toContain("ws-1_shell_a");

    await fireEvent.click(screen.getByRole("button", { name: "Move Shell to workflow" }));
    await screen.findByRole("tab", { name: /Shell/ });
    await fireEvent.click(screen.getByRole("button", { name: "Move Shell to terminal" }));
    await waitFor(() => expect(screen.queryByRole("tab", { name: /Shell/ })).toBeNull());

    // One tmux attachment from start to finish. Both regions render the same
    // pooled terminal, so moving between them reparents it instead of tearing
    // the shell down and reattaching to a scrollback-less new one.
    expect(sockets.filter((candidate) => candidate.url.includes("ws-1_shell_a"))).toHaveLength(1);
    expect(socket.close).not.toHaveBeenCalled();
  });

  it("hands a docked terminal back when the terminal panel closes", async () => {
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

    await fireEvent.click(screen.getAllByRole("button", { name: "Close terminal panel" })[0]!);

    // A closed panel renders nothing, and a pooled terminal nothing renders
    // would otherwise sit parked with its socket open forever.
    await waitFor(() => expect(sockets[0]!.close).toHaveBeenCalled());
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
    // Wait for ws-2's data to be applied (Delete re-enables), not
    // merely for its request to be issued: handleDelete intentionally
    // ignores clicks during the in-place transition window, so
    // clicking earlier races the metadata response's microtasks.
    await waitFor(() => {
      expect(screen.getByRole("button", { name: "Delete" }).hasAttribute("disabled")).toBe(false);
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

  it("drops a failed delete response after unmounting and remounting the workspace", async () => {
    localStorage.setItem("middleman-workspace-active-tab:ws-1", "home");
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
    window.history.pushState({}, "", "/terminal/ws-1");

    const firstVisit = render(WorkspaceTerminalView, {
      props: {
        workspaceId: "ws-1",
      },
    });

    await screen.findByRole("button", { name: "Delete" });
    await fireEvent.click(screen.getByRole("button", { name: "Delete" }));
    await waitFor(() => {
      expect(fetchMock.mock.calls.some(([input]) => input instanceof Request && input.method === "DELETE")).toBe(true);
    });

    firstVisit.unmount();
    render(WorkspaceTerminalView, {
      props: {
        workspaceId: "ws-1",
      },
    });
    await screen.findByRole("button", { name: "Delete" });

    deleteRequest.resolve(Response.json({ detail: "Old workspace delete failed." }, { status: 500 }));
    await new Promise((resolve) => setTimeout(resolve, 0));

    expect(mocks.showFlash).not.toHaveBeenCalled();
  });

  it("reports a successful delete even after switching to another workspace", async () => {
    localStorage.setItem("middleman-workspace-active-tab:ws-1", "home");
    const deleteRequest = deferred<Response>();
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
      if (pathname.endsWith("/workspaces/ws-1")) {
        return Promise.resolve(Response.json(workspaceResponse));
      }
      if (pathname.endsWith("/workspaces/ws-2")) {
        return Promise.resolve(Response.json(otherWorkspaceResponse));
      }
      if (pathname.endsWith("/api/v1/workspaces")) {
        return Promise.resolve(Response.json({ workspaces: [workspaceResponse, otherWorkspaceResponse] }));
      }
      return Promise.resolve(Response.json({}));
    });
    vi.stubGlobal("fetch", fetchMock);
    window.history.pushState({}, "", "/terminal/ws-1");

    const onWorkspaceDeleted = vi.fn();
    const view = render(WorkspaceTerminalView, {
      props: {
        workspaceId: "ws-1",
        onWorkspaceDeleted,
      },
    });

    await screen.findByRole("button", { name: "Delete" });
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

    // Switch to another workspace while the delete is still in flight.
    window.history.pushState({}, "", "/terminal/ws-2");
    await view.rerender({ workspaceId: "ws-2" });

    deleteRequest.resolve(new Response(null, { status: 204 }));

    // The server destroyed ws-1 regardless of the current selection:
    // inline claimants, tombstones, and route memory must still hear
    // about it, while navigation stays put on ws-2.
    await waitFor(() =>
      expect(onWorkspaceDeleted).toHaveBeenCalledWith("ws-1", undefined, {
        provider: "github",
        platformHost: "github.com",
        owner: "acme",
        name: "widget",
        repoPath: "acme/widget",
        number: 7,
        itemType: "pull_request",
      }),
    );
    expect(window.location.pathname).toBe("/terminal/ws-2");
  });

  it("reports a successful force delete even after switching to another workspace", async () => {
    localStorage.setItem("middleman-workspace-active-tab:ws-1", "home");
    const forceDeleteRequest = deferred<Response>();
    const otherWorkspaceResponse = {
      ...workspaceResponse,
      id: "ws-2",
      item_number: 8,
      worktree_path: "/tmp/worktree-2",
    };
    const fetchMock = vi.fn().mockImplementation((input: Request | URL | string, init?: RequestInit) => {
      const url = input instanceof Request ? input.url : String(input);
      const method = init?.method ?? (input instanceof Request ? input.method : "GET");
      const { pathname, searchParams } = new URL(url, "http://localhost");
      if (method === "DELETE" && pathname.endsWith("/workspaces/ws-1")) {
        if (searchParams.get("force") === "true") {
          return forceDeleteRequest.promise;
        }
        return Promise.resolve(Response.json({ detail: "Workspace has uncommitted changes." }, { status: 409 }));
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
      return Promise.resolve(Response.json({}));
    });
    vi.stubGlobal("fetch", fetchMock);
    window.history.pushState({}, "", "/terminal/ws-1");

    const onWorkspaceDeleted = vi.fn();
    const view = render(WorkspaceTerminalView, {
      props: {
        workspaceId: "ws-1",
        onWorkspaceDeleted,
      },
    });

    await screen.findByRole("button", { name: "Delete" });
    await fireEvent.click(screen.getByRole("button", { name: "Delete" }));

    // The 409 opens the force-delete confirmation; confirming issues the
    // forced DELETE that stays in flight while the user switches away.
    const forceButton = await screen.findByRole("button", { name: "Force delete" });
    await fireEvent.click(forceButton);
    await waitFor(() => {
      expect(
        fetchMock.mock.calls.some(([input]) => {
          if (!(input instanceof Request)) return false;
          const { pathname, searchParams } = new URL(input.url);
          return (
            input.method === "DELETE" && pathname === "/api/v1/workspaces/ws-1" && searchParams.get("force") === "true"
          );
        }),
      ).toBe(true);
    });

    window.history.pushState({}, "", "/terminal/ws-2");
    await view.rerender({ workspaceId: "ws-2" });

    forceDeleteRequest.resolve(new Response(null, { status: 204 }));

    await waitFor(() =>
      expect(onWorkspaceDeleted).toHaveBeenCalledWith("ws-1", undefined, {
        provider: "github",
        platformHost: "github.com",
        owner: "acme",
        name: "widget",
        repoPath: "acme/widget",
        number: 7,
        itemType: "pull_request",
      }),
    );
    expect(window.location.pathname).toBe("/terminal/ws-2");
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
  it("launches an explicitly queued target without a confirmation modal", async () => {
    queueWorkspaceLaunch("ws-1", "codex");
    mocks.getWorkspaceRuntime
      .mockResolvedValueOnce(runtimeWithCodexTarget())
      .mockResolvedValue(runtimeWithCodexTarget(true, [runningSession]));
    mocks.launchWorkspaceSession.mockResolvedValue(runningSession);

    render(WorkspaceTerminalView, { props: { workspaceId: "ws-1" } });

    await waitFor(() => {
      expect(mocks.launchWorkspaceSession).toHaveBeenCalledWith("ws-1", "codex", {
        hostKey: undefined,
        region: "workflow",
      });
    });
    expect(
      screen.queryByRole("dialog", {
        name: /Launch default agent/,
      }),
    ).toBeNull();
  });

  it("reacts when intent is queued after an already-ready workspace renders", async () => {
    mocks.getWorkspaceRuntime.mockResolvedValue(runtimeWithCodexTarget());
    mocks.launchWorkspaceSession.mockResolvedValue(runningSession);
    render(WorkspaceTerminalView, { props: { workspaceId: "ws-1" } });
    await screen.findByRole("tab", { name: "Home" });

    queueWorkspaceLaunch("ws-1", "codex");

    await waitFor(() => {
      expect(mocks.launchWorkspaceSession).toHaveBeenCalledTimes(1);
    });
  });

  it("allows an explicit fork-workspace launch", async () => {
    queueWorkspaceLaunch("ws-1", "codex");
    mocks.getWorkspaceRuntime.mockResolvedValue(runtimeWithCodexTarget());
    mocks.launchWorkspaceSession.mockResolvedValue(runningSession);
    const forkWorkspaceResponse = { ...workspaceResponse, mr_head_repo_kind: "fork" };
    vi.stubGlobal(
      "fetch",
      vi.fn().mockImplementation((input: Request | URL | string) => {
        const url = input instanceof Request ? input.url : String(input);
        const { pathname } = new URL(url, "http://localhost");
        if (pathname.endsWith("/workspaces/ws-1")) {
          return Promise.resolve(Response.json(forkWorkspaceResponse));
        }
        if (pathname.endsWith("/api/v1/workspaces")) {
          return Promise.resolve(Response.json({ workspaces: [forkWorkspaceResponse] }));
        }
        return Promise.resolve(Response.json({}));
      }),
    );

    render(WorkspaceTerminalView, { props: { workspaceId: "ws-1" } });
    await waitFor(() => expect(mocks.launchWorkspaceSession).toHaveBeenCalled());
    expect(mocks.launchWorkspaceSession.mock.calls[0]?.[2]).toEqual({
      hostKey: undefined,
      region: "workflow",
    });
  });

  it("launches manually with only workspace display options", async () => {
    mocks.getWorkspaceRuntime.mockResolvedValue(runtimeWithCodexTarget());
    mocks.launchWorkspaceSession.mockResolvedValue({
      ...runningSession,
      key: "ws-1:codex",
      target_key: "codex",
      label: "Codex",
    });

    render(WorkspaceTerminalView, {
      props: {
        workspaceId: "ws-1",
      },
    });

    await screen.findByRole("tab", { name: "Home" });
    await fireEvent.click(screen.getByRole("button", { name: "Launch" }));
    const popover = document.querySelector(".launch-popover");
    if (!popover) throw new Error("expected launch popover to open");
    await fireEvent.click(within(popover as HTMLElement).getByRole("button", { name: "Codex" }));

    await waitFor(() => {
      expect(mocks.launchWorkspaceSession).toHaveBeenCalledWith("ws-1", "codex", {
        hostKey: undefined,
        region: "workflow",
      });
    });
  });

  it("consumes unavailable explicit intent and flashes its reason", async () => {
    queueWorkspaceLaunch("ws-1", "codex");
    mocks.getWorkspaceRuntime.mockResolvedValue(runtimeWithCodexTarget(false));
    render(WorkspaceTerminalView, { props: { workspaceId: "ws-1" } });
    await screen.findByRole("tab", { name: "Home" });
    expect(mocks.launchWorkspaceSession).not.toHaveBeenCalled();
    expect(mocks.showFlash).toHaveBeenCalledWith(expect.stringContaining("Codex is not configured"), {
      tone: "danger",
    });
    expect(consumeWorkspaceLaunch("ws-1")).toBeNull();
  });

  it("consumes missing explicit intent and flashes a generic reason", async () => {
    queueWorkspaceLaunch("ws-1", "missing");
    mocks.getWorkspaceRuntime.mockResolvedValue({ launch_targets: [], sessions: [] });
    render(WorkspaceTerminalView, { props: { workspaceId: "ws-1" } });
    await screen.findByRole("tab", { name: "Home" });
    expect(mocks.launchWorkspaceSession).not.toHaveBeenCalled();
    expect(mocks.showFlash).toHaveBeenCalledWith(expect.stringContaining("is not available in this workspace"), {
      tone: "danger",
    });
    expect(consumeWorkspaceLaunch("ws-1")).toBeNull();
  });

  it("consumes explicit intent without launching when a session exists", async () => {
    queueWorkspaceLaunch("ws-1", "codex");
    mocks.getWorkspaceRuntime.mockResolvedValue(runtimeWithCodexTarget(true, [runningSession]));
    render(WorkspaceTerminalView, { props: { workspaceId: "ws-1" } });
    await screen.findByRole("tab", { name: /Helper/ });
    expect(mocks.launchWorkspaceSession).not.toHaveBeenCalled();
    expect(mocks.showFlash).not.toHaveBeenCalled();
    expect(consumeWorkspaceLaunch("ws-1")).toBeNull();
  });

  it("publishes the session the pane is showing, so a keyboard command can promote it", async () => {
    localStorage.setItem("middleman-workspace-active-tab:ws-1", "session:ws-1:helper");
    localStorage.setItem(
      "middleman-workspace-terminal-layout:ws-1",
      persistedTwoSessionWorkflowLayout("ws-1:helper", "ws-1:reviewer"),
    );
    mocks.getWorkspaceRuntime.mockResolvedValue(runtimeWithTwoWorkflowSessions());
    claimForPrs();

    render(WorkspaceTerminalView, {
      props: {
        workspaceId: "ws-1",
        paneSurface: "prs" as const,
      },
    });

    // The active workflow tab holds this session, so it is the one on screen.
    // Only the view can decide that; a palette command sees stores alone.
    await waitFor(() =>
      expect(activeHostedSession("prs")?.paneKey).toBe(sessionPaneKey("ws-1", undefined, "ws-1:helper")),
    );

    await fireEvent.click(await screen.findByRole("tab", { name: /Reviewer/ }));

    // Republished as the user moves around: the other session fills the pane now,
    // so a promote command must act on that one instead.
    await waitFor(() =>
      expect(activeHostedSession("prs")?.paneKey).toBe(sessionPaneKey("ws-1", undefined, "ws-1:reviewer")),
    );
  });

  it("hands its controls to the pane instead of a toolbar while embedded", async () => {
    claimForPrs();

    const { unmount } = render(WorkspaceTerminalView, {
      props: {
        workspaceId: "ws-1",
        paneSurface: "prs" as const,
      },
    });

    // One bar, not three: the pane's tab strip renders these controls, so a
    // toolbar here would be a second copy of them above the terminal.
    await waitFor(() => expect(hostedWorkspaceControls()).not.toBeNull());
    expect(document.querySelector(".workspace-toolbar")).toBeNull();

    // Unregistered on the way out, or a pane could open the controls of a
    // workspace no longer hosted there.
    unmount();
    await waitFor(() => expect(hostedWorkspaceControls()).toBeNull());
  });

  it("renders a sole embedded session without workspace, workflow, or dock chrome", async () => {
    claimForPrs();

    render(WorkspaceTerminalView, {
      props: {
        workspaceId: "ws-1",
        paneSurface: "prs" as const,
      },
    });

    await waitFor(() => expect(activeHostedSession("prs")?.label).toBe("Helper"));
    expect(document.querySelector(".header-bar")).toBeNull();
    expect(screen.queryByRole("tablist", { name: "Workflow group tabs" })).toBeNull();
    expect(screen.queryByRole("region", { name: "Terminal panel" })).toBeNull();
  });

  it("keeps the workflow strip for two embedded sessions", async () => {
    localStorage.setItem(
      "middleman-workspace-terminal-layout:ws-1",
      persistedTwoSessionWorkflowLayout("ws-1:helper", "ws-1:reviewer"),
    );
    mocks.getWorkspaceRuntime.mockResolvedValue(runtimeWithTwoWorkflowSessions());
    claimForPrs();

    render(WorkspaceTerminalView, {
      props: {
        workspaceId: "ws-1",
        paneSurface: "prs" as const,
      },
    });

    expect(await screen.findAllByRole("tablist", { name: "Workflow group tabs" })).not.toHaveLength(0);
  });

  it("keeps the workspace header for a sole standalone session", async () => {
    render(WorkspaceTerminalView, {
      props: {
        workspaceId: "ws-1",
      },
    });

    await waitFor(() => expect(mocks.mockTerminalInstances.length).toBeGreaterThanOrEqual(1));
    expect(document.querySelector(".header-bar")).not.toBeNull();
  });

  it("offers the branch and delete action from embedded workspace controls", async () => {
    claimForPrs();

    render(WorkspaceTerminalView, {
      props: {
        workspaceId: "ws-1",
        paneSurface: "prs" as const,
      },
    });

    await waitFor(() => expect(hostedWorkspaceControls()).not.toBeNull());
    render(WorkspacePaneControls);
    await fireEvent.click(screen.getByRole("button", { name: "Workspace controls" }));

    const controls = within(screen.getByRole("dialog", { name: "Workspace controls" }));
    expect(controls.getByText("feature/session-exit").closest("code")).not.toBeNull();
    expect(controls.getByRole("button", { name: "Delete" })).toBeTruthy();
  });

  it("keeps each workspace's own preset apply pending while another one runs", async () => {
    // Two applies in flight at once. With a single owner slot, B's apply overwrote
    // A's and B finishing re-enabled A's control while A's sessions were still being
    // launched - which is an invitation to launch the whole preset twice. Driven on
    // the standalone tab, which is the only place presets are offered.
    serveAnyWorkspace();
    localStorage.setItem(
      "middleman-workspace-layout-presets",
      JSON.stringify([
        {
          id: "preset-1",
          name: "Pair",
          createdAt: "2026-04-29T00:00:00Z",
          updatedAt: "2026-04-29T00:00:00Z",
          sessions: [{ sourceKey: "s1", targetKey: "helper", region: "workflow", label: "Helper" }],
          layout: JSON.parse(persistedSplitWorkflowLayout("s1")),
        },
      ]),
    );
    const launchA = deferred<typeof runningSession>();
    const launchB = deferred<typeof runningSession>();
    mocks.launchWorkspaceSession.mockReturnValueOnce(launchA.promise).mockReturnValueOnce(launchB.promise);
    mocks.getWorkspaceRuntime.mockResolvedValue(runtimeWithLaunchTargetsOnly());

    const { rerender } = render(WorkspaceTerminalView, { props: { workspaceId: "ws-1" } });

    const presetTrigger = () => screen.getByRole("button", { name: "Workflow presets" });
    async function applyPreset(): Promise<void> {
      await fireEvent.click(presetTrigger());
      await fireEvent.click(screen.getAllByRole("button", { name: /Pair/ })[0]!);
    }

    await waitFor(() => expect(presetTrigger()).toBeTruthy());
    await applyPreset();
    await waitFor(() => expect(presetTrigger().hasAttribute("disabled")).toBe(true));

    await rerender({ workspaceId: "ws-2" });
    await waitFor(() => expect(presetTrigger().hasAttribute("disabled")).toBe(false));
    await applyPreset();
    await waitFor(() => expect(presetTrigger().hasAttribute("disabled")).toBe(true));

    // B finishes first.
    launchB.resolve(runningSession);
    await waitFor(() => expect(presetTrigger().hasAttribute("disabled")).toBe(false));

    await rerender({ workspaceId: "ws-1" });
    await waitFor(() => expect(presetTrigger().hasAttribute("disabled")).toBe(true));

    launchA.resolve(runningSession);
  });

  it("leaves workflow presets out of an embedded workspace's controls", async () => {
    claimForPrs();

    render(WorkspaceTerminalView, {
      props: {
        workspaceId: "ws-1",
        paneSurface: "prs" as const,
      },
    });

    await waitFor(() => expect(hostedWorkspaceControls()).not.toBeNull());
    render(WorkspacePaneControls);
    await fireEvent.click(screen.getByRole("button", { name: "Workspace controls" }));

    // Presets compose a whole multi-session workflow, which is the standalone tab's
    // job; a pane hosts one workspace beside the thing being reviewed.
    const controls = within(screen.getByRole("dialog", { name: "Workspace controls" }));
    expect(controls.queryByRole("button", { name: "Workflow presets" })).toBeNull();
    expect(controls.getByRole("button", { name: "Launch session" })).toBeTruthy();
  });

  it("keeps a terminal settings save busy across a workspace switch", async () => {
    // Terminal font size is an app setting written through one single-flight
    // controller, so a save is in flight for every workspace at once. Keying the busy
    // flag by workspace reported the next one's controls free while the controller was
    // still refusing input - an enabled button that does nothing.
    serveAnyWorkspace();
    const save = deferred<{ terminal: { font_size: number } }>();
    mocks.mockUpdateSettings.mockReturnValueOnce(save.promise);
    claimForPrs();

    const { rerender } = render(WorkspaceTerminalView, {
      props: {
        workspaceId: "ws-1",
        paneSurface: "prs" as const,
      },
    });
    await waitFor(() => expect(hostedWorkspaceControls()).not.toBeNull());
    render(WorkspacePaneControls);
    await fireEvent.click(screen.getByRole("button", { name: "Workspace controls" }));
    await fireEvent.click(screen.getByRole("button", { name: "Increase terminal font size" }));
    await waitFor(() => expect(workspaceControlsBusy()).toBe(true));

    await rerender({ workspaceId: "ws-2", paneSurface: "prs" as const });
    await waitFor(() => expect(hostedWorkspaceControls()?.workspaceKey).toContain("ws-2"));
    expect(workspaceControlsBusy()).toBe(true);

    save.resolve({ terminal: { font_size: 15 } });
    await waitFor(() => expect(workspaceControlsBusy()).toBe(false));
  });

  it("keeps its own toolbar on the standalone Workspaces tab", async () => {
    render(WorkspaceTerminalView, {
      props: {
        workspaceId: "ws-1",
      },
    });

    // That tab's panes have no tab strip to hold the controls, so the bar stays
    // and nothing is published for a detail pane to render.
    await waitFor(() => expect(document.querySelector(".workspace-toolbar")).not.toBeNull());
    expect(hostedWorkspaceControls()).toBeNull();
  });

  describe("launcher overlay", () => {
    it("drops the Home tab in a pane and opens the launcher when nothing is running", async () => {
      localStorage.setItem("middleman-workspace-active-tab:ws-1", "home");
      mocks.getWorkspaceRuntime.mockResolvedValue(runtimeWithLaunchTargetsOnly());
      claimForPrs();

      render(WorkspaceTerminalView, {
        props: {
          workspaceId: "ws-1",
          paneSurface: "prs" as const,
        },
      });

      // The pane's one slot goes to a terminal, not to a surface only used to start
      // one; with nothing to show, the launcher is what opens instead of an empty
      // strip.
      await waitFor(() => expect(screen.getByRole("dialog", { name: /Launch a session/ })).toBeTruthy());
      expect(screen.queryByRole("tab", { name: "Home" })).toBeNull();
    });

    it("leaves a docked terminal alone instead of covering it with the launcher", async () => {
      localStorage.setItem("middleman-workspace-active-tab:ws-1", "home");
      localStorage.setItem(
        "middleman-workspace-terminal-layout:ws-1",
        persistedSplitWorkflowLayout("ws-1_shell_a", "terminal"),
      );
      mocks.getWorkspaceRuntime.mockResolvedValue(runtimeWithTerminalSession());
      claimForPrs();

      render(WorkspaceTerminalView, {
        props: {
          workspaceId: "ws-1",
          paneSurface: "prs" as const,
        },
      });

      // The sole-session surface replaces the dock panel without changing the
      // launcher's rule: the terminal is on screen, so the overlay stays away.
      await waitFor(() => expect(document.querySelector(".sole-embedded-session")).not.toBeNull());
      expect(screen.queryByRole("dialog", { name: /Launch a session/ })).toBeNull();
    });

    it("keeps the Home tab on the standalone Workspaces tab", async () => {
      localStorage.setItem("middleman-workspace-active-tab:ws-1", "home");
      mocks.getWorkspaceRuntime.mockResolvedValue(runtimeWithLaunchTargetsOnly());

      render(WorkspaceTerminalView, {
        props: {
          workspaceId: "ws-1",
        },
      });

      // That tab has room for it, and its chrome is out of scope here.
      expect(await screen.findByRole("tab", { name: "Home" })).toBeTruthy();
      expect(screen.queryByRole("dialog", { name: /Launch a session/ })).toBeNull();
    });

    it("leaves a running workspace's sessions on screen instead of the launcher", async () => {
      localStorage.setItem("middleman-workspace-active-tab:ws-1", "home");
      localStorage.setItem("middleman-workspace-terminal-layout:ws-1", persistedSplitWorkflowLayout("ws-1:helper"));
      claimForPrs();

      render(WorkspaceTerminalView, {
        props: {
          workspaceId: "ws-1",
          paneSurface: "prs" as const,
        },
      });

      // A remembered Home tab names a tab that does not exist here, so the session
      // takes its place directly rather than the overlay covering a live terminal.
      await waitFor(() => expect(document.querySelector(".sole-embedded-session")).not.toBeNull());
      expect(screen.queryByRole("dialog", { name: /Launch a session/ })).toBeNull();
    });

    it("closes on a successful launch and stays open when one fails", async () => {
      mocks.getWorkspaceRuntime.mockResolvedValue(runtimeWithLaunchTargetsOnly());
      mocks.launchWorkspaceSession.mockRejectedValueOnce(new Error("helper not on PATH"));
      claimForPrs();

      render(WorkspaceTerminalView, {
        props: {
          workspaceId: "ws-1",
          paneSurface: "prs" as const,
        },
      });

      const dialog = await screen.findByRole("dialog", { name: /Launch a session/ });
      await fireEvent.click(within(dialog).getByRole("button", { name: /Helper/ }));

      // A failed launch leaves nothing to show, so closing the overlay would strand
      // the user on an empty pane with the error out of sight.
      await waitFor(() => expect(mocks.showFlash).toHaveBeenCalled());
      expect(screen.getByRole("dialog", { name: /Launch a session/ })).toBeTruthy();

      mocks.launchWorkspaceSession.mockResolvedValueOnce(runningSession);
      mocks.getWorkspaceRuntime.mockResolvedValue(runtimeWithStaleSession());
      await fireEvent.click(within(dialog).getByRole("button", { name: /Helper/ }));

      await waitFor(() => expect(screen.queryByRole("dialog", { name: /Launch a session/ })).toBeNull());
      await waitFor(() => expect(document.querySelector(".sole-embedded-session")).not.toBeNull());
    });

    it("takes back an auto-opened launcher once a session shows up", async () => {
      const eventListeners: Record<string, () => void> = {};
      vi.stubGlobal(
        "EventSource",
        class {
          addEventListener(type: string, callback: () => void): void {
            eventListeners[type] = callback;
          }
          close(): void {}
        },
      );
      mocks.getWorkspaceRuntime.mockResolvedValue(runtimeWithLaunchTargetsOnly());
      claimForPrs();

      render(WorkspaceTerminalView, {
        props: {
          workspaceId: "ws-1",
          paneSurface: "prs" as const,
        },
      });

      await screen.findByRole("dialog", { name: /Launch a session/ });

      // A reconnect (or a first runtime load that lands before its sessions do)
      // reports zero sessions for a moment. The launcher opened over that gap is
      // ours to take back, or it sits on top of the terminal it stood in for.
      mocks.getWorkspaceRuntime.mockResolvedValue(runtimeWithStaleSession());
      eventListeners["reconnect.stale"]?.();

      await waitFor(() => expect(document.querySelector(".sole-embedded-session")).not.toBeNull());
      await waitFor(() => expect(screen.queryByRole("dialog", { name: /Launch a session/ })).toBeNull());
    });

    it("keeps the launcher up when the reload after a launch fails", async () => {
      mocks.getWorkspaceRuntime.mockResolvedValue(runtimeWithLaunchTargetsOnly());
      claimForPrs();

      render(WorkspaceTerminalView, {
        props: {
          workspaceId: "ws-1",
          paneSurface: "prs" as const,
        },
      });

      const dialog = await screen.findByRole("dialog", { name: /Launch a session/ });
      mocks.launchWorkspaceSession.mockResolvedValueOnce(runningSession);
      mocks.getWorkspaceRuntime.mockRejectedValueOnce(new Error("runtime unavailable"));
      await fireEvent.click(within(dialog).getByRole("button", { name: /Helper/ }));

      // The session did start, but the pane can only render what the runtime
      // reports: closing here would leave an empty pane and no explanation.
      await waitFor(() => expect(mocks.showFlash).toHaveBeenCalled());
      expect(screen.getByRole("dialog", { name: /Launch a session/ })).toBeTruthy();
    });

    it("does not carry an open launcher into the next workspace", async () => {
      serveAnyWorkspace();
      mocks.getWorkspaceRuntime.mockResolvedValue(runtimeWithLaunchTargetsOnly());
      claimForPrs();

      const { rerender } = render(WorkspaceTerminalView, {
        props: {
          workspaceId: "ws-1",
          paneSurface: "prs" as const,
        },
      });

      await screen.findByRole("dialog", { name: /Launch a session/ });

      // One embedded view serves every selection on the surface, so the overlay
      // has to belong to the workspace it was opened for - otherwise it covers the
      // next one's live terminal, and the once-per-workspace guard would refuse to
      // open the launcher that workspace actually needs.
      mocks.getWorkspaceRuntime.mockResolvedValue(runtimeWithStaleSession());
      await rerender({ workspaceId: "ws-2", paneSurface: "prs" as const });

      await waitFor(() => expect(screen.queryByRole("dialog", { name: /Launch a session/ })).toBeNull());
    });

    it("auto-opens again for a workspace visited after another one", async () => {
      serveAnyWorkspace();
      mocks.getWorkspaceRuntime.mockResolvedValue(runtimeWithLaunchTargetsOnly());
      claimForPrs();

      const { rerender } = render(WorkspaceTerminalView, {
        props: {
          workspaceId: "ws-1",
          paneSurface: "prs" as const,
        },
      });

      // Dismissed for ws-1, so ws-1 must not get another one...
      await screen.findByRole("dialog", { name: /Launch a session/ });
      await fireEvent.keyDown(window, { key: "Escape" });
      await waitFor(() => expect(screen.queryByRole("dialog", { name: /Launch a session/ })).toBeNull());

      // ...but ws-2 has never been offered one.
      await rerender({ workspaceId: "ws-2", paneSurface: "prs" as const });
      await screen.findByRole("dialog", { name: /Launch a session/ });
      await fireEvent.keyDown(window, { key: "Escape" });
      await waitFor(() => expect(screen.queryByRole("dialog", { name: /Launch a session/ })).toBeNull());

      await rerender({ workspaceId: "ws-1", paneSurface: "prs" as const });

      // Back on ws-1, whose launcher the user already dismissed. A single-slot
      // memory forgets ws-1 the moment ws-2 is offered one, and reopening here
      // traps the user in an overlay they closed.
      // Settled: the runtime for ws-1 has been applied again and nothing reopened.
      await waitFor(() => expect(document.querySelector(".terminal-view")).not.toBeNull());
      expect(screen.queryByRole("dialog", { name: /Launch a session/ })).toBeNull();
    });

    it("hands the palette an opener only while a pane is hosting the workspace", async () => {
      mocks.getWorkspaceRuntime.mockResolvedValue(runtimeWithStaleSession());
      claimForPrs();

      const { unmount } = render(WorkspaceTerminalView, {
        props: {
          workspaceId: "ws-1",
          paneSurface: "prs" as const,
        },
      });

      // A palette command sees stores, not components, and the overlay state lives
      // in the view.
      await waitFor(() => expect(hostedWorkspaceLauncher("prs")).not.toBeNull());
      expect(hostedWorkspaceLauncher("issues")).toBeNull();

      unmount();
      await waitFor(() => expect(hostedWorkspaceLauncher("prs")).toBeNull());
    });
  });

  it("keeps its toolbar when the detail surface is flattened", async () => {
    claimForPrs();
    // What a narrow detail surface reports: one strip for every pane, per-leaf
    // chrome suppressed, so the pane has nowhere to hang a controls button and no
    // tab of its own to name the session.
    getPaneLayoutStore("prs").notePaneRender({
      flattened: true,
      editableTabs: [],
      onScreenTabs: ["workspace"],
    });
    render(WorkspaceTerminalView, {
      props: {
        workspaceId: "ws-1",
        paneSurface: "prs" as const,
      },
    });

    // Even with a single session, dropping the chrome here would strip the only
    // route to presets, zoom, terminal options, launch and delete.
    await waitFor(() => expect(document.querySelector(".workspace-toolbar")).not.toBeNull());
    expect(document.querySelector(".header-bar")).not.toBeNull();
  });

  it("publishes the dock's session while the dock is open", async () => {
    localStorage.setItem("middleman-workspace-active-tab:ws-1", "home");
    localStorage.setItem(
      "middleman-workspace-terminal-layout:ws-1",
      persistedSplitWorkflowLayout("ws-1_shell_a", "terminal"),
    );
    mocks.getWorkspaceRuntime.mockResolvedValue(runtimeWithTerminalSession());
    claimForPrs();

    render(WorkspaceTerminalView, {
      props: {
        workspaceId: "ws-1",
        paneSurface: "prs" as const,
      },
    });

    await waitFor(() =>
      expect(activeHostedSession("prs")?.paneKey).toBe(sessionPaneKey("ws-1", undefined, "ws-1_shell_a")),
    );
  });

  it("publishes no session while the dock holding it is collapsed", async () => {
    localStorage.setItem("middleman-workspace-active-tab:ws-1", "home");
    localStorage.setItem(
      "middleman-workspace-terminal-layout:ws-1",
      JSON.stringify({
        ...JSON.parse(persistedSplitWorkflowLayout("ws-1_shell_a", "terminal")),
        open: false,
      }),
    );
    mocks.getWorkspaceRuntime.mockResolvedValue(runtimeWithTerminalSession());
    claimForPrs();

    render(WorkspaceTerminalView, {
      props: {
        workspaceId: "ws-1",
        paneSurface: "prs" as const,
      },
    });

    // Published, so the surface can offer the pane...
    await waitFor(() => expect(getInlineWorkspaceController("prs").promotableSessions()).toHaveLength(1));
    // ...but not as the current one. The dock still names an active session, and a
    // collapsed dock renders none of it, so promoting it by keyboard would move a
    // terminal the user cannot see.
    expect(activeHostedSession("prs")).toBeNull();
  });

  describe("promoted sessions", () => {
    it("masks a promoted session out of the workflow strip and gives back its placement on demote", async () => {
      localStorage.setItem("middleman-workspace-active-tab:ws-1", "session:ws-1:reviewer");
      localStorage.setItem(
        "middleman-workspace-terminal-layout:ws-1",
        persistedTwoSessionWorkflowLayout("ws-1:reviewer", "ws-1:helper"),
      );
      mocks.getWorkspaceRuntime.mockResolvedValue(runtimeWithTwoWorkflowSessions());
      const paneKey = promoteSession("prs", "ws-1:helper");

      render(WorkspaceTerminalView, {
        props: {
          workspaceId: "ws-1",
          paneSurface: "prs" as const,
        },
      });

      const reviewerTab = await screen.findByRole("tab", { name: /Reviewer/ });
      // The detail pane is showing this session, so the container must not show it
      // too: two slots for one terminal race for it and one renders empty.
      expect(screen.queryByRole("tab", { name: /Helper/ })).toBeNull();

      getPaneLayoutStore("prs").demoteTab(paneKey);

      const helperTab = await screen.findByRole("tab", { name: /Helper/ });
      // Its own leaf, not merged into the other session's. Masking must not prune
      // the stored tree, or a demotion returns the session to the region and loses
      // the place the user put it in.
      expect(helperTab.closest('[role="tablist"]')).not.toBe(reviewerTab.closest('[role="tablist"]'));
    });

    it("keeps a promoted session's terminal live without a tab of its own", async () => {
      localStorage.setItem("middleman-workspace-active-tab:ws-1", "home");
      localStorage.setItem("middleman-workspace-terminal-layout:ws-1", persistedSplitWorkflowLayout("ws-1:helper"));
      promoteSession("prs", "ws-1:helper");

      render(WorkspaceTerminalView, {
        props: {
          workspaceId: "ws-1",
          paneSurface: "prs" as const,
        },
      });

      // Nothing in the container renders it, so only the pool can: a promoted
      // session that is not mounted leaves the detail pane's slot empty.
      await waitFor(() =>
        expect(sockets.some((socket) => socket.url.includes("/sessions/ws-1:helper/terminal"))).toBe(true),
      );
    });

    it("masks a promoted session out of the terminal dock too", async () => {
      localStorage.setItem("middleman-workspace-active-tab:ws-1", "home");
      localStorage.setItem(
        "middleman-workspace-terminal-layout:ws-1",
        persistedSplitWorkflowLayout("ws-1_shell_a", "terminal"),
      );
      mocks.getWorkspaceRuntime.mockResolvedValue(runtimeWithTerminalSession());
      const paneKey = promoteSession("prs", "ws-1_shell_a");

      render(WorkspaceTerminalView, {
        props: {
          workspaceId: "ws-1",
          paneSurface: "prs" as const,
        },
      });

      // A masked leaf must be pruned, not left rendering the dock's
      // session-unavailable placeholder for a session that is alive elsewhere.
      await waitFor(() => expect(screen.getByText("No terminals")).toBeTruthy());
      expect(screen.queryByText("Session unavailable")).toBeNull();

      getPaneLayoutStore("prs").demoteTab(paneKey);
      await waitFor(() => expect(screen.queryByText("No terminals")).toBeNull());
    });

    it("promotes a docked session from its own control", async () => {
      localStorage.setItem("middleman-workspace-active-tab:ws-1", "home");
      localStorage.setItem(
        "middleman-workspace-terminal-layout:ws-1",
        persistedSplitWorkflowLayout("ws-1_shell_a", "terminal"),
      );
      // Two, because the per-session header carrying this control only renders
      // once the dock holds more than one session.
      mocks.getWorkspaceRuntime.mockResolvedValue(runtimeWithTwoTerminalSessions());
      claimForPrs();
      noteWorkspacePaneRendered("prs");

      render(WorkspaceTerminalView, {
        props: {
          workspaceId: "ws-1",
          paneSurface: "prs" as const,
        },
      });

      const promote = await screen.findByRole("button", { name: "Move Shell to a pane" });
      await fireEvent.click(promote);

      const layout = getPaneLayoutStore("prs");
      const paneKey = sessionPaneKey("ws-1", undefined, "ws-1_shell_a");
      expect(layout.hasTab(paneKey)).toBe(true);
      // Its own leaf beside the workspace pane, the same placement the palette
      // command uses: a tab stacked behind the workspace pane would look like the
      // control did nothing.
      expect(layout.leafIDForTab(paneKey)).not.toBe(layout.leafIDForTab("workspace"));
      // And masked out of the dock it came from; where the dock puts what is left
      // is the masking tests' subject, not this one's.
      await waitFor(() => expect(screen.queryByRole("button", { name: "Move Shell to a pane" })).toBeNull());
    });

    it("keeps the only docked session available to pane commands without dock chrome", async () => {
      localStorage.setItem("middleman-workspace-active-tab:ws-1", "home");
      localStorage.setItem(
        "middleman-workspace-terminal-layout:ws-1",
        persistedSplitWorkflowLayout("ws-1_shell_a", "terminal"),
      );
      mocks.getWorkspaceRuntime.mockResolvedValue(runtimeWithTerminalSession());
      claimForPrs();
      noteWorkspacePaneRendered("prs");

      render(WorkspaceTerminalView, {
        props: {
          workspaceId: "ws-1",
          paneSurface: "prs" as const,
        },
      });

      const paneKey = sessionPaneKey("ws-1", undefined, "ws-1_shell_a");
      await waitFor(() =>
        expect(getInlineWorkspaceController("prs").promotableSessions()).toEqual([{ paneKey, label: "Shell" }]),
      );
      expect(screen.queryByRole("button", { name: "Move Shell to a pane" })).toBeNull();
      expect(screen.queryByRole("region", { name: "Terminal panel" })).toBeNull();
    });

    it("offers no promote control on the standalone Workspaces tab", async () => {
      localStorage.setItem("middleman-workspace-active-tab:ws-1", "home");
      localStorage.setItem(
        "middleman-workspace-terminal-layout:ws-1",
        persistedSplitWorkflowLayout("ws-1_shell_a", "terminal"),
      );
      mocks.getWorkspaceRuntime.mockResolvedValue(runtimeWithTwoTerminalSessions());

      render(WorkspaceTerminalView, {
        props: {
          workspaceId: "ws-1",
        },
      });

      // The session's other controls are there, so the header rendered; only the
      // promote one is absent. No detail surface is hosting this workspace, so
      // there is no tree to promote into and the control would lead nowhere.
      await screen.findByRole("button", { name: "Move Shell to workflow" });
      expect(screen.queryByRole("button", { name: "Move Shell to a pane" })).toBeNull();
    });

    it("masks nothing on the standalone Workspaces tab, which has no detail panes", async () => {
      localStorage.setItem("middleman-workspace-active-tab:ws-1", "home");
      localStorage.setItem("middleman-workspace-terminal-layout:ws-1", persistedSplitWorkflowLayout("ws-1:helper"));
      promoteSession("prs", "ws-1:helper");

      render(WorkspaceTerminalView, {
        props: {
          workspaceId: "ws-1",
        },
      });

      // Promotion is per surface. A session promoted in the PRs surface is still
      // at home here, and hiding it would leave it unreachable.
      expect(await screen.findByRole("tab", { name: /Helper/ })).toBeTruthy();
    });
  });
  it("demotes a promoted session dropped back on the workflow strip", async () => {
    localStorage.setItem("middleman-workspace-active-tab:ws-1", "session:ws-1:reviewer");
    localStorage.setItem(
      "middleman-workspace-terminal-layout:ws-1",
      persistedTwoSessionWorkflowLayout("ws-1:reviewer", "ws-1:helper"),
    );
    mocks.getWorkspaceRuntime.mockResolvedValue(runtimeWithTwoWorkflowSessions());
    const paneKey = promoteSession("prs", "ws-1:helper");

    render(WorkspaceTerminalView, {
      props: {
        workspaceId: "ws-1",
        paneSurface: "prs" as const,
      },
    });

    // A pane has no Home tab, so the workspace's other session is the strip the
    // promoted one can be dropped back onto.
    const reviewerTab = await screen.findByRole("tab", { name: /Reviewer/ });
    expect(screen.queryByRole("tab", { name: /Helper/ })).toBeNull();
    await waitFor(() =>
      expect(sockets.some((socket) => socket.url.includes("/sessions/ws-1:helper/terminal"))).toBe(true),
    );
    const socket = sockets.find((candidate) => candidate.url.includes("/sessions/ws-1:helper/terminal"))!;

    // The pane's own drag, arriving from the surface's tree: same scope, and a
    // key in the canonical form the workspace tab does not use.
    const dataTransfer = fakeDataTransfer();
    startTabbedPanelTabDrag({ dataTransfer } as unknown as DragEvent, { scope: "detail:prs", tabKey: paneKey });
    const reviewerStrip = reviewerTab.closest('[role="tablist"]')!;
    await fireEvent.dragOver(reviewerStrip, { dataTransfer, clientX: 5 });
    await fireEvent.drop(reviewerStrip, { dataTransfer, clientX: 5 });
    clearActiveTabbedPanelDrag();

    // Demoted, and placed where it was dropped rather than back in the leaf it
    // came from: the drop names a target, so honoring the stored placement here
    // would ignore the gesture.
    const helperTab = await screen.findByRole("tab", { name: /Helper/ });
    expect(getPaneLayoutStore("prs").hasTab(paneKey)).toBe(false);
    expect(helperTab.closest('[role="tablist"]')).toBe(reviewerStrip);
    // Same shell, still attached: the drop reparents the pooled terminal into the
    // workflow slot rather than tearing it down and reattaching.
    expect(sockets.filter((candidate) => candidate.url.includes("/sessions/ws-1:helper/terminal"))).toHaveLength(1);
    expect(socket.close).not.toHaveBeenCalled();
  });

  it("refuses a session pane dropped from another workspace", async () => {
    localStorage.setItem("middleman-workspace-active-tab:ws-1", "session:ws-1:reviewer");
    localStorage.setItem(
      "middleman-workspace-terminal-layout:ws-1",
      persistedTwoSessionWorkflowLayout("ws-1:reviewer", "ws-1:helper"),
    );
    mocks.getWorkspaceRuntime.mockResolvedValue(runtimeWithTwoWorkflowSessions());

    render(WorkspaceTerminalView, {
      props: {
        workspaceId: "ws-1",
        paneSurface: "prs" as const,
      },
    });

    const reviewerTab = await screen.findByRole("tab", { name: /Reviewer/ });
    const helperTab = await screen.findByRole("tab", { name: /Helper/ });
    const helperStrip = helperTab.closest('[role="tablist"]');
    // Same session key, another workspace. A session key is unique only within
    // its own workspace, so this must not move the local session of that name.
    const dataTransfer = fakeDataTransfer();
    startTabbedPanelTabDrag({ dataTransfer } as unknown as DragEvent, {
      scope: "detail:prs",
      tabKey: sessionPaneKey("ws-2", undefined, "ws-1:helper"),
    });
    const reviewerStrip = reviewerTab.closest('[role="tablist"]')!;
    await fireEvent.dragOver(reviewerStrip, { dataTransfer, clientX: 5 });
    await fireEvent.drop(reviewerStrip, { dataTransfer, clientX: 5 });
    clearActiveTabbedPanelDrag();

    expect(screen.getByRole("tab", { name: /Helper/ }).closest('[role="tablist"]')).toBe(helperStrip);
    expect(helperStrip).not.toBe(reviewerStrip);
  });
});
