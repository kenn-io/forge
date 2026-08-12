import { cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/svelte";
import { afterEach, beforeEach, describe, expect, it, vi } from "vite-plus/test";
import { isNewWorkspaceDialogOpen, resetNewWorkspaceDialogState } from "../../stores/new-workspace.svelte.js";
import * as workspaceHost from "../../stores/workspace-host.svelte.js";
import MobileWorkspaceList from "./MobileWorkspaceListTestHarness.svelte";

const mockGet = vi.fn();
const mockPost = vi.fn();
const mockDelete = vi.fn();

vi.mock("../../api/runtime.js", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../../api/runtime.js")>();
  const client = {
    DELETE: (...args: unknown[]) => mockDelete(...args),
    GET: (...args: unknown[]) => mockGet(...args),
    POST: (...args: unknown[]) => mockPost(...args),
  };
  return { ...actual, client, createRuntimeClient: () => client };
});

class MockEventSource {
  addEventListener = vi.fn();
  removeEventListener = vi.fn();
  close = vi.fn();
}

const fixture = {
  id: "ws-1",
  created_at: "2026-08-11T12:00:00Z",
  git_head_ref: "feature/mobile-workspaces",
  item_number: 42,
  item_type: "pull_request",
  platform_host: "github.com",
  repo_name: "widgets",
  repo_owner: "acme",
  status: "ready",
  tmux_activity_source: "unknown",
  tmux_last_output_at: null,
  tmux_working: false,
  worktree_path: "/tmp/ws-1",
  mr_title: "Build mobile workspaces",
  mr_state: "open",
  mr_additions: 120,
  mr_deletions: 12,
  repo: {
    provider: "github",
    platform_host: "github.com",
    owner: "acme",
    name: "widgets",
    repo_path: "acme/widgets",
  },
};

describe("MobileWorkspaceList", () => {
  beforeEach(() => {
    mockGet.mockReset();
    mockPost.mockReset();
    mockDelete.mockReset();
    localStorage.clear();
    resetNewWorkspaceDialogState();
    vi.stubGlobal("EventSource", MockEventSource);
    mockGet.mockImplementation((path: string) => {
      if (path === "/snapshot") return Promise.resolve({ data: { hosts: [] } });
      if (path === "/workspaces") return Promise.resolve({ data: { workspaces: [fixture] } });
      return Promise.resolve({ data: {} });
    });
  });

  afterEach(() => {
    cleanup();
    vi.unstubAllGlobals();
  });

  it("filters rows and opens the selected workspace", async () => {
    const onOpen = vi.fn();
    render(MobileWorkspaceList, { props: { onOpen, onOpenItem: vi.fn() } });
    await screen.findByText("Build mobile workspaces");

    await fireEvent.input(screen.getByRole("searchbox", { name: "Filter workspaces" }), {
      target: { value: "unrelated" },
    });
    expect(screen.queryByText("Build mobile workspaces")).toBeNull();

    await fireEvent.input(screen.getByRole("searchbox", { name: "Filter workspaces" }), {
      target: { value: "mobile" },
    });
    await fireEvent.click(screen.getByRole("button", { name: "Open workspace Build mobile workspaces" }));
    expect(onOpen).toHaveBeenCalledWith("ws-1", undefined);
  });

  it.each([
    ["working", "Working", "working"],
    ["approval", "Approval", "waiting for approval"],
    ["input", "Input", "waiting for input"],
    ["done", "Done", "done"],
  ] as const)("shows the hook-reported %s agent state", async (agentState, label, announcement) => {
    mockGet.mockImplementation((path: string) => {
      if (path === "/snapshot") return Promise.resolve({ data: { hosts: [] } });
      if (path === "/workspaces") {
        return Promise.resolve({ data: { workspaces: [{ ...fixture, agent_state: agentState }] } });
      }
      return Promise.resolve({ data: {} });
    });

    render(MobileWorkspaceList, { props: { onOpen: vi.fn(), onOpenItem: vi.fn() } });

    expect(await screen.findByText(label)).toBeTruthy();
    expect(
      screen.getByRole("button", { name: `Open workspace Build mobile workspaces, agent ${announcement}` }),
    ).toBeTruthy();
  });

  it("does not expose a dead linked-item action for Kata workspaces", async () => {
    mockGet.mockImplementation((path: string) => {
      if (path === "/snapshot") return Promise.resolve({ data: { hosts: [] } });
      if (path === "/workspaces") {
        return Promise.resolve({
          data: {
            workspaces: [
              {
                ...fixture,
                item_type: "kata_task",
                item_number: 0,
                kata: {
                  daemon_id: "desktop",
                  project_uid: "project-7",
                  project_name: "Example project",
                  issue_uid: "issue-7",
                  short_id: "task-7",
                  title: "Build mobile workspaces",
                },
              },
            ],
          },
        });
      }
      return Promise.resolve({ data: {} });
    });

    render(MobileWorkspaceList, { props: { onOpen: vi.fn(), onOpenItem: vi.fn() } });

    await screen.findByText("Build mobile workspaces");
    expect(screen.queryByRole("button", { name: /Open linked item/ })).toBeNull();
  });

  it("exposes the View sheet and persists display choices", async () => {
    render(MobileWorkspaceList, { props: { onOpen: vi.fn(), onOpenItem: vi.fn() } });
    await screen.findByText("Build mobile workspaces");

    await fireEvent.click(screen.getByRole("button", { name: "View workspace options" }));
    expect(screen.getByRole("dialog", { name: "View workspace options" })).toBeTruthy();
    expect(screen.getByRole("radio", { name: /^Terminal activity/ })).toBeTruthy();

    await fireEvent.click(screen.getByRole("switch", { name: "Show organization names" }));
    await waitFor(() => {
      expect(localStorage.getItem("kenn-forge:workspaceListDisplayOptions")).toContain('"showOrgNames":false');
    });
  });

  it("opens New Workspace from the list header", async () => {
    render(MobileWorkspaceList, { props: { onOpen: vi.fn(), onOpenItem: vi.fn() } });
    await screen.findByText("Build mobile workspaces");
    await fireEvent.click(screen.getByRole("button", { name: "New workspace" }));
    expect(isNewWorkspaceDialogOpen()).toBe(true);
  });

  it("invalidates shared terminal and route state after deletion", async () => {
    const notifyWorkspaceDeleted = vi.spyOn(workspaceHost, "notifyWorkspaceDeleted");
    mockDelete.mockResolvedValue({
      data: undefined,
      error: undefined,
      response: { ok: true, status: 204 },
    });
    render(MobileWorkspaceList, { props: { onOpen: vi.fn(), onOpenItem: vi.fn() } });
    await screen.findByText("Build mobile workspaces");

    await fireEvent.click(screen.getByRole("button", { name: "Workspace actions for Build mobile workspaces" }));
    const actions = await screen.findByRole("dialog", { name: "Workspace actions" });
    await fireEvent.click(within(actions).getByRole("button", { name: "Delete workspace…" }));
    const confirmation = await screen.findByRole("dialog", { name: "Delete workspace?" });
    await fireEvent.click(within(confirmation).getByRole("button", { name: "Delete workspace" }));

    await waitFor(() => {
      expect(notifyWorkspaceDeleted).toHaveBeenCalledWith("ws-1", undefined, {
        provider: "github",
        platformHost: "github.com",
        owner: "acme",
        name: "widgets",
        repoPath: "acme/widgets",
        number: 42,
        itemType: "pull_request",
      });
    });
    notifyWorkspaceDeleted.mockRestore();
  });
});
