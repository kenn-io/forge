import { Context, Effect, Fiber, FiberHandle, Layer, Ref, Schedule, Stream } from "effect";
import type * as Duration from "effect/Duration";
import { TransientTransportError } from "../api/effect-errors.js";

type ProjectActivityResult<A, E, R> = (value: A) => Effect.Effect<void, E, R>;

interface ActivityWorkflowShape {
  readonly load: <A, E, R, ProjectError, ProjectRequirements>(
    read: Effect.Effect<A, E, R>,
    project: ProjectActivityResult<A, ProjectError, ProjectRequirements>,
    onFailure?: Effect.Effect<void>,
  ) => Effect.Effect<void, E | ProjectError, R | ProjectRequirements>;
  readonly pollRead: <A, E, R, ProjectError, ProjectRequirements>(
    read: Effect.Effect<A, E, R>,
    project: ProjectActivityResult<A, ProjectError, ProjectRequirements>,
    onFailure?: Effect.Effect<void>,
  ) => Effect.Effect<void, E | ProjectError, R | ProjectRequirements>;
  readonly reconcileRead: <A, E, R, ProjectError, ProjectRequirements>(
    read: Effect.Effect<A, E, R>,
    project: ProjectActivityResult<A, ProjectError, ProjectRequirements>,
  ) => Effect.Effect<void, E | ProjectError | TransientTransportError, R | ProjectRequirements>;
  readonly poll: <E, R>(pollOnce: Effect.Effect<void, E, R>, interval: Duration.Input) => Effect.Effect<void, E, R>;
  readonly stopPolling: Effect.Effect<void>;
}

export class ActivityWorkflow extends Context.Service<ActivityWorkflow, ActivityWorkflowShape>()(
  "kenn-forge/ActivityWorkflow",
) {}

export const ActivityWorkflowLive = Layer.effect(ActivityWorkflow)(
  Effect.gen(function* () {
    const loadHandle = yield* FiberHandle.make<unknown, unknown>();
    const pollingHandle = yield* FiberHandle.make<void, unknown>();
    const projectionGeneration = yield* Ref.make(0);

    function isCurrent(generation: number): Effect.Effect<boolean> {
      return Ref.get(projectionGeneration).pipe(Effect.map((current) => current === generation));
    }

    function projectRead<A, E, R, ProjectError, ProjectRequirements>(
      owner: "foreground" | "poll",
      read: Effect.Effect<A, E, R>,
      project: ProjectActivityResult<A, ProjectError, ProjectRequirements>,
      onFailure?: Effect.Effect<void>,
    ): Effect.Effect<void, E | ProjectError, R | ProjectRequirements>;
    function projectRead<A, E, R, ProjectError, ProjectRequirements>(
      owner: "reconcile",
      read: Effect.Effect<A, E, R>,
      project: ProjectActivityResult<A, ProjectError, ProjectRequirements>,
    ): Effect.Effect<void, E | ProjectError | TransientTransportError, R | ProjectRequirements>;
    function projectRead<A, E, R, ProjectError, ProjectRequirements>(
      owner: "foreground" | "poll" | "reconcile",
      read: Effect.Effect<A, E, R>,
      project: ProjectActivityResult<A, ProjectError, ProjectRequirements>,
      onFailure?: Effect.Effect<void>,
    ): Effect.Effect<void, E | ProjectError | TransientTransportError, R | ProjectRequirements> {
      return Effect.gen(function* () {
        const generation = yield* owner === "foreground"
          ? Ref.updateAndGet(projectionGeneration, (current) => current + 1)
          : Ref.get(projectionGeneration);
        const ownedRead =
          owner === "foreground" ? FiberHandle.run(loadHandle, read).pipe(Effect.flatMap(Fiber.join)) : read;
        const value = yield* ownedRead.pipe(
          Effect.tapError(() =>
            onFailure === undefined
              ? Effect.void
              : isCurrent(generation).pipe(Effect.flatMap((current) => (current ? onFailure : Effect.void))),
          ),
        );
        if (yield* isCurrent(generation)) {
          yield* project(value);
        } else if (owner === "reconcile") {
          return yield* Effect.fail(
            TransientTransportError.make({
              operation: "reconcile activity after superseded provider event",
              cause: new Error("a foreground activity query replaced event reconciliation"),
            }),
          );
        }
      });
    }

    function poll<E, R>(pollOnce: Effect.Effect<void, E, R>, interval: Duration.Input): Effect.Effect<void, E, R> {
      const program = Stream.fromEffectSchedule(pollOnce, Schedule.spaced(interval)).pipe(Stream.runDrain);
      return FiberHandle.run(pollingHandle, program).pipe(Effect.flatMap(Fiber.join));
    }

    function load<A, E, R, ProjectError, ProjectRequirements>(
      read: Effect.Effect<A, E, R>,
      project: ProjectActivityResult<A, ProjectError, ProjectRequirements>,
      onFailure?: Effect.Effect<void>,
    ): Effect.Effect<void, E | ProjectError, R | ProjectRequirements> {
      return projectRead("foreground", read, project, onFailure);
    }

    function pollRead<A, E, R, ProjectError, ProjectRequirements>(
      read: Effect.Effect<A, E, R>,
      project: ProjectActivityResult<A, ProjectError, ProjectRequirements>,
      onFailure?: Effect.Effect<void>,
    ): Effect.Effect<void, E | ProjectError, R | ProjectRequirements> {
      return projectRead("poll", read, project, onFailure);
    }

    function reconcileRead<A, E, R, ProjectError, ProjectRequirements>(
      read: Effect.Effect<A, E, R>,
      project: ProjectActivityResult<A, ProjectError, ProjectRequirements>,
    ) {
      return projectRead("reconcile", read, project);
    }

    return {
      load,
      pollRead,
      reconcileRead,
      poll,
      stopPolling: FiberHandle.clear(pollingHandle),
    };
  }),
);
