import { Context, Data, Deferred, Effect, Exit, FiberSet, Layer, Option, Ref, Semaphore } from "effect";
import { ApiProblemError, TransientTransportError } from "../api/effect-errors.js";
import { GeneratedApi } from "../api/generated-api.js";
import type { ProblemBody } from "../api/problems.js";
import { problemConflictReason } from "../api/problems.js";
import { CommandQueueClosed } from "../effect/ordered-command-queue.js";

export type ProviderMutationError = ApiProblemError | TransientTransportError;

export class MutationNeedsReview extends Data.TaggedError("MutationNeedsReview")<{
  readonly operation: string;
  readonly problem: ProblemBody;
  readonly refreshFailure?: ProviderMutationError;
}> {}

export type ProviderMutationFailure = ProviderMutationError | MutationNeedsReview | CommandQueueClosed;

export interface MutationCallbacks {
  readonly onSuccess?: () => void;
  readonly onFailure?: (message: string) => void;
  readonly onSettled?: () => void;
}

export const invokeMutationCallback = (callback: (() => void) | undefined): Effect.Effect<void> =>
  callback === undefined ? Effect.void : Effect.sync(callback).pipe(Effect.catchCause(() => Effect.void));

export function invokeMutationFailure(callback: ((message: string) => void) | undefined, message: string): void {
  try {
    callback?.(message);
  } catch {
    // Presentation callbacks must not change the mutation acknowledgement.
  }
}

export function providerMutationFailureMessage(failure: ProviderMutationFailure, fallback: string): string {
  switch (failure._tag) {
    case "ApiProblemError":
      return failure.problem.detail ?? failure.problem.title ?? fallback;
    case "MutationNeedsReview":
      if (failure.refreshFailure !== undefined) {
        return `${failure.problem.detail ?? fallback}. The latest state could not be refreshed; review it before trying again.`;
      }
      return `${failure.problem.detail ?? fallback}. Refresh completed; review the latest state before trying again.`;
    case "CommandQueueClosed":
      return "The provider mutation queue closed before the change completed.";
    case "TransientTransportError":
      return "Could not reach Kenn Forge";
  }
}

export function providerMutationProblem(failure: ProviderMutationFailure): ProblemBody | undefined {
  switch (failure._tag) {
    case "ApiProblemError":
      return failure.problem;
    case "MutationNeedsReview":
      return failure.problem;
    case "CommandQueueClosed":
    case "TransientTransportError":
      return undefined;
  }
}

export interface VersionedCommand<A, Requirements = never> {
  readonly key: string;
  readonly baseline: A;
  readonly optimistic: A;
  readonly apply: (value: A) => Effect.Effect<void>;
  readonly commit: Effect.Effect<A, ProviderMutationError, Requirements>;
  readonly refreshOnStale: Effect.Effect<A, ProviderMutationError, Requirements>;
}

export interface OrderedMutations<A> {
  readonly submit: (
    command: VersionedCommand<A>,
  ) => Effect.Effect<A, ProviderMutationError | MutationNeedsReview | CommandQueueClosed>;
  readonly rebase: (key: string, confirmed: A) => Effect.Effect<void>;
  readonly rebaseAll: (
    authoritative: Effect.Effect<boolean>,
    entries: ReadonlyArray<{ readonly key: string; readonly confirmed: A }>,
  ) => Effect.Effect<boolean>;
  readonly shutdown: Effect.Effect<void>;
}

interface AcceptedCommand<A> extends VersionedCommand<A> {
  readonly version: number;
}

interface MutationKeyState<A> {
  readonly confirmed: A;
  readonly nextVersion: number;
  readonly pending: ReadonlyMap<number, AcceptedCommand<A>>;
  readonly blockedAfter?: {
    readonly version: number;
    readonly operation: string;
    readonly problem: ProblemBody;
    readonly refreshFailure?: ProviderMutationError;
  };
}

function staleProblem(failure: ProviderMutationError): ProblemBody | undefined {
  if (failure._tag !== "ApiProblemError") return undefined;
  return problemConflictReason(failure.problem) === "stale_state" ? failure.problem : undefined;
}

export function makeOrderedMutations<A>(
  name: string,
  capacity = 64,
): Effect.Effect<OrderedMutations<A>, never, import("effect/Scope").Scope> {
  return Effect.gen(function* () {
    const states = yield* Ref.make<ReadonlyMap<string, MutationKeyState<A>>>(new Map());
    const acceptance = yield* Semaphore.make(1);
    const stateTransitions = yield* Semaphore.make(1);
    const capacityPermits = yield* Semaphore.make(capacity);
    const closed = yield* Ref.make(false);
    const shutdownSignal = yield* Deferred.make<void>();
    const workers = yield* FiberSet.make<void, never>();
    const tails = new Map<string, Deferred.Deferred<void>>();
    const pendingAcknowledgements = new Set<
      Deferred.Deferred<Exit.Exit<A, ProviderMutationError | MutationNeedsReview | CommandQueueClosed>>
    >();

    const settle = Effect.fn("OrderedMutations.settle")(function* (
      command: AcceptedCommand<A>,
      confirmed: Option.Option<A>,
    ) {
      yield* stateTransitions.withPermit(
        Effect.gen(function* () {
          const projection = yield* Ref.modify(states, (currentStates) => {
            const current = currentStates.get(command.key) ?? {
              confirmed: command.baseline,
              nextVersion: command.version,
              pending: new Map([[command.version, command]]),
            };
            const nextConfirmed = Option.isSome(confirmed) ? confirmed.value : current.confirmed;
            const pending = new Map(current.pending);
            pending.delete(command.version);
            const latest = Array.from(pending.values()).sort((left, right) => right.version - left.version)[0];
            const nextStates = new Map(currentStates);
            if (pending.size === 0) {
              nextStates.delete(command.key);
            } else {
              nextStates.set(command.key, { ...current, confirmed: nextConfirmed, pending });
            }
            return [latest?.optimistic ?? nextConfirmed, nextStates];
          });
          yield* command.apply(projection);
        }),
      );
    });

    const rebaseAll = Effect.fn("OrderedMutations.rebaseAll")(function* (
      authoritative: Effect.Effect<boolean>,
      entries: ReadonlyArray<{ readonly key: string; readonly confirmed: A }>,
    ) {
      return yield* stateTransitions.withPermit(
        Effect.gen(function* () {
          if (!(yield* authoritative)) return false;
          const projections = yield* Ref.modify(states, (currentStates) => {
            const nextStates = new Map(currentStates);
            const nextProjections: Array<AcceptedCommand<A>> = [];
            for (const entry of entries) {
              const current = nextStates.get(entry.key);
              if (current === undefined || current.pending.size === 0) continue;
              const latest = Array.from(current.pending.values()).sort(
                (left, right) => right.version - left.version,
              )[0];
              if (latest !== undefined) nextProjections.push(latest);
              nextStates.set(entry.key, { ...current, confirmed: entry.confirmed });
            }
            return [nextProjections, nextStates];
          });
          yield* Effect.forEach(projections, (projection) => projection.apply(projection.optimistic), {
            discard: true,
          });
          return true;
        }),
      );
    });

    const rebase = Effect.fn("OrderedMutations.rebase")(function* (key: string, confirmed: A) {
      yield* rebaseAll(Effect.succeed(true), [{ key, confirmed }]).pipe(Effect.asVoid);
    });

    const markBlocked = Effect.fn("OrderedMutations.markBlocked")(function* (
      command: AcceptedCommand<A>,
      operation: string,
      problem: ProblemBody,
      refreshFailure?: ProviderMutationError,
    ) {
      yield* Ref.update(states, (currentStates) => {
        const current = currentStates.get(command.key);
        if (current === undefined) return currentStates;
        return new Map(currentStates).set(command.key, {
          ...current,
          blockedAfter: {
            version: command.version,
            operation,
            problem,
            ...(refreshFailure !== undefined && { refreshFailure }),
          },
        });
      });
    });

    const execute = Effect.fn("OrderedMutations.execute")(function* (
      command: AcceptedCommand<A>,
    ): Effect.fn.Return<A, ProviderMutationError | MutationNeedsReview> {
      const blocked = yield* Ref.get(states).pipe(
        Effect.map((currentStates) => currentStates.get(command.key)?.blockedAfter),
      );
      if (blocked !== undefined && command.version > blocked.version) {
        yield* settle(command, Option.none());
        return yield* Effect.fail(
          new MutationNeedsReview({
            operation: blocked.operation,
            problem: blocked.problem,
            ...(blocked.refreshFailure !== undefined && { refreshFailure: blocked.refreshFailure }),
          }),
        );
      }

      return yield* command.commit.pipe(
        Effect.tap((confirmed) => settle(command, Option.some(confirmed))),
        Effect.catch((failure): Effect.Effect<never, ProviderMutationError | MutationNeedsReview> => {
          const problem = staleProblem(failure);
          if (problem !== undefined) {
            return markBlocked(command, failure.operation, problem).pipe(
              Effect.andThen(command.refreshOnStale),
              Effect.matchEffect({
                onFailure: (refreshFailure) =>
                  markBlocked(command, failure.operation, problem, refreshFailure).pipe(
                    Effect.andThen(settle(command, Option.none())),
                    Effect.andThen(
                      Effect.fail(new MutationNeedsReview({ operation: failure.operation, problem, refreshFailure })),
                    ),
                  ),
                onSuccess: (refreshed) =>
                  settle(command, Option.some(refreshed)).pipe(
                    Effect.andThen(Effect.fail(new MutationNeedsReview({ operation: failure.operation, problem }))),
                  ),
              }),
            );
          }
          return settle(command, Option.none()).pipe(Effect.andThen(Effect.fail(failure)));
        }),
      );
    });

    const submit = Effect.fn("OrderedMutations.submit")(function* (command: VersionedCommand<A>) {
      const queueClosed = () => new CommandQueueClosed({ queue: name });
      yield* Effect.raceFirst(
        capacityPermits.take(1),
        Deferred.await(shutdownSignal).pipe(Effect.andThen(Effect.fail(queueClosed()))),
      );
      const accepted = yield* acceptance.withPermit(
        Effect.gen(function* () {
          if (yield* Ref.get(closed)) {
            yield* capacityPermits.release(1);
            return yield* Effect.fail(queueClosed());
          }
          const acknowledgement =
            yield* Deferred.make<Exit.Exit<A, ProviderMutationError | MutationNeedsReview | CommandQueueClosed>>();
          const gate = yield* Deferred.make<void>();
          const previous = tails.get(command.key);
          tails.set(command.key, gate);
          pendingAcknowledgements.add(acknowledgement);
          const versioned = yield* stateTransitions.withPermit(
            Effect.gen(function* () {
              const accepted = yield* Ref.modify(states, (currentStates) => {
                const current = currentStates.get(command.key);
                const version = (current?.nextVersion ?? 0) + 1;
                const accepted: AcceptedCommand<A> = { ...command, version };
                const pending = new Map(current?.pending ?? []);
                pending.set(version, accepted);
                return [
                  accepted,
                  new Map(currentStates).set(command.key, {
                    confirmed: current?.confirmed ?? command.baseline,
                    nextVersion: version,
                    pending,
                  }),
                ];
              });
              yield* accepted.apply(accepted.optimistic);
              return accepted;
            }),
          );
          const worker = (previous === undefined ? Effect.void : Deferred.await(previous)).pipe(
            Effect.andThen(
              Effect.gen(function* () {
                if (yield* Ref.get(closed)) return yield* Effect.fail(queueClosed());
                return yield* execute(versioned);
              }),
            ),
            Effect.exit,
            Effect.flatMap((exit) => Deferred.succeed(acknowledgement, exit)),
            Effect.onInterrupt(() => Deferred.succeed(acknowledgement, Exit.fail(queueClosed()))),
            Effect.ensuring(
              Effect.gen(function* () {
                yield* Deferred.succeed(gate, undefined);
                yield* capacityPermits.release(1);
                pendingAcknowledgements.delete(acknowledgement);
                if (tails.get(command.key) === gate) tails.delete(command.key);
              }),
            ),
            Effect.asVoid,
          );
          yield* FiberSet.run(workers, worker);
          return acknowledgement;
        }),
      );
      return yield* Deferred.await(accepted).pipe(Effect.flatMap((exit) => exit));
    });

    const shutdown = Effect.gen(function* () {
      const shouldClose = yield* Ref.modify(closed, (isClosed): readonly [boolean, boolean] => [!isClosed, true]);
      if (!shouldClose) return;
      yield* Deferred.succeed(shutdownSignal, undefined);
      yield* FiberSet.clear(workers);
      const exit = Exit.fail(new CommandQueueClosed({ queue: name }));
      yield* Effect.forEach(
        Array.from(pendingAcknowledgements),
        (acknowledgement) => Deferred.succeed(acknowledgement, exit),
        { discard: true },
      );
      pendingAcknowledgements.clear();
      tails.clear();
      const currentStates = yield* Ref.getAndSet(states, new Map());
      yield* Effect.forEach(
        currentStates.values(),
        (state) => {
          const latest = Array.from(state.pending.values()).sort((left, right) => right.version - left.version)[0];
          return latest === undefined ? Effect.void : latest.apply(state.confirmed);
        },
        { discard: true },
      );
    });
    yield* Effect.addFinalizer(() => shutdown);

    return {
      submit,
      rebase,
      rebaseAll,
      shutdown,
    };
  });
}

export interface ProviderMutationService {
  readonly submit: <A>(
    command: VersionedCommand<A, GeneratedApi>,
  ) => Effect.Effect<void, ProviderMutationError | MutationNeedsReview | CommandQueueClosed>;
  readonly rebase: <A>(key: string, confirmed: A, apply: (value: A) => Effect.Effect<void>) => Effect.Effect<void>;
  readonly rebaseAll: (
    authoritative: Effect.Effect<boolean>,
    entries: ReadonlyArray<{ readonly key: string; readonly confirmed: Effect.Effect<void> }>,
  ) => Effect.Effect<boolean>;
  readonly shutdown: Effect.Effect<void>;
}

export const makeProviderMutations = (
  name: string,
  capacity = 64,
): Effect.Effect<ProviderMutationService, never, GeneratedApi | import("effect/Scope").Scope> =>
  Effect.gen(function* () {
    const api = yield* GeneratedApi;
    const mutations = yield* makeOrderedMutations<Effect.Effect<void>>(name, capacity);
    return {
      submit: <A>(command: VersionedCommand<A, GeneratedApi>) =>
        mutations
          .submit({
            key: command.key,
            baseline: command.apply(command.baseline),
            optimistic: command.apply(command.optimistic),
            apply: (projection) => projection,
            commit: command.commit.pipe(Effect.provideService(GeneratedApi, api), Effect.map(command.apply)),
            refreshOnStale: command.refreshOnStale.pipe(
              Effect.provideService(GeneratedApi, api),
              Effect.map(command.apply),
            ),
          })
          .pipe(Effect.asVoid),
      rebase: <A>(key: string, confirmed: A, apply: (value: A) => Effect.Effect<void>) =>
        mutations.rebase(key, apply(confirmed)),
      rebaseAll: (authoritative, entries) => mutations.rebaseAll(authoritative, entries),
      shutdown: mutations.shutdown,
    };
  });

export class ProviderMutations extends Context.Service<ProviderMutations, ProviderMutationService>()(
  "kenn-forge/ProviderMutations",
) {}

export const ProviderMutationsLive = Layer.effect(ProviderMutations)(makeProviderMutations("provider mutations"));
