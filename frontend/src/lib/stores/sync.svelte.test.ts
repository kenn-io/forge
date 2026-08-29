import { Effect } from "effect";
import { afterEach, beforeEach, describe, expect, it, vi } from "vite-plus/test";
import type { OwnedAppRuntime } from "../app/runtime.js";
import type { GeneratedClient } from "../api/generated-api.js";
import { makeTestAppRuntime } from "../testing/effect-layers.js";
import { createSyncStore as createRuntimeSyncStore, type SyncStoreOptions } from "./sync.svelte.js";

let runtime: OwnedAppRuntime | undefined;

function createSyncStore(client: GeneratedClient, options: Omit<SyncStoreOptions, "runtime"> = {}) {
  runtime = makeTestAppRuntime(client);
  return createRuntimeSyncStore({ ...options, runtime });
}

beforeEach(() => {
  runtime = undefined;
});

afterEach(async () => {
  vi.useRealTimers();
  if (runtime !== undefined) await Effect.runPromise(runtime.disposeEffect);
});

describe("sync store", () => {
  it("tracks hub availability independently of cached sync status", () => {
    const store = createSyncStore({ GET: vi.fn(), POST: vi.fn() } as unknown as GeneratedClient);

    expect(store.getProviderAvailable()).toBe(true);
    store.setSyncStatus({ running: false, last_run_at: "2026-08-05T12:00:00Z", last_error: "" });
    store.setProviderAvailable(false);

    expect(store.getProviderAvailable()).toBe(false);
    expect(store.getSyncState()?.last_run_at).toBe("2026-08-05T12:00:00Z");
  });

  it("keeps an acknowledged trigger running through a stale idle status", async () => {
    const baseline = "2026-08-05T12:00:00Z";
    const get = vi.fn(async (path: string) =>
      path === "/sync/status"
        ? { data: { running: false, last_run_at: baseline, last_error: "" } }
        : { data: { provider_pools: {}, local_ceilings: {} } },
    );
    const post = vi.fn(async () => ({ data: undefined, response: new Response(null, { status: 202 }) }));
    const store = createSyncStore({ GET: get, POST: post } as unknown as GeneratedClient);
    store.setSyncStatus({ running: false, last_run_at: baseline, last_error: "" });

    store.triggerSync();
    await vi.waitFor(() => expect(get).toHaveBeenCalledTimes(2));

    expect(store.getSyncState()).toEqual({
      running: true,
      last_run_at: baseline,
      last_error: "",
    });
  });

  it("accepts trigger completion when last-run time advances without observing running", async () => {
    const baseline = "2026-08-05T12:00:00Z";
    const completed = "2026-08-05T12:01:00Z";
    const get = vi.fn(async (path: string) =>
      path === "/sync/status"
        ? { data: { running: false, last_run_at: completed, last_error: "" } }
        : { data: { provider_pools: {}, local_ceilings: {} } },
    );
    const post = vi.fn(async () => ({ data: undefined, response: new Response(null, { status: 202 }) }));
    const store = createSyncStore({ GET: get, POST: post } as unknown as GeneratedClient);
    const onComplete = vi.fn();
    store.setSyncStatus({ running: false, last_run_at: baseline, last_error: "" });
    store.subscribeSyncComplete(onComplete);

    store.triggerSync();
    await vi.waitFor(() => expect(get).toHaveBeenCalledTimes(2));

    expect(store.getSyncState()).toEqual({
      running: false,
      last_run_at: completed,
      last_error: "",
    });
    expect(onComplete).toHaveBeenCalledOnce();
  });

  it("reads an authoritative baseline before triggering from null local status", async () => {
    const baseline = "2026-08-05T12:00:00Z";
    const events: string[] = [];
    const accepted = Promise.withResolvers<{ data: undefined; response: Response }>();
    const get = vi.fn(async (path: string) => {
      events.push(`GET ${path}`);
      return path === "/sync/status"
        ? { data: { running: false, last_run_at: baseline, last_error: "" } }
        : { data: { provider_pools: {}, local_ceilings: {} } };
    });
    const post = vi.fn(() => {
      events.push("POST /sync");
      return accepted.promise;
    });
    const store = createSyncStore({ GET: get, POST: post } as unknown as GeneratedClient);

    store.triggerSync();
    await vi.waitFor(() => expect(post).toHaveBeenCalledOnce());

    expect(events.slice(0, 2)).toEqual(["GET /sync/status", "POST /sync"]);
    expect(store.getSyncState()).toEqual({
      running: true,
      last_run_at: baseline,
      last_error: "",
    });

    accepted.resolve({ data: undefined, response: new Response(null, { status: 202 }) });
    await vi.waitFor(() => expect(get).toHaveBeenCalledTimes(3));
  });

  it("does not publish an idle refresh that started before the trigger", async () => {
    const baseline = "2026-08-05T12:00:00Z";
    const staleCompletion = "2026-08-05T12:00:30Z";
    const staleStatus = Promise.withResolvers<{
      data: { running: boolean; last_run_at: string; last_error: string };
    }>();
    const accepted = Promise.withResolvers<{ data: undefined; response: Response }>();
    let statusReads = 0;
    const get = vi.fn((path: string) => {
      if (path !== "/sync/status") {
        return Promise.resolve({ data: { provider_pools: {}, local_ceilings: {} } });
      }
      statusReads += 1;
      if (statusReads === 1) return staleStatus.promise;
      return Promise.resolve({ data: { running: true, last_run_at: baseline, last_error: "" } });
    });
    const post = vi.fn(() => accepted.promise);
    const store = createSyncStore({ GET: get, POST: post } as unknown as GeneratedClient);
    store.setSyncStatus({ running: false, last_run_at: baseline, last_error: "" });
    if (runtime === undefined) throw new Error("test runtime was not initialized");
    const refresh = runtime.runCommand(store.refreshSyncStatusEffect, {
      operation: "test pre-trigger sync refresh",
      safeContext: {},
      onFailure: () => {},
    });
    await vi.waitFor(() => expect(statusReads).toBe(1));

    store.triggerSync();
    await vi.waitFor(() => expect(post).toHaveBeenCalledOnce());
    staleStatus.resolve({
      data: { running: false, last_run_at: staleCompletion, last_error: "" },
    });
    await Effect.runPromise(refresh.await);

    expect(store.getSyncState()).toEqual({
      running: true,
      last_run_at: baseline,
      last_error: "",
    });

    accepted.resolve({ data: undefined, response: new Response(null, { status: 202 }) });
    await vi.waitFor(() => expect(statusReads).toBe(2));
  });

  it("wakes idle polling when a sync starts without an event stream", async () => {
    vi.useFakeTimers();
    const pendingSync = Promise.withResolvers<{
      data: undefined;
      response: Response;
    }>();
    const get = vi.fn(async (path: string) =>
      path === "/sync/status"
        ? { data: { running: false, last_run_at: "", last_error: "" } }
        : { data: { provider_pools: {}, local_ceilings: {} } },
    );
    const post = vi.fn(() => pendingSync.promise);
    const store = createSyncStore({ GET: get, POST: post } as unknown as GeneratedClient);
    if (runtime === undefined) throw new Error("test runtime was not initialized");
    const polling = runtime.runCommand(store.pollingEffect, {
      operation: "test sync polling",
      safeContext: {},
      onFailure: () => {},
    });

    await vi.waitFor(() => expect(get).toHaveBeenCalledTimes(2));
    get.mockClear();
    store.triggerSync();
    await vi.waitFor(() => expect(post).toHaveBeenCalledOnce());

    await vi.advanceTimersByTimeAsync(2_000);

    expect(get).toHaveBeenCalled();
    polling.interrupt();
  });

  it("fails stale-event reconciliation when authoritative sync status cannot be read", async () => {
    const store = createSyncStore({
      GET: vi.fn(async (path: string) =>
        path === "/sync/status"
          ? {
              error: {
                code: "serviceUnavailable",
                detail: "sync status unavailable",
                title: "Service unavailable",
                type: "about:blank",
              },
              response: new Response(null, { status: 503 }),
            }
          : { data: { provider_pools: {}, local_ceilings: {} } },
      ),
      POST: vi.fn(),
    } as unknown as GeneratedClient);
    const onFailure = vi.fn();

    if (runtime === undefined) throw new Error("test runtime was not initialized");
    const execution = runtime.runCommand(store.reconcileSyncStatusEffect, {
      operation: "reconcile sync status after stale event cursor",
      safeContext: {},
      onFailure,
    });
    await Effect.runPromise(execution.await);

    expect(onFailure).toHaveBeenCalledWith(expect.objectContaining({ _tag: "ApiProblemError" }));
  });

  it("passes selected repo filters as sync priorities", async () => {
    const post = vi.fn(async () => ({
      data: undefined,
      error: undefined,
      response: new Response(null, { status: 202 }),
    }));
    const get = vi.fn(async (path: string) => {
      if (path === "/sync/status") {
        return {
          data: { running: false, last_run_at: "", last_error: "" },
        };
      }
      return { data: { provider_pools: {}, local_ceilings: {} } };
    });
    const store = createSyncStore(
      {
        GET: get,
        POST: post,
      } as unknown as GeneratedClient,
      {
        getPriorityRepos: () => "github|github.com/acme/first, github|github.com/acme/second",
      },
    );

    store.triggerSync();
    await vi.waitFor(() => expect(post).toHaveBeenCalledOnce());

    expect(post).toHaveBeenCalledWith("/sync", {
      params: {
        query: {
          priority_repo: ["github|github.com/acme/first", "github|github.com/acme/second"],
        },
      },
    });
  });

  it("passes one provider-qualified repository as the only sync scope", async () => {
    const post = vi.fn(async () => ({
      data: undefined,
      error: undefined,
      response: new Response(null, { status: 202 }),
    }));
    const get = vi.fn(async (path: string) => {
      if (path === "/sync/status") {
        return {
          data: { running: false, last_run_at: "", last_error: "" },
        };
      }
      return { data: { provider_pools: {}, local_ceilings: {} } };
    });
    const store = createSyncStore({
      GET: get,
      POST: post,
    } as unknown as GeneratedClient);

    store.triggerRepoSync("gitlab|gitlab.example.com/group/subgroup/project");
    await vi.waitFor(() => expect(post).toHaveBeenCalledOnce());

    expect(post).toHaveBeenCalledWith("/sync", {
      params: {
        query: {
          only_repo: ["gitlab|gitlab.example.com/group/subgroup/project"],
        },
      },
    });
  });
});
