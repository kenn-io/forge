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

describe("KataWorkspace", () => {
  beforeEach(() => {
    resetKataWorkspaceTestState();
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
    const { api } = createWorkspaceAPI();
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
      expect(screen.getByRole("status").textContent).toContain("Authentication required");
    });
    expect(screen.queryByText("daemon token missing")).toBeNull();
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
