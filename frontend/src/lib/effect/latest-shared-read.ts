import { Cache, Effect, Fiber, FiberHandle, SynchronizedRef } from "effect";
import type { Scope } from "effect/Scope";

interface ActiveRead<A, E> {
  readonly key: string;
  readonly fiber: Fiber.Fiber<A, E>;
}

export interface LatestSharedRead<A, E, RequestRequirements = never> {
  readonly read: (key: string, request: Effect.Effect<A, E, RequestRequirements>) => Effect.Effect<A, E>;
  readonly invalidate: (key: string) => Effect.Effect<void>;
  readonly clear: Effect.Effect<void>;
}

export function makeLatestSharedRead<A, E, RequestRequirements = never>(): Effect.Effect<
  LatestSharedRead<A, E, RequestRequirements>,
  never,
  RequestRequirements | Scope
> {
  return Effect.gen(function* () {
    const handle = yield* FiberHandle.make<A, E>();
    const active = yield* SynchronizedRef.make<ActiveRead<A, E> | undefined>(undefined);
    const pending = new Map<string, Effect.Effect<A, E, RequestRequirements>>();
    const cache = yield* Cache.make({
      capacity: 64,
      timeToLive: "0 millis",
      lookup: (key: string) =>
        Effect.suspend(() => {
          const request = pending.get(key);
          if (request === undefined) return Effect.die(new Error(`missing shared read for ${key}`));
          return request;
        }),
    });
    const read = Effect.fn("LatestSharedRead.read")(function* (
      key: string,
      request: Effect.Effect<A, E, RequestRequirements>,
    ) {
      const fiber = yield* SynchronizedRef.modifyEffect(active, (current) => {
        if (current?.key === key) {
          return Effect.succeed<readonly [Fiber.Fiber<A, E>, ActiveRead<A, E> | undefined]>([current.fiber, current]);
        }
        return Effect.sync(() => pending.set(key, request)).pipe(
          Effect.andThen(
            FiberHandle.run(
              handle,
              Cache.get(cache, key).pipe(Effect.ensuring(Effect.sync(() => pending.delete(key)))),
            ),
          ),
          Effect.map((nextFiber): readonly [Fiber.Fiber<A, E>, ActiveRead<A, E> | undefined] => [
            nextFiber,
            { key, fiber: nextFiber },
          ]),
        );
      });
      return yield* Fiber.join(fiber).pipe(
        Effect.ensuring(SynchronizedRef.update(active, (current) => (current?.fiber === fiber ? undefined : current))),
      );
    });
    return {
      read,
      invalidate: (key: string) => Cache.invalidate(cache, key),
      clear: FiberHandle.clear(handle).pipe(
        Effect.andThen(Cache.invalidateAll(cache)),
        Effect.andThen(SynchronizedRef.set(active, undefined)),
        Effect.andThen(Effect.sync(() => pending.clear())),
      ),
    };
  });
}
