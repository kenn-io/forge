import { Cache, Context, Effect, Layer, Option, Semaphore } from "effect";
import type { ApiProblemError, TransientTransportError } from "../api/effect-errors.js";
import type { GeneratedApi } from "../api/generated-api.js";
import type { RateLimitsResponse, SyncStatus } from "../api/types.js";

export type SyncReadError = ApiProblemError | TransientTransportError;

export interface SyncSnapshot {
  readonly status: Option.Option<SyncStatus>;
  readonly rateLimits: Option.Option<RateLimitsResponse>;
}

interface PendingSyncReads {
  readonly status: Effect.Effect<SyncStatus, SyncReadError, GeneratedApi>;
  readonly rateLimits: Effect.Effect<RateLimitsResponse, SyncReadError, GeneratedApi>;
}

interface SyncWorkflowShape {
  readonly refresh: (
    generation: number,
    status: Effect.Effect<SyncStatus, SyncReadError, GeneratedApi>,
    rateLimits: Effect.Effect<RateLimitsResponse, SyncReadError, GeneratedApi>,
  ) => Effect.Effect<SyncSnapshot>;
}

export class SyncWorkflow extends Context.Service<SyncWorkflow, SyncWorkflowShape>()("kenn-forge/SyncWorkflow") {}

export const SyncWorkflowLive = Layer.effect(SyncWorkflow)(
  Effect.gen(function* () {
    const semaphore = yield* Semaphore.make(2);
    const pendingReads = new Map<number, PendingSyncReads>();
    const refreshCache = yield* Cache.make({
      capacity: 64,
      timeToLive: "2 seconds",
      lookup: (generation: number) =>
        Effect.suspend(() => {
          const reads = pendingReads.get(generation);
          if (reads === undefined) return Effect.die(new Error(`missing sync refresh reads for ${generation}`));
          return Effect.all(
            [
              semaphore.withPermit(reads.status.pipe(Effect.option)),
              semaphore.withPermit(reads.rateLimits.pipe(Effect.option)),
            ],
            { concurrency: "unbounded" },
          ).pipe(Effect.map(([status, rateLimits]) => ({ status, rateLimits })));
        }),
    });

    const refresh = Effect.fn("SyncWorkflow.refresh")(function* (
      generation: number,
      status: Effect.Effect<SyncStatus, SyncReadError, GeneratedApi>,
      rateLimits: Effect.Effect<RateLimitsResponse, SyncReadError, GeneratedApi>,
    ) {
      yield* Effect.sync(() => {
        pendingReads.set(generation, { status, rateLimits });
      });
      return yield* Cache.get(refreshCache, generation).pipe(
        Effect.ensuring(Effect.sync(() => pendingReads.delete(generation))),
      );
    });

    return { refresh };
  }),
);
