import type { RoborevClient } from "../../api/roborev/client.js";
import type { MiddlemanClient } from "../../types.js";

const UNAVAILABLE_POLL_INTERVAL_MS = 1_000;
const AVAILABLE_POLL_INTERVAL_MS = 30_000;

export interface DaemonStoreOptions {
  client: RoborevClient;
  middlemanClient: MiddlemanClient;
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
  let pollHandle: ReturnType<typeof setTimeout> | null = null;
  let pollGeneration = 0;

  async function checkHealthForGeneration(generation: number): Promise<boolean> {
    const isCurrent = () => generation === pollGeneration;
    if (!isCurrent()) return false;

    let recovered = false;
    loading = true;
    try {
      const { data, error } = await opts.middlemanClient.GET("/roborev/status");
      if (!isCurrent()) return false;

      if (error || !data) {
        available = false;
        queuedJobs = 0;
        runningJobs = 0;
        completedJobs = 0;
        failedJobs = 0;
        canceledJobs = 0;
        activeWorkers = 0;
        maxWorkers = 0;
        return false;
      }
      const prevAvailable = available;
      available = data.available;
      version = data.version;
      endpoint = data.endpoint;
      recovered = available && !prevAvailable;
    } catch {
      if (!isCurrent()) return false;

      available = false;
      queuedJobs = 0;
      runningJobs = 0;
      completedJobs = 0;
      failedJobs = 0;
      canceledJobs = 0;
      activeWorkers = 0;
      maxWorkers = 0;
    } finally {
      if (isCurrent()) loading = false;
    }

    if (recovered) {
      // Fire onRecover on ANY false→true transition,
      // including the first connect after a failed startup.
      // The mount-time loadJobs may have failed if the
      // daemon was unreachable; this ensures data loads
      // once the daemon becomes available.
      wasEverAvailable = true;
      void loadStatus();
      opts.onRecover?.();
    }
    return recovered;
  }

  async function checkHealth(): Promise<void> {
    const generation = pollGeneration;
    await checkHealthForGeneration(generation);
  }

  async function loadStatus(): Promise<void> {
    const { data, error } = await opts.client.GET("/api/status");
    if (error || !data) return;
    queuedJobs = data.queued_jobs;
    runningJobs = data.running_jobs;
    completedJobs = data.completed_jobs;
    failedJobs = data.failed_jobs;
    canceledJobs = data.canceled_jobs;
    activeWorkers = data.active_workers;
    maxWorkers = data.max_workers;
    if (data.version) version = data.version;
  }

  async function poll(generation: number): Promise<void> {
    const recovered = await checkHealthForGeneration(generation);
    if (generation !== pollGeneration) return;

    if (available && !recovered) void loadStatus();

    const interval = available ? AVAILABLE_POLL_INTERVAL_MS : UNAVAILABLE_POLL_INTERVAL_MS;
    pollHandle = setTimeout(() => {
      pollHandle = null;
      void poll(generation);
    }, interval);
  }

  function startPolling(): void {
    stopPolling();
    const generation = pollGeneration;
    void poll(generation);
  }

  function stopPolling(): void {
    pollGeneration += 1;
    loading = false;
    if (pollHandle !== null) {
      clearTimeout(pollHandle);
      pollHandle = null;
    }
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
    startPolling,
    stopPolling,
  };
}

export type DaemonStore = ReturnType<typeof createDaemonStore>;
