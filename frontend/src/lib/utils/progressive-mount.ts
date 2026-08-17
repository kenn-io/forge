type ScheduledCallback = () => void;
type ScheduledHandle = number | ReturnType<typeof setTimeout>;

export interface ProgressiveMountOptions {
  batchSize?: number;
  schedule?: (callback: ScheduledCallback) => ScheduledHandle;
  cancel?: (handle: ScheduledHandle) => void;
}

export interface ProgressiveMountController {
  start: (mounted: number, total: number, onProgress: (mounted: number) => void, onComplete?: () => void) => void;
  cancel: () => void;
}

type IdleSchedulingGlobal = typeof globalThis & {
  requestIdleCallback?: (callback: ScheduledCallback, options?: { timeout: number }) => number;
  cancelIdleCallback?: (handle: number) => void;
};

function scheduleWhenIdle(callback: ScheduledCallback): ScheduledHandle {
  const host = globalThis as IdleSchedulingGlobal;
  if (host.requestIdleCallback) return host.requestIdleCallback(callback, { timeout: 100 });
  return globalThis.setTimeout(callback, 0);
}

function cancelIdleSchedule(handle: ScheduledHandle): void {
  const host = globalThis as IdleSchedulingGlobal;
  if (host.cancelIdleCallback && typeof handle === "number") {
    host.cancelIdleCallback(handle);
    return;
  }
  globalThis.clearTimeout(handle);
}

export function createProgressiveMountController(options: ProgressiveMountOptions = {}): ProgressiveMountController {
  const batchSize = Math.max(1, options.batchSize ?? 25);
  const schedule = options.schedule ?? scheduleWhenIdle;
  const cancelScheduled = options.cancel ?? cancelIdleSchedule;
  let pendingHandle: ScheduledHandle | null = null;

  function cancel(): void {
    if (pendingHandle === null) return;
    cancelScheduled(pendingHandle);
    pendingHandle = null;
  }

  function start(
    mounted: number,
    total: number,
    onProgress: (count: number) => void,
    onComplete: () => void = () => {},
  ): void {
    cancel();
    let current = Math.min(Math.max(0, mounted), total);
    if (current >= total) {
      onComplete();
      return;
    }

    const mountBatch = (): void => {
      pendingHandle = null;
      current = Math.min(current + batchSize, total);
      onProgress(current);
      if (current >= total) {
        onComplete();
        return;
      }
      pendingHandle = schedule(mountBatch);
    };

    pendingHandle = schedule(mountBatch);
  }

  return { start, cancel };
}
