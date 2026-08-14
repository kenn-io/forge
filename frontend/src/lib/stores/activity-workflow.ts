import { Context, Effect, Fiber, FiberHandle, Layer, Ref, Schedule, Stream } from "effect";
import type * as Duration from "effect/Duration";
import { TransientTransportError } from "../api/effect-errors.js";

type ProjectActivityResult<A, E, R> = (value: A) => Effect.Effect<void, E, R>;

interface ActivityWorkflowShape {
  readonly load: <A, E, R, ProjectError, ProjectRequirements>(
    scope: string,
    read: Effect.Effect<A, E, R>,
    project: ProjectActivityResult<A, ProjectError, ProjectRequirements>,
    onFailure?: Effect.Effect<void>,
  ) => Effect.Effect<void, E | ProjectError, R | ProjectRequirements>;
  readonly pollRead: <A, E, R, ProjectError, ProjectRequirements>(
    scope: string,
    read: Effect.Effect<A, E, R>,
    project: ProjectActivityResult<A, ProjectError, ProjectRequirements>,
    onFailure?: Effect.Effect<void>,
  ) => Effect.Effect<void, E | ProjectError, R | ProjectRequirements>;
  readonly pollSnapshotRead: <A, E, R, ProjectError, ProjectRequirements>(
    scope: string,
    read: Effect.Effect<A, E, R>,
    project: ProjectActivityResult<A, ProjectError, ProjectRequirements>,
    onFailure?: Effect.Effect<void>,
  ) => Effect.Effect<void, E | ProjectError, R | ProjectRequirements>;
  readonly reconcileRead: <A, E, R, ProjectError, ProjectRequirements>(
    scope: string,
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
    const suppressedForegroundFailure = Symbol("suppressedForegroundFailure");
    const projectionState = yield* Ref.make({
      generation: 0,
      scope: undefined as string | undefined,
      requestStart: 0,
      successfulSnapshotStart: 0,
      reconciliation: 0,
      projectedReconciliation: 0,
    });

    function isCurrent(generation: number): Effect.Effect<boolean> {
      return Ref.get(projectionState).pipe(Effect.map((current) => current.generation === generation));
    }

    function isCurrentClaim(generation: number, scope: string): Effect.Effect<boolean> {
      return Ref.get(projectionState).pipe(
        Effect.map((current) => current.generation === generation && current.scope === scope),
      );
    }

    function claimRead(owner: "foreground" | "poll" | "poll-snapshot" | "reconcile", scope: string) {
      return Ref.modify(projectionState, (current) => {
        const requestStart = current.requestStart + 1;
        if (owner === "foreground") {
          const generation = current.generation + 1;
          return [
            { ...current, generation, scope, requestStart },
            { ...current, generation, scope, requestStart },
          ];
        }
        if (owner === "reconcile") {
          const reconciliation = current.reconciliation + 1;
          return [
            { ...current, scope, requestStart, reconciliation },
            { ...current, scope, requestStart, reconciliation },
          ];
        }
        return [
          { ...current, scope, requestStart },
          { ...current, scope, requestStart },
        ];
      });
    }

    function claimProjection(
      owner: "foreground" | "poll" | "poll-snapshot",
      scope: string,
      requestStart: number,
    ): Effect.Effect<boolean> {
      return Ref.modify(projectionState, (current) => {
        if (current.scope !== scope || current.successfulSnapshotStart >= requestStart) return [false, current];
        if (owner === "poll") return [true, current];
        return [true, { ...current, successfulSnapshotStart: requestStart }];
      });
    }

    function claimReconciliation(scope: string, requestStart: number, reconciliation: number): Effect.Effect<boolean> {
      return Ref.modify(projectionState, (current) => {
        if (
          current.scope !== scope ||
          current.successfulSnapshotStart > requestStart ||
          current.projectedReconciliation >= reconciliation
        ) {
          return [false, current];
        }
        return [
          true,
          {
            ...current,
            generation: current.generation + 1,
            successfulSnapshotStart: requestStart,
            projectedReconciliation: reconciliation,
          },
        ];
      });
    }

    // The overloads preserve both generic channels; the language service cannot
    // currently associate those contracts with the nested generator.
    // @effect-diagnostics effect/missingEffectContext:off
    // @effect-diagnostics effect/missingEffectError:off
    function projectRead<A, E, R, ProjectError, ProjectRequirements>(
      owner: "foreground" | "poll" | "poll-snapshot",
      scope: string,
      read: Effect.Effect<A, E, R>,
      project: ProjectActivityResult<A, ProjectError, ProjectRequirements>,
      onFailure?: Effect.Effect<void>,
    ): Effect.Effect<void, E | ProjectError, R | ProjectRequirements>;
    function projectRead<A, E, R, ProjectError, ProjectRequirements>(
      owner: "reconcile",
      scope: string,
      read: Effect.Effect<A, E, R>,
      project: ProjectActivityResult<A, ProjectError, ProjectRequirements>,
    ): Effect.Effect<void, E | ProjectError | TransientTransportError, R | ProjectRequirements>;
    function projectRead<A, E, R, ProjectError, ProjectRequirements>(
      owner: "foreground" | "poll" | "poll-snapshot" | "reconcile",
      scope: string,
      read: Effect.Effect<A, E, R>,
      project: ProjectActivityResult<A, ProjectError, ProjectRequirements>,
      onFailure?: Effect.Effect<void>,
    ): Effect.Effect<void, E | ProjectError | TransientTransportError, R | ProjectRequirements> {
      return Effect.gen(function* () {
        const claim = yield* claimRead(owner, scope);
        const ownedRead =
          owner === "foreground" ? FiberHandle.run(loadHandle, read).pipe(Effect.flatMap(Fiber.join)) : read;
        const value = yield* ownedRead.pipe(
          Effect.catch((failure) => {
            if (owner !== "foreground") {
              const settle =
                onFailure === undefined
                  ? Effect.void
                  : isCurrent(claim.generation).pipe(Effect.flatMap((current) => (current ? onFailure : Effect.void)));
              return settle.pipe(Effect.andThen(Effect.fail(failure)));
            }
            return isCurrentClaim(claim.generation, scope).pipe(
              Effect.flatMap((current) => {
                if (current) {
                  return (onFailure ?? Effect.void).pipe(Effect.andThen(Effect.fail(failure)));
                }
                return isCurrent(claim.generation).pipe(
                  Effect.flatMap((ownsLoading) => (ownsLoading ? (onFailure ?? Effect.void) : Effect.void)),
                  Effect.as(suppressedForegroundFailure),
                );
              }),
            );
          }),
        );
        if (value === suppressedForegroundFailure) return;
        const current =
          owner === "reconcile"
            ? yield* claimReconciliation(scope, claim.requestStart, claim.reconciliation)
            : yield* claimProjection(owner, scope, claim.requestStart);
        if (current) {
          yield* project(value);
        } else if (owner === "reconcile") {
          return yield* Effect.fail(
            TransientTransportError.make({
              operation: "reconcile activity after superseded provider event",
              cause: new Error("a foreground activity query replaced event reconciliation"),
            }),
          );
        } else if (owner === "foreground" && onFailure !== undefined) {
          const ownsLoading = yield* isCurrent(claim.generation);
          if (ownsLoading) yield* onFailure;
        }
      });
    }
    // @effect-diagnostics effect/missingEffectContext:error
    // @effect-diagnostics effect/missingEffectError:error

    function poll<E, R>(pollOnce: Effect.Effect<void, E, R>, interval: Duration.Input): Effect.Effect<void, E, R> {
      const program = Stream.fromEffectSchedule(pollOnce, Schedule.spaced(interval)).pipe(Stream.runDrain);
      return FiberHandle.run(pollingHandle, program).pipe(Effect.flatMap(Fiber.join));
    }

    function load<A, E, R, ProjectError, ProjectRequirements>(
      scope: string,
      read: Effect.Effect<A, E, R>,
      project: ProjectActivityResult<A, ProjectError, ProjectRequirements>,
      onFailure?: Effect.Effect<void>,
    ): Effect.Effect<void, E | ProjectError, R | ProjectRequirements> {
      return projectRead("foreground", scope, read, project, onFailure);
    }

    function pollRead<A, E, R, ProjectError, ProjectRequirements>(
      scope: string,
      read: Effect.Effect<A, E, R>,
      project: ProjectActivityResult<A, ProjectError, ProjectRequirements>,
      onFailure?: Effect.Effect<void>,
    ): Effect.Effect<void, E | ProjectError, R | ProjectRequirements> {
      return projectRead("poll", scope, read, project, onFailure);
    }

    function pollSnapshotRead<A, E, R, ProjectError, ProjectRequirements>(
      scope: string,
      read: Effect.Effect<A, E, R>,
      project: ProjectActivityResult<A, ProjectError, ProjectRequirements>,
      onFailure?: Effect.Effect<void>,
    ): Effect.Effect<void, E | ProjectError, R | ProjectRequirements> {
      return projectRead("poll-snapshot", scope, read, project, onFailure);
    }

    function reconcileRead<A, E, R, ProjectError, ProjectRequirements>(
      scope: string,
      read: Effect.Effect<A, E, R>,
      project: ProjectActivityResult<A, ProjectError, ProjectRequirements>,
    ) {
      return projectRead("reconcile", scope, read, project);
    }

    return {
      load,
      pollRead,
      pollSnapshotRead,
      reconcileRead,
      poll,
      stopPolling: FiberHandle.clear(pollingHandle),
    };
  }),
);
