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

import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/svelte";
import { afterEach, beforeEach, describe, expect, it, vi } from "vite-plus/test";

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

vi.mock("../../stores/flash.svelte.js", () => ({
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

    await waitFor(() => expect(mocks.showFlash).toHaveBeenCalledWith("Refresh failed (503)"));
  });
});
