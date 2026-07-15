import { cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/svelte";
import { afterEach, beforeEach, describe, expect, it, vi } from "vite-plus/test";
import * as flash from "@middleman/ui/stores/flash";
import { tick } from "svelte";

import { KataTaskAPIError } from "../../api/kata/taskClient.js";
import type {
  KataTaskAPI,
  KataTaskDetail,
  KataTaskEventsResponse,
  KataTaskLink,
  KataTaskSearchFilters,
  KataTaskSearchResponse,
  KataTaskViewName,
} from "../../api/kata/taskTypes.js";
import type { KataWorkspaceTarget } from "../../api/kata/workspaces.js";
import {
  getActiveKataDaemon,
  getDefaultKataDaemon,
  getKataDaemonRoster,
  setActiveKataDaemon,
} from "../../stores/active-kata-daemon.svelte.js";
import { defaultProviderCapabilities } from "../../components/repositories/repoSummary.js";
import KataWorkspace from "./KataWorkspace.svelte";
import KataWorkspaceRouteHost from "./KataWorkspaceRouteHost.svelte";
import { loadKataWorkspaceState, saveKataWorkspaceState } from "./kataWorkspacePersistence.js";
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
  recurrence,
  resetKataWorkspaceTestState,
} from "./KataWorkspaceTestSupport.js";

const { mockCreateKataWorkspaceForTask, mockNavigate } = vi.hoisted(() => ({
  mockCreateKataWorkspaceForTask: vi.fn(),
  mockNavigate: vi.fn(),
}));

vi.mock("../../api/kata/workspaces.js", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../../api/kata/workspaces.js")>();
  return {
    ...actual,
    createKataWorkspaceForTask: mockCreateKataWorkspaceForTask,
  };
});

vi.mock("../../stores/router.svelte.js", () => ({
  navigate: mockNavigate,
}));

vi.mock("./KataReachableGraph.svelte", async () => ({
  default: (await import("./KataReachableGraphTestStub.svelte")).default,
}));

function graphNodeWithText(text: string): HTMLElement {
  const node = screen
    .getAllByText(text)
    .find((element) => element.closest(".svelte-flow__node"))
    ?.closest(".svelte-flow__node");
  expect(node).toBeTruthy();
  return node as HTMLElement;
}

function graphNodeButtonWithText(text: string): HTMLButtonElement {
  const button = graphNodeWithText(text).querySelector<HTMLButtonElement>("button.graph-task-node");
  expect(button).toBeTruthy();
  return button!;
}

function acceptHomeDaemon(): void {
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
  setActiveKataDaemon("home");
}

async function waitForWorkspaceWritable(): Promise<void> {
  await waitFor(() =>
    expect((screen.getByRole("button", { name: "New task" }) as HTMLButtonElement).disabled).toBe(false),
  );
}

describe("KataWorkspace", () => {
  beforeEach(() => {
    resetKataWorkspaceTestState();
    mockCreateKataWorkspaceForTask.mockReset();
    mockNavigate.mockReset();
  });

  afterEach(() => {
    cleanup();
    for (const item of flash.getFlashes()) flash.dismissFlash(item.id);
    vi.useRealTimers();
    vi.restoreAllMocks();
  });

  it("restores the accepted daemon's filters and selected issue on a bare route", async () => {
    const child = {
      ...issue("issue-child", "Child task", "project-kata"),
      parent: { uid: "issue-parent", short_id: "parent" },
      parent_short_id: "parent",
    };
    const { api } = createWorkspaceAPI([issue("issue-parent", "Parent task", "project-kata"), child]);
    saveKataWorkspaceState("home", {
      view: "all",
      filters: {
        scope: { kind: "project", project_uid: "project-kata" },
        status: "all",
        owner: "Susan",
        label: "work",
        query: "child",
      },
      selectedIssueUID: child.uid,
    });
    const onRouteStateChange = vi.fn();

    render(KataWorkspace, { props: { api, onRouteStateChange } });

    await waitFor(() => expect(screen.getByRole("heading", { name: "Child task" })).toBeTruthy());
    expect(api.search).toHaveBeenCalledWith(
      expect.objectContaining({
        scope: { kind: "project", project_uid: "project-kata" },
        status: "all",
        owner: "Susan",
        label: "work",
        query: "child",
      }),
      expect.objectContaining({ signal: expect.any(AbortSignal) }),
    );
    expect(onRouteStateChange).toHaveBeenCalledWith(
      { view: null, scope: "project-kata", issue: child.uid },
      { replace: true },
    );
  });

  it("uses explicit route fields without writing them over persisted non-route filters", async () => {
    const explicit = issue("issue-explicit", "Explicit task", "project-finances");
    const { api, search } = createWorkspaceAPI([explicit]);
    const persisted = {
      view: "inbox" as const,
      filters: {
        scope: { kind: "project" as const, project_uid: "project-kata" },
        status: "closed" as const,
        owner: "Susan",
        label: "work",
        query: "omits explicit",
      },
      selectedIssueUID: "issue-stale",
    };
    saveKataWorkspaceState("home", persisted);

    render(KataWorkspace, {
      props: {
        api,
        routeViewName: "today",
        routeScopeUID: "project-finances",
        selectedIssueUID: explicit.uid,
      },
    });

    await waitFor(() => expect(screen.getByRole("heading", { name: "Explicit task" })).toBeTruthy());
    expect(search).toHaveBeenCalledWith(
      {
        ...persisted.filters,
        scope: { kind: "project", project_uid: "project-finances" },
      },
      expect.objectContaining({ signal: expect.any(AbortSignal) }),
    );
    expect(loadKataWorkspaceState("home")).toEqual(persisted);
  });

  it("rejects a missing persisted project before loading rows or detail", async () => {
    const { api, projects: projectCatalog, search, issue: issueDetail } = createWorkspaceAPI();
    saveKataWorkspaceState("home", {
      view: "inbox",
      filters: {
        scope: { kind: "project", project_uid: "project-missing" },
        status: "all",
        owner: "Susan",
        label: "stale",
        query: "discard",
      },
      selectedIssueUID: "issue-stale",
    });
    const onRouteStateChange = vi.fn();

    render(KataWorkspace, { props: { api, onRouteStateChange } });

    await waitFor(() => expect(projectCatalog).toHaveBeenCalledTimes(1));
    expect(screen.getByText("Select a task")).toBeTruthy();
    expect(search).not.toHaveBeenCalled();
    expect(issueDetail).not.toHaveBeenCalled();
    expect(screen.getByRole("heading", { name: "All Open" })).toBeTruthy();
    expect(screen.getByRole("button", { name: /Project scope: All projects/i })).toBeTruthy();
    expect(onRouteStateChange).toHaveBeenCalledTimes(1);
    expect(onRouteStateChange).toHaveBeenCalledWith({ view: null, scope: null, issue: null }, { replace: true });
    expect(loadKataWorkspaceState("home")).toBeNull();
  });

  it("discards an invalid persisted scope without discarding an explicit URL view", async () => {
    const { api, projects: projectCatalog, issues } = createWorkspaceAPI();
    saveKataWorkspaceState("home", {
      view: "inbox",
      filters: {
        scope: { kind: "project", project_uid: "project-missing" },
        status: "all",
        owner: "Susan",
        label: "stale",
        query: "discard",
      },
      selectedIssueUID: "issue-stale",
    });

    render(KataWorkspace, { props: { api, routeViewName: "today" } });

    await waitFor(() => expect(projectCatalog).toHaveBeenCalledTimes(1));
    expect(screen.getByRole("heading", { name: "Today" })).toBeTruthy();
    expect(issues).toHaveBeenCalledWith(
      expect.objectContaining({ view: "today" }),
      expect.objectContaining({ signal: expect.any(AbortSignal) }),
    );
    expect(loadKataWorkspaceState("home")).toBeNull();
  });

  it("discards an invalid persisted scope without discarding an explicit URL issue", async () => {
    const explicit = initialIssues[0]!;
    const { api, issue: issueDetail } = createWorkspaceAPI();
    saveKataWorkspaceState("home", {
      view: "inbox",
      filters: {
        scope: { kind: "project", project_uid: "project-missing" },
        status: "all",
        owner: "Susan",
        label: "stale",
        query: "discard",
      },
      selectedIssueUID: "issue-stale",
    });

    render(KataWorkspace, { props: { api, selectedIssueUID: explicit.uid } });

    await waitFor(() => expect(screen.getByRole("heading", { name: explicit.title })).toBeTruthy());
    expect(issueDetail).toHaveBeenCalledWith(
      explicit.uid,
      expect.objectContaining({ signal: expect.any(AbortSignal) }),
    );
    expect(loadKataWorkspaceState("home")).toBeNull();
  });

  it("discards an invalid persisted scope but initializes a valid URL scope with defaults", async () => {
    const { api, projects: projectCatalog, search } = createWorkspaceAPI();
    saveKataWorkspaceState("home", {
      view: "inbox",
      filters: {
        scope: { kind: "project", project_uid: "project-missing" },
        status: "all",
        owner: "Susan",
        label: "stale",
        query: "discard",
      },
      selectedIssueUID: "issue-stale",
    });

    render(KataWorkspace, { props: { api, routeScopeUID: "project-kata" } });

    await waitFor(() => expect(search).toHaveBeenCalled());
    expect(projectCatalog).toHaveBeenCalledTimes(1);
    expect(search).toHaveBeenCalledWith(
      {
        scope: { kind: "project", project_uid: "project-kata" },
        status: "open",
        owner: "",
        label: "",
        query: "",
      },
      expect.objectContaining({ signal: expect.any(AbortSignal) }),
    );
    expect(loadKataWorkspaceState("home")).toBeNull();
  });

  it("keeps an invalid explicit URL scope authoritative over persisted scope", async () => {
    const { api, search } = createWorkspaceAPI();
    const persisted = {
      view: "all" as const,
      filters: {
        scope: { kind: "project" as const, project_uid: "project-kata" },
        status: "all" as const,
        owner: "Susan",
        label: "work",
        query: "saved",
      },
      selectedIssueUID: null,
    };
    saveKataWorkspaceState("home", persisted);
    const onRouteStateChange = vi.fn();

    render(KataWorkspace, { props: { api, routeScopeUID: "project-missing", onRouteStateChange } });

    await waitFor(() =>
      expect(onRouteStateChange).toHaveBeenCalledWith({ view: null, scope: null, issue: null }, { replace: true }),
    );
    expect(search).toHaveBeenCalledWith(
      { ...persisted.filters, scope: { kind: "all" } },
      expect.objectContaining({ signal: expect.any(AbortSignal) }),
    );
    expect(loadKataWorkspaceState("home")).toEqual(persisted);
  });

  it("clears only a persisted selection missing from the filtered raw result", async () => {
    const selected = { ...issue("issue-closed", "Closed task", "project-kata"), status: "closed" as const };
    const { api, issue: issueDetail } = createWorkspaceAPI([selected]);
    saveKataWorkspaceState("home", {
      view: "all",
      filters: {
        scope: { kind: "project", project_uid: "project-kata" },
        status: "open",
        owner: "Susan",
        label: "work",
        query: "saved",
      },
      selectedIssueUID: selected.uid,
    });

    render(KataWorkspace, { props: { api } });

    await waitFor(() => expect(loadKataWorkspaceState("home")?.selectedIssueUID).toBeNull());
    expect(screen.getByText("Select a task")).toBeTruthy();
    expect(issueDetail).not.toHaveBeenCalled();
    expect(loadKataWorkspaceState("home")).toEqual({
      view: "all",
      filters: expect.objectContaining({ owner: "Susan", label: "work", query: "saved" }),
      selectedIssueUID: null,
    });
  });

  it("clears only the persisted selection after a definitive detail absence", async () => {
    const selected = issue("issue-missing-detail", "Missing detail", "project-kata");
    const { api, issue: issueDetail } = createWorkspaceAPI([selected]);
    issueDetail.mockRejectedValue(
      new KataTaskAPIError({ status: 404, code: "not_found", message: "not found", headers: new Headers() }),
    );
    saveKataWorkspaceState("home", {
      view: "all",
      filters: {
        scope: { kind: "project", project_uid: "project-kata" },
        status: "all",
        owner: "Susan",
        label: "work",
        query: "saved",
      },
      selectedIssueUID: selected.uid,
    });
    const onRouteStateChange = vi.fn();

    render(KataWorkspace, { props: { api, onRouteStateChange } });

    await waitFor(() =>
      expect(onRouteStateChange).toHaveBeenLastCalledWith(
        { view: null, scope: "project-kata", issue: null },
        { replace: true },
      ),
    );
    expect(screen.getByText("Select a task")).toBeTruthy();
    expect(loadKataWorkspaceState("home")?.selectedIssueUID).toBeNull();
    expect(loadKataWorkspaceState("home")?.filters.owner).toBe("Susan");
  });

  it("retries a transient catalog failure without clearing the persisted snapshot", async () => {
    const selected = issue("issue-catalog-retry", "Catalog retry task", "project-kata");
    const { api, projects: projectCatalog, search } = createWorkspaceAPI([selected]);
    projectCatalog.mockRejectedValueOnce(new Error("catalog timed out"));
    saveKataWorkspaceState("home", {
      view: "all",
      filters: {
        scope: { kind: "project", project_uid: "project-kata" },
        status: "all",
        owner: "Susan",
        label: "work",
        query: "saved",
      },
      selectedIssueUID: selected.uid,
    });

    render(KataWorkspace, { props: { api } });

    await waitFor(() => expect(screen.getByRole("button", { name: "Retry" })).toBeTruthy());
    expect(loadKataWorkspaceState("home")?.selectedIssueUID).toBe(selected.uid);
    expect(search).not.toHaveBeenCalled();
    await fireEvent.click(screen.getByRole("button", { name: "Retry" }));
    await waitFor(() => expect(screen.getByRole("heading", { name: "Catalog retry task" })).toBeTruthy());
    expect(projectCatalog).toHaveBeenCalledTimes(2);
    expect(search).toHaveBeenCalledTimes(1);
  });

  it("does not emit a restored route after unmount", async () => {
    const { api, projects: projectCatalog } = createWorkspaceAPI();
    const catalog = deferred<Awaited<ReturnType<typeof api.projects>>>();
    projectCatalog.mockReturnValue(catalog.promise);
    const onRouteStateChange = vi.fn();

    const rendered = render(KataWorkspace, { props: { api, onRouteStateChange } });
    await waitFor(() => expect(projectCatalog).toHaveBeenCalledTimes(1));
    rendered.unmount();
    catalog.resolve({ projects, fetched_at: fetchedAt });
    await Promise.resolve();
    await Promise.resolve();

    expect(onRouteStateChange).not.toHaveBeenCalled();
  });

  it("retries only transient persisted detail restoration and later clears a definitive failure", async () => {
    const selected = issue("issue-retry", "Retry task", "project-kata");
    const { api, projects: projectCatalog, search, issue: issueDetail } = createWorkspaceAPI([selected]);
    issueDetail
      .mockRejectedValueOnce(new Error("timed out"))
      .mockRejectedValueOnce(
        new KataTaskAPIError({ status: 404, code: "not_found", message: "not found", headers: new Headers() }),
      );
    saveKataWorkspaceState("home", {
      view: "all",
      filters: {
        scope: { kind: "project", project_uid: "project-kata" },
        status: "all",
        owner: "Susan",
        label: "work",
        query: "saved",
      },
      selectedIssueUID: selected.uid,
    });
    const onRouteStateChange = vi.fn();

    render(KataWorkspace, { props: { api, onRouteStateChange } });

    await screen.findByRole("button", { name: "Retry" });
    expect(loadKataWorkspaceState("home")?.selectedIssueUID).toBe(selected.uid);
    expect(projectCatalog).toHaveBeenCalledTimes(1);
    expect(search).toHaveBeenCalledTimes(1);
    await fireEvent.click(screen.getByRole("button", { name: "Retry" }));
    await waitFor(() => expect(issueDetail).toHaveBeenCalledTimes(2));
    await waitFor(() => expect(screen.queryByRole("button", { name: "Retry" })).toBeNull());
    expect(projectCatalog).toHaveBeenCalledTimes(1);
    expect(search).toHaveBeenCalledTimes(1);
    expect(loadKataWorkspaceState("home")?.selectedIssueUID).toBeNull();
    expect(onRouteStateChange).toHaveBeenLastCalledWith(
      { view: null, scope: "project-kata", issue: null },
      { replace: true },
    );
  });

  it("invalidates a persisted-selection Retry when the route navigates elsewhere", async () => {
    const selected = issue("issue-retry-route", "Retry route task", "project-kata");
    const { api, issue: issueDetail } = createWorkspaceAPI([selected]);
    issueDetail.mockRejectedValueOnce(new Error("timed out"));
    saveKataWorkspaceState("home", {
      view: "all",
      filters: {
        scope: { kind: "project", project_uid: "project-kata" },
        status: "all",
        owner: "Susan",
        label: "work",
        query: "saved",
      },
      selectedIssueUID: selected.uid,
    });

    const { component } = render(KataWorkspaceRouteHost, { props: { api } });

    const retry = await screen.findByRole("button", { name: "Retry" });
    component.setRoute({ view: "today", scope: null, issue: null });
    await waitFor(() => expect(screen.getByRole("heading", { name: "Today" })).toBeTruthy());
    expect(screen.queryByRole("button", { name: "Retry" })).toBeNull();

    await fireEvent.click(retry);
    await Promise.resolve();

    expect(issueDetail).toHaveBeenCalledTimes(1);
    expect(screen.getByRole("heading", { name: "Today" })).toBeTruthy();
    expect(screen.getByText("Select a task")).toBeTruthy();
  });

  it("removes a definitively missing explicit URL issue from the route", async () => {
    const { api, issue: issueDetail } = createWorkspaceAPI();
    issueDetail.mockRejectedValue(
      new KataTaskAPIError({ status: 404, code: "not_found", message: "not found", headers: new Headers() }),
    );
    const onRouteStateChange = vi.fn();

    const { component } = render(KataWorkspaceRouteHost, {
      props: {
        api,
        initialIssue: "issue-missing",
        onRouteStateChange,
      },
    });

    await waitFor(() => expect(component.route().issue).toBeNull());
    expect(screen.getByText("Select a task")).toBeTruthy();
    expect(onRouteStateChange).toHaveBeenCalledWith({ issue: null }, { replace: true });
    expect(screen.queryByRole("button", { name: "Retry" })).toBeNull();
  });

  it("accepts a restored selection before its ancestor request settles", async () => {
    vi.spyOn(globalThis, "fetch").mockImplementation(async (input) => {
      const url = String(input);
      if (url.includes("/api/v1/kata/daemons")) {
        return Response.json({
          daemons: [{ id: "home", url: "http://127.0.0.1:7777", default: true, auth: "none", health: "connected" }],
        });
      }
      if (url.includes("/api/v1/kata/proxy/api/v1/events/stream")) {
        return new Response(new ReadableStream({ start() {} }), {
          headers: { "Content-Type": "text/event-stream" },
        });
      }
      return Response.json({});
    });
    const parent = issue("issue-parent-slow", "Slow parent task", "project-kata");
    const child = {
      ...issue("issue-child-slow", "Child before parent", "project-kata"),
      parent: { uid: parent.uid, short_id: "parent-slow" },
      parent_short_id: "parent-slow",
    };
    const parentDetail = deferred<KataTaskDetail>();
    const { api, issue: issueDetail } = createWorkspaceAPI([child]);
    issueDetail.mockImplementation(async (uid: string) => {
      if (uid === child.uid) return detail(uid, [child]);
      if (uid === parent.uid) return parentDetail.promise;
      return detail(uid, [parent, child]);
    });
    saveKataWorkspaceState("home", {
      view: "all",
      filters: { scope: { kind: "all" }, status: "open", owner: "", label: "", query: "" },
      selectedIssueUID: child.uid,
    });

    render(KataWorkspace, { props: { api } });

    await waitFor(() => expect(screen.getByRole("heading", { name: "Child before parent" })).toBeTruthy());
    await waitFor(() => expect(api.events).toHaveBeenCalledWith({ after_id: 0, limit: 100 }, {}));
    await waitFor(() =>
      expect(
        vi
          .mocked(globalThis.fetch)
          .mock.calls.some(([input]) => String(input).includes("/api/v1/kata/proxy/api/v1/events/stream")),
      ).toBe(true),
    );
    expect(issueDetail.mock.calls.filter(([uid]) => uid === parent.uid)).toHaveLength(1);

    parentDetail.resolve(detail(parent.uid, [parent, child]));
  });

  it("drops an accepted routed ancestor walk when the route selection changes", async () => {
    vi.spyOn(globalThis, "fetch").mockImplementation(async () =>
      Response.json({
        daemons: [
          { id: "home", url: "http://127.0.0.1:7777", default: true, auth: "none", health: "connected" },
          { id: "work", url: "http://127.0.0.1:8888", default: false, auth: "none", health: "connected" },
        ],
      }),
    );
    setActiveKataDaemon("home");
    const grandparent = issue("issue-work-grandparent", "Work grandparent", "project-kata");
    const parent = {
      ...issue("issue-work-parent", "Work parent", "project-kata"),
      parent: { uid: grandparent.uid, short_id: grandparent.short_id },
      parent_short_id: grandparent.short_id,
    };
    const child = {
      ...issue("issue-work-child", "Work child", "project-kata"),
      parent: { uid: parent.uid, short_id: parent.short_id },
      parent_short_id: parent.short_id,
    };
    const api = createDaemonWorkspaceAPI({ home: [initialIssues[0]!], work: [child] });
    const parentDetail = deferred<KataTaskDetail>();
    vi.mocked(api.issue).mockImplementation(async (uid: string) => {
      const binding = vi.mocked(api.bindWorkflowDaemon!).mock.calls.at(-1)?.[0];
      if (uid === child.uid) return detail(child.uid, [child]);
      if (uid === parent.uid) return parentDetail.promise;
      if (uid === grandparent.uid && binding !== "work") throw new Error("ancestor crossed daemon boundary");
      return detail(uid, [grandparent, parent, child]);
    });
    saveKataWorkspaceState("work", {
      view: "all",
      filters: { scope: { kind: "all" }, status: "open", owner: "", label: "", query: "" },
      selectedIssueUID: child.uid,
    });

    const { component } = render(KataWorkspaceRouteHost, {
      props: { api, initialDaemon: "work" },
    });
    await waitFor(() => expect(screen.getByRole("heading", { name: "Work child" })).toBeTruthy());
    await waitFor(() => expect(api.issue).toHaveBeenCalledWith(parent.uid, { daemonId: "work" }));

    component.setRoute({ issue: null, daemon: null });
    await tick();
    expect(component.route().issue).toBeNull();
    parentDetail.resolve(detail(parent.uid, [grandparent, parent, child]));
    await Promise.resolve();
    await Promise.resolve();

    expect(getActiveKataDaemon()).toBe("work");
    expect(api.issue).not.toHaveBeenCalledWith(grandparent.uid);
    expect(screen.queryByText("ancestor crossed daemon boundary")).toBeNull();
  });

  it("reveals the ancestor chain for an accepted routed nested task", async () => {
    const parent = issue("issue-routed-parent", "Routed parent", "project-kata");
    const child = {
      ...issue("issue-routed-child", "Routed child", "project-kata"),
      parent: { uid: parent.uid, short_id: parent.short_id },
      parent_short_id: parent.short_id,
    };
    const { api, issue: issueDetail } = createWorkspaceAPI([parent, child]);
    issueDetail.mockImplementation(async (uid: string) => detail(uid, [parent, child]));

    render(KataWorkspaceRouteHost, { props: { api, initialIssue: child.uid } });

    await waitFor(() => expect(screen.getByRole("heading", { name: "Routed child" })).toBeTruthy());
    await waitFor(() =>
      expect(issueDetail).toHaveBeenCalledWith(parent.uid, expect.objectContaining({ daemonId: "home" })),
    );
    await waitFor(() => expect(screen.getByText("Routed child", { selector: ".title-text" })).toBeTruthy());
  });

  it("keeps an accepted task selected while retrying transient ancestor failures", async () => {
    const parent = issue("issue-parent-retry", "Parent retry task", "project-kata");
    const child = {
      ...issue("issue-child-retry", "Child retry task", "project-kata"),
      parent: { uid: parent.uid, short_id: "parent-retry" },
      parent_short_id: "parent-retry",
    };
    const { api, issue: issueDetail } = createWorkspaceAPI([child]);
    issueDetail.mockImplementation(async (uid: string) => {
      if (uid === child.uid) return detail(uid, [child]);
      if (uid === parent.uid) {
        const attempts = issueDetail.mock.calls.filter(([ref]) => ref === parent.uid).length;
        if (attempts === 1) throw new Error("ancestor timed out");
        if (attempts === 2) throw new Error("ancestor timed out again");
      }
      return detail(uid, [parent, child]);
    });
    saveKataWorkspaceState("home", {
      view: "all",
      filters: { scope: { kind: "all" }, status: "open", owner: "", label: "", query: "" },
      selectedIssueUID: child.uid,
    });

    const { component } = render(KataWorkspaceRouteHost, { props: { api } });

    await waitFor(() => expect(screen.getByRole("button", { name: "Retry" })).toBeTruthy());
    const layout = screen.getByLabelText("Kata tasks").parentElement as HTMLElement & {
      inert: boolean;
    };
    await waitForWorkspaceWritable();
    expect(layout.inert).toBe(false);
    expect(screen.getByRole("heading", { name: "Child retry task" })).toBeTruthy();
    expect(component.route().issue).toBe(child.uid);
    expect(loadKataWorkspaceState("home")?.selectedIssueUID).toBe(child.uid);

    await fireEvent.click(screen.getByRole("button", { name: "Retry" }));
    await waitFor(() => {
      expect(issueDetail.mock.calls.filter(([ref]) => ref === parent.uid)).toHaveLength(2);
      expect(screen.getByRole("button", { name: "Retry" })).toBeTruthy();
    });
    expect(screen.getByRole("heading", { name: "Child retry task" })).toBeTruthy();
    expect(component.route().issue).toBe(child.uid);

    const repeatedFailure = await screen.findByText("ancestor timed out again");
    expect(within(repeatedFailure).getByRole("button", { name: "Retry" })).toBeTruthy();
    expect(screen.getByRole("heading", { name: "Child retry task" })).toBeTruthy();
  });

  it("reconciles a stale configured daemon to the roster default", async () => {
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
    setActiveKataDaemon("removed");
    const { api } = createWorkspaceAPI();

    render(KataWorkspace, { props: { api } });

    await waitFor(() => expect(getActiveKataDaemon()).toBe("home"));
    expect(screen.getByTestId("daemon-chip").textContent).toContain("home");
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

  it("opens a reachable graph from the task list and keeps task selection active", async () => {
    const root = {
      ...issue("issue-root", "Root graph task", "project-kata"),
      priority: 0,
      blocks: [{ uid: "issue-blocked", short_id: "blocked" }],
    };
    const blocked = {
      ...issue("issue-blocked", "Blocked follow-up", "project-kata"),
      priority: 2,
    };
    const { api } = createWorkspaceAPI([root, blocked]);

    render(KataWorkspace, { props: { api } });

    await waitFor(() => {
      expect(screen.getByRole("button", { name: /Root graph task/ })).toBeTruthy();
    });
    const rootRow = screen.getByRole("button", { name: /Root graph task/ });
    const rootFrame = rootRow.parentElement;
    expect(rootFrame).toBeTruthy();
    await fireEvent.click(within(rootFrame!).getByRole("button", { name: "Open reachable graph" }));

    expect(screen.getByRole("region", { name: "Reachable task graph" })).toBeTruthy();
    expect(screen.queryByLabelText("Search tasks")).toBeNull();
    expect(screen.getAllByText("Root graph task").length).toBeGreaterThan(0);
    expect(screen.getAllByText("Blocked follow-up").length).toBeGreaterThan(0);
    expect(screen.getByText("P0")).toBeTruthy();

    await fireEvent.click(graphNodeWithText("Blocked follow-up"));
    await waitFor(() => {
      expect(screen.getByRole("heading", { name: "Blocked follow-up" })).toBeTruthy();
    });
    expect(api.issue).toHaveBeenCalledWith(
      "issue-blocked",
      expect.objectContaining({ signal: expect.any(AbortSignal) }),
    );

    await fireEvent.click(screen.getByRole("button", { name: "Back to task list" }));
    expect(screen.getByLabelText("Search tasks")).toBeTruthy();
    expect(screen.queryByRole("region", { name: "Reachable task graph" })).toBeNull();
  });

  it("keeps a graph node selection when no route callback is provided", async () => {
    const root = {
      ...issue("issue-root", "Root graph task", "project-kata"),
      blocks: [{ uid: "issue-blocked", short_id: "blocked" }],
    };
    const blocked = issue("issue-blocked", "Blocked follow-up", "project-kata");
    const { api } = createWorkspaceAPI([root, blocked]);

    render(KataWorkspace, { props: { api } });

    await waitFor(() => {
      expect(screen.getByRole("button", { name: /Root graph task/ })).toBeTruthy();
    });
    const rootRow = screen.getByRole("button", { name: /Root graph task/ });
    await fireEvent.click(within(rootRow.parentElement!).getByRole("button", { name: "Open reachable graph" }));
    await fireEvent.click(graphNodeWithText("Blocked follow-up"));

    await waitFor(() => {
      expect(screen.getByRole("heading", { name: "Blocked follow-up" })).toBeTruthy();
    });
    // Without a route callback the selectedIssueUID prop never updates. The
    // route-sync effect must not read that null prop as a route-driven
    // deselect and clear the selection right after it was made.
    await new Promise((resolve) => setTimeout(resolve, 20));
    expect(screen.getByRole("heading", { name: "Blocked follow-up" })).toBeTruthy();
  });

  it("opens a reachable graph from the task detail toolbar", async () => {
    const root = {
      ...issue("issue-root", "Root graph task", "project-kata"),
      related: [{ uid: "issue-related", short_id: "related" }],
    };
    const related = issue("issue-related", "Related graph task", "project-kata");
    const { api } = createWorkspaceAPI([root, related]);

    render(KataWorkspace, { props: { api, selectedIssueUID: root.uid } });

    await waitFor(() => {
      expect(screen.getByRole("heading", { name: "Root graph task" })).toBeTruthy();
    });
    await fireEvent.click(
      within(screen.getByRole("region", { name: "Task detail" })).getByRole("button", {
        name: "Open reachable graph",
      }),
    );

    expect(screen.getByRole("region", { name: "Reachable task graph" })).toBeTruthy();
    expect(screen.getAllByText("Related graph task").length).toBeGreaterThan(0);
  });

  it("updates the reachable graph direction with the task detail layout", async () => {
    const root = {
      ...issue("issue-root", "Root graph task", "project-kata"),
      blocks: [{ uid: "issue-blocked", short_id: "blocked" }],
    };
    const blocked = issue("issue-blocked", "Blocked graph task", "project-kata");
    const { api } = createWorkspaceAPI([root, blocked]);

    render(KataWorkspace, { props: { api, selectedIssueUID: root.uid } });

    await waitFor(() => {
      expect(screen.getByRole("heading", { name: "Root graph task" })).toBeTruthy();
    });
    await fireEvent.click(
      within(screen.getByRole("region", { name: "Task detail" })).getByRole("button", {
        name: "Open reachable graph",
      }),
    );

    const graph = screen.getByRole("region", { name: "Reachable task graph" });
    expect(graph.getAttribute("data-layout-direction")).toBe("TB");

    await fireEvent.click(screen.getByRole("button", { name: "Switch to side-by-side layout" }));
    expect(graph.getAttribute("data-layout-direction")).toBe("LR");

    await fireEvent.click(screen.getByRole("button", { name: "Switch to stacked layout" }));
    expect(graph.getAttribute("data-layout-direction")).toBe("TB");
  });

  it("keeps source detail links in the graph after selecting a linked node", async () => {
    const root = issue("issue-root", "Root graph task", "project-kata");
    const related = issue("issue-related", "Detail-only graph task", "project-kata");
    const { api } = createWorkspaceAPI([root, related]);
    const sourceLink: KataTaskLink = {
      id: 1,
      project_id: root.project_id,
      from: { uid: root.uid, short_id: root.short_id },
      to: { uid: related.uid, short_id: related.short_id },
      type: "related",
      author: "fixture-user",
      created_at: fetchedAt,
    };
    vi.mocked(api.issue).mockImplementation(async (uid: string) => {
      if (uid === root.uid) {
        const rootDetail = detail(root.uid, [root, related]);
        return { ...rootDetail, links: [sourceLink] };
      }
      return detail(uid, [root, related]);
    });

    render(KataWorkspace, { props: { api, selectedIssueUID: root.uid } });

    await waitFor(() => {
      expect(screen.getByRole("heading", { name: "Root graph task" })).toBeTruthy();
    });
    await fireEvent.click(
      within(screen.getByRole("region", { name: "Task detail" })).getByRole("button", {
        name: "Open reachable graph",
      }),
    );

    expect(screen.getAllByText("Detail-only graph task").length).toBeGreaterThan(0);
    await fireEvent.click(graphNodeWithText("Detail-only graph task"));

    await waitFor(() => {
      expect(screen.getByRole("heading", { name: "Detail-only graph task" })).toBeTruthy();
    });
    const graph = screen.getByRole("region", { name: "Reachable task graph" });
    expect(within(graph).getAllByText("Detail-only graph task").length).toBeGreaterThan(0);
  });

  it("uses the native graph endpoint when opening a graph from an unselected list row", async () => {
    const selected = issue("issue-selected", "Initially selected task", "project-kata");
    const root = issue("issue-root", "Root graph task", "project-kata");
    const related = issue("issue-related", "Detail-only graph task", "project-kata");
    const { api } = createWorkspaceAPI([selected, root, related]);
    vi.mocked(api.issue).mockImplementation(async (uid: string) => detail(uid, [selected, root, related]));

    render(KataWorkspace, { props: { api, selectedIssueUID: selected.uid } });

    await waitFor(() => {
      expect(screen.getByRole("heading", { name: "Initially selected task" })).toBeTruthy();
    });
    const rootRow = screen.getByRole("button", { name: /Root graph task/ });
    const rootFrame = rootRow.parentElement;
    expect(rootFrame).toBeTruthy();
    await fireEvent.click(within(rootFrame!).getByRole("button", { name: "Open reachable graph" }));

    expect(screen.getByRole("region", { name: "Reachable task graph" })).toBeTruthy();
    await waitFor(() => {
      expect(
        within(screen.getByRole("region", { name: "Reachable task graph" })).getAllByText("Detail-only graph task")
          .length,
      ).toBeGreaterThan(0);
    });
    expect(api.reachableGraph).toHaveBeenCalledWith(
      root.project_id,
      root.uid,
      { depth: "full", hide_done: false },
      expect.objectContaining({ signal: expect.any(AbortSignal) }),
    );
    expect(vi.mocked(api.issue).mock.calls.some(([uid]) => uid === root.uid)).toBe(false);

    await fireEvent.click(graphNodeWithText("Detail-only graph task"));

    await waitFor(() => {
      expect(screen.getByRole("heading", { name: "Detail-only graph task" })).toBeTruthy();
    });
    expect(
      within(screen.getByRole("region", { name: "Reachable task graph" })).getAllByText("Detail-only graph task")
        .length,
    ).toBeGreaterThan(0);
  });

  it("renders native graph nodes that are not cached locally", async () => {
    const root = {
      ...issue("issue-root", "Root graph task", "project-kata"),
      blocks: [{ uid: "issue-linked", short_id: "linked" }],
    };
    const linked = {
      ...issue("issue-linked", "Fetched linked task", "project-kata"),
      priority: 1,
    };
    const { api } = createWorkspaceAPI([root]);
    vi.mocked(api.issue).mockImplementation(async (uid: string) => detail(uid, [root, linked]));
    vi.mocked(api.reachableGraph).mockImplementation(async (_projectID, _ref, query = {}) => ({
      source_uid: root.uid,
      depth: query.depth ?? "full",
      hide_done: query.hide_done === true,
      nodes: [root, linked],
      edges: [{ from_uid: root.uid, to_uid: linked.uid, kind: "blocks", layout: true }],
      unresolved_refs: [],
      fetched_at: fetchedAt,
    }));

    render(KataWorkspace, { props: { api, selectedIssueUID: root.uid } });

    await waitFor(() => {
      expect(screen.getByRole("heading", { name: "Root graph task" })).toBeTruthy();
    });
    await fireEvent.click(
      within(screen.getByRole("region", { name: "Task detail" })).getByRole("button", {
        name: "Open reachable graph",
      }),
    );

    await waitFor(() => {
      expect(graphNodeButtonWithText("Fetched linked task").disabled).toBe(false);
    });
    expect(vi.mocked(api.issue).mock.calls.some(([uid]) => uid === linked.uid)).toBe(false);

    await waitFor(() => {
      expect(graphNodeButtonWithText("Fetched linked task").disabled).toBe(false);
    });
    await fireEvent.click(graphNodeWithText("Fetched linked task"));
    await waitFor(() => {
      expect(screen.getByRole("heading", { name: "Fetched linked task" })).toBeTruthy();
    });
  });

  it("selects a graph node immediately and routes the selection", async () => {
    const root = {
      ...issue("issue-root", "Root graph task", "project-kata"),
      blocks: [{ uid: "issue-blocked", short_id: "blocked" }],
    };
    const blocked = issue("issue-blocked", "Blocked follow-up", "project-kata");
    const { api } = createWorkspaceAPI([root, blocked]);
    const onSelectedIssueChange = vi.fn();

    render(KataWorkspaceRouteHost, {
      props: {
        api,
        initialIssue: root.uid,
        onSelectedIssueChange,
      },
    });

    await waitFor(() => {
      expect(screen.getByRole("heading", { name: "Root graph task" })).toBeTruthy();
    });
    await fireEvent.click(
      within(screen.getByRole("region", { name: "Task detail" })).getByRole("button", {
        name: "Open reachable graph",
      }),
    );

    await fireEvent.click(graphNodeWithText("Blocked follow-up"));
    await waitFor(() => {
      expect(onSelectedIssueChange).toHaveBeenCalledWith("issue-blocked");
      expect(screen.getByRole("heading", { name: "Blocked follow-up" })).toBeTruthy();
    });
    expect(screen.getByRole("region", { name: "Reachable task graph" })).toBeTruthy();
  });

  it("routes and starts loading graph node selections before the task detail resolves", async () => {
    const root = {
      ...issue("issue-root", "Root graph task", "project-kata"),
      blocks: [{ uid: "issue-blocked", short_id: "blocked" }],
    };
    const blocked = issue("issue-blocked", "Blocked follow-up", "project-kata");
    const { api } = createWorkspaceAPI([root, blocked]);
    const blockedDetail = deferred<KataTaskDetail>();
    vi.mocked(api.issue).mockImplementation(async (uid: string) => {
      if (uid === blocked.uid) return blockedDetail.promise;
      return detail(uid, [root, blocked]);
    });
    const onSelectedIssueChange = vi.fn();

    render(KataWorkspaceRouteHost, {
      props: {
        api,
        initialIssue: root.uid,
        onSelectedIssueChange,
      },
    });

    await waitFor(() => {
      expect(screen.getByRole("heading", { name: "Root graph task" })).toBeTruthy();
    });
    await fireEvent.click(
      within(screen.getByRole("region", { name: "Task detail" })).getByRole("button", {
        name: "Open reachable graph",
      }),
    );

    await fireEvent.click(graphNodeWithText("Blocked follow-up"));

    expect(onSelectedIssueChange).toHaveBeenCalledWith(blocked.uid);
    await waitFor(() => {
      expect(screen.getByText("Loading task")).toBeTruthy();
    });
    expect(api.issue).toHaveBeenCalledTimes(2);
    expect(api.issue).toHaveBeenLastCalledWith(
      blocked.uid,
      expect.objectContaining({ signal: expect.any(AbortSignal) }),
    );

    blockedDetail.resolve(detail(blocked.uid, [root, blocked]));
    await waitFor(() => {
      expect(screen.getByRole("heading", { name: "Blocked follow-up" })).toBeTruthy();
    });
  });

  it("clears stale detail when a routed graph node selection fails", async () => {
    const root = {
      ...issue("issue-root", "Root graph task", "project-kata"),
      blocks: [{ uid: "issue-blocked", short_id: "blocked" }],
    };
    const blocked = issue("issue-blocked", "Blocked follow-up", "project-kata");
    const { api } = createWorkspaceAPI([root, blocked]);
    vi.mocked(api.issue).mockImplementation(async (uid: string) => {
      if (uid === blocked.uid) {
        throw new KataTaskAPIError({
          status: 500,
          code: "internal",
          message: "detail failed",
          headers: new Headers(),
        });
      }
      return detail(uid, [root, blocked]);
    });
    const onSelectedIssueChange = vi.fn();

    render(KataWorkspaceRouteHost, {
      props: {
        api,
        initialIssue: root.uid,
        onSelectedIssueChange,
      },
    });

    await waitFor(() => {
      expect(screen.getByRole("heading", { name: "Root graph task" })).toBeTruthy();
    });
    await fireEvent.click(
      within(screen.getByRole("region", { name: "Task detail" })).getByRole("button", {
        name: "Open reachable graph",
      }),
    );

    await fireEvent.click(graphNodeWithText("Blocked follow-up"));

    await waitFor(() => {
      expect(screen.getByText("detail failed").getAttribute("role")).toBe("alert");
      expect(screen.getByText("Select a task")).toBeTruthy();
    });
    expect(screen.getByText("detail failed").closest(".daemon-fallback-status")).toBeNull();
    expect(screen.queryByRole("heading", { name: "Root graph task" })).toBeNull();
    expect(screen.queryByRole("heading", { name: "Blocked follow-up" })).toBeNull();
  });

  it("clears stale graph selection errors when the route deselects the task", async () => {
    const root = {
      ...issue("issue-root", "Root graph task", "project-kata"),
      blocks: [{ uid: "issue-blocked", short_id: "blocked" }],
    };
    const blocked = issue("issue-blocked", "Blocked follow-up", "project-kata");
    const { api } = createWorkspaceAPI([root, blocked]);
    vi.mocked(api.issue).mockImplementation(async (uid: string) => {
      if (uid === blocked.uid) {
        throw new KataTaskAPIError({
          status: 500,
          code: "internal",
          message: "detail failed",
          headers: new Headers(),
        });
      }
      return detail(uid, [root, blocked]);
    });
    const onSelectedIssueChange = vi.fn();

    const { rerender } = render(KataWorkspace, {
      props: {
        api,
        selectedIssueUID: root.uid,
        onSelectedIssueChange,
      },
    });

    await waitFor(() => {
      expect(screen.getByRole("heading", { name: "Root graph task" })).toBeTruthy();
    });
    await fireEvent.click(
      within(screen.getByRole("region", { name: "Task detail" })).getByRole("button", {
        name: "Open reachable graph",
      }),
    );

    await fireEvent.click(graphNodeWithText("Blocked follow-up"));
    await rerender({ api, selectedIssueUID: blocked.uid, onSelectedIssueChange });

    await waitFor(() => {
      expect(screen.getByText("detail failed").getAttribute("role")).toBe("alert");
    });

    await rerender({ api, selectedIssueUID: null, onSelectedIssueChange });

    await waitFor(() => {
      expect(screen.queryByText("detail failed")).toBeNull();
      expect(screen.getByText("Select a task")).toBeTruthy();
    });
  });

  it("lets route back cancel a pending graph node selection", async () => {
    const root = {
      ...issue("issue-root", "Root graph task", "project-kata"),
      blocks: [{ uid: "issue-blocked", short_id: "blocked" }],
    };
    const blocked = issue("issue-blocked", "Blocked follow-up", "project-kata");
    const { api } = createWorkspaceAPI([root, blocked]);
    const blockedDetail = deferred<KataTaskDetail>();
    vi.mocked(api.issue).mockImplementation(async (uid: string) => {
      if (uid === blocked.uid) return blockedDetail.promise;
      return detail(uid, [root, blocked]);
    });
    const onSelectedIssueChange = vi.fn();

    const { component } = render(KataWorkspaceRouteHost, {
      props: {
        api,
        initialIssue: root.uid,
        onSelectedIssueChange,
      },
    });

    await waitFor(() => {
      expect(screen.getByRole("heading", { name: "Root graph task" })).toBeTruthy();
    });
    await fireEvent.click(
      within(screen.getByRole("region", { name: "Task detail" })).getByRole("button", {
        name: "Open reachable graph",
      }),
    );
    await fireEvent.click(graphNodeWithText("Blocked follow-up"));

    await waitFor(() => {
      expect(screen.getByText("Loading task")).toBeTruthy();
    });

    component.setRoute({ issue: root.uid });

    await waitFor(() => {
      expect(screen.getByRole("heading", { name: "Root graph task" })).toBeTruthy();
    });
    expect(screen.queryByText("Loading task")).toBeNull();

    blockedDetail.resolve(detail(blocked.uid, [root, blocked]));
    await Promise.resolve();

    expect(screen.getByRole("heading", { name: "Root graph task" })).toBeTruthy();
    expect(screen.queryByRole("heading", { name: "Blocked follow-up" })).toBeNull();
    expect(screen.getByRole("region", { name: "Reachable task graph" })).toBeTruthy();
  });

  it("closes the reachable graph when the routed view or scope changes", async () => {
    const root = {
      ...issue("issue-root", "Root graph task", "project-kata"),
      blocks: [{ uid: "issue-blocked", short_id: "blocked" }],
    };
    const blocked = issue("issue-blocked", "Blocked follow-up", "project-kata");
    const { api } = createWorkspaceAPI([root, blocked]);
    const { rerender } = render(KataWorkspace, {
      props: {
        api,
        routeViewName: null,
        routeScopeUID: null,
        selectedIssueUID: root.uid,
      },
    });

    await waitFor(() => {
      expect(screen.getByRole("heading", { name: "Root graph task" })).toBeTruthy();
    });
    await fireEvent.click(
      within(screen.getByRole("region", { name: "Task detail" })).getByRole("button", {
        name: "Open reachable graph",
      }),
    );
    expect(screen.getByRole("region", { name: "Reachable task graph" })).toBeTruthy();

    await rerender({
      api,
      routeViewName: "inbox",
      routeScopeUID: null,
      selectedIssueUID: null,
    });

    await waitFor(() => {
      expect(screen.queryByRole("region", { name: "Reachable task graph" })).toBeNull();
      expect(screen.getByLabelText("Search tasks")).toBeTruthy();
    });
  });

  it("reloads All Open from an inert invalid-persistence workspace", async () => {
    const { api, issues } = createWorkspaceAPI();
    saveKataWorkspaceState("home", {
      view: "all",
      filters: {
        scope: { kind: "project", project_uid: "project-missing" },
        status: "all",
        owner: "Stale",
        label: "stale",
        query: "discard",
      },
      selectedIssueUID: "issue-stale",
    });

    render(KataWorkspaceRouteHost, { props: { api } });

    await waitFor(() => expect(screen.getByRole("heading", { name: "All Open" })).toBeTruthy());
    expect(issues).not.toHaveBeenCalled();

    await fireEvent.click(screen.getByRole("button", { name: "All Open" }));

    await waitFor(() =>
      expect(issues).toHaveBeenCalledWith(
        expect.objectContaining({ view: "all" }),
        expect.objectContaining({ signal: expect.any(AbortSignal) }),
      ),
    );
    expect(screen.getByRole("button", { name: /Pay rent/ })).toBeTruthy();
  });

  it("does not reload an already active system view", async () => {
    const { api, issues } = createWorkspaceAPI();
    const onRouteStateChange = vi.fn();

    render(KataWorkspaceRouteHost, { props: { api, onRouteStateChange } });

    await waitFor(() => expect(screen.getByRole("heading", { name: "All Open" })).toBeTruthy());
    const issueLoads = issues.mock.calls.length;
    onRouteStateChange.mockClear();

    await fireEvent.click(screen.getByRole("button", { name: "All Open" }));

    expect(issues).toHaveBeenCalledTimes(issueLoads);
    expect(onRouteStateChange).not.toHaveBeenCalled();
  });

  it("clears an active selection without reloading the current system view", async () => {
    const { api, issues } = createWorkspaceAPI();
    const onRouteStateChange = vi.fn();

    render(KataWorkspaceRouteHost, {
      props: {
        api,
        initialIssue: "issue-pay-rent",
        onRouteStateChange,
      },
    });

    await waitFor(() => expect(screen.getByRole("heading", { name: "Pay rent" })).toBeTruthy());
    const issueLoads = issues.mock.calls.length;
    onRouteStateChange.mockClear();

    await fireEvent.click(screen.getByRole("button", { name: "All Open" }));

    await waitFor(() => expect(screen.getByText("Select a task")).toBeTruthy());
    expect(issues).toHaveBeenCalledTimes(issueLoads);
    expect(onRouteStateChange).toHaveBeenCalledWith({ view: "all", scope: null, issue: null });
  });

  it("closes the reachable graph without reloading the current system view", async () => {
    const root = {
      ...issue("issue-root", "Root graph task", "project-kata"),
      blocks: [{ uid: "issue-blocked", short_id: "blocked" }],
    };
    const blocked = issue("issue-blocked", "Blocked follow-up", "project-kata");
    const { api, issues } = createWorkspaceAPI([root, blocked]);
    const onRouteStateChange = vi.fn();

    render(KataWorkspaceRouteHost, { props: { api, onRouteStateChange } });

    await waitFor(() => expect(screen.getByRole("button", { name: /Root graph task/ })).toBeTruthy());
    const rootRow = screen.getByRole("button", { name: /Root graph task/ });
    await fireEvent.click(within(rootRow.parentElement!).getByRole("button", { name: "Open reachable graph" }));
    expect(screen.getByRole("region", { name: "Reachable task graph" })).toBeTruthy();
    const issueLoads = issues.mock.calls.length;
    onRouteStateChange.mockClear();

    await fireEvent.click(screen.getByRole("button", { name: "All Open" }));

    expect(screen.queryByRole("region", { name: "Reachable task graph" })).toBeNull();
    expect(screen.getByLabelText("Search tasks")).toBeTruthy();
    expect(issues).toHaveBeenCalledTimes(issueLoads);
    expect(onRouteStateChange).not.toHaveBeenCalled();
  });

  it("selects the current route while a superseded view load is still pending", async () => {
    const target = issue("issue-target", "Current route task", "project-kata");
    const { api, issues } = createWorkspaceAPI([target]);
    const stalledInbox = deferred<Awaited<ReturnType<typeof api.issues>>>();
    vi.mocked(api.issues).mockImplementation(async (query) => {
      if (query.view === "inbox") return stalledInbox.promise;
      return {
        view: query.view,
        groups: [{ id: "all", title: "All", issues: [target] }],
        fetched_at: fetchedAt,
      };
    });

    const { component } = render(KataWorkspaceRouteHost, { props: { api } });

    await waitFor(() => expect(screen.getByRole("heading", { name: "All Open" })).toBeTruthy());
    component.setRoute({ view: "inbox", issue: null });
    await waitFor(() =>
      expect(issues).toHaveBeenCalledWith(
        expect.objectContaining({ view: "inbox" }),
        expect.objectContaining({ signal: expect.any(AbortSignal) }),
      ),
    );

    component.setRoute({ view: "all", issue: target.uid });

    await waitFor(() => expect(screen.getByRole("heading", { name: "Current route task" })).toBeTruthy());
    expect(api.issue).toHaveBeenCalledWith(target.uid, expect.objectContaining({ signal: expect.any(AbortSignal) }));

    stalledInbox.resolve({
      view: "inbox",
      groups: [{ id: "inbox", title: "Inbox", issues: [] }],
      fetched_at: fetchedAt,
    });
  });

  it("resets active filters when reopening the current system view", async () => {
    const { api, issues } = createWorkspaceAPI();
    const onRouteStateChange = vi.fn();

    render(KataWorkspaceRouteHost, { props: { api, onRouteStateChange } });

    await waitFor(() => expect(screen.getByRole("heading", { name: "All Open" })).toBeTruthy());
    await fireEvent.input(screen.getByLabelText("Search tasks"), { target: { value: "rent" } });
    await waitFor(() => expect((screen.getByLabelText("Search tasks") as HTMLInputElement).value).toBe("rent"));
    const issueLoads = issues.mock.calls.length;
    onRouteStateChange.mockClear();

    await fireEvent.click(screen.getByRole("button", { name: "All Open" }));

    await waitFor(() => expect((screen.getByLabelText("Search tasks") as HTMLInputElement).value).toBe(""));
    expect(issues.mock.calls.length).toBeGreaterThan(issueLoads);
    expect(onRouteStateChange).not.toHaveBeenCalled();
  });

  it("opens system views without auto-selecting the first task", async () => {
    const rows = initialIssues.map((item) => (item.uid === "issue-pay-rent" ? { ...item, project_name: "" } : item));
    const { api } = createWorkspaceAPI(rows);
    const onRouteStateChange = vi.fn();

    render(KataWorkspaceRouteHost, { props: { api, onRouteStateChange } });

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

  it("preserves accepted workspace state after a failed system-view request", async () => {
    acceptHomeDaemon();
    const selected = initialIssues[0]!;
    const persisted = {
      view: "all" as const,
      filters: { scope: { kind: "all" as const }, status: "open" as const, owner: "", label: "", query: "" },
      selectedIssueUID: selected.uid,
    };
    const { api, issues } = createWorkspaceAPI();
    saveKataWorkspaceState("home", persisted);
    const onRouteStateChange = vi.fn();

    render(KataWorkspace, { props: { api, selectedIssueUID: selected.uid, onRouteStateChange } });
    await waitFor(() => expect(screen.getByRole("heading", { name: "Pay rent" })).toBeTruthy());
    issues.mockRejectedValueOnce(new Error("inbox unavailable"));
    onRouteStateChange.mockClear();

    await fireEvent.click(screen.getByRole("button", { name: /^Inbox\s+1$/ }));

    await waitFor(() => expect(screen.getByText("inbox unavailable")).toBeTruthy());
    expect(screen.getByRole("heading", { name: "All Open" })).toBeTruthy();
    expect(screen.getByRole("heading", { name: "Pay rent" })).toBeTruthy();
    expect(onRouteStateChange).not.toHaveBeenCalled();
    expect(loadKataWorkspaceState("home")).toEqual(persisted);
  });

  it("persists an accepted filter change after deliberately clearing an incompatible selection", async () => {
    const selected = initialIssues[0]!;
    acceptHomeDaemon();
    const { api, search } = createWorkspaceAPI();
    search.mockImplementation(async (filters: KataTaskSearchFilters) => ({
      filters,
      issues: filters.status === "closed" ? [] : initialIssues,
      fetched_at: fetchedAt,
    }));

    render(KataWorkspace, { props: { api, selectedIssueUID: selected.uid } });

    await waitFor(() => expect(screen.getByRole("heading", { name: "Pay rent" })).toBeTruthy());
    await fireEvent.click(screen.getByRole("combobox", { name: "Status: Open" }));
    await fireEvent.click(screen.getByRole("option", { name: "Closed" }));

    await waitFor(() =>
      expect(loadKataWorkspaceState("home")).toEqual({
        view: "all",
        filters: { scope: { kind: "all" }, status: "closed", owner: "", label: "", query: "" },
        selectedIssueUID: null,
      }),
    );
  });

  it.each([
    {
      name: "system-view navigation",
      select: () => fireEvent.click(screen.getByRole("button", { name: /^Inbox\s+1$/ })),
      expected: {
        view: "inbox" as const,
        filters: { scope: { kind: "all" as const }, status: "open" as const, owner: "", label: "", query: "" },
        selectedIssueUID: null,
      },
    },
    {
      name: "project-scope navigation",
      select: () =>
        fireEvent.click(
          within(screen.getByRole("complementary", { name: "Kata navigation" })).getByRole("button", {
            name: /^Kata\s+1$/,
          }),
        ),
      expected: {
        view: "all" as const,
        filters: {
          scope: { kind: "project" as const, project_uid: "project-kata" },
          status: "open" as const,
          owner: "",
          label: "",
          query: "",
        },
        selectedIssueUID: "issue-email-susan",
      },
    },
  ])("persists accepted $name", async ({ select, expected }) => {
    acceptHomeDaemon();
    const { api } = createWorkspaceAPI();

    render(KataWorkspace, { props: { api, selectedIssueUID: "issue-pay-rent" } });

    await waitFor(() => expect(screen.getByRole("heading", { name: "Pay rent" })).toBeTruthy());
    await select();

    await waitFor(() => expect(loadKataWorkspaceState("home")).toEqual(expected));
  });

  it("persists a completed manual list selection", async () => {
    acceptHomeDaemon();
    const { api } = createWorkspaceAPI();

    render(KataWorkspace, { props: { api } });

    await waitFor(() => expect(screen.getByRole("button", { name: /Email Susan re: Q3/ })).toBeTruthy());
    await fireEvent.click(screen.getByRole("button", { name: /Email Susan re: Q3/ }));

    await waitFor(() =>
      expect(loadKataWorkspaceState("home")).toEqual({
        view: "all",
        filters: { scope: { kind: "all" }, status: "open", owner: "", label: "", query: "" },
        selectedIssueUID: "issue-email-susan",
      }),
    );
  });

  it("persists a completed graph task-node selection through route reconciliation", async () => {
    acceptHomeDaemon();
    const root = {
      ...issue("issue-root", "Root graph task", "project-kata"),
      blocks: [{ uid: "issue-blocked", short_id: "blocked" }],
    };
    const blocked = issue("issue-blocked", "Blocked follow-up", "project-kata");
    const { api } = createWorkspaceAPI([root, blocked]);

    const { component } = render(KataWorkspaceRouteHost, { props: { api, initialIssue: root.uid } });

    await waitFor(() => expect(screen.getByRole("heading", { name: "Root graph task" })).toBeTruthy());
    await fireEvent.click(
      within(screen.getByRole("region", { name: "Task detail" })).getByRole("button", {
        name: "Open reachable graph",
      }),
    );
    await fireEvent.click(graphNodeWithText("Blocked follow-up"));

    await waitFor(() =>
      expect(loadKataWorkspaceState("home")).toEqual({
        view: "all",
        filters: { scope: { kind: "all" }, status: "open", owner: "", label: "", query: "" },
        selectedIssueUID: blocked.uid,
      }),
    );
    expect(component.route().issue).toBe(blocked.uid);
  });

  it.each([
    {
      name: "move",
      expectedSelection: "issue-pay-rent",
      configure: (
        api: ReturnType<typeof createDaemonWorkspaceAPI>,
        replaceSelected: (changes: Record<string, unknown>) => void,
      ) => {
        api.moveIssue = vi.fn(async () => {
          replaceSelected({ project_id: projects[2]!.id, project_uid: "project-kata", project_name: "Kata" });
          return { changed: true, new_short_id: "moved" };
        });
      },
      perform: async () => {
        await fireEvent.click(screen.getByRole("button", { name: "More actions" }));
        await fireEvent.click(screen.getByRole("menuitem", { name: "Move to another project" }));
        await fireEvent.input(screen.getByRole("searchbox", { name: "Find project" }), {
          target: { value: "Kata" },
        });
        await fireEvent.click(
          within(screen.getByRole("dialog", { name: "Move to another project" })).getByRole("button", {
            name: /^Kata\s+1$/,
          }),
        );
      },
      accepted: (api: ReturnType<typeof createDaemonWorkspaceAPI>) => expect(api.moveIssue).toHaveBeenCalledTimes(1),
    },
    {
      name: "close",
      expectedSelection: null,
      configure: (
        api: ReturnType<typeof createDaemonWorkspaceAPI>,
        replaceSelected: (changes: Record<string, unknown>) => void,
      ) => {
        api.closeIssue = vi.fn(async () => {
          replaceSelected({ status: "closed", closed_at: fetchedAt });
          return { changed: true };
        });
      },
      perform: async () => {
        await fireEvent.click(screen.getByRole("button", { name: "Complete" }));
        await fireEvent.click(
          within(screen.getByRole("dialog", { name: "Complete task" })).getByRole("button", { name: "Complete" }),
        );
      },
      accepted: (api: ReturnType<typeof createDaemonWorkspaceAPI>) => expect(api.closeIssue).toHaveBeenCalledTimes(1),
    },
    {
      name: "delete",
      expectedSelection: null,
      configure: (
        api: ReturnType<typeof createDaemonWorkspaceAPI>,
        replaceSelected: (changes: Record<string, unknown>) => void,
      ) => {
        api.closeIssue = vi.fn(async () => {
          replaceSelected({ status: "closed", closed_at: fetchedAt });
          return { changed: true };
        });
      },
      perform: async () => {
        await fireEvent.click(screen.getByRole("button", { name: "More actions" }));
        await fireEvent.click(
          within(screen.getByRole("menu", { name: "Task actions" })).getByRole("menuitem", { name: "Delete issue" }),
        );
        await fireEvent.click(
          within(screen.getByRole("dialog", { name: "Delete issue" })).getByRole("button", { name: "Delete" }),
        );
      },
      accepted: (api: ReturnType<typeof createDaemonWorkspaceAPI>) => expect(api.closeIssue).toHaveBeenCalledTimes(1),
    },
    {
      name: "reopen",
      initial: { ...initialIssues[0]!, status: "closed" as const, closed_at: fetchedAt },
      view: "logbook" as const,
      expectedSelection: null,
      configure: (
        api: ReturnType<typeof createDaemonWorkspaceAPI>,
        replaceSelected: (changes: Record<string, unknown>) => void,
      ) => {
        api.reopenIssue = vi.fn(async () => {
          replaceSelected({ status: "open", closed_at: undefined, closed_reason: undefined });
          return { changed: true };
        });
      },
      perform: async () => {
        await fireEvent.click(screen.getByRole("button", { name: "Reopen" }));
      },
      accepted: (api: ReturnType<typeof createDaemonWorkspaceAPI>) => expect(api.reopenIssue).toHaveBeenCalledTimes(1),
    },
    {
      name: "edit",
      expectedSelection: "issue-pay-rent",
      expectedTitle: "Pay rent now",
      configure: (
        api: ReturnType<typeof createDaemonWorkspaceAPI>,
        replaceSelected: (changes: Record<string, unknown>) => void,
      ) => {
        api.editIssue = vi.fn(async (_target, _actor, patch) => {
          replaceSelected(patch);
          return { changed: true };
        });
      },
      perform: async () => {
        await fireEvent.click(screen.getByRole("button", { name: "Edit title" }));
        const input = screen.getByRole("textbox", { name: "Edit title" });
        await fireEvent.input(input, { target: { value: "Pay rent now" } });
        await fireEvent.keyDown(input, { key: "Enter" });
      },
      accepted: (api: ReturnType<typeof createDaemonWorkspaceAPI>) => expect(api.editIssue).toHaveBeenCalledTimes(1),
    },
    {
      name: "assign owner",
      expectedSelection: "issue-pay-rent",
      configure: () => {},
      perform: async () => {
        await fireEvent.click(screen.getByRole("button", { name: "Owner: fixture-user" }));
        const owner = screen.getByRole("combobox", { name: "Owner" });
        await fireEvent.input(owner, { target: { value: "agent:new" } });
        await fireEvent.keyDown(owner, { key: "Enter" });
      },
      accepted: (api: ReturnType<typeof createDaemonWorkspaceAPI>) => expect(api.assignOwner).toHaveBeenCalledTimes(1),
    },
    {
      name: "unassign owner",
      expectedSelection: "issue-pay-rent",
      configure: () => {},
      perform: async () => {
        await fireEvent.click(screen.getByRole("button", { name: "Owner: fixture-user" }));
        const owner = screen.getByRole("combobox", { name: "Owner" });
        await fireEvent.keyDown(owner, { key: "ArrowUp" });
        await fireEvent.keyDown(owner, { key: "Enter" });
      },
      accepted: (api: ReturnType<typeof createDaemonWorkspaceAPI>) =>
        expect(api.unassignOwner).toHaveBeenCalledTimes(1),
    },
    {
      name: "add label",
      expectedSelection: "issue-pay-rent",
      configure: () => {},
      perform: async () => {
        await fireEvent.click(screen.getByRole("button", { name: "Add label" }));
        const label = screen.getByRole("textbox", { name: "New label" });
        await fireEvent.input(label, { target: { value: "urgent" } });
        await fireEvent.keyDown(label, { key: "Enter" });
      },
      accepted: (api: ReturnType<typeof createDaemonWorkspaceAPI>) => expect(api.addLabel).toHaveBeenCalledTimes(1),
    },
    {
      name: "remove label",
      expectedSelection: "issue-pay-rent",
      configure: () => {},
      perform: async () => {
        await fireEvent.click(screen.getByRole("button", { name: "Edit labels" }));
        await fireEvent.click(screen.getByRole("button", { name: "Remove label home" }));
      },
      accepted: (api: ReturnType<typeof createDaemonWorkspaceAPI>) => expect(api.removeLabel).toHaveBeenCalledTimes(1),
    },
    {
      name: "set priority",
      expectedSelection: "issue-pay-rent",
      configure: () => {},
      perform: async () => {
        await fireEvent.click(screen.getByRole("button", { name: "Edit priority" }));
        await fireEvent.click(screen.getByRole("combobox", { name: "Priority: No priority" }));
        await fireEvent.click(screen.getByRole("option", { name: "P0" }));
      },
      accepted: (api: ReturnType<typeof createDaemonWorkspaceAPI>) => expect(api.setPriority).toHaveBeenCalledTimes(1),
    },
    {
      name: "metadata patch",
      expectedSelection: "issue-pay-rent",
      configure: () => {},
      perform: async () => {
        await fireEvent.click(screen.getByRole("button", { name: "More actions" }));
        await fireEvent.click(
          within(screen.getByRole("menu", { name: "Task actions" })).getByRole("menuitem", { name: "Add checklist" }),
        );
        const checklist = screen.getByRole("textbox", { name: "New checklist item" });
        await fireEvent.input(checklist, { target: { value: "Confirm amount" } });
        await fireEvent.keyDown(checklist, { key: "Enter" });
      },
      accepted: (api: ReturnType<typeof createDaemonWorkspaceAPI>) =>
        expect(api.patchIssueMetadata).toHaveBeenCalledTimes(1),
    },
  ])(
    "reconciles and persists an accepted selected-task $name mutation",
    async ({
      initial = initialIssues[0]!,
      view = "all" as KataTaskViewName,
      expectedSelection,
      expectedTitle = initial.title,
      configure,
      perform,
      accepted,
    }) => {
      acceptHomeDaemon();
      const rowsByDaemon = { home: [{ ...initial }] };
      const api = createDaemonWorkspaceAPI(rowsByDaemon);
      const replaceSelected = (changes: Record<string, unknown>): void => {
        rowsByDaemon.home = rowsByDaemon.home.map((row) => ({ ...row, ...changes }));
      };
      configure(api, replaceSelected);

      render(KataWorkspaceRouteHost, { props: { api, initialView: view, initialIssue: initial.uid } });
      await waitFor(() => expect(screen.getByRole("heading", { name: initial.title })).toBeTruthy());
      await waitFor(() =>
        expect((screen.getByRole("button", { name: "New task" }) as HTMLButtonElement).disabled).toBe(false),
      );
      saveKataWorkspaceState("home", {
        view,
        filters: { scope: { kind: "all" }, status: "open", owner: "", label: "", query: "" },
        selectedIssueUID: initial.uid,
      });
      await perform();

      await waitFor(() => accepted(api));
      await waitFor(() => expect(loadKataWorkspaceState("home")?.selectedIssueUID).toBe(expectedSelection));
      expect(loadKataWorkspaceState("home")?.filters).toEqual({
        scope: { kind: "all" },
        status: "open",
        owner: "",
        label: "",
        query: "",
      });
      if (expectedSelection === null) {
        expect(screen.getByText("Select a task")).toBeTruthy();
      } else {
        await waitFor(() => expect(screen.getByRole("heading", { name: expectedTitle })).toBeTruthy());
      }
    },
  );

  it("persists quick capture after Inbox opens and the new task is selected", async () => {
    acceptHomeDaemon();
    const { api } = createWorkspaceAPI();

    render(KataWorkspace, { props: { api } });
    await waitFor(() => expect(screen.getByRole("heading", { name: "All Open" })).toBeTruthy());
    await waitFor(() =>
      expect((screen.getByRole("button", { name: "New task" }) as HTMLButtonElement).disabled).toBe(false),
    );

    await fireEvent.click(screen.getByRole("button", { name: "New task" }));
    const dialog = screen.getByRole("dialog", { name: "New task" });
    await fireEvent.input(within(dialog).getByRole("textbox", { name: "Quick capture" }), {
      target: { value: "Persist captured task" },
    });
    await fireEvent.click(within(dialog).getByRole("button", { name: "Capture" }));

    await waitFor(() =>
      expect(loadKataWorkspaceState("home")).toEqual({
        view: "inbox",
        filters: { scope: { kind: "all" }, status: "open", owner: "", label: "", query: "" },
        selectedIssueUID: "issue-capture",
      }),
    );
  });

  it("does not persist graph display changes without a task selection", async () => {
    acceptHomeDaemon();
    const persisted = {
      view: "inbox" as const,
      filters: { scope: { kind: "all" as const }, status: "open" as const, owner: "", label: "", query: "" },
      selectedIssueUID: null,
    };
    const root = issue("issue-root", "Root graph task", "project-kata");
    const { api } = createWorkspaceAPI([root]);
    saveKataWorkspaceState("home", persisted);

    render(KataWorkspace, { props: { api, selectedIssueUID: root.uid } });
    await waitFor(() => expect(screen.getByRole("heading", { name: "Root graph task" })).toBeTruthy());

    await fireEvent.click(
      within(screen.getByRole("region", { name: "Task detail" })).getByRole("button", {
        name: "Open reachable graph",
      }),
    );
    await waitFor(() => expect(screen.getByRole("button", { name: "Back to task list" })).toBeTruthy());

    expect(loadKataWorkspaceState("home")).toEqual(persisted);
  });

  it("does not persist a failed filter request", async () => {
    acceptHomeDaemon();
    const persisted = {
      view: "all" as const,
      filters: { scope: { kind: "all" as const }, status: "open" as const, owner: "", label: "", query: "" },
      selectedIssueUID: null,
    };
    const { api, search } = createWorkspaceAPI();
    search.mockRejectedValueOnce(new Error("search failed"));
    saveKataWorkspaceState("home", persisted);

    render(KataWorkspace, { props: { api } });
    await waitFor(() => expect(screen.getByRole("heading", { name: "All Open" })).toBeTruthy());

    await fireEvent.input(screen.getByLabelText("Search tasks"), { target: { value: "failed" } });

    await waitFor(() => expect(screen.getByText("search failed")).toBeTruthy());
    expect((screen.getByLabelText("Search tasks") as HTMLInputElement).value).toBe("");
    expect(screen.getByRole("button", { name: /Pay rent/ })).toBeTruthy();
    expect(loadKataWorkspaceState("home")).toEqual(persisted);
  });

  it("does not persist external URL selection", async () => {
    acceptHomeDaemon();
    const persisted = {
      view: "inbox" as const,
      filters: { scope: { kind: "all" as const }, status: "open" as const, owner: "", label: "", query: "" },
      selectedIssueUID: null,
    };
    const { api } = createWorkspaceAPI();
    saveKataWorkspaceState("home", persisted);

    const { rerender } = render(KataWorkspace, { props: { api } });
    await waitFor(() => expect(screen.getByRole("heading", { name: "Inbox" })).toBeTruthy());

    await rerender({ api, selectedIssueUID: "issue-pay-rent" });

    await waitFor(() => expect(screen.getByRole("heading", { name: "Pay rent" })).toBeTruthy());
    expect(loadKataWorkspaceState("home")).toEqual(persisted);
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

  it("restores a closed Logbook selection while the persisted status remains open", async () => {
    const closedIssue = {
      ...issue("issue-done-restored", "Restored done work", "project-kata"),
      status: "closed" as const,
      closed_at: "2026-05-15T12:00:00.000Z",
    };
    const { api } = createWorkspaceAPI([closedIssue]);
    saveKataWorkspaceState("home", {
      view: "logbook",
      filters: { scope: { kind: "all" }, status: "open", owner: "", label: "", query: "" },
      selectedIssueUID: closedIssue.uid,
    });

    const { component } = render(KataWorkspaceRouteHost, { props: { api } });

    await waitFor(() => expect(screen.getByRole("heading", { name: "Restored done work" })).toBeTruthy());
    expect(screen.getByRole("combobox", { name: "Status: Open" })).toBeTruthy();
    expect(component.route().issue).toBe(closedIssue.uid);
    expect(loadKataWorkspaceState("home")?.selectedIssueUID).toBe(closedIssue.uid);
  });

  it("captures a new task into the inbox from the feature toolbar", async () => {
    const { api, createIssue } = createWorkspaceAPI();

    render(KataWorkspace, { props: { api } });
    await waitFor(() => {
      expect(screen.getByRole("heading", { name: "All Open" })).toBeTruthy();
      expect((screen.getByRole("button", { name: "New task" }) as HTMLButtonElement).disabled).toBe(false);
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

  it("restores the target daemon snapshot only after accepting the daemon switch", async () => {
    vi.spyOn(globalThis, "fetch").mockImplementation(async () =>
      Response.json({
        daemons: [
          { id: "home", url: "http://127.0.0.1:7777", default: true, auth: "none", health: "connected" },
          { id: "work", url: "http://127.0.0.1:8888", default: false, auth: "none", health: "connected" },
        ],
      }),
    );
    const home = initialIssues[0]!;
    const firstWork = initialIssues[1]!;
    const restoredWork = issue("issue-work-restored", "Restored work task", "project-kata");
    const api = createDaemonWorkspaceAPI(
      { home: [home], work: [firstWork, restoredWork] },
      {
        home: projects.filter((project) => project.uid !== "project-kata"),
        work: projects.filter((project) => project.uid === "project-kata"),
      },
    );
    const homeSnapshot = {
      view: "all" as const,
      filters: { scope: { kind: "all" as const }, status: "open" as const, owner: "Home", label: "", query: "" },
      selectedIssueUID: home.uid,
    };
    const workSnapshot = {
      view: "all" as const,
      filters: {
        scope: { kind: "project" as const, project_uid: "project-kata" },
        status: "all" as const,
        owner: "Work",
        label: "",
        query: "saved",
      },
      selectedIssueUID: restoredWork.uid,
    };
    saveKataWorkspaceState("home", homeSnapshot);
    saveKataWorkspaceState("work", workSnapshot);

    render(KataWorkspaceRouteHost, { props: { api } });
    await waitFor(() => expect(screen.getByRole("heading", { name: "Pay rent" })).toBeTruthy());

    await fireEvent.click(screen.getByTestId("daemon-chip"));
    const workDaemonRow = (await screen.findByTestId("daemon-row-work")) as HTMLButtonElement;
    await waitFor(() => expect(workDaemonRow.disabled).toBe(false));
    await fireEvent.click(workDaemonRow);

    await waitFor(() => expect(screen.getByRole("heading", { name: "Restored work task" })).toBeTruthy());
    expect((screen.getByLabelText("Search tasks") as HTMLInputElement).value).toBe("saved");
    expect(screen.getByRole("combobox", { name: "Status: All" })).toBeTruthy();
    expect(loadKataWorkspaceState("home")).toEqual(homeSnapshot);
    expect(loadKataWorkspaceState("work")).toEqual(workSnapshot);
  });

  it("keeps an unknown routed daemon inert without resolving its issue through the accepted daemon", async () => {
    vi.spyOn(globalThis, "fetch").mockImplementation(async () =>
      Response.json({
        daemons: [{ id: "home", url: "http://127.0.0.1:7777", default: true, auth: "none", health: "connected" }],
      }),
    );
    setActiveKataDaemon("home");
    const api = createDaemonWorkspaceAPI({ home: [initialIssues[0]!] });

    render(KataWorkspaceRouteHost, {
      props: { api, initialIssue: initialIssues[0]!.uid, initialDaemon: "missing" },
    });

    await screen.findByText("Kata daemon missing is not configured.");
    await new Promise((resolve) => setTimeout(resolve, 50));
    expect(api.issue).not.toHaveBeenCalled();
    expect(api.issues).not.toHaveBeenCalled();
    expect(api.search).not.toHaveBeenCalled();
    expect((screen.getByRole("button", { name: "New task" }) as HTMLButtonElement).disabled).toBe(true);
    expect(screen.getByText("Select a task")).toBeTruthy();
  });

  it("recovers the accepted daemon when an unknown routed daemon is removed on the same mount", async () => {
    vi.spyOn(globalThis, "fetch").mockImplementation(async () =>
      Response.json({
        daemons: [{ id: "home", url: "http://127.0.0.1:7777", default: true, auth: "none", health: "connected" }],
      }),
    );
    setActiveKataDaemon("home");
    const home = initialIssues[0]!;
    const api = createDaemonWorkspaceAPI({ home: [home] });

    const { component } = render(KataWorkspaceRouteHost, {
      props: { api, initialIssue: home.uid, initialDaemon: "missing" },
    });

    await screen.findByText("Kata daemon missing is not configured.");
    component.setRoute({ issue: home.uid, daemon: null });

    await waitFor(() => expect(screen.getByRole("heading", { name: "Pay rent" })).toBeTruthy());
    expect(getActiveKataDaemon()).toBe("home");
    expect(api.issue).toHaveBeenCalledWith(home.uid, expect.objectContaining({ signal: expect.any(AbortSignal) }));
    expect((screen.getByRole("button", { name: "New task" }) as HTMLButtonElement).disabled).toBe(false);
  });

  it("accepts a routed daemon after a successful restoration retry", async () => {
    vi.spyOn(globalThis, "fetch").mockImplementation(async () =>
      Response.json({
        daemons: [
          { id: "home", url: "http://127.0.0.1:7777", default: true, auth: "none", health: "connected" },
          { id: "work", url: "http://127.0.0.1:8888", default: false, auth: "none", health: "connected" },
        ],
      }),
    );
    setActiveKataDaemon("home");
    const work = initialIssues[1]!;
    const api = createDaemonWorkspaceAPI({ home: [initialIssues[0]!], work: [work] });
    vi.mocked(api.projects).mockRejectedValueOnce(new Error("catalog timed out"));
    vi.mocked(api.issue).mockImplementation(async (uid: string) => {
      const restored = detail(uid, [work]);
      return { ...restored, issue: { ...restored.issue, body: "" } };
    });

    const { component } = render(KataWorkspaceRouteHost, {
      props: { api, initialIssue: work.uid, initialDaemon: "work" },
    });

    await waitFor(() => expect(screen.getByRole("button", { name: "Retry" })).toBeTruthy());
    expect(getActiveKataDaemon()).toBe("home");
    expect(component.route().daemon).toBe("work");

    await fireEvent.click(screen.getByRole("button", { name: "Retry" }));

    await waitFor(() => {
      expect(getActiveKataDaemon()).toBe("work");
      expect(component.route().daemon).toBeNull();
      expect(screen.getByRole("heading", { name: "Email Susan re: Q3" })).toBeTruthy();
    });
  });

  it("persists a routed daemon selection only after the target is accepted", async () => {
    vi.spyOn(globalThis, "fetch").mockImplementation(async () =>
      Response.json({
        daemons: [
          { id: "home", url: "http://127.0.0.1:7777", default: true, auth: "none", health: "connected" },
          { id: "work", url: "http://127.0.0.1:8888", default: false, auth: "none", health: "connected" },
        ],
      }),
    );
    setActiveKataDaemon("home");
    const home = initialIssues[0]!;
    const work = initialIssues[1]!;
    const api = createDaemonWorkspaceAPI({ home: [home], work: [work] });
    saveKataWorkspaceState("work", {
      view: "all",
      filters: { scope: { kind: "all" }, status: "open", owner: "", label: "", query: "" },
      selectedIssueUID: null,
    });

    const { component } = render(KataWorkspaceRouteHost, {
      props: { api, initialIssue: work.uid, initialDaemon: "work" },
    });

    await waitFor(() => {
      expect(getActiveKataDaemon()).toBe("work");
      expect(component.route().daemon).toBeNull();
      expect(screen.getByRole("heading", { name: "Email Susan re: Q3" })).toBeTruthy();
    });
    expect(loadKataWorkspaceState("work")?.selectedIssueUID).toBe(work.uid);
  });

  it("keeps transient routed detail failures provisional and read-only", async () => {
    vi.spyOn(globalThis, "fetch").mockImplementation(async () =>
      Response.json({
        daemons: [
          { id: "home", url: "http://127.0.0.1:7777", default: true, auth: "none", health: "connected" },
          { id: "work", url: "http://127.0.0.1:8888", default: false, auth: "none", health: "connected" },
        ],
      }),
    );
    setActiveKataDaemon("home");
    const work = initialIssues[1]!;
    const api = createDaemonWorkspaceAPI({ home: [initialIssues[0]!], work: [work] });
    vi.mocked(api.issue).mockRejectedValueOnce(new Error("detail timed out"));

    const { component } = render(KataWorkspaceRouteHost, {
      props: { api, initialIssue: work.uid, initialDaemon: "work" },
    });

    const retry = await screen.findByRole("button", { name: "Retry" });
    const layout = screen.getByLabelText("Kata tasks").parentElement;
    expect(getActiveKataDaemon()).toBe("home");
    expect(component.route().daemon).toBe("work");
    expect(screen.getByTestId("daemon-chip").textContent).toContain("home");
    expect((layout as HTMLElement & { inert: boolean }).inert).toBe(true);
    expect((screen.getByRole("button", { name: "New task" }) as HTMLButtonElement).disabled).toBe(true);
    expect(retry.closest("[inert]")).toBeNull();
    expect(vi.mocked(api.events).mock.calls.every(([query]) => !(query && "after_id" in query))).toBe(true);
  });

  it("rolls back a same-mount routed daemon when its routed detail fails transiently", async () => {
    vi.spyOn(globalThis, "fetch").mockImplementation(async () =>
      Response.json({
        daemons: [
          { id: "home", url: "http://127.0.0.1:7777", default: true, auth: "none", health: "connected" },
          { id: "work", url: "http://127.0.0.1:8888", default: false, auth: "none", health: "connected" },
        ],
      }),
    );
    setActiveKataDaemon("home");
    const home = initialIssues[0]!;
    const work = initialIssues[1]!;
    const api = createDaemonWorkspaceAPI({ home: [home], work: [work] });
    vi.mocked(api.issue).mockImplementation(async (uid, opts) => {
      if (uid === work.uid && opts?.daemonId === "work") throw new Error("routed detail timed out");
      return detail(uid, opts?.daemonId === "work" ? [work] : [home]);
    });

    const { component } = render(KataWorkspaceRouteHost, { props: { api, initialIssue: home.uid } });
    await waitFor(() => expect(screen.getByRole("heading", { name: "Pay rent" })).toBeTruthy());

    component.setRoute({ issue: work.uid, daemon: "work" });

    await waitFor(() => expect(flash.getFlash()).toMatchObject({ message: "routed detail timed out", tone: "danger" }));
    expect(getActiveKataDaemon()).toBe("home");
    expect(screen.getByRole("heading", { name: "Pay rent" })).toBeTruthy();
    expect(
      vi
        .mocked(api.events)
        .mock.calls.every(([query, opts]) => query?.after_id === undefined || opts?.daemonId !== "work"),
    ).toBe(true);
  });

  it("commits a routed missing task as a cleared route", async () => {
    vi.spyOn(globalThis, "fetch").mockImplementation(async () =>
      Response.json({
        daemons: [
          { id: "home", url: "http://127.0.0.1:7777", default: true, auth: "none", health: "connected" },
          { id: "work", url: "http://127.0.0.1:8888", default: false, auth: "none", health: "connected" },
        ],
      }),
    );
    setActiveKataDaemon("home");
    const missingUID = "issue-work-missing";
    const api = createDaemonWorkspaceAPI({ home: [initialIssues[0]!], work: [initialIssues[1]!] });
    vi.mocked(api.issue).mockImplementation(async (uid: string) => {
      if (uid === missingUID) {
        throw new KataTaskAPIError({ status: 404, code: "not_found", message: "not found", headers: new Headers() });
      }
      return detail(uid, initialIssues);
    });

    const { component } = render(KataWorkspaceRouteHost, {
      props: { api, initialIssue: missingUID, initialDaemon: "work" },
    });

    await waitFor(() => {
      expect(getActiveKataDaemon()).toBe("work");
      expect(component.route()).toEqual({ issue: null, view: null, scope: null, daemon: null });
    });
    expect(screen.queryByRole("button", { name: "Retry" })).toBeNull();
  });

  it("abandons an in-flight routed restoration when navigation supersedes it", async () => {
    vi.spyOn(globalThis, "fetch").mockImplementation(async () =>
      Response.json({
        daemons: [
          { id: "home", url: "http://127.0.0.1:7777", default: true, auth: "none", health: "connected" },
          { id: "work", url: "http://127.0.0.1:8888", default: false, auth: "none", health: "connected" },
        ],
      }),
    );
    setActiveKataDaemon("home");
    const home = initialIssues[0]!;
    const work = initialIssues[1]!;
    const api = createDaemonWorkspaceAPI({ home: [home], work: [work] });
    const workCatalog = deferred<Awaited<ReturnType<typeof api.projects>>>();
    vi.mocked(api.projects).mockImplementation(async () => {
      const binding = vi.mocked(api.bindWorkflowDaemon!).mock.calls.at(-1)?.[0];
      if (binding === "work") return workCatalog.promise;
      return { projects, fetched_at: fetchedAt };
    });
    saveKataWorkspaceState("home", {
      view: "all",
      filters: { scope: { kind: "all" }, status: "open", owner: "Home", label: "", query: "" },
      selectedIssueUID: home.uid,
    });

    const { component } = render(KataWorkspaceRouteHost, {
      props: { api, initialIssue: work.uid, initialDaemon: "work" },
    });
    await waitFor(() =>
      expect(
        vi
          .mocked(api.projects)
          .mock.calls.some(() => vi.mocked(api.bindWorkflowDaemon!).mock.calls.some(([daemon]) => daemon === "work")),
      ).toBe(true),
    );

    component.setRoute({ issue: null, daemon: null });
    workCatalog.resolve({ projects, fetched_at: fetchedAt });

    await waitFor(() => expect(vi.mocked(api.bindWorkflowDaemon!).mock.calls.at(-1)?.[0]).toBe("home"));
    expect(getActiveKataDaemon()).toBe("home");
    expect(component.route().daemon).toBeNull();
    expect(screen.queryByRole("heading", { name: work.title })).toBeNull();
    expect(screen.getByText("Select a task")).toBeTruthy();
    expect(screen.getByTestId("daemon-chip").textContent).toContain("home");
  });

  it("applies the latest bare route when fallback recovery is superseded", async () => {
    vi.spyOn(globalThis, "fetch").mockImplementation(async () =>
      Response.json({
        daemons: [
          { id: "home", url: "http://127.0.0.1:7777", default: true, auth: "none", health: "connected" },
          { id: "work", url: "http://127.0.0.1:8888", default: false, auth: "none", health: "connected" },
        ],
      }),
    );
    setActiveKataDaemon("home");
    const home = initialIssues[0]!;
    const work = initialIssues[1]!;
    const api = createDaemonWorkspaceAPI({ home: [home], work: [work] });
    const workCatalog = deferred<Awaited<ReturnType<typeof api.projects>>>();
    const homeCatalog = deferred<Awaited<ReturnType<typeof api.projects>>>();
    vi.mocked(api.projects).mockImplementation(async () => {
      const binding = vi.mocked(api.bindWorkflowDaemon!).mock.calls.at(-1)?.[0];
      if (binding === "work") return workCatalog.promise;
      if (binding === "home") return homeCatalog.promise;
      return { projects, fetched_at: fetchedAt };
    });
    saveKataWorkspaceState("home", {
      view: "today",
      filters: {
        scope: { kind: "project", project_uid: "project-finances" },
        status: "open",
        owner: "Home",
        label: "saved",
        query: "saved",
      },
      selectedIssueUID: home.uid,
    });

    const { component } = render(KataWorkspaceRouteHost, {
      props: { api, initialIssue: work.uid, initialDaemon: "work" },
    });
    await waitFor(() => expect(vi.mocked(api.projects)).toHaveBeenCalledTimes(1));

    component.setRoute({ view: "inbox", scope: null, issue: null, daemon: null });
    await waitFor(() => expect(vi.mocked(api.bindWorkflowDaemon!).mock.calls.at(-1)?.[0]).toBe("home"));
    component.setRoute({ view: null, scope: null, issue: null, daemon: null });
    workCatalog.resolve({ projects, fetched_at: fetchedAt });
    homeCatalog.resolve({ projects, fetched_at: fetchedAt });

    await waitFor(() => expect(screen.getByRole("heading", { name: "All Open" })).toBeTruthy());
    expect(component.route()).toEqual({ issue: null, view: null, scope: null, daemon: null });
    expect(screen.getByText("Select a task")).toBeTruthy();
    expect((screen.getByLabelText("Search tasks") as HTMLInputElement).value).toBe("saved");
    expect(loadKataWorkspaceState("home")?.selectedIssueUID).toBe(home.uid);
  });

  it("restores the accepted daemon binding when navigation invalidates routed Retry", async () => {
    vi.spyOn(globalThis, "fetch").mockImplementation(async () =>
      Response.json({
        daemons: [
          { id: "home", url: "http://127.0.0.1:7777", default: true, auth: "none", health: "connected" },
          { id: "work", url: "http://127.0.0.1:8888", default: false, auth: "none", health: "connected" },
        ],
      }),
    );
    setActiveKataDaemon("home");
    const work = initialIssues[1]!;
    const api = createDaemonWorkspaceAPI({ home: [initialIssues[0]!], work: [work] });
    vi.mocked(api.issue).mockRejectedValueOnce(new Error("detail timed out"));

    const { component } = render(KataWorkspaceRouteHost, {
      props: { api, initialIssue: work.uid, initialDaemon: "work" },
    });
    await screen.findByRole("button", { name: "Retry" });

    component.setRoute({ issue: null, daemon: null });

    await waitFor(() => expect(screen.queryByRole("button", { name: "Retry" })).toBeNull());
    expect(getActiveKataDaemon()).toBe("home");
    expect(vi.mocked(api.bindWorkflowDaemon!).mock.calls.at(-1)?.[0]).toBe("home");
    expect(screen.getByTestId("daemon-chip").textContent).toContain("home");
  });

  it("ignores a running routed Retry after navigation supersedes it", async () => {
    vi.spyOn(globalThis, "fetch").mockImplementation(async () =>
      Response.json({
        daemons: [
          { id: "home", url: "http://127.0.0.1:7777", default: true, auth: "none", health: "connected" },
          { id: "work", url: "http://127.0.0.1:8888", default: false, auth: "none", health: "connected" },
        ],
      }),
    );
    setActiveKataDaemon("home");
    const home = initialIssues[0]!;
    const work = initialIssues[1]!;
    const api = createDaemonWorkspaceAPI({ home: [home], work: [work] });
    const retryCatalog = deferred<Awaited<ReturnType<typeof api.projects>>>();
    let workCatalogAttempts = 0;
    vi.mocked(api.projects).mockImplementation(async () => {
      const binding = vi.mocked(api.bindWorkflowDaemon!).mock.calls.at(-1)?.[0];
      if (binding === "work") {
        workCatalogAttempts += 1;
        if (workCatalogAttempts === 1) throw new Error("catalog timed out");
        if (workCatalogAttempts === 2) return retryCatalog.promise;
      }
      return { projects, fetched_at: fetchedAt };
    });
    saveKataWorkspaceState("home", {
      view: "all",
      filters: { scope: { kind: "all" }, status: "open", owner: "Home", label: "", query: "" },
      selectedIssueUID: home.uid,
    });

    const { component } = render(KataWorkspaceRouteHost, {
      props: { api, initialIssue: work.uid, initialDaemon: "work" },
    });
    await screen.findByRole("button", { name: "Retry" });

    await fireEvent.click(screen.getByRole("button", { name: "Retry" }));
    await waitFor(() => expect(workCatalogAttempts).toBe(2));
    component.setRoute({ issue: null, daemon: null });
    retryCatalog.resolve({ projects, fetched_at: fetchedAt });

    await waitFor(() => expect(vi.mocked(api.bindWorkflowDaemon!).mock.calls.at(-1)?.[0]).toBe("home"));
    expect(getActiveKataDaemon()).toBe("home");
    expect(component.route().daemon).toBeNull();
    expect(screen.queryByRole("button", { name: "Retry" })).toBeNull();
    expect(screen.queryByRole("heading", { name: work.title })).toBeNull();
    expect(screen.getByTestId("daemon-chip").textContent).toContain("home");
  });

  it("does not reinstall routed Retry when a superseded attempt rejects late", async () => {
    vi.spyOn(globalThis, "fetch").mockImplementation(async () =>
      Response.json({
        daemons: [
          { id: "home", url: "http://127.0.0.1:7777", default: true, auth: "none", health: "connected" },
          { id: "work", url: "http://127.0.0.1:8888", default: false, auth: "none", health: "connected" },
        ],
      }),
    );
    setActiveKataDaemon("home");
    const home = initialIssues[0]!;
    const work = initialIssues[1]!;
    const api = createDaemonWorkspaceAPI({ home: [home], work: [work] });
    const retryCatalog = deferred<Awaited<ReturnType<typeof api.projects>>>();
    let workCatalogAttempts = 0;
    vi.mocked(api.projects).mockImplementation(async () => {
      const binding = vi.mocked(api.bindWorkflowDaemon!).mock.calls.at(-1)?.[0];
      if (binding === "work") {
        workCatalogAttempts += 1;
        if (workCatalogAttempts === 1) throw new Error("catalog timed out");
        if (workCatalogAttempts === 2) return retryCatalog.promise;
      }
      return { projects, fetched_at: fetchedAt };
    });

    const { component } = render(KataWorkspaceRouteHost, {
      props: { api, initialIssue: work.uid, initialDaemon: "work" },
    });
    await screen.findByRole("button", { name: "Retry" });

    await fireEvent.click(screen.getByRole("button", { name: "Retry" }));
    await waitFor(() => expect(workCatalogAttempts).toBe(2));
    component.setRoute({ issue: null, daemon: null });
    retryCatalog.reject(new Error("late retry failure"));

    await waitFor(() => expect(vi.mocked(api.bindWorkflowDaemon!).mock.calls.at(-1)?.[0]).toBe("home"));
    expect(getActiveKataDaemon()).toBe("home");
    expect(component.route().daemon).toBeNull();
    expect(screen.queryByRole("button", { name: "Retry" })).toBeNull();
    expect(screen.queryByText("late retry failure")).toBeNull();
    expect(screen.getByTestId("daemon-chip").textContent).toContain("home");
  });

  it("defers invalid routed target cleanup until cursor catch-up accepts it", async () => {
    vi.spyOn(globalThis, "fetch").mockImplementation(async () =>
      Response.json({
        daemons: [
          { id: "home", url: "http://127.0.0.1:7777", default: true, auth: "none", health: "connected" },
          { id: "work", url: "http://127.0.0.1:8888", default: false, auth: "none", health: "connected" },
        ],
      }),
    );
    setActiveKataDaemon("home");
    const home = initialIssues[0]!;
    const work = initialIssues[1]!;
    const api = createDaemonWorkspaceAPI(
      { home: [home], work: [work] },
      { home: projects, work: projects.filter((project) => project.uid !== "project-finances") },
    );
    let workCursorAttempts = 0;
    vi.mocked(api.events).mockImplementation(async (params) => {
      const binding = vi.mocked(api.bindWorkflowDaemon!).mock.calls.at(-1)?.[0];
      if (binding === "work" && params && "after_id" in params && ++workCursorAttempts === 1) {
        throw new Error("cursor timed out");
      }
      return { reset_required: false, events: [], next_after_id: 0 };
    });
    const workSnapshot = {
      view: "all" as const,
      filters: {
        scope: { kind: "project" as const, project_uid: "project-missing" },
        status: "all" as const,
        owner: "Work",
        label: "stale",
        query: "discard",
      },
      selectedIssueUID: work.uid,
    };
    saveKataWorkspaceState("work", workSnapshot);

    const { component } = render(KataWorkspaceRouteHost, {
      props: { api, initialIssue: work.uid, initialDaemon: "work" },
    });

    await waitFor(() => expect(screen.getByRole("button", { name: "Retry" })).toBeTruthy());
    expect(workCursorAttempts).toBe(1);
    expect(getActiveKataDaemon()).toBe("home");
    expect(component.route().daemon).toBe("work");
    expect(loadKataWorkspaceState("work")).toEqual(workSnapshot);

    await fireEvent.click(screen.getByRole("button", { name: "Retry" }));

    await waitFor(() => {
      expect(getActiveKataDaemon()).toBe("work");
      expect(component.route().daemon).toBeNull();
      expect(loadKataWorkspaceState("work")).toBeNull();
    });
  });

  it("defers stale routed target selection cleanup until cursor catch-up accepts it", async () => {
    vi.spyOn(globalThis, "fetch").mockImplementation(async () =>
      Response.json({
        daemons: [
          { id: "home", url: "http://127.0.0.1:7777", default: true, auth: "none", health: "connected" },
          { id: "work", url: "http://127.0.0.1:8888", default: false, auth: "none", health: "connected" },
        ],
      }),
    );
    setActiveKataDaemon("home");
    const staleUID = "issue-work-missing";
    const api = createDaemonWorkspaceAPI({ home: [initialIssues[0]!], work: [initialIssues[1]!] });
    vi.mocked(api.issue).mockImplementation(async (uid: string) => {
      if (uid === staleUID) {
        throw new KataTaskAPIError({ status: 404, code: "not_found", message: "not found", headers: new Headers() });
      }
      return detail(uid, initialIssues);
    });
    let workCursorAttempts = 0;
    vi.mocked(api.events).mockImplementation(async (params) => {
      const binding = vi.mocked(api.bindWorkflowDaemon!).mock.calls.at(-1)?.[0];
      if (binding === "work" && params && "after_id" in params && ++workCursorAttempts === 1) {
        throw new Error("cursor timed out");
      }
      return { reset_required: false, events: [], next_after_id: 0 };
    });
    const workSnapshot = {
      view: "all" as const,
      filters: { scope: { kind: "all" as const }, status: "open" as const, owner: "Work", label: "", query: "saved" },
      selectedIssueUID: staleUID,
    };
    saveKataWorkspaceState("work", workSnapshot);

    const { component } = render(KataWorkspaceRouteHost, {
      props: { api, initialDaemon: "work" },
    });

    await waitFor(() => expect(screen.getByRole("button", { name: "Retry" })).toBeTruthy());
    expect(workCursorAttempts).toBe(1);
    expect(getActiveKataDaemon()).toBe("home");
    expect(component.route().daemon).toBe("work");
    expect(loadKataWorkspaceState("work")).toEqual(workSnapshot);

    await fireEvent.click(screen.getByRole("button", { name: "Retry" }));

    await waitFor(() => {
      expect(getActiveKataDaemon()).toBe("work");
      expect(component.route().daemon).toBeNull();
      expect(loadKataWorkspaceState("work")?.selectedIssueUID).toBeNull();
    });
  });

  it("keeps routed daemon Retry provisional until cursor catch-up succeeds", async () => {
    vi.spyOn(globalThis, "fetch").mockImplementation(async () =>
      Response.json({
        daemons: [
          { id: "home", url: "http://127.0.0.1:7777", default: true, auth: "none", health: "connected" },
          { id: "work", url: "http://127.0.0.1:8888", default: false, auth: "none", health: "connected" },
        ],
      }),
    );
    setActiveKataDaemon("home");
    const work = initialIssues[1]!;
    const api = createDaemonWorkspaceAPI({ home: [initialIssues[0]!], work: [work] });
    vi.mocked(api.projects).mockRejectedValueOnce(new Error("catalog timed out"));
    let workCursorAttempts = 0;
    vi.mocked(api.events).mockImplementation(async (params) => {
      const binding = vi.mocked(api.bindWorkflowDaemon!).mock.calls.at(-1)?.[0];
      if (binding === "work" && params && "after_id" in params && ++workCursorAttempts === 1) {
        throw new Error("cursor timed out");
      }
      return { reset_required: false, events: [], next_after_id: 0 };
    });
    const workSnapshot = {
      view: "all" as const,
      filters: { scope: { kind: "all" as const }, status: "open" as const, owner: "Work", label: "", query: "saved" },
      selectedIssueUID: work.uid,
    };
    saveKataWorkspaceState("work", workSnapshot);

    const { component } = render(KataWorkspaceRouteHost, {
      props: { api, initialIssue: work.uid, initialDaemon: "work" },
    });

    await waitFor(() => expect(screen.getByRole("button", { name: "Retry" })).toBeTruthy());
    await fireEvent.click(screen.getByRole("button", { name: "Retry" }));

    await waitFor(() => {
      expect(workCursorAttempts).toBe(1);
      expect(screen.getByRole("button", { name: "Retry" })).toBeTruthy();
    });
    expect(getActiveKataDaemon()).toBe("home");
    expect(component.route().daemon).toBe("work");
    expect(loadKataWorkspaceState("work")).toEqual(workSnapshot);

    await fireEvent.click(screen.getByRole("button", { name: "Retry" }));

    await waitFor(() => {
      expect(getActiveKataDaemon()).toBe("work");
      expect(component.route().daemon).toBeNull();
      expect(screen.getByRole("heading", { name: "Email Susan re: Q3" })).toBeTruthy();
    });
  });

  it("rolls back a manual switch when its persisted detail fails transiently", async () => {
    vi.spyOn(globalThis, "fetch").mockImplementation(async () =>
      Response.json({
        daemons: [
          { id: "home", url: "http://127.0.0.1:7777", default: true, auth: "none", health: "connected" },
          { id: "work", url: "http://127.0.0.1:8888", default: false, auth: "none", health: "connected" },
        ],
      }),
    );
    setActiveKataDaemon("home");
    const home = initialIssues[0]!;
    const work = initialIssues[1]!;
    const api = createDaemonWorkspaceAPI({ home: [home], work: [work] });
    saveKataWorkspaceState("home", {
      view: "all",
      filters: { scope: { kind: "all" }, status: "open", owner: "", label: "", query: "" },
      selectedIssueUID: home.uid,
    });
    vi.mocked(api.issue).mockImplementation(async (uid, opts) => {
      if (uid === work.uid && opts?.daemonId === "work") throw new Error("work detail timed out");
      return detail(uid, uid === work.uid ? [work] : [home]);
    });
    saveKataWorkspaceState("work", {
      view: "all",
      filters: { scope: { kind: "all" }, status: "open", owner: "", label: "", query: "" },
      selectedIssueUID: work.uid,
    });

    render(KataWorkspace, { props: { api } });
    await waitFor(() => expect(screen.getByRole("heading", { name: "Pay rent" })).toBeTruthy());
    await fireEvent.click(screen.getByTestId("daemon-chip"));
    const workDaemonRow = (await screen.findByTestId("daemon-row-work")) as HTMLButtonElement;
    await waitFor(() => expect(workDaemonRow.disabled).toBe(false));
    await fireEvent.click(workDaemonRow);

    await waitFor(() => expect(screen.getByText("work detail timed out")).toBeTruthy());
    expect(getActiveKataDaemon()).toBe("home");
    expect(screen.getByRole("heading", { name: "Pay rent" })).toBeTruthy();
    expect(screen.queryByRole("button", { name: "Retry" })).toBeNull();
  });

  it("loads candidate ancestors through the target daemon without mutating the accepted workspace", async () => {
    vi.spyOn(globalThis, "fetch").mockImplementation(async () =>
      Response.json({
        daemons: [
          { id: "home", url: "http://127.0.0.1:7777", default: true, auth: "none", health: "connected" },
          { id: "work", url: "http://127.0.0.1:8888", default: false, auth: "none", health: "connected" },
        ],
      }),
    );
    setActiveKataDaemon("home");
    const home = initialIssues[0]!;
    const parent = issue("issue-work-parent", "Work parent", "project-kata");
    const child = {
      ...issue("issue-work-child", "Work child", "project-kata"),
      parent: { uid: parent.uid, short_id: parent.short_id },
      parent_short_id: parent.short_id,
    };
    const api = createDaemonWorkspaceAPI({ home: [home], work: [child] });
    saveKataWorkspaceState("home", {
      view: "all",
      filters: { scope: { kind: "all" }, status: "open", owner: "", label: "", query: "" },
      selectedIssueUID: home.uid,
    });
    const parentDetail = deferred<KataTaskDetail>();
    vi.mocked(api.issue).mockImplementation(async (uid, opts) => {
      if (uid === child.uid) return detail(child.uid, [child]);
      if (uid === parent.uid) {
        if (opts?.daemonId !== "work") throw new Error("ancestor used accepted daemon");
        return parentDetail.promise;
      }
      return detail(uid, [home]);
    });
    saveKataWorkspaceState("work", {
      view: "all",
      filters: { scope: { kind: "all" }, status: "open", owner: "", label: "", query: "" },
      selectedIssueUID: child.uid,
    });

    render(KataWorkspace, { props: { api } });
    await waitFor(() => expect(screen.getByRole("heading", { name: "Pay rent" })).toBeTruthy());
    await fireEvent.click(screen.getByTestId("daemon-chip"));
    const workDaemonRow = (await screen.findByTestId("daemon-row-work")) as HTMLButtonElement;
    await waitFor(() => expect(workDaemonRow.disabled).toBe(false));
    await fireEvent.click(workDaemonRow);

    await waitFor(() => expect(api.issue).toHaveBeenCalledWith(parent.uid, { daemonId: "work" }));
    expect(getActiveKataDaemon()).toBe("home");
    expect(screen.getByRole("heading", { name: "Pay rent" })).toBeTruthy();
    expect(screen.queryByText("Work parent")).toBeNull();
    expect(screen.queryByText("ancestor used accepted daemon")).toBeNull();

    parentDetail.resolve(detail(parent.uid, [parent, child]));
    await waitFor(() => expect(screen.getByRole("heading", { name: "Work child" })).toBeTruthy());
    expect(getActiveKataDaemon()).toBe("work");
  });

  it("accepts a target daemon when candidate ancestor reconstruction fails transiently", async () => {
    vi.spyOn(globalThis, "fetch").mockImplementation(async () =>
      Response.json({
        daemons: [
          { id: "home", url: "http://127.0.0.1:7777", default: true, auth: "none", health: "connected" },
          { id: "work", url: "http://127.0.0.1:8888", default: false, auth: "none", health: "connected" },
        ],
      }),
    );
    setActiveKataDaemon("home");
    const home = initialIssues[0]!;
    const parent = issue("issue-work-parent-failed", "Unavailable parent", "project-kata");
    const child = {
      ...issue("issue-work-child-accepted", "Accepted child", "project-kata"),
      parent: { uid: parent.uid, short_id: parent.short_id },
      parent_short_id: parent.short_id,
    };
    const api = createDaemonWorkspaceAPI({ home: [home], work: [child] });
    saveKataWorkspaceState("home", {
      view: "all",
      filters: { scope: { kind: "all" }, status: "open", owner: "", label: "", query: "" },
      selectedIssueUID: home.uid,
    });
    vi.mocked(api.issue).mockImplementation(async (uid, opts) => {
      if (uid === child.uid && opts?.daemonId === "work") return detail(child.uid, [child]);
      if (uid === parent.uid && opts?.daemonId === "work") throw new Error("ancestor timed out");
      return detail(uid, [home]);
    });
    saveKataWorkspaceState("work", {
      view: "all",
      filters: { scope: { kind: "all" }, status: "open", owner: "", label: "", query: "" },
      selectedIssueUID: child.uid,
    });

    render(KataWorkspace, { props: { api } });
    await waitFor(() => expect(screen.getByRole("heading", { name: "Pay rent" })).toBeTruthy());
    await fireEvent.click(screen.getByTestId("daemon-chip"));
    const workDaemonRow = (await screen.findByTestId("daemon-row-work")) as HTMLButtonElement;
    await waitFor(() => expect(workDaemonRow.disabled).toBe(false));
    await fireEvent.click(workDaemonRow);

    await waitFor(() => {
      expect(getActiveKataDaemon()).toBe("work");
      expect(screen.getByRole("heading", { name: "Accepted child" })).toBeTruthy();
      expect(screen.getByText("ancestor timed out")).toBeTruthy();
    });
    expect(screen.getByRole("button", { name: "Retry" })).toBeTruthy();
  });

  it("does not reveal candidate ancestors while rollback is pending", async () => {
    vi.spyOn(globalThis, "fetch").mockImplementation(async () =>
      Response.json({
        daemons: [
          { id: "home", url: "http://127.0.0.1:7777", default: true, auth: "none", health: "connected" },
          { id: "work", url: "http://127.0.0.1:8888", default: false, auth: "none", health: "connected" },
        ],
      }),
    );
    setActiveKataDaemon("home");
    const home = initialIssues[0]!;
    const parent = issue("issue-work-rollback-parent", "Rollback work parent", "project-kata");
    const child = {
      ...issue("issue-work-rollback-child", "Rollback work child", "project-kata"),
      parent: { uid: parent.uid, short_id: parent.short_id },
      parent_short_id: parent.short_id,
    };
    const api = createDaemonWorkspaceAPI({ home: [home], work: [child] });
    const parentDetail = deferred<KataTaskDetail>();
    const rollbackCursor = deferred<KataTaskEventsResponse>();
    let homeCursorCalls = 0;
    vi.mocked(api.issue).mockImplementation(async (uid, opts) => {
      if (uid === child.uid && opts?.daemonId === "work") return detail(child.uid, [child]);
      if (uid === parent.uid && opts?.daemonId === "work") return parentDetail.promise;
      return detail(uid, [home]);
    });
    vi.mocked(api.events).mockImplementation(async (query = {}, opts) => {
      if (query.after_id !== undefined && query.issue_uid === undefined && opts?.daemonId === "work") {
        throw new Error("work cursor failed");
      }
      if (query.after_id !== undefined && query.issue_uid === undefined && opts?.daemonId === "home") {
        homeCursorCalls += 1;
        if (homeCursorCalls === 2) return rollbackCursor.promise;
      }
      return { reset_required: false, events: [], next_after_id: query.after_id ?? 0 };
    });
    saveKataWorkspaceState("home", {
      view: "all",
      filters: { scope: { kind: "all" }, status: "open", owner: "", label: "", query: "" },
      selectedIssueUID: home.uid,
    });
    saveKataWorkspaceState("work", {
      view: "all",
      filters: { scope: { kind: "all" }, status: "open", owner: "", label: "", query: "" },
      selectedIssueUID: child.uid,
    });

    render(KataWorkspace, { props: { api } });
    await waitFor(() => expect(screen.getByRole("heading", { name: "Pay rent" })).toBeTruthy());
    await fireEvent.click(screen.getByTestId("daemon-chip"));
    const workDaemonRow = (await screen.findByTestId("daemon-row-work")) as HTMLButtonElement;
    await waitFor(() => expect(workDaemonRow.disabled).toBe(false));
    await fireEvent.click(workDaemonRow);
    await waitFor(() => expect(api.issue).toHaveBeenCalledWith(parent.uid, { daemonId: "work" }));
    parentDetail.resolve(detail(parent.uid, [parent, child]));
    await waitFor(() =>
      expect(
        vi
          .mocked(api.events)
          .mock.calls.some(
            ([query, opts]) =>
              query?.after_id !== undefined && query.issue_uid === undefined && opts?.daemonId === "work",
          ),
      ).toBe(true),
    );
    await waitFor(() => expect(homeCursorCalls).toBe(2));
    await tick();
    await tick();

    expect(getActiveKataDaemon()).toBe("home");
    expect(screen.getByRole("heading", { name: "Pay rent" })).toBeTruthy();
    expect(screen.queryByText("Rollback work child", { selector: ".title-text" })).toBeNull();
    expect(screen.queryByText("Rollback work parent", { selector: ".title-text" })).toBeNull();

    rollbackCursor.resolve({ reset_required: false, events: [], next_after_id: 0 });
    await waitFor(() => expect(getActiveKataDaemon()).toBe("home"));
  });

  it("drops rollback cursor work after a route change supersedes the switch", async () => {
    vi.spyOn(globalThis, "fetch").mockImplementation(async () =>
      Response.json({
        daemons: [
          { id: "home", url: "http://127.0.0.1:7777", default: true, auth: "none", health: "connected" },
          { id: "work", url: "http://127.0.0.1:8888", default: false, auth: "none", health: "connected" },
        ],
      }),
    );
    setActiveKataDaemon("home");
    const home = initialIssues[0]!;
    const api = createDaemonWorkspaceAPI({ home: [home], work: [initialIssues[1]!] });
    const rollbackInstance = deferred<Awaited<ReturnType<KataTaskAPI["instance"]>>>();
    let workAttempted = false;
    let homeCursorCalls = 0;
    vi.mocked(api.instance).mockImplementation(async (opts) => {
      const daemonID = opts?.daemonId ?? vi.mocked(api.bindWorkflowDaemon!).mock.calls.at(-1)?.[0];
      if (daemonID === "work") {
        workAttempted = true;
        throw new Error("work unavailable");
      }
      if (workAttempted && daemonID === "home") return rollbackInstance.promise;
      return { instance_uid: "instance-1", version: "dev", schema_version: 1 };
    });
    vi.mocked(api.events).mockImplementation(async (query = {}, opts) => {
      const daemonID = opts?.daemonId ?? vi.mocked(api.bindWorkflowDaemon!).mock.calls.at(-1)?.[0];
      if (daemonID === "home" && query.after_id !== undefined && query.issue_uid === undefined) {
        homeCursorCalls += 1;
      }
      return { reset_required: false, events: [], next_after_id: query.after_id ?? 0 };
    });

    const { component } = render(KataWorkspaceRouteHost, {
      props: { api, initialIssue: home.uid },
    });
    await waitForWorkspaceWritable();
    expect(homeCursorCalls).toBe(1);

    await fireEvent.click(screen.getByTestId("daemon-chip"));
    const workDaemonRow = (await screen.findByTestId("daemon-row-work")) as HTMLButtonElement;
    await waitFor(() => expect(workDaemonRow.disabled).toBe(false));
    await fireEvent.click(workDaemonRow);
    await waitFor(() => expect(workAttempted).toBe(true));

    component.setRoute({ issue: null, daemon: null });
    await tick();
    rollbackInstance.resolve({ instance_uid: "instance-1", version: "dev", schema_version: 1 });
    await tick();
    await tick();

    expect(component.route().issue).toBeNull();
    expect(homeCursorCalls).toBe(1);
  });

  it("waits for candidate history and recurrence reads before adopting the target detail", async () => {
    vi.spyOn(globalThis, "fetch").mockImplementation(async () =>
      Response.json({
        daemons: [
          { id: "home", url: "http://127.0.0.1:7777", default: true, auth: "none", health: "connected" },
          { id: "work", url: "http://127.0.0.1:8888", default: false, auth: "none", health: "connected" },
        ],
      }),
    );
    setActiveKataDaemon("home");
    const home = initialIssues[0]!;
    const work = { ...initialIssues[1]!, recurrence_id: 9 };
    const api = createDaemonWorkspaceAPI({ home: [home], work: [work] });
    const workEvents = deferred<Awaited<ReturnType<KataTaskAPI["events"]>>>();
    const workRecurrences = deferred<Awaited<ReturnType<KataTaskAPI["recurrences"]>>>();
    vi.mocked(api.events).mockImplementation(async (query = {}, opts) =>
      query.issue_uid === work.uid && opts?.daemonId === "work"
        ? workEvents.promise
        : { reset_required: false, events: [], next_after_id: 0 },
    );
    vi.mocked(api.recurrences).mockImplementation(async (_projectID, opts) =>
      opts?.daemonId === "work" ? workRecurrences.promise : { recurrences: [], fetched_at: fetchedAt },
    );
    saveKataWorkspaceState("work", {
      view: "all",
      filters: { scope: { kind: "all" }, status: "open", owner: "", label: "", query: "" },
      selectedIssueUID: work.uid,
    });

    render(KataWorkspace, { props: { api } });
    await waitFor(() => expect((screen.getByTestId("daemon-chip") as HTMLButtonElement).disabled).toBe(false));
    await fireEvent.click(screen.getByTestId("daemon-chip"));
    const workDaemonRow = (await screen.findByTestId("daemon-row-work")) as HTMLButtonElement;
    await waitFor(() => expect(workDaemonRow.disabled).toBe(false));
    await fireEvent.click(workDaemonRow);
    await waitFor(() =>
      expect(api.issue).toHaveBeenCalledWith(work.uid, { daemonId: "work", signal: expect.any(AbortSignal) }),
    );
    expect(getActiveKataDaemon()).toBe("home");

    workEvents.resolve({
      reset_required: false,
      events: [
        {
          event_id: 9,
          event_uid: "event-work-created",
          origin_instance_uid: "instance-work",
          type: "issue.created",
          project_id: work.project_id,
          project_uid: work.project_uid,
          project_name: work.project_name,
          issue_uid: work.uid,
          issue_short_id: work.short_id,
          actor: "fixture-user",
          created_at: fetchedAt,
        },
      ],
      next_after_id: 9,
    });
    workRecurrences.resolve({
      recurrences: [recurrence({ id: 9, project_id: work.project_id })],
      fetched_at: fetchedAt,
    });

    await waitFor(() => {
      expect(getActiveKataDaemon()).toBe("work");
      expect(screen.getByRole("heading", { name: "Email Susan re: Q3" })).toBeTruthy();
      expect(screen.getByRole("region", { name: "Recurrence" })).toBeTruthy();
    });
  });

  it("clears an invalid target snapshot only after accepting the daemon switch", async () => {
    vi.spyOn(globalThis, "fetch").mockImplementation(async () =>
      Response.json({
        daemons: [
          { id: "home", url: "http://127.0.0.1:7777", default: true, auth: "none", health: "connected" },
          { id: "work", url: "http://127.0.0.1:8888", default: false, auth: "none", health: "connected" },
        ],
      }),
    );
    const home = initialIssues[0]!;
    const work = initialIssues[1]!;
    const api = createDaemonWorkspaceAPI(
      { home: [home], work: [work] },
      { home: projects, work: projects.filter((project) => project.uid !== "project-finances") },
    );
    saveKataWorkspaceState("work", {
      view: "all",
      filters: {
        scope: { kind: "project", project_uid: "project-missing" },
        status: "all",
        owner: "Work",
        label: "stale",
        query: "discard",
      },
      selectedIssueUID: work.uid,
    });

    render(KataWorkspace, { props: { api } });
    await waitFor(() => expect(screen.getByRole("heading", { name: "All Open" })).toBeTruthy());
    await fireEvent.click(screen.getByTestId("daemon-chip"));
    const workDaemonRow = (await screen.findByTestId("daemon-row-work")) as HTMLButtonElement;
    await waitFor(() => expect(workDaemonRow.disabled).toBe(false));
    await fireEvent.click(workDaemonRow);

    await waitFor(() => expect(getActiveKataDaemon()).toBe("work"));
    expect(screen.getByRole("button", { name: /Email Susan re: Q3/ })).toBeTruthy();
    expect(loadKataWorkspaceState("work")).toBeNull();
  });

  it("clears a restored target selection removed by cursor catch-up", async () => {
    vi.spyOn(globalThis, "fetch").mockImplementation(async () =>
      Response.json({
        daemons: [
          { id: "home", url: "http://127.0.0.1:7777", default: true, auth: "none", health: "connected" },
          { id: "work", url: "http://127.0.0.1:8888", default: false, auth: "none", health: "connected" },
        ],
      }),
    );
    const home = initialIssues[0]!;
    const restoredWork = issue("issue-work-stale", "Stale work task", "project-kata");
    const api = createDaemonWorkspaceAPI({ home: [home], work: [restoredWork] });
    let workRows = [restoredWork];
    vi.mocked(api.issues).mockImplementation(async (query, opts) => {
      const daemonID = opts?.daemonId ?? vi.mocked(api.bindWorkflowDaemon!).mock.calls.at(-1)?.[0] ?? "home";
      const rows = daemonID === "work" ? workRows : [home];
      return {
        view: query.view,
        groups: [{ id: "all", title: "All", issues: rows }],
        fetched_at: fetchedAt,
        daemon_id: daemonID,
      };
    });
    vi.mocked(api.events).mockImplementation(async (_query, opts) => {
      const daemonID = opts?.daemonId ?? vi.mocked(api.bindWorkflowDaemon!).mock.calls.at(-1)?.[0] ?? "home";
      if (daemonID === "work") {
        workRows = [];
        return {
          reset_required: true,
          reset_after_id: 1,
          events: [],
          next_after_id: 1,
        };
      }
      return { reset_required: false, events: [], next_after_id: 0 };
    });
    saveKataWorkspaceState("home", {
      view: "all",
      filters: { scope: { kind: "all" }, status: "open", owner: "", label: "", query: "" },
      selectedIssueUID: home.uid,
    });
    saveKataWorkspaceState("work", {
      view: "all",
      filters: { scope: { kind: "all" }, status: "open", owner: "", label: "", query: "" },
      selectedIssueUID: restoredWork.uid,
    });

    const { component } = render(KataWorkspaceRouteHost, { props: { api } });
    await waitFor(() => expect(screen.getByRole("heading", { name: "Pay rent" })).toBeTruthy());

    await fireEvent.click(screen.getByTestId("daemon-chip"));
    const workDaemonRow = (await screen.findByTestId("daemon-row-work")) as HTMLButtonElement;
    await waitFor(() => expect(workDaemonRow.disabled).toBe(false));
    await fireEvent.click(workDaemonRow);

    await waitFor(() => {
      expect(getActiveKataDaemon()).toBe("work");
      expect(screen.queryByRole("heading", { name: "Stale work task" })).toBeNull();
      expect(loadKataWorkspaceState("work")?.selectedIssueUID).toBeNull();
      expect(component.route().issue).toBeNull();
    });
    expect(vi.mocked(api.issue).mock.calls.filter(([uid]) => uid === restoredWork.uid)).toHaveLength(2);
  });

  it("blocks capture until initial cursor acceptance establishes the workspace owner", async () => {
    vi.spyOn(globalThis, "fetch").mockImplementation(async (input) => {
      const url = new URL(input instanceof Request ? input.url : String(input), window.location.origin);
      if (url.pathname === "/api/v1/kata/daemons") {
        return Response.json({
          daemons: [
            {
              id: "home",
              url: "http://127.0.0.1:7777",
              default: true,
              auth: "none",
              health: "connected",
            },
          ],
        });
      }
      if (url.pathname === "/api/v1/kata/proxy/api/v1/events/stream") {
        return new Response(new ReadableStream<Uint8Array>({}), {
          status: 200,
          headers: { "Content-Type": "text/event-stream" },
        });
      }
      return new Response("not found", { status: 404 });
    });
    setActiveKataDaemon("home");
    const api = createDaemonWorkspaceAPI({ home: [...initialIssues] });
    const cursor = deferred<Awaited<ReturnType<typeof api.events>>>();
    vi.mocked(api.events).mockImplementation(async (query = {}) => {
      if (query.after_id !== undefined && query.issue_uid === undefined) return cursor.promise;
      return { reset_required: false, events: [], next_after_id: 0 };
    });

    render(KataWorkspace, { props: { api } });

    await waitFor(() =>
      expect(vi.mocked(api.events)).toHaveBeenCalledWith({ after_id: 0, limit: 100 }, { daemonId: "home" }),
    );
    const captureButton = screen.getByRole("button", { name: "New task" }) as HTMLButtonElement;
    expect(captureButton.disabled).toBe(true);
    await fireEvent.click(captureButton);
    expect(screen.queryByRole("dialog", { name: "New task" })).toBeNull();

    cursor.resolve({ reset_required: false, events: [], next_after_id: 0 });

    await waitFor(() => expect(captureButton.disabled).toBe(false));
  });

  it("keeps the workspace inert after an initial cursor failure until Retry accepts ownership", async () => {
    vi.spyOn(globalThis, "fetch").mockImplementation(async (input) => {
      const url = new URL(input instanceof Request ? input.url : String(input), window.location.origin);
      if (url.pathname === "/api/v1/kata/daemons") {
        return Response.json({
          daemons: [
            {
              id: "home",
              url: "http://127.0.0.1:7777",
              default: true,
              auth: "none",
              health: "connected",
            },
          ],
        });
      }
      if (url.pathname === "/api/v1/kata/proxy/api/v1/events/stream") {
        return new Response(new ReadableStream<Uint8Array>({}), {
          status: 200,
          headers: { "Content-Type": "text/event-stream" },
        });
      }
      return new Response("not found", { status: 404 });
    });
    const api = createDaemonWorkspaceAPI({ home: [...initialIssues] });
    vi.mocked(api.events)
      .mockRejectedValueOnce(new Error("initial cursor unavailable"))
      .mockResolvedValue({ reset_required: false, events: [], next_after_id: 0 });

    render(KataWorkspace, { props: { api } });

    const retry = await screen.findByRole("button", { name: "Retry" });
    const captureButton = screen.getByRole("button", { name: "New task" }) as HTMLButtonElement;
    const layout = screen.getByLabelText("Kata tasks").parentElement as HTMLElement & { inert: boolean };
    expect(captureButton.disabled).toBe(true);
    expect(layout.inert).toBe(true);
    expect(screen.getByText("Select a task")).toBeTruthy();

    await fireEvent.click(retry);

    await waitFor(() => expect(captureButton.disabled).toBe(false));
    expect(layout.inert).toBe(false);
  });

  it("preserves a selection made while cursor catch-up that started unselected refreshes membership", async () => {
    acceptHomeDaemon();
    const payRent = initialIssues[0]!;
    const emailSusan = initialIssues[1]!;
    const rowsByDaemon = { home: [...initialIssues] };
    const api = createDaemonWorkspaceAPI(rowsByDaemon);
    const cursor = deferred<Awaited<ReturnType<typeof api.events>>>();
    vi.mocked(api.events).mockImplementationOnce(async () => cursor.promise);
    vi.mocked(api.issue).mockImplementation(async (uid) => {
      const found = initialIssues.find((candidate) => candidate.uid === uid);
      if (!found) throw new Error(`missing ${uid}`);
      return detail(uid, [found]);
    });

    const { component } = render(KataWorkspaceRouteHost, { props: { api } });
    await waitFor(() =>
      expect(vi.mocked(api.events)).toHaveBeenCalledWith({ after_id: 0, limit: 100 }, { daemonId: "home" }),
    );

    await fireEvent.click(screen.getByRole("button", { name: /Email Susan re: Q3/ }));
    await waitFor(() => expect(screen.getByRole("heading", { name: "Email Susan re: Q3" })).toBeTruthy());
    rowsByDaemon.home = [payRent];
    cursor.resolve({ reset_required: true, reset_after_id: 1, events: [], next_after_id: 1 });

    await waitFor(() => expect(vi.mocked(api.issues).mock.calls.length).toBeGreaterThan(1));
    expect(screen.getByRole("heading", { name: "Email Susan re: Q3" })).toBeTruthy();
    expect(component.route().issue).toBe(emailSusan.uid);
    expect(loadKataWorkspaceState("home")?.selectedIssueUID).toBe(emailSusan.uid);
  });

  it("accepts a target daemon after partial cursor catch-up and retries from the accepted cursor", async () => {
    vi.spyOn(globalThis, "fetch").mockImplementation(async () =>
      Response.json({
        daemons: [
          { id: "home", url: "http://127.0.0.1:7777", default: true, auth: "none", health: "connected" },
          { id: "work", url: "http://127.0.0.1:8888", default: false, auth: "none", health: "connected" },
        ],
      }),
    );
    const home = initialIssues[0]!;
    const work = initialIssues[1]!;
    const api = createDaemonWorkspaceAPI({ home: [home], work: [work] });
    saveKataWorkspaceState("home", {
      view: "all",
      filters: { scope: { kind: "all" }, status: "open", owner: "", label: "", query: "" },
      selectedIssueUID: home.uid,
    });
    saveKataWorkspaceState("work", {
      view: "all",
      filters: { scope: { kind: "all" }, status: "open", owner: "", label: "", query: "" },
      selectedIssueUID: work.uid,
    });
    let failedLaterPage = false;
    vi.mocked(api.events).mockImplementation(async (query = {}, opts) => {
      const daemonID = opts?.daemonId ?? vi.mocked(api.bindWorkflowDaemon!).mock.calls.at(-1)?.[0] ?? "home";
      if (daemonID !== "work") return { reset_required: false, events: [], next_after_id: 0 };
      if (query.after_id === 0) {
        return {
          reset_required: false,
          events: [
            {
              event_id: 10,
              event_uid: "event-work-catch-up",
              origin_instance_uid: "instance-1",
              type: "issue.edited",
              project_id: work.project_id,
              project_uid: work.project_uid,
              project_name: work.project_name,
              issue_uid: work.uid,
              issue_short_id: work.short_id,
              actor: "fixture-user",
              created_at: fetchedAt,
            },
          ],
          next_after_id: 10,
        };
      }
      if (query.after_id === 10 && !failedLaterPage) {
        failedLaterPage = true;
        throw new Error("later cursor page failed");
      }
      return { reset_required: false, events: [], next_after_id: 10 };
    });

    render(KataWorkspace, { props: { api } });
    await waitFor(() => expect(screen.getByRole("heading", { name: "Pay rent" })).toBeTruthy());
    await fireEvent.click(screen.getByTestId("daemon-chip"));
    const workDaemonRow = (await screen.findByTestId("daemon-row-work")) as HTMLButtonElement;
    await waitFor(() => expect(workDaemonRow.disabled).toBe(false));
    await fireEvent.click(workDaemonRow);

    await waitFor(() => {
      expect(getActiveKataDaemon()).toBe("work");
      expect(screen.getByRole("heading", { name: "Email Susan re: Q3" })).toBeTruthy();
      expect(screen.getByRole("alert").textContent).toContain("later cursor page failed");
    });
    expect(vi.mocked(api.events).mock.calls.some(([query]) => query?.after_id === 10)).toBe(true);

    await fireEvent.click(screen.getByRole("button", { name: "Retry" }));

    await waitFor(() => expect(screen.queryByRole("alert")).toBeNull());
    expect(vi.mocked(api.events).mock.calls.filter(([query]) => query?.after_id === 10)).toHaveLength(2);
  });

  it("rolls back to the persisted home workspace when the target daemon rejects", async () => {
    vi.spyOn(globalThis, "fetch").mockImplementation(async () =>
      Response.json({
        daemons: [
          { id: "home", url: "http://127.0.0.1:7777", default: true, auth: "none", health: "connected" },
          { id: "work", url: "http://127.0.0.1:8888", default: false, auth: "none", health: "connected" },
        ],
      }),
    );
    const home = initialIssues[0]!;
    const api = createDaemonWorkspaceAPI({ home: [home], work: [initialIssues[1]!] });
    const homeSnapshot = {
      view: "all" as const,
      filters: { scope: { kind: "all" as const }, status: "all" as const, owner: "Home", label: "", query: "captured" },
      selectedIssueUID: home.uid,
    };
    const workSnapshot = {
      view: "inbox" as const,
      filters: {
        scope: { kind: "all" as const },
        status: "open" as const,
        owner: "Work",
        label: "",
        query: "unchanged",
      },
      selectedIssueUID: null,
    };
    saveKataWorkspaceState("home", homeSnapshot);
    saveKataWorkspaceState("work", workSnapshot);
    vi.mocked(api.instance).mockImplementation(async (opts) => {
      const daemonID = opts?.daemonId ?? vi.mocked(api.bindWorkflowDaemon!).mock.calls.at(-1)?.[0];
      if (daemonID === "work") throw new Error("work unavailable");
      return { instance_uid: "instance-1", version: "dev", schema_version: 1 };
    });

    render(KataWorkspace, { props: { api } });
    await waitFor(() => expect(screen.getByRole("heading", { name: "Pay rent" })).toBeTruthy());

    await fireEvent.click(screen.getByTestId("daemon-chip"));
    const workDaemonRow = (await screen.findByTestId("daemon-row-work")) as HTMLButtonElement;
    await waitFor(() => expect(workDaemonRow.disabled).toBe(false));
    await fireEvent.click(workDaemonRow);

    await waitFor(() => expect(getActiveKataDaemon()).toBe("home"));
    await waitFor(() => expect(screen.getByRole("heading", { name: "Pay rent" })).toBeTruthy());
    expect((screen.getByLabelText("Search tasks") as HTMLInputElement).value).toBe("captured");
    expect(screen.getByRole("combobox", { name: "Status: All" })).toBeTruthy();
    expect(loadKataWorkspaceState("home")).toEqual(homeSnapshot);
    expect(loadKataWorkspaceState("work")).toEqual(workSnapshot);
  });

  it("rolls back to the roster default without an explicit daemon preference", async () => {
    vi.spyOn(globalThis, "fetch").mockImplementation(async () =>
      Response.json({
        daemons: [
          { id: "home", url: "http://127.0.0.1:7777", default: true, auth: "none", health: "connected" },
          { id: "work", url: "http://127.0.0.1:8888", default: false, auth: "none", health: "connected" },
        ],
      }),
    );
    const api = createDaemonWorkspaceAPI({ home: [initialIssues[0]!], work: [initialIssues[1]!] });
    vi.mocked(api.instance).mockImplementation(async (opts) => {
      const daemonID = opts?.daemonId ?? vi.mocked(api.bindWorkflowDaemon!).mock.calls.at(-1)?.[0];
      if (daemonID === "work") throw new Error("work unavailable");
      return { instance_uid: "instance-1", version: "dev", schema_version: 1 };
    });

    render(KataWorkspace, { props: { api } });
    await waitFor(() => expect(screen.getByRole("heading", { name: "All Open" })).toBeTruthy());
    expect(getActiveKataDaemon()).toBeUndefined();
    expect(getDefaultKataDaemon()).toBe("home");

    await fireEvent.click(screen.getByTestId("daemon-chip"));
    const workDaemonRow = (await screen.findByTestId("daemon-row-work")) as HTMLButtonElement;
    await waitFor(() => expect(workDaemonRow.disabled).toBe(false));
    await fireEvent.click(workDaemonRow);

    await waitFor(() => expect(getActiveKataDaemon()).toBe("home"));
    expect(screen.getByTestId("daemon-chip").textContent).toContain("home");
    expect(screen.queryByText(/Previous Kata daemon is unavailable/)).toBeNull();
    expect(vi.mocked(api.instance).mock.calls.length).toBeGreaterThanOrEqual(2);
    expect(vi.mocked(api.bindWorkflowDaemon!).mock.calls.at(-1)?.[0]).toBe("home");
  });

  it("clears the routed issue when manual-switch rollback catch-up removes the prior selection", async () => {
    vi.spyOn(globalThis, "fetch").mockImplementation(async () =>
      Response.json({
        daemons: [
          { id: "home", url: "http://127.0.0.1:7777", default: true, auth: "none", health: "connected" },
          { id: "work", url: "http://127.0.0.1:8888", default: false, auth: "none", health: "connected" },
        ],
      }),
    );
    setActiveKataDaemon("home");
    const payRent = initialIssues[0]!;
    const rowsByDaemon = { home: [payRent], work: [initialIssues[1]!] };
    const api = createDaemonWorkspaceAPI(rowsByDaemon);
    let homeCursorCalls = 0;
    vi.mocked(api.events).mockImplementation(async (_query, opts) => {
      const daemonID = opts?.daemonId ?? vi.mocked(api.bindWorkflowDaemon!).mock.calls.at(-1)?.[0];
      if (daemonID === "home" && ++homeCursorCalls === 2) {
        rowsByDaemon.home = [];
        return { reset_required: true, reset_after_id: 1, events: [], next_after_id: 1 };
      }
      return { reset_required: false, events: [], next_after_id: 0 };
    });
    vi.mocked(api.issue).mockImplementation(async (uid) => detail(uid, [payRent]));
    vi.mocked(api.instance).mockImplementation(async (opts) => {
      const daemonID = opts?.daemonId ?? vi.mocked(api.bindWorkflowDaemon!).mock.calls.at(-1)?.[0];
      if (daemonID === "work") throw new Error("work unavailable");
      return { instance_uid: "instance-1", version: "dev", schema_version: 1 };
    });
    saveKataWorkspaceState("home", {
      view: "all",
      filters: { scope: { kind: "all" }, status: "open", owner: "", label: "", query: "" },
      selectedIssueUID: payRent.uid,
    });

    const { component } = render(KataWorkspaceRouteHost, {
      props: { api, initialIssue: payRent.uid },
    });
    await waitFor(() => expect(screen.getByRole("heading", { name: "Pay rent" })).toBeTruthy());

    await fireEvent.click(screen.getByTestId("daemon-chip"));
    const workDaemonRow = (await screen.findByTestId("daemon-row-work")) as HTMLButtonElement;
    await waitFor(() => expect(workDaemonRow.disabled).toBe(false));
    await fireEvent.click(workDaemonRow);

    await waitFor(() => {
      expect(getActiveKataDaemon()).toBe("home");
      expect(component.route().issue).toBeNull();
      expect(screen.getByRole("region", { name: "Task detail" }).textContent).toContain("Select a task");
    });
    expect(loadKataWorkspaceState("home")).toMatchObject({
      view: "all",
      filters: { scope: { kind: "all" }, status: "open", owner: "", label: "", query: "" },
      selectedIssueUID: null,
    });
  });

  it("keeps the captured workspace when rollback health recovery fails", async () => {
    vi.spyOn(globalThis, "fetch").mockImplementation(async () =>
      Response.json({
        daemons: [
          { id: "home", url: "http://127.0.0.1:7777", default: true, auth: "none", health: "connected" },
          { id: "work", url: "http://127.0.0.1:8888", default: false, auth: "none", health: "connected" },
        ],
      }),
    );
    const home = initialIssues[0]!;
    const api = createDaemonWorkspaceAPI({ home: [home], work: [initialIssues[1]!] });
    const homeSnapshot = {
      view: "all" as const,
      filters: { scope: { kind: "all" as const }, status: "open" as const, owner: "", label: "", query: "" },
      selectedIssueUID: home.uid,
    };
    const workSnapshot = {
      view: "all" as const,
      filters: { scope: { kind: "all" as const }, status: "open" as const, owner: "", label: "", query: "" },
      selectedIssueUID: null,
    };
    saveKataWorkspaceState("home", homeSnapshot);
    saveKataWorkspaceState("work", workSnapshot);
    let targetAttempted = false;
    vi.mocked(api.instance).mockImplementation(async (opts) => {
      const daemonID = opts?.daemonId ?? vi.mocked(api.bindWorkflowDaemon!).mock.calls.at(-1)?.[0];
      if (daemonID === "work") {
        targetAttempted = true;
        throw new Error("work unavailable");
      }
      if (targetAttempted && daemonID === "home") throw new Error("home unavailable");
      return { instance_uid: "instance-1", version: "dev", schema_version: 1 };
    });
    const onRouteStateChange = vi.fn();

    render(KataWorkspace, { props: { api, onRouteStateChange } });
    await waitFor(() => expect(screen.getByRole("heading", { name: "Pay rent" })).toBeTruthy());
    onRouteStateChange.mockClear();

    await fireEvent.click(screen.getByTestId("daemon-chip"));
    const workDaemonRow = (await screen.findByTestId("daemon-row-work")) as HTMLButtonElement;
    await waitFor(() => expect(workDaemonRow.disabled).toBe(false));
    await fireEvent.click(workDaemonRow);

    await waitFor(() => expect(screen.getByRole("status", { name: "Connection: error" })).toBeTruthy());
    expect(getActiveKataDaemon()).toBe("home");
    expect(screen.getByRole("heading", { name: "Pay rent" })).toBeTruthy();
    expect(screen.getByRole("combobox", { name: "Status: Open" })).toBeTruthy();
    expect((screen.getByLabelText("Search tasks") as HTMLInputElement).value).toBe("");
    expect(onRouteStateChange).not.toHaveBeenCalled();
    expect(loadKataWorkspaceState("home")).toEqual(homeSnapshot);
    expect(loadKataWorkspaceState("work")).toEqual(workSnapshot);
    expect(screen.getByText(/retained Kata workspace is read-only/)).toBeTruthy();
    expect((screen.getByLabelText("Kata tasks").parentElement as HTMLElement & { inert: boolean }).inert).toBe(true);

    targetAttempted = false;
    await fireEvent.click(screen.getByRole("button", { name: "Retry" }));

    await waitFor(() => {
      expect(screen.queryByText(/retained Kata workspace is read-only/)).toBeNull();
      expect((screen.getByLabelText("Kata tasks").parentElement as HTMLElement & { inert: boolean }).inert).toBe(false);
    });
    expect(getActiveKataDaemon()).toBe("home");
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
    const workDaemonRow = (await screen.findByTestId("daemon-row-work")) as HTMLButtonElement;
    await waitFor(() => expect(workDaemonRow.disabled).toBe(false));
    await fireEvent.click(workDaemonRow);

    await waitFor(() => {
      expect(getActiveKataDaemon()).toBe("work");
      expect(screen.getByTestId("daemon-chip").textContent).toContain("work");
      expect(screen.getByRole("heading", { name: "Email Susan re: Q3" })).toBeTruthy();
    });
    expect(screen.queryByRole("heading", { name: "Pay rent" })).toBeNull();
    expect(api.issues).toHaveBeenCalledTimes(2);
    await waitFor(() => expect(api.bindWorkflowDaemon).toHaveBeenLastCalledWith("work"));
  });

  it("clears a stale routed task error after accepting a daemon switch", async () => {
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
    vi.mocked(api.issue).mockImplementation(async (uid: string) => {
      if (uid === "issue-missing") {
        throw new KataTaskAPIError({
          status: 404,
          code: "not_found",
          message: "Kata task not found",
          headers: new Headers(),
        });
      }
      return detail(uid, [initialIssues[0]!, initialIssues[1]!]);
    });

    render(KataWorkspaceRouteHost, {
      props: {
        api,
        initialIssue: "issue-missing",
      },
    });

    await waitFor(() => {
      expect(screen.getByRole("alert").textContent).toContain("Kata task not found");
    });

    await fireEvent.click(screen.getByTestId("daemon-chip"));
    const workDaemonRow = (await screen.findByTestId("daemon-row-work")) as HTMLButtonElement;
    await waitFor(() => expect(workDaemonRow.disabled).toBe(false));
    await fireEvent.click(workDaemonRow);

    await waitFor(() => {
      expect(getActiveKataDaemon()).toBe("work");
      expect(screen.getByRole("heading", { name: "Email Susan re: Q3" })).toBeTruthy();
    });
    expect(screen.queryByRole("alert")).toBeNull();
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

    render(KataWorkspaceRouteHost, {
      props: {
        api,
        initialIssue: "issue-pay-rent",
        onSelectedIssueChange,
      },
    });

    await waitFor(() => {
      expect(screen.getByRole("heading", { name: "Pay rent" })).toBeTruthy();
    });
    vi.mocked(api.issue).mockClear();

    await fireEvent.click(screen.getByTestId("daemon-chip"));
    const emptyDaemonRow = screen.getByTestId("daemon-row-empty") as HTMLButtonElement;
    await waitFor(() => expect(emptyDaemonRow.disabled).toBe(false));
    await fireEvent.click(emptyDaemonRow);

    await waitFor(() => {
      expect(getActiveKataDaemon()).toBe("empty");
      expect(screen.getByText("No tasks")).toBeTruthy();
    });
    expect(api.issue).not.toHaveBeenCalled();
    expect(screen.queryByRole("heading", { name: "Pay rent" })).toBeNull();
  });

  it("restores the accepted workspace when a project scope load fails", async () => {
    const { api, search } = createWorkspaceAPI();
    const onRouteStateChange = vi.fn();

    render(KataWorkspaceRouteHost, {
      props: { api, initialIssue: "issue-pay-rent", onRouteStateChange },
    });

    await waitFor(() => expect(screen.getByRole("heading", { name: "Pay rent" })).toBeTruthy());
    const acceptedPersistence = loadKataWorkspaceState("home");
    search.mockRejectedValueOnce(new Error("project scope timed out"));
    onRouteStateChange.mockClear();

    const nav = within(screen.getByRole("complementary", { name: "Kata navigation" }));
    await fireEvent.click(nav.getByRole("button", { name: /^Kata\s+1$/ }));

    await waitFor(() => expect(screen.getByRole("alert").textContent).toContain("project scope timed out"));
    expect(screen.getByRole("heading", { name: "Pay rent" })).toBeTruthy();
    expect(screen.getByRole("button", { name: /Pay rent/ })).toBeTruthy();
    expect(onRouteStateChange).not.toHaveBeenCalled();
    expect(loadKataWorkspaceState("home")).toEqual(acceptedPersistence);
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
    const nav = within(screen.getByRole("complementary", { name: "Kata navigation" }));
    expect(nav.getByRole("button", { name: /^Finances\s+1$/ })).toBeTruthy();
    expect(screen.getByText("Pay rent body")).toBeTruthy();

    await fireEvent.click(nav.getByRole("button", { name: /^Kata\s+1$/ }));

    await waitFor(() => {
      expect(screen.getByRole("heading", { name: "Email Susan re: Q3" })).toBeTruthy();
    });
    expect(screen.getByText("Email Susan re: Q3 body")).toBeTruthy();
    expect(search).toHaveBeenCalledWith(
      expect.objectContaining({ scope: { kind: "project", project_uid: "project-kata" } }),
      expect.objectContaining({ signal: expect.any(AbortSignal) }),
    );
  });

  it("creates a workspace with the accepted view daemon after the browser default changes", async () => {
    vi.spyOn(globalThis, "fetch").mockImplementation(async () =>
      Response.json({
        daemons: [
          {
            id: "home",
            url: "http://127.0.0.1:7777",
            default: false,
            auth: "none",
            health: "connected",
          },
          {
            id: "work",
            url: "http://127.0.0.1:8888",
            default: true,
            auth: "none",
            health: "connected",
          },
        ],
      }),
    );
    const target: KataWorkspaceTarget = {
      available: true,
      repo: {
        provider: "github",
        platform_host: "github.com",
        owner: "acme",
        name: "middleman",
        repo_path: "acme/middleman",
        capabilities: defaultProviderCapabilities,
      },
      item_type: "kata_task",
      item_key: "issue-pay-rent",
    };
    const createdWorkspace = {
      id: "workspace-kata",
      item_type: "kata_task",
      item_key: "issue-pay-rent",
      git_head_ref: "middleman/kata/pay-rent",
      status: "creating",
    } as const;
    const workspaceCreate = deferred<typeof createdWorkspace>();
    mockCreateKataWorkspaceForTask.mockReturnValue(workspaceCreate.promise);
    const { api } = createWorkspaceAPI();
    const loadIssues = vi.mocked(api.issues).getMockImplementation()!;
    vi.mocked(api.issues).mockImplementation(async (query) => ({
      ...(await loadIssues(query)),
      daemon_id: "home",
    }));
    vi.mocked(api.issue).mockImplementation(async (uid: string) => ({
      ...detail(uid, initialIssues),
      workspace_target: target,
    }));

    render(KataWorkspace, { props: { api, selectedIssueUID: "issue-pay-rent" } });

    await waitFor(() => {
      expect(screen.getByRole("heading", { name: "Pay rent" })).toBeTruthy();
      expect(screen.getByRole("button", { name: "Create workspace" })).toBeTruthy();
    });
    await waitForWorkspaceWritable();
    await fireEvent.click(screen.getByRole("button", { name: "Create workspace" }));
    await fireEvent.click(screen.getByTestId("daemon-chip"));
    const workDaemonRow = screen.getByTestId("daemon-row-work") as HTMLButtonElement;
    expect(workDaemonRow.disabled).toBe(true);
    workspaceCreate.resolve(createdWorkspace);

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
      expect(workDaemonRow.disabled).toBe(false);
    });
  });

  it("does not persist workspace fields when creating a terminal workspace", async () => {
    acceptHomeDaemon();
    const persisted = {
      view: "all" as const,
      filters: { scope: { kind: "all" as const }, status: "open" as const, owner: "", label: "", query: "" },
      selectedIssueUID: null,
    };
    const target: KataWorkspaceTarget = {
      available: true,
      repo: {
        provider: "github",
        platform_host: "github.com",
        owner: "acme",
        name: "middleman",
        repo_path: "acme/middleman",
        capabilities: defaultProviderCapabilities,
      },
      item_type: "kata_task",
      item_key: "issue-pay-rent",
    };
    mockCreateKataWorkspaceForTask.mockResolvedValue({
      id: "workspace-kata",
      item_type: "kata_task",
      item_key: "issue-pay-rent",
      git_head_ref: "middleman/kata/pay-rent",
      status: "creating",
    });
    const { api } = createWorkspaceAPI();
    vi.mocked(api.issue).mockImplementation(async (uid: string) => ({
      ...detail(uid, initialIssues),
      workspace_target: target,
    }));
    saveKataWorkspaceState("home", persisted);

    render(KataWorkspace, { props: { api, selectedIssueUID: "issue-pay-rent" } });
    await waitFor(() => expect(screen.getByRole("button", { name: "Create workspace" })).toBeTruthy());
    await waitForWorkspaceWritable();

    await fireEvent.click(screen.getByRole("button", { name: "Create workspace" }));

    await waitFor(() => expect(mockNavigate).toHaveBeenCalledWith("/terminal/workspace-kata"));
    expect(loadKataWorkspaceState("home")).toEqual(persisted);
  });

  it("flashes workspace creation failures and leaves the action retryable", async () => {
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
    const target: KataWorkspaceTarget = {
      available: true,
      repo: {
        provider: "github",
        platform_host: "github.com",
        owner: "acme",
        name: "middleman",
        repo_path: "acme/middleman",
        capabilities: defaultProviderCapabilities,
      },
      item_type: "kata_task",
      item_key: "issue-pay-rent",
    };
    mockCreateKataWorkspaceForTask.mockRejectedValueOnce(new Error("workspace unavailable"));
    const { api } = createWorkspaceAPI();
    vi.mocked(api.issue).mockImplementation(async (uid: string) => ({
      ...detail(uid, initialIssues),
      workspace_target: target,
    }));

    render(KataWorkspace, { props: { api, selectedIssueUID: "issue-pay-rent" } });
    const createButton = await screen.findByRole("button", { name: "Create workspace" });
    await waitForWorkspaceWritable();
    await fireEvent.click(createButton);

    await waitFor(() => {
      expect(flash.getFlash()).toMatchObject({ message: "workspace unavailable", tone: "danger" });
    });
    expect(screen.queryByText("workspace unavailable")).toBeNull();
    expect(createButton.hasAttribute("disabled")).toBe(false);
  });

  it("keeps the create workspace action visible while refreshing the same selected task", async () => {
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
    const target: KataWorkspaceTarget = {
      available: true,
      repo: {
        provider: "github",
        platform_host: "github.com",
        owner: "acme",
        name: "middleman",
        repo_path: "acme/middleman",
        capabilities: defaultProviderCapabilities,
      },
      item_type: "kata_task",
      item_key: "issue-pay-rent",
    };
    const { api, addLabel } = createWorkspaceAPI();
    vi.mocked(api.issue).mockImplementation(async (uid: string) => ({
      ...detail(uid, initialIssues),
      workspace_target: target,
    }));

    render(KataWorkspace, { props: { api, selectedIssueUID: "issue-pay-rent" } });

    await waitFor(() => {
      expect(screen.getByRole("button", { name: "Create workspace" })).toBeTruthy();
    });
    await waitForWorkspaceWritable();

    await fireEvent.click(screen.getByRole("button", { name: "Add label" }));
    await fireEvent.input(screen.getByLabelText("New label"), { target: { value: "blocked" } });
    await fireEvent.keyDown(screen.getByLabelText("New label"), { key: "Enter" });

    await waitFor(() => {
      expect(addLabel).toHaveBeenCalledWith(expect.objectContaining({ ref: "issue-pay-rent" }), "middleman", "blocked");
    });
    expect(screen.getByRole("button", { name: "Create workspace" })).toBeTruthy();
  });

  it("renders the workspace action together with the detail payload", async () => {
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
    const slowDetail = deferred<ReturnType<typeof detail>>();
    let holdEmailSusanDetail = false;
    vi.mocked(api.issue).mockImplementation(async (uid: string) => {
      if (holdEmailSusanDetail && uid === "issue-email-susan") return slowDetail.promise;
      return detail(uid, initialIssues);
    });

    render(KataWorkspace, { props: { api, selectedIssueUID: "issue-pay-rent" } });
    await waitFor(() => {
      expect(screen.getByRole("heading", { name: "Pay rent" })).toBeTruthy();
    });

    holdEmailSusanDetail = true;
    await fireEvent.click(screen.getByRole("button", { name: /Email Susan re: Q3/ }));
    expect(screen.queryByRole("heading", { name: "Email Susan re: Q3" })).toBeNull();

    // The combined payload carries the target: pane and action land in the
    // same flush, with no separate resolution that could pop in later.
    slowDetail.resolve({
      ...detail("issue-email-susan", initialIssues),
      workspace_target: {
        available: true,
        repo: {
          provider: "github",
          platform_host: "github.com",
          owner: "acme",
          name: "middleman",
          repo_path: "acme/middleman",
          capabilities: defaultProviderCapabilities,
        },
        item_type: "kata_task",
        item_key: "issue-email-susan",
      },
    });
    await waitFor(() => {
      expect(screen.getByRole("heading", { name: "Email Susan re: Q3" })).toBeTruthy();
      expect(screen.getByRole("button", { name: "Create workspace" })).toBeTruthy();
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
    const { api } = createWorkspaceAPI();
    vi.mocked(api.issue).mockImplementation(async (uid: string) => ({
      ...detail(uid, initialIssues),
      workspace_target: {
        available: true,
        existing_workspace: {
          id: "workspace-existing",
          status: "ready",
        },
        item_type: "kata_task",
        item_key: "issue-pay-rent",
      },
    }));

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
      expect(search).toHaveBeenLastCalledWith(
        {
          scope: { kind: "project", project_uid: "project-kata" },
          status: "all",
          owner: "fixture-user",
          label: "work",
          query: "q3",
        },
        expect.objectContaining({ signal: expect.any(AbortSignal) }),
      );
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
    await waitFor(() =>
      expect(search).toHaveBeenCalledWith(
        expect.objectContaining({ query: "old" }),
        expect.objectContaining({ signal: expect.any(AbortSignal) }),
      ),
    );
    await fireEvent.input(screen.getByLabelText("Search tasks"), { target: { value: "new" } });
    await waitFor(() =>
      expect(search).toHaveBeenCalledWith(
        expect.objectContaining({ query: "new" }),
        expect.objectContaining({ signal: expect.any(AbortSignal) }),
      ),
    );

    oldSearch.resolve({
      filters: { scope: { kind: "all" }, status: "open", owner: "", label: "", query: "old" },
      issues: [initialIssues[0]!],
      fetched_at: fetchedAt,
    });
    await waitFor(() => expect(oldSearchSettled).toBe(true));

    expect(screen.queryByText("Loading snapshot")).toBeTruthy();
    await fireEvent.click(screen.getByTestId("daemon-chip"));
    const homeDaemonRow = screen.getByTestId("daemon-row-home") as HTMLButtonElement;
    expect(homeDaemonRow.disabled).toBe(false);

    newSearch.resolve({
      filters: { scope: { kind: "all" }, status: "open", owner: "", label: "", query: "new" },
      issues: [initialIssues[1]!],
      fetched_at: fetchedAt,
    });
    await waitFor(() => {
      expect(screen.queryByText("Loading snapshot")).toBeNull();
      expect(screen.getByRole("heading", { name: "Email Susan re: Q3" })).toBeTruthy();
    });
    expect(homeDaemonRow.disabled).toBe(false);
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
    await waitFor(() =>
      expect(search).toHaveBeenCalledWith(
        expect.objectContaining({ query: "old" }),
        expect.objectContaining({ signal: expect.any(AbortSignal) }),
      ),
    );
    await fireEvent.input(screen.getByLabelText("Search tasks"), { target: { value: "new" } });
    await waitFor(() =>
      expect(search).toHaveBeenCalledWith(
        expect.objectContaining({ query: "new" }),
        expect.objectContaining({ signal: expect.any(AbortSignal) }),
      ),
    );

    newSearch.resolve({
      filters: { scope: { kind: "all" }, status: "open", owner: "", label: "", query: "new" },
      issues: [initialIssues[1]!],
      fetched_at: fetchedAt,
    });

    await waitFor(() => {
      expect(screen.queryByText("Loading snapshot")).toBeNull();
      expect(screen.getByRole("heading", { name: "Email Susan re: Q3" })).toBeTruthy();
    });
    await fireEvent.click(screen.getByTestId("daemon-chip"));
    const homeDaemonRow = screen.getByTestId("daemon-row-home") as HTMLButtonElement;
    expect(homeDaemonRow.disabled).toBe(false);

    oldSearch.resolve({
      filters: { scope: { kind: "all" }, status: "open", owner: "", label: "", query: "old" },
      issues: [initialIssues[0]!],
      fetched_at: fetchedAt,
    });
    await waitFor(() => expect(oldSearchSettled).toBe(true));

    expect(screen.queryByText("Loading snapshot")).toBeNull();
    expect(screen.getByRole("heading", { name: "Email Susan re: Q3" })).toBeTruthy();
    expect(homeDaemonRow.disabled).toBe(false);
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
    await waitFor(() => expect((screen.getByTestId("daemon-chip") as HTMLButtonElement).disabled).toBe(false));
    await fireEvent.click(screen.getByTestId("daemon-chip"));
    expect(within(await screen.findByTestId("daemon-row-home")).getByText("Authentication required")).toBeTruthy();
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
    await waitForWorkspaceWritable();
    const detailRegion = within(screen.getByRole("region", { name: "Task detail" }));
    await fireEvent.click(detailRegion.getByRole("button", { name: "Owner: fixture-user" }));
    const ownerInput = detailRegion.getByRole("combobox", { name: "Owner" }) as HTMLInputElement;
    await fireEvent.input(ownerInput, { target: { value: "agent:new" } });
    await fireEvent.keyDown(ownerInput, { key: "Enter" });

    await waitFor(() => {
      expect(flash.getFlash()).toMatchObject({ message: "owner unavailable", tone: "danger" });
    });
    expect(screen.queryByText("owner unavailable")).toBeNull();
    expect(ownerInput.value).toBe("agent:new");
    expect(screen.getByTestId("daemon-chip").textContent).not.toContain("owner unavailable");
  });

  it("does not surface a stale move failure after navigating A to B to A", async () => {
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
    const pendingMove = deferred<never>();
    const { api, moveIssue } = createWorkspaceAPI();
    moveIssue.mockImplementationOnce(() => pendingMove.promise);

    const { rerender } = render(KataWorkspace, {
      props: { api, selectedIssueUID: "issue-pay-rent" },
    });

    await screen.findByRole("heading", { name: "Pay rent" });
    await waitForWorkspaceWritable();
    const detail = within(screen.getByRole("region", { name: "Task detail" }));
    await fireEvent.click(detail.getByRole("button", { name: "More actions" }));
    await fireEvent.click(detail.getByRole("menuitem", { name: "Move to another project" }));
    await fireEvent.click(detail.getByRole("button", { name: /Kata/ }));

    await rerender({ api, selectedIssueUID: "issue-email-susan" });
    await screen.findByRole("heading", { name: "Email Susan re: Q3" });
    await rerender({ api, selectedIssueUID: "issue-pay-rent" });
    await screen.findByRole("heading", { name: "Pay rent" });
    const returnedDetail = within(screen.getByRole("region", { name: "Task detail" }));
    await fireEvent.click(returnedDetail.getByRole("button", { name: "More actions" }));
    expect(returnedDetail.queryByRole("menuitem", { name: "Move to another project" })).toBeNull();
    expect(moveIssue).toHaveBeenCalledTimes(1);

    pendingMove.reject(new Error("old move failed"));
    await pendingMove.promise.catch(() => undefined);
    await Promise.resolve();
    expect(moveIssue).toHaveBeenCalledTimes(1);
    expect(screen.queryByText("old move failed")).toBeNull();
    expect(flash.getFlashes()).toEqual([]);
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
      const binding = vi.mocked(api.bindWorkflowDaemon!).mock.calls.at(-1)?.[0];
      const active = (binding ?? getActiveKataDaemon()) === "work" ? "work" : "home";
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
    const workDaemonRow = (await screen.findByTestId("daemon-row-work")) as HTMLButtonElement;
    await waitFor(() => expect(workDaemonRow.disabled).toBe(false));
    await fireEvent.click(workDaemonRow);

    await waitFor(() => {
      expect(getActiveKataDaemon()).toBe("work");
      const currentDetail = screen.getByRole("region", { name: "Task detail" });
      const currentLinks = within(currentDetail).getByRole("region", { name: "Links" });
      expect(within(currentLinks).getByText("Work linked task")).toBeTruthy();
    });
    const currentDetail = screen.getByRole("region", { name: "Task detail" });
    const currentLinks = within(currentDetail).getByRole("region", { name: "Links" });
    expect(within(currentLinks).queryByText("Home linked task")).toBeNull();
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

    render(KataWorkspaceRouteHost, { props: { api, initialIssue: "issue-pay-rent" } });

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
    await waitForWorkspaceWritable();
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

  it("does not persist workspace fields when unlinking a message", async () => {
    acceptHomeDaemon();
    const persisted = {
      view: "all" as const,
      filters: { scope: { kind: "all" as const }, status: "open" as const, owner: "", label: "", query: "" },
      selectedIssueUID: null,
    };
    const link = messageLink({ message_id: 2001, subject: "Lease renewal" });
    const rows = initialIssues.map((item) =>
      item.uid === "issue-pay-rent" ? { ...item, metadata: { ...item.metadata, mail_links: [link] } } : item,
    );
    const { api } = createWorkspaceAPI(rows);
    saveKataWorkspaceState("home", persisted);

    render(KataWorkspace, { props: { api, selectedIssueUID: "issue-pay-rent" } });
    await waitFor(() => expect(screen.getByRole("button", { name: "Unlink Lease renewal" })).toBeTruthy());
    await waitForWorkspaceWritable();

    await fireEvent.click(screen.getByRole("button", { name: "Unlink Lease renewal" }));

    await waitFor(() => expect(screen.queryByText("Lease renewal")).toBeNull());
    expect(loadKataWorkspaceState("home")).toEqual(persisted);
  });
});
