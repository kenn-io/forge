import {
  Cause,
  Context,
  Deferred,
  Effect,
  Exit,
  Fiber,
  FiberMap,
  Layer,
  Option,
  Ref,
  Schedule,
  Schema,
  Stream,
} from "effect";
import type { Scope } from "effect/Scope";
import { kataEventStream, KataEventStreamError } from "../../api/kata/eventStream.js";
import { KataMutationOutcomeUnknownError, KataMutationPartiallyAppliedError } from "../../api/kata/taskClient.js";
import { GeneratedApi } from "../../api/generated-api.js";
import { reconnectSchedule } from "../../api/retry-policy.js";
import { KataEventStreamOpened, type KataTaskEventStreamFrame } from "../../api/kata/schemas.js";
import type {
  KataSnapshotAPIError,
  KataSnapshotIntent,
  KataWorkspaceSnapshotResponse,
} from "../../api/kata/snapshot.js";
import {
  normalizeKataWorkspaceSnapshot,
  type KataWorkspaceSnapshotProjection,
} from "../../api/kata/snapshotProjection.js";
import type { TransientTransportError } from "../../api/effect-errors.js";
import type { CommandQueueClosed } from "../../effect/ordered-command-queue.js";
import { makeOrderedCommandQueue } from "../../effect/ordered-command-queue.js";
import type { KataAuthorityStore } from "../../stores/kata-authority.svelte.js";

export interface KataSnapshotReplacementResult {
  readonly replacementAccepted: boolean;
  readonly replacementError: string | null;
}

export interface KataMutationResult<A> {
  readonly acknowledgement: A;
  readonly replacement: Effect.Effect<KataSnapshotReplacementResult, CommandQueueClosed>;
}

export interface KataMutationIdentity {
  readonly key: string;
  readonly daemonId: string;
  readonly operation: string;
  readonly target: string;
}

export type KataMutationResolution = "applied" | "absent" | "ambiguous";

export type KataMutationFenceState =
  | { readonly kind: "unknown"; readonly identity: KataMutationIdentity; readonly message: string }
  | { readonly kind: "partial"; readonly identity: KataMutationIdentity; readonly message: string }
  | { readonly kind: "reconciling"; readonly identity: KataMutationIdentity }
  | {
      readonly kind: "resolved";
      readonly identity: KataMutationIdentity;
      readonly resolution: Exclude<KataMutationResolution, "ambiguous">;
    };

export interface KataMutationUncertainty {
  readonly identity: KataMutationIdentity;
  readonly baseline: KataWorkspaceSnapshotProjection;
  readonly readFresh: Effect.Effect<
    KataWorkspaceSnapshotResponse,
    KataSnapshotAPIError | TransientTransportError,
    GeneratedApi
  >;
  readonly isApplied?: ((snapshot: KataWorkspaceSnapshotProjection) => boolean) | undefined;
}

export interface KataEventConnectionOptions {
  readonly owner: string;
  readonly daemonId?: string | undefined;
  readonly checkpoint: number;
  readonly onOpen: Effect.Effect<void>;
  readonly onFrame: (frame: KataTaskEventStreamFrame) => Effect.Effect<void, never, GeneratedApi>;
  readonly onError: (error: KataEventStreamError) => Effect.Effect<void>;
}

export class KataMutationError extends Schema.TaggedErrorClass<KataMutationError>()("KataMutationError", {
  message: Schema.String,
  cause: Schema.Defect(),
}) {}

export class KataAuthorityError extends Schema.TaggedErrorClass<KataAuthorityError>()("KataAuthorityError", {
  message: Schema.String,
  cause: Schema.Defect(),
}) {}

export class KataMutationBlocked extends Schema.TaggedErrorClass<KataMutationBlocked>()("KataMutationBlocked", {
  key: Schema.String,
  operation: Schema.String,
}) {}

export class KataMutationReconciliationError extends Schema.TaggedErrorClass<KataMutationReconciliationError>()(
  "KataMutationReconciliationError",
  {
    key: Schema.String,
    operation: Schema.String,
    message: Schema.String,
    cause: Schema.Defect(),
  },
) {}

export interface KataWorkflowService {
  readonly latestSnapshot: <E, R, AcceptanceR = never>(
    owner: string,
    store: KataAuthorityStore,
    intent: KataSnapshotIntent,
    read: Effect.Effect<KataWorkspaceSnapshotResponse, E, R>,
    onAccepted?: Effect.Effect<void, never, AcceptanceR> | undefined,
  ) => Effect.Effect<boolean, E | KataAuthorityError, R | AcceptanceR>;
  readonly interruptAuthority: (owner: string) => Effect.Effect<void>;
  readonly connectEvents: (options: KataEventConnectionOptions) => Effect.Effect<void, never, GeneratedApi>;
  readonly updateEventSource: (owner: string, daemonId: string | undefined, checkpoint: number) => Effect.Effect<void>;
  readonly disconnectEvents: (owner: string) => Effect.Effect<void>;
  readonly claimMutation: (
    key: string,
    owner: string,
    onState: (state: KataMutationFenceState) => Effect.Effect<void>,
  ) => Effect.Effect<void>;
  readonly releaseMutation: (owner: string) => Effect.Effect<void>;
  readonly acknowledgeMutation: (key: string) => Effect.Effect<void>;
  readonly reconcileMutation: (
    key: string,
  ) => Effect.Effect<KataMutationResolution, KataMutationReconciliationError, GeneratedApi>;
  readonly mutateAndRevalidate: <A, MutationError, RevalidationError, R>(
    mutation: Effect.Effect<A, MutationError>,
    revalidation: Effect.Effect<boolean, RevalidationError, R>,
    onAcknowledged?: ((acknowledgement: A) => Effect.Effect<void>) | undefined,
    uncertainty?: KataMutationUncertainty | undefined,
  ) => Effect.Effect<KataMutationResult<A>, MutationError | KataMutationBlocked | CommandQueueClosed, R>;
}

export class KataWorkflow extends Context.Service<KataWorkflow, KataWorkflowService>()("kenn-forge/KataWorkflow") {}

function errorMessage(error: unknown): string {
  return error instanceof Error ? error.message : String(error);
}

function replacementFailure<Error>(cause: Cause.Cause<Error>): KataSnapshotReplacementResult["replacementError"] {
  const failure = Cause.findErrorOption(cause);
  return Option.isSome(failure) ? errorMessage(failure.value) : Cause.pretty(cause);
}

function retainedMutationCause(
  failure: unknown,
): KataMutationOutcomeUnknownError | KataMutationPartiallyAppliedError | undefined {
  if (failure instanceof KataMutationOutcomeUnknownError || failure instanceof KataMutationPartiallyAppliedError) {
    return failure;
  }
  if (failure instanceof KataMutationError) {
    return retainedMutationCause(failure.cause);
  }
  return undefined;
}

function snapshotEvidence(snapshot: KataWorkspaceSnapshotProjection): string {
  return JSON.stringify({
    daemonId: snapshot.daemon_id,
    intent: snapshot.intent,
    projects: snapshot.projects,
    memberIssueUIDs: snapshot.member_issue_uids,
    issues: snapshot.issues,
    selectedIssueUID: snapshot.selected_issue_uid,
    selectedDetail: snapshot.selected_detail,
    selectedHistory: snapshot.selected_history,
    graphSourceUID: snapshot.graph_source_uid,
    graph: snapshot.graph,
    enrichmentErrors: snapshot.enrichment_errors,
  });
}

interface KataMutationFence {
  readonly uncertainty: KataMutationUncertainty;
  readonly cause: KataMutationOutcomeUnknownError | KataMutationPartiallyAppliedError;
  readonly state: KataMutationFenceState;
}

interface KataMutationSurface {
  readonly key: string;
  readonly owner: string;
  readonly onState: (state: KataMutationFenceState) => Effect.Effect<void>;
}

export const makeKataWorkflow: Effect.Effect<KataWorkflowService, never, Scope> = Effect.gen(function* () {
  const authorityFibers = yield* FiberMap.make<string, boolean, unknown>();
  const eventFibers = yield* FiberMap.make<string, void, never>();
  const eventSources = yield* Ref.make<
    ReadonlyMap<string, { readonly daemonId?: string | undefined; readonly checkpoint: number }>
  >(new Map());
  const mutationFences = yield* Ref.make<ReadonlyMap<string, KataMutationFence>>(new Map());
  const mutationSurfaces = yield* Ref.make<ReadonlyMap<string, KataMutationSurface>>(new Map());
  const commands = yield* makeOrderedCommandQueue("Kata mutations", (command: Effect.Effect<void>) => command, 64);

  const publishMutationState = Effect.fn("KataWorkflow.publishMutationState")(function* (
    key: string,
    state: KataMutationFenceState,
  ) {
    const observers = Array.from((yield* Ref.get(mutationSurfaces)).values()).filter((surface) => surface.key === key);
    yield* Effect.forEach(observers, (surface) => surface.onState(state), { discard: true });
  });

  const setMutationFence = Effect.fn("KataWorkflow.setMutationFence")(function* (fence: KataMutationFence) {
    yield* Ref.update(mutationFences, (fences) => new Map(fences).set(fence.uncertainty.identity.key, fence));
    yield* publishMutationState(fence.uncertainty.identity.key, fence.state);
  });

  const claimMutation = Effect.fn("KataWorkflow.claimMutation")(function* (
    key: string,
    owner: string,
    onState: (state: KataMutationFenceState) => Effect.Effect<void>,
  ) {
    yield* Ref.update(mutationSurfaces, (surfaces) => new Map(surfaces).set(owner, { key, owner, onState }));
    const fence = (yield* Ref.get(mutationFences)).get(key);
    if (fence !== undefined) yield* onState(fence.state);
  });

  const releaseMutation = (owner: string): Effect.Effect<void> =>
    Ref.update(mutationSurfaces, (surfaces) => {
      const next = new Map(surfaces);
      next.delete(owner);
      return next;
    });

  const acknowledgeMutation = Effect.fn("KataWorkflow.acknowledgeMutation")(function* (key: string) {
    const fence = (yield* Ref.get(mutationFences)).get(key);
    if (fence === undefined) return;
    yield* Ref.update(mutationFences, (fences) => {
      const next = new Map(fences);
      next.delete(key);
      return next;
    });
    yield* publishMutationState(key, {
      kind: "resolved",
      identity: fence.uncertainty.identity,
      resolution: "applied",
    });
  });

  const reconcileMutation = Effect.fn("KataWorkflow.reconcileMutation")(function* (key: string) {
    const fence = (yield* Ref.get(mutationFences)).get(key);
    if (fence === undefined) {
      const resolution: KataMutationResolution = "absent";
      return resolution;
    }
    if (fence.state.kind === "partial") {
      yield* publishMutationState(key, fence.state);
      return "ambiguous";
    }
    const reconciling = { kind: "reconciling", identity: fence.uncertainty.identity } satisfies KataMutationFenceState;
    yield* setMutationFence({ ...fence, state: reconciling });
    const freshExit = yield* Effect.exit(fence.uncertainty.readFresh);
    if (Exit.isFailure(freshExit)) {
      const failure = Cause.findErrorOption(freshExit.cause);
      const cause = Option.isSome(failure) ? failure.value : freshExit.cause;
      const message = Option.isSome(failure) ? errorMessage(failure.value) : Cause.pretty(freshExit.cause);
      yield* setMutationFence({
        ...fence,
        state: { kind: "unknown", identity: fence.uncertainty.identity, message },
      });
      return yield* Effect.fail(
        KataMutationReconciliationError.make({
          key,
          operation: fence.uncertainty.identity.operation,
          message,
          cause,
        }),
      );
    }
    const fresh = normalizeKataWorkspaceSnapshot(freshExit.value);
    const isFresh =
      fresh.daemon_id === fence.uncertainty.identity.daemonId &&
      fresh.invalidation_epoch > fence.uncertainty.baseline.invalidation_epoch;
    const resolution: KataMutationResolution = !isFresh
      ? "ambiguous"
      : fence.uncertainty.isApplied?.(fresh)
        ? "applied"
        : snapshotEvidence(fresh) === snapshotEvidence(fence.uncertainty.baseline)
          ? "absent"
          : "ambiguous";
    if (resolution === "ambiguous") {
      yield* setMutationFence({
        ...fence,
        state: {
          kind: "unknown",
          identity: fence.uncertainty.identity,
          message: "Fresh Kata authority could not prove whether the change was applied.",
        },
      });
      return resolution;
    }
    yield* Ref.update(mutationFences, (fences) => {
      const next = new Map(fences);
      next.delete(key);
      return next;
    });
    yield* publishMutationState(key, { kind: "resolved", identity: fence.uncertainty.identity, resolution });
    return resolution;
  });

  const latestSnapshot = <E, R, AcceptanceR = never>(
    owner: string,
    store: KataAuthorityStore,
    requestedIntent: KataSnapshotIntent,
    read: Effect.Effect<KataWorkspaceSnapshotResponse, E, R>,
    onAccepted?: Effect.Effect<void, never, AcceptanceR> | undefined,
  ): Effect.Effect<boolean, E | KataAuthorityError, R | AcceptanceR> => {
    const program = Effect.gen(function* () {
      const intent = yield* Effect.try({
        try: () => store.beginLoad(requestedIntent),
        catch: (cause) => KataAuthorityError.make({ message: errorMessage(cause), cause }),
      });
      const response = yield* read.pipe(
        Effect.tapError((failure) => Effect.sync(() => store.failSnapshot(intent, failure))),
      );
      const accepted = yield* Effect.try({
        try: () => store.acceptSnapshot(intent, response),
        catch: (cause) => KataAuthorityError.make({ message: errorMessage(cause), cause }),
      });
      if (accepted && onAccepted) yield* onAccepted;
      return accepted;
    });
    return FiberMap.run(authorityFibers, owner, program).pipe(Effect.flatMap(Fiber.join));
  };

  const eventRetrySchedule = reconnectSchedule.pipe(
    Schedule.while(({ input }) => input instanceof KataEventStreamError && input.retryable),
  );

  const connectEvents = Effect.fn("KataWorkflow.connectEvents")(function* (options: KataEventConnectionOptions) {
    yield* Ref.update(eventSources, (sources) => {
      const next = new Map(sources);
      next.set(options.owner, { daemonId: options.daemonId, checkpoint: options.checkpoint });
      return next;
    });
    const stream = Stream.unwrap(
      Ref.get(eventSources).pipe(
        Effect.flatMap((sources) => {
          const source = sources.get(options.owner);
          return source === undefined
            ? Effect.die(new Error(`missing Kata event source for ${options.owner}`))
            : Effect.succeed(kataEventStream({ daemonId: source.daemonId, lastEventID: source.checkpoint }));
        }),
      ),
    ).pipe(
      Stream.tap((event) => (event instanceof KataEventStreamOpened ? options.onOpen : options.onFrame(event))),
      Stream.tapError(options.onError),
      Stream.retry(eventRetrySchedule),
    );
    const consume = Stream.runDrain(stream).pipe(Effect.catch(() => Effect.void));
    yield* FiberMap.run(eventFibers, options.owner, consume);
  });

  const mutateAndRevalidate = Effect.fn("KataWorkflow.mutateAndRevalidate")(function* <
    A,
    MutationError,
    RevalidationError,
    R,
  >(
    mutation: Effect.Effect<A, MutationError>,
    revalidation: Effect.Effect<boolean, RevalidationError, R>,
    onAcknowledged?: ((acknowledgement: A) => Effect.Effect<void>) | undefined,
    uncertainty?: KataMutationUncertainty | undefined,
  ) {
    const revalidationContext = yield* Effect.context<R>();
    const acknowledgement = yield* Deferred.make<Exit.Exit<A, MutationError | KataMutationBlocked>>();
    const replacement = yield* Deferred.make<KataSnapshotReplacementResult>();
    const command = Effect.gen(function* () {
      if (uncertainty !== undefined && (yield* Ref.get(mutationFences)).has(uncertainty.identity.key)) {
        yield* Deferred.succeed(
          acknowledgement,
          Exit.fail(
            KataMutationBlocked.make({
              key: uncertainty.identity.key,
              operation: uncertainty.identity.operation,
            }),
          ),
        );
        return;
      }
      const mutationExit = yield* Effect.exit(mutation);
      if (Exit.isFailure(mutationExit) && uncertainty !== undefined) {
        const failure = Cause.findErrorOption(mutationExit.cause);
        if (Option.isSome(failure)) {
          const retainedCause = retainedMutationCause(failure.value);
          if (retainedCause instanceof KataMutationOutcomeUnknownError) {
            yield* setMutationFence({
              uncertainty,
              cause: retainedCause,
              state: { kind: "unknown", identity: uncertainty.identity, message: retainedCause.message },
            });
          } else if (retainedCause instanceof KataMutationPartiallyAppliedError) {
            yield* setMutationFence({
              uncertainty,
              cause: retainedCause,
              state: { kind: "partial", identity: uncertainty.identity, message: retainedCause.message },
            });
          }
        }
      }
      if (Exit.isFailure(mutationExit)) {
        yield* Deferred.succeed(acknowledgement, mutationExit);
        return;
      }
      if (onAcknowledged !== undefined) yield* onAcknowledged(mutationExit.value);
      yield* Deferred.succeed(acknowledgement, mutationExit);
      const replacementExit = yield* Effect.exit(revalidation.pipe(Effect.provide(revalidationContext)));
      yield* Deferred.succeed(replacement, {
        replacementAccepted: Exit.isSuccess(replacementExit) && replacementExit.value,
        replacementError: Exit.isFailure(replacementExit)
          ? replacementFailure(replacementExit.cause)
          : replacementExit.value
            ? null
            : "Kata snapshot replacement was not accepted.",
      });
    });
    const awaitCompletion = yield* commands.accept(command);
    const accepted = yield* Deferred.await(acknowledgement).pipe(Effect.flatMap((exit) => exit));
    return {
      acknowledgement: accepted,
      replacement: awaitCompletion.pipe(Effect.andThen(Deferred.await(replacement))),
    };
  });

  return {
    latestSnapshot,
    interruptAuthority: (owner) => FiberMap.remove(authorityFibers, owner),
    connectEvents,
    updateEventSource: (owner, daemonId, checkpoint) =>
      Ref.update(eventSources, (sources) => {
        const next = new Map(sources);
        next.set(owner, { daemonId, checkpoint });
        return next;
      }),
    disconnectEvents: (owner) =>
      FiberMap.remove(eventFibers, owner).pipe(
        Effect.andThen(
          Ref.update(eventSources, (sources) => {
            const next = new Map(sources);
            next.delete(owner);
            return next;
          }),
        ),
      ),
    claimMutation,
    releaseMutation,
    acknowledgeMutation,
    reconcileMutation,
    mutateAndRevalidate,
  };
});

export const KataWorkflowLive = Layer.effect(KataWorkflow)(makeKataWorkflow);
