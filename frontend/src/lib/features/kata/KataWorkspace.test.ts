import { cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/svelte";
import { afterEach, beforeEach, describe, expect, it, vi } from "vite-plus/test";

import { KataTaskAPIError } from "../../api/kata/taskClient.js";
import type { KataTaskDetail, KataTaskSearchFilters, KataTaskSearchResponse } from "../../api/kata/taskTypes.js";
import {
  getActiveKataDaemon,
  getDefaultKataDaemon,
  getKataDaemonRoster,
} from "../../stores/active-kata-daemon.svelte.js";
import KataWorkspace from "./KataWorkspace.svelte";
import {
  createDaemonWorkspaceAPI,
  createWorkspaceAPI,
  deferred,
  detail,
  fetchedAt,
  initialIssues,
  issue,
  messageLink,
  projects,
  resetKataWorkspaceTestState,
} from "./KataWorkspaceTestSupport.js";

const { mockCreateKataWorkspaceForTask, mockNavigate, mockResolveKataWorkspaceTarget } = vi.hoisted(() => ({
  mockCreateKataWorkspaceForTask: vi.fn(),
  mockNavigate: vi.fn(),
  mockResolveKataWorkspaceTarget: vi.fn(),
}));

vi.mock("../../api/kata/workspaces.js", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../../api/kata/workspaces.js")>();
  return {
    ...actual,
    createKataWorkspaceForTask: mockCreateKataWorkspaceForTask,
    resolveKataWorkspaceTarget: mockResolveKataWorkspaceTarget,
  };
});

vi.mock("../../stores/router.svelte.js", () => ({
  navigate: mockNavigate,
}));

describe("KataWorkspace", () => {
  beforeEach(() => {
    resetKataWorkspaceTestState();
    mockCreateKataWorkspaceForTask.mockReset();
    mockNavigate.mockReset();
    mockResolveKataWorkspaceTarget.mockReset();
    mockResolveKataWorkspaceTarget.mockResolvedValue({ available: false });
  });

  afterEach(() => {
    cleanup();
    vi.useRealTimers();
    vi.restoreAllMocks();
  });

  it("loads the daemon roster on mount", async () => {
    vi.spyOn(globalThis, "fetch").mockImplementation(async () =>
      Response.json({
        daemons: [
          {
            id: "home",
            url: "http://127.0.0.1:7777",
            default: true,
            auth: "none",
            health: "connected",
          },
          {
            id: "work",
            url: "https://work.example",
            default: false,
            auth: "token",
            health: "auth_required",
          },
        ],
      }),
    );

    render(KataWorkspace);

    await waitFor(() => {
      expect(getKataDaemonRoster()).toEqual(["home", "work"]);
    });
    expect(getDefaultKataDaemon()).toBe("home");
  });

  it("bootstraps the route-selected task view", async () => {
    const { api } = createWorkspaceAPI();

    render(KataWorkspace, { props: { api, routeViewName: "inbox" } });

    await waitFor(() => {
      expect(screen.getByRole("heading", { name: "Inbox" })).toBeTruthy();
    });
  });

  it("opens system views without auto-selecting the first task", async () => {
    const rows = initialIssues.map((item) => (item.uid === "issue-pay-rent" ? { ...item, project_name: "" } : item));
    const { api } = createWorkspaceAPI(rows);
    const onRouteStateChange = vi.fn();

    render(KataWorkspace, { props: { api, onRouteStateChange } });

    await waitFor(() => {
      expect(screen.getByRole("heading", { name: "All Open" })).toBeTruthy();
      expect(screen.getByText("Select a task")).toBeTruthy();
    });

    await fireEvent.click(screen.getByRole("button", { name: /^Inbox\s+1$/ }));

    await waitFor(() => {
      expect(screen.getByRole("heading", { name: "Inbox" })).toBeTruthy();
      expect(screen.getByText("Select a task")).toBeTruthy();
    });
    expect(api.issue).not.toHaveBeenCalled();
    expect(onRouteStateChange).toHaveBeenLastCalledWith({
      view: "inbox",
      scope: null,
      issue: null,
    });
  });

  it("toggles and persists the task detail layout", async () => {
    vi.spyOn(globalThis, "fetch").mockImplementation(async () =>
      Response.json({
        daemons: [
          {
            id: "home",
            url: "http://127.0.0.1:7777",
            default: true,
            auth: "none",
            health: "connected",
          },
        ],
      }),
    );
    const { api } = createWorkspaceAPI();

    render(KataWorkspace, { props: { api } });

    await waitFor(() => {
      expect(screen.getByRole("heading", { name: "All Open" })).toBeTruthy();
      expect(screen.getByText("Select a task")).toBeTruthy();
    });

    expect(screen.getByRole("separator", { name: "Resize Kata panes" }).getAttribute("aria-orientation")).toBe(
      "horizontal",
    );
    await fireEvent.click(screen.getByRole("button", { name: "Switch to side-by-side layout" }));

    expect(screen.getByRole("separator", { name: "Resize Kata panes" }).getAttribute("aria-orientation")).toBe(
      "vertical",
    );
    expect(window.localStorage.getItem("middleman:kata:task-layout/v1")).toContain('"orientation":"horizontal"');
    expect(screen.getByRole("button", { name: "Switch to stacked layout" })).toBeTruthy();
  });

  it("does not leave single-group task regions with dangling labels", async () => {
    const { api } = createWorkspaceAPI([issue("issue-inbox-note", "Inbox note", "project-inbox")]);

    render(KataWorkspace, { props: { api, routeViewName: "inbox" } });

    await waitFor(() => {
      expect(screen.getByRole("button", { name: /Inbox note/ })).toBeTruthy();
    });
    expect(screen.getByRole("region", { name: /^Inbox\s+1$/ })).toBeTruthy();
  });

  it("renders closed logbook tasks while the default status filter remains open", async () => {
    const closedIssue = {
      ...issue("issue-done-work", "Done work", "project-kata"),
      status: "closed" as const,
      closed_at: "2026-05-15T12:00:00.000Z",
    };
    const { api } = createWorkspaceAPI([closedIssue]);

    render(KataWorkspace, { props: { api, routeViewName: "logbook" } });

    await waitFor(() => {
      expect(screen.getByRole("heading", { name: "Logbook" })).toBeTruthy();
      expect(screen.getByRole("combobox", { name: "Status: Open" })).toBeTruthy();
      expect(screen.getByRole("button", { name: /Done work/ })).toBeTruthy();
    });
    expect(screen.queryByText("No tasks")).toBeNull();
  });

  it("captures a new task into the inbox from the feature toolbar", async () => {
    const { api, createIssue } = createWorkspaceAPI();

    render(KataWorkspace, { props: { api } });
    await waitFor(() => {
      expect(screen.getByRole("heading", { name: "All Open" })).toBeTruthy();
    });

    await fireEvent.click(screen.getByRole("button", { name: "New task" }));
    const dialog = screen.getByRole("dialog", { name: "New task" });
    const input = within(dialog).getByRole("textbox", { name: "Quick capture" });
    expect(input).toBe(document.activeElement);

    await fireEvent.input(input, { target: { value: "Capture from notes" } });
    await fireEvent.click(within(dialog).getByRole("button", { name: "Capture" }));

    await waitFor(() => {
      expect(createIssue).toHaveBeenCalledWith(
        projects[0]!.id,
        "middleman",
        { title: "Capture from notes" },
        expect.any(String),
      );
      expect(screen.getByRole("heading", { name: "Capture from notes" })).toBeTruthy();
    });
  });

  it("shows the daemon switcher and reloads tasks after daemon selection", async () => {
    vi.spyOn(globalThis, "fetch").mockImplementation(async () =>
      Response.json({
        daemons: [
          {
            id: "home",
            url: "http://127.0.0.1:7777",
            default: true,
            auth: "none",
            health: "connected",
          },
          {
            id: "work",
            url: "http://127.0.0.1:8888",
            default: false,
            auth: "none",
            health: "connected",
          },
        ],
      }),
    );
    const api = createDaemonWorkspaceAPI({
      home: [initialIssues[0]!],
      work: [initialIssues[1]!],
    });

    render(KataWorkspace, { props: { api } });

    await waitFor(() => {
      expect(screen.getByTestId("daemon-chip").textContent).toContain("home");
      expect(screen.getByRole("button", { name: /Pay rent/ })).toBeTruthy();
    });
    expect(screen.queryByRole("heading", { name: "Email Susan re: Q3" })).toBeNull();

    await fireEvent.click(screen.getByTestId("daemon-chip"));
    await fireEvent.click(screen.getByTestId("daemon-row-work"));

    await waitFor(() => {
      expect(getActiveKataDaemon()).toBe("work");
      expect(screen.getByTestId("daemon-chip").textContent).toContain("work");
      expect(screen.getByRole("heading", { name: "Email Susan re: Q3" })).toBeTruthy();
    });
    expect(screen.queryByRole("heading", { name: "Pay rent" })).toBeNull();
    expect(api.issues).toHaveBeenCalledTimes(2);
  });

  it("keeps the daemon switcher visible for a single daemon after connecting", async () => {
    vi.spyOn(globalThis, "fetch").mockImplementation(async () =>
      Response.json({
        daemons: [
          {
            id: "local",
            url: "http://127.0.0.1:7777",
            default: true,
            auth: "none",
            health: "connected",
          },
        ],
      }),
    );
    const { api } = createWorkspaceAPI();

    render(KataWorkspace, { props: { api } });

    await waitFor(() => {
      expect(screen.getByRole("heading", { name: "All Open" })).toBeTruthy();
      expect(screen.getByRole("button", { name: /Pay rent/ })).toBeTruthy();
      expect(screen.getByTestId("daemon-chip").textContent).toContain("local");
    });
  });

  it("does not render a separate header connection status while a connected daemon is bootstrapping", async () => {
    vi.spyOn(globalThis, "fetch").mockImplementation(async () =>
      Response.json({
        daemons: [
          {
            id: "kenn",
            url: "http://127.0.0.1:7777",
            default: true,
            auth: "none",
            health: "connected",
          },
        ],
      }),
    );
    const { api, instance: instanceMock } = createWorkspaceAPI();
    const instance = deferred<Awaited<ReturnType<typeof api.instance>>>();
    instanceMock.mockReturnValue(instance.promise);
    const { container } = render(KataWorkspace, { props: { api } });

    await waitFor(() => {
      expect(screen.getByTestId("daemon-chip").textContent).toContain("kenn");
    });

    expect(container.querySelector(".daemon-status")).toBeNull();
    instance.resolve({
      instance_uid: "instance-1",
      version: "dev",
      schema_version: 1,
    });
  });

  it("clears the routed task when daemon selection leaves no selected task", async () => {
    vi.spyOn(globalThis, "fetch").mockImplementation(async () =>
      Response.json({
        daemons: [
          {
            id: "home",
            url: "http://127.0.0.1:7777",
            default: true,
            auth: "none",
            health: "connected",
          },
          {
            id: "empty",
            url: "http://127.0.0.1:8888",
            default: false,
            auth: "none",
            health: "connected",
          },
        ],
      }),
    );
    const api = createDaemonWorkspaceAPI({
      home: [initialIssues[0]!],
      empty: [],
    });
    const onSelectedIssueChange = vi.fn();

    render(KataWorkspace, {
      props: {
        api,
        selectedIssueUID: "issue-pay-rent",
        onSelectedIssueChange,
      },
    });

    await waitFor(() => {
      expect(screen.getByRole("heading", { name: "Pay rent" })).toBeTruthy();
    });
    vi.mocked(api.issue).mockClear();

    await fireEvent.click(screen.getByTestId("daemon-chip"));
    await fireEvent.click(screen.getByTestId("daemon-row-empty"));

    await waitFor(() => {
      expect(getActiveKataDaemon()).toBe("empty");
      expect(onSelectedIssueChange).toHaveBeenCalledWith(null);
      expect(screen.getByText("No tasks")).toBeTruthy();
    });
    expect(api.issue).not.toHaveBeenCalled();
    expect(screen.queryByRole("heading", { name: "Pay rent" })).toBeNull();
  });

  it("renders the read-only task workspace and switches project scope", async () => {
    vi.spyOn(globalThis, "fetch").mockImplementation(async () =>
      Response.json({
        daemons: [
          {
            id: "home",
            url: "http://127.0.0.1:7777",
            default: true,
            auth: "none",
            health: "connected",
          },
        ],
      }),
    );
    const { api, search } = createWorkspaceAPI();

    render(KataWorkspace, { props: { api, selectedIssueUID: "issue-pay-rent" } });

    await waitFor(() => {
      expect(screen.getByRole("heading", { name: "Pay rent" })).toBeTruthy();
    });
    const nav = within(screen.getByLabelText("Kata navigation"));
    expect(nav.getByRole("button", { name: /^Finances\s+1$/ })).toBeTruthy();
    expect(screen.getByText("Pay rent body")).toBeTruthy();

    await fireEvent.click(nav.getByRole("button", { name: /^Kata\s+1$/ }));

    await waitFor(() => {
      expect(screen.getByRole("heading", { name: "Email Susan re: Q3" })).toBeTruthy();
    });
    expect(screen.getByText("Email Susan re: Q3 body")).toBeTruthy();
    expect(search).toHaveBeenCalledWith(
      expect.objectContaining({ scope: { kind: "project", project_uid: "project-kata" } }),
    );
  });

  it("creates a workspace from the selected Kata task when a repository target resolves", async () => {
    vi.spyOn(globalThis, "fetch").mockImplementation(async () =>
      Response.json({
        daemons: [
          {
            id: "home",
            url: "http://127.0.0.1:7777",
            default: true,
            auth: "none",
            health: "connected",
          },
        ],
      }),
    );
    mockResolveKataWorkspaceTarget.mockResolvedValue({
      available: true,
      repo: {
        provider: "github",
        platform_host: "github.com",
        owner: "acme",
        name: "middleman",
        repo_path: "acme/middleman",
      },
      item_type: "kata_task",
      item_key: "issue-pay-rent",
    });
    mockCreateKataWorkspaceForTask.mockResolvedValue({
      id: "workspace-kata",
      item_type: "kata_task",
      item_key: "issue-pay-rent",
      git_head_ref: "middleman/kata/pay-rent",
      status: "creating",
    });
    const { api } = createWorkspaceAPI();

    render(KataWorkspace, { props: { api, selectedIssueUID: "issue-pay-rent" } });

    await waitFor(() => {
      expect(screen.getByRole("heading", { name: "Pay rent" })).toBeTruthy();
      expect(screen.getByRole("button", { name: "Create workspace" })).toBeTruthy();
    });
    expect(mockResolveKataWorkspaceTarget).toHaveBeenCalledWith(
      expect.objectContaining({
        daemon_id: "home",
        project_uid: "project-finances",
        project_name: "Finances",
        issue_uid: "issue-pay-rent",
      }),
    );
    await fireEvent.click(screen.getByRole("button", { name: "Create workspace" }));

    await waitFor(() => {
      expect(mockCreateKataWorkspaceForTask).toHaveBeenCalledWith(
        expect.objectContaining({
          daemon_id: "home",
          project_uid: "project-finances",
          project_name: "Finances",
          issue_uid: "issue-pay-rent",
        }),
      );
      expect(mockNavigate).toHaveBeenCalledWith("/terminal/workspace-kata");
    });
  });

  it("opens an existing workspace for the selected Kata task", async () => {
    vi.spyOn(globalThis, "fetch").mockImplementation(async () =>
      Response.json({
        daemons: [
          {
            id: "home",
            url: "http://127.0.0.1:7777",
            default: true,
            auth: "none",
            health: "connected",
          },
        ],
      }),
    );
    mockResolveKataWorkspaceTarget.mockResolvedValue({
      available: true,
      existing_workspace: {
        id: "workspace-existing",
        status: "ready",
      },
      item_type: "kata_task",
      item_key: "issue-pay-rent",
    });
    const { api } = createWorkspaceAPI();

    render(KataWorkspace, { props: { api, selectedIssueUID: "issue-pay-rent" } });

    await waitFor(() => {
      expect(screen.getByRole("heading", { name: "Pay rent" })).toBeTruthy();
      expect(screen.getByRole("button", { name: "Open workspace" })).toBeTruthy();
    });
    await fireEvent.click(screen.getByRole("button", { name: "Open workspace" }));

    expect(mockCreateKataWorkspaceForTask).not.toHaveBeenCalled();
    expect(mockNavigate).toHaveBeenCalledWith("/terminal/workspace-existing");
  });

  it("applies visible search and filter controls through the task search API", async () => {
    vi.spyOn(globalThis, "fetch").mockImplementation(async () =>
      Response.json({
        daemons: [
          {
            id: "home",
            url: "http://127.0.0.1:7777",
            default: true,
            auth: "none",
            health: "connected",
          },
        ],
      }),
    );
    const { api, search } = createWorkspaceAPI();

    render(KataWorkspace, { props: { api } });

    await waitFor(() => {
      expect(screen.getByRole("heading", { name: "All Open" })).toBeTruthy();
    });

    await fireEvent.input(screen.getByLabelText("Search tasks"), { target: { value: "q3" } });
    await fireEvent.click(screen.getByRole("combobox", { name: "Status: Open" }));
    await fireEvent.click(screen.getByRole("option", { name: "All" }));
    await fireEvent.change(screen.getByLabelText("Owner"), { target: { value: "fixture-user" } });
    await fireEvent.change(screen.getByLabelText("Label"), { target: { value: "work" } });
    await fireEvent.click(screen.getByRole("button", { name: /Project scope: All projects/i }));
    const projectInput = screen.getByRole("combobox", { name: "Project scope" });
    expect(document.activeElement).toBe(projectInput);
    await fireEvent.input(projectInput, { target: { value: "kat" } });
    await fireEvent.keyDown(projectInput, { key: "Enter" });

    await waitFor(() => {
      expect(search).toHaveBeenLastCalledWith({
        scope: { kind: "project", project_uid: "project-kata" },
        status: "all",
        owner: "fixture-user",
        label: "work",
        query: "q3",
      });
    });
  });

  it("hides closed task rows immediately when the status filter changes to open", async () => {
    vi.spyOn(globalThis, "fetch").mockImplementation(async () =>
      Response.json({
        daemons: [
          {
            id: "home",
            url: "http://127.0.0.1:7777",
            default: true,
            auth: "none",
            health: "connected",
          },
        ],
      }),
    );
    const openIssue = issue("issue-open-work", "Open work", "project-kata");
    const closedIssue = {
      ...issue("issue-done-work", "Done work", "project-kata"),
      status: "closed" as const,
      closed_at: "2026-05-15T12:00:00.000Z",
    };
    const { api, search } = createWorkspaceAPI([openIssue, closedIssue]);
    const delayedOpenSearch = deferred<KataTaskSearchResponse>();
    let openSearches = 0;
    search.mockImplementation(async (filters: KataTaskSearchFilters) => {
      if (filters.scope.kind !== "project" || filters.scope.project_uid !== "project-kata") {
        return { filters, issues: [], fetched_at: fetchedAt };
      }
      if (filters.status === "closed") {
        return { filters, issues: [closedIssue], fetched_at: fetchedAt };
      }
      if (filters.status === "open") {
        openSearches += 1;
        if (openSearches > 1) return delayedOpenSearch.promise;
        return { filters, issues: [openIssue], fetched_at: fetchedAt };
      }
      return { filters, issues: [openIssue, closedIssue], fetched_at: fetchedAt };
    });

    render(KataWorkspace, { props: { api, routeScopeUID: "project-kata" } });

    await waitFor(() => {
      expect(screen.getByRole("button", { name: /Open work/ })).toBeTruthy();
    });
    await fireEvent.click(screen.getByRole("combobox", { name: "Status: Open" }));
    await fireEvent.click(screen.getByRole("option", { name: "Closed" }));

    await waitFor(() => {
      expect(screen.getByRole("button", { name: /Done work/ })).toBeTruthy();
      expect(screen.getByRole("button", { name: "Reopen" })).toBeTruthy();
    });

    await fireEvent.click(screen.getByRole("combobox", { name: "Status: Closed" }));
    await fireEvent.click(screen.getByRole("option", { name: "Open" }));

    expect(screen.getByRole("combobox", { name: "Status: Open" })).toBeTruthy();
    expect(screen.queryByRole("button", { name: /Done work/ })).toBeNull();
    expect(screen.queryByRole("button", { name: "Reopen" })).toBeNull();

    delayedOpenSearch.resolve({
      filters: {
        scope: { kind: "project", project_uid: "project-kata" },
        status: "open",
        owner: "",
        label: "",
        query: "",
      },
      issues: [openIssue],
      fetched_at: fetchedAt,
    });
  });

  it("keeps the loading announcement active until the newest overlapping search finishes", async () => {
    vi.spyOn(globalThis, "fetch").mockImplementation(async () =>
      Response.json({
        daemons: [
          {
            id: "home",
            url: "http://127.0.0.1:7777",
            default: true,
            auth: "none",
            health: "connected",
          },
        ],
      }),
    );
    const { api, search } = createWorkspaceAPI();
    const oldSearch = deferred<KataTaskSearchResponse>();
    const newSearch = deferred<KataTaskSearchResponse>();
    let oldSearchSettled = false;
    search.mockImplementation(async (filters: KataTaskSearchFilters) => {
      if (filters.query === "old") {
        const result = await oldSearch.promise;
        oldSearchSettled = true;
        return result;
      }
      if (filters.query === "new") return newSearch.promise;
      return Promise.resolve({ filters, issues: initialIssues, fetched_at: fetchedAt });
    });

    render(KataWorkspace, { props: { api } });

    await waitFor(() => {
      expect(screen.getByRole("heading", { name: "All Open" })).toBeTruthy();
    });
    await fireEvent.input(screen.getByLabelText("Search tasks"), { target: { value: "old" } });
    await waitFor(() => expect(search).toHaveBeenCalledWith(expect.objectContaining({ query: "old" })));
    await fireEvent.input(screen.getByLabelText("Search tasks"), { target: { value: "new" } });
    await waitFor(() => expect(search).toHaveBeenCalledWith(expect.objectContaining({ query: "new" })));

    oldSearch.resolve({
      filters: { scope: { kind: "all" }, status: "open", owner: "", label: "", query: "old" },
      issues: [initialIssues[0]!],
      fetched_at: fetchedAt,
    });
    await waitFor(() => expect(oldSearchSettled).toBe(true));

    expect(screen.queryByText("Loading snapshot")).toBeTruthy();

    newSearch.resolve({
      filters: { scope: { kind: "all" }, status: "open", owner: "", label: "", query: "new" },
      issues: [initialIssues[1]!],
      fetched_at: fetchedAt,
    });
    await waitFor(() => {
      expect(screen.queryByText("Loading snapshot")).toBeNull();
      expect(screen.getByRole("heading", { name: "Email Susan re: Q3" })).toBeTruthy();
    });
  });

  it("clears the loading announcement when the newest overlapping search finishes first", async () => {
    vi.spyOn(globalThis, "fetch").mockImplementation(async () =>
      Response.json({
        daemons: [
          {
            id: "home",
            url: "http://127.0.0.1:7777",
            default: true,
            auth: "none",
            health: "connected",
          },
        ],
      }),
    );
    const { api, search } = createWorkspaceAPI();
    const oldSearch = deferred<KataTaskSearchResponse>();
    const newSearch = deferred<KataTaskSearchResponse>();
    let oldSearchSettled = false;
    search.mockImplementation(async (filters: KataTaskSearchFilters) => {
      if (filters.query === "old") {
        const result = await oldSearch.promise;
        oldSearchSettled = true;
        return result;
      }
      if (filters.query === "new") return newSearch.promise;
      return Promise.resolve({ filters, issues: initialIssues, fetched_at: fetchedAt });
    });

    render(KataWorkspace, { props: { api } });

    await waitFor(() => {
      expect(screen.getByRole("heading", { name: "All Open" })).toBeTruthy();
    });
    await fireEvent.input(screen.getByLabelText("Search tasks"), { target: { value: "old" } });
    await waitFor(() => expect(search).toHaveBeenCalledWith(expect.objectContaining({ query: "old" })));
    await fireEvent.input(screen.getByLabelText("Search tasks"), { target: { value: "new" } });
    await waitFor(() => expect(search).toHaveBeenCalledWith(expect.objectContaining({ query: "new" })));

    newSearch.resolve({
      filters: { scope: { kind: "all" }, status: "open", owner: "", label: "", query: "new" },
      issues: [initialIssues[1]!],
      fetched_at: fetchedAt,
    });

    await waitFor(() => {
      expect(screen.queryByText("Loading snapshot")).toBeNull();
      expect(screen.getByRole("heading", { name: "Email Susan re: Q3" })).toBeTruthy();
    });

    oldSearch.resolve({
      filters: { scope: { kind: "all" }, status: "open", owner: "", label: "", query: "old" },
      issues: [initialIssues[0]!],
      fetched_at: fetchedAt,
    });
    await waitFor(() => expect(oldSearchSettled).toBe(true));

    expect(screen.queryByText("Loading snapshot")).toBeNull();
    expect(screen.getByRole("heading", { name: "Email Susan re: Q3" })).toBeTruthy();
  });

  it("shows the normalized authentication message when bootstrap fails", async () => {
    vi.spyOn(globalThis, "fetch").mockImplementation(async () =>
      Response.json({
        daemons: [
          {
            id: "home",
            url: "http://127.0.0.1:7777",
            default: true,
            auth: "token",
            health: "auth_required",
          },
        ],
      }),
    );
    const { api } = createWorkspaceAPI();
    vi.mocked(api.instance).mockRejectedValueOnce(
      new KataTaskAPIError({
        status: 401,
        code: "unauthorized",
        message: "daemon token missing",
        headers: new Headers(),
      }),
    );

    render(KataWorkspace, { props: { api } });

    await waitFor(() => {
      expect(screen.getByTestId("daemon-chip").textContent).toContain("home");
    });
    await fireEvent.click(screen.getByTestId("daemon-chip"));
    expect(within(screen.getByTestId("daemon-row-home")).getByText("Authentication required")).toBeTruthy();
    expect(screen.queryByText("daemon token missing")).toBeNull();
  });

  it("shows a header error when bootstrap fails without a daemon switcher", async () => {
    vi.spyOn(globalThis, "fetch").mockImplementation(async () =>
      Response.json({
        daemons: [],
      }),
    );
    const { api, instance } = createWorkspaceAPI();
    instance.mockRejectedValueOnce(new Error("Kata daemon catalog is empty"));

    render(KataWorkspace, { props: { api } });

    const alert = await screen.findByRole("alert");
    expect(alert.textContent).toContain("Kata daemon catalog is empty");
    expect(screen.queryByTestId("daemon-chip")).toBeNull();
  });

  it("surfaces task request failures outside the daemon switcher", async () => {
    vi.spyOn(globalThis, "fetch").mockImplementation(async () =>
      Response.json({
        daemons: [
          {
            id: "home",
            url: "http://127.0.0.1:7777",
            default: true,
            auth: "none",
            health: "connected",
          },
        ],
      }),
    );
    const { api, assignOwner } = createWorkspaceAPI();
    assignOwner.mockRejectedValueOnce(new Error("owner unavailable"));

    render(KataWorkspace, { props: { api, selectedIssueUID: "issue-pay-rent" } });

    await waitFor(() => {
      expect(screen.getByRole("heading", { name: "Pay rent" })).toBeTruthy();
    });
    const detailRegion = within(screen.getByRole("region", { name: "Task detail" }));
    await fireEvent.click(detailRegion.getByRole("button", { name: "Owner: fixture-user" }));
    const ownerInput = detailRegion.getByRole("combobox", { name: "Owner" }) as HTMLInputElement;
    await fireEvent.input(ownerInput, { target: { value: "agent:new" } });
    await fireEvent.keyDown(ownerInput, { key: "Enter" });

    const alert = await screen.findByRole("alert");
    expect(alert.textContent).toContain("owner unavailable");
    expect(ownerInput.value).toBe("agent:new");
    expect(screen.getByTestId("daemon-chip").textContent).not.toContain("owner unavailable");
  });

  it("rehydrates linked task titles when switching daemons with matching peer uids", async () => {
    vi.spyOn(globalThis, "fetch").mockImplementation(async () =>
      Response.json({
        daemons: [
          {
            id: "home",
            url: "http://127.0.0.1:7777",
            default: true,
            auth: "none",
            health: "connected",
          },
          {
            id: "work",
            url: "http://127.0.0.1:8888",
            default: false,
            auth: "none",
            health: "connected",
          },
        ],
      }),
    );
    const payRent = initialIssues[0]!;
    const linkedHome = {
      ...issue("issue-linked", "Home linked task", "project-finances"),
      short_id: "linked",
      qualified_id: "Finances#linked",
    };
    const linkedWork = {
      ...linkedHome,
      title: "Work linked task",
    };
    const api = createDaemonWorkspaceAPI({
      home: [payRent],
      work: [payRent],
    });
    const issueMock = vi.fn(async (uid: string): Promise<KataTaskDetail> => {
      const active = getActiveKataDaemon() === "work" ? "work" : "home";
      const linked = active === "work" ? linkedWork : linkedHome;
      if (uid === payRent.uid) {
        return {
          ...detail(payRent.uid, [payRent]),
          links: [
            {
              id: 1,
              project_id: payRent.project_id,
              from: { uid: payRent.uid, short_id: payRent.short_id },
              to: { uid: linked.uid, short_id: linked.short_id },
              type: "related",
              author: "fixture-user",
              created_at: fetchedAt,
            },
          ],
        };
      }
      if (uid === linked.uid) {
        return {
          ...detail(linked.uid, [linked]),
          issue: { ...linked, body: `${linked.title} body` },
        };
      }
      return detail(uid, [payRent, linked]);
    });

    render(KataWorkspace, { props: { api: { ...api, issue: issueMock }, selectedIssueUID: payRent.uid } });

    await waitFor(() => {
      expect(screen.getByRole("heading", { name: "Pay rent" })).toBeTruthy();
    });
    const detailRegion = await screen.findByRole("region", { name: "Task detail" });
    const links = within(detailRegion).getByRole("region", { name: "Links" });
    await waitFor(() => {
      expect(within(links).getByText("Home linked task")).toBeTruthy();
    });

    await fireEvent.click(screen.getByTestId("daemon-chip"));
    await fireEvent.click(screen.getByTestId("daemon-row-work"));

    await waitFor(() => {
      expect(getActiveKataDaemon()).toBe("work");
      expect(within(links).getByText("Work linked task")).toBeTruthy();
    });
    expect(within(links).queryByText("Home linked task")).toBeNull();
  });

  it("resets detail drafts when switching selected tasks", async () => {
    vi.spyOn(globalThis, "fetch").mockImplementation(async () =>
      Response.json({
        daemons: [
          {
            id: "home",
            url: "http://127.0.0.1:7777",
            default: true,
            auth: "none",
            health: "connected",
          },
        ],
      }),
    );
    const { api } = createWorkspaceAPI();

    render(KataWorkspace, { props: { api, selectedIssueUID: "issue-pay-rent" } });

    await waitFor(() => {
      expect(screen.getByRole("heading", { name: "Pay rent" })).toBeTruthy();
    });
    const detail = screen.getByRole("region", { name: "Task detail" });

    await fireEvent.input(within(detail).getByLabelText("Comment"), { target: { value: "Draft reply" } });
    await fireEvent.click(within(detail).getByRole("button", { name: "Add label" }));
    await fireEvent.input(within(detail).getByLabelText("New label"), { target: { value: "personal" } });

    await fireEvent.click(screen.getByRole("button", { name: /Email Susan re: Q3/ }));
    await waitFor(() => {
      expect(screen.getByRole("heading", { name: "Email Susan re: Q3" })).toBeTruthy();
    });

    const nextDetail = screen.getByRole("region", { name: "Task detail" });
    expect((within(nextDetail).getByLabelText("Comment") as HTMLTextAreaElement).value).toBe("");
    expect(within(nextDetail).queryByLabelText("New label")).toBeNull();
  });

  it("unlinks messages through the existing metadata patch path", async () => {
    vi.spyOn(globalThis, "fetch").mockImplementation(async () =>
      Response.json({
        daemons: [
          {
            id: "home",
            url: "http://127.0.0.1:7777",
            default: true,
            auth: "none",
            health: "connected",
          },
        ],
      }),
    );
    const link = messageLink({ message_id: 2001, subject: "Lease renewal" });
    const rows = initialIssues.map((item) =>
      item.uid === "issue-pay-rent" ? { ...item, metadata: { ...item.metadata, mail_links: [link] } } : item,
    );
    const { api, patchIssueMetadata } = createWorkspaceAPI(rows);

    render(KataWorkspace, { props: { api, selectedIssueUID: "issue-pay-rent" } });

    await waitFor(() => {
      expect(screen.getByRole("heading", { name: "Pay rent" })).toBeTruthy();
    });
    await fireEvent.click(screen.getByRole("button", { name: "Unlink Lease renewal" }));

    await waitFor(() => {
      expect(patchIssueMetadata).toHaveBeenCalledWith(
        { project_id: projects[1]!.id, ref: "issue-pay-rent" },
        "middleman",
        { mail_links: null },
        '"rev-1"',
      );
    });
    await waitFor(() => {
      expect(screen.queryByText("Lease renewal")).toBeNull();
    });
  });
});
