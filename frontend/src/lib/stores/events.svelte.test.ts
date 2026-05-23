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

  it("parses pushed-head refresh events and routes them to callbacks", () => {
    const onWorkspacePushedHeadChanged = vi.fn();
    const onWorkspacePRRefreshQueued = vi.fn();
    const onPRDetailRefreshed = vi.fn();
    const onPRCIRefreshQueued = vi.fn();
    const onPRCIRefreshed = vi.fn();
    const onWorkspacePRAssociated = vi.fn();
    const store = createEventsStore({
      onWorkspacePushedHeadChanged,
      onWorkspacePRRefreshQueued,
      onPRDetailRefreshed,
      onPRCIRefreshQueued,
      onPRCIRefreshed,
      onWorkspacePRAssociated,
    });
    store.connect();
    const source = instances[0] as StubEventSource;

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

    expect(onWorkspacePushedHeadChanged).toHaveBeenCalledWith(
      expect.objectContaining({ workspace_id: "ws_123", new_sha: "2222222" }),
    );
    expect(onWorkspacePRRefreshQueued).toHaveBeenCalledWith(
      expect.objectContaining({ workspace_id: "ws_123", priority: "high" }),
    );
    expect(onPRDetailRefreshed).toHaveBeenCalledWith(
      expect.objectContaining({ repo_path: "acme/widgets", number: 42 }),
    );
    expect(onPRCIRefreshQueued).toHaveBeenCalledWith(
      expect.objectContaining({ head_sha: "2222222", priority: "low" }),
    );
    expect(onPRCIRefreshed).toHaveBeenCalledWith(
      expect.objectContaining({ refreshed_at: "2026-05-20T14:15:20Z" }),
    );
    expect(onWorkspacePRAssociated).toHaveBeenCalledWith(
      expect.objectContaining({ issue_number: 7, pr_number: 42 }),
    );
  });

  it("swallows malformed pushed-head refresh event frames", () => {
    const onPRDetailRefreshed = vi.fn();
    const store = createEventsStore({ onPRDetailRefreshed });
    store.connect();
    expect(() =>
      emit(instances[0] as StubEventSource, "pr_detail_refreshed", {
        data: "not-json",
      }),
    ).not.toThrow();
    expect(onPRDetailRefreshed).not.toHaveBeenCalled();
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
