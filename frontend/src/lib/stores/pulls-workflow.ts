import { Cache, Context, Effect, Fiber, FiberHandle, FiberMap, Layer, Ref, Semaphore } from "effect";
import { TransientTransportError } from "../api/effect-errors.js";
import type { PullRequest } from "../api/types.js";

export type FetchPullResult =
  | { readonly status: "found"; readonly pull: PullRequest }
  | { readonly status: "not-found" }
  | { readonly status: "error"; readonly message: string };

interface PullsWorkflowShape {
  readonly list: <E, R>(read: Effect.Effect<PullRequest[], E, R>) => Effect.Effect<PullRequest[], E, R>;
  readonly reconcile: <E, R, ProjectError, ProjectRequirements>(
    read: Effect.Effect<readonly PullRequest[], E, R>,
    project: (result: readonly PullRequest[]) => Effect.Effect<void, ProjectError, ProjectRequirements>,
  ) => Effect.Effect<void, E | ProjectError | TransientTransportError, R | ProjectRequirements>;
  readonly refresh: (key: string, read: Effect.Effect<FetchPullResult>) => Effect.Effect<FetchPullResult>;
  readonly invalidate: (key: string) => Effect.Effect<void>;
}

export class PullsWorkflow extends Context.Service<PullsWorkflow, PullsWorkflowShape>()("kenn-forge/PullsWorkflow") {}

export const PullsWorkflowLive = Layer.effect(PullsWorkflow)(
  Effect.gen(function* () {
    const listHandle = yield* FiberHandle.make<PullRequest[], unknown>();
    const listGeneration = yield* Ref.make(0);
    const projection = yield* Semaphore.make(1);
    const itemFibers = yield* FiberMap.make<string, FetchPullResult>();
    const pendingReads = new Map<string, Effect.Effect<FetchPullResult>>();
    const itemCache = yield* Cache.make({
      capacity: 64,
      timeToLive: "2 seconds",
      lookup: (key: string) =>
        Effect.suspend(() => {
          const read = pendingReads.get(key);
          if (read === undefined) return Effect.die(new Error(`missing pull refresh for ${key}`));
          return FiberMap.run(itemFibers, key, read).pipe(Effect.flatMap(Fiber.join));
        }),
    });

    function list<E, R>(read: Effect.Effect<PullRequest[], E, R>): Effect.Effect<PullRequest[], E, R> {
      return projection
        .withPermit(Ref.update(listGeneration, (generation) => generation + 1))
        .pipe(Effect.andThen(FiberHandle.run(listHandle, read)), Effect.flatMap(Fiber.join));
    }

    function reconcile<E, R, ProjectError, ProjectRequirements>(
      read: Effect.Effect<readonly PullRequest[], E, R>,
      project: (result: readonly PullRequest[]) => Effect.Effect<void, ProjectError, ProjectRequirements>,
    ): Effect.Effect<void, E | ProjectError | TransientTransportError, R | ProjectRequirements> {
      return Effect.gen(function* () {
        const generation = yield* Ref.get(listGeneration);
        const result = yield* read;
        yield* projection.withPermit(
          Ref.get(listGeneration).pipe(
            Effect.flatMap(
              (current): Effect.Effect<void, ProjectError | TransientTransportError, ProjectRequirements> =>
                current === generation
                  ? project(result)
                  : Effect.fail(
                      TransientTransportError.make({
                        operation: "reconcile pull requests after superseded provider event",
                        cause: new Error("a foreground pull request query replaced event reconciliation"),
                      }),
                    ),
            ),
          ),
        );
      });
    }

    const refresh = Effect.fn("PullsWorkflow.refresh")(function* (key: string, read: Effect.Effect<FetchPullResult>) {
      yield* Effect.sync(() => pendingReads.set(key, read));
      return yield* Cache.get(itemCache, key).pipe(Effect.ensuring(Effect.sync(() => pendingReads.delete(key))));
    });

    return {
      list,
      reconcile,
      refresh,
      invalidate: (key: string) => Cache.invalidate(itemCache, key),
    };
  }),
);
