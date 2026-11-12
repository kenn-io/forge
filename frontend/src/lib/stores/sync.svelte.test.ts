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
  let accepted = 0;
  const store = createSyncStore(
    makeGeneratedClient({
      SyncService: {
        getSyncStatus: async () => {
          const status = await getStatus();
          return { accepted_full_syncs: accepted, ...status };
        },
        getRateLimits: async () => ({ provider_pools: {}, local_ceilings: {} }),
        triggerSync: async (args) => {
          const result = await post(args);
          accepted++;
          return result;
        },
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
  // The server counts every full sync it retains, so a lost response can be
  // reconciled against the count read before the trigger was sent.
  describe("reconciling a lost first-sync response", () => {
    const status = (accepted: number, rest: Record<string, unknown> = {}) => ({
      running: false,
      last_run_at: "",
      last_error: "",
      accepted_full_syncs: accepted,
      ...rest,
    });

    it.each([false, true])("does not repeat an accepted first sync (finished: %s)", async (finished) => {
      const done = "2026-08-05T12:01:00Z";
      let accepted = 0;
      const getStatus = vi.fn(async () =>
        status(
          accepted,
          accepted && finished ? { last_run_at: done, completed_full_syncs: 1 } : { running: accepted > 0 },
        ),
      );
      const post = vi.fn(async () => {
        accepted = 1;
        throw new Error("response lost");
      });
      const onFailure = vi.fn();
      const { store, execution } = startOnboarding(getStatus, post, onFailure);
      await Effect.runPromise(execution.await);
      expect(post).toHaveBeenCalledOnce();
      expect(onFailure).not.toHaveBeenCalled();
      expect(store.getSyncState()?.running).toBe(!finished);
      if (finished) expect(store.getSyncState()?.last_run_at).toBe(done);
    });

    it.each(["lost response", "stopping"])("retries %s when no full sync was accepted", async (failure) => {
      let accepted = 2;
      const getStatus = vi.fn(async () => status(accepted, { running: true }));
      const problem = {
        code: "serviceUnavailable",
        detail: "syncer is stopping",
        status: 503,
        title: "Service Unavailable",
        type: "about:blank",
      };
      const error =
        failure === "stopping"
          ? new GeneratedProblemResponse(problem, Response.json(problem, { status: 503 }))
          : new Error("connection lost");
      const post = vi
        .fn()
        .mockRejectedValueOnce(error)
        .mockImplementation(async () => {
          accepted++;
        });
      const { execution } = startOnboarding(getStatus, post);
      await Effect.runPromise(execution.await);
      expect(post).toHaveBeenCalledTimes(2);
    });

    it.each(["forbidden", "lost response"])(
      "does not repeat the trigger when the status read has a %s",
      async (failure) => {
        vi.useFakeTimers();
        let posted = false;
        let lostReads = 0;
        const denied = {
          code: "forbidden",
          detail: "status denied",
          status: 403,
          title: "Forbidden",
          type: "about:blank",
        };
        const getStatus = vi.fn(async () => {
          if (posted && failure === "forbidden")
            throw new GeneratedProblemResponse(denied, Response.json(denied, { status: 403 }));
          if (posted && lostReads++ < 4) throw new Error("status response lost");
          return status(posted ? 1 : 0, { running: posted });
        });
        const post = vi.fn(async () => {
          posted = true;
          throw new Error("trigger response lost");
        });
        const { execution } = startOnboarding(getStatus, post);
        await vi.advanceTimersByTimeAsync(120000);
        await Effect.runPromise(execution.await);
        expect(post).toHaveBeenCalledOnce();
        if (failure === "lost response") expect(lostReads).toBeGreaterThan(4);
      },
    );

    it("keeps waiting when a different run completes before the accepted full sync", async () => {
      let accepted = 0;
      const getStatus = vi.fn(async () => status(accepted, accepted > 0 ? { running: true } : {}));
      const post = vi.fn(async () => {
        accepted = 1;
      });
      const { store, execution } = startOnboarding(getStatus, post);
      await Effect.runPromise(execution.await);
      expect(store.getSyncState()?.running).toBe(true);

      store.setSyncStatus(status(1, { last_run_at: "2026-08-05T12:00:30Z" }));
      expect(store.getSyncState()?.running).toBe(true);

      const done = "2026-08-05T12:01:00Z";
      store.setSyncStatus(status(1, { last_run_at: done, completed_full_syncs: 1 }));
      expect(store.getSyncState()).toMatchObject({ running: false, last_run_at: done });
    });

    it("restarts the first sync after the daemon loses its accepted work", async () => {
      let accepted = 2;
      const getStatus = vi.fn(async () => status(accepted, { running: accepted > 0 }));
      const post = vi.fn(async () => {
        accepted++;
      });
      const { store, execution } = startOnboarding(getStatus, post);
      await Effect.runPromise(execution.await);
      accepted = 0;
      store.setSyncStatus(status(0));
      await vi.waitFor(() => expect(post).toHaveBeenCalledTimes(2));
    });
  });

  it.each([403, 503])("handles a %s baseline failure before triggering", async (status) => {
    const problem = {
      code: status === 403 ? "forbidden" : "serviceUnavailable",
      detail: "status unavailable",
      status,
      title: "Unavailable",
      type: "about:blank",
    };
    const error = new GeneratedProblemResponse(problem, Response.json(problem, { status }));
    const getStatus = vi.fn();
    if (status === 403) getStatus.mockRejectedValue(error);
    else
      getStatus
        .mockRejectedValueOnce(error)
        .mockRejectedValueOnce(error)
        .mockResolvedValue({ running: true, last_run_at: "", last_error: "" });
    const post = vi.fn();
    if (status === 403) post.mockResolvedValue(undefined);
    else post.mockRejectedValueOnce(new Error("response lost")).mockResolvedValue(undefined);
    const onFailure = vi.fn();
    const { execution } = startOnboarding(getStatus, post, onFailure);
    await Effect.runPromise(execution.await);
    expect(post).toHaveBeenCalledTimes(status === 403 ? 1 : 2);
    expect(onFailure).not.toHaveBeenCalled();
  });

  it.each([403, 503])("does not retry a permanent %s first sync failure", async (status) => {
    const detail = status === 403 ? "Access denied" : "syncer not configured";
    const problem = {
      code: status === 403 ? "forbidden" : "serviceUnavailable",
      detail,
      ...(status === 503 && { details: { retryable: false } }),
      status,
      title: status === 403 ? "Forbidden" : "Service Unavailable",
      type: "about:blank",
    };
    const post = vi.fn().mockRejectedValue(new GeneratedProblemResponse(problem, Response.json(problem, { status })));
    const { store, execution } = startOnboarding(
      async () => ({ running: false, last_run_at: "", last_error: "" }),
      post,
    );
    await Effect.runPromise(execution.await);
    expect(post).toHaveBeenCalledOnce();
    expect(store.getSyncState()).toMatchObject({ running: false, last_error: detail });
  });

  it("stops after exhausting first-sync retries", async () => {
    vi.useFakeTimers();
    const getStatus = vi.fn().mockResolvedValue({ running: false, last_run_at: "", last_error: "" });
    const post = vi.fn().mockRejectedValue(new Error("connection lost"));
    const { store, execution } = startOnboarding(getStatus, post);
    await vi.waitFor(() => expect(post).toHaveBeenCalledOnce());
    await vi.advanceTimersByTimeAsync(120000);
    await Effect.runPromise(execution.await);
    expect(post).toHaveBeenCalledTimes(6);
    expect(store.getSyncState()).toMatchObject({ running: false, last_error: "Could not reach Kenn Forge" });
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

  it.each([false, true])("discards an idle refresh started while the trigger is pending: %s", async (during) => {
    const baseline = "2026-08-05T12:00:00Z";
    const stale = Promise.withResolvers<{ running: boolean; last_run_at: string; last_error: string }>();
    const accepted = Promise.withResolvers<void>();
    const getStatus = vi
      .fn()
      .mockReturnValueOnce(stale.promise)
      .mockResolvedValue({ running: true, last_run_at: baseline, last_error: "" });
    const post = vi.fn(() => accepted.promise);
    const store = createSyncStore(
      makeGeneratedClient({ SyncService: { getSyncStatus: getStatus, triggerSync: post } }),
    );
    store.setSyncStatus({ running: false, last_run_at: baseline, last_error: "" });
    const completed = vi.fn();
    store.subscribeSyncComplete(completed);
    if (runtime === undefined) throw new Error("test runtime was not initialized");
    if (during) {
      store.triggerSync();
      await vi.waitFor(() => expect(post).toHaveBeenCalledOnce());
    }
    const refresh = runtime.runCommand(store.refreshSyncStatusEffect, {
      operation: "test stale sync refresh",
      safeContext: {},
      onFailure: () => {},
    });
    await vi.waitFor(() => expect(getStatus).toHaveBeenCalledOnce());
    if (during) {
      accepted.resolve();
      await new Promise((resolve) => setTimeout(resolve, 0));
    } else {
      store.triggerSync();
      await vi.waitFor(() => expect(post).toHaveBeenCalledOnce());
    }
    stale.resolve({ running: false, last_run_at: "2026-08-05T12:01:00Z", last_error: "" });
    await Effect.runPromise(refresh.await);
    expect(completed).not.toHaveBeenCalled();
    expect(store.getSyncState()?.running).toBe(true);
    if (!during) {
      accepted.resolve();
    }
    await vi.waitFor(() => expect(getStatus).toHaveBeenCalledTimes(2));
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
