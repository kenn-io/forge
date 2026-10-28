import { cancelIdleSchedule, scheduleWhenIdle, type IdleCallback, type IdleHandle } from "../browser/idle.js";

type ScheduledCallback = IdleCallback;
type ScheduledHandle = IdleHandle;

export interface ProgressiveMountOptions {
  batchSize?: number;
  schedule?: (callback: ScheduledCallback) => ScheduledHandle;
  cancel?: (handle: ScheduledHandle) => void;
}

export interface ProgressiveMountController {
  start: (mounted: number, total: number, onProgress: (mounted: number) => void, onComplete?: () => void) => void;
  cancel: () => void;
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
