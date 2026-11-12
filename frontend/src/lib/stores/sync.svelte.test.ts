import { Effect } from "effect";
import { afterEach, beforeEach, describe, expect, it, vi } from "vite-plus/test";
import type { OwnedAppRuntime } from "../app/runtime.js";
import type { GeneratedClient } from "../api/generated-api.js";
import { makeTestAppRuntime } from "../testing/effect-layers.js";
import { makeGeneratedClient } from "../testing/generated-client.js";
import { GeneratedProblemResponse } from "../api/runtime.js";
import type { SyncStatus } from "../api/types.js";
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
  describe("first sync recovery", () => {
    const idle = { running: false, last_run_at: "", last_error: "" };
    const receipt = { instance_id: "instance", generation: 1 };
    const succeeded = { instance_id: "instance", settled_generation: 1, successful_generation: 1 };
    const noRates = () => vi.fn().mockResolvedValue({ provider_pools: {}, local_ceilings: {} });
    const problem = (code: string, status: number) => {
      const body = { code, status, title: code, detail: code, type: "about:blank" };
      return new GeneratedProblemResponse(body, Response.json(body, { status }));
    };
    const syncStore = (SyncService: Partial<GeneratedClient["SyncService"]>, initial?: SyncStatus) => {
      const store = createSyncStore(makeGeneratedClient({ SyncService }));
      if (initial) store.setSyncStatus(initial);
      return store;
    };
    const run = (effect: Effect.Effect<void, unknown>, onFailure: () => void = () => {}) => {
      if (!runtime) throw new Error("test runtime was not initialized");
      return runtime.runCommand(effect, { operation: "first sync", safeContext: {}, onFailure });
    };
    const firstSync = (store: ReturnType<typeof syncStore>, onFailure?: () => void, onSuccess?: () => void) =>
      run(store.triggerOnboardingSyncEffect().pipe(Effect.tap(() => Effect.sync(() => onSuccess?.()))), onFailure);

    it.each([
      { name: "temporary outage", failure: problem("hubUnavailable", 503), attempts: 2, error: "" },
      { name: "gateway error", failure: problem("mutationOutcomeUnknown", 502), attempts: 2, error: "" },
      { name: "forbidden", failure: problem("forbidden", 403), attempts: 1, error: "forbidden" },
      {
        name: "service refusal",
        failure: problem("serviceUnavailable", 503),
        attempts: 1,
        error: "serviceUnavailable",
      },
      {
        name: "exhausted transport errors",
        failure: new Error("response lost"),
        attempts: 3,
        error: "Could not reach Kenn Forge",
      },
    ])("handles $name", async ({ failure, attempts, error }) => {
      if (attempts === 3) vi.useFakeTimers();
      const post = vi.fn().mockRejectedValue(failure);
      if (attempts === 2) post.mockRejectedValueOnce(failure).mockResolvedValue(receipt);
      const getStatus = vi.fn().mockResolvedValue({ ...idle, full_sync: succeeded });
      const store = syncStore({ triggerSync: post, getSyncStatus: getStatus }, idle);
      const execution = firstSync(store);
      if (attempts === 3) await vi.advanceTimersByTimeAsync(120000);
      await Effect.runPromise(execution.await);
      expect(post).toHaveBeenCalledTimes(attempts);
      expect(getStatus).toHaveBeenCalledTimes(attempts === 2 ? 1 : 0);
      expect(store.getSyncState()?.last_error).toBe(error);
    });

    it.each(["request", "observed progress"])(
      "preserves the right sync state on interruption during %s",
      async (phase) => {
        const post = vi.fn((_params?: unknown, _options?: { signal?: AbortSignal }) => Promise.resolve());
        post.mockImplementationOnce(() => new Promise<void>(() => {}));
        const store = syncStore({ triggerSync: post, getSyncStatus: vi.fn().mockResolvedValue(idle) }, idle);
        const complete = vi.fn();
        store.subscribeSyncComplete(complete);
        const execution = firstSync(store);
        await vi.waitFor(() => expect(post).toHaveBeenCalledOnce());
        const running = { ...idle, running: true, current_repo: "acme/project" };
        if (phase === "observed progress") store.setSyncStatus(running);
        execution.interrupt();
        await Effect.runPromise(execution.await);
        if (phase === "request") expect(post.mock.calls[0][1]?.signal?.aborted).toBe(true);
        expect(store.getSyncState()).toEqual(phase === "observed progress" ? running : idle);
        expect(complete).not.toHaveBeenCalled();
      },
    );

    it.each(["refresh", "reconcile"])("reports a permanent %s failure after a transient read", async (path) => {
      const getStatus = vi
        .fn()
        .mockRejectedValueOnce(problem("hubUnavailable", 503))
        .mockRejectedValue(problem("forbidden", 403));
      const post = vi.fn().mockResolvedValue(receipt);
      const store = syncStore({ triggerSync: post, getSyncStatus: getStatus, getRateLimits: noRates() }, idle);
      const failure = vi.fn();
      const execution = firstSync(store, failure);
      await vi.waitFor(() => expect(getStatus).toHaveBeenCalledOnce());
      expect(failure).not.toHaveBeenCalled();
      await Effect.runPromise(
        run(path === "refresh" ? store.refreshSyncStatusEffect : store.reconcileSyncStatusEffect).await,
      );
      await Effect.runPromise(execution.await);
      expect(failure).toHaveBeenCalledOnce();
      expect(store.getSyncState()).toEqual(idle);
      expect(post).toHaveBeenCalledOnce();
    });

    it("waits through transient status and quota failures for the matching completion", async () => {
      const getStatus = vi.fn().mockRejectedValue(problem("hubUnavailable", 503));
      const post = vi.fn().mockResolvedValue(receipt);
      const getRateLimits = vi.fn().mockRejectedValue(problem("forbidden", 403));
      const store = syncStore({ triggerSync: post, getSyncStatus: getStatus, getRateLimits }, idle);
      const failure = vi.fn();
      const completed = vi.fn();
      const execution = firstSync(store, failure, completed);
      await vi.waitFor(() => expect(getStatus).toHaveBeenCalledOnce());
      await Effect.runPromise(run(store.refreshSyncStatusEffect).await);
      expect(failure).not.toHaveBeenCalled();
      expect(completed).not.toHaveBeenCalled();
      store.setSyncStatus({ ...idle, full_sync: succeeded });
      await Effect.runPromise(execution.await);
      expect(completed).toHaveBeenCalledOnce();
      expect(post).toHaveBeenCalledOnce();
    });

    it.each(["before acceptance", "after acceptance"])("ignores a forbidden read started %s", async (phase) => {
      const accepted = Promise.withResolvers<typeof receipt>();
      const pending = Promise.withResolvers<never>();
      const getStatus = vi
        .fn()
        .mockImplementationOnce(() => pending.promise)
        .mockResolvedValue(idle);
      const post = vi.fn(() => accepted.promise);
      const store = syncStore({ triggerSync: post, getSyncStatus: getStatus, getRateLimits: noRates() }, idle);
      const failure = vi.fn();
      const completed = vi.fn();
      const execution = firstSync(store, failure, completed);
      await vi.waitFor(() => expect(post).toHaveBeenCalledOnce());
      const read = run(store.refreshSyncStatusEffect);
      await vi.waitFor(() => expect(getStatus).toHaveBeenCalledOnce());
      if (phase === "before acceptance") {
        pending.reject(problem("forbidden", 403));
        await Effect.runPromise(read.await);
      }
      accepted.resolve(receipt);
      await vi.waitFor(() => expect(getStatus).toHaveBeenCalledTimes(2));
      if (phase === "after acceptance") {
        pending.reject(problem("forbidden", 403));
        await Effect.runPromise(read.await);
        expect(failure).not.toHaveBeenCalled();
      }
      store.setSyncStatus({ ...idle, full_sync: succeeded });
      await Effect.runPromise(execution.await);
      expect(completed).toHaveBeenCalledOnce();
      expect(failure).not.toHaveBeenCalled();
      expect(post).toHaveBeenCalledOnce();
    });

    it("publishes a fast accepted completion after the initial status poll fails", async () => {
      const completeStatus = { running: false, last_run_at: "2026-08-02T12:01:00Z", full_sync: succeeded };
      const getStatus = vi.fn().mockRejectedValueOnce(problem("forbidden", 403)).mockResolvedValue(completeStatus);
      const post = vi.fn().mockResolvedValue(receipt);
      const store = syncStore({ triggerSync: post, getSyncStatus: getStatus, getRateLimits: noRates() });
      const complete = vi.fn();
      store.subscribeSyncComplete(complete);
      await Effect.runPromise(run(store.refreshSyncStatusEffect).await);
      expect(store.getSyncState()).toBeNull();
      await Effect.runPromise(firstSync(store).await);
      expect(store.getSyncState()).toMatchObject(completeStatus);
      expect(complete).toHaveBeenCalledOnce();
    });

    it("publishes a fast completion received before the sync receipt", async () => {
      const accepted = Promise.withResolvers<typeof receipt>();
      const post = vi.fn(() => accepted.promise);
      const store = syncStore({
        triggerSync: post,
        getSyncStatus: vi.fn().mockRejectedValue(problem("forbidden", 403)),
      });
      const complete = vi.fn();
      store.subscribeSyncComplete(complete);
      const execution = firstSync(store);
      await vi.waitFor(() => expect(post).toHaveBeenCalledOnce());
      const completed = { running: false, last_run_at: "2026-08-02T12:01:00Z", full_sync: succeeded };
      store.setSyncStatus(completed);
      accepted.resolve(receipt);
      await Effect.runPromise(execution.await);
      expect(store.getSyncState()).toMatchObject(completed);
      expect(complete).toHaveBeenCalledOnce();
    });

    it("keeps live progress when an older onboarding status read finishes", async () => {
      const stale = Promise.withResolvers<{ running: boolean; last_run_at: string }>();
      const getStatus = vi.fn(() => stale.promise);
      const store = syncStore({ triggerSync: vi.fn().mockResolvedValue(receipt), getSyncStatus: getStatus }, idle);
      const execution = firstSync(store);
      await vi.waitFor(() => expect(getStatus).toHaveBeenCalledOnce());
      store.setSyncStatus({ running: true, progress: "2/3" });
      stale.resolve({ running: false, last_run_at: "2026-08-02T12:01:00Z" });
      await new Promise((resolve) => setTimeout(resolve, 0));
      expect(store.getSyncState()).toMatchObject({ running: true, progress: "2/3" });
      execution.interrupt();
      await Effect.runPromise(execution.await);
    });

    it("finishes from an event while the status refresh is delayed", async () => {
      const getStatus = vi.fn(() => new Promise<never>(() => {}));
      const post = vi.fn().mockResolvedValue({ instance_id: "instance", generation: 1 });
      const store = createSyncStore(
        makeGeneratedClient({ SyncService: { triggerSync: post, getSyncStatus: getStatus } }),
      );
      store.setSyncStatus(idle);
      if (!runtime) throw new Error("test runtime was not initialized");
      const finished = vi.fn();
      const execution = runtime.runCommand(
        store.triggerOnboardingSyncEffect().pipe(Effect.tap(() => Effect.sync(finished))),
        {
          operation: "first sync",
          safeContext: {},
          onFailure: () => {},
        },
      );
      await vi.waitFor(() => expect(getStatus).toHaveBeenCalledOnce());
      store.setSyncStatus({
        running: true,
        full_sync: { instance_id: "instance", settled_generation: 1, successful_generation: 1 },
      });
      await Effect.runPromise(execution.await);
      expect(finished).toHaveBeenCalledOnce();
    });

    it("keeps observed status when the accepted wait is interrupted", async () => {
      const getStatus = vi.fn().mockResolvedValue({
        ...idle,
        full_sync: { instance_id: "instance", settled_generation: 0, successful_generation: 0 },
      });
      const post = vi.fn().mockResolvedValue({ instance_id: "instance", generation: 1 });
      const store = createSyncStore(
        makeGeneratedClient({ SyncService: { triggerSync: post, getSyncStatus: getStatus } }),
      );
      store.setSyncStatus(idle);
      if (!runtime) throw new Error("test runtime was not initialized");
      const execution = runtime.runCommand(store.triggerOnboardingSyncEffect(), {
        operation: "first sync",
        safeContext: {},
        onFailure: () => {},
      });
      await vi.waitFor(() => expect(getStatus).toHaveBeenCalledOnce());
      const running = { ...idle, running: true, progress: "Syncing" };
      store.setSyncStatus(running);
      execution.interrupt();
      await Effect.runPromise(execution.await);
      expect(store.getSyncState()).toEqual(running);
      expect(post).toHaveBeenCalledOnce();
    });

    it("restores the latest completion when a retried request is interrupted", async () => {
      const post = vi
        .fn()
        .mockRejectedValueOnce(new Error("response lost"))
        .mockImplementation(() => new Promise<void>(() => {}));
      const store = syncStore({ triggerSync: post }, { ...idle, running: true, last_run_at: "2026-08-02T12:00:00Z" });
      const execution = firstSync(store);
      await vi.waitFor(() => expect(post).toHaveBeenCalledOnce());
      const completed = { ...idle, last_run_at: "2026-08-02T12:00:30Z" };
      store.setSyncStatus(completed);
      await vi.waitFor(() => expect(post).toHaveBeenCalledTimes(2));
      execution.interrupt();
      await Effect.runPromise(execution.await);
      expect(store.getSyncState()).toEqual(completed);
    });
    it("shows sync progress while a retry response is delayed", async () => {
      const accepted = Promise.withResolvers<void>();
      const post = vi
        .fn()
        .mockRejectedValueOnce(new Error("response lost"))
        .mockImplementation(() => accepted.promise);
      const store = createSyncStore(
        makeGeneratedClient({ SyncService: { triggerSync: post, getSyncStatus: vi.fn().mockResolvedValue(idle) } }),
      );
      store.setSyncStatus(idle);
      if (!runtime) throw new Error("test runtime was not initialized");
      const execution = runtime.runCommand(store.triggerOnboardingSyncEffect(), {
        operation: "first sync",
        safeContext: {},
        onFailure: () => {},
      });
      await vi.waitFor(() => expect(post).toHaveBeenCalledTimes(2));
      store.setSyncStatus({ ...idle, running: true, current_repo: "acme/project", progress: "Syncing pull requests" });
      expect(store.getSyncState()).toMatchObject({
        running: true,
        current_repo: "acme/project",
        progress: "Syncing pull requests",
      });
      accepted.resolve();
      await Effect.runPromise(execution.await);
    });
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

  it.each(["existing", "fresh"])("accepts completion without observing running with %s baseline", async (kind) => {
    const baseline = "2026-08-05T12:00:00Z";
    const completed = "2026-08-05T12:01:00Z";
    const getStatus = vi.fn().mockResolvedValue({ running: false, last_run_at: completed, last_error: "" });
    if (kind === "fresh") getStatus.mockResolvedValueOnce({ running: false });
    const getRates = vi.fn(async () => ({ provider_pools: {}, local_ceilings: {} }));
    const post = vi.fn(async () => undefined);
    const store = createSyncStore(
      makeGeneratedClient({ SyncService: { getSyncStatus: getStatus, getRateLimits: getRates, triggerSync: post } }),
    );
    const onComplete = vi.fn();
    if (kind === "existing") store.setSyncStatus({ running: false, last_run_at: baseline, last_error: "" });
    store.subscribeSyncComplete(onComplete);

    store.triggerSync();
    await vi.waitFor(() => expect(getStatus).toHaveBeenCalledTimes(kind === "fresh" ? 2 : 1));
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
    const post = vi.fn(async (_params?: { priority_repo?: string[] }) => undefined);
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

    expect(post.mock.calls[0][0]).toEqual({
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
