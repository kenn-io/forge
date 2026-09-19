import * as roborevAPI from "../../api/roborev/generated/client.js";
import { Effect, Option } from "effect";
import type { AppRuntime } from "../../app/runtime.js";
import { TransientTransportError } from "../../api/effect-errors.js";
import type { RoborevClient } from "../../api/roborev/client.js";
import type * as RoborevModels from "../../api/roborev/generated/models/index.js";
import { RoborevDaemonWorkflow } from "./daemon-workflow.js";

const STATUS_TIMEOUT = "5 seconds";

type DaemonStatus = RoborevModels.DaemonStatus;

export interface DaemonStoreOptions {
  client: RoborevClient;
  runtime: AppRuntime;
}

export function createDaemonStore(opts: DaemonStoreOptions) {
  let available = $state(false);
  let wasEverAvailable = $state(false);
  let version = $state("");
  let endpoint = $state("");
  let loading = $state(false);
  let queuedJobs = $state(0);
  let runningJobs = $state(0);
  let completedJobs = $state(0);
  let failedJobs = $state(0);
  let canceledJobs = $state(0);
  let activeWorkers = $state(0);
  let maxWorkers = $state(0);

  function clearStatus(): void {
    queuedJobs = 0;
    runningJobs = 0;
    completedJobs = 0;
    failedJobs = 0;
    canceledJobs = 0;
    activeWorkers = 0;
    maxWorkers = 0;
  }

  function applyStatus(status: DaemonStatus): void {
    queuedJobs = status.queued_jobs;
    runningJobs = status.running_jobs;
    completedJobs = status.completed_jobs;
    failedJobs = status.failed_jobs;
    canceledJobs = status.canceled_jobs;
    activeWorkers = status.active_workers;
    maxWorkers = status.max_workers;
    if (status.version) version = status.version;
  }

  const loadStatusProgram = Effect.tryPromise({
    try: (signal) => roborevAPI.getStatus({ signal }, opts.client),
    catch: (cause) => TransientTransportError.make({ operation: "GET Roborev daemon status", cause }),
  }).pipe(
    Effect.flatMap((result) =>
      result.status !== 200
        ? Effect.fail(
            TransientTransportError.make({
              operation: "GET Roborev daemon status",
              cause: result.data ?? new Error("Roborev status response was empty"),
            }),
          )
        : Effect.succeed(result.data),
    ),
    Effect.tap((status) => Effect.sync(() => applyStatus(status))),
    Effect.timeout(STATUS_TIMEOUT),
  );

  const healthProgram = Effect.gen(function* () {
    const workflow = yield* RoborevDaemonWorkflow;
    yield* Effect.sync(() => {
      loading = true;
    });
    const result = yield* workflow.health.pipe(Effect.option);

    if (Option.isNone(result)) {
      yield* Effect.sync(() => {
        available = false;
        clearStatus();
      });
      return false;
    }

    const previous = available;
    yield* Effect.sync(() => {
      available = result.value.available;
      version = result.value.version;
      endpoint = result.value.endpoint;
      if (!available) clearStatus();
    });
    const recovered = available && !previous;
    if (recovered) wasEverAvailable = true;
    return recovered;
  }).pipe(
    Effect.ensuring(
      Effect.sync(() => {
        loading = false;
      }),
    ),
  );

  const refreshEffect = Effect.gen(function* () {
    yield* healthProgram;
    if (available) {
      yield* loadStatusProgram.pipe(Effect.catch(() => Effect.void));
    }
  });

  function checkHealth(): void {
    opts.runtime.runCommand(healthProgram, {
      operation: "check Roborev daemon health",
      safeContext: {},
      onFailure: () => {},
    });
  }

  function loadStatus(): void {
    opts.runtime.runCommand(loadStatusProgram, {
      operation: "load Roborev daemon status",
      safeContext: {},
      onFailure: () => {},
    });
  }

  function isAvailable(): boolean {
    return available;
  }
  function getVersion(): string {
    return version;
  }
  function getEndpoint(): string {
    return endpoint;
  }
  function isLoading(): boolean {
    return loading;
  }
  function getQueuedJobs(): number {
    return queuedJobs;
  }
  function getRunningJobs(): number {
    return runningJobs;
  }
  function getCompletedJobs(): number {
    return completedJobs;
  }
  function getFailedJobs(): number {
    return failedJobs;
  }
  function getCanceledJobs(): number {
    return canceledJobs;
  }
  function getActiveWorkers(): number {
    return activeWorkers;
  }
  function getMaxWorkers(): number {
    return maxWorkers;
  }
  function getWasEverAvailable(): boolean {
    return wasEverAvailable;
  }

  return {
    isAvailable,
    getVersion,
    getEndpoint,
    isLoading,
    getQueuedJobs,
    getRunningJobs,
    getCompletedJobs,
    getFailedJobs,
    getCanceledJobs,
    getActiveWorkers,
    getMaxWorkers,
    getWasEverAvailable,
    checkHealth,
    loadStatus,
    refreshEffect,
  };
}

export type DaemonStore = ReturnType<typeof createDaemonStore>;
