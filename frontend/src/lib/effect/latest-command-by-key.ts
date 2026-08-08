import { Cause, Deferred, Effect, Exit, FiberMap, Option, Ref, Semaphore } from "effect";
import type { Scope } from "effect/Scope";
import { CommandQueueClosed } from "./ordered-command-queue.js";

interface CommandEntry<Error> {
  readonly command: Effect.Effect<void, Error>;
  readonly acknowledgement: Deferred.Deferred<Exit.Exit<void, Error | CommandQueueClosed>>;
}

interface KeyState<Error> {
  readonly pending: Option.Option<CommandEntry<Error>>;
}

interface AcceptResult<Error> {
  readonly startWorker: boolean;
  readonly superseded: Option.Option<CommandEntry<Error>>;
}

export interface LatestCommandByKey<Error> {
  readonly submit: (
    key: string,
    command: Effect.Effect<void, Error>,
  ) => Effect.Effect<void, Error | CommandQueueClosed>;
  readonly shutdown: Effect.Effect<void>;
}

export function makeLatestCommandByKey<Error>(
  name: string,
  blocksPending: (failure: Error) => boolean = () => false,
): Effect.Effect<LatestCommandByKey<Error>, never, Scope> {
  return Effect.gen(function* () {
    const states = yield* Ref.make<ReadonlyMap<string, KeyState<Error>>>(new Map());
    const closed = yield* Ref.make(false);
    const acceptance = yield* Semaphore.make(1);
    const workers = yield* FiberMap.make<string, void, never>();
    const acknowledgements = new Set<Deferred.Deferred<Exit.Exit<void, Error | CommandQueueClosed>>>();

    const takeNext = (key: string) =>
      acceptance.withPermit(
        Ref.modify(
          states,
          (current): readonly [Option.Option<CommandEntry<Error>>, ReadonlyMap<string, KeyState<Error>>] => {
            const state = current.get(key);
            if (state === undefined) return [Option.none(), current];
            const next = new Map(current);
            if (Option.isNone(state.pending)) {
              next.delete(key);
              return [Option.none(), next];
            }
            next.set(key, { pending: Option.none() });
            return [state.pending, next];
          },
        ),
      );

    const failPending = (key: string, failure: Error) =>
      acceptance.withPermit(
        Effect.gen(function* () {
          const pending = yield* Ref.modify(
            states,
            (current): readonly [Option.Option<CommandEntry<Error>>, ReadonlyMap<string, KeyState<Error>>] => {
              const entry = current.get(key)?.pending ?? Option.none();
              const next = new Map(current);
              next.delete(key);
              return [entry, next];
            },
          );
          if (Option.isNone(pending)) return;
          acknowledgements.delete(pending.value.acknowledgement);
          yield* Deferred.succeed(pending.value.acknowledgement, Exit.fail(failure));
        }),
      );

    function consume(key: string): Effect.Effect<void> {
      return Effect.suspend(() =>
        takeNext(key).pipe(
          Effect.flatMap((entry) => {
            if (Option.isNone(entry)) return Effect.void;
            return Effect.exit(entry.value.command).pipe(
              Effect.tap((exit) => Deferred.succeed(entry.value.acknowledgement, exit)),
              Effect.tap(() => Effect.sync(() => acknowledgements.delete(entry.value.acknowledgement))),
              Effect.flatMap((exit) => {
                if (Exit.isFailure(exit)) {
                  const failure = Cause.findErrorOption(exit.cause);
                  if (Option.isSome(failure) && blocksPending(failure.value)) {
                    return failPending(key, failure.value);
                  }
                }
                return consume(key);
              }),
            );
          }),
        ),
      );
    }

    const submit = Effect.fn("LatestCommandByKey.submit")(function* (key: string, command: Effect.Effect<void, Error>) {
      const acknowledgement = yield* Deferred.make<Exit.Exit<void, Error | CommandQueueClosed>>();
      yield* acceptance.withPermit(
        Effect.gen(function* () {
          if (yield* Ref.get(closed)) {
            return yield* Effect.fail(new CommandQueueClosed({ queue: name }));
          }
          acknowledgements.add(acknowledgement);
          const entry: CommandEntry<Error> = { command, acknowledgement };
          const accepted = yield* Ref.modify(
            states,
            (current): readonly [AcceptResult<Error>, ReadonlyMap<string, KeyState<Error>>] => {
              const existing = current.get(key);
              const next = new Map(current);
              next.set(key, { pending: Option.some(entry) });
              return [
                {
                  startWorker: existing === undefined,
                  superseded: existing?.pending ?? Option.none(),
                },
                next,
              ];
            },
          );
          if (Option.isSome(accepted.superseded)) {
            acknowledgements.delete(accepted.superseded.value.acknowledgement);
            yield* Deferred.succeed(accepted.superseded.value.acknowledgement, Exit.succeed(undefined));
          }
          if (accepted.startWorker) yield* FiberMap.run(workers, key, consume(key));
        }),
      );
      return yield* Deferred.await(acknowledgement).pipe(Effect.flatMap((exit) => exit));
    });

    const shutdown = Effect.gen(function* () {
      const shouldClose = yield* acceptance.withPermit(
        Ref.modify(closed, (isClosed): readonly [boolean, boolean] => [!isClosed, true]),
      );
      if (!shouldClose) return;
      yield* FiberMap.clear(workers);
      const exit = Exit.fail(new CommandQueueClosed({ queue: name }));
      yield* Effect.forEach(
        Array.from(acknowledgements),
        (acknowledgement) => Deferred.succeed(acknowledgement, exit),
        { discard: true },
      );
      acknowledgements.clear();
      yield* Ref.set(states, new Map());
    });
    yield* Effect.addFinalizer(() => shutdown);

    return { submit, shutdown };
  });
}
