import { Duration, Effect, Option, Schedule } from "effect";
import type { AppRuntime } from "../../app/runtime.js";
import { TransientTransportError } from "../../api/effect-errors.js";
import type { RoborevClient } from "../../api/roborev/client.js";
import type { components as RoborevComponents } from "../../api/roborev/generated/schema.js";
import { RoborevDaemonWorkflow } from "./daemon-workflow.js";

const UNAVAILABLE_POLL_INTERVAL_MS = 1_000;
const AVAILABLE_POLL_INTERVAL_MS = 30_000;
const STATUS_TIMEOUT = "5 seconds";

type DaemonStatus = RoborevComponents["schemas"]["DaemonStatus"];

export interface DaemonStoreOptions {
  client: RoborevClient;
  runtime: AppRuntime;
  onRecover?: () => void;
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
    try: (signal) => opts.client.GET("/api/status", { signal }),
    catch: (cause) => TransientTransportError.make({ operation: "GET Roborev daemon status", cause }),
  }).pipe(
    Effect.flatMap((result) =>
      result.data === undefined
        ? Effect.fail(
            TransientTransportError.make({
              operation: "GET Roborev daemon status",
              cause: result.error ?? new Error("Roborev status response was empty"),
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
    if (recovered) {
      yield* Effect.sync(() => {
        wasEverAvailable = true;
        opts.onRecover?.();
      });
    }
    return recovered;
  }).pipe(
    Effect.ensuring(
      Effect.sync(() => {
        loading = false;
      }),
    ),
  );

  const pollOnce = Effect.gen(function* () {
    yield* healthProgram;
    if (available) {
      yield* loadStatusProgram.pipe(Effect.catch(() => Effect.void));
    }
  });

  const pollingSchedule = Schedule.forever.pipe(
    Schedule.addDelay(() =>
      Effect.succeed(Duration.millis(available ? AVAILABLE_POLL_INTERVAL_MS : UNAVAILABLE_POLL_INTERVAL_MS)),
    ),
  );
  const pollingEffect = pollOnce.pipe(
    Effect.repeat(pollingSchedule),
    Effect.ensuring(
      Effect.sync(() => {
        loading = false;
      }),
    ),
  );

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
    pollingEffect,
  };
}

export type DaemonStore = ReturnType<typeof createDaemonStore>;
