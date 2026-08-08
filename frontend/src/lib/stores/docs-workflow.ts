import { Cause, Context, Deferred, Effect, Exit, Fiber, FiberMap, Layer, Option, Ref } from "effect";
import type { DocsRequestError } from "../api/docs/api.js";
import type { GitPublishResponse } from "../api/docs/types.js";
import type { CommandQueueClosed } from "../effect/ordered-command-queue.js";
import { makeOrderedCommandQueue } from "../effect/ordered-command-queue.js";

export interface DocsWorkflowService {
  readonly read: <A, E, R>(owner: string, key: string, request: Effect.Effect<A, E, R>) => Effect.Effect<A, E, R>;
  readonly stop: (owner: string) => Effect.Effect<void>;
  readonly mutate: <A, E, R>(mutation: Effect.Effect<A, E, R>) => Effect.Effect<A, E | CommandQueueClosed, R>;
  readonly claimPublisher: (
    folderID: string,
    sessionID: string,
    onState: (state: DocsPublishState) => Effect.Effect<void>,
  ) => Effect.Effect<void>;
  readonly releasePublisher: (sessionID: string) => Effect.Effect<void>;
  readonly acknowledgePublisher: (sessionID: string) => Effect.Effect<void>;
  readonly publish: <R>(command: DocsPublishCommand<R>) => Effect.Effect<void, CommandQueueClosed, R>;
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

export class DocsWorkflow extends Context.Service<DocsWorkflow, DocsWorkflowService>()("kenn-forge/DocsWorkflow") {}

let nextDocsOwner = 0;

export function makeDocsOwner(prefix: string): string {
  nextDocsOwner += 1;
  return `${prefix}:${nextDocsOwner}`;
}

export const makeDocsWorkflow: Effect.Effect<DocsWorkflowService, never, import("effect/Scope").Scope> = Effect.gen(
  function* () {
    const reads = yield* FiberMap.make<string, unknown, unknown>();
    const readKeysByOwner = yield* Ref.make<ReadonlyMap<string, ReadonlySet<string>>>(new Map());
    const publishSequence = yield* Ref.make(1);
    const publishRegistry = yield* Ref.make<DocsPublishRegistry>({ entries: new Map(), surfaces: new Map() });
    const mutations = yield* makeOrderedCommandQueue("Docs mutations", (command: Effect.Effect<void>) => command, 64);

    const read = Effect.fn("DocsWorkflow.read")(function* <A, E, R>(
      owner: string,
      readKey: string,
      request: Effect.Effect<A, E, R>,
    ) {
      const key = JSON.stringify([owner, readKey]);
      yield* Ref.update(readKeysByOwner, (keysByOwner) => {
        const next = new Map(keysByOwner);
        const ownedKeys = new Set(keysByOwner.get(owner));
        ownedKeys.add(key);
        next.set(owner, ownedKeys);
        return next;
      });
      return yield* FiberMap.run(reads, key, request).pipe(Effect.flatMap(Fiber.join));
    });

    const stop = Effect.fn("DocsWorkflow.stop")(function* (owner: string) {
      const ownedKeys = yield* Ref.modify(
        readKeysByOwner,
        (keysByOwner): readonly [ReadonlyArray<string>, ReadonlyMap<string, ReadonlySet<string>>] => {
          const keys = Array.from(keysByOwner.get(owner) ?? []);
          const next = new Map(keysByOwner);
          next.delete(owner);
          return [keys, next];
        },
      );
      yield* Effect.forEach(ownedKeys, (key) => FiberMap.remove(reads, key), { discard: true });
    });

    const mutate = Effect.fn("DocsWorkflow.mutate")(function* <A, E, R>(mutation: Effect.Effect<A, E, R>) {
      const context = yield* Effect.context<R>();
      const result = yield* Deferred.make<Exit.Exit<A, E>>();
      const command = Effect.exit(mutation.pipe(Effect.provide(context))).pipe(
        Effect.flatMap((exit) => Deferred.succeed(result, exit)),
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

    return { read, stop, mutate, claimPublisher, releasePublisher, acknowledgePublisher, publish };
  },
);

export const DocsWorkflowLive = Layer.effect(DocsWorkflow)(makeDocsWorkflow);
