import { assert, it } from "@effect/vitest";
import { Deferred, Effect, Fiber, Ref } from "effect";
import { TestClock } from "effect/testing";
import { DiffContextPrefetch, makeDiffContextPrefetchLayer } from "./diff-context-prefetch.js";

const deferredBackgroundTurn = Effect.sleep("50 millis");
const concurrencyTwo = makeDiffContextPrefetchLayer({
  concurrency: 2,
  deferBackground: deferredBackgroundTurn,
});
const concurrencyOne = makeDiffContextPrefetchLayer({
  concurrency: 1,
  deferBackground: deferredBackgroundTurn,
});

it.layer(concurrencyTwo)("bounded diff context prefetch", (it) => {
  it.effect("starts no more work than the configured concurrency and reuses a released slot", () =>
    Effect.gen(function* () {
      const prefetch = yield* DiffContextPrefetch;
      const starts = yield* Ref.make<readonly string[]>([]);
      const started = yield* Effect.all([Deferred.make<void>(), Deferred.make<void>(), Deferred.make<void>()]);
      const releases = yield* Effect.all([Deferred.make<void>(), Deferred.make<void>(), Deferred.make<void>()]);

      const start = (id: string, index: number) =>
        Effect.forkChild(
          prefetch.run({
            generation: "diff-a",
            id,
            priority: "foreground",
            task: () =>
              Ref.update(starts, (current) => [...current, id]).pipe(
                Effect.andThen(Deferred.succeed(started[index]!, undefined)),
                Effect.andThen(Deferred.await(releases[index]!)),
              ),
          }),
        );

      const first = yield* start("one", 0);
      yield* Deferred.await(started[0]!);
      const second = yield* start("two", 1);
      yield* Deferred.await(started[1]!);
      const third = yield* start("three", 2);
      yield* Effect.yieldNow;
      assert.isFalse(yield* Deferred.isDone(started[2]!));
      assert.deepStrictEqual(yield* Ref.get(starts), ["one", "two"]);

      yield* Deferred.succeed(releases[0]!, undefined);
      yield* Deferred.await(started[2]!);
      assert.deepStrictEqual(yield* Ref.get(starts), ["one", "two", "three"]);

      yield* Effect.forEach(releases, (release) => Deferred.succeed(release, undefined));
      yield* Effect.forEach([first, second, third], Fiber.join);
    }),
  );
});

it.layer(concurrencyOne)("background diff context prefetch", (it) => {
  it.effect("defers background work until the browser background turn", () =>
    Effect.gen(function* () {
      const prefetch = yield* DiffContextPrefetch;
      const started = yield* Deferred.make<void>();
      const release = yield* Deferred.make<void>();
      const fiber = yield* Effect.forkChild(
        prefetch.run({
          generation: "diff-a",
          id: "background",
          priority: "background",
          task: () => Deferred.succeed(started, undefined).pipe(Effect.andThen(Deferred.await(release))),
        }),
      );

      yield* Effect.yieldNow;
      assert.isFalse(yield* Deferred.isDone(started));

      yield* TestClock.adjust("50 millis");
      yield* Deferred.await(started);
      yield* Deferred.succeed(release, undefined);
      yield* Fiber.join(fiber);
    }),
  );
});

it.layer(concurrencyOne)("priority changes", (it) => {
  it.effect("promotes foreground work ahead of deferred background work", () =>
    Effect.gen(function* () {
      const prefetch = yield* DiffContextPrefetch;
      const starts = yield* Ref.make<readonly string[]>([]);
      const blockerStarted = yield* Deferred.make<void>();
      const blockerRelease = yield* Deferred.make<void>();
      const promotedRelease = yield* Deferred.make<void>();
      const backgroundRelease = yield* Deferred.make<void>();
      const promotedStarted = yield* Deferred.make<void>();
      const backgroundStarted = yield* Deferred.make<void>();

      const blocker = yield* Effect.forkChild(
        prefetch.run({
          generation: "diff-a",
          id: "blocker",
          priority: "foreground",
          task: () =>
            Ref.update(starts, (current) => [...current, "blocker"]).pipe(
              Effect.andThen(Deferred.succeed(blockerStarted, undefined)),
              Effect.andThen(Deferred.await(blockerRelease)),
            ),
        }),
      );
      yield* Deferred.await(blockerStarted);
      const background = yield* Effect.forkChild(
        prefetch.run({
          generation: "diff-a",
          id: "background",
          priority: "background",
          task: () =>
            Ref.update(starts, (current) => [...current, "background"]).pipe(
              Effect.andThen(Deferred.succeed(backgroundStarted, undefined)),
              Effect.andThen(Deferred.await(backgroundRelease)),
            ),
        }),
      );
      const promoted = yield* Effect.forkChild(
        prefetch.run({
          generation: "diff-a",
          id: "promoted",
          priority: "background",
          task: () =>
            Ref.update(starts, (current) => [...current, "promoted"]).pipe(
              Effect.andThen(Deferred.succeed(promotedStarted, undefined)),
              Effect.andThen(Deferred.await(promotedRelease)),
            ),
        }),
      );

      yield* Effect.yieldNow;
      yield* prefetch.setPriority("diff-a", "promoted", "foreground");
      yield* Deferred.succeed(blockerRelease, undefined);
      yield* Deferred.await(promotedStarted);
      assert.deepStrictEqual(yield* Ref.get(starts), ["blocker", "promoted"]);

      yield* Deferred.succeed(promotedRelease, undefined);
      yield* TestClock.adjust("50 millis");
      yield* Deferred.await(backgroundStarted);
      assert.deepStrictEqual(yield* Ref.get(starts), ["blocker", "promoted", "background"]);

      yield* Deferred.succeed(backgroundRelease, undefined);
      yield* Effect.forEach([blocker, promoted, background], Fiber.join);
    }),
  );
});

it.layer(concurrencyOne)("generation replacement", (it) => {
  it.effect("cancels the old generation without releasing its active slot early", () =>
    Effect.gen(function* () {
      const prefetch = yield* DiffContextPrefetch;
      const oldStarted = yield* Deferred.make<void>();
      const oldRelease = yield* Deferred.make<void>();
      const cancellationProbe = yield* Deferred.make<Effect.Effect<boolean>>();
      const currentStarted = yield* Deferred.make<void>();
      const currentRelease = yield* Deferred.make<void>();

      const old = yield* Effect.forkChild(
        prefetch.run({
          generation: "diff-old",
          id: "old",
          priority: "foreground",
          task: (isCancelled) =>
            Deferred.succeed(cancellationProbe, isCancelled).pipe(
              Effect.andThen(Deferred.succeed(oldStarted, undefined)),
              Effect.andThen(Deferred.await(oldRelease)),
            ),
        }),
      );
      yield* Deferred.await(oldStarted);
      yield* prefetch.setGeneration("diff-current");
      const current = yield* Effect.forkChild(
        prefetch.run({
          generation: "diff-current",
          id: "current",
          priority: "foreground",
          task: () => Deferred.succeed(currentStarted, undefined).pipe(Effect.andThen(Deferred.await(currentRelease))),
        }),
      );

      const isCancelled = yield* Deferred.await(cancellationProbe);
      assert.isTrue(yield* isCancelled);
      yield* Effect.yieldNow;
      assert.isFalse(yield* Deferred.isDone(currentStarted));

      yield* Deferred.succeed(oldRelease, undefined);
      yield* Deferred.await(currentStarted);
      yield* Deferred.succeed(currentRelease, undefined);
      yield* Effect.forEach([old, current], Fiber.join);
    }),
  );
});
