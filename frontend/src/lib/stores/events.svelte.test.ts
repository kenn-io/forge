import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { createEventsStore } from "@middleman/ui/stores/events";
import type { SyncStatus } from "@middleman/ui/api/types";

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

function emit(src: StubEventSource, name: string, ev: unknown): void {
  const set = src.handlers.get(name);
  if (!set) return;
  for (const fn of set) fn(ev);
}

beforeEach(() => {
  instances = [];
  (globalThis as unknown as {
    EventSource: typeof EventSourceStub;
  }).EventSource = EventSourceStub;
});

afterEach(() => {
  vi.restoreAllMocks();
});

describe("createEventsStore URL building", () => {
  it("uses root when no basePath option supplied", () => {
    const store = createEventsStore();
    store.connect();
    expect(instances).toHaveLength(1);
    expect(instances[0]?.url).toBe("/api/v1/events");
  });

  it("handles basePath of \"/\"", () => {
    const store = createEventsStore({ getBasePath: () => "/" });
    store.connect();
    expect(instances[0]?.url).toBe("/api/v1/events");
  });

  it("handles basePath with prefix", () => {
    const store = createEventsStore({
      getBasePath: () => "/some/prefix",
    });
    store.connect();
    expect(instances[0]?.url).toBe("/some/prefix/api/v1/events");
  });

  it("tolerates trailing slash on basePath", () => {
    const store = createEventsStore({
      getBasePath: () => "/some/prefix/",
    });
    store.connect();
    expect(instances[0]?.url).toBe("/some/prefix/api/v1/events");
  });
});

describe("createEventsStore connect idempotence", () => {
  it("second connect is a no-op when already connected", () => {
    const store = createEventsStore();
    store.connect();
    store.connect();
    expect(instances).toHaveLength(1);
  });
});

describe("createEventsStore event dispatch", () => {
  it("fires onDataChanged for data_changed frames", () => {
    const onDataChanged = vi.fn();
    const store = createEventsStore({ onDataChanged });
    store.connect();
    const src = instances[0];
    expect(src).toBeDefined();
    emit(src as StubEventSource, "data_changed", { data: "" });
    emit(src as StubEventSource, "data_changed", { data: "" });
    expect(onDataChanged).toHaveBeenCalledTimes(2);
  });

  it("parses sync_status JSON and fires onSyncStatus", () => {
    const onSyncStatus = vi.fn();
    const store = createEventsStore({ onSyncStatus });
    store.connect();
    const payload: SyncStatus = {
      running: true,
      last_run_at: "2026-04-08T12:00:00Z",
      last_error: "",
    };
    emit(instances[0] as StubEventSource, "sync_status", {
      data: JSON.stringify(payload),
    });
    expect(onSyncStatus).toHaveBeenCalledTimes(1);
    expect(onSyncStatus).toHaveBeenCalledWith(payload);
  });

  it("swallows malformed sync_status frames", () => {
    const onSyncStatus = vi.fn();
    const store = createEventsStore({ onSyncStatus });
    store.connect();
    expect(() =>
      emit(instances[0] as StubEventSource, "sync_status", {
        data: "not-json",
      }),
    ).not.toThrow();
    expect(onSyncStatus).not.toHaveBeenCalled();
  });

  it("fires onReconnectStale for reconnect.stale frames", () => {
    const onReconnectStale = vi.fn();
    const store = createEventsStore({ onReconnectStale });
    store.connect();
    const src = instances[0];
    expect(src).toBeDefined();
    emit(src as StubEventSource, "reconnect.stale", { data: "{}" });
    expect(onReconnectStale).toHaveBeenCalledTimes(1);
  });

  it("ignores unknown event types without throwing", () => {
    const onDataChanged = vi.fn();
    const store = createEventsStore({ onDataChanged });
    store.connect();
    expect(() =>
      emit(instances[0] as StubEventSource, "totally_unknown", {
        data: "{}",
      }),
    ).not.toThrow();
    expect(onDataChanged).not.toHaveBeenCalled();
  });
});

describe("createEventsStore connection lifecycle", () => {
  it("isConnected reflects the open event", () => {
    const store = createEventsStore();
    store.connect();
    expect(store.isConnected()).toBe(false);
    emit(instances[0] as StubEventSource, "open", {});
    expect(store.isConnected()).toBe(true);
    emit(instances[0] as StubEventSource, "error", {});
    expect(store.isConnected()).toBe(false);
  });

  it("disconnect closes source and allows reconnect", () => {
    const store = createEventsStore();
    store.connect();
    emit(instances[0] as StubEventSource, "open", {});
    expect(store.isConnected()).toBe(true);
    store.disconnect();
    expect(instances[0]?.closed).toBe(true);
    expect(store.isConnected()).toBe(false);

    store.connect();
    expect(instances).toHaveLength(2);
    expect(instances[1]?.closed).toBe(false);
  });
});
