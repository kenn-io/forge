import { assert, it } from "@effect/vitest";
import { Effect, Fiber, Queue } from "effect";
import { AnimationFrames, makeAnimationFrameScheduler } from "./animation-frame.js";

it.effect("coalesces animation-frame demand and stops publishing after scope interruption", () =>
  Effect.scoped(
    Effect.gen(function* () {
      let callback: FrameRequestCallback | undefined;
      let cancelled = 0;
      let published = 0;
      let requestCount = 0;
      let schedule = (): void => {};
      let cancel = (): void => {};
      const requested = yield* Queue.unbounded<void>();
      const callbacks: FrameRequestCallback[] = [];
      const frames = {
        request: (next: FrameRequestCallback) => {
          requestCount += 1;
          callback = next;
          callbacks.push(next);
          Queue.offerUnsafe(requested, undefined);
          return requestCount;
        },
        cancel: () => {
          cancelled += 1;
        },
      };
      const fiber = yield* Effect.forkChild(
        Effect.scoped(
          Effect.gen(function* () {
            const scheduler = yield* makeAnimationFrameScheduler(
              Effect.sync(() => {
                published += 1;
              }),
            );
            schedule = scheduler.schedule;
            cancel = scheduler.cancel;
            scheduler.schedule();
            scheduler.schedule();
            return yield* Effect.never;
          }),
        ).pipe(Effect.provideService(AnimationFrames, frames)),
      );
      yield* Queue.take(requested);
      schedule();

      callback?.(1);
      yield* Effect.yieldNow;

      assert.strictEqual(published, 1);
      assert.strictEqual(requestCount, 1);
      schedule();
      yield* Queue.take(requested);
      cancel();
      schedule();
      yield* Effect.yieldNow;

      assert.strictEqual(cancelled, 1);
      assert.strictEqual(requestCount, 3);
      callbacks[1]?.(2);
      callbacks[2]?.(3);
      yield* Effect.yieldNow;
      assert.strictEqual(published, 2);

      schedule();
      yield* Queue.take(requested);
      yield* Fiber.interrupt(fiber);
      assert.strictEqual(cancelled, 2);
    }),
  ),
);
