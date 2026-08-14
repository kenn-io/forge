import { Effect, Queue, Stream } from "effect";
import type { WorkspaceEventsNotification } from "../../stores/events.svelte.js";

export type WorkspaceEventSignal =
  | { readonly _tag: "Open" }
  | { readonly _tag: "Status"; readonly workspaceId?: string | undefined }
  | { readonly _tag: "Associated"; readonly workspaceId?: string | undefined }
  | { readonly _tag: "ReconnectStale" }
  | {
      readonly _tag: "DiffReady" | "DiffChanged";
      readonly workspaceId?: string | undefined;
      readonly version?: string | undefined;
    };

type WorkspaceEventsSubscription = (subscriber: (event: WorkspaceEventsNotification) => void) => () => void;
type WorkspaceSelection = () => () => void;

function workspaceEventSignal(event: WorkspaceEventsNotification): WorkspaceEventSignal | undefined {
  switch (event.type) {
    case "open":
      return { _tag: "Open" };
    case "workspace_status":
      return { _tag: "Status", workspaceId: event.payload.id };
    case "workspace_pr_associated":
      return { _tag: "Associated", workspaceId: event.payload.workspace_id };
    case "reconnect.stale":
      return { _tag: "ReconnectStale" };
    case "workspace_diff_ready":
      return {
        _tag: "DiffReady",
        workspaceId: event.payload.workspace_id,
        version: event.payload.version,
      };
    case "workspace_diff_changed":
      return {
        _tag: "DiffChanged",
        workspaceId: event.payload.workspace_id,
        version: event.payload.version,
      };
    default:
      return undefined;
  }
}

export const workspaceEventStream = (
  subscribe: WorkspaceEventsSubscription,
  selectWorkspace?: WorkspaceSelection,
): Stream.Stream<WorkspaceEventSignal> =>
  Stream.callback<WorkspaceEventSignal>((queue) =>
    Effect.gen(function* () {
      const offer = (signal: WorkspaceEventSignal): void => {
        Queue.offerUnsafe(queue, signal);
      };
      const unsubscribe = yield* Effect.sync(() =>
        subscribe((event) => {
          const signal = workspaceEventSignal(event);
          if (signal !== undefined) offer(signal);
        }),
      );
      yield* Effect.addFinalizer(() => Effect.sync(unsubscribe));
      if (selectWorkspace !== undefined) {
        const releaseSelection = yield* Effect.sync(selectWorkspace);
        yield* Effect.addFinalizer(() => Effect.sync(releaseSelection));
      }
    }),
  );
