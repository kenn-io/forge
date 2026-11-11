import { Effect } from "effect";
import { afterEach, beforeEach, describe, expect, it, vi } from "vite-plus/test";
import type { OwnedAppRuntime } from "../app/runtime.js";
import type { GeneratedClient } from "../api/generated-api.js";
import { makeTestAppRuntime } from "../testing/effect-layers.js";
import { makeGeneratedClient } from "../testing/generated-client.js";
import { GeneratedProblemResponse } from "../api/runtime.js";
import { createSyncStore as createRuntimeSyncStore, type SyncStoreOptions } from "./sync.svelte.js";

let runtime: OwnedAppRuntime | undefined;

function createSyncStore(client: GeneratedClient, options: Omit<SyncStoreOptions, "runtime"> = {}) {
  runtime = makeTestAppRuntime(client);
  return createRuntimeSyncStore({ ...options, runtime });
}

function startOnboarding(
  getStatus: GeneratedClient["SyncService"]["getSyncStatus"],
  post: GeneratedClient["SyncService"]["triggerSync"],
  onFailure = vi.fn(),
) {
  const store = createSyncStore(
    makeGeneratedClient({
      SyncService: {
        getSyncStatus: getStatus,
        getRateLimits: async () => ({ provider_pools: {}, local_ceilings: {} }),
        triggerSync: post,
      },
    }),
  );
  if (!runtime) throw new Error("test runtime was not initialized");
  const execution = runtime.runCommand(store.triggerOnboardingSyncEffect(), {
    operation: "start first sync",
    safeContext: {},
    onFailure,
  });
  return { store, execution };
}

beforeEach(() => {
  runtime = undefined;
});

afterEach(async () => {
  vi.useRealTimers();
  if (runtime !== undefined) await Effect.runPromise(runtime.disposeEffect);
});

describe("sync store", () => {
  it("does not repeat an acknowledged POST when the final status read fails", async () => {
    const idle = { running: false, last_run_at: "", last_error: "" };
    const problem = {
      code: "forbidden",
      detail: "status denied",
      status: 403,
      title: "Forbidden",
      type: "about:blank",
    };
    const getStatus = vi
      .fn()
      .mockResolvedValueOnce(idle)
      .mockResolvedValueOnce(idle)
      .mockRejectedValue(new GeneratedProblemResponse(problem, Response.json(problem, { status: 403 })));
    const post = vi.fn().mockResolvedValue(undefined);
    const onFailure = vi.fn();
    const { execution } = startOnboarding(getStatus, post, onFailure);
    await Effect.runPromise(execution.await);
    expect(post).toHaveBeenCalledOnce();
    expect(onFailure).toHaveBeenCalledOnce();
  });
  it("retries when an unrelated sync was already running before the trigger failed", async () => {
    const running = { running: true, last_run_at: "2026-08-05T12:00:00Z", last_error: "" };
    const getStatus = vi.fn().mockResolvedValue(running);
    const post = vi.fn().mockRejectedValueOnce(new Error("connection lost")).mockResolvedValue(undefined);
    const { execution } = startOnboarding(getStatus, post);
    await Effect.runPromise(execution.await);
    expect(post).toHaveBeenCalledTimes(2);
  });

  it("retries when the previous running sync completes before reconciliation", async () => {
    const running = { running: true, last_run_at: "2026-08-05T12:00:00Z", last_error: "" };
    const completed = { running: false, last_run_at: "2026-08-05T12:01:00Z", last_error: "" };
    const getStatus = vi.fn().mockResolvedValueOnce(running).mockResolvedValue(completed);
    const post = vi.fn().mockRejectedValueOnce(new Error("connection lost")).mockResolvedValue(undefined);
    const { execution } = startOnboarding(getStatus, post);
    await Effect.runPromise(execution.await);
    expect(post).toHaveBeenCalledTimes(2);
  });

  it("recognizes a completed first sync using the cached baseline when the fresh baseline fails", async () => {
    const cached = { running: false, last_run_at: "2026-08-05T12:00:00Z", last_error: "" };
    const completed = { running: false, last_run_at: "2026-08-05T12:01:00Z", last_error: "" };
    const getStatus = vi
      .fn()
      .mockRejectedValueOnce(new Error("connection lost"))
      .mockRejectedValueOnce(new Error("connection lost"))
      .mockRejectedValueOnce(new Error("connection lost"))
      .mockResolvedValue(completed);
    const post = vi.fn().mockResolvedValue(undefined);
    const store = createSyncStore(
      makeGeneratedClient({
        SyncService: {
          getSyncStatus: getStatus,
          getRateLimits: async () => ({ provider_pools: {}, local_ceilings: {} }),
          triggerSync: post,
        },
      }),
    );
    store.setSyncStatus(cached);
    if (!runtime) throw new Error("test runtime was not initialized");
    const execution = runtime.runCommand(store.triggerOnboardingSyncEffect(), {
      operation: "start first sync",
      safeContext: {},
      onFailure: () => {},
    });
    await Effect.runPromise(execution.await);
    expect(post).toHaveBeenCalledOnce();
    expect(store.getSyncState()).toMatchObject(completed);
  }, 10000);

  it("still starts a sync when the baseline cannot be read", async () => {
    const getStatus = vi.fn().mockRejectedValue(new Error("connection lost"));
    const post = vi.fn().mockResolvedValue(undefined);
    const { execution } = startOnboarding(getStatus, post);
    await vi.waitFor(() => expect(post).toHaveBeenCalledOnce(), { timeout: 8000 });
    execution.interrupt();
    await Effect.runPromise(execution.await);
  }, 10000);

  it("retries when a missing baseline cannot attribute a running sync", async () => {
    const problem = {
      code: "serviceUnavailable",
      detail: "status unavailable",
      status: 503,
      title: "Unavailable",
      type: "about:blank",
    };
    const running = { running: true, last_run_at: "", last_error: "" };
    const getStatus = vi
      .fn()
      .mockRejectedValueOnce(new GeneratedProblemResponse(problem, Response.json(problem, { status: 503 })))
      .mockResolvedValue(running);
    const post = vi.fn().mockRejectedValueOnce(new Error("connection lost")).mockResolvedValue(undefined);
    const { execution } = startOnboarding(getStatus, post);
    await Effect.runPromise(execution.await);
    expect(post).toHaveBeenCalledTimes(2);
  });

  it("retries a stopping syncer even if another sync is running", async () => {
    const running = { running: true, last_run_at: "", last_error: "" };
    const problem = {
      code: "serviceUnavailable",
      detail: "syncer is stopping",
      status: 503,
      title: "Service Unavailable",
      type: "about:blank",
    };
    const post = vi
      .fn()
      .mockRejectedValueOnce(new GeneratedProblemResponse(problem, Response.json(problem, { status: 503 })))
      .mockResolvedValue(undefined);
    const { execution } = startOnboarding(async () => running, post);
    await Effect.runPromise(execution.await);
    expect(post).toHaveBeenCalledTimes(2);
  });

  it("requires an acknowledged trigger after a definite stopping response", async () => {
    const idle = { running: false, last_run_at: "", last_error: "" };
    const running = { running: true, last_run_at: "", last_error: "" };
    const problem = {
      code: "serviceUnavailable",
      detail: "syncer is stopping",
      status: 503,
      title: "Service Unavailable",
      type: "about:blank",
    };
    const getStatus = vi.fn().mockResolvedValueOnce(idle).mockResolvedValue(running);
    const post = vi
      .fn()
      .mockRejectedValueOnce(new GeneratedProblemResponse(problem, Response.json(problem, { status: 503 })))
      .mockRejectedValueOnce(new Error("response lost"))
      .mockResolvedValue(undefined);
    const { execution } = startOnboarding(getStatus, post);
    await Effect.runPromise(execution.await);
    expect(post).toHaveBeenCalledTimes(3);
  });

  it.each(["syncer not configured", "provider sync is disabled"])(
    "does not retry a permanent 503: %s",
    async (detail) => {
      const problem = {
        code: "serviceUnavailable",
        detail,
        status: 503,
        title: "Service Unavailable",
        type: "about:blank",
      };
      const post = vi
        .fn()
        .mockRejectedValue(new GeneratedProblemResponse(problem, Response.json(problem, { status: 503 })));
      const { store, execution } = startOnboarding(
        async () => ({ running: false, last_run_at: "", last_error: "" }),
        post,
      );
      await Effect.runPromise(execution.await);
      expect(post).toHaveBeenCalledOnce();
      expect(store.getSyncState()?.last_error).toBe(detail);
    },
  );

  it("stops when transient status reads cannot confirm whether the trigger started", async () => {
    const idle = { running: false, last_run_at: "", last_error: "" };
    const getStatus = vi
      .fn()
      .mockResolvedValueOnce(idle)
      .mockRejectedValueOnce(new Error("connection lost"))
      .mockRejectedValueOnce(new Error("connection lost"))
      .mockRejectedValueOnce(new Error("connection lost"))
      .mockResolvedValue(idle);
    const post = vi.fn().mockRejectedValueOnce(new Error("connection lost")).mockResolvedValue(undefined);
    const { store, execution } = startOnboarding(getStatus, post);
    await Effect.runPromise(execution.await);
    expect(post).toHaveBeenCalledOnce();
    expect(store.getSyncState()).toMatchObject({ running: false, last_error: "Could not reach Kenn Forge" });
  }, 10000);

  it("retries the first sync after a fresh idle status confirms no run started", async () => {
    const baseline = { running: false, last_run_at: "2026-08-05T12:00:00Z", last_error: "" };
    const getStatus = vi.fn(async () => baseline);
    const post = vi.fn().mockRejectedValueOnce(new Error("connection lost")).mockResolvedValue(undefined);
    const { store, execution } = startOnboarding(getStatus, post);
    await Effect.runPromise(execution.await);
    expect(post).toHaveBeenCalledTimes(2);
    expect(getStatus.mock.calls.length).toBeGreaterThanOrEqual(2);
    expect(store.getSyncState()?.running).toBe(true);
  });

  it.each([
    { running: true, last_run_at: "2026-08-05T12:00:00Z", last_error: "" },
    { running: false, last_run_at: "2026-08-05T12:01:00Z", last_error: "" },
  ])("does not repeat a first sync when fresh status proves admission: $running", async (admitted) => {
    const baseline = { running: false, last_run_at: "2026-08-05T12:00:00Z", last_error: "" };
    const getStatus = vi.fn().mockResolvedValueOnce(baseline).mockResolvedValue(admitted);
    const post = vi.fn().mockRejectedValue(new Error("response lost"));
    const { store, execution } = startOnboarding(getStatus, post);
    await Effect.runPromise(execution.await);
    expect(post).toHaveBeenCalledOnce();
    expect(store.getSyncState()).toMatchObject(admitted);
  });

  it("stops first-sync recovery when status reconciliation fails", async () => {
    const baseline = { running: false, last_run_at: "", last_error: "" };
    const problem = {
      code: "unavailable",
      detail: "status unavailable",
      status: 503,
      title: "Unavailable",
      type: "about:blank",
    };
    const getStatus = vi
      .fn()
      .mockResolvedValueOnce(baseline)
      .mockRejectedValue(new GeneratedProblemResponse(problem, Response.json(problem, { status: 503 })));
    const post = vi.fn().mockRejectedValue(new Error("response lost"));
    const onFailure = vi.fn();
    const { store, execution } = startOnboarding(getStatus, post, onFailure);
    await Effect.runPromise(execution.await);
    expect(post).toHaveBeenCalledOnce();
    expect(onFailure).toHaveBeenCalledOnce();
    expect(store.getSyncState()).toMatchObject({ running: false, last_error: "status unavailable" });
  });

  it("does not retry a rejected first sync", async () => {
    const problem = {
      code: "forbidden",
      detail: "Access denied",
      status: 403,
      title: "Forbidden",
      type: "about:blank",
    };
    const getStatus = vi.fn().mockResolvedValue({ running: false, last_run_at: "", last_error: "" });
    const post = vi
      .fn()
      .mockRejectedValue(new GeneratedProblemResponse(problem, Response.json(problem, { status: 403 })));
    const { store, execution } = startOnboarding(getStatus, post);
    await Effect.runPromise(execution.await);
    expect(post).toHaveBeenCalledOnce();
    expect(getStatus).toHaveBeenCalledOnce();
    expect(store.getSyncState()).toMatchObject({ running: false, last_error: "Access denied" });
  });

  it("stops after exhausting first-sync retries", async () => {
    const getStatus = vi.fn().mockResolvedValue({ running: false, last_run_at: "", last_error: "" });
    const post = vi.fn().mockRejectedValue(new Error("connection lost"));
    const { store, execution } = startOnboarding(getStatus, post);
    await Effect.runPromise(execution.await);
    expect(post).toHaveBeenCalledTimes(3);
    expect(store.getSyncState()).toMatchObject({ running: false, last_error: "Could not reach Kenn Forge" });
  });

  it("interrupts recovery during backoff", async () => {
    vi.useFakeTimers();
    const idle = { running: false, last_run_at: "", last_error: "" };
    const getStatus = vi.fn().mockResolvedValue(idle);
    const post = vi.fn().mockRejectedValue(new Error("connection lost"));
    const { execution } = startOnboarding(getStatus, post);
    await vi.waitFor(() => expect(getStatus).toHaveBeenCalledTimes(2));
    execution.interrupt();
    await Effect.runPromise(execution.await);
    await vi.advanceTimersByTimeAsync(5000);
    expect(post).toHaveBeenCalledOnce();
  });

  it("tracks hub availability independently of cached sync status", () => {
    const store = createSyncStore(makeGeneratedClient());

    expect(store.getProviderAvailable()).toBe(true);
    store.setSyncStatus({ running: false, last_run_at: "2026-08-05T12:00:00Z", last_error: "" });
    store.setProviderAvailable(false);

    expect(store.getProviderAvailable()).toBe(false);
    expect(store.getSyncState()?.last_run_at).toBe("2026-08-05T12:00:00Z");
  });

  it("leaves list reloads after a sync to data_changed while live updates are connected", () => {
    const store = createSyncStore(makeGeneratedClient());
    const always = vi.fn();
    const withoutLiveUpdates = vi.fn();
    store.subscribeSyncComplete(always);
    store.subscribeSyncCompleteWithoutLiveUpdates(withoutLiveUpdates);
    const finishSync = () => {
      store.setSyncStatus({ running: true, last_run_at: "2026-08-05T12:00:00Z", last_error: "" });
      store.setSyncStatus({ running: false, last_run_at: "2026-08-05T12:01:00Z", last_error: "" });
    };

    store.setLiveUpdatesConnected(true);
    finishSync();
    expect(always).toHaveBeenCalledTimes(1);
    expect(withoutLiveUpdates).not.toHaveBeenCalled();

    store.setLiveUpdatesConnected(false);
    finishSync();
    expect(always).toHaveBeenCalledTimes(2);
    expect(withoutLiveUpdates).toHaveBeenCalledTimes(1);
  });

  it("keeps an acknowledged trigger running through a stale idle status", async () => {
    const baseline = "2026-08-05T12:00:00Z";
    const getStatus = vi.fn(async () => ({ running: false, last_run_at: baseline, last_error: "" }));
    const getRates = vi.fn(async () => ({ provider_pools: {}, local_ceilings: {} }));
    const post = vi.fn(async () => undefined);
    const store = createSyncStore(
      makeGeneratedClient({ SyncService: { getSyncStatus: getStatus, getRateLimits: getRates, triggerSync: post } }),
    );
    store.setSyncStatus({ running: false, last_run_at: baseline, last_error: "" });

    store.triggerSync();
    await vi.waitFor(() => expect(getStatus).toHaveBeenCalledOnce());
    expect(getRates).toHaveBeenCalledOnce();

    expect(store.getSyncState()).toEqual({
      running: true,
      last_run_at: baseline,
      last_error: "",
    });
  });

  it("accepts trigger completion when last-run time advances without observing running", async () => {
    const baseline = "2026-08-05T12:00:00Z";
    const completed = "2026-08-05T12:01:00Z";
    const getStatus = vi.fn(async () => ({ running: false, last_run_at: completed, last_error: "" }));
    const getRates = vi.fn(async () => ({ provider_pools: {}, local_ceilings: {} }));
    const post = vi.fn(async () => undefined);
    const store = createSyncStore(
      makeGeneratedClient({ SyncService: { getSyncStatus: getStatus, getRateLimits: getRates, triggerSync: post } }),
    );
    const onComplete = vi.fn();
    store.setSyncStatus({ running: false, last_run_at: baseline, last_error: "" });
    store.subscribeSyncComplete(onComplete);

    store.triggerSync();
    await vi.waitFor(() => expect(getStatus).toHaveBeenCalledOnce());
    expect(getRates).toHaveBeenCalledOnce();

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
    const accepted = Promise.withResolvers<void>();
    const getStatus = vi.fn(async () => {
      events.push("GET /sync/status");
      return { running: false, last_run_at: baseline, last_error: "" };
    });
    const getRates = vi.fn(async () => ({ provider_pools: {}, local_ceilings: {} }));
    const post = vi.fn(() => {
      events.push("POST /sync");
      return accepted.promise;
    });
    const store = createSyncStore(
      makeGeneratedClient({ SyncService: { getSyncStatus: getStatus, getRateLimits: getRates, triggerSync: post } }),
    );

    store.triggerSync();
    await vi.waitFor(() => expect(post).toHaveBeenCalledOnce());

    expect(events.slice(0, 2)).toEqual(["GET /sync/status", "POST /sync"]);
    expect(store.getSyncState()).toEqual({
      running: true,
      last_run_at: baseline,
      last_error: "",
    });

    accepted.resolve();
    await vi.waitFor(() => expect(getStatus).toHaveBeenCalledTimes(2));
  });

  it("keeps failing repositories while a manual trigger is pending and after it fails", async () => {
    const baseline = "2026-08-05T12:00:00Z";
    const repoErrors = [
      { repository: "acme/gone", host: "github.com", error: "404 Not Found", since: "2026-08-05T11:00:00Z" },
    ];
    const rejected = Promise.withResolvers<void>();
    const post = vi.fn(() => rejected.promise);
    const store = createSyncStore(makeGeneratedClient({ SyncService: { triggerSync: post } }));
    store.setSyncStatus({ running: false, last_run_at: baseline, last_error: "", repo_errors: repoErrors });

    store.triggerSync();
    await vi.waitFor(() => expect(post).toHaveBeenCalledOnce());
    expect(store.getSyncState()).toMatchObject({ running: true, repo_errors: repoErrors });

    rejected.reject(new Error("daemon unreachable"));
    await vi.waitFor(() => expect(store.getSyncState()?.running).toBe(false));
    expect(store.getSyncState()?.last_error).not.toBe("");
    expect(store.getSyncState()?.repo_errors).toEqual(repoErrors);
  });

  it("does not publish an idle refresh that started before the trigger", async () => {
    const baseline = "2026-08-05T12:00:00Z";
    const staleCompletion = "2026-08-05T12:00:30Z";
    const staleStatus = Promise.withResolvers<{ running: boolean; last_run_at: string; last_error: string }>();
    const accepted = Promise.withResolvers<void>();
    let statusReads = 0;
    const getStatus = vi.fn(() => {
      statusReads += 1;
      if (statusReads === 1) return staleStatus.promise;
      return Promise.resolve({ running: true, last_run_at: baseline, last_error: "" });
    });
    const getRates = vi.fn(async () => ({ provider_pools: {}, local_ceilings: {} }));
    const post = vi.fn(() => accepted.promise);
    const store = createSyncStore(
      makeGeneratedClient({ SyncService: { getSyncStatus: getStatus, getRateLimits: getRates, triggerSync: post } }),
    );
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
    staleStatus.resolve({ running: false, last_run_at: staleCompletion, last_error: "" });
    await Effect.runPromise(refresh.await);

    expect(store.getSyncState()).toEqual({
      running: true,
      last_run_at: baseline,
      last_error: "",
    });

    accepted.resolve();
    await vi.waitFor(() => expect(statusReads).toBe(2));
  });

  it("publishes refreshed rate limits even when a sync status event lands mid-read", async () => {
    const raised = {
      provider_pools: {},
      local_ceilings: {
        "github.com": {
          provider: "github",
          platform_host: "github.com",
          rate_principal: "",
          principal_label: "",
          limit: 3000,
          background_limit: 2700,
          spent: 500,
          remaining: 2500,
          reset_at: "2026-08-05T13:00:00Z",
        },
      },
    };
    const pendingRates = Promise.withResolvers<typeof raised>();
    const getRates = vi.fn(() => pendingRates.promise);
    const store = createSyncStore(makeGeneratedClient({ SyncService: { getRateLimits: getRates } }));

    store.refreshRateLimits();
    await vi.waitFor(() => expect(getRates).toHaveBeenCalledOnce());
    store.setSyncStatus({ running: true, last_run_at: "2026-08-05T12:00:00Z", last_error: "" });
    pendingRates.resolve(raised);

    await vi.waitFor(() => expect(store.getRateLimits().local_ceilings["github.com"]?.limit).toBe(3000));
  });

  it("wakes idle polling when a sync starts without an event stream", async () => {
    vi.useFakeTimers();
    const pendingSync = Promise.withResolvers<void>();
    const getStatus = vi.fn(async () => ({ running: false, last_run_at: "", last_error: "" }));
    const getRates = vi.fn(async () => ({ provider_pools: {}, local_ceilings: {} }));
    const post = vi.fn(() => pendingSync.promise);
    const store = createSyncStore(
      makeGeneratedClient({ SyncService: { getSyncStatus: getStatus, getRateLimits: getRates, triggerSync: post } }),
    );
    if (runtime === undefined) throw new Error("test runtime was not initialized");
    const polling = runtime.runCommand(store.pollingEffect, {
      operation: "test sync polling",
      safeContext: {},
      onFailure: () => {},
    });

    await vi.waitFor(() => expect(getStatus).toHaveBeenCalledOnce());
    expect(getRates).toHaveBeenCalledOnce();
    getStatus.mockClear();
    store.triggerSync();
    await vi.waitFor(() => expect(post).toHaveBeenCalledOnce());

    await vi.advanceTimersByTimeAsync(2_000);

    expect(getStatus).toHaveBeenCalled();
    polling.interrupt();
  });

  it("uses slow polling with live updates and resumes fast polling when they disconnect", async () => {
    vi.useFakeTimers();
    const running = { running: true, last_run_at: "", last_error: "" };
    const getStatus = vi.fn(async () => running);
    const getRates = vi.fn(async () => ({ provider_pools: {}, local_ceilings: {} }));
    const store = createSyncStore(
      makeGeneratedClient({ SyncService: { getSyncStatus: getStatus, getRateLimits: getRates } }),
    );
    store.setLiveUpdatesConnected(true);
    store.setSyncStatus(running);
    if (runtime === undefined) throw new Error("test runtime was not initialized");
    const polling = runtime.runCommand(store.pollingEffect, {
      operation: "test sync polling with live updates",
      safeContext: {},
      onFailure: () => {},
    });

    await vi.waitFor(() => expect(getStatus).toHaveBeenCalledOnce());
    await vi.advanceTimersByTimeAsync(6_000);
    expect(getStatus).toHaveBeenCalledOnce();
    expect(getRates).toHaveBeenCalledOnce();

    store.setLiveUpdatesConnected(false);
    await vi.advanceTimersByTimeAsync(2_000);
    expect(getStatus.mock.calls.length).toBeGreaterThanOrEqual(2);
    polling.interrupt();
  });

  it("fails stale-event reconciliation when authoritative sync status cannot be read", async () => {
    const store = createSyncStore(
      makeGeneratedClient({
        SyncService: {
          getSyncStatus: vi.fn(async () => {
            const problem = {
              code: "serviceUnavailable",
              detail: "sync status unavailable",
              status: 503,
              title: "Service unavailable",
              type: "about:blank",
            };
            throw new GeneratedProblemResponse(problem, Response.json(problem, { status: 503 }));
          }),
        },
      }),
    );
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
    const post = vi.fn(async () => undefined);
    const getStatus = vi.fn(async () => ({ running: false, last_run_at: "", last_error: "" }));
    const getRates = vi.fn(async () => ({ provider_pools: {}, local_ceilings: {} }));
    const store = createSyncStore(
      makeGeneratedClient({ SyncService: { getSyncStatus: getStatus, getRateLimits: getRates, triggerSync: post } }),
      {
        getPriorityRepos: () => "github|github.com/acme/first, github|github.com/acme/second",
      },
    );

    store.triggerSync();
    await vi.waitFor(() => expect(post).toHaveBeenCalledOnce());

    expect(post).toHaveBeenCalledWith({
      priority_repo: ["github|github.com/acme/first", "github|github.com/acme/second"],
    });
  });

  it("passes one provider-qualified repository as the only sync scope", async () => {
    const post = vi.fn(async () => undefined);
    const getStatus = vi.fn(async () => ({ running: false, last_run_at: "", last_error: "" }));
    const getRates = vi.fn(async () => ({ provider_pools: {}, local_ceilings: {} }));
    const store = createSyncStore(
      makeGeneratedClient({ SyncService: { getSyncStatus: getStatus, getRateLimits: getRates, triggerSync: post } }),
    );

    store.triggerRepoSync("gitlab|gitlab.example.com/group/subgroup/project");
    await vi.waitFor(() => expect(post).toHaveBeenCalledOnce());

    expect(post).toHaveBeenCalledWith({
      only_repo: ["gitlab|gitlab.example.com/group/subgroup/project"],
    });
  });
});
