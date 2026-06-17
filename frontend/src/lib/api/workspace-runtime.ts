import type { LaunchTarget, RuntimeSession, WorkspaceRuntime } from "@middleman/ui/api/types";

export type WorkspaceRuntimeState = Omit<WorkspaceRuntime, "launch_targets" | "sessions"> & {
  launch_targets: LaunchTarget[];
  sessions: RuntimeSession[];
};

export type RuntimeFetch = typeof fetch;

function basePath(): string {
  const path = typeof window !== "undefined" ? (window.__BASE_PATH__ ?? "/") : "/";
  return path.replace(/\/$/, "");
}

function apiBaseUrl(): string {
  return `${basePath()}/api/v1`;
}

function wsBaseUrl(): string {
  return `${basePath()}/ws/v1`;
}

function hostPrefix(hostKey?: string): string {
  return hostKey ? `/fleet/hosts/${encodeURIComponent(hostKey)}` : "";
}

function workspaceRuntimeURL(workspaceId: string, hostKey?: string): string {
  return `${apiBaseUrl()}${hostPrefix(hostKey)}/workspaces/${encodeURIComponent(workspaceId)}/runtime`;
}

async function readJSON<T>(response: Response, fallback: string): Promise<T> {
  if (response.ok) {
    return (await response.json()) as T;
  }
  const body = (await response.json().catch(() => ({}))) as {
    detail?: string;
    title?: string;
  };
  throw new Error(body.detail ?? body.title ?? fallback);
}

export async function getWorkspaceRuntime(
  workspaceId: string,
  hostKeyOrFetch?: string | RuntimeFetch,
  fetchFn: RuntimeFetch = fetch,
): Promise<WorkspaceRuntimeState> {
  const hostKey = typeof hostKeyOrFetch === "string" ? hostKeyOrFetch : undefined;
  const runtimeFetch = typeof hostKeyOrFetch === "function" ? hostKeyOrFetch : fetchFn;
  const response = await runtimeFetch(workspaceRuntimeURL(workspaceId, hostKey));
  const runtime = await readJSON<WorkspaceRuntime>(response, `GET workspace runtime failed (${response.status})`);
  return {
    ...runtime,
    launch_targets: runtime.launch_targets ?? [],
    sessions: runtime.sessions ?? [],
  };
}

export async function launchWorkspaceSession(
  workspaceId: string,
  targetKey: string,
  hostKeyOrRegionOrFetch?: string | RuntimeFetch,
  regionOrFetch?: "workflow" | "terminal" | RuntimeFetch,
  fetchFn: RuntimeFetch = fetch,
): Promise<RuntimeSession> {
  const hostKey = typeof hostKeyOrRegionOrFetch === "string" ? hostKeyOrRegionOrFetch : undefined;
  const displayRegion = regionOrFetch === "workflow" || regionOrFetch === "terminal" ? regionOrFetch : undefined;
  const runtimeFetch =
    typeof hostKeyOrRegionOrFetch === "function"
      ? hostKeyOrRegionOrFetch
      : typeof regionOrFetch === "function"
        ? regionOrFetch
        : fetchFn;
  const response = await runtimeFetch(`${workspaceRuntimeURL(workspaceId, hostKey)}/sessions`, {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({
      target_key: targetKey,
      ...(displayRegion ? { display_region: displayRegion } : {}),
    }),
  });
  return readJSON<RuntimeSession>(response, `Launch session failed (${response.status})`);
}

export async function stopWorkspaceSession(
  workspaceId: string,
  sessionKey: string,
  hostKeyOrFetch?: string | RuntimeFetch,
  fetchFn: RuntimeFetch = fetch,
): Promise<void> {
  const hostKey = typeof hostKeyOrFetch === "string" ? hostKeyOrFetch : undefined;
  const runtimeFetch = typeof hostKeyOrFetch === "function" ? hostKeyOrFetch : fetchFn;
  const response = await runtimeFetch(
    `${workspaceRuntimeURL(workspaceId, hostKey)}/sessions/${encodeURIComponent(sessionKey)}`,
    {
      method: "DELETE",
      headers: { "Content-Type": "application/json" },
    },
  );
  if (!response.ok && response.status !== 204) {
    await readJSON<unknown>(response, `Stop session failed (${response.status})`);
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
  const runtimeFetch = typeof hostKeyOrFetch === "function" ? hostKeyOrFetch : fetchFn;
  const response = await runtimeFetch(
    `${workspaceRuntimeURL(workspaceId, hostKey)}/sessions/${encodeURIComponent(sessionKey)}`,
    {
      method: "PATCH",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ label }),
    },
  );
  return readJSON<RuntimeSession>(response, `Rename session failed (${response.status})`);
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
