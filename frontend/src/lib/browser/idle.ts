import { Context, Effect, Layer } from "effect";

export type IdleCallback = () => void;
export type IdleHandle = number | ReturnType<typeof setTimeout>;

type IdleSchedulingGlobal = typeof globalThis & {
  requestIdleCallback?: (callback: IdleCallback, options?: { timeout: number }) => number;
  cancelIdleCallback?: (handle: number) => void;
};

// Browsers without requestIdleCallback (Safari) fall back to the next task.
export function scheduleWhenIdle(callback: IdleCallback): IdleHandle {
  const host = globalThis as IdleSchedulingGlobal;
  if (host.requestIdleCallback) return host.requestIdleCallback(callback, { timeout: 100 });
  return globalThis.setTimeout(callback, 0);
}

export function cancelIdleSchedule(handle: IdleHandle): void {
  const host = globalThis as IdleSchedulingGlobal;
  if (host.cancelIdleCallback && typeof handle === "number") {
    host.cancelIdleCallback(handle);
    return;
  }
  globalThis.clearTimeout(handle);
}

interface IdleCallbackFactories {
  readonly request: (callback: IdleCallback) => IdleHandle;
  readonly cancel: (handle: IdleHandle) => void;
}

export class IdleCallbacks extends Context.Service<IdleCallbacks, IdleCallbackFactories>()(
  "kenn-forge/browser/IdleCallbacks",
) {}

export const IdleCallbacksLive = Layer.succeed(IdleCallbacks)({
  request: scheduleWhenIdle,
  cancel: cancelIdleSchedule,
});

export const nextIdlePeriod = Effect.gen(function* () {
  const idle = yield* IdleCallbacks;
  return yield* Effect.callback<void>((resume) => {
    const handle = idle.request(() => resume(Effect.void));
    return Effect.sync(() => idle.cancel(handle));
  });
});
