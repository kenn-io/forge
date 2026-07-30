import type { RateLimitsResponse, SyncStatus } from "../api/types.js";
import type { ForgeClient } from "../types.js";

export interface SyncStoreOptions {
  client: ForgeClient;
  getPriorityRepos?: (() => string | undefined) | undefined;
}

export function createSyncStore(opts: SyncStoreOptions) {
  const apiClient = opts.client;
  const getPriorityRepos = opts.getPriorityRepos ?? (() => undefined);

  // --- state ---

  let status = $state<SyncStatus | null>(null);
  let rateLimits = $state.raw<RateLimitsResponse>({ provider_pools: {}, local_ceilings: {} });
  let pollingHandle: ReturnType<typeof setInterval> | null = null;
  let wasRunning = false;
  let onSyncCompleteOnce: (() => void) | null = null;
  const syncCompleteListeners = new Set<() => void>();
  let currentIntervalMs = 30_000;
  // Monotonic counter incremented by SSE pushes. Poll results
  // captured before an SSE update are stale and must be dropped.
  let sseGeneration = 0;

  // --- reads ---

  function getSyncState(): SyncStatus | null {
    return status;
  }

  function getRateLimits(): RateLimitsResponse {
    return rateLimits;
  }

  // --- writes ---

  function onNextSyncComplete(fn: () => void): void {
    onSyncCompleteOnce = fn;
  }

  function subscribeSyncComplete(fn: () => void): () => void {
    syncCompleteListeners.add(fn);
    return () => {
      syncCompleteListeners.delete(fn);
    };
  }

  function applySyncStatus(next: SyncStatus | null): void {
    status = next;

    const isRunning = status?.running ?? false;

    if (wasRunning && !isRunning) {
      if (onSyncCompleteOnce) {
        const cb = onSyncCompleteOnce;
        onSyncCompleteOnce = null;
        cb();
      }
      for (const fn of syncCompleteListeners) fn();
    }
    wasRunning = isRunning;

    adjustPollingSpeed(isRunning);
  }

  async function refreshSyncStatus(): Promise<void> {
    const gen = sseGeneration;
    const [syncResult, rateResult] = await Promise.allSettled([
      apiClient.GET("/sync/status"),
      apiClient.GET("/rate-limits"),
    ]);

    // If an SSE push arrived while the poll was in flight, the
    // SSE data is fresher — drop this stale poll result.
    if (gen !== sseGeneration) return;

    if (syncResult.status === "fulfilled") {
      const { data, error } = syncResult.value;
      if (!error && data) {
        applySyncStatus(data);
      }
    }

    if (rateResult.status === "fulfilled") {
      const { data, error } = rateResult.value;
      if (!error && data) {
        rateLimits = data;
      }
    }
  }

  function setSyncStatus(next: SyncStatus): void {
    sseGeneration++;
    applySyncStatus(next);
  }

  type SyncRequest = () => Promise<{
    error?: { detail?: string | undefined; title?: string | undefined } | undefined;
  }>;

  async function runTriggeredSync(request: SyncRequest): Promise<void> {
    const previous = status;

    status = {
      running: true,
      last_run_at: previous?.last_run_at ?? "",
      last_error: "",
    };
    wasRunning = true;
    adjustPollingSpeed(true);

    try {
      const { error } = await request();
      if (error) {
        throw new Error(error.detail ?? error.title ?? "failed to trigger sync");
      }
      await refreshSyncStatus();
    } catch (err) {
      status = {
        running: false,
        last_run_at: previous?.last_run_at ?? "",
        last_error: err instanceof Error ? err.message : "failed to trigger sync",
      };
      wasRunning = false;
      adjustPollingSpeed(false);
      throw err;
    }
  }

  async function triggerSync(): Promise<void> {
    const priorityRepos = parsePriorityRepos(getPriorityRepos());
    const syncOptions = priorityRepos.length > 0 ? { params: { query: { priority_repo: priorityRepos } } } : {};
    await runTriggeredSync(() => apiClient.POST("/sync", syncOptions));
  }

  async function triggerRepoSync(repo: string): Promise<void> {
    await runTriggeredSync(() =>
      apiClient.POST("/sync", {
        params: { query: { only_repo: [repo] } },
      }),
    );
  }

  function parsePriorityRepos(value: string | undefined): string[] {
    return (value ?? "")
      .split(",")
      .map((part) => part.trim())
      .filter((part) => part !== "");
  }

  function adjustPollingSpeed(running: boolean): void {
    const targetMs = running ? 2_000 : 30_000;
    if (targetMs === currentIntervalMs) return;
    currentIntervalMs = targetMs;
    if (pollingHandle !== null) {
      clearInterval(pollingHandle);
      pollingHandle = setInterval(() => {
        void refreshSyncStatus();
      }, currentIntervalMs);
    }
  }

  function startPolling(intervalMs = 30_000): void {
    if (pollingHandle !== null) return;
    currentIntervalMs = intervalMs;
    void refreshSyncStatus();
    pollingHandle = setInterval(() => {
      void refreshSyncStatus();
    }, currentIntervalMs);
  }

  function stopPolling(): void {
    if (pollingHandle === null) return;
    clearInterval(pollingHandle);
    pollingHandle = null;
  }

  return {
    getSyncState,
    getRateLimits,
    onNextSyncComplete,
    subscribeSyncComplete,
    refreshSyncStatus,
    setSyncStatus,
    triggerSync,
    triggerRepoSync,
    startPolling,
    stopPolling,
  };
}

export type SyncStore = ReturnType<typeof createSyncStore>;
