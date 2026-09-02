import { Context, Effect, Layer, Queue, Stream } from "effect";
import type * as Duration from "effect/Duration";
import type { Scope } from "effect/Scope";

import { pollWhileVisible } from "../../effect/poll-while-visible.js";

export interface WorkspaceListWorkflowService {
  readonly claim: (owner: string, refresh: () => void) => Effect.Effect<void, never, Scope>;
  readonly request: () => void;
}

export class WorkspaceListWorkflow extends Context.Service<WorkspaceListWorkflow, WorkspaceListWorkflowService>()(
  "kenn-forge/WorkspaceListWorkflow",
) {}

export const makeWorkspaceListWorkflow = Effect.sync(() => {
  const owners = new Map<string, () => void>();
  let pending = false;

  return {
    claim: (owner: string, refresh: () => void) =>
      Effect.acquireRelease(
        Effect.sync(() => {
          owners.set(owner, refresh);
          if (pending) {
            pending = false;
            refresh();
          }
        }),
        () =>
          Effect.sync(() => {
            owners.delete(owner);
          }),
      ),
    request: () => {
      if (owners.size === 0) {
        pending = true;
        return;
      }
      for (const refresh of owners.values()) refresh();
    },
  } satisfies WorkspaceListWorkflowService;
});

export const WorkspaceListWorkflowLive = Layer.effect(WorkspaceListWorkflow)(makeWorkspaceListWorkflow);

export interface WorkspaceRefreshHub<R> {
  readonly program: Effect.Effect<void, never, R | Scope>;
  readonly request: () => void;
}

export function makeWorkspaceRefreshHub<R>(load: Effect.Effect<void, never, R>): WorkspaceRefreshHub<R> {
  let publish: () => void = () => {
    pending = true;
  };
  let pending = false;

  const program = Effect.gen(function* () {
    const requests = yield* Queue.sliding<void>(1);
    publish = () => {
      Queue.offerUnsafe(requests, undefined);
    };
    const runImmediately = pending;
    pending = false;
    yield* Effect.addFinalizer(() =>
      Effect.sync(() => {
        publish = () => {
          pending = true;
        };
      }).pipe(Effect.andThen(Queue.shutdown(requests))),
    );
    if (runImmediately) yield* load;
    yield* Stream.fromQueue(requests).pipe(
      Stream.debounce("25 millis"),
      Stream.runForEach(() => load),
    );
  });

  return {
    program,
    request: () => publish(),
  };
}

export interface WorkspaceListLifecycleOptions<WorkspaceR, FleetR, EventR> {
  readonly refreshWorkspaces: WorkspaceRefreshHub<WorkspaceR>;
  readonly refreshFleet: WorkspaceRefreshHub<FleetR>;
  readonly workspaceEvents: Stream.Stream<unknown, never, EventR>;
}

export function workspaceListLifecycle<WorkspaceR, FleetR, EventR>({
  refreshWorkspaces,
  refreshFleet,
  workspaceEvents,
}: WorkspaceListLifecycleOptions<WorkspaceR, FleetR, EventR>): Effect.Effect<
  void,
  never,
  WorkspaceR | FleetR | EventR | Scope
> {
  // Timer polls pause while the document is hidden and refresh at once when
  // it is shown again; workspaceEvents keeps refreshing regardless.
  const poll = (request: () => void, interval: Duration.Input) => pollWhileVisible(Effect.sync(request), interval);
  const sharedRefresh = refreshWorkspaces === (refreshFleet as unknown as WorkspaceRefreshHub<WorkspaceR>);

  return Effect.sync(() => {
    refreshWorkspaces.request();
    if (!sharedRefresh) refreshFleet.request();
  }).pipe(
    Effect.andThen(
      Effect.all(
        [
          refreshWorkspaces.program,
          poll(refreshWorkspaces.request, "5 seconds"),
          Stream.runForEach(workspaceEvents, () =>
            Effect.sync(refreshWorkspaces.request).pipe(Effect.andThen(Effect.yieldNow)),
          ),
          ...(sharedRefresh ? [] : [refreshFleet.program, poll(refreshFleet.request, "15 seconds")]),
        ],
        { concurrency: "unbounded", discard: true },
      ),
    ),
  );
}
