export type DiffContextPrefetchPriority = "foreground" | "background";

export interface DiffContextPrefetchTaskHandle {
  cancel(): void;
  setPriority(priority: DiffContextPrefetchPriority): void;
}

export interface DiffContextPrefetchScheduler {
  dispose(): void;
  reset(): void;
  schedule(
    id: string,
    priority: DiffContextPrefetchPriority,
    run: (signal: AbortSignal) => Promise<void>,
  ): DiffContextPrefetchTaskHandle;
}

interface SchedulerOptions {
  concurrency?: number;
  scheduleDeferred?: (callback: () => void) => () => void;
}

interface PrefetchTask {
  controller: AbortController;
  generation: number;
  id: string;
  priority: DiffContextPrefetchPriority;
  run: (signal: AbortSignal) => Promise<void>;
  state: "queued" | "active" | "cancelled" | "settled";
}

export function createDiffContextPrefetchScheduler(options: SchedulerOptions = {}): DiffContextPrefetchScheduler {
  const concurrency = options.concurrency ?? 4;
  if (!Number.isInteger(concurrency) || concurrency < 1) {
    throw new RangeError("Diff context prefetch concurrency must be a positive integer");
  }

  const scheduleDeferred = options.scheduleDeferred ?? scheduleBrowserBackgroundTurn;
  let generation = 0;
  let disposed = false;
  let cancelDeferred: (() => void) | undefined;
  const foregroundQueue: PrefetchTask[] = [];
  const backgroundQueue: PrefetchTask[] = [];
  const active = new Set<PrefetchTask>();

  function removeQueuedTask(task: PrefetchTask): void {
    const queue = task.priority === "foreground" ? foregroundQueue : backgroundQueue;
    const index = queue.indexOf(task);
    if (index >= 0) queue.splice(index, 1);
  }

  function start(task: PrefetchTask): void {
    if (task.state !== "queued" || task.generation !== generation || disposed) return;
    task.state = "active";
    active.add(task);
    let result: Promise<void>;
    try {
      result = task.run(task.controller.signal);
    } catch (error) {
      result = Promise.reject(error);
    }
    void result.catch(() => undefined).finally(() => settle(task));
  }

  function settle(task: PrefetchTask): void {
    task.state = "settled";
    if (disposed || task.generation !== generation || !active.delete(task)) return;
    drainForeground();
    scheduleBackground();
  }

  function drainForeground(): void {
    while (active.size < concurrency && foregroundQueue.length > 0) {
      const task = foregroundQueue.shift()!;
      start(task);
    }
  }

  function drainBackgroundTurn(): void {
    cancelDeferred = undefined;
    drainForeground();
    while (active.size < concurrency && backgroundQueue.length > 0) {
      const task = backgroundQueue.shift()!;
      start(task);
    }
  }

  function scheduleBackground(): void {
    if (disposed || cancelDeferred || active.size >= concurrency || backgroundQueue.length === 0) return;
    cancelDeferred = scheduleDeferred(drainBackgroundTurn);
  }

  function cancelTask(task: PrefetchTask): void {
    if (task.state === "cancelled" || task.state === "settled") return;
    if (task.state === "queued") removeQueuedTask(task);
    task.state = "cancelled";
    task.controller.abort();
    // Active work retains its slot until the task settles because file previews
    // use a shared cache request that cancellation deliberately does not abort.
  }

  function resetTasks(): void {
    generation += 1;
    cancelDeferred?.();
    cancelDeferred = undefined;
    while (foregroundQueue.length > 0) cancelTask(foregroundQueue[0]!);
    while (backgroundQueue.length > 0) cancelTask(backgroundQueue[0]!);
    for (const task of active) cancelTask(task);
    foregroundQueue.length = 0;
    backgroundQueue.length = 0;
    active.clear();
  }

  return {
    dispose(): void {
      if (disposed) return;
      resetTasks();
      disposed = true;
    },
    reset(): void {
      if (!disposed) resetTasks();
    },
    schedule(id, priority, run): DiffContextPrefetchTaskHandle {
      if (disposed) {
        return { cancel: () => {}, setPriority: () => {} };
      }
      const task: PrefetchTask = {
        controller: new AbortController(),
        generation,
        id,
        priority,
        run,
        state: "queued",
      };
      (priority === "foreground" ? foregroundQueue : backgroundQueue).push(task);
      if (priority === "foreground") drainForeground();
      else scheduleBackground();

      return {
        cancel: () => cancelTask(task),
        setPriority(nextPriority): void {
          if (task.state !== "queued" || task.priority === nextPriority) return;
          removeQueuedTask(task);
          task.priority = nextPriority;
          (nextPriority === "foreground" ? foregroundQueue : backgroundQueue).push(task);
          if (nextPriority === "foreground") drainForeground();
          else scheduleBackground();
        },
      };
    },
  };
}

function scheduleBrowserBackgroundTurn(callback: () => void): () => void {
  if (typeof globalThis.requestIdleCallback === "function") {
    const id = globalThis.requestIdleCallback(callback, { timeout: 500 });
    return () => globalThis.cancelIdleCallback(id);
  }
  const id = globalThis.setTimeout(callback, 50);
  return () => globalThis.clearTimeout(id);
}
