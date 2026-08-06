import { Cause, Effect, Option, Queue, Schema, Stream } from "effect";
import { TransientTransportError } from "../../api/effect-errors.js";
import { openEventSource, type EventSourceFactory } from "../../browser/event-source.js";

class WorkspaceStatusPayload extends Schema.Class<WorkspaceStatusPayload>("WorkspaceStatusPayload")({
  id: Schema.optionalKey(Schema.String),
}) {}

class WorkspaceAssociatedPayload extends Schema.Class<WorkspaceAssociatedPayload>("WorkspaceAssociatedPayload")({
  workspace_id: Schema.optionalKey(Schema.String),
}) {}

class WorkspaceDiffPayload extends Schema.Class<WorkspaceDiffPayload>("WorkspaceDiffPayload")({
  workspace_id: Schema.optionalKey(Schema.String),
  version: Schema.optionalKey(Schema.String),
}) {}

export type WorkspaceEventSignal =
  | { readonly _tag: "Open" }
  | { readonly _tag: "Status"; readonly workspaceId?: string | undefined }
  | { readonly _tag: "Associated"; readonly workspaceId?: string | undefined }
  | { readonly _tag: "ReconnectStale" }
  | {
      readonly _tag: "DiffChanged";
      readonly workspaceId?: string | undefined;
      readonly version?: string | undefined;
    };

export const workspaceEventStream = (
  url: string,
): Stream.Stream<WorkspaceEventSignal, TransientTransportError, EventSourceFactory> =>
  Stream.callback<WorkspaceEventSignal, TransientTransportError, EventSourceFactory>(
    (queue) =>
      Effect.gen(function* () {
        const source = yield* openEventSource(url);
        const offer = (signal: WorkspaceEventSignal): void => {
          if (Queue.offerUnsafe(queue, signal)) return;
          Queue.failCauseUnsafe(
            queue,
            Cause.fail(
              TransientTransportError.make({
                operation: `buffer workspace events ${url}`,
                cause: new Error("Workspace event buffer overflow"),
              }),
            ),
          );
        };
        const onOpen = (): void => offer({ _tag: "Open" });
        const onStatus = (event: Event): void => {
          if (!(event instanceof MessageEvent) || typeof event.data !== "string") return;
          const decoded = Schema.decodeUnknownOption(Schema.fromJsonString(WorkspaceStatusPayload))(event.data);
          if (Option.isSome(decoded)) offer({ _tag: "Status", workspaceId: decoded.value.id });
        };
        const onAssociated = (event: Event): void => {
          if (!(event instanceof MessageEvent) || typeof event.data !== "string") return;
          const decoded = Schema.decodeUnknownOption(Schema.fromJsonString(WorkspaceAssociatedPayload))(event.data);
          if (Option.isSome(decoded)) offer({ _tag: "Associated", workspaceId: decoded.value.workspace_id });
        };
        const onReconnectStale = (): void => offer({ _tag: "ReconnectStale" });
        const onDiffChanged = (event: Event): void => {
          if (!(event instanceof MessageEvent) || typeof event.data !== "string") return;
          const decoded = Schema.decodeUnknownOption(Schema.fromJsonString(WorkspaceDiffPayload))(event.data);
          if (Option.isSome(decoded)) {
            offer({
              _tag: "DiffChanged",
              workspaceId: decoded.value.workspace_id,
              version: decoded.value.version,
            });
          }
        };

        source.addEventListener("open", onOpen);
        source.addEventListener("workspace_status", onStatus);
        source.addEventListener("workspace_pr_associated", onAssociated);
        source.addEventListener("reconnect.stale", onReconnectStale);
        source.addEventListener("workspace_diff_ready", onDiffChanged);
        source.addEventListener("workspace_diff_changed", onDiffChanged);
        yield* Effect.addFinalizer(() =>
          Effect.sync(() => {
            source.removeEventListener("open", onOpen);
            source.removeEventListener("workspace_status", onStatus);
            source.removeEventListener("workspace_pr_associated", onAssociated);
            source.removeEventListener("reconnect.stale", onReconnectStale);
            source.removeEventListener("workspace_diff_ready", onDiffChanged);
            source.removeEventListener("workspace_diff_changed", onDiffChanged);
          }),
        );
      }),
    { bufferSize: 64, strategy: "dropping" },
  );
