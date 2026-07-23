import type { components } from "@middleman/ui/api/schema";

import { getActiveKataDaemon, getDefaultKataDaemon } from "../../stores/active-kata-daemon.svelte.js";
import { apiErrorMessage, createRuntimeClient } from "../runtime.js";
import { KATA_DAEMON_HEADER } from "./daemons.js";

export { KATA_DAEMON_HEADER };

export type KataWorkspaceSnapshotResponse = components["schemas"]["KataTaskSnapshotResponse"];
export type KataTaskReference = components["schemas"]["KataTaskReference"];
export type KataTaskReferenceResponse = components["schemas"]["KataTaskReferenceResponse"];

export type KataAuthorityScope = "global" | "project";
export type KataAuthority = "open" | "ready" | "closed" | "all";

export interface KataSnapshotIntent {
  daemon_id?: string | undefined;
  scope: KataAuthorityScope;
  project_uid?: string | undefined;
  authority: KataAuthority;
  selected_issue_uid?: string | undefined;
  graph_source_uid?: string | undefined;
}

interface KataClientOptions {
  fetchImpl?: typeof fetch | undefined;
  signal?: AbortSignal | undefined;
  getDaemonId?: (() => string | undefined) | undefined;
  getDefaultDaemonId?: (() => string | undefined) | undefined;
}

export interface FetchKataSnapshotOptions extends KataClientOptions {}

export interface SearchKataTaskReferencesOptions extends KataClientOptions {
  daemon_id?: string | undefined;
  limit?: number | undefined;
  status?: "open" | "all" | undefined;
}

export type KataTaskReferenceSearchOptions = Pick<SearchKataTaskReferencesOptions, "daemon_id" | "limit" | "signal">;

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

function daemonHeaders(daemonID: string | undefined): { "X-Middleman-Kata-Daemon"?: string } {
  return daemonID ? { [KATA_DAEMON_HEADER]: daemonID } : {};
}

export async function fetchKataWorkspaceSnapshot(
  intent: KataSnapshotIntent,
  options: FetchKataSnapshotOptions = {},
): Promise<KataWorkspaceSnapshotResponse> {
  const query = {
    scope: intent.scope,
    authority: intent.authority,
    ...(intent.project_uid ? { project_uid: intent.project_uid } : {}),
    ...(intent.selected_issue_uid ? { selected_issue_uid: intent.selected_issue_uid } : {}),
    ...(intent.graph_source_uid ? { graph_source_uid: intent.graph_source_uid } : {}),
  };
  const client = createRuntimeClient(options.fetchImpl);
  const { data, error, response } = await client.GET("/kata/tasks/snapshot", {
    params: {
      header: daemonHeaders(effectiveDaemonID(intent.daemon_id, options)),
      query,
    },
    ...(options.signal ? { signal: options.signal } : {}),
  });
  if (!response.ok || !data) {
    throw new Error(apiErrorMessage(error, `Could not load Kata task snapshot (${response.status})`));
  }
  return data;
}

export async function searchKataTaskReferences(
  query: string,
  options: SearchKataTaskReferencesOptions = {},
): Promise<KataTaskReferenceResponse> {
  const client = createRuntimeClient(options.fetchImpl);
  const { data, error, response } = await client.GET("/kata/tasks/references", {
    params: {
      header: daemonHeaders(effectiveDaemonID(options.daemon_id, options)),
      query: {
        q: query,
        ...(options.limit === undefined ? {} : { limit: options.limit }),
        ...(options.status === undefined ? {} : { status: options.status }),
      },
    },
    ...(options.signal ? { signal: options.signal } : {}),
  });
  if (!response.ok || !data) {
    throw new Error(apiErrorMessage(error, `Could not search Kata task references (${response.status})`));
  }
  return data;
}
