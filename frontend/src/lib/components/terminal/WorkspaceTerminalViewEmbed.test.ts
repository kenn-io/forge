// Pins the embed-only props on WorkspaceTerminalView so a refactor that
// loses the conditional rendering around the workspace list column or the
// right detail sidebar fails loudly rather than silently breaking
// embedders that mount the surface via /workspaces/embed/terminal.
//
// Lives in its own file because the broader WorkspaceTerminalView test
// suite stubs globalThis.fetch *after* the runtime client module has
// captured it; that's a pre-existing test-infrastructure issue
// (introduced in #182) which affects neither this branch nor the embed
// props themselves. Mocking the api/runtime module here avoids the
// captured-fetch problem entirely.

import { cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/svelte";
import { afterEach, beforeEach, describe, expect, it, vi } from "vite-plus/test";
import { pushModalFrame, resetModalStack } from "@middleman/ui/stores/keyboard/modal-stack";

const mocks = vi.hoisted(() => ({
  runtimeClient: {
    GET: vi.fn(),
    POST: vi.fn(),
    DELETE: vi.fn(),
  },
  showFlash: vi.fn(),
}));

vi.mock("../../api/runtime.js", () => ({
  client: mocks.runtimeClient,
  apiErrorMessage: (_err: unknown, fallback: string) => fallback,
}));

vi.mock("@middleman/ui/stores/flash", () => ({
  showFlash: mocks.showFlash,
}));

vi.mock("../../api/workspace-runtime.js", () => ({
  getWorkspaceRuntime: vi.fn().mockResolvedValue({
    launch_targets: [],
    sessions: [],
  }),
  launchWorkspaceSession: vi.fn(),
  renameWorkspaceSession: vi.fn(),
  stopWorkspaceSession: vi.fn(),
  workspaceSessionWebSocketPath: () => "",
  workspaceTmuxWebSocketPath: () => "",
}));

// Stub xterm so the terminal panes don't try to render in jsdom.
vi.mock("@xterm/xterm", () => ({
  Terminal: vi.fn().mockImplementation(function () {
    return {
      cols: 80,
      rows: 24,
      open: vi.fn(),
      loadAddon: vi.fn(),
      onData: vi.fn(),
      onBinary: vi.fn(),
      dispose: vi.fn(),
      write: vi.fn(),
      refresh: vi.fn(),
      clearTextureAtlas: vi.fn(),
      options: {},
    };
  }),
}));

vi.mock("@xterm/addon-fit", () => ({
  FitAddon: vi.fn().mockImplementation(function () {
    return { fit: vi.fn() };
  }),
}));

vi.mock("@xterm/addon-webgl", () => ({
  WebglAddon: vi.fn().mockImplementation(function () {
    return {};
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
          renderer: "xterm",
        }),
        setTerminalSettings: vi.fn(),
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
        getTerminalRenderer: () => "xterm",
      },
    }),
  };
});

import WorkspaceTerminalView from "./WorkspaceTerminalView.svelte";

const readyWorkspaceData = {
  id: "ws-1",
  platform_host: "github.com",
  repo_owner: "acme",
  repo_name: "widget",
  item_type: "pull_request",
  item_number: 7,
  git_head_ref: "feature/embed-props",
  worktree_path: "/tmp/worktree",
  tmux_session: "middleman-ws-1",
  status: "ready",
  created_at: "2026-04-29T00:00:00Z",
};

const readyIssueWorkspaceData = {
  ...readyWorkspaceData,
  item_type: "issue",
  item_number: 9,
  associated_pr_number: null,
};

describe("WorkspaceTerminalView embed props", () => {
  beforeEach(() => {
    mocks.runtimeClient.GET.mockReset();
    mocks.runtimeClient.POST.mockReset();
    mocks.runtimeClient.DELETE.mockReset();
    mocks.showFlash.mockReset();
    mocks.runtimeClient.GET.mockResolvedValue({
      data: readyWorkspaceData,
      error: undefined,
      response: { status: 200 },
    });

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
    vi.stubGlobal("requestAnimationFrame", (callback: FrameRequestCallback) => {
      callback(0);
      return 1;
    });
  });

  afterEach(() => {
    cleanup();
    vi.unstubAllGlobals();
  });

  it("hides the workspace list column when hideWorkspaceList is true", async () => {
    render(WorkspaceTerminalView, {
      props: {
        workspaceId: "ws-1",
        hideWorkspaceList: true,
      },
    });

    // Wait for the header branch element that only renders once the
    // workspace payload resolves; this confirms the component reached
    // steady state rather than failing the load early.
    await waitFor(() => expect(screen.getAllByText("feature/embed-props").length).toBeGreaterThan(0));

    // The workspace-list column header reads "Workspaces"; with
    // hideWorkspaceList the entire column is skipped so the heading
    // must not be in the DOM.
    expect(screen.queryByText("Workspaces")).toBeNull();
  });

  it("renders the workspace list column by default", async () => {
    render(WorkspaceTerminalView, {
      props: { workspaceId: "ws-1" },
    });

    await waitFor(() => expect(screen.getAllByText("feature/embed-props").length).toBeGreaterThan(0));

    expect(screen.queryByText("Workspaces")).not.toBeNull();
  });

  it("hides the PR/Reviews segmented control when hideRightSidebar is true", async () => {
    render(WorkspaceTerminalView, {
      props: {
        workspaceId: "ws-1",
        hideWorkspaceList: true,
        hideRightSidebar: true,
      },
    });

    await waitFor(() => expect(screen.getAllByText("feature/embed-props").length).toBeGreaterThan(0));

    expect(screen.queryByRole("button", { name: "PR" })).toBeNull();
    expect(screen.queryByRole("button", { name: "Reviews" })).toBeNull();
  });

  it("renders the PR/Reviews segmented control by default", async () => {
    render(WorkspaceTerminalView, {
      props: { workspaceId: "ws-1" },
    });

    await waitFor(() => expect(screen.getAllByText("feature/embed-props").length).toBeGreaterThan(0));

    expect(screen.getByRole("button", { name: "PR" })).toBeTruthy();
    expect(screen.getByRole("button", { name: "Reviews" })).toBeTruthy();
  });

  it("refreshes workspace details and reveals a newly associated PR", async () => {
    mocks.runtimeClient.GET.mockResolvedValue({
      data: readyIssueWorkspaceData,
      error: undefined,
      response: { status: 200 },
    });
    mocks.runtimeClient.POST.mockResolvedValue({
      data: {
        ...readyIssueWorkspaceData,
        associated_pr_number: 42,
      },
      error: undefined,
      response: { status: 200 },
    });

    render(WorkspaceTerminalView, {
      props: {
        workspaceId: "ws-1",
        hideWorkspaceList: true,
      },
    });

    await waitFor(() => expect(screen.getAllByText("feature/embed-props").length).toBeGreaterThan(0));
    expect(screen.queryByRole("button", { name: "PR" })).toBeNull();

    await fireEvent.click(screen.getByRole("button", { name: "Refresh workspace details" }));

    await waitFor(() => expect(screen.getByRole("button", { name: "PR" })).toBeTruthy());
    expect(mocks.runtimeClient.POST).toHaveBeenCalledWith("/workspaces/{id}/refresh", {
      params: { path: { id: "ws-1" } },
    });
  });

  it("shows a flash when workspace detail refresh fails", async () => {
    mocks.runtimeClient.GET.mockResolvedValue({
      data: readyIssueWorkspaceData,
      error: undefined,
      response: { status: 200 },
    });
    mocks.runtimeClient.POST.mockResolvedValue({
      data: undefined,
      error: { detail: "temporarily unavailable" },
      response: { status: 503 },
    });

    render(WorkspaceTerminalView, {
      props: {
        workspaceId: "ws-1",
        hideWorkspaceList: true,
      },
    });

    await waitFor(() => expect(screen.getAllByText("feature/embed-props").length).toBeGreaterThan(0));

    await fireEvent.click(screen.getByRole("button", { name: "Refresh workspace details" }));

    await waitFor(() => {
      expect(mocks.showFlash).toHaveBeenCalledWith("Refresh failed (503)", {
        tone: "danger",
      });
    });
  });

  it("reports a 404 workspace load as a deletion so cached refs clear", async () => {
    // A 404 is authoritative absence: the workspace was deleted by
    // another client. Without reporting it, created-records and
    // overrides keep advertising the dead ID indefinitely.
    mocks.runtimeClient.GET.mockResolvedValue({
      data: undefined,
      error: { detail: "workspace not found" },
      response: { status: 404 },
    });
    const onWorkspaceDeleted = vi.fn();

    render(WorkspaceTerminalView, {
      props: { workspaceId: "ws-1", hideWorkspaceList: true, onWorkspaceDeleted },
    });

    await waitFor(() => {
      // No cached envelope yet, so no identity snapshot to report.
      expect(onWorkspaceDeleted).toHaveBeenCalledWith("ws-1", undefined, undefined);
    });
  });

  it("a 404 after a successful load clears the cached workspace and reports the identity", async () => {
    // The workspace was deleted by another client mid-session. Liveness
    // rendering keys off the cached envelope, so without clearing it the
    // route would keep showing the deleted workspace; and the deletion
    // callback needs the identity snapshot to tombstone controller-less
    // cached detail.
    const workspaceWithRepo = {
      ...readyWorkspaceData,
      repo: {
        provider: "github",
        platform_host: "github.com",
        owner: "acme",
        name: "widget",
        repo_path: "acme/widget",
      },
    };
    let gone = false;
    mocks.runtimeClient.GET.mockImplementation(async (path: string) => {
      if (path === "/workspaces/{id}" && gone) {
        return { data: undefined, error: { detail: "workspace not found" }, response: { status: 404 } };
      }
      return { data: workspaceWithRepo, error: undefined, response: { status: 200 } };
    });
    const listeners = new Map<string, (e: { data: string }) => void>();
    vi.stubGlobal(
      "EventSource",
      class {
        addEventListener(type: string, cb: (e: { data: string }) => void): void {
          listeners.set(type, cb);
        }
        close(): void {}
      },
    );
    const onWorkspaceDeleted = vi.fn();

    render(WorkspaceTerminalView, {
      props: { workspaceId: "ws-1", hideWorkspaceList: true, onWorkspaceDeleted },
    });
    await waitFor(() => expect(screen.getAllByText("feature/embed-props").length).toBeGreaterThan(0));

    gone = true;
    listeners.get("workspace_status")?.({ data: JSON.stringify({ id: "ws-1" }) });

    await waitFor(() => {
      expect(onWorkspaceDeleted).toHaveBeenCalledWith(
        "ws-1",
        undefined,
        expect.objectContaining({ provider: "github", owner: "acme", name: "widget", number: 7 }),
      );
    });
    // The dead cached envelope must not keep rendering as live.
    await waitFor(() => {
      expect(screen.queryAllByText("feature/embed-props")).toHaveLength(0);
      expect(screen.getAllByText("Failed to load workspace (404)").length).toBeGreaterThan(0);
    });
  });

  it("a transient workspace load failure is not treated as a deletion", async () => {
    mocks.runtimeClient.GET.mockResolvedValue({
      data: undefined,
      error: { detail: "upstream boom" },
      response: { status: 500 },
    });
    const onWorkspaceDeleted = vi.fn();

    render(WorkspaceTerminalView, {
      props: { workspaceId: "ws-1", hideWorkspaceList: true, onWorkspaceDeleted },
    });

    await waitFor(() => {
      expect(screen.getAllByText("Failed to load workspace (500)").length).toBeGreaterThan(0);
    });
    expect(onWorkspaceDeleted).not.toHaveBeenCalled();
  });

  describe("inlineDock toolbar controls", () => {
    afterEach(() => {
      resetModalStack();
    });

    it("renders no inline dock buttons without an inlineDock prop", async () => {
      render(WorkspaceTerminalView, {
        props: { workspaceId: "ws-1", hideWorkspaceList: true },
      });

      await waitFor(() => expect(screen.getAllByText("feature/embed-props").length).toBeGreaterThan(0));

      expect(screen.queryByRole("button", { name: "Expand Terminal" })).toBeNull();
      expect(screen.queryByRole("button", { name: "Collapse Terminal" })).toBeNull();
    });

    it("renders the toggle and collapse buttons in the same toolbar container as Delete", async () => {
      const inlineDock = { getMode: () => "split" as const, setMode: vi.fn() };
      render(WorkspaceTerminalView, {
        props: { workspaceId: "ws-1", hideWorkspaceList: true, inlineDock },
      });

      await waitFor(() => expect(screen.getAllByText("feature/embed-props").length).toBeGreaterThan(0));

      const deleteButton = screen.getByRole("button", { name: "Delete" });
      const container = deleteButton.closest(".header-end");
      expect(container).toBeTruthy();
      const scoped = within(container as HTMLElement);
      expect(scoped.getByRole("button", { name: "Expand Terminal" })).toBeTruthy();
      expect(scoped.getByRole("button", { name: "Collapse Terminal" })).toBeTruthy();
    });

    it("flips the toggle label with mode and drives setMode through it", async () => {
      const setMode = vi.fn();
      const { rerender } = render(WorkspaceTerminalView, {
        props: {
          workspaceId: "ws-1",
          hideWorkspaceList: true,
          inlineDock: { getMode: () => "split" as const, setMode },
        },
      });

      await waitFor(() => expect(screen.getAllByText("feature/embed-props").length).toBeGreaterThan(0));

      await fireEvent.click(screen.getByRole("button", { name: "Expand Terminal" }));
      expect(setMode).toHaveBeenCalledWith("expanded");

      await rerender({
        workspaceId: "ws-1",
        hideWorkspaceList: true,
        inlineDock: { getMode: () => "expanded" as const, setMode },
      });

      expect(screen.queryByRole("button", { name: "Expand Terminal" })).toBeNull();
      await fireEvent.click(screen.getByRole("button", { name: "Show Details" }));
      expect(setMode).toHaveBeenCalledWith("split");
    });

    it("collapses via the inline dock collapse button", async () => {
      const setMode = vi.fn();
      render(WorkspaceTerminalView, {
        props: {
          workspaceId: "ws-1",
          hideWorkspaceList: true,
          inlineDock: { getMode: () => "split" as const, setMode },
        },
      });

      await waitFor(() => expect(screen.getAllByText("feature/embed-props").length).toBeGreaterThan(0));

      await fireEvent.click(screen.getByRole("button", { name: "Collapse Terminal" }));
      expect(setMode).toHaveBeenCalledWith("collapsed");
    });

    it("keeps a collapse control reachable while the workspace is still creating", async () => {
      // The toolbar that carries the dock controls only renders once the
      // workspace is ready; without a state-level control a slow setup
      // would leave the inline dock impossible to close.
      mocks.runtimeClient.GET.mockResolvedValue({
        data: { ...readyWorkspaceData, status: "creating" },
        error: undefined,
        response: { status: 200 },
      });
      const setMode = vi.fn();
      render(WorkspaceTerminalView, {
        props: {
          workspaceId: "ws-1",
          hideWorkspaceList: true,
          inlineDock: { getMode: () => "split" as const, setMode },
        },
      });

      await waitFor(() => expect(screen.getByText("Setting up workspace...")).toBeTruthy());

      await fireEvent.click(screen.getByRole("button", { name: "Collapse Terminal" }));
      expect(setMode).toHaveBeenCalledWith("collapsed");
    });

    it("keeps a collapse control reachable after workspace setup fails", async () => {
      mocks.runtimeClient.GET.mockResolvedValue({
        data: { ...readyWorkspaceData, status: "error", error_message: "clone failed" },
        error: undefined,
        response: { status: 200 },
      });
      const setMode = vi.fn();
      render(WorkspaceTerminalView, {
        props: {
          workspaceId: "ws-1",
          hideWorkspaceList: true,
          inlineDock: { getMode: () => "split" as const, setMode },
        },
      });

      await waitFor(() => expect(screen.getByText("clone failed")).toBeTruthy());

      await fireEvent.click(screen.getByRole("button", { name: "Collapse Terminal" }));
      expect(setMode).toHaveBeenCalledWith("collapsed");
    });

    it("keeps a collapse control reachable when the workspace fetch fails", async () => {
      mocks.runtimeClient.GET.mockResolvedValue({
        data: undefined,
        error: { detail: "boom" },
        response: { status: 500 },
      });
      const setMode = vi.fn();
      render(WorkspaceTerminalView, {
        props: {
          workspaceId: "ws-1",
          hideWorkspaceList: true,
          inlineDock: { getMode: () => "split" as const, setMode },
        },
      });

      await waitFor(() => expect(screen.getByText("Failed to load workspace (500)")).toBeTruthy());

      await fireEvent.click(screen.getByRole("button", { name: "Collapse Terminal" }));
      expect(setMode).toHaveBeenCalledWith("collapsed");
    });

    it("a failed in-place workspace switch shows the error state, not the stale toolbar", async () => {
      // Switching the inline dock from A to B keeps A cached while B
      // loads. When B's fetch fails, the error state (with its retry and
      // collapse controls) must render instead of A's ready toolbar,
      // which would be a stale header whose action guards leave the dock
      // uncollapsible.
      mocks.runtimeClient.GET.mockImplementation(async (_path: string, opts: { params: { path: { id: string } } }) => {
        if (opts.params.path.id === "ws-2") {
          return { data: undefined, error: { detail: "boom" }, response: { status: 500 } };
        }
        return { data: readyWorkspaceData, error: undefined, response: { status: 200 } };
      });
      const setMode = vi.fn();
      const inlineDock = { getMode: () => "split" as const, setMode };
      const { rerender } = render(WorkspaceTerminalView, {
        props: { workspaceId: "ws-1", hideWorkspaceList: true, inlineDock },
      });

      await waitFor(() => expect(screen.getAllByText("feature/embed-props").length).toBeGreaterThan(0));

      await rerender({ workspaceId: "ws-2", hideWorkspaceList: true, inlineDock });

      await waitFor(() => expect(screen.getByText("Failed to load workspace (500)")).toBeTruthy());
      expect(screen.queryByText("feature/embed-props")).toBeNull();

      const collapse = screen.getByRole("button", { name: "Collapse Terminal" });
      expect(collapse.hasAttribute("disabled")).toBe(false);
      await fireEvent.click(collapse);
      expect(setMode).toHaveBeenCalledWith("collapsed");
    });

    it("a slow in-place workspace switch shows the loading state, not the stale toolbar", async () => {
      mocks.runtimeClient.GET.mockImplementation((_path: string, opts: { params: { path: { id: string } } }) => {
        if (opts.params.path.id === "ws-2") {
          return new Promise(() => {});
        }
        return Promise.resolve({ data: readyWorkspaceData, error: undefined, response: { status: 200 } });
      });
      const inlineDock = { getMode: () => "split" as const, setMode: vi.fn() };
      const { rerender } = render(WorkspaceTerminalView, {
        props: { workspaceId: "ws-1", hideWorkspaceList: true, inlineDock },
      });

      await waitFor(() => expect(screen.getAllByText("feature/embed-props").length).toBeGreaterThan(0));

      await rerender({ workspaceId: "ws-2", hideWorkspaceList: true, inlineDock });

      await waitFor(() => expect(screen.getByText("Setting up workspace...")).toBeTruthy());
      expect(screen.queryByText("feature/embed-props")).toBeNull();
      expect(screen.getByRole("button", { name: "Collapse Terminal" })).toBeTruthy();
    });

    it("shows no collapse control in setup states without an inlineDock prop", async () => {
      mocks.runtimeClient.GET.mockResolvedValue({
        data: { ...readyWorkspaceData, status: "creating" },
        error: undefined,
        response: { status: 200 },
      });
      render(WorkspaceTerminalView, {
        props: { workspaceId: "ws-1", hideWorkspaceList: true },
      });

      await waitFor(() => expect(screen.getByText("Setting up workspace...")).toBeTruthy());

      expect(screen.queryByRole("button", { name: "Collapse Terminal" })).toBeNull();
    });

    it("disables the expand direction while a modal frame is open", async () => {
      const setMode = vi.fn();
      render(WorkspaceTerminalView, {
        props: {
          workspaceId: "ws-1",
          hideWorkspaceList: true,
          inlineDock: { getMode: () => "split" as const, setMode },
        },
      });

      await waitFor(() => expect(screen.getAllByText("feature/embed-props").length).toBeGreaterThan(0));

      const expandButton = screen.getByRole("button", { name: "Expand Terminal" });
      expect(expandButton.hasAttribute("disabled")).toBe(false);

      const pop = pushModalFrame("test-modal", []);
      await waitFor(() =>
        expect(screen.getByRole("button", { name: "Expand Terminal" }).hasAttribute("disabled")).toBe(true),
      );

      pop();
      await waitFor(() =>
        expect(screen.getByRole("button", { name: "Expand Terminal" }).hasAttribute("disabled")).toBe(false),
      );
    });
  });
});
