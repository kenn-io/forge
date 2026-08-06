import { Context, Effect, Fiber, FiberMap, Layer } from "effect";
import { GeneratedApi } from "../api/generated-api.js";

export type RepoBrowserOwner = string;

export interface RepoBrowserWorkflowService {
  readonly repo: <A, E>(owner: RepoBrowserOwner, request: Effect.Effect<A, E, GeneratedApi>) => Effect.Effect<A, E>;
  readonly tree: <A, E>(owner: RepoBrowserOwner, request: Effect.Effect<A, E, GeneratedApi>) => Effect.Effect<A, E>;
  readonly initialPath: <E>(
    owner: RepoBrowserOwner,
    request: Effect.Effect<void, E, GeneratedApi>,
  ) => Effect.Effect<void>;
  readonly path: <A, E>(owner: RepoBrowserOwner, request: Effect.Effect<A, E, GeneratedApi>) => Effect.Effect<A, E>;
  readonly commit: <A, E>(owner: RepoBrowserOwner, request: Effect.Effect<A, E, GeneratedApi>) => Effect.Effect<A, E>;
  readonly startMetadata: <E>(
    owner: RepoBrowserOwner,
    request: Effect.Effect<void, E, GeneratedApi>,
  ) => Effect.Effect<void>;
  readonly stop: (owner: RepoBrowserOwner) => Effect.Effect<void>;
}

export class RepoBrowserWorkflow extends Context.Service<RepoBrowserWorkflow, RepoBrowserWorkflowService>()(
  "kenn-forge/RepoBrowserWorkflow",
) {}

export const RepoBrowserWorkflowLive = Layer.effect(RepoBrowserWorkflow)(
  Effect.gen(function* () {
    const api = yield* GeneratedApi;
    const repoFibers = yield* FiberMap.make<RepoBrowserOwner, unknown, unknown>();
    const treeFibers = yield* FiberMap.make<RepoBrowserOwner, unknown, unknown>();
    const pathFibers = yield* FiberMap.make<RepoBrowserOwner, unknown, unknown>();
    const commitFibers = yield* FiberMap.make<RepoBrowserOwner, unknown, unknown>();
    const metadataFibers = yield* FiberMap.make<RepoBrowserOwner, void, unknown>();

    const run = <A, E>(
      fibers: FiberMap.FiberMap<RepoBrowserOwner, unknown, unknown>,
      owner: RepoBrowserOwner,
      request: Effect.Effect<A, E, GeneratedApi>,
    ): Effect.Effect<A, E> =>
      FiberMap.run(fibers, owner, request.pipe(Effect.provideService(GeneratedApi, api))).pipe(
        Effect.flatMap(Fiber.join),
      );

    const clearTreeWork = (owner: RepoBrowserOwner) =>
      Effect.all(
        [
          FiberMap.remove(treeFibers, owner),
          FiberMap.remove(pathFibers, owner),
          FiberMap.remove(commitFibers, owner),
          FiberMap.remove(metadataFibers, owner),
        ],
        { discard: true },
      );
    const clearPathWork = (owner: RepoBrowserOwner) =>
      Effect.all([FiberMap.remove(pathFibers, owner), FiberMap.remove(commitFibers, owner)], {
        discard: true,
      });

    return {
      repo: (owner, request) => clearTreeWork(owner).pipe(Effect.andThen(run(repoFibers, owner, request))),
      tree: (owner, request) =>
        clearPathWork(owner).pipe(
          Effect.andThen(FiberMap.remove(metadataFibers, owner)),
          Effect.andThen(run(treeFibers, owner, request)),
        ),
      initialPath: (owner, request) =>
        FiberMap.run(pathFibers, owner, request.pipe(Effect.provideService(GeneratedApi, api))).pipe(
          Effect.flatMap(Fiber.await),
          Effect.asVoid,
        ),
      path: (owner, request) =>
        FiberMap.remove(commitFibers, owner).pipe(Effect.andThen(run(pathFibers, owner, request))),
      commit: (owner, request) => run(commitFibers, owner, request),
      startMetadata: (owner, request) =>
        FiberMap.run(metadataFibers, owner, request.pipe(Effect.provideService(GeneratedApi, api), Effect.ignore)).pipe(
          Effect.asVoid,
        ),
      stop: (owner) =>
        Effect.all(
          [
            FiberMap.remove(repoFibers, owner),
            FiberMap.remove(treeFibers, owner),
            FiberMap.remove(pathFibers, owner),
            FiberMap.remove(commitFibers, owner),
            FiberMap.remove(metadataFibers, owner),
          ],
          { discard: true },
        ),
    };
  }),
);
