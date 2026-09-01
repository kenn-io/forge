import { afterEach, beforeEach, describe, expect, it, vi } from "vite-plus/test";
import { Effect } from "effect";

import { createEventsStore as createRuntimeEventsStore, type EventsStoreOptions } from "./events.svelte.js";
import type { SyncStatus } from "../api/types.js";
import { makeAppRuntime, type OwnedAppRuntime } from "../app/runtime.js";

type Handler = (ev: unknown) => void;

interface StubEventSource {
  url: string;
  closed: boolean;
  handlers: Map<string, Set<Handler>>;
  addEventListener(name: string, fn: Handler): void;
  removeEventListener(name: string, fn: Handler): void;
  close(): void;
}

let instances: StubEventSource[] = [];
let runtime: OwnedAppRuntime | undefined;

class EventSourceStub implements StubEventSource {
  url: string;
  closed = false;
  handlers = new Map<string, Set<Handler>>();

  constructor(url: string) {
    this.url = url;
    instances.push(this);
  }

  addEventListener(name: string, fn: Handler): void {
    let set = this.handlers.get(name);
    if (!set) {
      set = new Set();
      this.handlers.set(name, set);
    }
    set.add(fn);
  }

  removeEventListener(name: string, fn: Handler): void {
    this.handlers.get(name)?.delete(fn);
  }

  close(): void {
    this.closed = true;
  }
}

function emit(src: StubEventSource, name: string, ev: unknown, lastEventId = ""): void {
  const set = src.handlers.get(name);
  if (!set) return;
  const event =
    name === "open" || name === "error"
      ? new Event(name)
      : new MessageEvent(name, {
          data: typeof ev === "object" && ev !== null && "data" in ev ? ev.data : undefined,
          lastEventId,
        });
  for (const fn of set) fn(event);
}

function createEventsStore(opts: Omit<EventsStoreOptions, "runtime"> = {}) {
  if (runtime === undefined) throw new Error("test runtime is not initialized");
  return createRuntimeEventsStore({ runtime, ...opts });
}

function start(store: ReturnType<typeof createEventsStore>) {
  if (runtime === undefined) throw new Error("test runtime is not initialized");
  return runtime.runCommand(store.streamEffect, {
    operation: "test provider events",
    safeContext: {},
    onFailure: () => {},
  });
}

async function awaitSource(index = 0): Promise<StubEventSource> {
  await vi.waitFor(() => expect(instances.length).toBeGreaterThan(index));
  const source = instances[index];
  if (source === undefined) throw new Error(`EventSource ${index} did not open`);
  return source;
}

beforeEach(() => {
  instances = [];
  runtime = makeAppRuntime();
  (
    globalThis as unknown as {
      EventSource: typeof EventSourceStub;
    }
  ).EventSource = EventSourceStub;
});

afterEach(async () => {
  if (runtime !== undefined) {
    await Effect.runPromise(runtime.disposeEffect);
    runtime = undefined;
  }
  vi.restoreAllMocks();
});

describe("createEventsStore URL building", () => {
  it("requires event consequences to return an Effect", () => {
    createEventsStore({
      // @ts-expect-error Event handlers must acknowledge asynchronous consequences through Effect.
      onDataChanged: () => undefined,
    });
  });

  it("uses root when no basePath option supplied", async () => {
    const store = createEventsStore();
    start(store);
    expect((await awaitSource()).url).toBe("/api/v1/events");
  });

  it('handles basePath of "/"', async () => {
    const store = createEventsStore({ getBasePath: () => "/" });
    start(store);
    expect((await awaitSource()).url).toBe("/api/v1/events");
  });

  it("handles basePath with prefix", async () => {
    const store = createEventsStore({
      getBasePath: () => "/some/prefix",
    });
    start(store);
    expect((await awaitSource()).url).toBe("/some/prefix/api/v1/events");
  });

  it("tolerates trailing slash on basePath", async () => {
    const store = createEventsStore({
      getBasePath: () => "/some/prefix/",
    });
    start(store);
    expect((await awaitSource()).url).toBe("/some/prefix/api/v1/events");
  });
});

describe("createEventsStore event dispatch", () => {
  it("fans workspace diff events out from the shared provider stream", async () => {
    const received: unknown[] = [];
    const store = createEventsStore();
    const unsubscribe = store.subscribeWorkspaceEvents((event) => {
      received.push(event);
    });
    start(store);
    const source = await awaitSource();

    emit(source, "workspace_diff_ready", {
      data: JSON.stringify({ workspace_id: "ws-1", version: "generation:ready" }),
    });

    await vi.waitFor(() =>
      expect(received).toEqual([
        {
          type: "workspace_diff_ready",
          payload: { workspace_id: "ws-1", version: "generation:ready" },
        },
      ]),
    );
    unsubscribe();
  });

  it("fires onDataChanged for data_changed frames", async () => {
    const onDataChanged = vi.fn(() => Effect.void);
    const store = createEventsStore({ onDataChanged });
    start(store);
    const source = await awaitSource();
    emit(source, "data_changed", { data: "{}" });
    emit(source, "data_changed", { data: "{}" });
    await vi.waitFor(() => expect(onDataChanged).toHaveBeenCalledTimes(2));
  });

  it("parses sync_status JSON and fires onSyncStatus", async () => {
    const onSyncStatus = vi.fn(() => Effect.void);
    const store = createEventsStore({ onSyncStatus });
    start(store);
    const payload: SyncStatus = {
      running: true,
      last_run_at: "2026-04-08T12:00:00Z",
      last_error: "",
    };
    emit(await awaitSource(), "sync_status", {
      data: JSON.stringify(payload),
    });
    await vi.waitFor(() => expect(onSyncStatus).toHaveBeenCalledWith(payload));
  });

  it("disconnects on malformed sync_status frames", async () => {
    const onSyncStatus = vi.fn(() => Effect.void);
    const store = createEventsStore({ onSyncStatus });
    start(store);
    const source = await awaitSource();
    expect(() =>
      emit(source, "sync_status", {
        data: "not-json",
      }),
    ).not.toThrow();
    await vi.waitFor(() => expect(store.getConnectionState()).toBe("disconnected"));
    expect(onSyncStatus).not.toHaveBeenCalled();
  });

  it("fires onReconnectStale for reconnect.stale frames", async () => {
    const onReconnectStale = vi.fn(() => Effect.void);
    const store = createEventsStore({ onReconnectStale });
    start(store);
    emit(await awaitSource(), "reconnect.stale", {
      data: JSON.stringify({ hub_connected: false }),
    });
    await vi.waitFor(() => expect(onReconnectStale).toHaveBeenCalledWith({ hub_connected: false }));
  });

  it("routes hub availability through the shared event source", async () => {
    const onHubConnectionChanged = vi.fn(() => Effect.void);
    const store = createEventsStore({ onHubConnectionChanged });
    start(store);
    const source = await awaitSource();

    emit(source, "hub_connection_changed", {
      data: JSON.stringify({ connected: false }),
    });

    await vi.waitFor(() => expect(onHubConnectionChanged).toHaveBeenCalledWith({ connected: false }));
    expect(instances).toHaveLength(1);
  });

  it("parses pushed-head refresh events and routes them to callbacks", async () => {
    const onWorkspacePushedHeadChanged = vi.fn(() => Effect.void);
    const onWorkspacePRRefreshQueued = vi.fn(() => Effect.void);
    const onPRDetailRefreshed = vi.fn(() => Effect.void);
    const onPRCIRefreshQueued = vi.fn(() => Effect.void);
    const onPRCIRefreshed = vi.fn(() => Effect.void);
    const onWorkspacePRAssociated = vi.fn(() => Effect.void);
    const store = createEventsStore({
      onWorkspacePushedHeadChanged,
      onWorkspacePRRefreshQueued,
      onPRDetailRefreshed,
      onPRCIRefreshQueued,
      onPRCIRefreshed,
      onWorkspacePRAssociated,
    });
    start(store);
    const source = await awaitSource();

    emit(source, "workspace_pushed_head_changed", {
      data: JSON.stringify({
        workspace_id: "ws_123",
        provider: "github",
        platform_host: "github.com",
        repo_path: "acme/widgets",
        owner: "acme",
        name: "widgets",
        number: 42,
        old_sha: "1111111",
        new_sha: "2222222",
        remote: "origin",
        branch: "feature/widgets",
        tracking_ref: "refs/remotes/origin/feature/widgets",
        observed_at: "2026-05-20T14:15:00Z",
      }),
    });
    emit(source, "workspace_pr_refresh_queued", {
      data: JSON.stringify({
        workspace_id: "ws_123",
        provider: "github",
        platform_host: "github.com",
        repo_path: "acme/widgets",
        owner: "acme",
        name: "widgets",
        number: 42,
        head_sha: "2222222",
        priority: "high",
        queued_at: "2026-05-20T14:15:01Z",
      }),
    });
    emit(source, "pr_detail_refreshed", {
      data: JSON.stringify({
        provider: "github",
        platform_host: "github.com",
        repo_path: "acme/widgets",
        owner: "acme",
        name: "widgets",
        number: 42,
        head_sha: "2222222",
        synced_at: "2026-05-20T14:15:04Z",
        warnings: [],
      }),
    });
    emit(source, "pr_ci_refresh_queued", {
      data: JSON.stringify({
        provider: "github",
        platform_host: "github.com",
        repo_path: "acme/widgets",
        owner: "acme",
        name: "widgets",
        number: 42,
        head_sha: "2222222",
        priority: "low",
        queued_at: "2026-05-20T14:15:05Z",
      }),
    });
    emit(source, "pr_ci_refreshed", {
      data: JSON.stringify({
        provider: "github",
        platform_host: "github.com",
        repo_path: "acme/widgets",
        owner: "acme",
        name: "widgets",
        number: 42,
        head_sha: "2222222",
        refreshed_at: "2026-05-20T14:15:20Z",
        warnings: [],
      }),
    });
    emit(source, "workspace_pr_associated", {
      data: JSON.stringify({
        workspace_id: "ws_123",
        provider: "github",
        platform_host: "github.com",
        repo_path: "acme/widgets",
        owner: "acme",
        name: "widgets",
        issue_number: 7,
        pr_number: 42,
        associated_at: "2026-05-20T14:15:00Z",
      }),
    });

    await vi.waitFor(() =>
      expect(onWorkspacePushedHeadChanged).toHaveBeenCalledWith(
        expect.objectContaining({
          workspace_id: "ws_123",
          new_sha: "2222222",
        }),
      ),
    );
    await vi.waitFor(() =>
      expect(onWorkspacePRAssociated).toHaveBeenCalledWith(expect.objectContaining({ issue_number: 7, pr_number: 42 })),
    );
    expect(onWorkspacePRRefreshQueued).toHaveBeenCalledWith(
      expect.objectContaining({
        workspace_id: "ws_123",
        priority: "high",
      }),
    );
    expect(onPRDetailRefreshed).toHaveBeenCalledWith(
      expect.objectContaining({
        repo_path: "acme/widgets",
        number: 42,
      }),
    );
    expect(onPRCIRefreshQueued).toHaveBeenCalledWith(expect.objectContaining({ head_sha: "2222222", priority: "low" }));
    expect(onPRCIRefreshed).toHaveBeenCalledWith(expect.objectContaining({ refreshed_at: "2026-05-20T14:15:20Z" }));
  });

  it("disconnects on malformed pushed-head refresh event frames", async () => {
    const onPRDetailRefreshed = vi.fn(() => Effect.void);
    const store = createEventsStore({ onPRDetailRefreshed });
    start(store);
    const source = await awaitSource();
    expect(() =>
      emit(source, "pr_detail_refreshed", {
        data: "not-json",
      }),
    ).not.toThrow();
    await vi.waitFor(() => expect(store.getConnectionState()).toBe("disconnected"));
    expect(onPRDetailRefreshed).not.toHaveBeenCalled();
  });

  it("ignores unknown event types without throwing", async () => {
    const onDataChanged = vi.fn(() => Effect.void);
    const store = createEventsStore({ onDataChanged });
    start(store);
    const source = await awaitSource();
    expect(() =>
      emit(source, "totally_unknown", {
        data: "{}",
      }),
    ).not.toThrow();
    expect(onDataChanged).not.toHaveBeenCalled();
  });
});

describe("createEventsStore connection lifecycle", () => {
  it("replays the open signal to a subscriber that mounts after connection", async () => {
    const store = createEventsStore();
    start(store);
    emit(await awaitSource(), "open", {});
    await vi.waitFor(() => expect(store.getConnectionState()).toBe("connected"));

    const received: unknown[] = [];
    store.subscribeWorkspaceEvents((event) => received.push(event));

    expect(received).toEqual([{ type: "open" }]);
  });

  it("moves workspace selection onto the singleton provider stream", async () => {
    const store = createEventsStore();
    start(store);
    const initial = await awaitSource();

    const releaseSelection = store.selectWorkspace("ws-1");
    const selected = await awaitSource(1);

    expect(initial.closed).toBe(true);
    expect(selected.url).toBe("/api/v1/events?workspace_id=ws-1");

    releaseSelection();
    const released = await awaitSource(2);
    expect(selected.closed).toBe(true);
    expect(released.url).toBe("/api/v1/events");
  });

  it("connection state reflects open and error events", async () => {
    const store = createEventsStore();
    start(store);
    expect(store.getConnectionState()).toBe("connecting");
    await vi.waitFor(() => expect(instances).toHaveLength(1));
    emit(instances[0] as StubEventSource, "open", {});
    await vi.waitFor(() => expect(store.getConnectionState()).toBe("connected"));
    emit(instances[0] as StubEventSource, "error", {});
    await vi.waitFor(() => expect(store.getConnectionState()).toBe("reconnecting"));
  });

  it("interrupt closes the source and a new owned stream can reconnect", async () => {
    const store = createEventsStore();
    const firstExecution = start(store);
    await vi.waitFor(() => expect(instances).toHaveLength(1));
    emit(instances[0] as StubEventSource, "open", {});
    await vi.waitFor(() => expect(store.getConnectionState()).toBe("connected"));
    firstExecution.interrupt();
    await vi.waitFor(() => expect(instances[0]?.closed).toBe(true));
    expect(store.getConnectionState()).toBe("disconnected");

    start(store);
    await vi.waitFor(() => expect(instances).toHaveLength(2));
    expect(instances[1]).not.toBe(instances[0]);
    expect(instances[1]?.closed).toBe(false);
  });

  it("resumes from the last accepted event after an owner handoff", async () => {
    const onDataChanged = vi.fn(() => Effect.void);
    const store = createEventsStore({ onDataChanged });
    const firstExecution = start(store);
    const first = await awaitSource();
    emit(first, "open", {});
    emit(first, "data_changed", { data: "{}" }, "17");
    await vi.waitFor(() => expect(onDataChanged).toHaveBeenCalledOnce());

    firstExecution.interrupt();
    await vi.waitFor(() => expect(first.closed).toBe(true));
    start(store);
    const second = await awaitSource(1);

    expect(second.url).toBe("/api/v1/events?since=17");
  });

  it("resumes from the runtime checkpoint when the store is recreated", async () => {
    const onDataChanged = vi.fn(() => Effect.void);
    const firstStore = createEventsStore({ onDataChanged });
    const firstExecution = start(firstStore);
    const firstSource = await awaitSource();
    emit(firstSource, "open", {});
    emit(firstSource, "data_changed", { data: "{}" }, "23");
    await vi.waitFor(() => expect(onDataChanged).toHaveBeenCalledOnce());
    firstExecution.interrupt();
    await vi.waitFor(() => expect(firstSource.closed).toBe(true));

    const replacementStore = createEventsStore();
    start(replacementStore);
    const replacementSource = await awaitSource(1);

    expect(replacementSource).not.toBe(firstSource);
    expect(replacementSource.url).toBe("/api/v1/events?since=23");
  });
});
