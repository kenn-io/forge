import type { LaunchTarget, RuntimeSession, WorkspaceRuntime } from "./types.js";
import { configuredAPIBaseURL } from "./runtime-base.js";

import { createRuntimeClient } from "./runtime.js";

export type WorkspaceRuntimeState = Omit<WorkspaceRuntime, "launch_targets" | "sessions"> & {
  launch_targets: LaunchTarget[];
  sessions: RuntimeSession[];
};

export type RuntimeFetch = typeof fetch;

function basePath(): string {
  const path = typeof window !== "undefined" ? (window.__BASE_PATH__ ?? "/") : "/";
  return path.replace(/\/$/, "");
}

function wsBaseUrl(): string {
  return `${basePath()}/ws/v1`;
}

function hostPrefix(hostKey?: string): string {
  return hostKey ? `/fleet/hosts/${encodeURIComponent(hostKey)}` : "";
}

function problemMessage(error: unknown): string | undefined {
  if (typeof error !== "object" || error === null) return undefined;
  if ("detail" in error && typeof error.detail === "string") return error.detail;
  if ("title" in error && typeof error.title === "string") return error.title;
  return undefined;
}

function runtimeResult<T>(data: unknown, error: unknown, response: Response, fallback: string): T {
  if (response.ok && data !== undefined) {
    return data as T;
  }
  throw new Error(problemMessage(error) ?? fallback);
}

function runtimeFetch(hostKeyOrFetch: string | RuntimeFetch | undefined, fallback: RuntimeFetch): RuntimeFetch {
  return typeof hostKeyOrFetch === "function" ? hostKeyOrFetch : fallback;
}

function workspaceRuntimeClient(fetchImpl: RuntimeFetch) {
  return createRuntimeClient(fetchImpl, configuredAPIBaseURL());
}

export async function getWorkspaceRuntime(
  workspaceId: string,
  hostKeyOrFetch?: string | RuntimeFetch,
  fetchFn: RuntimeFetch = fetch,
): Promise<WorkspaceRuntimeState> {
  const hostKey = typeof hostKeyOrFetch === "string" ? hostKeyOrFetch : undefined;
  const client = workspaceRuntimeClient(runtimeFetch(hostKeyOrFetch, fetchFn));
  const result = hostKey
    ? await client.GET("/fleet/hosts/{host_key}/workspaces/{id}/runtime", {
        params: { path: { host_key: hostKey, id: workspaceId } },
      })
    : await client.GET("/workspaces/{id}/runtime", {
        params: { path: { id: workspaceId } },
      });
  const runtime = runtimeResult<WorkspaceRuntime>(
    result.data,
    result.error,
    result.response,
    `GET workspace runtime failed (${result.response.status})`,
  );
  return {
    ...runtime,
    launch_targets: runtime.launch_targets ?? [],
    sessions: runtime.sessions ?? [],
  };
}

export interface LaunchWorkspaceSessionOptions {
  hostKey?: string | undefined;
  region?: "workflow" | "terminal";
  fetch?: RuntimeFetch;
}

export async function launchWorkspaceSession(
  workspaceId: string,
  targetKey: string,
  options: LaunchWorkspaceSessionOptions = {},
): Promise<RuntimeSession> {
  const { hostKey, region, fetch: fetchImpl = fetch } = options;
  const client = workspaceRuntimeClient(fetchImpl);
  const body = {
    target_key: targetKey,
    ...(region ? { display_region: region } : {}),
  };
  const result = hostKey
    ? await client.POST("/fleet/hosts/{host_key}/workspaces/{id}/runtime/sessions", {
        params: { path: { host_key: hostKey, id: workspaceId } },
        body,
      })
    : await client.POST("/workspaces/{id}/runtime/sessions", {
        params: { path: { id: workspaceId } },
        body,
      });
  return runtimeResult<RuntimeSession>(
    result.data,
    result.error,
    result.response,
    `Launch session failed (${result.response.status})`,
  );
}

export async function stopWorkspaceSession(
  workspaceId: string,
  sessionKey: string,
  hostKeyOrFetch?: string | RuntimeFetch,
  fetchFn: RuntimeFetch = fetch,
): Promise<void> {
  const hostKey = typeof hostKeyOrFetch === "string" ? hostKeyOrFetch : undefined;
  const client = workspaceRuntimeClient(runtimeFetch(hostKeyOrFetch, fetchFn));
  const result = hostKey
    ? await client.DELETE("/fleet/hosts/{host_key}/workspaces/{id}/runtime/sessions/{session_key}", {
        params: { path: { host_key: hostKey, id: workspaceId, session_key: sessionKey } },
        headers: { "Content-Type": "application/json" },
      })
    : await client.DELETE("/workspaces/{id}/runtime/sessions/{session_key}", {
        params: { path: { id: workspaceId, session_key: sessionKey } },
        headers: { "Content-Type": "application/json" },
      });
  if (!result.response.ok && result.response.status !== 204) {
    throw new Error(problemMessage(result.error) ?? `Stop session failed (${result.response.status})`);
  }
}

export async function renameWorkspaceSession(
  workspaceId: string,
  sessionKey: string,
  label: string,
  hostKeyOrFetch?: string | RuntimeFetch,
  fetchFn: RuntimeFetch = fetch,
): Promise<RuntimeSession> {
  const hostKey = typeof hostKeyOrFetch === "string" ? hostKeyOrFetch : undefined;
  const client = workspaceRuntimeClient(runtimeFetch(hostKeyOrFetch, fetchFn));
  const result = hostKey
    ? await client.PATCH("/fleet/hosts/{host_key}/workspaces/{id}/runtime/sessions/{session_key}", {
        params: { path: { host_key: hostKey, id: workspaceId, session_key: sessionKey } },
        body: { label },
      })
    : await client.PATCH("/workspaces/{id}/runtime/sessions/{session_key}", {
        params: { path: { id: workspaceId, session_key: sessionKey } },
        body: { label },
      });
  return runtimeResult<RuntimeSession>(
    result.data,
    result.error,
    result.response,
    `Rename session failed (${result.response.status})`,
  );
}

export function workspaceSessionWebSocketPath(workspaceId: string, sessionKey: string, hostKey?: string): string {
  return (
    `${wsBaseUrl()}${hostPrefix(hostKey)}/workspaces/${encodeURIComponent(workspaceId)}` +
    `/runtime/sessions/${encodeURIComponent(sessionKey)}` +
    "/terminal"
  );
}

export function workspaceTmuxWebSocketPath(workspaceId: string, hostKey?: string): string {
  return `${wsBaseUrl()}${hostPrefix(hostKey)}/workspaces/${encodeURIComponent(workspaceId)}` + "/terminal";
}
