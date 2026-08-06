import { Cause, Context, Data, Deferred, Effect, Exit, Fiber, FiberMap, Layer, Option, Ref, Semaphore } from "effect";
import type { DocsRequestError } from "../api/docs/api.js";
import type { GitPublishResponse } from "../api/docs/types.js";
import type { CommandQueueClosed } from "../effect/ordered-command-queue.js";
import { makeOrderedCommandQueue } from "../effect/ordered-command-queue.js";

export interface DocsWorkflowService {
  readonly read: <A, E, R>(
    owner: string,
    identity: DocsReadIdentity,
    request: Effect.Effect<A, E, R>,
  ) => Effect.Effect<A, E, R>;
  readonly stop: (owner: string) => Effect.Effect<void>;
  readonly reconcileMutation: <A, S, R, R2>(
    identity: DocsMutationIdentity,
    operation: string,
    request: Effect.Effect<A, DocsRequestError, R>,
    reconcile: Effect.Effect<S, DocsRequestError, R2>,
    recover: (snapshot: S, recoveryEvidence?: string) => Option.Option<A>,
  ) => Effect.Effect<ReconciledDocsMutation<A, S>, DocsRequestError | DocsMutationStateUncertainError, R | R2>;
  readonly claimPresenter: (
    surfaceID: string,
    sessionID: string,
    onRefresh: Effect.Effect<void>,
  ) => Effect.Effect<void>;
  readonly releasePresenter: (surfaceID: string, sessionID: string) => Effect.Effect<void>;
  readonly present: (surfaceID: string, sessionID: string, presentation: Effect.Effect<void>) => Effect.Effect<void>;
  readonly mutate: <A, E, R>(
    surfaceID: string,
    sessionID: string,
    mutation: Effect.Effect<A, E, R>,
  ) => Effect.Effect<A, E | CommandQueueClosed, R>;
  readonly claimPublisher: (
    folderID: string,
    sessionID: string,
    onState: (state: DocsPublishState) => Effect.Effect<void>,
  ) => Effect.Effect<void>;
  readonly releasePublisher: (sessionID: string) => Effect.Effect<void>;
  readonly acknowledgePublisher: (sessionID: string) => Effect.Effect<void>;
  readonly publish: <R>(command: DocsPublishCommand<R>) => Effect.Effect<void, CommandQueueClosed, R>;
}

export class DocsMutationStateUncertainError extends Data.TaggedError("DocsMutationStateUncertainError")<{
  readonly operation: string;
  readonly requestFailure: DocsRequestError | null;
  readonly reconciliationFailure: DocsRequestError | null;
}> {}

export interface ReconciledDocsMutation<A, S> {
  readonly result: A;
  readonly snapshot: S;
  readonly recovered: boolean;
}

export interface DocsMutationIdentity {
  readonly resource: string;
  readonly intent: string;
  readonly recoveryEvidence?: string | undefined;
}

export interface DocsReadIdentity {
  readonly lane: string;
  readonly resource: string;
}

interface RetainedDocsUncertainty {
  readonly operation: string;
  readonly intent: string;
  readonly recoveryEvidence?: string | undefined;
  readonly requestFailure: DocsRequestError | null;
}

export interface DocsPublishRequest {
  readonly submissionID: number;
  readonly folderID: string;
  readonly message: string;
}

export type DocsPublishState =
  | { readonly kind: "pending"; readonly request: DocsPublishRequest }
  | { readonly kind: "succeeded"; readonly request: DocsPublishRequest; readonly result: GitPublishResponse }
  | { readonly kind: "failed"; readonly request: DocsPublishRequest; readonly error: DocsRequestError };

export interface DocsPublishCommand<R> {
  readonly folderID: string;
  readonly sessionID: string;
  readonly message: string;
  readonly request: Effect.Effect<GitPublishResponse, DocsRequestError, R>;
}

interface DocsPublisherSurface {
  readonly sessionID: string;
  readonly onState: (state: DocsPublishState) => Effect.Effect<void>;
}

interface DocsPublishEntry {
  readonly state: DocsPublishState;
  readonly targetSession?: string | undefined;
}

interface DocsPublishRegistry {
  readonly entries: ReadonlyMap<string, DocsPublishEntry>;
  readonly surfaces: ReadonlyMap<string, DocsPublisherSurface>;
}

interface DocsPresenter {
  readonly sessionID: string;
  readonly onRefresh: Effect.Effect<void>;
}

interface DocsPresenterEntry {
  readonly pending: boolean;
  readonly presenter?: DocsPresenter | undefined;
}

interface DocsReadClaim {
  readonly key: string;
  readonly generation: number;
}

export class DocsWorkflow extends Context.Service<DocsWorkflow, DocsWorkflowService>()("kenn-forge/DocsWorkflow") {}

export const docsMutationOwner = "docs-mutations";

let nextDocsOwner = 0;

export function makeDocsOwner(prefix: string): string {
  nextDocsOwner += 1;
  return `${prefix}:${nextDocsOwner}`;
}

export const makeDocsWorkflow: Effect.Effect<DocsWorkflowService, never, import("effect/Scope").Scope> = Effect.gen(
  function* () {
    const reads = yield* FiberMap.make<string, unknown, unknown>();
    const readClaimsByOwner = yield* Ref.make<ReadonlyMap<string, ReadonlyMap<string, DocsReadClaim>>>(new Map());
    const readGenerations = yield* Ref.make<ReadonlyMap<string, number>>(new Map());
    const readAcceptance = yield* Semaphore.make(1);
    const presenters = yield* Ref.make<ReadonlyMap<string, DocsPresenterEntry>>(new Map());
    const uncertainties = yield* Ref.make<ReadonlyMap<string, RetainedDocsUncertainty>>(new Map());
    const publishSequence = yield* Ref.make(1);
    const publishRegistry = yield* Ref.make<DocsPublishRegistry>({ entries: new Map(), surfaces: new Map() });
    const mutations = yield* makeOrderedCommandQueue("Docs mutations", (command: Effect.Effect<void>) => command, 64);

    const read = Effect.fn("DocsWorkflow.read")(function* <A, E, R>(
      owner: string,
      identity: DocsReadIdentity,
      request: Effect.Effect<A, E, R>,
    ) {
      if (owner === docsMutationOwner) return yield* request;
      const fiber = yield* readAcceptance.withPermit(
        Effect.gen(function* () {
          const previous = yield* Ref.get(readClaimsByOwner).pipe(
            Effect.map((claimsByOwner) => claimsByOwner.get(owner)?.get(identity.lane)),
          );
          const generation = yield* Ref.modify(readGenerations, (generations) => {
            const nextGeneration = (generations.get(identity.resource) ?? 0) + 1;
            return [nextGeneration, new Map(generations).set(identity.resource, nextGeneration)];
          });
          yield* Ref.update(readClaimsByOwner, (claimsByOwner) => {
            const next = new Map(claimsByOwner);
            const ownedClaims = new Map(claimsByOwner.get(owner));
            ownedClaims.set(identity.lane, { key: identity.resource, generation });
            next.set(owner, ownedClaims);
            return next;
          });
          if (previous !== undefined && previous.key !== identity.resource) {
            const previousIsCurrent = yield* Ref.get(readGenerations).pipe(
              Effect.map((generations) => generations.get(previous.key) === previous.generation),
            );
            if (previousIsCurrent) yield* FiberMap.remove(reads, previous.key);
          }
          return yield* FiberMap.run(reads, identity.resource, request);
        }),
      );
      return yield* Fiber.join(fiber);
    });

    const stop = Effect.fn("DocsWorkflow.stop")((owner: string) =>
      readAcceptance.withPermit(
        Effect.gen(function* () {
          const ownedClaims = yield* Ref.modify(
            readClaimsByOwner,
            (
              claimsByOwner,
            ): readonly [ReadonlyArray<DocsReadClaim>, ReadonlyMap<string, ReadonlyMap<string, DocsReadClaim>>] => {
              const claims = Array.from(claimsByOwner.get(owner)?.values() ?? []);
              const next = new Map(claimsByOwner);
              next.delete(owner);
              return [claims, next];
            },
          );
          const currentGenerations = yield* Ref.get(readGenerations);
          yield* Effect.forEach(
            ownedClaims,
            (claim) =>
              currentGenerations.get(claim.key) === claim.generation ? FiberMap.remove(reads, claim.key) : Effect.void,
            { discard: true },
          );
        }),
      ),
    );

    const reconcileMutation = Effect.fn("DocsWorkflow.reconcileMutation")(function* <A, S, R, R2>(
      identity: DocsMutationIdentity,
      operation: string,
      request: Effect.Effect<A, DocsRequestError, R>,
      reconcile: Effect.Effect<S, DocsRequestError, R2>,
      recover: (snapshot: S, recoveryEvidence?: string) => Option.Option<A>,
    ) {
      const retained = (yield* Ref.get(uncertainties)).get(identity.resource);
      if (retained !== undefined) {
        const snapshot = yield* reconcile.pipe(
          Effect.mapError(
            (reconciliationFailure) =>
              new DocsMutationStateUncertainError({
                operation: retained.operation,
                requestFailure: retained.requestFailure,
                reconciliationFailure,
              }),
          ),
        );
        if (retained.intent === identity.intent) {
          const recovered = recover(snapshot, retained.recoveryEvidence);
          if (Option.isSome(recovered)) {
            yield* Ref.update(uncertainties, (entries) => {
              const next = new Map(entries);
              next.delete(identity.resource);
              return next;
            });
            return { result: recovered.value, snapshot, recovered: true } satisfies ReconciledDocsMutation<A, S>;
          }
          if (retained.requestFailure === null) {
            return yield* Effect.fail(
              new DocsMutationStateUncertainError({
                operation: retained.operation,
                requestFailure: null,
                reconciliationFailure: null,
              }),
            );
          }
        }
        yield* Ref.update(uncertainties, (entries) => {
          const next = new Map(entries);
          next.delete(identity.resource);
          return next;
        });
      }

      const requestResult = yield* request.pipe(
        Effect.match({
          onFailure: (failure) => ({ failed: failure }),
          onSuccess: (value) => ({ succeeded: value }),
        }),
      );
      if ("failed" in requestResult && requestResult.failed.status !== 0) {
        return yield* Effect.fail(requestResult.failed);
      }
      const requestFailure = "failed" in requestResult ? requestResult.failed : null;
      const snapshot = yield* reconcile.pipe(
        Effect.mapError(
          (reconciliationFailure) =>
            new DocsMutationStateUncertainError({ operation, requestFailure, reconciliationFailure }),
        ),
        Effect.tapError(() =>
          Ref.update(uncertainties, (entries) =>
            new Map(entries).set(identity.resource, {
              operation,
              intent: identity.intent,
              ...(identity.recoveryEvidence === undefined ? {} : { recoveryEvidence: identity.recoveryEvidence }),
              requestFailure,
            }),
          ),
        ),
      );
      yield* Ref.update(uncertainties, (entries) => {
        const next = new Map(entries);
        next.delete(identity.resource);
        return next;
      });
      if ("succeeded" in requestResult) {
        return { result: requestResult.succeeded, snapshot, recovered: false } satisfies ReconciledDocsMutation<A, S>;
      }
      const recovered = recover(snapshot, identity.recoveryEvidence);
      if (Option.isSome(recovered)) {
        return { result: recovered.value, snapshot, recovered: true } satisfies ReconciledDocsMutation<A, S>;
      }
      return yield* Effect.fail(requestResult.failed);
    });

    const claimPresenter = Effect.fn("DocsWorkflow.claimPresenter")(function* (
      surfaceID: string,
      sessionID: string,
      onRefresh: Effect.Effect<void>,
    ) {
      const refresh = yield* Ref.modify(
        presenters,
        (registry): readonly [Option.Option<Effect.Effect<void>>, ReadonlyMap<string, DocsPresenterEntry>] => {
          const current = registry.get(surfaceID);
          const pending = current?.pending === true;
          const next = new Map(registry).set(surfaceID, {
            pending: false,
            presenter: { sessionID, onRefresh },
          });
          return [pending ? Option.some(onRefresh) : Option.none(), next];
        },
      );
      if (Option.isSome(refresh)) yield* refresh.value;
    });

    const releasePresenter = (surfaceID: string, sessionID: string): Effect.Effect<void> =>
      Ref.update(presenters, (registry) => {
        const current = registry.get(surfaceID);
        if (current?.presenter?.sessionID !== sessionID) return registry;
        return new Map(registry).set(surfaceID, { pending: current.pending });
      });

    const present = Effect.fn("DocsWorkflow.present")(function* (
      surfaceID: string,
      sessionID: string,
      presentation: Effect.Effect<void>,
    ) {
      const current = yield* Ref.get(presenters).pipe(Effect.map((registry) => registry.get(surfaceID)));
      if (current?.presenter?.sessionID !== sessionID) return;
      yield* presentation;
      yield* Ref.update(presenters, (registry) => {
        const latest = registry.get(surfaceID);
        if (latest?.presenter?.sessionID !== sessionID) return registry;
        return new Map(registry).set(surfaceID, { ...latest, pending: false });
      });
    });

    const markPresenterPending = Effect.fn("DocsWorkflow.markPresenterPending")(function* (
      surfaceID: string,
      submittedSessionID: string,
    ) {
      const refresh = yield* Ref.modify(
        presenters,
        (registry): readonly [Option.Option<Effect.Effect<void>>, ReadonlyMap<string, DocsPresenterEntry>] => {
          const current = registry.get(surfaceID);
          if (current?.presenter !== undefined && current.presenter.sessionID !== submittedSessionID) {
            return [
              Option.some(current.presenter.onRefresh),
              new Map(registry).set(surfaceID, { ...current, pending: false }),
            ];
          }
          return [
            Option.none(),
            new Map(registry).set(surfaceID, {
              pending: true,
              ...(current?.presenter === undefined ? {} : { presenter: current.presenter }),
            }),
          ];
        },
      );
      if (Option.isSome(refresh)) yield* refresh.value;
    });

    const mutate = Effect.fn("DocsWorkflow.mutate")(function* <A, E, R>(
      surfaceID: string,
      sessionID: string,
      mutation: Effect.Effect<A, E, R>,
    ) {
      const context = yield* Effect.context<R>();
      const result = yield* Deferred.make<Exit.Exit<A, E>>();
      const command = Effect.exit(mutation.pipe(Effect.provide(context))).pipe(
        Effect.flatMap((exit) =>
          markPresenterPending(surfaceID, sessionID).pipe(Effect.andThen(Deferred.succeed(result, exit))),
        ),
        Effect.asVoid,
      );
      const completion = yield* mutations.accept(command);
      yield* completion;
      return yield* Deferred.await(result).pipe(Effect.flatMap((exit) => exit));
    });

    const completePublisher = Effect.fn("DocsWorkflow.completePublisher")(function* (
      folderID: string,
      state: DocsPublishState,
    ) {
      const observer = yield* Ref.modify(
        publishRegistry,
        (registry): readonly [Option.Option<DocsPublisherSurface>, DocsPublishRegistry] => {
          const current = registry.entries.get(folderID);
          if (current === undefined || current.state.request.submissionID !== state.request.submissionID) {
            return [Option.none(), registry];
          }
          const entry: DocsPublishEntry = {
            state,
            ...(current.targetSession === undefined ? {} : { targetSession: current.targetSession }),
          };
          const entries = new Map(registry.entries).set(folderID, entry);
          const surface = registry.surfaces.get(folderID);
          return [
            surface !== undefined && surface.sessionID === entry.targetSession ? Option.some(surface) : Option.none(),
            { entries, surfaces: registry.surfaces },
          ];
        },
      );
      if (Option.isSome(observer)) yield* observer.value.onState(state);
    });

    const claimPublisher = Effect.fn("DocsWorkflow.claimPublisher")(function* (
      folderID: string,
      sessionID: string,
      onState: (state: DocsPublishState) => Effect.Effect<void>,
    ) {
      const adopted = yield* Ref.modify(
        publishRegistry,
        (registry): readonly [Option.Option<DocsPublishState>, DocsPublishRegistry] => {
          const surfaces = new Map(registry.surfaces).set(folderID, { sessionID, onState });
          const current = registry.entries.get(folderID);
          if (current === undefined) return [Option.none(), { entries: registry.entries, surfaces }];
          const entries = new Map(registry.entries);
          if (current.state.kind === "succeeded") {
            entries.delete(folderID);
            return [Option.none(), { entries, surfaces }];
          }
          entries.set(folderID, { state: current.state, targetSession: sessionID });
          return [Option.some(current.state), { entries, surfaces }];
        },
      );
      if (Option.isSome(adopted)) yield* onState(adopted.value);
    });

    const releasePublisher = (sessionID: string): Effect.Effect<void> =>
      Ref.update(publishRegistry, (registry) => {
        const surfaces = new Map(registry.surfaces);
        for (const [folderID, surface] of surfaces) {
          if (surface.sessionID === sessionID) surfaces.delete(folderID);
        }
        const entries = new Map(registry.entries);
        for (const [folderID, entry] of entries) {
          if (entry.targetSession !== sessionID) continue;
          if (entry.state.kind === "succeeded") entries.delete(folderID);
          else entries.set(folderID, { state: entry.state });
        }
        return { entries, surfaces };
      });

    const acknowledgePublisher = (sessionID: string): Effect.Effect<void> =>
      Ref.update(publishRegistry, (registry) => {
        const entries = new Map(registry.entries);
        for (const [folderID, entry] of entries) {
          const surface = registry.surfaces.get(folderID);
          if (
            entry.state.kind === "failed" &&
            (entry.targetSession === sessionID || surface?.sessionID === sessionID)
          ) {
            entries.delete(folderID);
          }
        }
        return { entries, surfaces: registry.surfaces };
      });

    const publish = Effect.fn("DocsWorkflow.publish")(function* <R>(submitted: DocsPublishCommand<R>) {
      const context = yield* Effect.context<R>();
      const submissionID = yield* Ref.getAndUpdate(publishSequence, (sequence) => sequence + 1);
      const request: DocsPublishRequest = {
        submissionID,
        folderID: submitted.folderID,
        message: submitted.message,
      };
      const pendingState = { kind: "pending", request } satisfies DocsPublishState;
      const observer = yield* Ref.modify(
        publishRegistry,
        (registry): readonly [Option.Option<DocsPublisherSurface>, DocsPublishRegistry] => {
          const surface = registry.surfaces.get(submitted.folderID);
          const entry: DocsPublishEntry = {
            state: pendingState,
            ...(surface === undefined ? {} : { targetSession: surface.sessionID }),
          };
          return [
            surface === undefined ? Option.none() : Option.some(surface),
            {
              entries: new Map(registry.entries).set(submitted.folderID, entry),
              surfaces: registry.surfaces,
            },
          ];
        },
      );
      if (Option.isSome(observer)) yield* observer.value.onState(pendingState);
      const command = Effect.exit(submitted.request.pipe(Effect.provide(context))).pipe(
        Effect.flatMap((result) => {
          if (Exit.isSuccess(result)) {
            return completePublisher(submitted.folderID, { kind: "succeeded", request, result: result.value });
          }
          const error = Cause.findErrorOption(result.cause);
          return Option.isSome(error)
            ? completePublisher(submitted.folderID, { kind: "failed", request, error: error.value })
            : Effect.failCause(result.cause).pipe(Effect.orDie);
        }),
      );
      const completion = yield* mutations.accept(command);
      yield* completion;
    });

    return {
      read,
      stop,
      reconcileMutation,
      claimPresenter,
      releasePresenter,
      present,
      mutate,
      claimPublisher,
      releasePublisher,
      acknowledgePublisher,
      publish,
    };
  },
);

export const DocsWorkflowLive = Layer.effect(DocsWorkflow)(makeDocsWorkflow);
