import { cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/svelte";
import { afterEach, beforeEach, describe, expect, it, vi } from "vite-plus/test";

import type {
  KataTaskAPI,
  KataTaskDetail,
  KataTaskIssuesQuery,
  KataTaskSearchFilters,
  KataTaskSearchResponse,
} from "../../api/kata/taskTypes.js";
import { buildKataTaskView } from "../../api/kata/taskViewBuilder.js";

import KataWorkspace from "./KataWorkspace.svelte";
import {
  createWorkspaceAPI,
  deferred,
  detail,
  fetchedAt,
  initialIssues,
  issue,
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

  it("does not emit stale project-scope routes after a newer system-view navigation", async () => {
    vi.useFakeTimers();
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
    const stalledSearch = deferred<KataTaskSearchResponse>();
    let stalledSearchSettled = false;
    search.mockImplementationOnce(async (filters: KataTaskSearchFilters) => {
      await stalledSearch.promise;
      stalledSearchSettled = true;
      return {
        filters,
        issues: initialIssues.filter((item) =>
          filters.scope.kind === "project" ? item.project_uid === filters.scope.project_uid : true,
        ),
        fetched_at: fetchedAt,
      };
    });
    const onRouteStateChange = vi.fn();
    render(KataWorkspace, { props: { api, onRouteStateChange } });

    await waitFor(() => {
      expect(screen.getByRole("heading", { name: "All Open" })).toBeTruthy();
    });
    await fireEvent.click(screen.getByRole("button", { name: /^Kata\s+1$/ }));
    await vi.advanceTimersByTimeAsync(1_000);
    await waitFor(() => expect(search).toHaveBeenCalledTimes(1));

    await fireEvent.click(screen.getByRole("button", { name: /^Inbox\b/ }));
    await waitFor(() => {
      expect(screen.getByRole("heading", { name: "Inbox" })).toBeTruthy();
    });
    stalledSearch.resolve({
      filters: {
        scope: { kind: "project", project_uid: "project-kata" },
        status: "open",
        owner: "",
        label: "",
        query: "",
      },
      issues: [initialIssues[1]!],
      fetched_at: fetchedAt,
    });
    await Promise.resolve();
    await Promise.resolve();
    await waitFor(() => expect(stalledSearchSettled).toBe(true));

    expect(onRouteStateChange).toHaveBeenLastCalledWith({
      view: "inbox",
      scope: null,
      issue: null,
    });
  });

  it("does not let a stale routed view select an issue after a newer route wins", async () => {
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
    const stalledView = deferred<Awaited<ReturnType<KataTaskAPI["issues"]>>>();
    const { rerender } = render(KataWorkspace, { props: { api, routeViewName: "today" } });

    await waitFor(() => {
      expect(screen.getByRole("heading", { name: "Today" })).toBeTruthy();
    });
    await screen.findByText("Live updates disconnected");
    await waitFor(() => expect(api.issues).toHaveBeenCalledTimes(2));
    await Promise.resolve();
    await Promise.resolve();
    vi.mocked(api.issues).mockImplementationOnce(async (_query: KataTaskIssuesQuery) => stalledView.promise);
    await rerender({ api, routeViewName: "inbox", selectedIssueUID: "issue-email-susan" });
    await waitFor(() => expect(api.issues).toHaveBeenCalledWith({ view: "inbox" }));

    await rerender({ api, routeViewName: null, selectedIssueUID: null });
    await waitFor(() => {
      expect(screen.getByRole("heading", { name: "All Open" })).toBeTruthy();
      expect(screen.getByText("Select a task")).toBeTruthy();
    });
    stalledView.resolve(
      buildKataTaskView({
        view: "inbox",
        issues: initialIssues,
        projects,
        today: "2026-05-15",
        fetched_at: fetchedAt,
      }),
    );
    await Promise.resolve();
    await Promise.resolve();

    // Match on the first argument only: issue() also receives a
    // { signal } options argument, so a full-arguments matcher would
    // pass vacuously even if the stale selection fired.
    expect(vi.mocked(api.issue).mock.calls.map((call) => call[0])).not.toContain("issue-email-susan");
    expect(screen.queryByRole("heading", { name: "Email Susan re: Q3" })).toBeNull();
    expect(screen.queryByRole("heading", { name: "Pay rent" })).toBeNull();
    const taskList = document.querySelector(".issue-list");
    expect(taskList).not.toBeNull();
    expect(within(taskList as HTMLElement).getByRole("heading", { name: "All Open", level: 2 })).toBeTruthy();
    expect(within(taskList as HTMLElement).queryByRole("heading", { name: "Inbox" })).toBeNull();
  });

  it("selects the routed task after bootstrap", async () => {
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

    render(KataWorkspace, { props: { api, selectedIssueUID: "issue-email-susan" } });

    await waitFor(() => {
      expect(screen.getByRole("heading", { name: "Email Susan re: Q3" })).toBeTruthy();
    });
    expect(api.issue).toHaveBeenCalledWith("issue-email-susan", { signal: expect.any(AbortSignal) });
  });

  it("selects the routed task after bootstrap when it is outside the initial view", async () => {
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
    const routedIssue = issue("issue-later-review", "Later review", "project-kata", {
      scheduled_on: "2026-05-20",
    });
    const { api } = createWorkspaceAPI([...initialIssues, routedIssue]);
    const issueMock = vi.mocked(api.issue);

    render(KataWorkspace, { props: { api, selectedIssueUID: "issue-later-review" } });

    await waitFor(() => {
      expect(screen.getByRole("heading", { name: "Later review" })).toBeTruthy();
    });
    expect(issueMock).toHaveBeenCalledWith("issue-later-review", { signal: expect.any(AbortSignal) });
  });

  it("updates the selection when the routed task changes", async () => {
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
    const { rerender } = render(KataWorkspace, { props: { api } });

    await waitFor(() => {
      expect(screen.getByRole("heading", { name: "All Open" })).toBeTruthy();
    });

    await rerender({ api, selectedIssueUID: "issue-email-susan" });

    await waitFor(() => {
      expect(screen.getByRole("heading", { name: "Email Susan re: Q3" })).toBeTruthy();
    });
  });

  it("clears the selected detail when the route task is reset", async () => {
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
    const { rerender } = render(KataWorkspace, { props: { api, selectedIssueUID: "issue-email-susan" } });

    await waitFor(() => {
      expect(screen.getByRole("heading", { name: "Email Susan re: Q3" })).toBeTruthy();
    });

    await rerender({ api, selectedIssueUID: null });

    await waitFor(() => {
      expect(screen.getByText("Select a task")).toBeTruthy();
      expect(screen.queryByRole("heading", { name: "Email Susan re: Q3" })).toBeNull();
    });
  });

  it("clears row-selected detail when the route task is reset", async () => {
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
    const onSelectedIssueChange = vi.fn();
    const { rerender } = render(KataWorkspace, { props: { api, onSelectedIssueChange } });

    await waitFor(() => {
      expect(screen.getByRole("heading", { name: "All Open" })).toBeTruthy();
    });
    await fireEvent.click(screen.getByRole("button", { name: /Email Susan re: Q3/ }));
    await waitFor(() => {
      expect(onSelectedIssueChange).toHaveBeenCalledWith("issue-email-susan");
      expect(screen.getByRole("heading", { name: "Email Susan re: Q3" })).toBeTruthy();
    });

    await rerender({ api, selectedIssueUID: "issue-email-susan", onSelectedIssueChange });
    await rerender({ api, selectedIssueUID: null, onSelectedIssueChange });

    await waitFor(() => {
      expect(screen.getByText("Select a task")).toBeTruthy();
      expect(screen.queryByRole("heading", { name: "Email Susan re: Q3" })).toBeNull();
    });
  });

  it("notifies when a task row is selected", async () => {
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
    const onSelectedIssueChange = vi.fn();

    render(KataWorkspace, { props: { api, onSelectedIssueChange } });

    await waitFor(() => {
      expect(screen.getByRole("heading", { name: "All Open" })).toBeTruthy();
    });
    await fireEvent.click(screen.getByRole("button", { name: /Email Susan re: Q3/ }));

    await waitFor(() => {
      expect(screen.getByRole("heading", { name: "Email Susan re: Q3" })).toBeTruthy();
    });
    await waitFor(() => {
      expect(onSelectedIssueChange).toHaveBeenCalledWith("issue-email-susan");
    });
  });

  it("highlights the pending task row immediately while detail loads", async () => {
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
    const slowEmail = deferred<KataTaskDetail>();
    vi.mocked(api.issue).mockImplementation(async (uid: string) => {
      if (uid === "issue-email-susan") return slowEmail.promise;
      return detail(uid);
    });

    render(KataWorkspace, { props: { api } });

    await waitFor(() => {
      expect(screen.getByRole("heading", { name: "All Open" })).toBeTruthy();
    });
    await fireEvent.click(screen.getByRole("button", { name: /Email Susan re: Q3/ }));

    await waitFor(() => {
      const emailRow = document.querySelector<HTMLElement>('.row[data-uid="issue-email-susan"]');
      expect(emailRow?.classList.contains("selected")).toBe(true);
    });
    expect(screen.getByText("Loading task")).toBeTruthy();

    slowEmail.resolve(detail("issue-email-susan"));
    await waitFor(() => {
      expect(screen.getByRole("heading", { name: "Email Susan re: Q3" })).toBeTruthy();
    });
  });

  it("does not notify for a stale row selection after a newer selection wins", async () => {
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
    const slowEmail = deferred<KataTaskDetail>();
    vi.mocked(api.issue).mockImplementation(async (uid: string) => {
      if (uid === "issue-email-susan") return slowEmail.promise;
      return detail(uid);
    });
    const onSelectedIssueChange = vi.fn();

    render(KataWorkspace, { props: { api, onSelectedIssueChange } });

    await waitFor(() => {
      expect(screen.getByRole("heading", { name: "All Open" })).toBeTruthy();
    });

    await fireEvent.click(screen.getByRole("button", { name: /Email Susan re: Q3/ }));
    await Promise.resolve();
    await fireEvent.click(screen.getByRole("button", { name: /Pay rent/ }));

    await waitFor(() => {
      expect(onSelectedIssueChange).toHaveBeenCalledWith("issue-pay-rent");
    });
    slowEmail.resolve(detail("issue-email-susan"));
    await Promise.resolve();

    expect(screen.getByRole("heading", { name: "Pay rent" })).toBeTruthy();
    expect(onSelectedIssueChange).not.toHaveBeenCalledWith("issue-email-susan");
  });
});
