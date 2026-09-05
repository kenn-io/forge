import { Context, Effect, Exit, Fiber, FiberHandle, Layer, Ref, Semaphore } from "effect";
import type * as Duration from "effect/Duration";
import { pollWhileVisible } from "../effect/poll-while-visible.js";
import { TransientTransportError, type ApiProblemError } from "../api/effect-errors.js";
import { GeneratedApi } from "../api/generated-api.js";
import type { Issue, IssueDetail } from "../api/types.js";
import { makeLatestCommandByKey } from "../effect/latest-command-by-key.js";
import { makeLatestSharedRead } from "../effect/latest-shared-read.js";
import { MutationNeedsReview, type ProviderMutationFailure } from "./ordered-mutations.js";

export type IssueDetailSyncMode = boolean | "background";

export interface IssueDetailReadResult {
  readonly detail: IssueDetail;
  readonly syncMode?: IssueDetailSyncMode;
}

export type IssueDetailReadError = ApiProblemError | TransientTransportError;

interface IssuesWorkflowShape {
  readonly list: <E, R>(read: Effect.Effect<Issue[], E, R>) => Effect.Effect<Issue[], E, R>;
  readonly reconcile: <E, R, ProjectError, ProjectRequirements>(
    read: Effect.Effect<readonly Issue[], E, R>,
    project: (result: readonly Issue[]) => Effect.Effect<void, ProjectError, ProjectRequirements>,
  ) => Effect.Effect<void, E | ProjectError | TransientTransportError, R | ProjectRequirements>;
  readonly detail: (
    key: string,
    syncMode: IssueDetailSyncMode,
    read: Effect.Effect<IssueDetail, IssueDetailReadError, GeneratedApi>,
  ) => Effect.Effect<IssueDetailReadResult, IssueDetailReadError>;
  readonly poll: <E, R>(
    generation: number,
    pollOnce: Effect.Effect<void, E, R>,
    interval: Duration.Input,
  ) => Effect.Effect<void, E, R>;
  readonly submitLatestWrite: (
    key: string,
    command: Effect.Effect<void, ProviderMutationFailure, GeneratedApi>,
  ) => Effect.Effect<void, ProviderMutationFailure>;
  readonly stopPolling: (generation: number) => Effect.Effect<void>;
}

interface DetailIntentState {
  readonly waiters: number;
  readonly syncMode: IssueDetailSyncMode;
  readonly claimed: boolean;
  readonly latestToken: number;
}

function syncIntentRank(mode: IssueDetailSyncMode): number {
  if (mode === true) return 2;
  if (mode === "background") return 1;
  return 0;
}

function strongerSyncMode(a: IssueDetailSyncMode, b: IssueDetailSyncMode): IssueDetailSyncMode {
  return syncIntentRank(b) > syncIntentRank(a) ? b : a;
}

export class IssuesWorkflow extends Context.Service<IssuesWorkflow, IssuesWorkflowShape>()(
  "kenn-forge/IssuesWorkflow",
) {}

export const IssuesWorkflowLive = Layer.effect(IssuesWorkflow)(
  Effect.gen(function* () {
    const api = yield* GeneratedApi;
    const listHandle = yield* FiberHandle.make<Issue[], unknown>();
    const listGeneration = yield* Ref.make(0);
    const projection = yield* Semaphore.make(1);
    const detailReads = yield* makeLatestSharedRead<IssueDetail, IssueDetailReadError, GeneratedApi>();
    const detailIntents = yield* Ref.make<ReadonlyMap<string, DetailIntentState>>(new Map());
    const nextDetailToken = yield* Ref.make(0);
    const detailPollingHandle = yield* FiberHandle.make<void, unknown>();
    const pollingGeneration = yield* Ref.make(0);
    const pollingAcceptance = yield* Semaphore.make(1);
    const latestWrites = yield* makeLatestCommandByKey<ProviderMutationFailure>(
      "latest issue writes",
      (failure) => failure instanceof MutationNeedsReview,
    );

    function list<E, R>(read: Effect.Effect<Issue[], E, R>): Effect.Effect<Issue[], E, R> {
      return projection
        .withPermit(Ref.update(listGeneration, (generation) => generation + 1))
        .pipe(Effect.andThen(FiberHandle.run(listHandle, read)), Effect.flatMap(Fiber.join));
    }

    function reconcile<E, R, ProjectError, ProjectRequirements>(
      read: Effect.Effect<readonly Issue[], E, R>,
      project: (result: readonly Issue[]) => Effect.Effect<void, ProjectError, ProjectRequirements>,
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
                        operation: "reconcile issues after superseded provider event",
                        cause: new Error("a foreground issue query replaced event reconciliation"),
                      }),
                    ),
            ),
          ),
        );
      });
    }

    const registerDetailIntent = (key: string, syncMode: IssueDetailSyncMode) =>
      Ref.updateAndGet(nextDetailToken, (token) => token + 1).pipe(
        Effect.tap((token) =>
          Ref.update(detailIntents, (current) => {
            const existing = current.get(key);
            return new Map(current).set(key, {
              waiters: (existing?.waiters ?? 0) + 1,
              syncMode: existing === undefined ? syncMode : strongerSyncMode(existing.syncMode, syncMode),
              claimed: existing?.claimed ?? false,
              latestToken: token,
            });
          }),
        ),
      );

    const finishDetailIntent = (key: string, token: number, claim: boolean) =>
      Ref.modify(
        detailIntents,
        (current): readonly [IssueDetailSyncMode | undefined, ReadonlyMap<string, DetailIntentState>] => {
          const existing = current.get(key);
          if (existing === undefined) return [undefined, current];
          const syncMode = claim && !existing.claimed && token === existing.latestToken ? existing.syncMode : undefined;
          const next = new Map(current);
          if (existing.waiters === 1) {
            next.delete(key);
          } else {
            next.set(key, {
              ...existing,
              waiters: existing.waiters - 1,
              claimed: existing.claimed || syncMode !== undefined,
            });
          }
          return [syncMode, next];
        },
      );

    const detail = Effect.fn("IssuesWorkflow.detail")(function* (
      key: string,
      syncMode: IssueDetailSyncMode,
      read: Effect.Effect<IssueDetail, IssueDetailReadError, GeneratedApi>,
    ) {
      const token = yield* registerDetailIntent(key, syncMode);
      const exit = yield* Effect.exit(detailReads.read(key, read));
      if (Exit.isFailure(exit)) {
        yield* finishDetailIntent(key, token, false);
        return yield* Effect.failCause(exit.cause);
      }
      const ownedSyncMode = yield* finishDetailIntent(key, token, true);
      return ownedSyncMode === undefined ? { detail: exit.value } : { detail: exit.value, syncMode: ownedSyncMode };
    });

    function poll<E, R>(
      generation: number,
      pollOnce: Effect.Effect<void, E, R>,
      interval: Duration.Input,
    ): Effect.Effect<void, E, R> {
      const program = pollWhileVisible(pollOnce, interval);
      return pollingAcceptance
        .withPermit(
          Ref.modify(pollingGeneration, (current): readonly [boolean, number] =>
            generation < current ? [false, current] : [true, generation],
          ).pipe(
            Effect.flatMap((accepted) =>
              accepted
                ? FiberHandle.run(detailPollingHandle, program).pipe(Effect.map((fiber) => fiber))
                : Effect.succeed(null),
            ),
          ),
        )
        .pipe(Effect.flatMap((fiber) => (fiber === null ? Effect.void : Fiber.join(fiber))));
    }

    const stopPolling = (generation: number) =>
      pollingAcceptance.withPermit(
        Ref.modify(pollingGeneration, (current): readonly [boolean, number] =>
          generation < current ? [false, current] : [true, generation],
        ).pipe(Effect.flatMap((accepted) => (accepted ? FiberHandle.clear(detailPollingHandle) : Effect.void))),
      );

    return {
      list,
      reconcile,
      detail,
      poll,
      submitLatestWrite: (key, command) =>
        latestWrites.submit(key, command.pipe(Effect.provideService(GeneratedApi, api))),
      stopPolling,
    };
  }),
);
