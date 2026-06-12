import { cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/svelte";
import { afterEach, beforeEach, describe, expect, it, vi } from "vite-plus/test";

import type {
  KataTaskAPI,
  KataTaskEventsResponse,
  KataTaskIssuesQuery,
  KataTaskSummary,
} from "../../api/kata/taskTypes.js";
import { buildKataTaskView } from "../../api/kata/taskViewBuilder.js";
import { getActiveKataDaemon, getDefaultKataDaemon } from "../../stores/active-kata-daemon.svelte.js";
import KataWorkspace from "./KataWorkspace.svelte";
import {
  createDaemonWorkspaceAPI,
  createWorkspaceAPI,
  deferred,
  fetchedAt,
  initialIssues,
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

  it("ignores queued old-stream messages after a daemon switch aborts the stream", async () => {
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
    const oldStreamRefreshStarted = deferred<void>();
    const releaseOldStreamRefresh = deferred<void>();
    vi.mocked(api.issues).mockImplementation(async (query: KataTaskIssuesQuery) => {
      if (stallNextViewLoad) {
        stallNextViewLoad = false;
        oldStreamRefreshStarted.resolve();
        await releaseOldStreamRefresh.promise;
      }
      const rows = rowsByDaemon[getActiveKataDaemon() ?? getDefaultKataDaemon() ?? "home"] ?? [];
      return buildKataTaskView({
        view: query.view,
        issues: rows.filter((item) => (query.project_uid ? item.project_uid === query.project_uid : true)),
        projects,
        today: "2026-05-15",
        fetched_at: fetchedAt,
      });
    });

    render(KataWorkspace, { props: { api } });

    await waitFor(() => {
      expect(screen.getByRole("button", { name: /Pay rent/ })).toBeTruthy();
      expect(streamControllers).toHaveLength(1);
    });

    stallNextViewLoad = true;
    streamControllers[0]?.enqueue(
      new TextEncoder().encode(
        `id: 6\nevent: sync.reset_required\ndata: ${JSON.stringify({ event_id: 6, reset_after_id: 6 })}\n\n` +
          `id: 7\nevent: sync.reset_required\ndata: ${JSON.stringify({ event_id: 7, reset_after_id: 7 })}\n\n`,
      ),
    );
    await oldStreamRefreshStarted.promise;

    await fireEvent.click(screen.getByTestId("daemon-chip"));
    await fireEvent.click(screen.getByTestId("daemon-row-work"));
    await waitFor(() => {
      expect(screen.getByRole("heading", { name: "Email Susan re: Q3" })).toBeTruthy();
      expect(streamControllers).toHaveLength(2);
    });
    const viewLoadsAfterSwitch = vi.mocked(api.issues).mock.calls.length;

    releaseOldStreamRefresh.resolve();
    await Promise.resolve();
    await Promise.resolve();

    expect(api.issues).toHaveBeenCalledTimes(viewLoadsAfterSwitch);
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
    const eventCallCount = vi.mocked(api.events).mock.calls.length;
    vi.mocked(api.events).mockImplementationOnce(async () => stalledEvents.promise);
    await fireEvent.click(screen.getByTestId("daemon-chip"));
    await fireEvent.click(screen.getByTestId("daemon-row-work"));
    await waitFor(() => expect(api.events).toHaveBeenCalledTimes(eventCallCount + 1));

    await rerender({ api, routeViewName: "inbox" });
    stalledEvents.resolve({
      reset_required: false,
      events: [],
      next_after_id: 0,
    });

    await waitFor(() => expect(streamControllers).toHaveLength(2));
  });
});
