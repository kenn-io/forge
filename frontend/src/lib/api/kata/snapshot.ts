import { Effect, Schema } from "effect";
import type { components } from "../generated/schema.js";

import { getActiveKataDaemon, getDefaultKataDaemon } from "../../stores/active-kata-daemon.svelte.js";
import { TransientTransportError } from "../effect-errors.js";
import { GeneratedApi } from "../generated-api.js";
import { apiErrorMessage } from "../runtime.js";
import { KATA_DAEMON_HEADER } from "./daemons.js";

export { KATA_DAEMON_HEADER };

export type KataWorkspaceSnapshotResponse = components["schemas"]["KataTaskSnapshotResponse"];
export type KataTaskReference = components["schemas"]["KataTaskReference"];
export type KataTaskReferenceResponse = components["schemas"]["KataTaskReferenceResponse"];

export type KataAuthorityScope = "global" | "project";
export type KataAuthority = "open" | "ready" | "closed" | "all";

export class KataSnapshotAPIError extends Schema.TaggedErrorClass<KataSnapshotAPIError>()("KataSnapshotAPIError", {
  status: Schema.Number,
  code: Schema.optionalKey(Schema.String),
  message: Schema.String,
}) {}

export interface KataSnapshotIntent {
  daemon_id?: string | undefined;
  scope: KataAuthorityScope;
  project_uid?: string | undefined;
  authority: KataAuthority;
  selected_issue_uid?: string | undefined;
  graph_source_uid?: string | undefined;
}

interface KataClientOptions {
  getDaemonId?: (() => string | undefined) | undefined;
  getDefaultDaemonId?: (() => string | undefined) | undefined;
}

export interface FetchKataSnapshotOptions extends Pick<KataClientOptions, "getDaemonId" | "getDefaultDaemonId"> {
  fresh?: boolean | undefined;
}

export interface SearchKataTaskReferencesOptions extends KataClientOptions {
  daemon_id?: string | undefined;
  limit?: number | undefined;
  status?: "open" | "all" | undefined;
}

export interface KataTaskReferenceSearchOptions {
  daemon_id?: string | undefined;
  limit?: number | undefined;
  status?: "open" | "all" | undefined;
  signal?: AbortSignal | undefined;
}

export type KataTaskReferenceSearch = (
  query: string,
  options?: KataTaskReferenceSearchOptions,
) => Promise<KataTaskReferenceResponse>;

function effectiveDaemonID(requested: string | undefined, options: KataClientOptions): string | undefined {
  const explicit = requested?.trim();
  if (explicit) return explicit;
  const active = (options.getDaemonId ?? getActiveKataDaemon)()?.trim();
  if (active) return active;
  return (options.getDefaultDaemonId ?? getDefaultKataDaemon)()?.trim() || undefined;
}

function daemonHeaders(daemonID: string | undefined): { "X-Kenn-Forge-Kata-Daemon"?: string } {
  return daemonID ? { [KATA_DAEMON_HEADER]: daemonID } : {};
}

export const fetchKataWorkspaceSnapshot = Effect.fn("KataSnapshot.fetch")(function* (
  intent: KataSnapshotIntent,
  options: FetchKataSnapshotOptions = {},
) {
  const query = {
    scope: intent.scope,
    authority: intent.authority,
    ...(intent.project_uid ? { project_uid: intent.project_uid } : {}),
    ...(intent.selected_issue_uid ? { selected_issue_uid: intent.selected_issue_uid } : {}),
    ...(intent.graph_source_uid ? { graph_source_uid: intent.graph_source_uid } : {}),
    ...(options.fresh === true ? { fresh: true } : {}),
  };
  const { client } = yield* GeneratedApi;
  const { data, error, response } = yield* Effect.tryPromise({
    try: (signal) =>
      client.GET("/kata/tasks/snapshot", {
        params: {
          header: daemonHeaders(effectiveDaemonID(intent.daemon_id, options)),
          query,
        },
        signal,
      }),
    catch: (cause) => TransientTransportError.make({ operation: "load Kata task snapshot", cause }),
  });
  if (!response.ok || !data) {
    return yield* Effect.fail(
      KataSnapshotAPIError.make({
        status: response.status,
        ...(typeof error?.code === "string" ? { code: error.code } : {}),
        message: apiErrorMessage(error, `Could not load Kata task snapshot (${response.status})`),
      }),
    );
  }
  return data;
});

export const searchKataTaskReferences = Effect.fn("KataSnapshot.searchReferences")(function* (
  query: string,
  options: SearchKataTaskReferencesOptions = {},
) {
  const { client } = yield* GeneratedApi;
  const { data, error, response } = yield* Effect.tryPromise({
    try: (signal) =>
      client.GET("/kata/tasks/references", {
        params: {
          header: daemonHeaders(effectiveDaemonID(options.daemon_id, options)),
          query: {
            q: query,
            ...(options.limit === undefined ? {} : { limit: options.limit }),
            ...(options.status === undefined ? {} : { status: options.status }),
          },
        },
        signal,
      }),
    catch: (cause) => TransientTransportError.make({ operation: "search Kata task references", cause }),
  });
  if (!response.ok || !data) {
    return yield* Effect.fail(
      KataSnapshotAPIError.make({
        status: response.status,
        ...(typeof error?.code === "string" ? { code: error.code } : {}),
        message: apiErrorMessage(error, `Could not search Kata task references (${response.status})`),
      }),
    );
  }
  return data;
});
