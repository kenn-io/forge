import { Deferred, Duration, Effect, Option } from "effect";
import { pollWhileVisible } from "../effect/poll-while-visible.js";
import type { AppRuntime } from "../app/runtime.js";
import { executeGeneratedApiRequest, type GeneratedApi } from "../api/generated-api.js";
import { ApiProblemError } from "../api/effect-errors.js";
import { isTransientPickerReadFailure, retryIdempotentRead, onboardingRetrySchedule } from "../api/retry-policy.js";
import type { RateLimitsResponse, SyncStatus } from "../api/types.js";
import { SyncWorkflow, type SyncReadError } from "./sync-workflow.js";

export interface SyncStoreOptions {
  runtime: AppRuntime;
  getPriorityRepos?: (() => string | undefined) | undefined;
}

export function readSyncErrorMessage(error: SyncReadError, fallback: string): string {
  if (error._tag === "ApiProblemError") {
    return error.problem.detail ?? error.problem.title ?? fallback;
  }
  return "Could not reach Kenn Forge";
}

export function createSyncStore(opts: SyncStoreOptions) {
  const runtime = opts.runtime;
  const getPriorityRepos = opts.getPriorityRepos ?? (() => undefined);

  let status = $state<SyncStatus | null>(null);
  let providerAvailable = $state(true);
  let rateLimits = $state.raw<RateLimitsResponse>({ provider_pools: {}, local_ceilings: {} });
  let wasRunning = false;
  let onSyncCompleteOnce: (() => void) | null = null;
  const syncCompleteListeners = new Set<() => void>();
  let currentIntervalMs = 30_000;
  let liveUpdatesConnected = false;
  let refreshGeneration = 0;
  // The trigger endpoint can acknowledge before status observes the new run.
  // Retain the optimistic state until status reports running or advances past
  // the authoritative pre-trigger completion time.
  let triggeredSyncLastRunAt: string | null | undefined;
  // A first sync is complete only when the server reports it finished the full
  // sync this client accepted, not whichever run happens to end first. null
  // while the accepted count is unknown, undefined when no count is awaited.
  let triggeredFullSyncTarget: number | null | undefined;
  let pollWakeSignal: Deferred.Deferred<void> | null = null;

  function getSyncState(): SyncStatus | null {
    return status;
  }

  function getRateLimits(): RateLimitsResponse {
    return rateLimits;
  }

  function getProviderAvailable(): boolean {
    return providerAvailable;
  }

  function setProviderAvailable(available: boolean): void {
    providerAvailable = available;
  }

  function setLiveUpdatesConnected(connected: boolean): void {
    liveUpdatesConnected = connected;
    adjustPollingSpeed(status?.running ?? false);
  }

  function onNextSyncComplete(fn: () => void): void {
    onSyncCompleteOnce = fn;
  }

  function subscribeSyncComplete(fn: () => void): () => void {
    syncCompleteListeners.add(fn);
    return () => {
      syncCompleteListeners.delete(fn);
    };
  }

  // For list reloads that the server's end-of-sync data_changed already
  // covers: while live updates are connected, that event reloads the visible
  // list, so reloading here as well would read every list twice per sync.
  function subscribeSyncCompleteWithoutLiveUpdates(fn: () => void): () => void {
    return subscribeSyncComplete(() => {
      if (!liveUpdatesConnected) fn();
    });
  }

  function applySyncStatus(next: SyncStatus | null): void {
    const isRunning = next?.running ?? false;
    if (triggeredFullSyncTarget !== undefined) {
      if (next && triggeredFullSyncTarget !== null && (next.accepted_full_syncs ?? 0) < triggeredFullSyncTarget) {
        triggeredFullSyncTarget = null;
        runtime.runCommand(triggerOnboardingSyncEffect(), {
          operation: "restart first sync",
          safeContext: {},
          onFailure: () => {
            for (const listener of syncCompleteListeners) listener();
          },
        });
        return;
      }
      if (triggeredFullSyncTarget !== null && (next?.completed_full_syncs ?? 0) >= triggeredFullSyncTarget) {
        triggeredFullSyncTarget = undefined;
        triggeredSyncLastRunAt = undefined;
      } else if (!isRunning) {
        return;
      }
    } else if (triggeredSyncLastRunAt !== undefined) {
      const nextLastRunAt = next?.last_run_at ?? "";
      if (isRunning || (triggeredSyncLastRunAt !== null && lastRunAdvanced(triggeredSyncLastRunAt, nextLastRunAt))) {
        triggeredSyncLastRunAt = undefined;
      } else {
        return;
      }
    }

    status = next;
    if (wasRunning && !isRunning) {
      if (onSyncCompleteOnce) {
        const callback = onSyncCompleteOnce;
        onSyncCompleteOnce = null;
        callback();
      }
      for (const listener of syncCompleteListeners) listener();
    }
    wasRunning = isRunning;
    adjustPollingSpeed(isRunning);
  }

  function syncStatusRead() {
    return executeGeneratedApiRequest("GET /sync/status", (client) => client.SyncService.getSyncStatus()).pipe(
      retryIdempotentRead,
    );
  }

  function rateLimitsRead() {
    return executeGeneratedApiRequest("GET /rate-limits", (client) => client.SyncService.getRateLimits()).pipe(
      retryIdempotentRead,
    );
  }

  function refreshSyncStatusProgram() {
    const generation = refreshGeneration;
    return Effect.gen(function* () {
      const workflow = yield* SyncWorkflow;
      const snapshot = yield* workflow.refresh(generation, syncStatusRead(), rateLimitsRead());
      if (generation !== refreshGeneration) return;
      yield* Effect.sync(() => {
        if (Option.isSome(snapshot.status)) {
          applySyncStatus(snapshot.status.value);
        }
        if (Option.isSome(snapshot.rateLimits)) {
          rateLimits = snapshot.rateLimits.value;
        }
      });
    });
  }

  function refreshSyncStatus(): void {
    runtime.runCommand(refreshSyncStatusProgram(), {
      operation: "refresh sync status",
      safeContext: {},
      onFailure: () => {},
    });
  }

  const refreshSyncStatusEffect = Effect.suspend(refreshSyncStatusProgram);

  // Rate limits never arrive through sync status events, so this read is not
  // tied to refreshGeneration: a status event that lands while it is in flight
  // must not discard it. Use it after a change that moves a quota or ceiling.
  function refreshRateLimits(): void {
    runtime.runCommand(
      rateLimitsRead().pipe(
        Effect.map((next) => {
          rateLimits = next;
        }),
      ),
      {
        operation: "refresh rate limits",
        safeContext: {},
        onFailure: () => {},
      },
    );
  }

  function reconcileSyncStatusProgram() {
    const generation = refreshGeneration;
    return Effect.gen(function* () {
      const [nextStatus, nextRateLimits] = yield* Effect.all([syncStatusRead(), Effect.option(rateLimitsRead())], {
        concurrency: "unbounded",
      });
      if (generation !== refreshGeneration) return;
      yield* Effect.sync(() => {
        applySyncStatus(nextStatus);
        if (Option.isSome(nextRateLimits)) rateLimits = nextRateLimits.value;
      });
    });
  }

  const reconcileSyncStatusEffect = Effect.suspend(reconcileSyncStatusProgram);

  function setSyncStatus(next: SyncStatus): void {
    refreshGeneration += 1;
    applySyncStatus(next);
  }

  function lastRunAdvanced(previous: string, next: string): boolean {
    if (next === "") return false;
    if (previous === "") return true;
    return Date.parse(next) > Date.parse(previous);
  }

  function triggeredSyncProgram(request: Effect.Effect<unknown, SyncReadError, GeneratedApi>, awaitFullSync = false) {
    const previous = status;
    refreshGeneration += 1;
    let baselineLastRunAt = previous?.last_run_at ?? null;

    return Effect.gen(function* () {
      if (previous === null) {
        const baseline = yield* Effect.option(syncStatusRead());
        baselineLastRunAt = Option.isSome(baseline) ? (baseline.value.last_run_at ?? null) : null;
      }
      yield* Effect.sync(() => {
        triggeredSyncLastRunAt = baselineLastRunAt;
        triggeredFullSyncTarget = awaitFullSync ? null : undefined;
        status = {
          ...(previous?.relay && { relay: previous.relay }),
          ...(previous?.repo_errors && { repo_errors: previous.repo_errors }),
          running: true,
          last_run_at: baselineLastRunAt ?? "",
          last_error: "",
        };
        wasRunning = true;
        adjustPollingSpeed(true);
      });
      yield* request;
      yield* Effect.sync(() => {
        refreshGeneration += 1;
      });
      yield* Effect.suspend(refreshSyncStatusProgram);
    }).pipe(
      Effect.tapError((failure) =>
        Effect.sync(() => {
          triggeredSyncLastRunAt = undefined;
          triggeredFullSyncTarget = undefined;
          status = {
            ...(previous?.relay && { relay: previous.relay }),
            ...(previous?.repo_errors && { repo_errors: previous.repo_errors }),
            running: false,
            last_run_at: previous?.last_run_at ?? baselineLastRunAt ?? "",
            last_error: readSyncErrorMessage(failure, "failed to trigger sync"),
          };
          wasRunning = false;
          adjustPollingSpeed(false);
        }),
      ),
      Effect.asVoid,
    );
  }

  function runTriggeredSync(request: Effect.Effect<unknown, SyncReadError, GeneratedApi>): void {
    runtime.runCommand(triggeredSyncProgram(request), {
      operation: "trigger provider sync",
      safeContext: {},
      onFailure: () => {},
    });
  }

  function fullSyncRequest() {
    const priorityRepos = parsePriorityRepos(getPriorityRepos());
    return executeGeneratedApiRequest("POST /sync", (client) =>
      client.SyncService.triggerSync(priorityRepos.length > 0 ? { priority_repo: priorityRepos } : undefined),
    );
  }

  function triggerSyncEffect() {
    return triggeredSyncProgram(fullSyncRequest());
  }

  // A lost response may hide an accepted trigger. Retry the POST only after
  // a successful status read shows that no full sync was accepted.
  function triggerOnboardingSyncEffect() {
    const isRetryable = (failure: unknown) =>
      !(failure instanceof ApiProblemError && failure.problem.details?.retryable === false) &&
      isTransientPickerReadFailure(failure);
    const readWithBackoff = () =>
      syncStatusRead().pipe(Effect.retry({ schedule: onboardingRetrySchedule, while: isRetryable }));
    const request = Effect.gen(function* () {
      const baseline = yield* Effect.option(readWithBackoff());
      const before = Option.isSome(baseline) ? (baseline.value.accepted_full_syncs ?? 0) : undefined;
      yield* fullSyncRequest().pipe(
        Effect.catchIf(isRetryable, (failure) =>
          readWithBackoff().pipe(
            Effect.mapError(() => ({ _tag: "UnreadableStatus" as const, failure })),
            Effect.flatMap((after) =>
              before === undefined || (after.accepted_full_syncs ?? 0) > before ? Effect.void : Effect.fail(failure),
            ),
          ),
        ),
        Effect.retry({ schedule: onboardingRetrySchedule, while: isRetryable }),
        Effect.mapError((failure) =>
          "_tag" in failure && failure._tag === "UnreadableStatus" ? failure.failure : failure,
        ),
      );
      if (before !== undefined) {
        triggeredFullSyncTarget = before + 1;
      } else {
        const read = yield* Effect.option(readWithBackoff());
        triggeredFullSyncTarget = Option.isSome(read) ? read.value.accepted_full_syncs || undefined : undefined;
        if (Option.isSome(read)) applySyncStatus(read.value);
      }
    });
    return triggeredSyncProgram(request, true);
  }

  function triggerSync(): void {
    runtime.runCommand(triggerSyncEffect(), {
      operation: "trigger provider sync",
      safeContext: {},
      onFailure: () => {},
    });
  }

  function triggerRepoSync(repo: string): void {
    runTriggeredSync(
      executeGeneratedApiRequest("POST /sync", (client) => client.SyncService.triggerSync({ only_repo: [repo] })),
    );
  }

  function parsePriorityRepos(value: string | undefined): string[] {
    return (value ?? "")
      .split(",")
      .map((part) => part.trim())
      .filter((part) => part !== "");
  }

  function adjustPollingSpeed(running: boolean): void {
    const targetMs = running && !liveUpdatesConnected ? 2_000 : 30_000;
    if (targetMs === currentIntervalMs) return;
    currentIntervalMs = targetMs;
    const signal = pollWakeSignal;
    if (signal !== null) {
      runtime.runCommand(Deferred.succeed(signal, undefined), {
        operation: "wake sync polling for cadence change",
        safeContext: { intervalMs: targetMs },
        onFailure: () => {},
      });
    }
  }

  const waitForPollingCadence = Effect.gen(function* () {
    const signal = yield* Deferred.make<void>();
    const intervalMs = currentIntervalMs;
    yield* Effect.sync(() => {
      pollWakeSignal = signal;
    });
    yield* Effect.raceFirst(Effect.sleep(Duration.millis(intervalMs)), Deferred.await(signal)).pipe(
      Effect.ensuring(
        Effect.sync(() => {
          if (pollWakeSignal === signal) pollWakeSignal = null;
        }),
      ),
    );
  });

  const pollingEffect = pollWhileVisible(Effect.suspend(refreshSyncStatusProgram), waitForPollingCadence, {
    immediate: true,
  }).pipe(
    Effect.ensuring(
      Effect.sync(() => {
        pollWakeSignal = null;
      }),
    ),
  );

  return {
    getSyncState,
    getRateLimits,
    getProviderAvailable,
    setProviderAvailable,
    setLiveUpdatesConnected,
    onNextSyncComplete,
    subscribeSyncComplete,
    subscribeSyncCompleteWithoutLiveUpdates,
    refreshSyncStatus,
    refreshRateLimits,
    refreshSyncStatusEffect,
    reconcileSyncStatusEffect,
    setSyncStatus,
    triggerSyncEffect,
    triggerOnboardingSyncEffect,
    triggerSync,
    triggerRepoSync,
    pollingEffect,
  };
}

export type SyncStore = ReturnType<typeof createSyncStore>;
