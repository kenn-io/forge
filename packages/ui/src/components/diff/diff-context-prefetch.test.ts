import { describe, expect, it } from "vite-plus/test";
import { createDiffContextPrefetchScheduler } from "./diff-context-prefetch.js";

function controlledTask(
  starts: string[],
  id: string,
): {
  resolve: () => void;
  run: (signal: AbortSignal) => Promise<void>;
  signal: () => AbortSignal | undefined;
} {
  let resolve = (): void => {};
  let capturedSignal: AbortSignal | undefined;
  return {
    resolve: () => resolve(),
    run: (signal) => {
      starts.push(id);
      capturedSignal = signal;
      return new Promise<void>((done) => {
        resolve = done;
      });
    },
    signal: () => capturedSignal,
  };
}

function deferredCallbacks(): {
  runNext: () => void;
  scheduleDeferred: (callback: () => void) => () => void;
} {
  const callbacks: Array<{ callback: () => void; cancelled: boolean }> = [];
  return {
    runNext: () => {
      const next = callbacks.shift();
      if (next && !next.cancelled) next.callback();
    },
    scheduleDeferred: (callback) => {
      const entry = { callback, cancelled: false };
      callbacks.push(entry);
      return () => {
        entry.cancelled = true;
      };
    },
  };
}

describe("diff context prefetch scheduler", () => {
  it("runs at most four file tasks and reuses a released slot", async () => {
    const starts: string[] = [];
    const tasks = Array.from({ length: 5 }, (_, index) => controlledTask(starts, String(index + 1)));
    const scheduler = createDiffContextPrefetchScheduler({ concurrency: 4 });

    tasks.forEach((task, index) => scheduler.schedule(String(index), "foreground", task.run));

    expect(starts).toEqual(["1", "2", "3", "4"]);
    tasks[0]!.resolve();
    await Promise.resolve();
    await Promise.resolve();
    expect(starts).toEqual(["1", "2", "3", "4", "5"]);
  });

  it("waits for a deferred turn before starting background work", () => {
    const starts: string[] = [];
    const deferred = deferredCallbacks();
    const task = controlledTask(starts, "background");
    const scheduler = createDiffContextPrefetchScheduler({
      concurrency: 4,
      scheduleDeferred: deferred.scheduleDeferred,
    });

    scheduler.schedule("background", "background", task.run);
    expect(starts).toEqual([]);

    deferred.runNext();
    expect(starts).toEqual(["background"]);
  });

  it("promotes queued background work ahead of the background queue", async () => {
    const starts: string[] = [];
    const deferred = deferredCallbacks();
    const blocker = controlledTask(starts, "blocker");
    const firstBackground = controlledTask(starts, "first-background");
    const promoted = controlledTask(starts, "promoted");
    const scheduler = createDiffContextPrefetchScheduler({
      concurrency: 1,
      scheduleDeferred: deferred.scheduleDeferred,
    });

    scheduler.schedule("blocker", "foreground", blocker.run);
    scheduler.schedule("first-background", "background", firstBackground.run);
    const promotedHandle = scheduler.schedule("promoted", "background", promoted.run);
    promotedHandle.setPriority("foreground");

    blocker.resolve();
    await Promise.resolve();
    await Promise.resolve();
    expect(starts).toEqual(["blocker", "promoted"]);

    promoted.resolve();
    await Promise.resolve();
    await Promise.resolve();
    expect(starts).toEqual(["blocker", "promoted"]);
    deferred.runNext();
    expect(starts).toEqual(["blocker", "promoted", "first-background"]);
  });

  it("cancels queued work and aborts active work on reset", () => {
    const starts: string[] = [];
    const active = controlledTask(starts, "active");
    const queued = controlledTask(starts, "queued");
    const scheduler = createDiffContextPrefetchScheduler({ concurrency: 1 });

    scheduler.schedule("active", "foreground", active.run);
    scheduler.schedule("queued", "foreground", queued.run);
    scheduler.reset();

    expect(active.signal()?.aborted).toBe(true);
    expect(starts).toEqual(["active"]);
  });

  it("ignores stale completion from a prior generation", async () => {
    const starts: string[] = [];
    const oldTask = controlledTask(starts, "old");
    const currentTask = controlledTask(starts, "current");
    const waitingTask = controlledTask(starts, "waiting");
    const scheduler = createDiffContextPrefetchScheduler({ concurrency: 1 });

    scheduler.schedule("old", "foreground", oldTask.run);
    scheduler.reset();
    scheduler.schedule("current", "foreground", currentTask.run);
    scheduler.schedule("waiting", "foreground", waitingTask.run);

    oldTask.resolve();
    await Promise.resolve();
    await Promise.resolve();
    expect(starts).toEqual(["old", "current"]);

    currentTask.resolve();
    await Promise.resolve();
    await Promise.resolve();
    expect(starts).toEqual(["old", "current", "waiting"]);
  });

  it("cancels an individual queued task", () => {
    const starts: string[] = [];
    const deferred = deferredCallbacks();
    const queued = controlledTask(starts, "queued");
    const scheduler = createDiffContextPrefetchScheduler({
      concurrency: 1,
      scheduleDeferred: deferred.scheduleDeferred,
    });
    const handle = scheduler.schedule("queued", "background", queued.run);

    handle.cancel();
    deferred.runNext();

    expect(starts).toEqual([]);
  });

  it("aborts an individually cancelled active task", () => {
    const starts: string[] = [];
    const active = controlledTask(starts, "active");
    const scheduler = createDiffContextPrefetchScheduler({ concurrency: 1 });
    const handle = scheduler.schedule("active", "foreground", active.run);

    handle.cancel();

    expect(active.signal()?.aborted).toBe(true);
  });

  it("does not accept new work after disposal", () => {
    const starts: string[] = [];
    const scheduler = createDiffContextPrefetchScheduler({ concurrency: 1 });
    scheduler.dispose();

    scheduler.schedule("late", "foreground", controlledTask(starts, "late").run);

    expect(starts).toEqual([]);
  });
});
