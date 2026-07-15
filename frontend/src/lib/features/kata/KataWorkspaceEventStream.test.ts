import { cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/svelte";
import { afterEach, beforeEach, describe, expect, it, vi } from "vite-plus/test";

import type {
  KataTaskAPI,
  KataTaskEventsQuery,
  KataTaskEventsResponse,
  KataTaskIssuesQuery,
  KataTaskSummary,
} from "../../api/kata/taskTypes.js";
import { buildKataTaskView } from "../../api/kata/taskViewBuilder.js";
import {
  getActiveKataDaemon,
  getDefaultKataDaemon,
  setActiveKataDaemon,
} from "../../stores/active-kata-daemon.svelte.js";
import KataWorkspace from "./KataWorkspace.svelte";
import KataWorkspaceRouteHost from "./KataWorkspaceRouteHost.svelte";
import { loadKataWorkspaceState, saveKataWorkspaceState } from "./kataWorkspacePersistence.js";
import {
  createDaemonWorkspaceAPI,
  createWorkspaceAPI,
  deferred,
  fetchedAt,
  initialIssues,
  projects,
  resetKataWorkspaceTestState,
} from "./KataWorkspaceTestSupport.js";

// Mounting KataWorkspace always fetches the daemon roster and opens the live
// event stream; tests that are not about stream behavior still need both to
// succeed, or the stream error recovery reloads the view mid-test.
function mockDaemonAndStreamFetch(): void {
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
}

describe("KataWorkspace", () => {
  beforeEach(() => {
    resetKataWorkspaceTestState();
  });

  afterEach(() => {
    cleanup();
    vi.useRealTimers();
    vi.restoreAllMocks();
  });

  it("reloads the visible workspace from the live Kata event stream", async () => {
    let streamController: ReadableStreamDefaultController<Uint8Array> | undefined;
    let streamHeaders: Headers | undefined;
    vi.spyOn(globalThis, "fetch").mockImplementation(async (input, init) => {
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
        streamHeaders = new Headers(init?.headers);
        return new Response(
          new ReadableStream<Uint8Array>({
            start(controller) {
              streamController = controller;
            },
            cancel() {
              streamController = undefined;
            },
          }),
          { status: 200, headers: { "Content-Type": "text/event-stream" } },
        );
      }
      return new Response("not found", { status: 404 });
    });
    const rowsByDaemon = { home: [initialIssues[0]!] };
    const api = createDaemonWorkspaceAPI(rowsByDaemon);
    vi.mocked(api.events).mockImplementation(async (query = {}) => ({
      reset_required: false,
      events: [],
      next_after_id: query.after_id === 0 ? 5 : (query.after_id ?? 0),
    }));

    const { unmount } = render(KataWorkspace, { props: { api } });

    await waitFor(() => {
      expect(screen.getByRole("button", { name: /Pay rent/ })).toBeTruthy();
      expect(streamController).toBeTruthy();
    });
    expect(streamHeaders?.get("X-Middleman-Kata-Daemon")).toBe("home");
    expect(streamHeaders?.get("Last-Event-ID")).toBe("5");

    rowsByDaemon.home = [initialIssues[1]!];
    streamController?.enqueue(
      new TextEncoder().encode(
        `id: 6\nevent: sync.reset_required\ndata: ${JSON.stringify({ event_id: 6, reset_after_id: 6 })}\n\n`,
      ),
    );

    await waitFor(() => {
      expect(screen.getByRole("button", { name: /Email Susan re: Q3/ })).toBeTruthy();
      expect(screen.getByText("Select a task")).toBeTruthy();
    });
    expect(screen.queryByRole("heading", { name: "Pay rent" })).toBeNull();

    unmount();
    await waitFor(() => {
      expect(streamController).toBeUndefined();
    });
  });

  it.each([
    {
      name: "deletion",
      rows: [],
      event: { type: "issue.deleted" },
    },
    {
      name: "move out of scope",
      rows: [{ ...initialIssues[0]!, project_uid: "project-kata", project_id: projects[2]!.id, project_name: "Kata" }],
      event: { type: "issue.moved" },
    },
    {
      name: "status reclassification",
      rows: [{ ...initialIssues[0]!, status: "closed" as const, closed_at: fetchedAt }],
      event: { type: "issue.closed" },
    },
    {
      name: "owner reclassification",
      rows: [{ ...initialIssues[0]!, owner: "other" }],
      event: { type: "issue.owner_changed" },
      filters: { owner: "fixture-user" },
    },
    {
      name: "label reclassification",
      rows: [{ ...initialIssues[0]!, labels: ["other"] }],
      event: { type: "issue.label_removed" },
      filters: { label: "urgent" },
    },
    {
      name: "query reclassification",
      rows: [{ ...initialIssues[0]!, title: "Renew lease" }],
      event: { type: "issue.edited" },
      filters: { query: "Pay" },
    },
  ])(
    "clears a persisted selection after a $name event refresh loses raw-result membership",
    async ({ rows, event, filters: filterPatch = {} }) => {
      let streamController: ReadableStreamDefaultController<Uint8Array> | undefined;
      vi.spyOn(globalThis, "fetch").mockImplementation(async (input) => {
        const url = new URL(input instanceof Request ? input.url : String(input), window.location.origin);
        if (url.pathname === "/api/v1/kata/daemons") {
          return Response.json({
            daemons: [{ id: "home", url: "http://127.0.0.1:7777", default: true, auth: "none", health: "connected" }],
          });
        }
        if (url.pathname === "/api/v1/kata/proxy/api/v1/events/stream") {
          return new Response(
            new ReadableStream<Uint8Array>({
              start(controller) {
                streamController = controller;
              },
            }),
            { status: 200, headers: { "Content-Type": "text/event-stream" } },
          );
        }
        return new Response("not found", { status: 404 });
      });
      setActiveKataDaemon("home");
      const rowsByDaemon = { home: [initialIssues[0]!] };
      const api = createDaemonWorkspaceAPI(rowsByDaemon);
      const filters = {
        scope: { kind: "project" as const, project_uid: "project-finances" },
        status: "open" as const,
        owner: "",
        label: "",
        query: "",
        ...filterPatch,
      };
      vi.mocked(api.search).mockImplementation(async (requestedFilters) => ({
        filters: requestedFilters,
        issues: rowsByDaemon.home.filter(
          (issue) =>
            (requestedFilters.scope.kind !== "project" || issue.project_uid === requestedFilters.scope.project_uid) &&
            (requestedFilters.owner === "" || issue.owner === requestedFilters.owner) &&
            (requestedFilters.label === "" || issue.labels?.includes(requestedFilters.label)) &&
            (requestedFilters.query === "" || issue.title.includes(requestedFilters.query)),
        ),
        fetched_at: fetchedAt,
      }));
      saveKataWorkspaceState("home", { view: "all", filters, selectedIssueUID: "issue-pay-rent" });
      render(KataWorkspaceRouteHost, {
        props: { api, initialScope: "project-finances", initialIssue: "issue-pay-rent" },
      });
      await waitFor(() => {
        expect(screen.getByRole("heading", { name: "Pay rent" })).toBeTruthy();
        expect(streamController).toBeTruthy();
      });
      rowsByDaemon.home = rows;
      streamController?.enqueue(
        new TextEncoder().encode(
          `id: 6\nevent: ${event.type}\ndata: ${JSON.stringify({
            event_id: 6,
            event_uid: "event-selection-reclassified",
            origin_instance_uid: "instance-1",
            project_id: projects[1]!.id,
            project_uid: "project-finances",
            project_name: "Finances",
            issue_id: initialIssues[0]!.id,
            issue_uid: initialIssues[0]!.uid,
            issue_short_id: initialIssues[0]!.short_id,
            actor: "fixture-user",
            created_at: fetchedAt,
            payload: event,
          })}\n\n`,
        ),
      );

      await waitFor(() => expect(loadKataWorkspaceState("home")?.selectedIssueUID).toBeNull());
      expect(screen.getByText("Select a task")).toBeTruthy();
      expect(loadKataWorkspaceState("home")?.filters).toEqual(filters);
    },
  );

  it("starts streaming and retries accepted cursor membership after a later page failure", async () => {
    const streamControllers: ReadableStreamDefaultController<Uint8Array>[] = [];
    vi.spyOn(globalThis, "fetch").mockImplementation(async (input) => {
      const url = new URL(input instanceof Request ? input.url : String(input), window.location.origin);
      if (url.pathname === "/api/v1/kata/daemons") {
        return Response.json({
          daemons: [{ id: "home", url: "http://127.0.0.1:7777", default: true, auth: "none", health: "connected" }],
        });
      }
      if (url.pathname === "/api/v1/kata/proxy/api/v1/events/stream") {
        return new Response(
          new ReadableStream<Uint8Array>({
            start(controller) {
              streamControllers.push(controller);
            },
          }),
          { status: 200, headers: { "Content-Type": "text/event-stream" } },
        );
      }
      return new Response("not found", { status: 404 });
    });
    setActiveKataDaemon("home");
    const rowsByDaemon = { home: [initialIssues[0]!, initialIssues[1]!] };
    const api = createDaemonWorkspaceAPI(rowsByDaemon);
    const cursorPage = deferred<KataTaskEventsResponse>();
    const retryPage = deferred<KataTaskEventsResponse>();
    let afterEightyeightReads = 0;
    vi.mocked(api.events).mockImplementation(async (query) => {
      if (query?.issue_uid) {
        return { reset_required: false, events: [], next_after_id: 0 };
      }
      if (query?.after_id === 0) return cursorPage.promise;
      if (query?.after_id === 88) {
        afterEightyeightReads += 1;
        if (afterEightyeightReads === 1) throw new Error("later cursor page failed");
        if (afterEightyeightReads === 2) return retryPage.promise;
      }
      return { reset_required: false, events: [], next_after_id: query?.after_id ?? 0 };
    });
    const persisted = {
      view: "all" as const,
      filters: { scope: { kind: "all" as const }, status: "open" as const, owner: "", label: "", query: "" },
      selectedIssueUID: initialIssues[0]!.uid,
    };
    saveKataWorkspaceState("home", persisted);
    const onRouteStateChange = vi.fn();
    render(KataWorkspace, {
      props: { api, selectedIssueUID: initialIssues[0]!.uid, onRouteStateChange },
    });
    await waitFor(() => expect(screen.getByRole("heading", { name: "Pay rent" })).toBeTruthy());

    rowsByDaemon.home = [initialIssues[1]!];
    vi.mocked(api.issue).mockImplementation(async (uid) => {
      if (uid === initialIssues[0]!.uid) throw new Error("detail unavailable");
      return {
        issue: { ...initialIssues[1]!, body: "Email Susan re: Q3 body" },
        comments: [],
        labels: [],
        links: [],
        children: [],
      };
    });
    cursorPage.resolve({
      reset_required: false,
      events: [
        {
          event_id: 88,
          event_uid: "event-removed-before-pagination-failure",
          origin_instance_uid: "instance-1",
          type: "issue.deleted",
          project_id: initialIssues[0]!.project_id,
          project_uid: initialIssues[0]!.project_uid,
          project_name: initialIssues[0]!.project_name,
          issue_id: initialIssues[0]!.id,
          issue_uid: initialIssues[0]!.uid,
          issue_short_id: initialIssues[0]!.short_id,
          actor: "fixture-user",
          created_at: fetchedAt,
        },
      ],
      next_after_id: 88,
    });

    await waitFor(() => expect(screen.getByText("Select a task")).toBeTruthy());
    await waitFor(() =>
      expect(vi.mocked(api.events)).toHaveBeenCalledWith({ after_id: 88, limit: 100 }, { daemonId: "home" }),
    );
    await waitFor(() => expect(streamControllers).toHaveLength(1));
    expect(screen.getByText(/later cursor page failed/)).toBeTruthy();
    expect(screen.getByRole("button", { name: "Retry" })).toBeTruthy();
    expect(loadKataWorkspaceState("home")?.selectedIssueUID).toBeNull();
    expect(onRouteStateChange).toHaveBeenCalledWith({ view: null, scope: null, issue: null }, { replace: true });

    await fireEvent.click(screen.getByRole("button", { name: "Retry" }));

    await waitFor(() =>
      expect(vi.mocked(api.events)).toHaveBeenLastCalledWith({ after_id: 88, limit: 100 }, { daemonId: "home" }),
    );
    expect(screen.getByText(/later cursor page failed/).closest('[role="alert"]')).toBeTruthy();
    expect((screen.getByRole("button", { name: "Retrying…" }) as HTMLButtonElement).disabled).toBe(true);
    retryPage.reject(new Error("retry still failing"));

    await waitFor(() => expect(screen.getByText(/retry still failing/).closest('[role="alert"]')).toBeTruthy());
    expect(screen.getByRole("button", { name: "Retry" })).toBeTruthy();

    await fireEvent.click(screen.getByRole("button", { name: "Retry" }));
    await waitFor(() =>
      expect(vi.mocked(api.events)).toHaveBeenLastCalledWith({ after_id: 88, limit: 100 }, { daemonId: "home" }),
    );
    await waitFor(() => expect(screen.queryByText(/later cursor page failed|retry still failing/)).toBeNull());
    expect(streamControllers).toHaveLength(1);
  });

  it("ignores a retry completion from a daemon after switching away", async () => {
    const streamControllers: ReadableStreamDefaultController<Uint8Array>[] = [];
    vi.spyOn(globalThis, "fetch").mockImplementation(async (input) => {
      const url = new URL(input instanceof Request ? input.url : String(input), window.location.origin);
      if (url.pathname === "/api/v1/kata/daemons") {
        return Response.json({
          daemons: [
            { id: "home", url: "http://127.0.0.1:7777", default: true, auth: "none", health: "connected" },
            { id: "work", url: "http://127.0.0.1:8888", default: false, auth: "none", health: "connected" },
          ],
        });
      }
      if (url.pathname === "/api/v1/kata/proxy/api/v1/events/stream") {
        return new Response(
          new ReadableStream<Uint8Array>({
            start(controller) {
              streamControllers.push(controller);
            },
          }),
          { status: 200, headers: { "Content-Type": "text/event-stream" } },
        );
      }
      return new Response("not found", { status: 404 });
    });
    setActiveKataDaemon("home");
    const api = createDaemonWorkspaceAPI({ home: [initialIssues[0]!], work: [initialIssues[1]!] });
    const delayedHomeRetry = deferred<KataTaskEventsResponse>();
    let homeRetry = false;
    vi.mocked(api.events).mockImplementation(async (query = {}, opts) => {
      const daemonID =
        opts?.daemonId ?? vi.mocked(api.bindWorkflowDaemon!).mock.calls.at(-1)?.[0] ?? getActiveKataDaemon();
      if (query.issue_uid) return { reset_required: false, events: [], next_after_id: 0 };
      if (daemonID === "home" && query.after_id === 0) {
        return {
          reset_required: false,
          events: [
            {
              event_id: 5,
              event_uid: "home-cursor-event",
              origin_instance_uid: "instance-1",
              type: "issue.edited",
              project_id: initialIssues[0]!.project_id,
              project_uid: initialIssues[0]!.project_uid,
              project_name: initialIssues[0]!.project_name,
              issue_id: initialIssues[0]!.id,
              issue_uid: initialIssues[0]!.uid,
              issue_short_id: initialIssues[0]!.short_id,
              actor: "fixture-user",
              created_at: fetchedAt,
            },
          ],
          next_after_id: 5,
        };
      }
      if (daemonID === "home" && query.after_id === 5) {
        if (!homeRetry) {
          homeRetry = true;
          throw new Error("home cursor failed");
        }
        return delayedHomeRetry.promise;
      }
      return { reset_required: false, events: [], next_after_id: query.after_id ?? 0 };
    });

    render(KataWorkspace, { props: { api } });
    await waitFor(() => expect(screen.getByRole("button", { name: "Retry" })).toBeTruthy());
    expect(streamControllers).toHaveLength(1);

    await fireEvent.click(screen.getByRole("button", { name: "Retry" }));
    await waitFor(() => expect(screen.getByRole("button", { name: "Retrying…" })).toBeTruthy());
    await fireEvent.click(screen.getByTestId("daemon-chip"));
    await fireEvent.click(screen.getByTestId("daemon-row-work"));
    await waitFor(() => expect(getActiveKataDaemon()).toBe("work"));
    await waitFor(() => expect(screen.getByRole("button", { name: /Email Susan re: Q3/ })).toBeTruthy());

    delayedHomeRetry.resolve({ reset_required: false, events: [], next_after_id: 5 });
    await Promise.resolve();
    await Promise.resolve();

    expect(screen.queryByText("home cursor failed")).toBeNull();
    expect(screen.queryByRole("button", { name: "Retry" })).toBeNull();
    await waitFor(() => expect(streamControllers).toHaveLength(2));
  });

  it("does not clear a newer selection when the event refresh removes the prior selection", async () => {
    let streamController: ReadableStreamDefaultController<Uint8Array> | undefined;
    vi.spyOn(globalThis, "fetch").mockImplementation(async (input) => {
      const url = new URL(input instanceof Request ? input.url : String(input), window.location.origin);
      if (url.pathname === "/api/v1/kata/daemons") {
        return Response.json({
          daemons: [{ id: "home", url: "http://127.0.0.1:7777", default: true, auth: "none", health: "connected" }],
        });
      }
      if (url.pathname === "/api/v1/kata/proxy/api/v1/events/stream") {
        return new Response(
          new ReadableStream<Uint8Array>({
            start(controller) {
              streamController = controller;
            },
          }),
          { status: 200, headers: { "Content-Type": "text/event-stream" } },
        );
      }
      return new Response("not found", { status: 404 });
    });
    setActiveKataDaemon("home");
    const replacement = initialIssues[1]!;
    const rowsByDaemon = { home: [initialIssues[0]!, replacement] };
    const api = createDaemonWorkspaceAPI(rowsByDaemon);
    const stalledRefresh = deferred<Awaited<ReturnType<KataTaskAPI["issues"]>>>();
    let refresh = false;
    vi.mocked(api.issues).mockImplementation(async (query) => {
      if (refresh) return stalledRefresh.promise;
      return buildKataTaskView({
        view: query.view,
        issues: rowsByDaemon.home,
        projects,
        today: "2026-05-15",
        fetched_at: fetchedAt,
      });
    });
    const persisted = {
      view: "all" as const,
      filters: { scope: { kind: "all" as const }, status: "open" as const, owner: "", label: "", query: "" },
      selectedIssueUID: initialIssues[0]!.uid,
    };
    saveKataWorkspaceState("home", persisted);
    render(KataWorkspaceRouteHost, { props: { api, initialIssue: initialIssues[0]!.uid } });
    await waitFor(() => expect(streamController).toBeTruthy());

    refresh = true;
    streamController?.enqueue(
      new TextEncoder().encode(
        `id: 6\nevent: issue.deleted\ndata: ${JSON.stringify({
          event_id: 6,
          event_uid: "event-old-selection-deleted",
          origin_instance_uid: "instance-1",
          type: "issue.deleted",
          project_id: initialIssues[0]!.project_id,
          project_uid: initialIssues[0]!.project_uid,
          project_name: initialIssues[0]!.project_name,
          issue_id: initialIssues[0]!.id,
          issue_uid: initialIssues[0]!.uid,
          issue_short_id: initialIssues[0]!.short_id,
          actor: "fixture-user",
          created_at: fetchedAt,
          payload: {},
        })}\n\n`,
      ),
    );
    await waitFor(() => expect(api.issues).toHaveBeenCalledTimes(2));
    await fireEvent.click(screen.getByRole("button", { name: /Email Susan re: Q3/ }));
    await waitFor(() => expect(screen.getByRole("heading", { name: "Email Susan re: Q3" })).toBeTruthy());
    stalledRefresh.resolve(
      buildKataTaskView({ view: "all", issues: [replacement], projects, today: "2026-05-15", fetched_at: fetchedAt }),
    );

    await waitFor(() => expect(loadKataWorkspaceState("home")?.selectedIssueUID).toBe(replacement.uid));
    expect(screen.getByRole("heading", { name: "Email Susan re: Q3" })).toBeTruthy();
  });

  it("does not write a workspace snapshot for an event-only content refresh that preserves selected membership", async () => {
    let streamController: ReadableStreamDefaultController<Uint8Array> | undefined;
    vi.spyOn(globalThis, "fetch").mockImplementation(async (input) => {
      const url = new URL(input instanceof Request ? input.url : String(input), window.location.origin);
      if (url.pathname === "/api/v1/kata/daemons") {
        return Response.json({
          daemons: [{ id: "home", url: "http://127.0.0.1:7777", default: true, auth: "none", health: "connected" }],
        });
      }
      if (url.pathname === "/api/v1/kata/proxy/api/v1/events/stream") {
        return new Response(
          new ReadableStream<Uint8Array>({
            start(controller) {
              streamController = controller;
            },
          }),
          { status: 200, headers: { "Content-Type": "text/event-stream" } },
        );
      }
      return new Response("not found", { status: 404 });
    });
    setActiveKataDaemon("home");
    const rowsByDaemon = { home: [initialIssues[0]!] };
    const api = createDaemonWorkspaceAPI(rowsByDaemon);
    const persisted = {
      view: "all" as const,
      filters: { scope: { kind: "all" as const }, status: "open" as const, owner: "", label: "", query: "" },
      selectedIssueUID: "issue-pay-rent",
    };
    render(KataWorkspace, { props: { api, selectedIssueUID: "issue-pay-rent" } });
    await waitFor(() => {
      expect(screen.getByRole("heading", { name: "Pay rent" })).toBeTruthy();
      expect(streamController).toBeTruthy();
    });
    saveKataWorkspaceState("home", persisted);

    streamController?.enqueue(
      new TextEncoder().encode(
        `id: 6\nevent: issue.edited\ndata: ${JSON.stringify({
          event_id: 6,
          event_uid: "event-content-refresh",
          origin_instance_uid: "instance-1",
          type: "issue.edited",
          project_id: projects[1]!.id,
          project_uid: "project-finances",
          project_name: "Finances",
          issue_id: initialIssues[0]!.id,
          issue_uid: initialIssues[0]!.uid,
          issue_short_id: initialIssues[0]!.short_id,
          actor: "fixture-user",
          created_at: fetchedAt,
          payload: {},
        })}\n\n`,
      ),
    );

    await waitFor(() => expect(vi.mocked(api.issues).mock.calls.length).toBeGreaterThan(1));
    expect(loadKataWorkspaceState("home")).toEqual(persisted);
  });

  it("keeps a row selection made while a stream-triggered reload is in flight", async () => {
    let streamController: ReadableStreamDefaultController<Uint8Array> | undefined;
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
        return new Response(
          new ReadableStream<Uint8Array>({
            start(controller) {
              streamController = controller;
            },
            cancel() {
              streamController = undefined;
            },
          }),
          { status: 200, headers: { "Content-Type": "text/event-stream" } },
        );
      }
      return new Response("not found", { status: 404 });
    });
    const { api } = createWorkspaceAPI();
    const onSelectedIssueChange = vi.fn();

    render(KataWorkspace, { props: { api, onSelectedIssueChange } });

    await waitFor(() => {
      expect(screen.getByRole("button", { name: /Pay rent/ })).toBeTruthy();
      expect(streamController).toBeTruthy();
    });

    // Stall the view reload the reset event triggers so the click below
    // lands while that refetch is still in flight.
    const stalledView = deferred<Awaited<ReturnType<KataTaskAPI["issues"]>>>();
    let reloadStarted = false;
    vi.mocked(api.issues).mockImplementationOnce(async () => {
      reloadStarted = true;
      return stalledView.promise;
    });
    streamController?.enqueue(
      new TextEncoder().encode(
        `id: 6\nevent: sync.reset_required\ndata: ${JSON.stringify({ event_id: 6, reset_after_id: 6 })}\n\n`,
      ),
    );
    await waitFor(() => {
      expect(reloadStarted).toBe(true);
    });

    await fireEvent.click(screen.getByRole("button", { name: /Email Susan re: Q3/ }));
    await waitFor(() => {
      expect(screen.getByRole("heading", { name: "Email Susan re: Q3" })).toBeTruthy();
    });

    stalledView.resolve(
      buildKataTaskView({
        view: "all",
        issues: initialIssues,
        projects,
        today: "2026-05-15",
        fetched_at: fetchedAt,
      }),
    );

    // The reload must not displace the selection the user just made.
    await new Promise((resolve) => setTimeout(resolve, 0));
    await waitFor(() => {
      expect(screen.getByRole("heading", { name: "Email Susan re: Q3" })).toBeTruthy();
      expect(screen.queryByText("Select a task")).toBeNull();
    });
    expect(onSelectedIssueChange).toHaveBeenCalledWith("issue-email-susan");
  });

  it("routes a clicked task before its event-log read completes", async () => {
    mockDaemonAndStreamFetch();
    const { api } = createWorkspaceAPI();
    const slowEvents = deferred<KataTaskEventsResponse>();
    vi.mocked(api.events).mockImplementation(async (query: KataTaskEventsQuery = {}, opts) => {
      if (query.issue_uid === "issue-email-susan") {
        return await new Promise<KataTaskEventsResponse>((resolve, reject) => {
          const abort = () => reject(new DOMException("Aborted", "AbortError"));
          if (opts?.signal?.aborted) {
            abort();
            return;
          }
          opts?.signal?.addEventListener("abort", abort, { once: true });
          slowEvents.promise.then(resolve, reject);
        });
      }
      return { reset_required: false, events: [], next_after_id: 0 };
    });
    const onSelectedIssueChange = vi.fn();

    const { rerender } = render(KataWorkspace, { props: { api, onSelectedIssueChange } });
    await waitFor(() => {
      expect(screen.getByRole("button", { name: /Email Susan re: Q3/ })).toBeTruthy();
    });
    onSelectedIssueChange.mockClear();

    await fireEvent.click(screen.getByRole("button", { name: /Email Susan re: Q3/ }));

    // The detail pane and the route callback both land while the event-log
    // read is still held open; a slow walk must not leave the URL on the
    // previous task.
    await waitFor(() => {
      expect(screen.getByRole("heading", { name: "Email Susan re: Q3" })).toBeTruthy();
    });
    await waitFor(() => {
      expect(onSelectedIssueChange).toHaveBeenCalledWith("issue-email-susan");
    });
    await rerender({ api, onSelectedIssueChange, selectedIssueUID: "issue-email-susan" });

    slowEvents.resolve({
      reset_required: false,
      events: [
        {
          event_id: 10,
          event_uid: "event-email-susan-created",
          origin_instance_uid: "instance-1",
          type: "issue.created",
          project_id: 3,
          project_uid: "project-kata",
          project_name: "Kata",
          issue_id: 2,
          issue_uid: "issue-email-susan",
          issue_short_id: "email-susan",
          actor: "fixture-user",
          created_at: fetchedAt,
        },
      ],
      next_after_id: 10,
    });
    await waitFor(() => {
      expect(screen.getByText("created the task")).toBeTruthy();
    });
  });

  it("routes a clicked task even when its event-log read fails", async () => {
    mockDaemonAndStreamFetch();
    const { api } = createWorkspaceAPI();
    vi.mocked(api.events).mockImplementation(async (query: KataTaskEventsQuery = {}) => {
      if (query.issue_uid === "issue-email-susan") throw new Error("event log walk failed");
      return { reset_required: false, events: [], next_after_id: 0 };
    });
    const onSelectedIssueChange = vi.fn();

    render(KataWorkspace, { props: { api, onSelectedIssueChange } });
    await waitFor(() => {
      expect(screen.getByRole("button", { name: /Email Susan re: Q3/ })).toBeTruthy();
    });
    onSelectedIssueChange.mockClear();

    await fireEvent.click(screen.getByRole("button", { name: /Email Susan re: Q3/ }));

    // The failed best-effort events read must neither fail the selection
    // nor block the route update.
    await waitFor(() => {
      expect(screen.getByRole("heading", { name: "Email Susan re: Q3" })).toBeTruthy();
    });
    await waitFor(() => {
      expect(onSelectedIssueChange).toHaveBeenCalledWith("issue-email-susan");
    });
  });

  it("surfaces a disconnected live Kata event stream", async () => {
    let streamController: ReadableStreamDefaultController<Uint8Array> | undefined;
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
        return new Response(
          new ReadableStream<Uint8Array>({
            start(controller) {
              streamController = controller;
            },
          }),
          { status: 200, headers: { "Content-Type": "text/event-stream" } },
        );
      }
      return new Response("not found", { status: 404 });
    });
    const api = createWorkspaceAPI().api;

    render(KataWorkspace, { props: { api } });

    await waitFor(() => {
      expect(screen.getByRole("heading", { name: "All Open" })).toBeTruthy();
      expect(streamController).toBeTruthy();
    });
    streamController?.close();

    await waitFor(() => {
      expect(screen.queryByRole("status")).toBeNull();
    });
    await fireEvent.click(screen.getByTestId("daemon-chip"));
    expect(within(screen.getByTestId("daemon-row-home")).getByText("Live updates disconnected")).toBeTruthy();
  });

  it("does not reconnect after a permanent live Kata stream failure", async () => {
    let streamRequests = 0;
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
        streamRequests += 1;
        return new Response("unauthorized", { status: 401 });
      }
      return new Response("not found", { status: 404 });
    });
    const api = createWorkspaceAPI().api;

    render(KataWorkspace, { props: { api } });

    await waitFor(() => {
      expect(streamRequests).toBe(1);
    });
    await fireEvent.click(screen.getByTestId("daemon-chip"));
    expect(within(screen.getByTestId("daemon-row-home")).getByText("Kata event stream failed: HTTP 401")).toBeTruthy();
    await new Promise((resolve) => setTimeout(resolve, 150));
    expect(streamRequests).toBe(1);
  });

  it("reconnects after a transient live Kata stream setup failure", async () => {
    let streamRequests = 0;
    let streamController: ReadableStreamDefaultController<Uint8Array> | null = null;
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
        streamRequests += 1;
        if (streamRequests === 1) {
          return new Response("bad gateway", { status: 503 });
        }
        return new Response(
          new ReadableStream<Uint8Array>({
            start(controller) {
              streamController = controller;
            },
          }),
          { status: 200, headers: { "Content-Type": "text/event-stream" } },
        );
      }
      return new Response("not found", { status: 404 });
    });
    const api = createWorkspaceAPI().api;

    render(KataWorkspace, { props: { api } });

    await waitFor(() => {
      expect(streamRequests).toBe(2);
      expect(streamController).toBeTruthy();
    });
    await fireEvent.click(screen.getByTestId("daemon-chip"));
    expect(within(screen.getByTestId("daemon-row-home")).getByText("connected")).toBeTruthy();
  });

  it("reconnects the live Kata event stream after a transient close", async () => {
    const streamControllers: ReadableStreamDefaultController<Uint8Array>[] = [];
    const streamHeaders: Headers[] = [];
    vi.spyOn(globalThis, "fetch").mockImplementation(async (input, init) => {
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
        streamHeaders.push(new Headers(init?.headers));
        return new Response(
          new ReadableStream<Uint8Array>({
            start(controller) {
              streamControllers.push(controller);
            },
          }),
          { status: 200, headers: { "Content-Type": "text/event-stream" } },
        );
      }
      return new Response("not found", { status: 404 });
    });
    const rowsByDaemon = { home: [initialIssues[0]!] };
    const api = createDaemonWorkspaceAPI(rowsByDaemon);
    vi.mocked(api.events).mockImplementation(async (query = {}) => ({
      reset_required: false,
      events: [],
      next_after_id: query.after_id === 0 ? 5 : (query.after_id ?? 0),
    }));

    render(KataWorkspace, { props: { api } });

    await waitFor(() => {
      expect(screen.getByRole("button", { name: /Pay rent/ })).toBeTruthy();
      expect(streamControllers).toHaveLength(1);
    });
    streamControllers[0]?.close();
    await waitFor(() => {
      expect(streamControllers).toHaveLength(2);
    });
    await fireEvent.click(screen.getByTestId("daemon-chip"));
    expect(within(screen.getByTestId("daemon-row-home")).getByText("connected")).toBeTruthy();
    await fireEvent.click(screen.getByTestId("daemon-chip"));
    expect(streamHeaders[1]?.get("X-Middleman-Kata-Daemon")).toBe("home");
    expect(streamHeaders[1]?.get("Last-Event-ID")).toBe("5");

    rowsByDaemon.home = [initialIssues[1]!];
    streamControllers[1]?.enqueue(
      new TextEncoder().encode(
        `id: 6\nevent: sync.reset_required\ndata: ${JSON.stringify({ event_id: 6, reset_after_id: 6 })}\n\n`,
      ),
    );

    await waitFor(() => {
      expect(screen.getByRole("button", { name: /Email Susan re: Q3/ })).toBeTruthy();
      expect(screen.getByText("Select a task")).toBeTruthy();
    });
    expect(screen.queryByRole("heading", { name: "Pay rent" })).toBeNull();

    streamControllers[1]?.close();
    await waitFor(() => {
      expect(streamControllers).toHaveLength(3);
    });
    expect(streamHeaders[2]?.get("Last-Event-ID")).toBe("6");
  });

  it("retries an initial cursor failure before opening the stream", async () => {
    const streamControllers: ReadableStreamDefaultController<Uint8Array>[] = [];
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
        return new Response(
          new ReadableStream<Uint8Array>({
            start(controller) {
              streamControllers.push(controller);
            },
          }),
          { status: 200, headers: { "Content-Type": "text/event-stream" } },
        );
      }
      return new Response("not found", { status: 404 });
    });
    const api = createDaemonWorkspaceAPI({ home: [initialIssues[0]!] });
    vi.mocked(api.events)
      .mockRejectedValueOnce(new Error("initial cursor unavailable"))
      .mockResolvedValue({ reset_required: false, events: [], next_after_id: 9 });

    render(KataWorkspace, { props: { api } });

    await waitFor(() => expect(screen.getByRole("button", { name: "Retry" })).toBeTruthy());
    expect(screen.getByRole("button", { name: /Pay rent/ })).toBeTruthy();
    expect(streamControllers).toHaveLength(0);

    await fireEvent.click(screen.getByRole("button", { name: "Retry" }));

    await waitFor(() => expect(streamControllers).toHaveLength(1));
    expect(screen.queryByText("initial cursor unavailable")).toBeNull();
    expect(api.events).toHaveBeenCalledTimes(2);
  });

  it("keeps daemon switching available while draining queued old-stream messages", async () => {
    const streamControllers: ReadableStreamDefaultController<Uint8Array>[] = [];
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
            {
              id: "work",
              url: "http://127.0.0.1:8888",
              default: false,
              auth: "none",
              health: "connected",
            },
          ],
        });
      }
      if (url.pathname === "/api/v1/kata/proxy/api/v1/events/stream") {
        return new Response(
          new ReadableStream<Uint8Array>({
            start(controller) {
              streamControllers.push(controller);
            },
          }),
          { status: 200, headers: { "Content-Type": "text/event-stream" } },
        );
      }
      return new Response("not found", { status: 404 });
    });
    const rowsByDaemon: Record<string, KataTaskSummary[]> = {
      home: [initialIssues[0]!],
      work: [initialIssues[1]!],
    };
    const api = createDaemonWorkspaceAPI(rowsByDaemon);
    let stallNextViewLoad = false;
    let completedViewLoads = 0;
    const oldStreamRefreshStarted = deferred<void>();
    const releaseOldStreamRefresh = deferred<void>();
    vi.mocked(api.issues).mockImplementation(async (query: KataTaskIssuesQuery) => {
      if (stallNextViewLoad) {
        stallNextViewLoad = false;
        oldStreamRefreshStarted.resolve();
        await releaseOldStreamRefresh.promise;
      }
      const rows = rowsByDaemon[getActiveKataDaemon() ?? getDefaultKataDaemon() ?? "home"] ?? [];
      const view = buildKataTaskView({
        view: query.view,
        issues: rows.filter((item) => (query.project_uid ? item.project_uid === query.project_uid : true)),
        projects,
        today: "2026-05-15",
        fetched_at: fetchedAt,
      });
      completedViewLoads += 1;
      return view;
    });

    render(KataWorkspace, { props: { api } });

    await waitFor(() => {
      expect(screen.getByRole("button", { name: /Pay rent/ })).toBeTruthy();
      expect(streamControllers).toHaveLength(1);
    });
    const viewLoadsBeforeEvents = completedViewLoads;

    stallNextViewLoad = true;
    streamControllers[0]?.enqueue(
      new TextEncoder().encode(
        `id: 6\nevent: sync.reset_required\ndata: ${JSON.stringify({ event_id: 6, reset_after_id: 6 })}\n\n` +
          `id: 7\nevent: sync.reset_required\ndata: ${JSON.stringify({ event_id: 7, reset_after_id: 7 })}\n\n`,
      ),
    );
    await oldStreamRefreshStarted.promise;
    await fireEvent.click(screen.getByTestId("daemon-chip"));
    const homeDaemonRow = screen.getByTestId("daemon-row-home") as HTMLButtonElement;
    expect(homeDaemonRow.disabled).toBe(false);

    releaseOldStreamRefresh.resolve();
    await waitFor(() => {
      expect(completedViewLoads).toBe(viewLoadsBeforeEvents + 2);
    });

    const viewLoadsAfterDrain = vi.mocked(api.issues).mock.calls.length;
    await Promise.resolve();
    await Promise.resolve();

    expect(api.issues).toHaveBeenCalledTimes(viewLoadsAfterDrain);
  });

  it("releases the daemon switch gate and reconnects when a stream refresh fails", async () => {
    const streamControllers: ReadableStreamDefaultController<Uint8Array>[] = [];
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
        return new Response(
          new ReadableStream<Uint8Array>({
            start(controller) {
              streamControllers.push(controller);
            },
          }),
          { status: 200, headers: { "Content-Type": "text/event-stream" } },
        );
      }
      return new Response("not found", { status: 404 });
    });
    const { api } = createWorkspaceAPI([initialIssues[0]!]);
    const failedRefresh = deferred<Awaited<ReturnType<KataTaskAPI["issues"]>>>();

    render(KataWorkspace, { props: { api } });
    await waitFor(() => {
      expect(screen.getByRole("button", { name: /Pay rent/ })).toBeTruthy();
      expect(streamControllers).toHaveLength(1);
    });

    vi.mocked(api.issues).mockImplementationOnce(() => failedRefresh.promise);
    streamControllers[0]?.enqueue(
      new TextEncoder().encode(
        `id: 6\nevent: sync.reset_required\ndata: ${JSON.stringify({ event_id: 6, reset_after_id: 6 })}\n\n`,
      ),
    );
    await fireEvent.click(screen.getByTestId("daemon-chip"));
    const homeDaemonRow = screen.getByTestId("daemon-row-home") as HTMLButtonElement;
    expect(homeDaemonRow.disabled).toBe(false);

    failedRefresh.reject(new Error("stream refresh failed"));
    await waitFor(() => {
      expect(streamControllers).toHaveLength(2);
    });
    expect(screen.getByRole("button", { name: /Pay rent/ })).toBeTruthy();
  });

  it("keeps the accepted workspace visible until a daemon switch commits", async () => {
    const streamControllers: ReadableStreamDefaultController<Uint8Array>[] = [];
    vi.spyOn(globalThis, "fetch").mockImplementation(async (input) => {
      const url = new URL(input instanceof Request ? input.url : String(input), window.location.origin);
      if (url.pathname === "/api/v1/kata/daemons") {
        return Response.json({
          daemons: [
            { id: "home", url: "http://127.0.0.1:7777", default: true, auth: "none", health: "connected" },
            { id: "work", url: "http://127.0.0.1:8888", default: false, auth: "none", health: "connected" },
          ],
        });
      }
      if (url.pathname === "/api/v1/kata/proxy/api/v1/events/stream") {
        return new Response(
          new ReadableStream<Uint8Array>({
            start(controller) {
              streamControllers.push(controller);
            },
          }),
          { status: 200, headers: { "Content-Type": "text/event-stream" } },
        );
      }
      return new Response("not found", { status: 404 });
    });
    const api = createDaemonWorkspaceAPI({ home: [initialIssues[0]!], work: [initialIssues[1]!] });
    const stalledEvents = deferred<KataTaskEventsResponse>();
    setActiveKataDaemon("home");

    render(KataWorkspace, { props: { api } });
    await waitFor(() => expect(screen.getByRole("button", { name: /Pay rent/ })).toBeTruthy());

    await fireEvent.click(screen.getByTestId("daemon-chip"));
    const workDaemonRow = screen.getByTestId("daemon-row-work") as HTMLButtonElement;
    await waitFor(() => expect(workDaemonRow.disabled).toBe(false));
    vi.mocked(api.events).mockImplementation(async (query = {}, options) =>
      query.after_id !== undefined && options?.daemonId === "work"
        ? stalledEvents.promise
        : { reset_required: false, events: [], next_after_id: 0 },
    );
    await fireEvent.click(workDaemonRow);
    await waitFor(() =>
      expect(api.events).toHaveBeenCalledWith(
        expect.objectContaining({ after_id: expect.any(Number) }),
        expect.objectContaining({ daemonId: "work" }),
      ),
    );

    expect(screen.getByRole("button", { name: /Pay rent/ })).toBeTruthy();
    expect(screen.queryByRole("button", { name: /Email Susan re: Q3/ })).toBeNull();
    expect(getActiveKataDaemon()).toBe("home");

    stalledEvents.resolve({ reset_required: false, events: [], next_after_id: 0 });
    await waitFor(() => expect(screen.getByRole("heading", { name: "Email Susan re: Q3" })).toBeTruthy());
    expect(getActiveKataDaemon()).toBe("work");
  });

  it("restores a stopped cursor retry when a route change cancels a daemon switch", async () => {
    const streamControllers: ReadableStreamDefaultController<Uint8Array>[] = [];
    vi.spyOn(globalThis, "fetch").mockImplementation(async (input) => {
      const url = new URL(input instanceof Request ? input.url : String(input), window.location.origin);
      if (url.pathname === "/api/v1/kata/daemons") {
        return Response.json({
          daemons: [
            { id: "home", url: "http://127.0.0.1:7777", default: true, auth: "none", health: "connected" },
            { id: "work", url: "http://127.0.0.1:8888", default: false, auth: "none", health: "connected" },
          ],
        });
      }
      if (url.pathname === "/api/v1/kata/proxy/api/v1/events/stream") {
        return new Response(
          new ReadableStream<Uint8Array>({
            start(controller) {
              streamControllers.push(controller);
            },
          }),
          { status: 200, headers: { "Content-Type": "text/event-stream" } },
        );
      }
      return new Response("not found", { status: 404 });
    });
    const api = createDaemonWorkspaceAPI({ home: [initialIssues[0]!], work: [initialIssues[1]!] });
    const stalledWorkCursor = deferred<KataTaskEventsResponse>();
    let homeCursorAttempts = 0;
    vi.mocked(api.events).mockImplementation(async (query = {}, options) => {
      if (query.after_id !== undefined && options?.daemonId === "work") return stalledWorkCursor.promise;
      if (query.after_id !== undefined && options?.daemonId === "home") {
        homeCursorAttempts += 1;
        if (homeCursorAttempts === 1) throw new Error("initial cursor unavailable");
      }
      return { reset_required: false, events: [], next_after_id: query.after_id ?? 0 };
    });
    setActiveKataDaemon("home");
    const { component } = render(KataWorkspaceRouteHost, { props: { api } });

    await waitFor(() => expect(screen.getByRole("button", { name: "Retry" })).toBeTruthy());
    expect(streamControllers).toHaveLength(0);

    await fireEvent.click(screen.getByTestId("daemon-chip"));
    const workDaemonRow = screen.getByTestId("daemon-row-work") as HTMLButtonElement;
    await waitFor(() => expect(workDaemonRow.disabled).toBe(false));
    await fireEvent.click(workDaemonRow);
    await waitFor(() =>
      expect(api.events).toHaveBeenCalledWith(
        expect.objectContaining({ after_id: expect.any(Number) }),
        expect.objectContaining({ daemonId: "work" }),
      ),
    );

    component.setRoute({ view: "inbox", issue: null });

    await waitFor(() => expect(screen.getByRole("heading", { name: "Inbox" })).toBeTruthy());
    expect(screen.getByText("initial cursor unavailable")).toBeTruthy();
    expect(screen.getByRole("button", { name: "Retry" })).toBeTruthy();
    expect(streamControllers).toHaveLength(0);

    await fireEvent.click(screen.getByRole("button", { name: "Retry" }));
    await waitFor(() => expect(streamControllers).toHaveLength(1));
    expect(screen.queryByText("initial cursor unavailable")).toBeNull();

    stalledWorkCursor.resolve({ reset_required: false, events: [], next_after_id: 0 });
  });

  it("does not commit a daemon switch after its route changes", async () => {
    const streamControllers: ReadableStreamDefaultController<Uint8Array>[] = [];
    vi.spyOn(globalThis, "fetch").mockImplementation(async (input) => {
      const url = new URL(input instanceof Request ? input.url : String(input), window.location.origin);
      if (url.pathname === "/api/v1/kata/daemons") {
        return Response.json({
          daemons: [
            { id: "home", url: "http://127.0.0.1:7777", default: true, auth: "none", health: "connected" },
            { id: "work", url: "http://127.0.0.1:8888", default: false, auth: "none", health: "connected" },
          ],
        });
      }
      if (url.pathname === "/api/v1/kata/proxy/api/v1/events/stream") {
        return new Response(
          new ReadableStream<Uint8Array>({
            start(controller) {
              streamControllers.push(controller);
            },
          }),
          { status: 200, headers: { "Content-Type": "text/event-stream" } },
        );
      }
      return new Response("not found", { status: 404 });
    });
    const api = createDaemonWorkspaceAPI({ home: [initialIssues[0]!], work: [initialIssues[1]!] });
    const stalledEvents = deferred<KataTaskEventsResponse>();
    setActiveKataDaemon("home");
    const { component } = render(KataWorkspaceRouteHost, { props: { api } });
    await waitFor(() => expect(screen.getByRole("button", { name: /Pay rent/ })).toBeTruthy());

    await fireEvent.click(screen.getByTestId("daemon-chip"));
    const workDaemonRow = screen.getByTestId("daemon-row-work") as HTMLButtonElement;
    await waitFor(() => expect(workDaemonRow.disabled).toBe(false));
    vi.mocked(api.events).mockImplementation(async (query = {}, options) =>
      query.after_id !== undefined && options?.daemonId === "work"
        ? stalledEvents.promise
        : { reset_required: false, events: [], next_after_id: 0 },
    );
    await fireEvent.click(workDaemonRow);
    await waitFor(() =>
      expect(api.events).toHaveBeenCalledWith(
        expect.objectContaining({ after_id: expect.any(Number) }),
        expect.objectContaining({ daemonId: "work" }),
      ),
    );
    component.setRoute({ view: "inbox", issue: null });
    stalledEvents.resolve({ reset_required: false, events: [], next_after_id: 0 });

    await waitFor(() => expect(screen.getByRole("heading", { name: "Inbox" })).toBeTruthy());
    expect(getActiveKataDaemon()).toBe("home");
    expect(streamControllers).toHaveLength(2);
  });

  it("does not commit a daemon switch after unmount", async () => {
    vi.spyOn(globalThis, "fetch").mockImplementation(async (input) => {
      const url = new URL(input instanceof Request ? input.url : String(input), window.location.origin);
      if (url.pathname === "/api/v1/kata/daemons") {
        return Response.json({
          daemons: [
            { id: "home", url: "http://127.0.0.1:7777", default: true, auth: "none", health: "connected" },
            { id: "work", url: "http://127.0.0.1:8888", default: false, auth: "none", health: "connected" },
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
    const api = createDaemonWorkspaceAPI({ home: [initialIssues[0]!], work: [initialIssues[1]!] });
    const stalledEvents = deferred<KataTaskEventsResponse>();
    setActiveKataDaemon("home");
    const rendered = render(KataWorkspace, { props: { api } });
    await waitFor(() => expect(screen.getByRole("button", { name: /Pay rent/ })).toBeTruthy());

    await fireEvent.click(screen.getByTestId("daemon-chip"));
    const workDaemonRow = screen.getByTestId("daemon-row-work") as HTMLButtonElement;
    await waitFor(() => expect(workDaemonRow.disabled).toBe(false));
    vi.mocked(api.events).mockImplementation(async (query = {}, options) =>
      query.after_id !== undefined && options?.daemonId === "work"
        ? stalledEvents.promise
        : { reset_required: false, events: [], next_after_id: 0 },
    );
    await fireEvent.click(workDaemonRow);
    await waitFor(() =>
      expect(api.events).toHaveBeenCalledWith(
        expect.objectContaining({ after_id: expect.any(Number) }),
        expect.objectContaining({ daemonId: "work" }),
      ),
    );
    rendered.unmount();
    stalledEvents.resolve({ reset_required: false, events: [], next_after_id: 0 });
    await Promise.resolve();
    await Promise.resolve();

    expect(getActiveKataDaemon()).toBe("home");
  });

  it("restarts the live stream after a stale daemon switch completion", async () => {
    const streamControllers: ReadableStreamDefaultController<Uint8Array>[] = [];
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
            {
              id: "work",
              url: "http://127.0.0.1:8888",
              default: false,
              auth: "none",
              health: "connected",
            },
          ],
        });
      }
      if (url.pathname === "/api/v1/kata/proxy/api/v1/events/stream") {
        return new Response(
          new ReadableStream<Uint8Array>({
            start(controller) {
              streamControllers.push(controller);
            },
          }),
          { status: 200, headers: { "Content-Type": "text/event-stream" } },
        );
      }
      return new Response("not found", { status: 404 });
    });
    const api = createDaemonWorkspaceAPI({
      home: [initialIssues[0]!],
      work: [initialIssues[1]!],
    });
    const stalledEvents = deferred<KataTaskEventsResponse>();
    const { rerender } = render(KataWorkspace, { props: { api } });

    await waitFor(() => {
      expect(screen.getByRole("button", { name: /Pay rent/ })).toBeTruthy();
      expect(streamControllers).toHaveLength(1);
    });
    vi.mocked(api.events).mockImplementation(async (query = {}, options) =>
      query.after_id !== undefined && options?.daemonId === "work"
        ? stalledEvents.promise
        : { reset_required: false, events: [], next_after_id: 0 },
    );
    await fireEvent.click(screen.getByTestId("daemon-chip"));
    await fireEvent.click(screen.getByTestId("daemon-row-work"));
    await waitFor(() =>
      expect(api.events).toHaveBeenCalledWith(
        expect.objectContaining({ after_id: expect.any(Number) }),
        expect.objectContaining({ daemonId: "work" }),
      ),
    );

    await rerender({ api, routeViewName: "inbox" });
    stalledEvents.resolve({
      reset_required: false,
      events: [],
      next_after_id: 0,
    });

    await waitFor(() => expect(streamControllers).toHaveLength(2));
  });
});
