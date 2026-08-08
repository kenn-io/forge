import { assert, it } from "@effect/vitest";
import { Effect, Fiber, Queue } from "effect";
import { makeMicrotaskScheduler, Microtasks } from "./microtask.js";

it.effect("coalesces microtask demand and stops publishing after scope interruption", () =>
  Effect.scoped(
    Effect.gen(function* () {
      const scheduled = yield* Queue.unbounded<void>();
      const callbacks: Array<() => void> = [];
      let published = 0;
      let schedule = (): void => {};
      const fiber = yield* Effect.forkChild(
        Effect.scoped(
          Effect.gen(function* () {
            const scheduler = yield* makeMicrotaskScheduler(
              Effect.sync(() => {
                published += 1;
              }),
            );
            schedule = scheduler.schedule;
            scheduler.schedule();
            scheduler.schedule();
            return yield* Effect.never;
          }),
        ).pipe(
          Effect.provideService(Microtasks, {
            schedule: (callback) => {
              callbacks.push(callback);
              Queue.offerUnsafe(scheduled, undefined);
            },
          }),
        ),
      );
      yield* Queue.take(scheduled);

      assert.strictEqual(callbacks.length, 1);
      callbacks.shift()?.();
      yield* Effect.yieldNow;
      assert.strictEqual(published, 1);

      schedule();
      yield* Queue.take(scheduled);
      yield* Fiber.interrupt(fiber);
      callbacks.shift()?.();
      yield* Effect.yieldNow;

      assert.strictEqual(published, 1);
    }),
  ),
);
