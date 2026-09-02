import { Context, Effect, Fiber, FiberHandle, FiberMap, Layer, Option, Semaphore } from "effect";
import type * as Duration from "effect/Duration";
import { pollWhileVisible } from "../effect/poll-while-visible.js";
import type { ApiProblemError, TransientTransportError } from "../api/effect-errors.js";
import { GeneratedApi } from "../api/generated-api.js";
import type { PullDetail } from "../api/types.js";
import { makeLatestCommandByKey } from "../effect/latest-command-by-key.js";
import { makeLatestSharedRead, type LatestSharedRead } from "../effect/latest-shared-read.js";
import { MutationNeedsReview, type ProviderMutationFailure } from "./ordered-mutations.js";

export type DetailReadError = ApiProblemError | TransientTransportError;

interface DetailWorkflowShape extends LatestSharedRead<PullDetail, DetailReadError, GeneratedApi> {
  readonly refreshCI: (
    key: string,
    request: Effect.Effect<PullDetail, DetailReadError, GeneratedApi>,
  ) => Effect.Effect<PullDetail, DetailReadError>;
  readonly poll: <E, R>(pollOnce: Effect.Effect<void, E, R>, interval: Duration.Input) => Effect.Effect<void, E, R>;
  readonly submitLatestWrite: (
    key: string,
    command: Effect.Effect<void, ProviderMutationFailure, GeneratedApi>,
  ) => Effect.Effect<void, ProviderMutationFailure>;
  readonly stopPolling: Effect.Effect<void>;
}

export class DetailWorkflow extends Context.Service<DetailWorkflow, DetailWorkflowShape>()(
  "kenn-forge/DetailWorkflow",
) {}

export const DetailWorkflowLive = Layer.effect(DetailWorkflow)(
  Effect.gen(function* () {
    const api = yield* GeneratedApi;
    const detailReads = yield* makeLatestSharedRead<PullDetail, DetailReadError, GeneratedApi>();
    const ciFibers = yield* FiberMap.make<string, PullDetail, DetailReadError>();
    const ciAcceptance = yield* Semaphore.make(1);
    const pollingHandle = yield* FiberHandle.make<void, unknown>();
    const latestWrites = yield* makeLatestCommandByKey<ProviderMutationFailure>(
      "latest detail writes",
      (failure) => failure instanceof MutationNeedsReview,
    );
    const refreshCI = Effect.fn("DetailWorkflow.refreshCI")(function* (
      key: string,
      request: Effect.Effect<PullDetail, DetailReadError, GeneratedApi>,
    ) {
      const fiber = yield* ciAcceptance.withPermit(
        Effect.gen(function* () {
          const existing = yield* FiberMap.get(ciFibers, key);
          if (Option.isSome(existing)) return existing.value;
          return yield* FiberMap.run(ciFibers, key, request.pipe(Effect.provideService(GeneratedApi, api)));
        }),
      );
      return yield* Fiber.join(fiber);
    });
    function poll<E, R>(pollOnce: Effect.Effect<void, E, R>, interval: Duration.Input): Effect.Effect<void, E, R> {
      const program = pollWhileVisible(pollOnce, interval, { immediate: true });
      return FiberHandle.run(pollingHandle, program).pipe(Effect.flatMap(Fiber.join));
    }
    return {
      ...detailReads,
      refreshCI,
      poll,
      submitLatestWrite: (key, command) =>
        latestWrites.submit(key, command.pipe(Effect.provideService(GeneratedApi, api))),
      stopPolling: FiberHandle.clear(pollingHandle),
    };
  }),
);
